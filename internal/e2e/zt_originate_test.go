package e2e

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/agent"
)

type upstreamRecorder struct {
	srv  *httptest.Server
	hits atomic.Int64
	mu   sync.Mutex
	last http.Header
}

func newUpstream(t *testing.T, useTLS bool) *upstreamRecorder {
	t.Helper()
	return newUpstreamWithCert(t, useTLS, nil)
}

func newSelfSignedUpstream(t *testing.T) *upstreamRecorder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "untrusted.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return newUpstreamWithCert(t, false, &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key})
}

func newUpstreamWithCert(t *testing.T, useTLS bool, ownCert *tls.Certificate) *upstreamRecorder {
	t.Helper()
	u := &upstreamRecorder{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.mu.Lock()
		u.last = r.Header.Clone()
		u.mu.Unlock()
		_, _ = fmt.Fprintf(w, "%s %s", r.Method, r.URL.Path)
	})
	if useTLS {
		u.srv = httptest.NewTLSServer(h)
	} else if ownCert != nil {
		u.srv = httptest.NewUnstartedServer(h)
		u.srv.TLS = &tls.Config{Certificates: []tls.Certificate{*ownCert}}
		u.srv.StartTLS()
	} else {
		u.srv = httptest.NewServer(h)
	}
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstreamRecorder) port() string {
	_, port, _ := net.SplitHostPort(u.srv.Listener.Addr().String())
	return port
}

func (u *upstreamRecorder) header(name string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last.Get(name)
}

type originResponse struct {
	status int
	body   string
}

