package ingress

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/clientip"
	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/store"
)

type gateDeps struct {
	oidc         *oidcGate
	trusted      []*net.IPNet
	limiter      ratelimit.Limiter
	basic        *ttlCache[struct{}]
	basicLockout *ttlCache[time.Time]
	caPools      *ttlCache[*x509.CertPool]
}

func applyPolicyGate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint, deps gateDeps) bool {
	if ep == nil || ep.Policy == nil {
		return true
	}
	p := ep.Policy

	if p.ForceHTTPS && !requestIsHTTPS(r, deps.trusted) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return false
	}

	ip := clientip.Resolve(r, deps.trusted)
	if len(p.IPDeny) > 0 && cidrMatch(p.IPDeny, ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if len(p.IPAllow) > 0 && !cidrMatch(p.IPAllow, ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}

	if !allowRate(w, r, ep, ip, deps.limiter) {
		return false
	}

	if p.BasicAuthUser != "" {
		key := basicAuthVerifyKey(ep.ID, ip)
		var lockedFor time.Duration
		locked := func() bool {
			until, ok := deps.basicLockout.get(key)
			lockedFor = time.Until(until)
			return ok && lockedFor > 0
		}
		switch checkBasicAuth(r, ep.ID, p.BasicAuthUser, p.BasicAuthHash, deps.basic, locked) {
		case basicAuthThrottled:
			writeBasicAuthThrottled(w, lockedFor)
			return false
		case basicAuthDenied:
			if deps.limiter != nil {
				if d := deps.limiter.Allow(r.Context(), key, ratelimit.Limit{Requests: basicAuthVerifyRequests, Period: basicAuthVerifyPeriod}); !d.Allowed {
					deps.basicLockout.put(key, time.Now().Add(d.RetryAfter))
					writeBasicAuthThrottled(w, d.RetryAfter)
					return false
				}
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="mishmesh"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
	}

	if p.MTLS != nil {
		if !checkMTLS(r, p.MTLS, deps.caPools) {
			http.Error(w, "client certificate required", http.StatusForbidden)
			return false
		}
	}

	if p.OIDC != nil {
		if deps.oidc == nil {
			http.Error(w, "endpoint oidc auth not configured", http.StatusServiceUnavailable)
			return false
		}
		if !deps.oidc.authenticate(w, r, ep) {
			return false
		}
	}

	if p.MaxBodyBytes > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, p.MaxBodyBytes)
	}
	return true
}

func stripGateCredentials(h http.Header, ep *store.Endpoint) {
	stripCookies(h, oidcSessionCookie, oidcStateCookie)
	if ep != nil && ep.Policy != nil && ep.Policy.BasicAuthUser != "" {
		h.Del("Authorization")
	}
}

func stripCookies(h http.Header, names ...string) {
	stripCookiesMatching(h, func(name string) bool { return slices.Contains(names, name) })
}

func stripCookiesMatching(h http.Header, drop func(name string) bool) {
	values := h.Values("Cookie")
	if len(values) == 0 {
		return
	}
	var kept []string
	for _, line := range values {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, _, _ := strings.Cut(part, "=")
			if drop(strings.TrimSpace(name)) {
				continue
			}
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		h.Del("Cookie")
		return
	}
	h.Set("Cookie", strings.Join(kept, "; "))
}

func applyRequestPolicy(outReq *http.Request, ep *store.Endpoint) {
	if ep == nil || ep.Policy == nil {
		return
	}
	for _, name := range ep.Policy.RequestHeadersRemove {
		outReq.Header.Del(name)
	}
	for k, v := range ep.Policy.RequestHeadersAdd {
		outReq.Header.Set(k, v)
	}
}

func applyResponsePolicy(h http.Header, ep *store.Endpoint) {
	if ep == nil || ep.Policy == nil {
		return
	}
	for _, name := range ep.Policy.ResponseHeadersRemove {
		h.Del(name)
	}
	for k, v := range ep.Policy.ResponseHeadersAdd {
		h.Set(k, v)
	}
}

func requestIsHTTPS(r *http.Request, trusted []*net.IPNet) bool {
	return clientip.IsHTTPS(r, trusted)
}

func allowRate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint, ip net.IP, limiter ratelimit.Limiter) bool {
	rl := ep.Policy.RateLimit
	if rl == nil || limiter == nil {
		return true
	}
	limit := ratelimit.Limit{
		Requests: rl.Requests,
		Period:   time.Duration(rl.PeriodSeconds) * time.Second,
		Burst:    rl.Burst,
	}
	d := limiter.Allow(r.Context(), rateLimitKey(ep.ID, rl, ip), limit)
	if d.Allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(d.RetryAfter)))
	http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
	return false
}

func rateLimitKey(endpointID string, rl *store.RateLimit, ip net.IP) string {
	if rl.PerEndpoint() || ip == nil {
		return "ep:" + endpointID
	}
	return "ip:" + endpointID + ":" + rateLimitIPKey(ip)
}

func rateLimitIPKey(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

func cidrMatch(cidrs []string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			if pip := net.ParseIP(c); pip != nil && pip.Equal(ip) {
				return true
			}
			continue
		}
		if _, network, err := net.ParseCIDR(c); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func shouldCompress(ep *store.Endpoint, r *http.Request, resp *http.Response) bool {
	if ep == nil || ep.Policy == nil || !ep.Policy.Compression {
		return false
	}
	if resp.Header.Get("Content-Encoding") != "" || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || r.Method == http.MethodHead {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip")
}

const clientCAPoolCacheTTL = 10 * time.Minute

func clientCAPool(cache *ttlCache[*x509.CertPool], caPEM string) *x509.CertPool {
	if roots, ok := cache.get(caPEM); ok {
		return roots
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(caPEM)) {
		return nil
	}
	cache.put(caPEM, roots)
	return roots
}

func checkMTLS(r *http.Request, m *store.MTLSConfig, pools *ttlCache[*x509.CertPool]) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	roots := clientCAPool(pools, m.ClientCAPEM)
	if roots == nil {
		return false
	}
	leaf := r.TLS.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range r.TLS.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: inter,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return false
	}
	if len(m.AllowedCNs) == 0 {
		return true
	}
	for _, cn := range m.AllowedCNs {
		if subtle.ConstantTimeCompare([]byte(cn), []byte(leaf.Subject.CommonName)) == 1 {
			return true
		}
	}
	return false
}

const basicAuthCacheTTL = time.Minute

type basicAuthResult int

const (
	basicAuthDenied basicAuthResult = iota
	basicAuthOK
	basicAuthThrottled
)

const (
	basicAuthVerifyRequests = 10
	basicAuthVerifyPeriod   = time.Minute
)

func basicAuthVerifyKey(epID string, ip net.IP) string {
	if ip == nil {
		return "basic:" + epID + ":unknown"
	}
	return "basic:" + epID + ":" + rateLimitIPKey(ip)
}

func writeBasicAuthThrottled(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(retryAfter)))
	http.Error(w, "too many authentication attempts", http.StatusTooManyRequests)
}

func checkBasicAuth(r *http.Request, epID, user, hash string, cache *ttlCache[struct{}], locked func() bool) basicAuthResult {
	u, pass, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 || hash == "" {
		return basicAuthDenied
	}
	sum := sha256.Sum256([]byte(hash + "|" + u + ":" + pass))
	key := epID + "|" + hex.EncodeToString(sum[:])
	if _, hit := cache.get(key); hit {
		return basicAuthOK
	}
	if locked != nil && locked() {
		return basicAuthThrottled
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil {
		return basicAuthDenied
	}
	cache.put(key, struct{}{})
	return basicAuthOK
}
