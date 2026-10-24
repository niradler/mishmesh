package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/credseal"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

const (
	decisionTimeout    = 5 * time.Second
	maxCachedDecisions = 4096
)

var (
	errDecisionLost        = errors.New("decision lost: tunnel session changed")
	errSessionUnbound      = errors.New("decision unavailable: session identity not acknowledged yet")
	errCredentialUnsealed  = errors.New("credential could not be opened")
	errCredentialMalformed = errors.New("credential header or value is not valid http")
)

type DeniedError struct {
	Reason string
}

func (e *DeniedError) Error() string {
	if e.Reason == "" {
		return "denied by policy"
	}
	return "denied by policy: " + e.Reason
}

type decisionKey struct {
	action    string
	connect   string
	kind      string
	host      string
	port      int
	method    string
	path      string
	tls       bool
	decrypted bool
	auth      [sha256.Size]byte
	exit      string
}

type injection struct {
	header string
	value  string
}

type cachedDecision struct {
	allowed bool
	reason  string
	inject  *injection
	expires time.Time
}

type egressRequest struct {
	host      string
	port      int
	method    string
	path      string
	tls       bool
	decrypted bool
	auth      string
	exit      string
}

type sessionBinding struct {
	orgID   string
	agentID string
}

type remoteDecider struct {
	now func() time.Time

	mu         sync.Mutex
	gen        uint64
	epoch      uint64
	ctrl       *tunnel.Control
	seal       *credseal.Key
	binding    sessionBinding
	scope      tunnel.DecisionScope
	scopeKnown bool
	nextID     uint64
	pending    map[uint64]chan tunnel.DecisionAnswer
	certs      map[uint64]chan tunnel.CertResponse
	cache      map[decisionKey]cachedDecision
	onFlush    func()
}

func newRemoteDecider(now func() time.Time) *remoteDecider {
	return &remoteDecider{
		now: now, pending: map[uint64]chan tunnel.DecisionAnswer{},
		certs: map[uint64]chan tunnel.CertResponse{}, cache: map[decisionKey]cachedDecision{},
	}
}

func (d *remoteDecider) attach(ctrl *tunnel.Control) uint64 {
	seal, err := credseal.NewKey()
	if err != nil {
		seal = nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resetLocked()
	d.gen++
	d.ctrl = ctrl
	d.seal = seal
	d.binding = sessionBinding{}
	return d.gen
}

func (d *remoteDecider) bind(gen uint64, orgID, agentID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen || orgID == "" || agentID == "" {
		return
	}
	d.binding = sessionBinding{orgID: orgID, agentID: agentID}
}

func (d *remoteDecider) detach(gen uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	d.resetLocked()
	d.ctrl = nil
	d.seal = nil
	d.binding = sessionBinding{}
}

func (d *remoteDecider) flush() {
	d.mu.Lock()
	d.epoch++
	clear(d.cache)
	d.scopeKnown = false
	hook := d.onFlush
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (d *remoteDecider) resetLocked() {
	d.epoch++
	clear(d.cache)
	d.scopeKnown = false
	for id, ch := range d.pending {
		close(ch)
		delete(d.pending, id)
	}
	for id, ch := range d.certs {
		close(ch)
		delete(d.certs, id)
	}
}

func (d *remoteDecider) deliver(gen uint64, answer tunnel.DecisionAnswer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	ch, ok := d.pending[answer.ID]
	if !ok {
		return
	}
	delete(d.pending, answer.ID)
	ch <- answer
}

func (d *remoteDecider) deliverCert(gen uint64, resp tunnel.CertResponse) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen {
		return
	}
	ch, ok := d.certs[resp.ID]
	if !ok {
		return
	}
	delete(d.certs, resp.ID)
	ch <- resp
}

func (d *remoteDecider) requestCert(ctx context.Context, gen uint64, csr []byte) (tunnel.CertResponse, error) {
	d.mu.Lock()
	if d.ctrl == nil || d.gen != gen {
		d.mu.Unlock()
		return tunnel.CertResponse{}, errDecisionLost
	}
	if d.binding == (sessionBinding{}) {
		d.mu.Unlock()
		return tunnel.CertResponse{}, errSessionUnbound
	}
	d.nextID++
	id, ctrl := d.nextID, d.ctrl
	ch := make(chan tunnel.CertResponse, 1)
	d.certs[id] = ch
	d.mu.Unlock()

	forget := func() {
		d.mu.Lock()
		if d.gen == gen {
			delete(d.certs, id)
		}
		d.mu.Unlock()
	}
	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgCertRequest, CertRequest: &tunnel.CertRequest{ID: id, CSR: csr}}); err != nil {
		forget()
		return tunnel.CertResponse{}, fmt.Errorf("send cert request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	select {
	case resp, ok := <-ch:
		if !ok {
			return tunnel.CertResponse{}, errDecisionLost
		}
		return resp, nil
	case <-ctx.Done():
		forget()
		return tunnel.CertResponse{}, fmt.Errorf("cert request: %w", ctx.Err())
	}
}

