package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

type Readiness struct {
	TCP  string         `json:"tcp,omitempty"`
	HTTP *HTTPReadiness `json:"http,omitempty"`
}

type HTTPReadiness struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

func DecodeReadiness(raw json.RawMessage) (Readiness, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Readiness{}, nil
	}
	object, err := decodeReadinessObject(raw)
	if err != nil {
		return Readiness{}, err
	}
	if _, tcpPresent := object["tcp"]; tcpPresent {
		if _, httpPresent := object["http"]; httpPresent {
			return Readiness{}, errors.New("readiness must contain only TCP or HTTP")
		}
	}
	var ready Readiness
	for key, value := range object {
		switch key {
		case "tcp":
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &ready.TCP) != nil {
				return Readiness{}, errors.New("readiness.tcp must be a string")
			}
		case "http":
			httpObject, err := decodeReadinessObject(value)
			if err != nil {
				return Readiness{}, errors.New("readiness.http must be a strict JSON object")
			}
			ready.HTTP = &HTTPReadiness{Status: http.StatusOK}
			for field, supplied := range httpObject {
				switch field {
				case "url":
					if bytes.Equal(bytes.TrimSpace(supplied), []byte("null")) || json.Unmarshal(supplied, &ready.HTTP.URL) != nil {
						return Readiness{}, errors.New("readiness.http.url must be a string")
					}
				case "status":
					if bytes.Equal(bytes.TrimSpace(supplied), []byte("null")) || json.Unmarshal(supplied, &ready.HTTP.Status) != nil {
						return Readiness{}, errors.New("readiness.http.status must be an integer")
					}
				default:
					return Readiness{}, errors.New("unknown readiness.http field")
				}
			}
		default:
			return Readiness{}, errors.New("unknown readiness field")
		}
	}
	if err := ValidateReadiness(ready); err != nil {
		return Readiness{}, err
	}
	return ready, nil
}

func decodeReadinessObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("readiness must be a JSON object")
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid readiness JSON")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid readiness JSON")
		}
		if _, exists := object[key]; exists {
			return nil, errors.New("duplicate readiness field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("invalid readiness JSON")
		}
		object[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid readiness JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("trailing readiness JSON")
	}
	return object, nil
}

func ValidateReadiness(ready Readiness) error {
	if ready.TCP != "" && ready.HTTP != nil {
		return errors.New("readiness must contain only TCP or HTTP")
	}
	if ready.TCP != "" {
		host, port, err := net.SplitHostPort(ready.TCP)
		if err != nil || !validReadinessHost(host) || !validReadinessPort(port) || (strings.HasPrefix(ready.TCP, "[") && !validReadinessIP(host)) {
			return errors.New("readiness.tcp must be host:port with a numeric port between 1 and 65535")
		}
	}
	if ready.HTTP != nil {
		target, err := url.Parse(ready.HTTP.URL)
		if err != nil || target == nil || !target.IsAbs() || (target.Scheme != "http" && target.Scheme != "https") || !validReadinessHost(target.Hostname()) || target.Opaque != "" {
			return errors.New("readiness.http.url must be an absolute HTTP or HTTPS URL")
		}
		if (strings.HasPrefix(target.Host, "[") && !validReadinessIP(target.Hostname())) || (target.Port() != "" && !validReadinessPort(target.Port())) || strings.HasSuffix(target.Host, ":") {
			return errors.New("readiness.http.url has an invalid host or port")
		}
		if ready.HTTP.Status < 100 || ready.HTTP.Status > 599 {
			return errors.New("readiness.http.status must be between 100 and 599")
		}
	}
	return nil
}

func validReadinessHost(host string) bool {
	if host == "" {
		return false
	}
	if validReadinessIP(host) {
		return true
	}
	for _, character := range host {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validReadinessIP(host string) bool {
	_, err := netip.ParseAddr(host)
	return err == nil
}

func validReadinessPort(port string) bool {
	for _, character := range port {
		if character < '0' || character > '9' {
			return false
		}
	}
	number, err := strconv.Atoi(port)
	return err == nil && number > 0 && number <= 65535
}

func (m *Manager) probeHTTP(ctx context.Context, ready HTTPReadiness) error {
	probeContext, cancel := context.WithTimeout(ctx, readyDialTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeContext, http.MethodGet, ready.URL, nil)
	if err != nil {
		return errors.New("invalid HTTP readiness request")
	}
	response, err := m.httpClient.Do(request)
	if err != nil {
		if probeContext.Err() != nil {
			return fmt.Errorf("HTTP readiness request failed: %w", probeContext.Err())
		}
		return errors.New("HTTP readiness request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != ready.Status {
		return fmt.Errorf("HTTP readiness status is %d, want %d", response.StatusCode, ready.Status)
	}
	return nil
}

func (m *Manager) probeReadiness(ctx context.Context, ready Readiness) error {
	if ready.HTTP != nil {
		return m.probeHTTP(ctx, *ready.HTTP)
	}
	return m.probe(ctx, ready.TCP)
}
