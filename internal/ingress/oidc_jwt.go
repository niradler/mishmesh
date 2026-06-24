package ingress

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"time"
)

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

func verifyIDToken(raw string, keys map[string]*rsa.PublicKey, issuer, clientID string, now time.Time) (*idClaims, error) {
	headerSeg, rest, ok := cut(raw, ".")
	if !ok {
		return nil, errors.New("malformed jwt")
	}
	payloadSeg, sigSeg, ok := cut(rest, ".")
	if !ok {
		return nil, errors.New("malformed jwt")
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(headerSeg, &header); err != nil {
		return nil, fmt.Errorf("jwt header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported alg %q", header.Alg)
	}
	key := keys[header.Kid]
	if key == nil {
		return nil, errors.New("no matching jwks key")
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigSeg)
	if err != nil {
		return nil, fmt.Errorf("jwt signature: %w", err)
	}
	digest := sha256.Sum256([]byte(headerSeg + "." + payloadSeg))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, errors.New("signature verification failed")
	}

	var raw0 struct {
		Iss           string          `json:"iss"`
		Aud           json.RawMessage `json:"aud"`
		Email         string          `json:"email"`
		EmailVerified *bool           `json:"email_verified"`
		Exp           int64           `json:"exp"`
	}
	if err := decodeSegment(payloadSeg, &raw0); err != nil {
		return nil, fmt.Errorf("jwt payload: %w", err)
	}
	claims := &idClaims{Iss: raw0.Iss, Email: raw0.Email, Exp: raw0.Exp, Aud: parseAud(raw0.Aud)}
	if raw0.EmailVerified != nil {
		claims.EmailVerified = *raw0.EmailVerified
	}

	if claims.Iss != issuer {
		return nil, fmt.Errorf("issuer mismatch: %q", claims.Iss)
	}
	if !slices.Contains(claims.Aud, clientID) {
		return nil, errors.New("audience mismatch")
	}
	if now.Unix() > claims.Exp {
		return nil, errors.New("token expired")
	}
	return claims, nil
}

func parseAud(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	return nil
}

func decodeSegment(seg string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

type jwksDoc struct {
	Keys []struct {
		Kty string `json:"kty"`
		Kid string `json:"kid"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func fetchJWKS(ctx context.Context, client *http.Client, uri string) (map[string]*rsa.PublicKey, error) {
	doc, err := fetchJSON[jwksDoc](ctx, client, uri)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*rsa.PublicKey)
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := rsaKeyFromJWK(k.N, k.E)
		if err != nil {
			continue
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("no usable RSA keys in jwks")
	}
	return out, nil
}

func rsaKeyFromJWK(nStr, eStr string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, errors.New("invalid jwk exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func fetchJSON[T any](ctx context.Context, client *http.Client, uri string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", uri, resp.StatusCode)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
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
