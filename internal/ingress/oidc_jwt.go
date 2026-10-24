package ingress

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	maxIDTokenBytes     = 16 << 10
	maxOIDCDocumentSize = 1 << 20
	idTokenClockSkew    = 2 * time.Minute
)

var (
	errIDTokenInvalid = errors.New("id token invalid")
	errUnknownKeyID   = errors.New("id token signed by an unknown key")
)

var idTokenAlgorithms = []jose.SignatureAlgorithm{jose.RS256, jose.ES256}

type idClaims struct {
	Iss           string
	Aud           []string
	Email         string
	EmailVerified bool
	Exp           int64
}

func (c *idClaims) verifiedEmail() bool {
	return c.Email != "" && c.EmailVerified
}

type idTokenExtra struct {
	Email         string `json:"email"`
	EmailVerified *bool  `json:"email_verified"`
	Nonce         string `json:"nonce"`
}

func verifyIDToken(raw string, keys *jose.JSONWebKeySet, issuer, clientID, nonce string, now time.Time) (*idClaims, error) {
	if raw == "" || len(raw) > maxIDTokenBytes || keys == nil {
		return nil, errIDTokenInvalid
	}
	tok, err := jwt.ParseSigned(raw, idTokenAlgorithms)
	if err != nil || len(tok.Headers) != 1 {
		return nil, errIDTokenInvalid
	}
	header := tok.Headers[0]
	candidates := keys.Keys
	if header.KeyID != "" {
		candidates = keys.Key(header.KeyID)
	}
	if len(candidates) == 0 {
		return nil, errUnknownKeyID
	}
	var std jwt.Claims
	var extra idTokenExtra
	verified := false
	for _, k := range candidates {
		if !k.Valid() || !k.IsPublic() || k.Use == "enc" || (k.Algorithm != "" && k.Algorithm != header.Algorithm) {
			continue
		}
		if tok.Claims(k.Key, &std, &extra) == nil {
			verified = true
			break
		}
	}
	if !verified {
		return nil, errIDTokenInvalid
	}
	if std.Expiry == nil {
		return nil, errIDTokenInvalid
	}
	if err := std.ValidateWithLeeway(jwt.Expected{Issuer: issuer, AnyAudience: jwt.Audience{clientID}, Time: now}, idTokenClockSkew); err != nil {
		return nil, fmt.Errorf("%w: %w", errIDTokenInvalid, err)
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(extra.Nonce), []byte(nonce)) != 1 {
		return nil, fmt.Errorf("%w: nonce mismatch", errIDTokenInvalid)
	}
	claims := &idClaims{Iss: std.Issuer, Aud: []string(std.Audience), Email: extra.Email, Exp: std.Expiry.Time().Unix()}
	if extra.EmailVerified != nil {
		claims.EmailVerified = *extra.EmailVerified
	}
	return claims, nil
}

func decodeLimitedJSON(resp *http.Response, out any) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOIDCDocumentSize+1))
	if err != nil {
		return err
	}
	if len(body) > maxOIDCDocumentSize {
		return errors.New("oidc response too large")
	}
	return json.Unmarshal(body, out)
}

func fetchJWKS(ctx context.Context, client *http.Client, uri string) (*jose.JSONWebKeySet, error) {
	set, err := fetchJSON[jose.JSONWebKeySet](ctx, client, uri)
	if err != nil {
		return nil, err
	}
	usable := set.Keys[:0]
	for _, k := range set.Keys {
		if k.Valid() && k.IsPublic() && k.Use != "enc" {
			usable = append(usable, k)
		}
	}
	if len(usable) == 0 {
		return nil, errors.New("no usable signing keys in jwks")
	}
	set.Keys = usable
	return set, nil
}

func fetchJSON[T any](ctx context.Context, client *http.Client, uri string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", uri, resp.StatusCode)
	}
	var out T
	if err := decodeLimitedJSON(resp, &out); err != nil {
		return nil, fmt.Errorf("GET %s: %w", uri, err)
	}
	return &out, nil
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
