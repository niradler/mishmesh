package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIBaseFromGateway(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"wss://gw.example.test/agent/connect", "https://gw.example.test", false},
		{"ws://127.0.0.1:8080", "http://127.0.0.1:8080", false},
		{"https://gw.example.test", "https://gw.example.test", false},
		{"ftp://gw.example.test", "", true},
		{"not a url", "", true},
	}
	for _, tt := range tests {
		got, err := APIBaseFromGateway(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("APIBaseFromGateway(%q) = %q, %v", tt.in, got, err)
		}
	}
}

func TestLoginBrowserCompletesPKCEFlow(t *testing.T) {
	var challenge, redirect string
	srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != "the-code" ||
				pkceChallenge(r.PostForm.Get("code_verifier")) != challenge || r.PostForm.Get("redirect_uri") != redirect {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			writeTokens(w, "acc", "ref")
		default:
			http.NotFound(w, r)
		}
	})
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("client_id") != AgentClientID || q.Get("code_challenge_method") != "S256" || q.Get("org_id") != "org1" {
			t.Errorf("authorize query = %v", q)
		}
		challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
		if !strings.HasPrefix(redirect, "http://127.0.0.1:") {
			t.Errorf("redirect = %q, want loopback", redirect)
		}
		go func() {
			resp, err := http.Get(redirect + "?" + url.Values{"code": {"the-code"}, "state": {q.Get("state")}}.Encode())
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	creds, err := LoginBrowser(ctx, LoginOptions{Server: srv.URL, OrgID: "org1", Open: open})
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != "acc" || creds.RefreshToken != "ref" || creds.OrgID != "org1" || creds.Server != srv.URL {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestLoginBrowserRejectsStateMismatch(t *testing.T) {
	srv := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) { writeTokens(w, "acc", "ref") })
	open := func(raw string) error {
		u, _ := url.Parse(raw)
		redirect := u.Query().Get("redirect_uri")
		go func() {
			resp, err := http.Get(redirect + "?code=x&state=forged")
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := LoginBrowser(ctx, LoginOptions{Server: srv.URL, Open: open}); err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("err = %v, want state mismatch", err)
	}
}

func TestLoginDeviceHandlesPendingAndSlowDown(t *testing.T) {
	var polls atomic.Int32
	srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/device_authorization":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCD-EFGH", "verification_uri": "http://x/device", "expires_in": 30, "interval": 0})
		case "/oauth2/token":
			if r.PostForm.Get("grant_type") != grantDeviceCode || r.PostForm.Get("device_code") != "dc" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
				return
			}
			switch polls.Add(1) {
			case 1:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			default:
				writeTokens(w, "acc", "ref")
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out strings.Builder
	creds, err := LoginDevice(ctx, LoginOptions{Server: srv.URL, Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessToken != "acc" || polls.Load() != 2 {
		t.Fatalf("creds=%+v polls=%d", creds, polls.Load())
	}
	if !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Fatalf("user code not printed: %q", out.String())
	}
}

func TestLoginDeviceStopsOnDenied(t *testing.T) {
	srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth2/device_authorization" {
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "U", "verification_uri": "v", "expires_in": 30, "interval": 0})
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"access_denied"}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := LoginDevice(ctx, LoginOptions{Server: srv.URL, Out: io.Discard}); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v, want denied", err)
	}
}

func TestRevokePostsRefreshToken(t *testing.T) {
	var got url.Values
	srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.WriteHeader(http.StatusOK)
	})
	if err := Revoke(context.Background(), nil, srv.URL, "ref"); err != nil {
		t.Fatal(err)
	}
	if got.Get("token") != "ref" || got.Get("client_id") != AgentClientID {
		t.Fatalf("form = %v", got)
	}
}
