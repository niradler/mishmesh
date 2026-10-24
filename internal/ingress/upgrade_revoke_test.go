package ingress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

type upgradeEchoAgent struct{}

func (upgradeEchoAgent) AgentID() string { return "ag_test" }
func (upgradeEchoAgent) Close() error    { return nil }
func (upgradeEchoAgent) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		br := bufio.NewReader(server)
		if _, err := http.ReadRequest(br); err != nil {
			return
		}
		if _, err := io.WriteString(server, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"); err != nil {
			return
		}
		_, _ = io.Copy(server, br)
	}()
	return client, nil
}

type hijackableRecorder struct {
	*httptest.ResponseRecorder
}

func (hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrHijacked
}

func newUpgradeServer(t *testing.T, svc *identity.Service) string {
	t.Helper()
	ing := &Ingress{log: discardLogger(), conns: nopConns{}, identity: svc, deciders: &fakeDeciders{decide: allowAll}}
	ing.upgrades = newUpgradeTracker(ing, svc)
	ep := identityEndpoint(store.EndpointIdentity{Required: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, ok := ing.applyIdentityGate(w, r, ep, "/ws")
		if !ok {
			return
		}
		ing.proxyUpgrade(w, r, upgradeEchoAgent{}, ep, "/ws", 0)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func dialUpgrade(t *testing.T, addr, raw string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET /ws HTTP/1.1\r\nHost: demo.localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n" + identityAuthHeader + ": Bearer " + raw + "\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, br, resp
}

func TestUpgradeClosedWhenSessionRevoked(t *testing.T) {
	svc := newIdentityService(t)
	raw := issueSAToken(t, svc, "org_1")
	p, err := svc.Authenticate(context.Background(), raw, token.TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	c, br, resp := dialUpgrade(t, newUpgradeServer(t, svc), raw)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err := io.ReadFull(br, got); err != nil || string(got) != "ping" {
		t.Fatalf("echo = %q, %v", got, err)
	}
	if err := svc.RevokeSession(context.Background(), p.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(br); err != nil {
		t.Fatalf("upgraded connection not closed after revocation: %v", err)
	}
}

func TestUpgradeRefusedWhenSessionRevokedBeforeTracking(t *testing.T) {
	svc := newIdentityService(t)
	raw := issueSAToken(t, svc, "org_1")
	p, err := svc.Authenticate(context.Background(), raw, token.TypeAccess)
	if err != nil {
		t.Fatal(err)
	}
	ing := &Ingress{log: discardLogger(), conns: nopConns{}, identity: svc}
	ing.upgrades = newUpgradeTracker(ing, svc)
	ep := identityEndpoint(store.EndpointIdentity{Required: true})
	r := httptest.NewRequest(http.MethodGet, "http://demo.localhost/ws", nil)
	r = r.WithContext(context.WithValue(r.Context(), identityKey{}, &requestIdentity{principal: &p}))
	if err := svc.RevokeSession(context.Background(), p.SessionID); err != nil {
		t.Fatal(err)
	}
	w := hijackableRecorder{httptest.NewRecorder()}
	ing.proxyUpgrade(w, r, upgradeEchoAgent{}, ep, "/ws", 0)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if ing.upgrades.Len() != 0 {
		t.Fatalf("tracked upgrades = %d, want 0", ing.upgrades.Len())
	}
}
