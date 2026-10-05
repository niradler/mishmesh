package ingress

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSetForwardedHeaders(t *testing.T) {
	trusted := []*net.IPNet{mustCIDR(t, "10.0.0.0/8")}
	cases := []struct {
		name      string
		remote    string
		tls       bool
		inbound   map[string]string
		trusted   []*net.IPNet
		wantXFF   string
		wantProto string
		wantHost  string
	}{
		{"untrusted client cannot spoof", "203.0.113.9:4000", false,
			map[string]string{"X-Forwarded-For": "6.6.6.6", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "evil"},
			nil, "203.0.113.9", "http", "app.example.com"},
		{"untrusted with tls", "203.0.113.9:4000", true, nil, nil, "203.0.113.9", "https", "app.example.com"},
		{"trusted proxy appends to xff and keeps proto and host", "10.1.2.3:5000", false,
			map[string]string{"X-Forwarded-For": "198.51.100.7", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "public.example.com"},
			trusted, "198.51.100.7, 10.1.2.3", "https", "public.example.com"},
		{"trusted proxy without headers", "10.1.2.3:5000", false, nil, trusted, "10.1.2.3", "http", "app.example.com"},
		{"peer outside trusted nets", "192.0.2.1:5000", false,
			map[string]string{"X-Forwarded-For": "6.6.6.6"}, trusted, "192.0.2.1", "http", "app.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := httptest.NewRequest("GET", "http://app.example.com/", nil)
			in.RemoteAddr = tc.remote
			if tc.tls {
				in.TLS = &tls.ConnectionState{}
			}
			for k, v := range tc.inbound {
				in.Header.Set(k, v)
			}
			out := in.Clone(context.Background())
			out.Header.Set("Forwarded", "for=6.6.6.6")
			setForwardedHeaders(in, out, tc.trusted)
			if got := out.Header.Get("X-Forwarded-For"); got != tc.wantXFF {
				t.Errorf("xff = %q want %q", got, tc.wantXFF)
			}
			if got := out.Header.Get("X-Forwarded-Proto"); got != tc.wantProto {
				t.Errorf("proto = %q want %q", got, tc.wantProto)
			}
			if got := out.Header.Get("X-Forwarded-Host"); got != tc.wantHost {
				t.Errorf("host = %q want %q", got, tc.wantHost)
			}
			if out.Header.Get("Forwarded") != "" {
				t.Error("Forwarded header must be dropped")
			}
		})
	}
}

func TestMeteredConnEnforcesQuotaInBatches(t *testing.T) {
	conns := memory.NewConnStore()
	near, far := net.Pipe()
	defer far.Close()
	go func() {
		chunk := bytes.Repeat([]byte("x"), 16<<10)
		for {
			if _, err := far.Write(chunk); err != nil {
				return
			}
		}
	}()

	mc := newMeteredConn(near, meterTarget{conns: conns, orgID: "org_q", kind: store.KindHTTP, limit: 256 << 10})
	var total int64
	buf := make([]byte, 16<<10)
	for {
		n, err := mc.Read(buf)
		total += int64(n)
		if err != nil {
			if !errors.Is(err, errBandwidthExceeded) && !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("unexpected error: %v", err)
			}
			break
		}
	}
	if total > 512<<10 {
		t.Fatalf("read %d bytes against a 256KiB quota", total)
	}
	if !mc.exceeded.Load() {
		t.Fatal("conn not flagged as exceeded")
	}
	_ = mc.Close()
	if usage := conns.Usage("org_q"); usage != total {
		t.Fatalf("usage = %d, want %d", usage, total)
	}
}

func TestMeteredConnBatchesStoreWrites(t *testing.T) {
	conns := &countingConns{ConnStore: memory.NewConnStore()}
	near, far := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, far) }()

	mc := newMeteredConn(near, meterTarget{conns: conns, orgID: "org_b", kind: store.KindHTTP})
	chunk := bytes.Repeat([]byte("x"), 4<<10)
	for i := 0; i < 256; i++ {
		if _, err := mc.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = mc.Close()
	if got := conns.addCalls.Load(); got > 3 {
		t.Fatalf("AddUsage called %d times for 1MiB, want batching", got)
	}
	if usage := conns.ConnStore.Usage("org_b"); usage != 1<<20 {
		t.Fatalf("usage = %d", usage)
	}
}

