package e2e

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
	"github.com/mishmesh/mishmesh/internal/connect/proxy/dnstest"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	decryptAPIHost   = "api.decrypt.test"
	decryptWSHost    = "ws.decrypt.test"
	decryptBankHost  = "bank.decrypt.test"
	decryptBadHost   = "bad.decrypt.test"
	decryptPlainHost = "plain.other.test"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(72 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(ca.cert)
	return p
}

func (ca *testCA) serverCert(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(72 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type decryptUpstream struct {
	srv  *httptest.Server
	hits atomic.Int64
	mu   sync.Mutex
	auth string
	host string
}

func newDecryptUpstream(t *testing.T, cert tls.Certificate) *decryptUpstream {
	t.Helper()
	u := &decryptUpstream{}
	u.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.mu.Lock()
		u.auth, u.host = r.Header.Get("Authorization"), r.Host
		u.mu.Unlock()
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			echoUpgrade(w)
			return
		}
		_, _ = fmt.Fprintf(w, "%s %s", r.Method, r.URL.Path)
	}))
	u.srv.EnableHTTP2 = true
	u.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	u.srv.StartTLS()
	t.Cleanup(u.srv.Close)
	return u
}

func echoUpgrade(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	c, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer c.Close()
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	_ = rw.Flush()
	_, _ = io.Copy(c, rw)
}

func (u *decryptUpstream) seen() (auth, host string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.auth, u.host
}

func (u *decryptUpstream) port() string {
	_, port, _ := net.SplitHostPort(u.srv.Listener.Addr().String())
	return port
}

type decryptFixture struct {
	t         *testing.T
	s         *ztStack
	owner     *ztUser
	clock     *fakeClock
	upstream  *decryptUpstream
	bad       *decryptUpstream
	testCA    *testCA
	orgRoots  *x509.CertPool
	proxyAddr string
	agentID   string
}

