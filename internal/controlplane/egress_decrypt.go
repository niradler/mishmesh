package controlplane

import (
	"errors"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/decryptscope"
	"github.com/mishmesh/mishmesh/internal/egressca"
	"github.com/mishmesh/mishmesh/internal/store"
)

func (a *API) SetEgressCA(ca *egressca.Authority) {
	a.egressCA = ca
}

type egressCADTO struct {
	PEM    string `json:"pem"`
	SHA256 string `json:"sha256"`
}

type egressDecryptDTO struct {
	Allow     []string   `json:"allow"`
	Bypass    []string   `json:"bypass"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func (a *API) getEgressCAHandler(w http.ResponseWriter, r *http.Request) {
	if a.egressCA == nil {
		writeError(w, http.StatusServiceUnavailable, "egress decryption is not available on this server")
		return
	}
	root, err := a.egressCA.Root(r.Context(), a.orgScope(r))
	if errors.Is(err, egressca.ErrNoBox) {
		writeError(w, http.StatusServiceUnavailable, "egress decryption is not available on this server")
		return
	}
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, egressCADTO{PEM: string(root.PEM), SHA256: root.SHA256})
}

func (a *API) getEgressDecryptHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.data.GetOrgDecrypt(r.Context(), a.orgScope(r))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, egressDecryptDTO{Allow: []string{}, Bypass: []string{}})
		return
	}
	if a.handleErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toEgressDecryptDTO(cfg))
}

func (a *API) putEgressDecryptHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Allow  []string `json:"allow"`
		Bypass []string `json:"bypass"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	allow, err := decryptscope.Normalize(req.Allow)
	if err != nil {
		writeError(w, http.StatusBadRequest, "allow: "+err.Error())
		return
	}
	bypass, err := decryptscope.Normalize(req.Bypass)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bypass: "+err.Error())
		return
	}
	orgID := a.orgScope(r)
	cfg := &store.OrgDecrypt{OrgID: orgID, Allow: allow, Bypass: bypass, UpdatedAt: a.now().UTC()}
	if a.handleErr(w, a.data.SetOrgDecrypt(r.Context(), cfg)) {
		return
	}
	a.flushDecisions(orgID)
	a.audit(r, "egress_decrypt.put", orgID, "")
	writeJSON(w, http.StatusOK, toEgressDecryptDTO(cfg))
}

func toEgressDecryptDTO(cfg *store.OrgDecrypt) egressDecryptDTO {
	out := egressDecryptDTO{Allow: cfg.Allow, Bypass: cfg.Bypass, UpdatedAt: &cfg.UpdatedAt}
	if out.Allow == nil {
		out.Allow = []string{}
	}
	if out.Bypass == nil {
		out.Bypass = []string{}
	}
	return out
}