type countingConns struct {
	*memory.ConnStore
	addCalls atomic.Int64
}

func (c *countingConns) AddUsage(org string, n int64) {
	c.addCalls.Add(1)
	c.ConnStore.AddUsage(org, n)
}

func TestGzipBodyRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"small", "hello"},
		{"large", string(bytes.Repeat([]byte("abc"), 100000))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zr, err := gzip.NewReader(newGzipBody(io.NopCloser(bytes.NewBufferString(tc.in))))
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(zr)
			if err != nil || string(got) != tc.in {
				t.Fatalf("roundtrip mismatch err=%v len=%d want %d", err, len(got), len(tc.in))
			}
		})
	}
}

type scriptedAgent struct {
	open func() (net.Conn, error)
}

func (s *scriptedAgent) AgentID() string { return "ag_test" }
func (s *scriptedAgent) Close() error    { return nil }
func (s *scriptedAgent) OpenStream(context.Context, string, string, map[string]string) (net.Conn, error) {
	return s.open()
}

func TestParseTrustedProxies(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"", 0, false},
		{"10.0.0.0/8, 192.168.1.5", 2, false},
		{"::1", 1, false},
		{"not-an-ip", 0, true},
		{"10.0.0.0/99", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseTrustedProxies(tc.in)
		if (err != nil) != tc.wantErr || len(got) != tc.want {
			t.Errorf("%q: got %d nets err=%v", tc.in, len(got), err)
		}
	}
}

func TestProxyErrorMapping(t *testing.T) {
	ep := &store.Endpoint{ID: "ep_1", Kind: store.KindHTTP}
	cases := []struct {
		name     string
		timeout  time.Duration
		open     func() (net.Conn, error)
		wantCode int
		wantText string
	}{
		{
			name: "open stream failure is 503", open: func() (net.Conn, error) { return nil, errors.New("session closed") },
			wantCode: http.StatusServiceUnavailable, wantText: "tunnel offline",
		},
		{
			name: "agent reports a dial failure", open: func() (net.Conn, error) {
				near, far := net.Pipe()
				go func() { _ = tunnel.WriteStreamError(far, "connection refused"); _ = far.Close() }()
				return near, nil
			},
			wantCode: http.StatusBadGateway, wantText: "upstream service unreachable on the agent side (connection refused)",
		},
		{
			name: "stream closed with no response is 502", open: func() (net.Conn, error) {
				near, far := net.Pipe()
				_ = far.Close()
				return near, nil
			},
			wantCode: http.StatusBadGateway, wantText: "upstream service unreachable on the agent side",
		},
		{
			name: "silent upstream times out as 504", timeout: 100 * time.Millisecond, open: func() (net.Conn, error) {
				near, far := net.Pipe()
				go func() { _, _ = io.Copy(io.Discard, far) }()
				return near, nil
			},
			wantCode: http.StatusGatewayTimeout, wantText: "timed out",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ing := &Ingress{log: discardLogger(), conns: nopConns{}}
			ing.proxy = ing.newProxy(tc.timeout)
			r := httptest.NewRequest("GET", "http://demo.localhost/", nil)
			w := httptest.NewRecorder()
			ing.proxyHTTP(w, r, &scriptedAgent{open: tc.open}, ep, "/", 0)
			if w.Code != tc.wantCode {
				t.Fatalf("code = %d want %d (body %q)", w.Code, tc.wantCode, w.Body.String())
			}
			if !bytes.Contains(w.Body.Bytes(), []byte(tc.wantText)) {
				t.Fatalf("body %q does not contain %q", w.Body.String(), tc.wantText)
			}
		})
	}
}
