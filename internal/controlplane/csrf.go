package controlplane

import (
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

var (
	errCSRFContentType = errors.New("content type must be application/json")
	errCSRFOrigin      = errors.New("cross-origin request rejected")
)

func (a *API) SetAllowedOrigins(origins []string) {
	hosts := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		if h := originHost(strings.TrimSpace(o)); h != "" {
			hosts[h] = struct{}{}
		}
	}
	a.allowedOrigins = hosts
}

func originHost(origin string) string {
	if origin == "" {
		return ""
	}
	if !strings.Contains(origin, "://") {
		return strings.ToLower(origin)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func (a *API) trustedHost(r *http.Request, host string) bool {
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	if host == strings.ToLower(r.Host) {
		return true
	}
	if a.baseDomain != "" && host == strings.ToLower(a.baseDomain) {
		return true
	}
	if a.auth != nil && a.auth.redirectURL != "" && host == originHost(a.auth.redirectURL) {
		return true
	}
	_, ok := a.allowedOrigins[host]
	return ok
}

func (a *API) csrfCheck(r *http.Request) error {
	if isSafeMethod(r.Method) {
		return nil
	}
	if r.ContentLength != 0 {
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			return errCSRFContentType
		}
	}
	site := r.Header.Get("Sec-Fetch-Site")
	if site == "cross-site" {
		return errCSRFOrigin
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		if site == "same-site" {
			return errCSRFOrigin
		}
		return nil
	}
	if !a.trustedHost(r, originHost(origin)) {
		return errCSRFOrigin
	}
	return nil
}

func (a *API) csrfGuarded(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.csrfCheck(r); err != nil {
			writeCSRFError(w, err)
			return
		}
		h(w, r)
	}
}

func writeCSRFError(w http.ResponseWriter, err error) {
	status := http.StatusForbidden
	if errors.Is(err, errCSRFContentType) {
		status = http.StatusUnsupportedMediaType
	}
	writeError(w, status, err.Error())
}
