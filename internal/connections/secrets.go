package connections

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"sort"
	"strings"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

func SecretContext(orgID, connectionID string) string {
	return "connection:" + orgID + ":" + connectionID
}

func openConfig(record store.Connection, box *secrets.Box, hostCommands bool) (Config, error) {
	values := make(map[string]string)
	if len(record.SecretsEnc) > 0 {
		if box == nil {
			return Config{}, errors.New("connection secrets unavailable")
		}
		plain, err := box.Open(record.SecretsEnc, SecretContext(record.OrgID, record.ID))
		if err != nil {
			return Config{}, errors.New("connection secrets unavailable")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(plain, &object); err != nil || object == nil {
			return Config{}, errors.New("invalid connection secrets")
		}
		fields := make([]typedfields.Field, 0, len(object))
		for key := range object {
			fields = append(fields, typedfields.Field{Key: key, Kind: "secret"})
		}
		if err := typedfields.Validate(fields, plain); err != nil {
			return Config{}, errors.New("invalid connection secrets")
		}
		for key, raw := range object {
			var value string
			if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
				return Config{}, errors.New("invalid connection secrets")
			}
			values[key] = value
		}
	}
	record.Config = append(json.RawMessage(nil), record.Config...)
	record.SecretsEnc = nil
	return Config{Record: record, Secrets: values, HostCommands: hostCommands}, nil
}

func cloneConfig(cfg Config) Config {
	cfg.Record.Config = append(json.RawMessage(nil), cfg.Record.Config...)
	cfg.Record.SecretsEnc = append([]byte(nil), cfg.Record.SecretsEnc...)
	values := make(map[string]string, len(cfg.Secrets))
	for key, value := range cfg.Secrets {
		values[key] = value
	}
	cfg.Secrets = values
	return cfg
}

type redactor struct {
	values []string
	keys   map[string]bool
}

func newRedactor(cfg Config, sealed []byte, fields []typedfields.Field) *redactor {
	r := &redactor{keys: make(map[string]bool)}
	for key, value := range cfg.Secrets {
		r.keys[strings.ToLower(key)] = true
		r.add(value)
	}
	if len(sealed) > 0 {
		r.add(base64.StdEncoding.EncodeToString(sealed))
		r.add(hex.EncodeToString(sealed))
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal(cfg.Record.Config, &object)
	for _, field := range fields {
		if field.Kind != "secret" && !field.Sensitive {
			continue
		}
		r.keys[strings.ToLower(field.Key)] = true
		var value any
		raw := object[field.Key]
		if len(raw) == 0 {
			raw = field.Default
		}
		if json.Unmarshal(raw, &value) == nil {
			r.collect(value)
		}
	}
	sort.Slice(r.values, func(i, j int) bool { return len(r.values[i]) > len(r.values[j]) })
	return r
}

func (r *redactor) collect(value any) {
	switch value := value.(type) {
	case string:
		r.add(value)
	case []any:
		for _, item := range value {
			r.collect(item)
		}
	case map[string]any:
		for _, item := range value {
			r.collect(item)
		}
	}
}

func (r *redactor) add(value string) {
	if value == "" {
		return
	}
	r.values = append(r.values, value, url.QueryEscape(value), base64.StdEncoding.EncodeToString([]byte(value)))
}

func (r *redactor) text(value string) string {
	for _, secret := range r.values {
		value = strings.ReplaceAll(value, secret, "<redacted>")
	}
	return value
}

func (r *redactor) sensitive(key string) bool {
	key = strings.ToLower(key)
	return r.keys[key] || strings.Contains(key, "secret") || strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "credential") || key == "authorization" || key == "kubeconfig" || key == "private_key"
}

func (r *redactor) value(value any) any {
	switch value := value.(type) {
	case string:
		return r.text(value)
	case []any:
		for i := range value {
			value[i] = r.value(value[i])
		}
		return value
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			if r.sensitive(key) {
				result[r.text(key)] = "<redacted>"
			} else {
				result[r.text(key)] = r.value(item)
			}
		}
		return result
	default:
		return value
	}
}

func (r *redactor) status(status Status) Status {
	status.Level = r.text(status.Level)
	status.Health = r.text(status.Health)
	if status.LastError != "" {
		status.LastError = "connection provider reported an error"
	}
	status.Hostnames = append([]HostStatus(nil), status.Hostnames...)
	for i := range status.Hostnames {
		host := &status.Hostnames[i]
		host.Host = r.text(host.Host)
		host.State = r.text(host.State)
		host.EndpointID = r.text(host.EndpointID)
		host.Detail = r.text(host.Detail)
		host.Fix = r.text(host.Fix)
	}
	if status.Details != nil {
		raw, err := json.Marshal(status.Details)
		var details map[string]any
		if err == nil {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			err = decoder.Decode(&details)
		}
		if err != nil {
			status.Details = nil
			status.LastError = "invalid connection provider status"
		} else {
			status.Details = r.value(details).(map[string]any)
		}
	}
	return status
}

type redactingHandler struct {
	base     slog.Handler
	redactor *redactor
}

func (h redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

func (h redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, h.redactor.text(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool { safe.AddAttrs(h.attr(attr)); return true })
	return h.base.Handle(ctx, safe)
}

func (h redactingHandler) attr(attr slog.Attr) slog.Attr {
	key := h.redactor.text(attr.Key)
	if h.redactor.sensitive(attr.Key) {
		return slog.String(key, "<redacted>")
	}
	value := attr.Value.Resolve()
	if value.Kind() == slog.KindGroup {
		attrs := value.Group()
		cloned := make([]slog.Attr, len(attrs))
		for i := range attrs {
			cloned[i] = h.attr(attrs[i])
		}
		return slog.Attr{Key: key, Value: slog.GroupValue(cloned...)}
	}
	if value.Kind() == slog.KindString {
		return slog.String(key, h.redactor.text(value.String()))
	}
	if value.Kind() == slog.KindAny {
		if _, ok := value.Any().(error); ok {
			return slog.String(key, "connection provider error")
		}
		raw, err := json.Marshal(value.Any())
		if err != nil {
			return slog.String(key, "<redacted>")
		}
		var decoded any
		if json.Unmarshal(raw, &decoded) != nil {
			return slog.String(key, "<redacted>")
		}
		raw, err = json.Marshal(h.redactor.value(decoded))
		if err != nil {
			return slog.String(key, "<redacted>")
		}
		return slog.String(key, string(raw))
	}
	return slog.Attr{Key: key, Value: value}
}

func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i := range attrs {
		safe[i] = h.attr(attrs[i])
	}
	return redactingHandler{base: h.base.WithAttrs(safe), redactor: h.redactor}
}

func (h redactingHandler) WithGroup(name string) slog.Handler {
	return redactingHandler{base: h.base.WithGroup(h.redactor.text(name)), redactor: h.redactor}
}
