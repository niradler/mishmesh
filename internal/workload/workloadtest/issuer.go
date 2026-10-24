package workloadtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type Issuer struct {
	URL        string
	Key        *ecdsa.PrivateKey
	KeyID      string
	JWKSHits   atomic.Int64
	srv        *httptest.Server
	discovered atomic.Value
}

func NewIssuer(t *testing.T) *Issuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	iss := &Issuer{Key: key, KeyID: "k1"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		issuer := iss.URL
		if v, ok := iss.discovered.Load().(string); ok {
			issuer = v
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "jwks_uri": iss.URL + "/jwks"})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		iss.JWKSHits.Add(1)
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &iss.Key.PublicKey, KeyID: iss.KeyID, Algorithm: string(jose.ES256), Use: "sig"}}}
		_ = json.NewEncoder(w).Encode(set)
	})
	iss.srv = httptest.NewServer(mux)
	iss.URL = iss.srv.URL
	t.Cleanup(iss.srv.Close)
	return iss
}

func (i *Issuer) SetDiscoveredIssuer(v string) {
	i.discovered.Store(v)
}

type Assertion struct {
	Issuer    string
	Subject   string
	Audience  []string
	ID        string
	IssuedAt  time.Time
	Expiry    time.Time
	KeyID     string
	SignWith  any
	Algorithm jose.SignatureAlgorithm
}

func (i *Issuer) Default(subject, audience string) Assertion {
	now := time.Now()
	return Assertion{
		Issuer:   i.URL,
		Subject:  subject,
		Audience: []string{audience},
		ID:       rand.Text(),
		IssuedAt: now,
		Expiry:   now.Add(10 * time.Minute),
	}
}

func (i *Issuer) Sign(t *testing.T, a Assertion) string {
	t.Helper()
	alg := a.Algorithm
	if alg == "" {
		alg = jose.ES256
	}
	var key any = i.Key
	if a.SignWith != nil {
		key = a.SignWith
	}
	kid := a.KeyID
	if kid == "" {
		kid = i.KeyID
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", kid))
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.Claims{
		Issuer:   a.Issuer,
		Subject:  a.Subject,
		Audience: jwt.Audience(a.Audience),
		ID:       a.ID,
	}
	if !a.IssuedAt.IsZero() {
		claims.IssuedAt = jwt.NewNumericDate(a.IssuedAt)
	}
	if !a.Expiry.IsZero() {
		claims.Expiry = jwt.NewNumericDate(a.Expiry)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
