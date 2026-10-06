package ingress

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestOIDCStateTokenNotAcceptedAsSession(t *testing.T) {
	key, kid := genKey(t)
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
				issuer+"/authorize", issuer+"/token", issuer+"/jwks")
		case "/jwks":
			io.WriteString(w, jwksJSON(kid, &key.PublicKey))
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	g := testGate()
	ep := &store.Endpoint{
		ID: "ep_b", Kind: store.KindHTTP,
		Policy: &store.EndpointPolicy{OIDC: &store.OIDCEndpointAuth{Issuer: issuer, ClientID: "c"}},
	}

	w1 := httptest.NewRecorder()
	if g.authenticate(w1, httptest.NewRequest("GET", "http://b.local/", nil), ep) {
		t.Fatal("unauthenticated request must not pass")
	}
	loc, _ := url.Parse(w1.Result().Header.Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in redirect")
	}

	r2 := httptest.NewRequest("GET", "http://b.local/", nil)
	r2.AddCookie(&http.Cookie{Name: oidcSessionCookie, Value: state})
	if g.authenticate(httptest.NewRecorder(), r2, ep) {
		t.Fatal("state token replayed as session cookie must not authenticate")
	}
}

func TestSessionRejectsCrossPurposeAndEmptyEmail(t *testing.T) {
	g := testGate()
	exp := time.Now().Add(time.Minute).Unix()
	tests := []struct {
		name  string
		token string
	}{
		{"state signed token", g.signState(stateClaims{Ep: "ep_1", Exp: exp})},
		{"empty email session", g.signSession("ep_1", "")},
		{"session wrong endpoint", g.signSession("ep_2", "u@example.com")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := g.verifySession(tt.token, "ep_1"); ok {
				t.Fatal("must be rejected")
			}
		})
	}
	if _, err := g.verifyState(g.signSession("ep_1", "u@example.com")); err == nil {
		t.Fatal("session token must not verify as state")
	}
}
