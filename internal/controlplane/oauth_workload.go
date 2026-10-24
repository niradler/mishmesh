package controlplane

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"time"
	"unicode"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/workload"
)

const (
	jwtBearerAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	maxWorkloadAudience    = 256
	maxWorkloadSubject     = 512
	workloadJTIKeyPrefix   = "wif-jti:"
)

type workloadIdentityDTO struct {
	ID               string    `json:"id"`
	ServiceAccountID string    `json:"service_account_id"`
	Issuer           string    `json:"issuer"`
	Audience         string    `json:"audience"`
	Subject          string    `json:"subject"`
	CreatedAt        time.Time `json:"created_at"`
}

func toWorkloadIdentityDTO(w *store.WorkloadIdentity) workloadIdentityDTO {
	return workloadIdentityDTO{ID: w.ID, ServiceAccountID: w.ServiceAccountID, Issuer: w.Issuer, Audience: w.Audience, Subject: w.Subject, CreatedAt: w.CreatedAt}
}

func (a *API) SetWorkloadVerifier(v *workload.Verifier) {
	a.workload = v
}

func (a *API) workloadAssertionGrant(w http.ResponseWriter, r *http.Request) {
	if a.workload == nil {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "client assertions are not supported")
		return
	}
	if r.PostForm.Get("client_assertion_type") != jwtBearerAssertionType {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "unsupported client_assertion_type")
		return
	}
	if _, _, hasBasic := r.BasicAuth(); hasBasic || r.PostForm.Get("client_secret") != "" {
		writeOAuthError(w, http.StatusBadRequest, oauthInvalidRequest, "use exactly one client authentication method")
		return
	}
	raw := r.PostForm.Get("client_assertion")
	issuer, err := workload.UnverifiedIssuer(raw)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return
	}
	if !a.authRateAllowed(r, "wif:"+issuer) {
		w.Header().Set("Retry-After", "12")
		writeOAuthError(w, http.StatusTooManyRequests, oauthSlowDown, "too many attempts")
		return
	}
	trusted, err := a.data.ListWorkloadIdentitiesByIssuer(r.Context(), issuer)
	if err != nil {
		a.log.Error("workload identity lookup failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	if len(trusted) == 0 {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return
	}
	claims, err := a.workload.Verify(r.Context(), raw, issuer)
	if errors.Is(err, workload.ErrUnavailable) {
		a.log.Warn("workload issuer unavailable", "issuer", issuer, "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return
	}
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return
	}
	sa, ok := a.matchWorkloadServiceAccount(w, r, trusted, claims)
	if !ok {
		return
	}
	if !a.claimAssertionOnce(w, r, claims) {
		return
	}
	a.issueServiceAccountToken(w, r, sa, authz.AuthMethodWorkloadIdentity, "")
}

func (a *API) matchWorkloadServiceAccount(w http.ResponseWriter, r *http.Request, trusted []*store.WorkloadIdentity, claims workload.Claims) (*store.ServiceAccount, bool) {
	if clientID := r.PostForm.Get("client_id"); clientID != "" {
		named, err := a.data.GetServiceAccountByClientID(r.Context(), clientID)
		if errors.Is(err, store.ErrNotFound) {
			writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
			return nil, false
		}
		if err != nil {
			a.log.Error("workload service account lookup failed", "err", err)
			writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
			return nil, false
		}
		trusted = slices.DeleteFunc(slices.Clone(trusted), func(t *store.WorkloadIdentity) bool {
			return t.ServiceAccountID != named.ID || t.OrgID != named.OrgID
		})
	}
	var match *store.WorkloadIdentity
	for _, t := range trusted {
		if subtle.ConstantTimeCompare([]byte(t.Subject), []byte(claims.Subject)) != 1 || !slices.Contains(claims.Audience, t.Audience) {
			continue
		}
		if match != nil && match.ServiceAccountID != t.ServiceAccountID {
			writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
			return nil, false
		}
		match = t
	}
	if match == nil {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return nil, false
	}
	sa, err := a.data.GetServiceAccount(r.Context(), match.OrgID, match.ServiceAccountID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sa.Disabled) {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "client authentication failed")
		return nil, false
	}
	if err != nil {
		a.log.Error("workload service account lookup failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return nil, false
	}
	return sa, true
}

func (a *API) claimAssertionOnce(w http.ResponseWriter, r *http.Request, claims workload.Claims) bool {
	sum := sha256.Sum256([]byte(claims.Issuer + "\x00" + claims.ID))
	ttl := claims.Expiry.Sub(a.now()) + workload.ClockSkew
	fresh, err := a.identity.Sessions().ClaimOnce(r.Context(), workloadJTIKeyPrefix+hex.EncodeToString(sum[:]), ttl)
	if err != nil {
		a.log.Error("workload assertion replay check failed", "err", err)
		writeOAuthError(w, http.StatusServiceUnavailable, oauthTemporarilyUnavail, "")
		return false
	}
	if !fresh {
		writeOAuthError(w, http.StatusUnauthorized, oauthInvalidClient, "assertion already used")
		return false
	}
	return true
}

func validWorkloadText(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (a *API) listWorkloadIdentitiesHandler(w http.ResponseWriter, r *http.Request) {
	sa, ok := a.scopedServiceAccount(w, r)
	if !ok {
		return
	}
	list, err := a.data.ListWorkloadIdentities(r.Context(), sa.OrgID, sa.ID)
	if a.handleErr(w, err) {
		return
	}
	out := make([]workloadIdentityDTO, 0, len(list))
	for _, wi := range list {
		out = append(out, toWorkloadIdentityDTO(wi))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) createWorkloadIdentityHandler(w http.ResponseWriter, r *http.Request) {
	if a.workload == nil {
		writeError(w, http.StatusServiceUnavailable, "workload identity federation is not configured")
		return
	}
	var req struct {
		Issuer   string `json:"issuer"`
		Audience string `json:"audience"`
		Subject  string `json:"subject"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.workload.ValidIssuer(req.Issuer) {
		writeError(w, http.StatusBadRequest, "issuer must be an https URL without query, fragment, credentials or trailing slash")
		return
	}
	if !validWorkloadText(req.Audience, maxWorkloadAudience) || !validWorkloadText(req.Subject, maxWorkloadSubject) {
		writeError(w, http.StatusBadRequest, "audience (1-256) and subject (1-512) are required and must not contain control characters")
		return
	}
	sa, ok := a.scopedServiceAccount(w, r)
	if !ok {
		return
	}
	wi := &store.WorkloadIdentity{
		ID:               store.NewID("wid"),
		OrgID:            sa.OrgID,
		ServiceAccountID: sa.ID,
		Issuer:           req.Issuer,
		Audience:         req.Audience,
		Subject:          req.Subject,
		CreatedAt:        a.now().UTC(),
	}
	err := a.data.CreateWorkloadIdentity(r.Context(), wi)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "workload identity already exists for this issuer and subject")
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "workload_identity.create", sa.ID, wi.ID)
	writeJSON(w, http.StatusCreated, toWorkloadIdentityDTO(wi))
}

func (a *API) deleteWorkloadIdentityHandler(w http.ResponseWriter, r *http.Request) {
	err := a.data.DeleteWorkloadIdentity(r.Context(), a.orgScope(r), r.PathValue("id"), r.PathValue("wid"))
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "workload_identity.delete", r.PathValue("id"), r.PathValue("wid"))
	w.WriteHeader(http.StatusNoContent)
}
