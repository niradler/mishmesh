package controlplane

import "github.com/mishmesh/mishmesh/internal/connect/proxy"

func (a *API) SetProxyGuard(g *proxy.Guard) {
	a.proxyGuard = g
}

func (a *API) guardProxyTarget(target string) error {
	g := a.proxyGuard
	if g == nil {
		g = proxy.DefaultGuard()
	}
	return g.ValidateTarget(target)
}
