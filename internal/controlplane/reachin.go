package controlplane

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
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
	meta := map[string]string{"target": req.Target}
	if req.TLS {
		meta["tls"] = "true"
	}
	if req.Insecure {
		meta["insecure"] = "true"
	}
	stream, ok := a.openReachStream(w, r, agentID, store.KindHTTP, meta)
	if !ok {
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
	br := bufio.NewReader(stream)
	if reason, failed := tunnel.ReadStreamError(br); failed {
		writeError(w, http.StatusBadGateway, "agent could not reach target: "+reason)
		return
	}
	resp, err := http.ReadResponse(br, outReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, "tunnel read failed")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	a.audit(r, "reachin.http", agentID, req.Target)
	writeJSON(w, http.StatusOK, reachInResponse{Status: resp.StatusCode, Headers: resp.Header, Body: string(body)})
}

func (a *API) authorizeReachAgent(w http.ResponseWriter, r *http.Request, agentID string) bool {
	ag, err := a.data.GetAgent(r.Context(), agentID)
	if a.handleErr(w, err) {
		return false
	}
	if ag.OrgID != a.orgScope(r) {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

func (a *API) openReachStream(w http.ResponseWriter, r *http.Request, agentID, kind string, meta map[string]string) (net.Conn, bool) {
	if !a.authorizeReachAgent(w, r, agentID) {
		return nil, false
	}
	conn, ok := a.conns.GetAgent(agentID)
	if !ok {
		writeError(w, http.StatusBadGateway, "agent offline")
		return nil, false
	}
	stream, err := conn.OpenStream(r.Context(), "", kind, meta)
	if err != nil {
		writeError(w, http.StatusBadGateway, "open stream failed")
		return nil, false
	}
	return stream, true
}

const reachStreamProtocol = "mishmesh-stream"

type hijackedConn struct {
	net.Conn
	reader io.Reader
}

func (c *hijackedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func (c *hijackedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}

func (a *API) reachInStreamHandler(w http.ResponseWriter, r *http.Request) {
	if !a.authorizeReachAgent(w, r, r.PathValue("agent_id")) {
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), reachStreamProtocol) || !headerHasToken(r.Header, "Connection", "upgrade") {
		writeError(w, http.StatusBadRequest, "Upgrade: "+reachStreamProtocol+" required")
		return
	}
	q := r.URL.Query()
	target := q.Get("target")
	if target == "" {
		writeError(w, http.StatusBadRequest, "target required")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream upgrade unsupported")
		return
	}
	meta := map[string]string{"target": target}
	if q.Get("tls") == "true" {
		meta["tls"] = "true"
	}
	if q.Get("insecure") == "true" {
		meta["insecure"] = "true"
	}
	agentID := r.PathValue("agent_id")
	stream, ok := a.openReachStream(w, r, agentID, store.KindTCP, meta)
	if !ok {
		return
	}
	defer stream.Close()

	client, rw, err := hj.Hijack()
	if err != nil {
		a.log.Warn("reach-in hijack failed", "err", err)
		return
	}
	defer client.Close()
	const switching = "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + reachStreamProtocol + "\r\n\r\n"
	if _, err := rw.WriteString(switching); err != nil {
		return
	}
	if err := rw.Flush(); err != nil {
		return
	}
	a.audit(r, "reachin.stream", agentID, target)
	up, down := tunnel.Splice(&hijackedConn{Conn: client, reader: rw.Reader}, stream)
	a.conns.AddUsage(a.orgScope(r), up+down)
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}