func (d *remoteDecider) check(ctx context.Context, connect, kind string) error {
	key := decisionKey{action: tunnel.DecisionActionConnect, connect: connect, kind: kind}
	d.mu.Lock()
	if c, ok := d.lookupLocked(key); ok {
		d.mu.Unlock()
		return verdict(c.allowed, c.reason)
	}
	d.mu.Unlock()
	answer, gen, epoch, err := d.ask(ctx, tunnel.DecisionQuery{Action: key.action, Connect: connect, Kind: kind}, false)
	if err != nil {
		return err
	}
	d.remember(gen, epoch, key, answer, nil)
	return verdict(answer.Allowed, answer.Reason)
}

func (d *remoteDecider) checkRequest(ctx context.Context, req egressRequest) (*injection, error) {
	d.mu.Lock()
	if c, ok := d.lookupLocked(d.requestKeyLocked(req, d.scope, d.scopeKnown)); ok {
		d.mu.Unlock()
		return c.inject, verdict(c.allowed, c.reason)
	}
	d.mu.Unlock()
	query := tunnel.DecisionQuery{
		Action: tunnel.DecisionActionEgressRequest,
		HTTP: &tunnel.HTTPQuery{
			Host: req.host, Port: req.port, Method: req.method, Path: req.path,
			TLS: req.tls, Decrypted: req.decrypted, Auth: req.auth, Exit: req.exit,
		},
	}
	answer, gen, epoch, err := d.ask(ctx, query, true)
	if err != nil {
		return nil, err
	}
	if !answer.Allowed {
		d.rememberRequest(gen, epoch, req, answer, nil)
		return nil, verdict(false, answer.Reason)
	}
	var inject *injection
	if answer.Credential != nil {
		if inject, err = d.open(gen, answer.Credential); err != nil {
			return nil, err
		}
		if !req.tls {
			return nil, &DeniedError{Reason: "credential_requires_tls"}
		}
	}
	d.rememberRequest(gen, epoch, req, answer, inject)
	return inject, nil
}