func newDecryptFixture(t *testing.T) *decryptFixture {
	t.Helper()
	dns := dnstest.Start(t, map[string][]string{
		decryptAPIHost: {"127.0.0.1"}, decryptWSHost: {"127.0.0.1"}, decryptBankHost: {"127.0.0.1"},
		decryptBadHost: {"127.0.0.1"}, decryptPlainHost: {"127.0.0.1"},
	})
	clock := &fakeClock{now: time.Now()}
	s := newZTStackWith(t, ztOptions{resolver: dns.Resolver(), clock: clock.Now})
	owner := s.register("owner@decrypt.io", "")

	ca := newTestCA(t, "upstream test ca")
	upstream := newDecryptUpstream(t, ca.serverCert(t, decryptAPIHost, decryptWSHost, decryptBankHost, decryptPlainHost))
	bad := newDecryptUpstream(t, newTestCA(t, "rogue ca").serverCert(t, decryptBadHost))

	agentID, agentToken := owner.createAgent("decrypting")
	owner.putPolicy(fmt.Sprintf(`
permit (principal, action == Action::"egress:connect", resource);
permit (principal, action == Action::"egress:request", resource) when { resource.host == %[1]q && context.decrypted && context.method == "GET" && context.path like "/v1/*" };
@credential("api-key")
permit (principal, action == Action::"egress:request", resource) when { resource.host == %[1]q && context.decrypted && context.path like "/inject/*" };
permit (principal, action == Action::"egress:request", resource) when { (resource.host == %[2]q || resource.host == %[3]q) && context.decrypted };
`, decryptAPIHost, decryptWSHost, decryptBadHost))
	owner.putSecret("api-secret", "s1")
	owner.mustJSON(http.MethodPut, "/api/v1/egress/credentials/api-key", map[string]any{
		"header": "Authorization", "prefix": "Bearer ", "secret": "api-secret", "agent_ids": []string{agentID}, "destinations": []string{decryptAPIHost},
	}, http.StatusOK, nil)
	owner.mustJSON(http.MethodPut, "/api/v1/egress/decrypt", map[string]any{
		"allow": []string{"*.decrypt.test"}, "bypass": []string{decryptBankHost},
	}, http.StatusOK, nil)

	proxyAddr := freeAddr(t, "tcp")
	s.runAgent(agent.Options{Token: agentToken, UpstreamRoots: ca.pool(), Clock: clock.Now, Egress: &agent.EgressSpec{Listen: proxyAddr}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var orgCA tunnel.EgressCA
	ztEventually(t, 5*time.Second, "agent egress ca endpoint", func() bool {
		var err error
		orgCA, err = agent.FetchEgressCA(ctx, s.wsURL, agentToken, nil)
		return err == nil
	})
	var apiCA struct {
		PEM string `json:"pem"`
	}
	owner.mustJSON(http.MethodGet, "/api/v1/egress/ca", nil, http.StatusOK, &apiCA)
	if apiCA.PEM != orgCA.PEM {
		t.Fatal("agent egress ca route and user api disagree on the org root")
	}
	block, _ := pem.Decode([]byte(orgCA.PEM))
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	orgRoots := x509.NewCertPool()
	orgRoots.AddCert(root)
	return &decryptFixture{t: t, s: s, owner: owner, clock: clock, upstream: upstream, bad: bad, testCA: ca, orgRoots: orgRoots, proxyAddr: proxyAddr, agentID: agentID}
}

func (f *decryptFixture) target(host string) string {
	port := f.upstream.port()
	if host == decryptBadHost {
		port = f.bad.port()
	}
	return "https://" + net.JoinHostPort(host, port)
}

type decryptResult struct {
	status int
	body   string
	proto  string
	chain  []*x509.Certificate
	err    error
}

func (f *decryptFixture) get(host, path string, roots *x509.CertPool, h2 bool, header http.Header) decryptResult {
	f.t.Helper()
	rt := &http.Transport{
		Proxy:             http.ProxyURL(&url.URL{Scheme: "http", Host: f.proxyAddr}),
		TLSClientConfig:   &tls.Config{RootCAs: roots, Time: f.clock.Now},
		ForceAttemptHTTP2: h2,
	}
	if !h2 {
		rt.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	}
	defer rt.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, f.target(host)+path, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := (&http.Client{Transport: rt, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return decryptResult{err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := decryptResult{status: resp.StatusCode, body: string(body), proto: resp.Proto}
	if resp.TLS != nil {
		out.chain = resp.TLS.PeerCertificates
	}
	return out
}

func (f *decryptFixture) mustDecrypted(host, path string, h2 bool, header http.Header) decryptResult {
	f.t.Helper()
	var r decryptResult
	ztEventually(f.t, 8*time.Second, "decrypted "+host+path, func() bool {
		r = f.get(host, path, f.orgRoots, h2, header)
		return r.err == nil && r.status != http.StatusServiceUnavailable
	})
	if len(r.chain) < 2 || r.chain[0].DNSNames[0] != host {
		f.t.Fatalf("decrypted %s presented an unexpected chain", host)
	}
	return r
}

func TestDecryptAllowlistedHostEnforcesPerRequestPolicy(t *testing.T) {
	f := newDecryptFixture(t)
	for _, h2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("h2=%v", h2), func(t *testing.T) {
			wantProto := "HTTP/1.1"
			if h2 {
				wantProto = "HTTP/2.0"
			}
			r := f.mustDecrypted(decryptAPIHost, "/v1/items", h2, nil)
			if r.status != http.StatusOK || r.body != "GET /v1/items" || r.proto != wantProto {
				t.Fatalf("allowed request = %d %q over %s, want 200 over %s", r.status, r.body, r.proto, wantProto)
			}
			if _, host := f.upstream.seen(); host != net.JoinHostPort(decryptAPIHost, f.upstream.port()) {
				t.Fatalf("upstream saw Host %q", host)
			}
			before := f.upstream.hits.Load()
			r = f.mustDecrypted(decryptAPIHost, "/admin", h2, nil)
			if r.status != http.StatusForbidden || !strings.Contains(r.body, "egress refused") {
				t.Fatalf("denied path = %d %q, want 403", r.status, r.body)
			}
			if f.upstream.hits.Load() != before {
				t.Fatal("a denied decrypted request reached the upstream")
			}
		})
	}

	t.Run("credential injection through decryption", func(t *testing.T) {
		r := f.mustDecrypted(decryptAPIHost, "/inject/x", false, http.Header{"Authorization": {"Bearer client-value"}})
		if r.status != http.StatusOK {
			t.Fatalf("inject = %d %q", r.status, r.body)
		}
		if auth, _ := f.upstream.seen(); auth != "Bearer s1" {
			t.Fatalf("upstream Authorization = %q, want the injected credential", auth)
		}
	})

	t.Run("host header cannot redirect a decrypted request", func(t *testing.T) {
		r := f.mustDecrypted(decryptAPIHost, "/v1/front", false, http.Header{"Host": {decryptWSHost}})
		if _, host := f.upstream.seen(); r.status != http.StatusOK || !strings.HasPrefix(host, decryptAPIHost) {
			t.Fatalf("got %d, upstream Host %q", r.status, host)
		}
	})

	t.Run("upstream certificate failure is a 502", func(t *testing.T) {
		r := f.mustDecrypted(decryptBadHost, "/x", false, nil)
		if r.status != http.StatusBadGateway || !strings.Contains(r.body, "certificate verification failed") {
			t.Fatalf("got %d %q", r.status, r.body)
		}
		if f.bad.hits.Load() != 0 {
			t.Fatal("request reached an upstream with an untrusted certificate")
		}
	})
}

func TestDecryptBypassedAndUnlistedHostsPassThrough(t *testing.T) {
	f := newDecryptFixture(t)
	f.mustDecrypted(decryptAPIHost, "/v1/ready", false, nil)
	for _, host := range []string{decryptBankHost, decryptPlainHost} {
		t.Run(host, func(t *testing.T) {
			r := f.get(host, "/anything", f.testCA.pool(), true, nil)
			if r.err != nil || r.status != http.StatusOK {
				t.Fatalf("passthrough %s = %d, %v", host, r.status, r.err)
			}
			if r.chain[0].Issuer.CommonName != "upstream test ca" {
				t.Fatalf("%s was intercepted: issuer %q", host, r.chain[0].Issuer.CommonName)
			}
			if r := f.get(host, "/anything", f.orgRoots, true, nil); r.err == nil {
				t.Fatalf("%s verified against the org root, so it was decrypted", host)
			}
		})
	}
}

func (f *decryptFixture) raw(host, requestLine string) (int, string) {
	f.t.Helper()
	c, _, status := proxyConnect(f.t, f.proxyAddr, net.JoinHostPort(host, f.upstream.port()))
	if status != http.StatusOK {
		f.t.Fatalf("CONNECT = %d", status)
	}
	tc := tls.Client(c, &tls.Config{ServerName: host, RootCAs: f.orgRoots, Time: f.clock.Now, NextProtos: []string{"http/1.1"}})
	defer tc.Close()
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(tc, "%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", requestLine, host)
	resp, err := http.ReadResponse(bufio.NewReader(tc), &http.Request{Method: http.MethodGet})
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDecryptRefusesNonCanonicalRequests(t *testing.T) {
	f := newDecryptFixture(t)
	f.mustDecrypted(decryptAPIHost, "/v1/ready", false, nil)
	before := f.upstream.hits.Load()
	for _, tc := range []struct {
		line string
		want int
	}{
		{"get /v1/items", http.StatusBadRequest},
		{"GET //v1/items", http.StatusBadRequest},
		{"GET /v1//items", http.StatusBadRequest},
		{"GET /v1/items;x=1", http.StatusBadRequest},
		{"GET /v1/%2e%2e/admin", http.StatusBadRequest},
		{"TRACE /inject/x", http.StatusForbidden},
	} {
		if status, body := f.raw(decryptAPIHost, tc.line); status != tc.want {
			t.Errorf("%q = %d %q, want %d", tc.line, status, body, tc.want)
		}
	}
	if f.upstream.hits.Load() != before {
		t.Fatal("a non-canonical or TRACE request reached the upstream")
	}
}

func TestDecryptRefusesMismatchedSNI(t *testing.T) {
	f := newDecryptFixture(t)
	f.mustDecrypted(decryptAPIHost, "/v1/ready", false, nil)
	c, _, status := proxyConnect(t, f.proxyAddr, net.JoinHostPort(decryptAPIHost, f.upstream.port()))
	if status != http.StatusOK {
		t.Fatalf("CONNECT = %d", status)
	}
	tc := tls.Client(c, &tls.Config{ServerName: decryptWSHost, RootCAs: f.orgRoots, Time: f.clock.Now})
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := tc.Handshake(); err == nil {
		t.Fatal("handshake with an SNI other than the CONNECT host succeeded")
	}
}

func TestDecryptWebSocket(t *testing.T) {
	f := newDecryptFixture(t)
	f.mustDecrypted(decryptWSHost, "/ready", false, nil)
	before := f.upstream.hits.Load()
	c, _, status := proxyConnect(t, f.proxyAddr, net.JoinHostPort(decryptWSHost, f.upstream.port()))
	if status != http.StatusOK {
		t.Fatalf("CONNECT = %d", status)
	}
	tc := tls.Client(c, &tls.Config{ServerName: decryptWSHost, RootCAs: f.orgRoots, Time: f.clock.Now, NextProtos: []string{"http/1.1"}})
	_ = tc.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(tc, "GET /socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", decryptWSHost)
	br := bufio.NewReader(tc)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upgrade = %d %q", resp.StatusCode, body)
	}
	assertEcho(t, tc, br)
	if f.upstream.hits.Load() != before+1 {
		t.Fatal("websocket upgrade did not reach the upstream exactly once")
	}
}

func TestDecryptRotatesExpiredIntermediate(t *testing.T) {
	f := newDecryptFixture(t)
	first := f.mustDecrypted(decryptAPIHost, "/v1/a", false, nil)
	f.clock.Advance(25 * time.Hour)
	second := f.mustDecrypted(decryptAPIHost, "/v1/b", false, nil)
	if second.status != http.StatusOK {
		t.Fatalf("after rotation = %d %q", second.status, second.body)
	}
	oldInter, newInter := first.chain[1], second.chain[1]
	if oldInter.SerialNumber.Cmp(newInter.SerialNumber) == 0 {
		t.Fatal("the expired intermediate was reused")
	}
	if !f.clock.Now().Before(newInter.NotAfter) || f.clock.Now().Before(newInter.NotBefore) {
		t.Fatalf("new intermediate %v..%v is not valid at %v", newInter.NotBefore, newInter.NotAfter, f.clock.Now())
	}
}

func TestDecryptConfigChangeFlushesAgent(t *testing.T) {
	f := newDecryptFixture(t)
	f.mustDecrypted(decryptAPIHost, "/v1/a", false, nil)
	f.owner.mustJSON(http.MethodPut, "/api/v1/egress/decrypt", map[string]any{
		"allow": []string{"*.decrypt.test"}, "bypass": []string{decryptBankHost, decryptAPIHost},
	}, http.StatusOK, nil)
	ztEventually(t, 5*time.Second, "bypass applied after flush", func() bool {
		r := f.get(decryptAPIHost, "/v1/a", f.testCA.pool(), false, nil)
		return r.err == nil && r.chain[0].Issuer.CommonName == "upstream test ca"
	})
}

func TestDecryptDisabledOrgPassesThrough(t *testing.T) {
	f := newDecryptFixture(t)
	f.mustDecrypted(decryptAPIHost, "/v1/a", false, nil)
	f.owner.mustJSON(http.MethodPut, "/api/v1/egress/decrypt", map[string]any{"allow": []string{}}, http.StatusOK, nil)
	ztEventually(t, 5*time.Second, "passthrough after decryption is disabled", func() bool {
		r := f.get(decryptAPIHost, "/v1/a", f.testCA.pool(), true, nil)
		return r.err == nil && r.chain[0].Issuer.CommonName == "upstream test ca"
	})
	if r := f.get(decryptWSHost, "/x", f.orgRoots, false, nil); r.err == nil {
		t.Fatal("a host was still decrypted after the org disabled decryption")
	}
}

func TestDecryptIntermediateNameConstraintsHoldAgainstOrgRoot(t *testing.T) {
	f := newDecryptFixture(t)
	presented := f.mustDecrypted(decryptAPIHost, "/v1/a", false, nil).chain[1]

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "probe"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := f.s.data.GetAgent(f.s.ctx, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := f.s.egressCA.IssueDecryptCert(f.s.ctx, ag, csr)
	if err != nil || resp.Error != "" || len(resp.Chain) == 0 {
		t.Fatalf("issue = %+v, %v", resp, err)
	}
	inter, err := x509.ParseCertificate(resp.Chain[0])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inter.PermittedDNSDomains, presented.PermittedDNSDomains) || !slices.Equal(inter.ExcludedDNSDomains, presented.ExcludedDNSDomains) || !presented.PermittedDNSDomainsCritical {
		t.Fatalf("agent intermediate constraints %v / %v differ from issued %v / %v", presented.PermittedDNSDomains, presented.ExcludedDNSDomains, inter.PermittedDNSDomains, inter.ExcludedDNSDomains)
	}
	intermediates := x509.NewCertPool()
	intermediates.AddCert(inter)

	for _, tc := range []struct {
		name  string
		dns   []string
		ips   []net.IP
		valid bool
	}{
		{"allowlisted host", []string{decryptAPIHost}, nil, true},
		{"outside the allowlist", []string{"evil.test"}, nil, false},
		{"bypassed host", []string{decryptBankHost}, nil, false},
		{"allowlisted plus outside name", []string{decryptAPIHost, "evil.test"}, nil, false},
		{"ip address", nil, []net.IP{net.ParseIP("127.0.0.1")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			now := f.clock.Now()
			der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
				SerialNumber: big.NewInt(now.UnixNano()),
				Subject:      pkix.Name{CommonName: "leaf"},
				DNSNames:     tc.dns,
				IPAddresses:  tc.ips,
				NotBefore:    now.Add(-time.Minute),
				NotAfter:     now.Add(time.Hour),
				KeyUsage:     x509.KeyUsageDigitalSignature,
				ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}, inter, &leafKey.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := x509.ParseCertificate(der)
			if err != nil {
				t.Fatal(err)
			}
			_, err = leaf.Verify(x509.VerifyOptions{Roots: f.orgRoots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
			if (err == nil) != tc.valid {
				t.Fatalf("verify err = %v, want valid=%v", err, tc.valid)
			}
		})
	}
}
