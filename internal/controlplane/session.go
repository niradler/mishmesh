package controlplane

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	sessionCookie    = "mm_session"
	oauthNonceCookie = "mm_oauth_nonce"
)

type sessionUser struct {
	user       *store.User
	orgID      string
	role       string
	idHash     string
	authMethod authz.AuthMethod
	authTime   time.Time
}

var errNoSession = errors.New("no valid session")

func (a *API) resolveSession(r *http.Request) (*sessionUser, bool) {
	su, err := a.lookupSession(r)
	return su, err == nil
}

func (a *API) lookupSession(r *http.Request) (*sessionUser, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, errNoSession
	}
	sess, err := a.data.GetSession(r.Context(), store.HashToken(c.Value))
	if err != nil {
		return nil, sessionLookupErr(err)
	}
	if a.now().After(sess.ExpiresAt) {
		_ = a.data.DeleteSession(r.Context(), sess.IDHash)
		return nil, errNoSession
	}
	user, err := a.data.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		return nil, sessionLookupErr(err)
	}
	m, err := a.data.GetMembership(r.Context(), sess.OrgID, user.ID)
	if err != nil {
		return nil, sessionLookupErr(err)
	}
	if m.DisabledAt != nil {
		return nil, errNoSession
	}
	return &sessionUser{user: user, orgID: sess.OrgID, role: m.Role, idHash: sess.IDHash, authMethod: authz.AuthMethod(sess.AuthMethod), authTime: sess.CreatedAt}, nil
}

func sessionLookupErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return errNoSession
	}
	return err
}

func writeStoreUnavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable, "temporarily unavailable, retry shortly")
}

func (a *API) issueSession(ctx context.Context, w http.ResponseWriter, userID, orgID string, method authz.AuthMethod, authTime time.Time) error {
	raw, err := randomToken()
	if err != nil {
		return err
	}
	now := a.now()
	if authTime.IsZero() || authTime.After(now) {
		authTime = now
	}
	sess := &store.Session{
		IDHash:     store.HashToken(raw),
		UserID:     userID,
		OrgID:      orgID,
		AuthMethod: string(method),
		CreatedAt:  authTime,
		ExpiresAt:  now.Add(a.auth.sessionTTL),
	}
	if err := a.data.CreateSession(ctx, sess); err != nil {
		return err
	}
	cookie := a.newCookie(sessionCookie, raw)
	cookie.Expires = sess.ExpiresAt
	http.SetCookie(w, cookie)
	return nil
}

func (a *API) newCookie(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.auth.cookieSecure, SameSite: http.SameSiteLaxMode}
}

func (a *API) setShortLivedCookie(w http.ResponseWriter, name, value string) {
	cookie := a.newCookie(name, value)
	cookie.MaxAge = 600
	http.SetCookie(w, cookie)
}

func (a *API) expireCookie(w http.ResponseWriter, name string) {
	cookie := a.newCookie(name, "")
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
}

func (a *API) clearSessionCookie(w http.ResponseWriter) {
	a.expireCookie(w, sessionCookie)
}

func (a *API) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = a.data.DeleteSession(r.Context(), store.HashToken(c.Value))
	}
	a.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}
