package main

import (
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

func metricsAuth(metricsToken, apiToken string, next http.Handler) http.Handler {
	required := metricsToken
	if required == "" {
		required = apiToken
	}
	if required == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) || !store.ConstantTimeEqualHash(strings.TrimSpace(h[len(prefix):]), required) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
