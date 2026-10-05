package controlplane

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const challengePrefix = "_mishmesh-challenge."

var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type DNSResolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

func (a *API) SetDomainVerification(enabled bool, resolver DNSResolver) {
	a.domainVerification = enabled
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	a.resolver = resolver
}

type challengeDTO struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type domainDTO struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Verified    bool          `json:"verified"`
	VerifiedAt  *time.Time    `json:"verified_at,omitempty"`
	Challenge   *challengeDTO `json:"challenge,omitempty"`
	CNAMETarget string        `json:"cname_target"`
	CreatedAt   time.Time     `json:"created_at"`
}

func (a *API) toDomainDTO(d *store.Domain) domainDTO {
	out := domainDTO{
		ID: d.ID, Name: d.Name, Verified: d.VerifiedAt != nil, VerifiedAt: d.VerifiedAt,
		CNAMETarget: hostOnly(a.baseDomain), CreatedAt: d.CreatedAt,
	}
	if d.VerifiedAt == nil {
		out.Challenge = &challengeDTO{Type: "TXT", Name: challengePrefix + d.Name, Value: d.Token}
	}
	return out
}

func (a *API) normalizeDomain(raw string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if name == "" || len(name) > 253 {
		return "", errors.New("invalid domain")
	}
	if net.ParseIP(name) != nil {
		return "", errors.New("domain must be a hostname, not an IP address")
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", errors.New("domain must be a fully qualified hostname")
	}
	for _, l := range labels {
		if !domainLabel.MatchString(l) {
			return "", errors.New("invalid domain")
		}
	}
	if base := hostOnly(a.baseDomain); base != "" && (name == base || strings.HasSuffix(name, "."+base)) {
		return "", errors.New("domain may not be the platform base domain or a subdomain of it")
	}
	return name, nil
}

func (a *API) listDomainsHandler(w http.ResponseWriter, r *http.Request) {
	ds, err := a.data.ListDomainsByOrg(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	out := make([]domainDTO, 0, len(ds))
	for _, d := range ds {
		out = append(out, a.toDomainDTO(d))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createDomainHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := a.normalizeDomain(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	orgID := a.orgScope(r)
	if existing, err := a.data.GetDomain(r.Context(), orgID, name); err == nil {
		writeJSON(w, http.StatusOK, a.toDomainDTO(existing))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.handleErr(w, err)
		return
	}
	if _, err := a.data.GetVerifiedDomain(r.Context(), name); err == nil {
		writeError(w, http.StatusConflict, "domain is already claimed")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.handleErr(w, err)
		return
	}
	token, _, err := store.GenerateToken()
	if err != nil {
		a.handleErr(w, err)
		return
	}
	d := &store.Domain{ID: store.NewID("dom"), OrgID: orgID, Name: name, Token: token, CreatedAt: time.Now()}
	if a.handleErr(w, a.data.CreateDomain(r.Context(), d)) {
		return
	}
	a.audit(r, "domain.create", d.ID, name)
	writeJSON(w, http.StatusCreated, a.toDomainDTO(d))
}

func (a *API) orgDomain(w http.ResponseWriter, r *http.Request) (*store.Domain, bool) {
	ds, err := a.data.ListDomainsByOrg(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return nil, false
	}
	for _, d := range ds {
		if d.ID == r.PathValue("id") {
			return d, true
		}
	}
	writeError(w, http.StatusNotFound, "not found")
	return nil, false
}

func (a *API) verifyDomainHandler(w http.ResponseWriter, r *http.Request) {
	d, ok := a.orgDomain(w, r)
	if !ok {
		return
	}
	if d.VerifiedAt != nil {
		writeJSON(w, http.StatusOK, a.toDomainDTO(d))
		return
	}
	if _, err := a.data.GetVerifiedDomain(r.Context(), d.Name); err == nil {
		writeError(w, http.StatusConflict, "domain is already claimed")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.handleErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	records, _ := a.resolver.LookupTXT(ctx, challengePrefix+d.Name)
	if !containsToken(records, d.Token) {
		writeError(w, http.StatusUnprocessableEntity, "challenge TXT record not found at "+challengePrefix+d.Name)
		return
	}
	now := time.Now()
	if a.handleErr(w, a.data.SetDomainVerified(r.Context(), d.ID, now)) {
		return
	}
	d.VerifiedAt = &now
	a.audit(r, "domain.verify", d.ID, d.Name)
	writeJSON(w, http.StatusOK, a.toDomainDTO(d))
}

func containsToken(records []string, token string) bool {
	for _, rec := range records {
		if strings.TrimSpace(rec) == token {
			return true
		}
	}
	return false
}

func (a *API) deleteDomainHandler(w http.ResponseWriter, r *http.Request) {
	d, ok := a.orgDomain(w, r)
	if !ok {
		return
	}
	if ep, err := a.data.GetEndpointByDomain(r.Context(), d.Name); err == nil && ep.OrgID == d.OrgID {
		writeError(w, http.StatusConflict, "domain is bound to an endpoint")
		return
	}
	if a.handleErr(w, a.data.DeleteDomain(r.Context(), d.OrgID, d.ID)) {
		return
	}
	a.audit(r, "domain.delete", d.ID, d.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) authorizeDomainBinding(ctx context.Context, orgID, raw string) (string, int, string) {
	name, err := a.normalizeDomain(raw)
	if err != nil {
		return "", http.StatusBadRequest, err.Error()
	}
	if !a.domainVerification {
		return name, 0, ""
	}
	d, err := a.data.GetVerifiedDomain(ctx, name)
	if err != nil || d.OrgID != orgID {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.log.Warn("verified domain lookup failed", "domain", name, "err", err)
			return "", http.StatusInternalServerError, "internal error"
		}
		return "", http.StatusForbidden, "domain is not verified for this organization"
	}
	return name, 0, ""
}
