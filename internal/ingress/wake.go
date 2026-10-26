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
	VisibleTo(orgID, id string) bool
	ServiceState(id string) (name, state string, ok bool)
	EnsureRunning(ctx context.Context, id, readyAddr string) error
	StartAsync(id, readyAddr string)
	Acquire(id string) func()
}

const wakingRetrySeconds = "2"

func (i *Ingress) wakeService(w http.ResponseWriter, r *http.Request, ep *store.Endpoint) (release func(), proceed bool) {
	id := endpointServiceID(ep)
	if i.services == nil || id == "" {
		return func() {}, true
	}
	name, state, known := i.services.ServiceState(id)
	if !known || !i.services.VisibleTo(ep.OrgID, id) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		i.recordCode(http.StatusServiceUnavailable)
		i.log.Warn("endpoint references unknown service", "endpoint_id", ep.ID, "service_id", id)
		return nil, false
	}
	if state != services.StateRunning && state != services.StateUnhealthy {
		ready := readyAddr(ep)
		if wantsWakingPage(r) {
			i.services.StartAsync(id, ready)
			writeWakingPage(w, name)
			i.recordCode(http.StatusServiceUnavailable)
			return nil, false
		}
		if err := i.services.EnsureRunning(r.Context(), id, ready); err != nil {
			w.Header().Set("Retry-After", wakingRetrySeconds)
			http.Error(w, "service is not available yet, retry shortly", http.StatusServiceUnavailable)
			i.recordCode(http.StatusServiceUnavailable)
			i.log.Warn("service wake failed", "endpoint_id", ep.ID, "service", name, "service_id", id, "err", err)
			return nil, false
		}
	}
	return i.services.Acquire(id), true
}

func endpointServiceID(ep *store.Endpoint) string {
	if ep == nil || ep.Policy == nil {
		return ""
	}
	return ep.Policy.ServiceID
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
