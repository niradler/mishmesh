package workload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	MaxAssertionBytes = 15 << 10
	MaxLifetime       = time.Hour
	ClockSkew         = 5 * time.Minute
	MaxIssuerLength   = 512

	keysTTL           = time.Hour
	unknownKidBackoff = time.Minute
	maxDocumentBytes  = 1 << 20
)

var (
	ErrInvalidAssertion = errors.New("workload: invalid assertion")
	ErrUnavailable      = errors.New("workload: issuer unavailable")
)

var allowedAlgorithms = []jose.SignatureAlgorithm{jose.RS256, jose.ES256, jose.ES384, jose.EdDSA}

type Claims struct {
	Issuer   string
	Subject  string
	ID       string
	Audience []string
	Expiry   time.Time
}

type Options struct {
	Client    *http.Client
	AllowHTTP bool
	Now       func() time.Time
}

type issuerKeys struct {
	mu          sync.Mutex
	keys        *jose.JSONWebKeySet
	fetchedAt   time.Time
	lastRefresh time.Time
}

type Verifier struct {
	client    *http.Client
	allowHTTP bool
	now       func() time.Time

	mu      sync.Mutex
	issuers map[string]*issuerKeys
}

func NewVerifier(opts Options) *Verifier {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Verifier{client: client, allowHTTP: opts.AllowHTTP, now: now, issuers: make(map[string]*issuerKeys)}
}

func parse(raw string) (*jwt.JSONWebToken, error) {
	if raw == "" || len(raw) > MaxAssertionBytes {
		return nil, ErrInvalidAssertion
	}
	tok, err := jwt.ParseSigned(raw, allowedAlgorithms)
	if err != nil || len(tok.Headers) != 1 {
		return nil, ErrInvalidAssertion
	}
	return tok, nil
}

func UnverifiedIssuer(raw string) (string, error) {
	tok, err := parse(raw)
	if err != nil {
		return "", err
	}
	var c jwt.Claims
	if err := tok.UnsafeClaimsWithoutVerification(&c); err != nil || c.Issuer == "" {
		return "", ErrInvalidAssertion
	}
	return c.Issuer, nil
}

func (v *Verifier) Verify(ctx context.Context, raw, issuer string) (Claims, error) {
	tok, err := parse(raw)
	if err != nil {
		return Claims{}, err
	}
	header := tok.Headers[0]
	entry := v.entry(issuer)
	keys, err := v.keysFor(ctx, entry, issuer, header.KeyID)
	if err != nil {
		return Claims{}, err
	}
	var c jwt.Claims
	if !verifyWithAny(tok, keys, header.Algorithm, &c) {
		return Claims{}, ErrInvalidAssertion
	}
	now := v.now()
	if err := c.ValidateWithLeeway(jwt.Expected{Issuer: issuer, Time: now}, ClockSkew); err != nil {
		return Claims{}, ErrInvalidAssertion
	}
	if c.Expiry == nil || c.Subject == "" || c.ID == "" || len(c.Audience) == 0 {
		return Claims{}, ErrInvalidAssertion
	}
	exp := c.Expiry.Time()
	start := now
	if c.IssuedAt != nil {
		start = c.IssuedAt.Time()
	}
	if exp.Sub(start) > MaxLifetime {
		return Claims{}, ErrInvalidAssertion
	}
	return Claims{Issuer: c.Issuer, Subject: c.Subject, ID: c.ID, Audience: []string(c.Audience), Expiry: exp}, nil
}

func verifyWithAny(tok *jwt.JSONWebToken, keys []jose.JSONWebKey, alg string, out *jwt.Claims) bool {
	for _, k := range keys {
		if !k.Valid() || !k.IsPublic() || k.Use == "enc" || (k.Algorithm != "" && k.Algorithm != alg) {
			continue
		}
		if err := tok.Claims(k.Key, out); err == nil {
			return true
		}
	}
	return false
}

func (v *Verifier) entry(issuer string) *issuerKeys {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.issuers[issuer]
	if !ok {
		e = &issuerKeys{}
		v.issuers[issuer] = e
	}
	return e
}

func selectKeys(set *jose.JSONWebKeySet, kid string) []jose.JSONWebKey {
	if kid == "" {
		return set.Keys
	}
	return set.Key(kid)
}

func (v *Verifier) keysFor(ctx context.Context, e *issuerKeys, issuer, kid string) ([]jose.JSONWebKey, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := v.now()
	stale := e.keys == nil || now.Sub(e.fetchedAt) > keysTTL
	if !stale {
		if keys := selectKeys(e.keys, kid); len(keys) > 0 {
			return keys, nil
		}
		if now.Sub(e.lastRefresh) < unknownKidBackoff {
			return nil, ErrInvalidAssertion
		}
	}
	e.lastRefresh = now
	set, err := v.fetchKeys(ctx, issuer)
	if err != nil {
		return nil, err
	}
	e.keys, e.fetchedAt = set, now
	keys := selectKeys(set, kid)
	if len(keys) == 0 {
		return nil, ErrInvalidAssertion
	}
	return keys, nil
}

func (v *Verifier) ValidIssuer(raw string) bool {
	if raw == "" || len(raw) > MaxIssuerLength || v.checkURL(raw) != nil {
		return false
	}
	u, _ := url.Parse(raw)
	return u.RawQuery == "" && u.Fragment == "" && !strings.HasSuffix(raw, "/")
}

func (v *Verifier) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: bad url %q", ErrUnavailable, raw)
	}
	if u.Scheme != "https" && !(v.allowHTTP && u.Scheme == "http") {
		return fmt.Errorf("%w: url must be https", ErrUnavailable)
	}
	return nil
}

func (v *Verifier) getJSON(ctx context.Context, raw string, out any) error {
	if err := v.checkURL(raw); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s returned %d", ErrUnavailable, raw, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(body) > maxDocumentBytes {
		return fmt.Errorf("%w: %s response too large", ErrUnavailable, raw)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: decode %s: %v", ErrUnavailable, raw, err)
	}
	return nil
}

func (v *Verifier) fetchKeys(ctx context.Context, issuer string) (*jose.JSONWebKeySet, error) {
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.getJSON(ctx, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", &doc); err != nil {
		return nil, err
	}
	if doc.Issuer != issuer {
		return nil, fmt.Errorf("%w: discovery issuer mismatch", ErrUnavailable)
	}
	var set jose.JSONWebKeySet
	if err := v.getJSON(ctx, doc.JWKSURI, &set); err != nil {
		return nil, err
	}
	return &set, nil
}
