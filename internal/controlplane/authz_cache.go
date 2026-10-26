package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

func (a *API) authorizerFor(ctx context.Context, orgID string) *authz.Authorizer {
	az, err := a.orgAuthorizer(ctx, orgID)
	if err != nil {
		a.log.Error("org policy lookup failed, denying", "org", orgID, "err", err)
		return a.denyAuthz.WithOwner(orgID)
	}
	return az
}

func (a *API) orgAuthorizer(ctx context.Context, orgID string) (*authz.Authorizer, error) {
	a.authzMu.Lock()
	if e, ok := a.authzCache[orgID]; ok && a.now().Before(e.expires) {
		a.authzMu.Unlock()
		return e.az, nil
	}
	key := authzLoadKey{orgID: orgID, gen: a.authzGen}
	call, inflight := a.authzLoads[key]
	if !inflight {
		call = &authzLoad{done: make(chan struct{})}
		a.authzLoads[key] = call
		a.spawn(func(bg context.Context) { a.runAuthzLoad(bg, key, call) })
	}
	a.authzMu.Unlock()
	select {
	case <-call.done:
		return call.az, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *API) runAuthzLoad(ctx context.Context, key authzLoadKey, call *authzLoad) {
	ctx, cancel := context.WithTimeout(ctx, authzLoadTimeout)
	defer cancel()
	call.az, call.err = a.loadOrgAuthorizer(ctx, key.orgID)
	a.authzMu.Lock()
	delete(a.authzLoads, key)
	if call.err == nil && a.authzGen == key.gen {
		a.authzCache[key.orgID] = authzEntry{az: call.az, expires: a.now().Add(authzCacheTTL)}
	}
	a.authzMu.Unlock()
	close(call.done)
}

func (a *API) loadOrgAuthorizer(ctx context.Context, orgID string) (*authz.Authorizer, error) {
	pol, err := a.data.GetOrgPolicy(ctx, orgID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("load org policy: %w", err)
	}
	az := a.defaultAuthz
	if err == nil && pol.CedarSrc != "" {
		parsed, perr := a.policies.Get(authz.ForOrgSource([]byte(pol.CedarSrc)))
		if perr != nil {
			a.log.Error("org policy parse failed, denying", "org", orgID, "err", perr)
			parsed = a.denyAuthz
		}
		az = parsed.WithOwner(orgID)
	}
	return az, nil
}

type authzLoadKey struct {
	orgID string
	gen   uint64
}

type authzLoad struct {
	done chan struct{}
	az   *authz.Authorizer
	err  error
}

const (
	authzCacheTTL    = 5 * time.Second
	authzLoadTimeout = 10 * time.Second
)

type authzEntry struct {
	az      *authz.Authorizer
	expires time.Time
}

func (a *API) ClearAuthzCache(orgID string) {
	a.authzMu.Lock()
	a.authzGen++
	if orgID == "" {
		clear(a.authzCache)
	} else {
		delete(a.authzCache, orgID)
	}
	a.authzMu.Unlock()
}

func (a *API) invalidateAuthz(orgID string) {
	a.authzMu.Lock()
	a.authzGen++
	delete(a.authzCache, orgID)
	a.authzMu.Unlock()
	a.flushDecisions(orgID)
}

func (a *API) SetDecisionFlusher(flush func(orgID string)) { a.decisionFlush = flush }

func (a *API) flushDecisions(orgID string) {
	if a.decisionFlush != nil {
		a.decisionFlush(orgID)
	}
}