func (d *remoteDecider) ask(ctx context.Context, query tunnel.DecisionQuery, sealed bool) (tunnel.DecisionAnswer, uint64, uint64, error) {
	d.mu.Lock()
	if d.ctrl == nil {
		d.mu.Unlock()
		return tunnel.DecisionAnswer{}, 0, 0, errNotConnected
	}
	if sealed {
		if d.seal == nil || d.binding == (sessionBinding{}) {
			d.mu.Unlock()
			return tunnel.DecisionAnswer{}, 0, 0, errSessionUnbound
		}
		query.SealTo = d.seal.PublicKey()
	}
	d.nextID++
	query.ID = d.nextID
	gen, epoch, ctrl := d.gen, d.epoch, d.ctrl
	ch := make(chan tunnel.DecisionAnswer, 1)
	d.pending[query.ID] = ch
	d.mu.Unlock()

	if err := ctrl.Send(tunnel.ControlMessage{Type: tunnel.MsgDecisionRequest, Decision: &query}); err != nil {
		d.forget(gen, query.ID)
		return tunnel.DecisionAnswer{}, 0, 0, fmt.Errorf("send decision request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, decisionTimeout)
	defer cancel()
	select {
	case answer, ok := <-ch:
		if !ok {
			return tunnel.DecisionAnswer{}, 0, 0, errDecisionLost
		}
		return answer, gen, epoch, nil
	case <-ctx.Done():
		d.forget(gen, query.ID)
		return tunnel.DecisionAnswer{}, 0, 0, fmt.Errorf("decision request: %w", ctx.Err())
	}
}

func (d *remoteDecider) open(gen uint64, cred *tunnel.SealedCredential) (*injection, error) {
	d.mu.Lock()
	seal, binding, current := d.seal, d.binding, d.gen == gen
	d.mu.Unlock()
	if !current || seal == nil {
		return nil, errDecisionLost
	}
	header := http.CanonicalHeaderKey(strings.TrimSpace(cred.Header))
	if !validHeaderName(header) {
		return nil, errCredentialMalformed
	}
	value, err := seal.Open(credseal.Binding{OrgID: binding.orgID, AgentID: binding.agentID, Name: cred.Name}, cred.Sealed)
	if err != nil {
		return nil, errCredentialUnsealed
	}
	if !validHeaderValue(value) {
		clear(value)
		return nil, errCredentialMalformed
	}
	inject := &injection{header: header, value: string(value)}
	clear(value)
	return inject, nil
}

func (d *remoteDecider) forget(gen, id uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen == gen {
		delete(d.pending, id)
	}
}

func (d *remoteDecider) lookupLocked(key decisionKey) (cachedDecision, bool) {
	c, ok := d.cache[key]
	if !ok {
		return cachedDecision{}, false
	}
	if !d.now().Before(c.expires) {
		delete(d.cache, key)
		return cachedDecision{}, false
	}
	return c, true
}

func (d *remoteDecider) requestKeyLocked(req egressRequest, scope tunnel.DecisionScope, known bool) decisionKey {
	key := decisionKey{
		action: tunnel.DecisionActionEgressRequest,
		host:   strings.ToLower(req.host), port: req.port,
		tls: req.tls, decrypted: req.decrypted,
		auth: sha256.Sum256([]byte(req.auth)), exit: req.exit,
	}
	if !known || scope.Method {
		key.method = req.method
	}
	if !known || scope.Path {
		key.path = req.path
	}
	return key
}

func (d *remoteDecider) rememberRequest(gen, epoch uint64, req egressRequest, answer tunnel.DecisionAnswer, inject *injection) {
	scope := tunnel.DecisionScope{Path: true, Method: true}
	if answer.Scope != nil {
		scope = *answer.Scope
	}
	d.mu.Lock()
	if d.gen == gen && d.epoch == epoch {
		d.scope, d.scopeKnown = scope, true
	}
	key := d.requestKeyLocked(req, scope, true)
	d.mu.Unlock()
	d.remember(gen, epoch, key, answer, inject)
}

func (d *remoteDecider) remember(gen, epoch uint64, key decisionKey, answer tunnel.DecisionAnswer, inject *injection) {
	ttl := min(answer.TTLSeconds, tunnel.MaxDecisionTTLSeconds)
	if ttl <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gen != gen || d.epoch != epoch {
		return
	}
	if len(d.cache) >= maxCachedDecisions {
		d.pruneLocked()
	}
	d.cache[key] = cachedDecision{allowed: answer.Allowed, reason: answer.Reason, inject: inject, expires: d.now().Add(time.Duration(ttl) * time.Second)}
}

func (d *remoteDecider) pruneLocked() {
	now := d.now()
	for k, c := range d.cache {
		if !now.Before(c.expires) {
			delete(d.cache, k)
		}
	}
	if len(d.cache) >= maxCachedDecisions {
		clear(d.cache)
	}
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if r > 0x7e || r <= 0x20 || strings.ContainsRune("\"(),/:;<=>?@[\\]{}", r) {
			return false
		}
	}
	return true
}

func validHeaderValue(value []byte) bool {
	for _, b := range value {
		if b == '\r' || b == '\n' || b == 0 || b == 0x7f || (b < 0x20 && b != '\t') {
			return false
		}
	}
	return true
}

func verdict(allowed bool, reason string) error {
	if allowed {
		return nil
	}
	return &DeniedError{Reason: reason}
}
