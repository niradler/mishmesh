package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/clientip"
	"github.com/mishmesh/mishmesh/internal/ratelimit"
)

var (
	ipLimit    = ratelimit.Limit{Requests: 1, Period: 3 * time.Second, Burst: 20}
	emailLimit = ratelimit.Limit{Requests: 1, Period: 12 * time.Second, Burst: 5}
)

func (a *API) SetLimiter(l ratelimit.Limiter) {
	a.limiter = l
}

func (a *API) SetTrustedProxies(nets []*net.IPNet) {
	a.trustedProxies = nets
}

func (a *API) authRateAllowed(r *http.Request, email string) bool {
	ctx := r.Context()
	ip := clientip.Resolve(r, a.trustedProxies)
	ipKey := "cp:ip:" + r.RemoteAddr
	if ip != nil {
		ipKey = "cp:ip:" + ip.String()
	}
	if !a.limiter.Allow(ctx, ipKey, ipLimit).Allowed {
		return false
	}
	if email == "" {
		return true
	}
	return a.limiter.Allow(ctx, "cp:email:"+hashKey(email), emailLimit).Allowed
}

func hashKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
