package ingress

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/clientip"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

type AccessRecorder interface {
	Record(e *store.AccessEvent)
}

type accessKey struct{}

type accessRecord struct {
	principal *identity.Principal
	decision  *authz.Decision
	forwarded bool
	upgraded  bool
	bytesIn   atomic.Int64
	bytesOut  atomic.Int64
}

func accessFrom(r *http.Request) *accessRecord {
	rec, _ := r.Context().Value(accessKey{}).(*accessRecord)
	return rec
}

func (rec *accessRecord) setDecision(principal *identity.Principal, d authz.Decision) {
	if rec == nil {
		return
	}
	rec.principal = principal
	rec.decision = &d
}

func (rec *accessRecord) markForwarded() {
	if rec != nil {
		rec.forwarded = true
	}
}

func (rec *accessRecord) markUpgraded(in, out int64) {
	if rec == nil {
		return
	}
	rec.upgraded = true
	rec.bytesIn.Store(in)
	rec.bytesOut.Store(out)
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	bytes   atomic.Int64
	firstAt time.Time
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 && code >= 200 {
		s.status = code
		s.firstAt = time.Now()
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
		s.firstAt = time.Now()
	}
	n, err := s.ResponseWriter.Write(p)
	s.bytes.Add(int64(n))
	return n, err
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	if s.status == 0 {
		s.status = http.StatusSwitchingProtocols
		s.firstAt = time.Now()
	}
	return hj.Hijack()
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

type countingBody struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (i *Ingress) beginAccess(w http.ResponseWriter, r *http.Request) (*statusWriter, *http.Request, *accessRecord) {
	rec := &accessRecord{}
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = countingBody{ReadCloser: r.Body, n: &rec.bytesIn}
	}
	return &statusWriter{ResponseWriter: w}, r.WithContext(context.WithValue(r.Context(), accessKey{}, rec)), rec
}

func (i *Ingress) finishAccess(sw *statusWriter, r *http.Request, ep *store.Endpoint, rec *accessRecord, start time.Time) {
	if i.access == nil {
		return
	}
	e := &store.AccessEvent{
		OrgID:      ep.OrgID,
		At:         start.UTC(),
		Source:     store.AccessSourceIngress,
		AgentID:    ep.AgentID,
		EndpointID: ep.ID,
		Method:     r.Method,
		Path:       r.URL.Path,
		Status:     sw.status,
		Protocol:   "http",
		BytesIn:    rec.bytesIn.Load(),
		BytesOut:   sw.bytes.Load(),
		DurationMs: time.Since(start).Milliseconds(),
	}
	if ip := clientip.Resolve(r, i.trusted); ip != nil {
		e.ClientIP = ip.String()
	}
	if !sw.firstAt.IsZero() {
		e.LatencyMs = sw.firstAt.Sub(start).Milliseconds()
	}
	if rec.upgraded {
		e.Protocol = "upgrade"
		e.BytesOut = rec.bytesOut.Load()
	}
	if rec.principal != nil {
		e.PrincipalType = string(rec.principal.Ref.Type)
		e.PrincipalID = rec.principal.Ref.ID
		e.SessionID = rec.principal.SessionID
	}
	e.Decision, e.Reason = store.AccessDeny, statusReason(sw.status)
	if rec.forwarded {
		e.Decision, e.Reason = store.AccessAllow, string(authz.ReasonAllow)
	}
	if d := rec.decision; d != nil {
		if !rec.forwarded && d.Reason != "" {
			e.Reason = string(d.Reason)
		}
		for _, p := range d.Policies {
			e.PolicyIDs = append(e.PolicyIDs, p.ID)
		}
	}
	i.access.Record(e)
}

func statusReason(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthenticated"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return string(authz.ReasonUnavailable)
	case http.StatusFound, http.StatusSeeOther, http.StatusPermanentRedirect, http.StatusTemporaryRedirect:
		return "redirect"
	case 0:
		return "no_response"
	}
	return "gate"
}
