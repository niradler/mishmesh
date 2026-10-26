package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestDecodePolicyKeepsOnlyAgentSettableFields(t *testing.T) {
	populated := store.EndpointPolicy{
		RequestHeadersAdd:     map[string]string{"X-A": "1"},
		RequestHeadersRemove:  []string{"X-B"},
		ResponseHeadersAdd:    map[string]string{"X-C": "1"},
		ResponseHeadersRemove: []string{"X-D"},
		HostHeader:            "h.example",
		StripPathPrefix:       "/s",
		AddPathPrefix:         "/a",
		BasicAuthUser:         "u",
		BasicAuthHash:         "hash",
		IPAllow:               []string{"10.0.0.0/8"},
		IPDeny:                []string{"192.168.0.0/16"},
		ForceHTTPS:            true,
		MaxBodyBytes:          10,
		Compression:           true,
		OIDC:                  &store.OIDCEndpointAuth{Issuer: "https://i", ClientID: "c", ClientSecret: "s"},
		MTLS:                  &store.MTLSConfig{ClientCAPEM: "pem"},
		RateLimit:             &store.RateLimit{Requests: 5, PeriodSeconds: 1},
		ProxyTargets:          []string{"169.254.169.254:80"},
		LoadBalance:           store.LoadBalanceFailover,
		OriginTLS:             &store.OriginTLS{InsecureSkipVerify: true},
		ServiceID:             "svc",
		Connect:               &store.ConnectPolicy{Name: "db", AllowAgents: []string{"*"}},
		Access:                []store.AccessRef{{ConnectionID: "c"}},
		AccessPolicy:          &store.AccessPolicy{AllowEmails: []string{"a@b.c"}},
		Identity: &store.EndpointIdentity{
			Required:    true,
			Credentials: map[string]store.EndpointCredential{"c": {Header: "Authorization", Secret: "org-secret"}},
		},
	}
	agentSettable := map[string]bool{
		"RequestHeadersAdd": true, "RequestHeadersRemove": true, "ResponseHeadersAdd": true, "ResponseHeadersRemove": true,
		"HostHeader": true, "StripPathPrefix": true, "AddPathPrefix": true, "BasicAuthUser": true, "BasicAuthHash": true,
		"IPAllow": true, "IPDeny": true, "ForceHTTPS": true, "MaxBodyBytes": true, "Compression": true,
		"OIDC": true, "MTLS": true, "RateLimit": true, "Connect": true,
	}
	wantIdentity := &store.EndpointIdentity{Required: true}
	raw, err := json.Marshal(populated)
	if err != nil {
		t.Fatal(err)
	}
	got := decodePolicy(raw, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got == nil {
		t.Fatal("policy dropped entirely")
	}
	gv, wv := reflect.ValueOf(*got), reflect.ValueOf(populated)
	for i := 0; i < gv.NumField(); i++ {
		name := gv.Type().Field(i).Name
		if name == "Identity" {
			if !reflect.DeepEqual(gv.Field(i).Interface(), wantIdentity) {
				t.Errorf("identity must keep its gating flags and lose its credentials, got %+v", gv.Field(i).Interface())
			}
			continue
		}
		if agentSettable[name] {
			if !reflect.DeepEqual(gv.Field(i).Interface(), wv.Field(i).Interface()) {
				t.Errorf("agent-settable field %s was altered: %v", name, gv.Field(i).Interface())
			}
			continue
		}
		if !gv.Field(i).IsZero() {
			t.Errorf("non-agent field %s survived decodePolicy: %v", name, gv.Field(i).Interface())
		}
	}
}
