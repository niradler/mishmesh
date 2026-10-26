package controlplane

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	SignupModeOrg    = "org"
	SignupModeInvite = "invite"
)

type authConfig struct {
	enabled         bool
	passwordEnabled bool
	cookieSecure    bool
	sessionTTL      time.Duration
	signupMode      string

	googleClientID     string
	googleClientSecret string
	redirectURL        string
	issuer             string

	mu        sync.Mutex
	discovery *oidcDiscovery
}

type oidcDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

type AuthOptions struct {
	Enabled            bool
	PasswordEnabled    bool
	CookieSecure       bool
	SessionTTL         time.Duration
	SignupMode         string
	GoogleClientID     string
	GoogleClientSecret string
	RedirectURL        string
	Issuer             string
}

func (a *API) ConfigureAuth(opts AuthOptions) {
	if opts.SessionTTL <= 0 {
		opts.SessionTTL = 168 * time.Hour
	}
	if opts.SignupMode != SignupModeInvite {
		opts.SignupMode = SignupModeOrg
	}
	a.auth = &authConfig{
		signupMode:         opts.SignupMode,
		enabled:            opts.Enabled,
		passwordEnabled:    opts.PasswordEnabled,
		cookieSecure:       opts.CookieSecure,
		sessionTTL:         opts.SessionTTL,
		googleClientID:     opts.GoogleClientID,
		googleClientSecret: opts.GoogleClientSecret,
		redirectURL:        opts.RedirectURL,
		issuer:             opts.Issuer,
	}
}

func (a *API) authEnabled() bool { return a.auth != nil && a.auth.enabled }

func (a *API) googleEnabled() bool {
	return a.auth != nil && a.auth.googleClientID != "" && a.auth.googleClientSecret != "" && a.auth.redirectURL != ""
}

func (a *API) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/config", a.authConfigHandler)
	mux.HandleFunc("POST /api/v1/auth/login", a.csrfGuarded(a.loginHandler))
	mux.HandleFunc("POST /api/v1/auth/logout", a.csrfGuarded(a.logoutHandler))
	mux.HandleFunc("GET /api/v1/auth/me", a.meHandler)
	mux.HandleFunc("POST /api/v1/auth/switch-org", a.csrfGuarded(a.switchOrgHandler))
	mux.HandleFunc("POST /api/v1/auth/accept-invite", a.csrfGuarded(a.acceptInviteHandler))
	mux.HandleFunc("POST /api/v1/auth/register", a.csrfGuarded(a.registerHandler))
	mux.HandleFunc("GET /api/v1/auth/google/start", a.googleStartHandler)
	mux.HandleFunc("GET /api/v1/auth/google/callback", a.googleCallbackHandler)
	mux.HandleFunc("GET "+identity.EndpointLoginPath, a.endpointLoginHandler)
}

func (a *API) authConfigHandler(w http.ResponseWriter, r *http.Request) {
	mode := SignupModeOrg
	if a.auth != nil {
		mode = a.auth.signupMode
	}
	passwordEnabled := a.auth != nil && a.auth.passwordEnabled
	bootstrapRequired := false
	if a.authEnabled() && passwordEnabled {
		count, err := a.data.CountUsers(r.Context())
		if err != nil {
			writeStoreUnavailable(w)
			return
		}
		bootstrapRequired = count == 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bootstrap_required": bootstrapRequired,
		"auth_enabled":       a.authEnabled(),
		"password_enabled":   passwordEnabled,
		"password_signup":    a.authEnabled() && passwordEnabled,
		"google_enabled":     a.googleEnabled(),
		"signup_mode":        mode,
	})
}

func userDTO(u *store.User) map[string]string {
	return map[string]string{"id": u.ID, "email": u.Email, "name": u.Name}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
