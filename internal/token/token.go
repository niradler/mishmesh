package token

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
)

const (
	prefix    = "mmt1"
	MaxLength = 1024
)

type Type string

const (
	TypeAccess    Type = "access"
	TypeSession   Type = "session"
	TypeAgentUser Type = "agent_user"
)

var (
	ErrMalformed = errors.New("token: malformed")
	ErrSignature = errors.New("token: bad signature")
	ErrExpired   = errors.New("token: expired")
)

type Claims struct {
	Type      Type   `json:"typ"`
	SessionID string `json:"sid"`
	TokenID   string `json:"tid"`
	OrgID     string `json:"org"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

func (c Claims) Expiry() time.Time { return time.Unix(c.ExpiresAt, 0) }

func (c Claims) complete() bool {
	return c.Type != "" && c.SessionID != "" && c.TokenID != "" && c.ExpiresAt != 0
}

type signingKey struct {
	id      string
	private ed25519.PrivateKey
}

type Keyring struct {
	signer   signingKey
	verifier map[string]ed25519.PublicKey
}

func NewKeyring(seeds ...[]byte) (*Keyring, error) {
	if len(seeds) == 0 {
		return nil, errors.New("token: keyring needs at least one seed")
	}
	k := &Keyring{verifier: make(map[string]ed25519.PublicKey, len(seeds))}
	for i, seed := range seeds {
		if len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("token: seed %d must be %d bytes", i, ed25519.SeedSize)
		}
		private := ed25519.NewKeyFromSeed(seed)
		public := private.Public().(ed25519.PublicKey)
		id := keyID(public)
		if i == 0 {
			k.signer = signingKey{id: id, private: private}
		}
		k.verifier[id] = public
	}
	return k, nil
}

func LoadOrCreateKeyring(path string) (*Keyring, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := secrets.LoadOrCreateKey(path); err != nil {
			return nil, fmt.Errorf("token: create signing key: %w", err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("token: read signing keys %s: %w", path, err)
	}
	var seeds [][]byte
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		seed, err := hex.DecodeString(line)
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("token: signing key file %s must hold one %d-byte hex seed per line", path, ed25519.SeedSize)
		}
		seeds = append(seeds, seed)
	}
	return NewKeyring(seeds...)
}

func keyID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return hex.EncodeToString(sum[:6])
}

func (k *Keyring) Sign(c Claims) (string, error) {
	if !c.complete() {
		return "", errors.New("token: claims need typ, sid, tid and exp")
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("token: encode claims: %w", err)
	}
	head := prefix + "." + k.signer.id + "." + base64.RawURLEncoding.EncodeToString(payload)
	sig := ed25519.Sign(k.signer.private, []byte(head))
	out := head + "." + base64.RawURLEncoding.EncodeToString(sig)
	if len(out) > MaxLength {
		return "", errors.New("token: claims too large")
	}
	return out, nil
}

func (k *Keyring) Verify(raw string, now time.Time) (Claims, error) {
	if !LooksLikeToken(raw) {
		return Claims{}, ErrMalformed
	}
	parts := strings.Split(raw, ".")
	public, ok := k.verifier[parts[1]]
	if !ok {
		return Claims{}, ErrSignature
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return Claims{}, ErrMalformed
	}
	head := raw[:len(raw)-len(parts[3])-1]
	if !ed25519.Verify(public, []byte(head), sig) {
		return Claims{}, ErrSignature
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil || !c.complete() {
		return Claims{}, ErrMalformed
	}
	if !now.Before(c.Expiry()) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func LooksLikeToken(raw string) bool {
	if len(raw) < len(prefix)+8 || len(raw) > MaxLength || !strings.HasPrefix(raw, prefix+".") {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if !tokenByte(raw[i]) {
			return false
		}
	}
	parts := strings.Split(raw, ".")
	return len(parts) == 4 && !slices.Contains(parts, "")
}

func tokenByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-' || c == '_' || c == '.':
		return true
	}
	return false
}
