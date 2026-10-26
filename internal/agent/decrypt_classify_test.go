package agent

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func classifyDecryptor(t *testing.T, now time.Time, mutate func(*decryptor)) *decryptor {
	t.Helper()
	d := newDecryptor(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	mutate(d)
	return d
}

func knownScope(t *testing.T, now time.Time, permitted, allow []string) func(*decryptor) {
	t.Helper()
	key := mustKey(t)
	return func(d *decryptor) {
		st, err := d.stateFrom(key, decryptCertResponse(t, now, decryptChainSpec{
			interKey: key, permitted: permitted, isCA: true, notAfter: now.Add(time.Hour), allow: allow, bypass: []string{"bank.decrypt.test"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		d.bound, d.state = true, st
	}
}

func TestDecryptClassifyFailsClosed(t *testing.T) {
	now := time.Now()
	scoped := knownScope(t, now, []string{"decrypt.test"}, []string{"*.decrypt.test"})
	cases := []struct {
		name    string
		d       *decryptor
		host    string
		verdict decryptVerdict
	}{
		{"no decryptor", nil, "api.decrypt.test", decryptPassthrough},
		{"ip literal", classifyDecryptor(t, now, func(*decryptor) {}), "10.0.0.1", decryptPassthrough},
		{"unbound session", classifyDecryptor(t, now, func(*decryptor) {}), "api.decrypt.test", decryptRefuse},
		{"retry window after an issuance error", classifyDecryptor(t, now, func(d *decryptor) { d.bound, d.retryAt = true, now.Add(time.Minute) }), "api.decrypt.test", decryptRefuse},
		{"org has decryption off", classifyDecryptor(t, now, func(d *decryptor) { d.bound, d.disabled = true, true }), "api.decrypt.test", decryptPassthrough},
		{"in scope", classifyDecryptor(t, now, scoped), "api.decrypt.test", decryptTerminate},
		{"bypassed", classifyDecryptor(t, now, scoped), "bank.decrypt.test", decryptPassthrough},
		{"outside scope", classifyDecryptor(t, now, scoped), "api.other.test", decryptPassthrough},
		{"leaf cannot be minted", classifyDecryptor(t, now, knownScope(t, now, []string{"elsewhere.test"}, []string{"*.decrypt.test"})), "api.decrypt.test", decryptRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := tc.d.classify(context.Background(), tc.host)
			if plan.verdict != tc.verdict {
				t.Fatalf("verdict = %v, want %v", plan.verdict, tc.verdict)
			}
			if (plan.cert != nil) != (tc.verdict == decryptTerminate) {
				t.Fatalf("cert presence %v for verdict %v", plan.cert != nil, plan.verdict)
			}
		})
	}
}

func TestDecryptClassifyRefusesWhenIssuanceTimesOut(t *testing.T) {
	now := time.Now()
	d := classifyDecryptor(t, now, func(d *decryptor) { d.bound, d.inflight = true, make(chan struct{}) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if plan := d.classify(ctx, "api.decrypt.test"); plan.verdict != decryptRefuse {
		t.Fatalf("verdict = %v, want refuse while the intermediate is still pending", plan.verdict)
	}
}

func TestServeDecryptedRefusesConnectWhenScopeIsUnknown(t *testing.T) {
	a := &Agent{log: slog.New(slog.NewTextHandler(io.Discard, nil)), decrypt: classifyDecryptor(t, time.Now(), func(*decryptor) {})}
	server, client := net.Pipe()
	defer client.Close()
	handled := make(chan bool, 1)
	go func() {
		defer server.Close()
		handled <- a.serveDecrypted(server, bufio.NewReader(server), "api.decrypt.test:443", egressCreds{})
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), errDecryptUnavailable.Error()) {
		t.Fatalf("CONNECT = %d %q, want 503", resp.StatusCode, body)
	}
	if !<-handled {
		t.Fatal("refused CONNECT fell through to passthrough")
	}
}

func TestSocksRefusesDecryptScopedOrUnknownDestinations(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		d    *decryptor
		dest string
		want bool
	}{
		{"no decryptor", nil, "api.decrypt.test:443", true},
		{"scope unknown", classifyDecryptor(t, now, func(*decryptor) {}), "api.decrypt.test:443", false},
		{"decryption off", classifyDecryptor(t, now, func(d *decryptor) { d.bound, d.disabled = true, true }), "api.decrypt.test:443", true},
		{"in scope", classifyDecryptor(t, now, knownScope(t, now, []string{"decrypt.test"}, []string{"*.decrypt.test"})), "api.decrypt.test:443", false},
		{"outside scope", classifyDecryptor(t, now, knownScope(t, now, []string{"decrypt.test"}, []string{"*.decrypt.test"})), "api.other.test:443", true},
		{"malformed", classifyDecryptor(t, now, func(d *decryptor) { d.bound, d.disabled = true, true }), "no-port", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{log: logger, decrypt: tc.d}
			if got := a.socksMayPassThrough(context.Background(), tc.dest); got != tc.want {
				t.Fatalf("socksMayPassThrough(%q) = %v, want %v", tc.dest, got, tc.want)
			}
		})
	}
}
