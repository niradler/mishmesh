package ingress

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
)

type ServiceWaker interface {
	ServiceState(name string) (string, bool)
	EnsureRunning(ctx context.Context, name, readyAddr string) error
	StartAsync(name, readyAddr string)
	Acquire(name string) func()
}

const wakingRetrySeconds = "2"

func (i *Ingress) wakeService(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) (release func(), proceed bool) {
	name := endpointService(ep)
	if i.services == nil || name == "" {
		return func() {}, true
	}
	state, known := i.services.ServiceState(name)
	if !known {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		i.recordCode(http.StatusServiceUnavailable)
		i.log.Warn("endpoint references unknown service", "endpoint_id", ep.ID, "service", name)
		return nil, false
	}
	if state != services.StateRunning {
		ready := readyAddr(ep)
		if wantsWakingPage(r) {
			i.services.StartAsync(name, ready)
			writeWakingPage(w, name)
			i.recordCode(http.StatusServiceUnavailable)
			return nil, false
		}
		if err := i.services.EnsureRunning(r.Context(), name, ready); err != nil {
			w.Header().Set("Retry-After", wakingRetrySeconds)
			http.Error(w, "service is not available yet, retry shortly", http.StatusServiceUnavailable)
			i.recordCode(http.StatusServiceUnavailable)
			i.log.Warn("service wake failed", "endpoint_id", ep.ID, "service", name, "err", err)
			return nil, false
		}
	}
	return i.services.Acquire(name), true
}

func endpointService(ep *store.Endpoint) string {
	if ep == nil || ep.Policy == nil {
		return ""
	}
	return ep.Policy.Service
}

func readyAddr(ep *store.Endpoint) string {
	if ep.Policy == nil || len(ep.Policy.ProxyTargets) == 0 {
		return ""
	}
	return ep.Policy.ProxyTargets[0]
}

func wantsWakingPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if isUpgrade(r) {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

func writeWakingPage(w http.ResponseWriter, name string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Retry-After", wakingRetrySeconds)
	w.WriteHeader(http.StatusServiceUnavailable)
	escaped := html.EscapeString(name)
	fmt.Fprintf(w, wakingPage, wakingRetrySeconds, escaped, escaped)
}

const wakingPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="%s">
<title>Starting %s…</title>
<style>
:root{color-scheme:light dark}
body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,sans-serif;background:Canvas;color:CanvasText}
main{text-align:center;padding:16px}
.spinner{width:32px;height:32px;margin:0 auto 16px;border:3px solid color-mix(in srgb,CanvasText 20%%,transparent);border-top-color:CanvasText;border-radius:50%%;animation:spin 1s linear infinite}
@keyframes spin{to{transform:rotate(360deg)}}
p{opacity:.7;margin:4px 0 0}
</style>
</head>
<body>
<main>
<div class="spinner"></div>
<strong>Starting %s…</strong>
<p>This service was asleep. The page reloads when it is ready.</p>
</main>
</body>
</html>
`
