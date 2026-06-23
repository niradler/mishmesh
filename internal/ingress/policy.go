package ingress

import (
	"crypto/subtle"
	"crypto/x509"
	"net"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/mishmesh/mishmesh/internal/store"
)

func applyPolicyGate(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) bool {
	if ep == nil || ep.Policy == nil {
		return true
	}
	p := ep.Policy

	if p.ForceHTTPS && !requestIsHTTPS(r) {
		target := "https://" + r.Host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
		return false
	}

	ip := clientIP(r)
	if len(p.IPDeny) > 0 && cidrMatch(p.IPDeny, ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if len(p.IPAllow) > 0 && !cidrMatch(p.IPAllow, ip) {
		http.Error(w, "forbidden", http.StatusForbidden)
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
		http.Error(w, "endpoint oidc auth not available on this build", http.StatusServiceUnavailable)
		return false
	}

	if p.MaxBodyBytes > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, p.MaxBodyBytes)
	}
	return true
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

func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
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
	if resp.Header.Get("Content-Encoding") != "" {
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
