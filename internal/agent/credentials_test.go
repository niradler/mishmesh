package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tokenServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)
	return srv
}

func writeTokens(w http.ResponseWriter, access, refresh string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3600, "org_id": "org1", "token_type": "Bearer"})
}

func seededStore(t *testing.T, server string, issued time.Time) *CredentialStore {
	t.Helper()
	s := NewCredentialStore(filepath.Join(t.TempDir(), "mishmesh", credentialsFile), nil)
	creds := &Credentials{Server: server, OrgID: "org1", AccessToken: "old-access", RefreshToken: "old-refresh", IssuedAt: issued, ExpiresAt: issued.Add(time.Hour)}
	if err := s.Save(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCredentialStoreSaveIsPrivateAndAtomic(t *testing.T) {
	s := seededStore(t, "http://127.0.0.1:1", time.Now())
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file perm = %o, want 600", perm)
		}
		dir, err := os.Stat(filepath.Dir(s.Path()))
		if err != nil {
			t.Fatal(err)
		}
		if perm := dir.Mode().Perm(); perm != 0o700 {
			t.Fatalf("dir perm = %o, want 700", perm)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover temp file %s", e.Name())
		}
	}
	c, err := s.Load()
	if err != nil || c.RefreshToken != "old-refresh" {
		t.Fatalf("load = %+v, %v", c, err)
	}
}

func TestCredentialStoreMissingOrCorruptNeedsLogin(t *testing.T) {
	s := NewCredentialStore(filepath.Join(t.TempDir(), credentialsFile), nil)
	if _, err := s.AccessToken(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("missing: err = %v", err)
	}
	if err := os.WriteFile(s.Path(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("corrupt: err = %v", err)
	}
}

func TestAccessTokenRefreshesAtHalfLifetime(t *testing.T) {
	var hits atomic.Int32
	srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "old-refresh" || r.PostForm.Get("client_id") != AgentClientID {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		writeTokens(w, "new-access", "new-refresh")
	})
	issued := time.Unix(1_000_000, 0).UTC()
	s := seededStore(t, srv.URL, issued)

	s.now = func() time.Time { return issued.Add(29 * time.Minute) }
	tok, err := s.AccessToken(context.Background())
	if err != nil || tok != "old-access" || hits.Load() != 0 {
		t.Fatalf("before half-life: tok=%q err=%v hits=%d", tok, err, hits.Load())
	}

	s.now = func() time.Time { return issued.Add(31 * time.Minute) }
	tok, err = s.AccessToken(context.Background())
	if err != nil || tok != "new-access" || hits.Load() != 1 {
		t.Fatalf("after half-life: tok=%q err=%v hits=%d", tok, err, hits.Load())
	}
	c, err := s.Load()
	if err != nil || c.RefreshToken != "new-refresh" {
		t.Fatalf("rotated refresh not persisted: %+v %v", c, err)
	}
}

func TestAccessTokenInvalidGrantRequiresLogin(t *testing.T) {
	srv := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	issued := time.Unix(1_000_000, 0).UTC()
	s := seededStore(t, srv.URL, issued)
	s.now = func() time.Time { return issued.Add(40 * time.Minute) }
	if _, err := s.AccessToken(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want login required", err)
	}
}

func TestAccessTokenServerOutageKeepsValidToken(t *testing.T) {
	srv := tokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	})
	issued := time.Unix(1_000_000, 0).UTC()
	s := seededStore(t, srv.URL, issued)

	s.now = func() time.Time { return issued.Add(40 * time.Minute) }
	tok, err := s.AccessToken(context.Background())
	if err != nil || tok != "old-access" {
		t.Fatalf("outage within lifetime: tok=%q err=%v", tok, err)
	}

	s.now = func() time.Time { return issued.Add(61 * time.Minute) }
	if _, err := s.AccessToken(context.Background()); err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("outage after expiry: err = %v, want transient error", err)
	}
}

func TestAccessTokenConcurrentCallersRefreshOnce(t *testing.T) {
	var hits atomic.Int32
	srv := tokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("refresh_token") != "old-refresh" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		hits.Add(1)
		writeTokens(w, "new-access", "new-refresh")
	})
	issued := time.Now().Add(-40 * time.Minute).UTC()
	path := seededStore(t, srv.URL, issued).Path()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := NewCredentialStore(path, nil)
			tok, err := s.AccessToken(context.Background())
			if err == nil && tok != "new-access" {
				err = errors.New("unexpected token " + tok)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("refresh hits = %d, want 1", hits.Load())
	}
}

func TestRemoveDeletesCredentials(t *testing.T) {
	s := seededStore(t, "http://127.0.0.1:1", time.Now())
	if err := s.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("after remove: %v", err)
	}
	if err := s.Remove(context.Background()); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}

func TestCredentialsPathIn(t *testing.T) {
	tests := []struct {
		name    string
		current bool
		legacy  bool
		want    string
	}{
		{name: "fresh install uses mishmesh home", want: ".mishmesh"},
		{name: "legacy only keeps the existing login", legacy: true, want: filepath.Join(".config", "mishmesh")},
		{name: "both prefer mishmesh home", current: true, legacy: true, want: ".mishmesh"},
		{name: "current only", current: true, want: ".mishmesh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			for dir, present := range map[string]bool{".mishmesh": tt.current, filepath.Join(".config", "mishmesh"): tt.legacy} {
				if !present {
					continue
				}
				if err := os.MkdirAll(filepath.Join(home, dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, dir, credentialsFile), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got, want := credentialsPathIn(home), filepath.Join(home, tt.want, credentialsFile); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