func originDo(t *testing.T, method, rawURL string, header http.Header) originResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	for {
		req, err := http.NewRequest(method, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header[k] = v
		}
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
				return originResponse{resp.StatusCode, string(body)}
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("%s %s: %v", method, rawURL, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func absoluteFormDo(t *testing.T, proxyAddr, target string) originResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", proxyAddr)
		if err == nil {
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			host := strings.SplitN(strings.SplitN(target, "://", 2)[1], "/", 2)[0]
			fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer client-value\r\nConnection: close\r\n\r\n", target, host)
			resp, rerr := http.ReadResponse(bufio.NewReader(c), nil)
			if rerr == nil {
				body, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				_ = c.Close()
				if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
					return originResponse{resp.StatusCode, string(body)}
				}
			} else {
				_ = c.Close()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy %s never answered %s: %v", proxyAddr, target, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestOriginateInjectsScopedSealedCredential(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner@originate.io", "")

	api := newUpstream(t, true)
	untrusted := newSelfSignedUpstream(t)
	plain := newUpstream(t, false)
	roots := x509.NewCertPool()
	roots.AddCert(api.srv.Certificate())

	scopedID, scopedToken := owner.createAgent("scoped")
	_, outsiderToken := owner.createAgent("outsider")

	owner.putPolicy(fmt.Sprintf(`
permit (principal, action == Action::"egress:connect", resource) when { resource.port == %[1]s || resource.port == %[2]s || resource.port == %[3]s };
@credential("api-key")
permit (principal, action == Action::"egress:request", resource) when { resource.port == %[1]s && context.method == "GET" && context.path like "/v1/*" };
permit (principal, action == Action::"egress:request", resource) when { resource.port == %[2]s };
@credential("api-key")
permit (principal, action == Action::"egress:request", resource) when { resource.port == %[3]s && context.path like "/inject/*" };
permit (principal, action == Action::"egress:request", resource) when { resource.port == %[3]s && context.path like "/open/*" };
`, api.port(), untrusted.port(), plain.port()))
	owner.putSecret("api-secret", "s1")
	owner.mustJSON(http.MethodPut, "/api/v1/egress/credentials/api-key", map[string]any{
		"header": "Authorization", "prefix": "Bearer ", "secret": "api-secret", "agent_ids": []string{scopedID}, "destinations": []string{"127.0.0.1"},
	}, http.StatusOK, nil)

	runOriginAgent := func(token string) (proxyAddr, apiListen, untrustedListen string) {
		proxyAddr, apiListen, untrustedListen = freeAddr(t, "tcp"), freeAddr(t, "tcp"), freeAddr(t, "tcp")
		s.runAgent(agent.Options{Token: token, UpstreamRoots: roots, Egress: &agent.EgressSpec{
			Listen: proxyAddr,
			Originate: []agent.OriginateSpec{
				{Name: "api", Listen: apiListen, Upstream: api.srv.URL},
				{Name: "untrusted", Listen: untrustedListen, Upstream: untrusted.srv.URL},
			},
		}})
		return proxyAddr, apiListen, untrustedListen
	}
	proxyAddr, apiListen, untrustedListen := runOriginAgent(scopedToken)
	clientAuth := http.Header{"Authorization": {"Bearer client-value"}}

	t.Run("injects the credential and strips the client value", func(t *testing.T) {
		r := originDo(t, http.MethodGet, "http://"+apiListen+"/v1/items", clientAuth)
		if r.status != http.StatusOK || r.body != "GET /v1/items" {
			t.Fatalf("got %d %q", r.status, r.body)
		}
		if got := api.header("Authorization"); got != "Bearer s1" {
			t.Fatalf("upstream Authorization = %q, want injected value", got)
		}
	})

	t.Run("method and path policy are enforced per request", func(t *testing.T) {
		before := api.hits.Load()
		for _, tc := range []struct{ method, path string }{{http.MethodPost, "/v1/items"}, {http.MethodGet, "/admin"}, {http.MethodGet, "/v1/../admin"}} {
			r := originDo(t, tc.method, "http://"+apiListen+tc.path, nil)
			if r.status != http.StatusForbidden && r.status != http.StatusBadRequest {
				t.Fatalf("%s %s = %d %q, want refusal", tc.method, tc.path, r.status, r.body)
			}
		}
		if api.hits.Load() != before {
			t.Fatal("a refused request reached the upstream")
		}
	})

	t.Run("absolute-form https on the proxy listener is originated", func(t *testing.T) {
		r := absoluteFormDo(t, proxyAddr, api.srv.URL+"/v1/abs")
		if r.status != http.StatusOK || r.body != "GET /v1/abs" {
			t.Fatalf("got %d %q", r.status, r.body)
		}
		if got := api.header("Authorization"); got != "Bearer s1" {
			t.Fatalf("upstream Authorization = %q", got)
		}
	})

	t.Run("secret update applies on the next request after the flush", func(t *testing.T) {
		owner.putSecret("api-secret", "s2")
		deadline := time.Now().Add(5 * time.Second)
		for {
			r := originDo(t, http.MethodGet, "http://"+apiListen+"/v1/items", nil)
			if r.status == http.StatusOK && api.header("Authorization") == "Bearer s2" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("rotated secret never injected: %d %q, upstream saw %q", r.status, r.body, api.header("Authorization"))
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("upstream certificate failure is a 502", func(t *testing.T) {
		r := originDo(t, http.MethodGet, "http://"+untrustedListen+"/any", nil)
		if r.status != http.StatusBadGateway || !strings.Contains(r.body, "certificate verification failed") {
			t.Fatalf("got %d %q", r.status, r.body)
		}
		if untrusted.hits.Load() != 0 {
			t.Fatal("request reached an unverified upstream")
		}
	})

	t.Run("injection onto plaintext http is refused", func(t *testing.T) {
		r := absoluteFormDo(t, proxyAddr, plain.srv.URL+"/inject/x")
		if r.status != http.StatusForbidden || !strings.Contains(r.body, "credential_requires_tls") {
			t.Fatalf("got %d %q", r.status, r.body)
		}
		if plain.hits.Load() != 0 {
			t.Fatal("credentialed plaintext request reached the upstream")
		}
		r = absoluteFormDo(t, proxyAddr, plain.srv.URL+"/open/x")
		if r.status != http.StatusOK || plain.header("Authorization") != "Bearer client-value" {
			t.Fatalf("plain passthrough = %d %q, upstream Authorization %q", r.status, r.body, plain.header("Authorization"))
		}
	})

	t.Run("an agent outside agent_ids gets no credential", func(t *testing.T) {
		_, outsiderAPI, _ := runOriginAgent(outsiderToken)
		before := api.hits.Load()
		r := originDo(t, http.MethodGet, "http://"+outsiderAPI+"/v1/items", clientAuth)
		if r.status != http.StatusForbidden || !strings.Contains(r.body, "credential_unavailable") {
			t.Fatalf("got %d %q", r.status, r.body)
		}
		if api.hits.Load() != before {
			t.Fatal("out-of-scope agent request reached the upstream")
		}
	})
}

func TestOriginateRequiresEgressRequestPermit(t *testing.T) {
	s := newZTStack(t, nil)
	owner := s.register("owner2@originate.io", "")
	api := newUpstream(t, true)
	roots := x509.NewCertPool()
	roots.AddCert(api.srv.Certificate())
	_, token := owner.createAgent("a")
	owner.putPolicy(fmt.Sprintf(`permit (principal, action == Action::"egress:connect", resource) when { resource.port == %s };`, api.port()))
	listen := freeAddr(t, "tcp")
	s.runAgent(agent.Options{Token: token, UpstreamRoots: roots, Egress: &agent.EgressSpec{
		Originate: []agent.OriginateSpec{{Name: "api", Listen: listen, Upstream: api.srv.URL}},
	}})
	r := originDo(t, http.MethodGet, "http://"+listen+"/x", nil)
	if r.status != http.StatusForbidden || api.hits.Load() != 0 {
		t.Fatalf("got %d %q with %d upstream hits, want 403 and none", r.status, r.body, api.hits.Load())
	}
}
