package controlplane

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	clientIDKind       = "mmc"
	clientSecretPrefix = "mms_"
	maxServiceAcctName = 64
)

type serviceAccountDTO struct {
	ID        string    `json:"id"`
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	ClientID  string    `json:"client_id"`
	Disabled  bool      `json:"disabled"`
	HasSecret bool      `json:"has_secret"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toServiceAccountDTO(sa *store.ServiceAccount) serviceAccountDTO {
	return serviceAccountDTO{
		ID:        sa.ID,
		OrgID:     sa.OrgID,
		Name:      sa.Name,
		Role:      sa.Role,
		ClientID:  sa.ClientID,
		Disabled:  sa.Disabled,
		HasSecret: sa.SecretHash != "",
		CreatedAt: sa.CreatedAt,
		UpdatedAt: sa.UpdatedAt,
	}
}

func (a *API) SetIdentity(svc *identity.Service) {
	a.identity = svc
}

func (a *API) SetAPIPublicURL(raw string) {
	a.apiPublicURL = strings.TrimRight(raw, "/")
}

func validServiceAccountRole(role string) bool {
	return role == store.RoleMember || role == store.RoleAdmin
}

func validServiceAccountName(w http.ResponseWriter, name string) bool {
	if name == "" || len(name) > maxServiceAcctName || strings.TrimSpace(name) != name {
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters without leading or trailing spaces")
		return false
	}
	return true
}

func newClientSecret() (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	return clientSecretPrefix + raw, nil
}

func (a *API) listServiceAccountsHandler(w http.ResponseWriter, r *http.Request) {
	list, err := a.data.ListServiceAccounts(r.Context(), a.orgScope(r))
	writeList(a, w, list, err, toServiceAccountDTO)
}

func (a *API) createServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &req) || !validServiceAccountName(w, req.Name) {
		return
	}
	if req.Role == "" {
		req.Role = store.RoleMember
	}
	if !validServiceAccountRole(req.Role) {
		writeError(w, http.StatusBadRequest, "role must be member or admin")
		return
	}
	secret, err := newClientSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := a.now().UTC()
	sa := &store.ServiceAccount{
		ID:         store.NewID("sa"),
		OrgID:      a.orgScope(r),
		Name:       req.Name,
		Role:       req.Role,
		ClientID:   store.NewID(clientIDKind),
		SecretHash: store.HashToken(secret),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if a.handleStoreErr(w, a.data.CreateServiceAccount(r.Context(), sa), "service account name already exists") {
		return
	}
	a.audit(r, "service_account.create", sa.ID, sa.Name)
	writeJSON(w, http.StatusCreated, map[string]any{
		"service_account": toServiceAccountDTO(sa),
		"client_secret":   secret,
	})
}

func (a *API) scopedServiceAccount(w http.ResponseWriter, r *http.Request) (*store.ServiceAccount, bool) {
	sa, err := a.data.GetServiceAccount(r.Context(), a.orgScope(r), r.PathValue("id"))
	if a.handleErr(w, err) {
		return nil, false
	}
	return sa, true
}

func (a *API) getServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	sa, ok := a.scopedServiceAccount(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toServiceAccountDTO(sa))
}

func (a *API) patchServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     *string `json:"name"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	sa, ok := a.scopedServiceAccount(w, r)
	if !ok {
		return
	}
	if req.Name != nil {
		if !validServiceAccountName(w, *req.Name) {
			return
		}
		sa.Name = *req.Name
	}
	if req.Role != nil {
		if !validServiceAccountRole(*req.Role) {
			writeError(w, http.StatusBadRequest, "role must be member or admin")
			return
		}
		sa.Role = *req.Role
	}
	revoke := false
	if req.Disabled != nil {
		revoke = *req.Disabled && !sa.Disabled
		sa.Disabled = *req.Disabled
	}
	sa.UpdatedAt = a.now().UTC()
	if a.handleStoreErr(w, a.data.UpdateServiceAccount(r.Context(), sa), "service account name already exists") {
		return
	}
	if revoke && !a.revokeServiceAccount(w, r, sa) {
		return
	}
	a.audit(r, "service_account.update", sa.ID, sa.Name)
	writeJSON(w, http.StatusOK, toServiceAccountDTO(sa))
}

func (a *API) rotateServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	sa, ok := a.scopedServiceAccount(w, r)
	if !ok {
		return
	}
	secret, err := newClientSecret()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	sa.SecretHash = store.HashToken(secret)
	sa.UpdatedAt = a.now().UTC()
	if a.handleErr(w, a.data.UpdateServiceAccount(r.Context(), sa)) {
		return
	}
	if !a.revokeServiceAccount(w, r, sa) {
		return
	}
	a.audit(r, "service_account.rotate", sa.ID, "")
	writeJSON(w, http.StatusOK, map[string]any{
		"service_account": toServiceAccountDTO(sa),
		"client_secret":   secret,
	})
}

func (a *API) deleteServiceAccountHandler(w http.ResponseWriter, r *http.Request) {
	sa, ok := a.scopedServiceAccount(w, r)
	if !ok {
		return
	}
	if a.handleErr(w, a.data.DeleteServiceAccount(r.Context(), sa.OrgID, sa.ID)) {
		return
	}
	if !a.revokeServiceAccount(w, r, sa) {
		return
	}
	a.audit(r, "service_account.delete", sa.ID, sa.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) revokeServiceAccount(w http.ResponseWriter, r *http.Request, sa *store.ServiceAccount) bool {
	if err := a.revokeSubject(r.Context(), sa.OrgID, authz.EntityRef{Type: authz.EntityServiceAccount, ID: sa.ID}); err != nil {
		a.log.Error("revoke service account sessions", "service_account", sa.ID, "err", err)
		writeStoreUnavailable(w)
		return false
	}
	return true
}

func (a *API) revokeSubject(ctx context.Context, orgID string, ref authz.EntityRef) error {
	if a.identity == nil {
		return nil
	}
	return a.identity.RevokeSubject(ctx, orgID, ref)
}
