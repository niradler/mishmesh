package ingress

import (
	"crypto/subtle"
	"crypto/x509"
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
	oidc    *oidcGate
	trusted []*net.IPNet
	limiter ratelimit.Limiter
}

func applyPolicyGate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint, deps gateDeps) bool {
	if ep == nil || ep.Policy == nil {
		return true
	}
	p := ep.Policy

	if p.ForceHTTPS && !requestIsHTTPS(r) {
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
		if !checkBasicAuth(r, p.BasicAuthUser, p.BasicAuthHash) {
			w.Header().Set("WWW-Authenticate", `Basic realm="mishmesh"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
	}

	if p.MTLS != nil {
		if !checkMTLS(r, p.MTLS) {
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
			if slices.Contains(names, strings.TrimSpace(name)) {
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

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
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

func checkMTLS(r *http.Request, m *store.MTLSConfig) bool {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(m.ClientCAPEM)) {
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

func checkBasicAuth(r *http.Request, user, hash string) bool {
	u, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 {
		return false
	}
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil
}
