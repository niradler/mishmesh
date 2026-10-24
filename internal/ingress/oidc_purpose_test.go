package ingress

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestOIDCStateTokenNotAcceptedAsSession(t *testing.T) {
	key, kid := genKey(t)
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
				issuer, issuer+"/authorize", issuer+"/token", issuer+"/jwks")
		case "/jwks":
			io.WriteString(w, jwksJSON(kid, &key.PublicKey))
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	g := testGate(t)
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
