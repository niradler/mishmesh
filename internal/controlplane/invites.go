package controlplane

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const inviteTTL = 7 * 24 * time.Hour

var errInvalidInvite = errors.New("invalid or expired invite")

func roleRank(role string) int {
	switch role {
	case store.RoleOwner:
		return 3
	case store.RoleAdmin:
		return 2
	case store.RoleMember:
		return 1
	default:
		return 0
	}
}

func (a *API) callerRole(r *http.Request) string {
	if a.isAdmin(r) {
		return store.RoleOwner
	}
	role, _ := r.Context().Value(ctxRole).(string)
	return role
}

func (a *API) canGrant(r *http.Request, role string) bool {
	return roleRank(role) > 0 && roleRank(role) <= roleRank(a.callerRole(r))
}

type inviteDTO struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Role        string    `json:"role"`
	InvitedBy   string    `json:"invited_by"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	InviteToken string    `json:"invite_token,omitempty"`
	InviteURL   string    `json:"invite_url,omitempty"`
}

func toInviteDTO(inv *store.Invite) inviteDTO {
	return inviteDTO{ID: inv.ID, Email: inv.Email, Role: inv.Role, InvitedBy: inv.InvitedBy, CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt}
}

func (a *API) createInvite(ctx context.Context, orgID, email, role, invitedBy string) (*store.Invite, string, error) {
	token, err := randomToken()
	if err != nil {
		return nil, "", fmt.Errorf("invite token: %w", err)
	}
	now := time.Now()
	inv := &store.Invite{
		ID:        store.NewID("inv"),
		TokenHash: store.HashToken(token),
		OrgID:     orgID,
		Email:     email,
		Role:      role,
		InvitedBy: invitedBy,
		CreatedAt: now,
		ExpiresAt: now.Add(inviteTTL),
	}
	if err := a.data.CreateInvite(ctx, inv); err != nil {
		return nil, "", fmt.Errorf("create invite: %w", err)
	}
	return inv, token, nil
}

func (a *API) lookupInvite(ctx context.Context, token, email string) (*store.Invite, error) {
	if token == "" {
		return nil, errInvalidInvite
	}
	hash := store.HashToken(token)
	inv, err := a.data.GetInviteByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errInvalidInvite
		}
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(inv.TokenHash), []byte(hash)) != 1 {
		return nil, errInvalidInvite
	}
	if time.Now().After(inv.ExpiresAt) {
		_ = a.data.DeleteInvite(ctx, inv.OrgID, inv.ID)
		return nil, errInvalidInvite
	}
	if subtle.ConstantTimeCompare([]byte(inv.Email), []byte(email)) != 1 {
		return nil, errInvalidInvite
	}
	return inv, nil
}

func (a *API) claimInvite(ctx context.Context, inv *store.Invite) error {
	if err := a.data.DeleteInvite(ctx, inv.OrgID, inv.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return errInvalidInvite
		}
		return err
	}
	return nil
}

func (a *API) grantInviteMembership(ctx context.Context, userID string, inv *store.Invite) error {
	if _, err := a.data.GetMembership(ctx, inv.OrgID, userID); err == nil {
		return nil
	}
	if err := a.data.CreateMembership(ctx, &store.Membership{OrgID: inv.OrgID, UserID: userID, Role: inv.Role, CreatedAt: time.Now()}); err != nil {
		return fmt.Errorf("create membership: %w", err)
	}
	return nil
}

func (a *API) redeemVerifiedEmailInvites(ctx context.Context, user *store.User) (int, error) {
	invites, err := a.data.ListInvitesByEmail(ctx, user.Email)
	if err != nil {
		return 0, err
	}
	joined := 0
	now := time.Now()
	for _, inv := range invites {
		if err := a.claimInvite(ctx, inv); err != nil {
			continue
		}
		if now.After(inv.ExpiresAt) {
			continue
		}
		if err := a.grantInviteMembership(ctx, user.ID, inv); err != nil {
			return joined, err
		}
		joined++
	}
	return joined, nil
}

func (a *API) hasLiveInviteFor(ctx context.Context, email string) bool {
	invites, err := a.data.ListInvitesByEmail(ctx, email)
	if err != nil {
		return false
	}
	now := time.Now()
	for _, inv := range invites {
		if now.Before(inv.ExpiresAt) {
			return true
		}
	}
	return false
}

func (a *API) listInvitesHandler(w http.ResponseWriter, r *http.Request) {
	invites, err := a.data.ListInvitesByOrg(r.Context(), a.orgScope(r))
	if a.handleErr(w, err) {
		return
	}
	out := make([]inviteDTO, 0, len(invites))
	for _, inv := range invites {
		out = append(out, toInviteDTO(inv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) revokeInviteHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.data.DeleteInvite(r.Context(), a.orgScope(r), id); a.handleErr(w, err) {
		return
	}
	a.audit(r, "invite.revoke", id, "")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) acceptInviteHandler(w http.ResponseWriter, r *http.Request) {
	su, ok := a.resolveSession(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req struct {
		InviteToken string `json:"invite_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if !a.authRateAllowed(r, su.user.Email) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	inv, err := a.lookupInvite(r.Context(), req.InviteToken, su.user.Email)
	if errors.Is(err, errInvalidInvite) {
		writeError(w, http.StatusForbidden, "invalid or expired invite")
		return
	}
	if a.handleErr(w, err) {
		return
	}
	if err := a.claimInvite(r.Context(), inv); errors.Is(err, errInvalidInvite) {
		writeError(w, http.StatusForbidden, "invalid or expired invite")
		return
	} else if a.handleErr(w, err) {
		return
	}
	if err := a.grantInviteMembership(r.Context(), su.user.ID, inv); err != nil {
		a.log.Warn("accept invite failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.writeMe(w, r, http.StatusOK, su.user, su.orgID)
}

func inviteURL(token string) string {
	return "/?invite=" + url.QueryEscape(token)
}
