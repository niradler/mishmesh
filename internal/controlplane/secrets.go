package controlplane

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mishmesh/mishmesh/internal/orgsecret"
	"github.com/mishmesh/mishmesh/internal/store"
)

type orgSecretDTO struct {
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	HasValue  bool      `json:"has_value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toOrgSecretDTO(s *store.OrgSecret) orgSecretDTO {
	return orgSecretDTO{Name: s.Name, Version: s.Version, HasValue: len(s.ValueEnc) > 0, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func (a *API) SetSecretVault(v *orgsecret.Vault) {
	a.vault = v
}

func (a *API) listSecretsHandler(w http.ResponseWriter, r *http.Request) {
	list, err := a.data.ListOrgSecrets(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	out := make([]orgSecretDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toOrgSecretDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) putSecretHandler(w http.ResponseWriter, r *http.Request) {
	if a.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "secret storage is not configured")
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	sec, err := a.vault.Put(r.Context(), a.orgScope(r), name, []byte(req.Value))
	switch {
	case errors.Is(err, orgsecret.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "secret name must be 1-63 lowercase letters, digits, '_' or '-'")
		return
	case errors.Is(err, orgsecret.ErrInvalidValue):
		writeError(w, http.StatusBadRequest, "secret value must be 1-"+strconv.Itoa(orgsecret.MaxValueBytes)+" bytes without CR, LF or NUL")
		return
	case errors.Is(err, orgsecret.ErrNoBox):
		writeError(w, http.StatusServiceUnavailable, "secret storage is not configured")
		return
	case a.handleErr(w, err):
		return
	}
	a.audit(r, "secret.put", name, "version="+strconv.Itoa(sec.Version))
	writeJSON(w, http.StatusOK, toOrgSecretDTO(sec))
}

func (a *API) deleteSecretHandler(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	remove := a.data.DeleteOrgSecret
	if a.vault != nil {
		remove = a.vault.Delete
	}
	err := remove(r.Context(), a.orgScope(r), name)
	if errors.Is(err, orgsecret.ErrInvalidName) {
		writeError(w, http.StatusBadRequest, "secret name must be 1-63 lowercase letters, digits, '_' or '-'")
		return
	}
	if a.handleErr(w, err) {
		return
	}
	a.audit(r, "secret.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}
