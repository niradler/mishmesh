package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func (a *API) registerHandler(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil || !a.auth.passwordEnabled {
		writeError(w, http.StatusForbidden, "password registration disabled")
		return
	}
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Name        string `json:"name"`
		InviteToken string `json:"invite_token"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	email := normalizeEmail(req.Email)
	if !a.authRateAllowed(r, email) {
		writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	if !validEmail(email) {
		writeError(w, http.StatusBadRequest, "valid email required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password (>=8 chars) required")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash failed")
		return
	}
	user, err := a.registerPasswordUser(r.Context(), email, req.Name, string(hash), req.InviteToken)
	switch {
	case errors.Is(err, errEmailTaken):
		writeError(w, http.StatusConflict, "email already registered")
		return
	case errors.Is(err, errInvalidInvite):
		writeError(w, http.StatusForbidden, "invalid or expired invite")
		return
	case errors.Is(err, errSignupClosed):
		writeError(w, http.StatusForbidden, "registration is by invitation only")
		return
	case err != nil:
		a.log.Warn("register failed", "err", err)
		writeError(w, http.StatusInternalServerError, "register failed")
		return
	}
	orgID, ok := a.primaryOrg(r.Context(), user.ID)
	if !ok {
		writeError(w, http.StatusForbidden, "no organization membership")
		return
	}
	if err := a.issueSession(r.Context(), w, user.ID, orgID, authz.AuthMethodPassword, time.Time{}); err != nil {
		writeError(w, http.StatusInternalServerError, "session failed")
		return
	}
	a.writeMe(w, r, http.StatusCreated, user, orgID)
}

var (
	errEmailTaken   = errors.New("email already registered")
	errSignupClosed = errors.New("signup closed")
)

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validEmail(email string) bool {
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return false
	}
	at := strings.LastIndex(email, "@")
	return at > 0 && strings.Contains(email[at+1:], ".") && !strings.HasSuffix(email, ".")
}

func (a *API) registerPasswordUser(ctx context.Context, email, name, hash, inviteToken string) (*store.User, error) {
	_, err := a.data.GetUserByEmail(ctx, email)
	if err == nil {
		return nil, errEmailTaken
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	user := &store.User{ID: store.NewID("usr"), Email: email, Name: name, PasswordHash: hash, CreatedAt: a.now()}
	if inviteToken == "" {
		return a.provisionNewUser(ctx, user)
	}
	inv, err := a.lookupInvite(ctx, inviteToken, email)
	if err != nil {
		return nil, err
	}
	if err := a.claimInvite(ctx, inv); err != nil {
		return nil, err
	}
	if err := a.data.CreateUser(ctx, user); err != nil {
		a.releaseInvite(ctx, inv)
		return nil, fmt.Errorf("create user: %w", err)
	}
	if err := a.grantInviteMembership(ctx, user.ID, inv); err != nil {
		a.releaseInvite(ctx, inv)
		return nil, err
	}
	return user, nil
}

func (a *API) provisionNewUser(ctx context.Context, user *store.User) (*store.User, error) {
	if a.auth.signupMode == SignupModeInvite {
		if a.hasDefaultOwner(ctx) {
			return nil, errSignupClosed
		}
		if err := a.data.CreateUser(ctx, user); err != nil {
			return nil, fmt.Errorf("create user: %w", err)
		}
		if err := a.joinDefaultOrg(ctx, user.ID, store.RoleOwner); err != nil {
			return nil, err
		}
		return user, nil
	}
	if err := a.data.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	org, err := a.createOrg(ctx, orgNameFor(user))
	if err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}
	if err := a.data.CreateMembership(ctx, &store.Membership{OrgID: org.ID, UserID: user.ID, Role: store.RoleOwner, CreatedAt: a.now()}); err != nil {
		return nil, fmt.Errorf("create membership: %w", err)
	}
	return user, nil
}

func (a *API) hasDefaultOwner(ctx context.Context) bool {
	ms, err := a.data.ListMembershipsByOrg(ctx, defaultOrgID)
	if err != nil {
		return true
	}
	for _, m := range ms {
		if m.Role == store.RoleOwner {
			return true
		}
	}
	return false
}

func (a *API) joinDefaultOrg(ctx context.Context, userID, role string) error {
	org, err := a.ensureOrg(ctx, defaultOrgID)
	if err != nil {
		return fmt.Errorf("ensure default org: %w", err)
	}
	if err := a.data.CreateMembership(ctx, &store.Membership{OrgID: org.ID, UserID: userID, Role: role, CreatedAt: a.now()}); err != nil {
		return fmt.Errorf("create membership: %w", err)
	}
	return nil
}

func orgNameFor(u *store.User) string {
	if n := strings.TrimSpace(u.Name); n != "" {
		return n
	}
	if at := strings.Index(u.Email, "@"); at > 0 {
		return u.Email[:at]
	}
	return u.Email
}
