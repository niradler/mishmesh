package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/controlplane"
	"github.com/mishmesh/mishmesh/internal/gateway"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("no interface addresses: %v", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP.To4()
		if ip != nil && ip.IsPrivate() {
			return ip.String()
		}
	}
	t.Skip("no private non-loopback IPv4 address available")
	return ""
}

func TestReachInOnlyAgentWithIngressDisabled(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	data, err := sqlite.Open(filepath.Join(t.TempDir(), "reach.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	conns := memory.NewConnStore()

	now := time.Now()
	org := &store.Org{ID: store.NewID("org"), Name: "t", CreatedAt: now}
	if err := data.CreateOrg(ctx, org); err != nil {
		t.Fatal(err)
	}
	ag := &store.Agent{ID: store.NewID("ag"), OrgID: org.ID, Name: "gw", Status: store.AgentActive, CreatedAt: now}
	if err := data.CreateAgent(ctx, ag); err != nil {
		t.Fatal(err)
	}
	rawToken, hash, err := store.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := data.CreateToken(ctx, &store.Token{ID: store.NewID("tok"), OrgID: org.ID, AgentID: ag.ID, Hash: hash, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	const disabled = "ingress is disabled on this server (MISHMESH_INGRESS_ENABLED=false)"
	gw := gateway.New(gateway.Options{
		Data: data, Conns: conns, Log: log, BaseDomain: "localhost", PublicScheme: "http",
		KindUnavailable: map[string]string{store.KindHTTP: disabled, store.KindTLS: disabled, store.KindTCP: disabled},
	})
	mux := http.NewServeMux()
	mux.HandleFunc(tunnel.AgentConnectPath, gw.HandleAgentConnect)
	api := controlplane.New(data, conns, "op-secret", log)
	api.SetReachInEnabled(true)
	api.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hostIP := nonLoopbackIPv4(t)
	listener, err := net.Listen("tcp", net.JoinHostPort(hostIP, "0"))
	if err != nil {
		t.Skipf("cannot listen on %s: %v", hostIP, err)
	}
	internal := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "internal:"+r.URL.Path)
	}))
	internal.Listener = listener
	internal.Start()
	t.Cleanup(internal.Close)
	target := mustHost(t, internal.URL)

	cli := agent.New(agent.Options{
		GatewayURL: "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:      rawToken,
		Log:        log,
		Out:        io.Discard,
		Allowlist:  []string{hostIP + "/32"},
	})
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	runErr := make(chan error, 1)
	go func() { runErr <- cli.Run(runCtx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := conns.GetAgent(ag.ID); ok {
			break
		}
		select {
		case err := <-runErr:
			t.Fatalf("agent exited: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not connect")
		}
		time.Sleep(20 * time.Millisecond)
	}

	body := `{"target":"` + target + `","path":"/hello"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/reach/"+ag.ID+"/http?org_id="+org.ID, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer op-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reach-in status %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != http.StatusOK || out.Body != "internal:/hello" {
		t.Fatalf("reach-in response: %+v", out)
	}

	select {
	case err := <-runErr:
		t.Fatalf("agent exited after reach-in: %v", err)
	default:
	}
}
