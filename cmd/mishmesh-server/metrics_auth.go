package main

import (
	"net"
	"net/http"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/token"
)

var forwardingHeaders = []string{"X-Forwarded-For", "Forwarded", "X-Real-IP", "CF-Connecting-IP"}

func isDirectLoopback(r *http.Request) bool {
	for _, h := range forwardingHeaders {
		if len(r.Header.Values(h)) > 0 {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func metricsAuth(metricsToken, apiToken string, next http.Handler) http.Handler {
	required := metricsToken
	if required == "" {
		required = apiToken
	}
	if required == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isDirectLoopback(r) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !store.ConstantTimeEqualHash(token.Bearer(r.Header.Get("Authorization")), required) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
