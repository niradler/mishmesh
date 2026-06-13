package controlplane

import (
	"bufio"
	"io"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

func (a *API) SetReachInEnabled(enabled bool) {
	a.reachInEnabled = enabled
}

type reachInRequest struct {
	Target   string            `json:"target"`
	TLS      bool              `json:"tls"`
	Insecure bool              `json:"insecure"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
}

type reachInResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

func (a *API) reachInHTTPHandler(w http.ResponseWriter, r *http.Request) {
	var req reachInRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Target == "" {
		writeError(w, http.StatusBadRequest, "target required")
		return
	}
	agentID := r.PathValue("agent_id")
	ag, err := a.data.GetAgent(r.Context(), agentID)
	if a.handleErr(w, err) {
		return
	}
	if ag.OrgID != a.orgScope(r) {
		writeError(w, http.StatusForbidden, "agent not in org")
		return
	}
	conn, ok := a.conns.GetAgent(agentID)
	if !ok {
		writeError(w, http.StatusBadGateway, "agent offline")
		return
	}
	meta := map[string]string{"target": req.Target}
	if req.TLS {
		meta["tls"] = "true"
	}
	if req.Insecure {
		meta["insecure"] = "true"
	}
	stream, err := conn.OpenStream(r.Context(), "", store.KindHTTP, meta)
	if err != nil {
		writeError(w, http.StatusBadGateway, "open stream failed")
		return
	}
	defer stream.Close()

	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	path := req.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	outReq, err := http.NewRequestWithContext(r.Context(), method, "http://"+req.Target+path, strings.NewReader(req.Body))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	for k, v := range req.Headers {
		outReq.Header.Set(k, v)
	}
	if err := outReq.Write(stream); err != nil {
		writeError(w, http.StatusBadGateway, "tunnel write failed")
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), outReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tunnel read failed")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	a.audit(r, "reachin.http", agentID, req.Target)
	writeJSON(w, http.StatusOK, reachInResponse{Status: resp.StatusCode, Headers: resp.Header, Body: string(body)})
}
