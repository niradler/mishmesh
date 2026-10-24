package credseal

import (
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
)

const (
	PublicKeySize = 32
	MaxSealedSize = 8 << 10
	infoPrefix    = "mishmesh egress credential"
)

var ErrInvalidKey = errors.New("credseal: invalid public key")

type Binding struct {
	OrgID   string
	AgentID string
	Name    string
}

func (b Binding) info() []byte {
	var sb strings.Builder
	sb.WriteString(infoPrefix)
	for _, field := range []string{b.OrgID, b.AgentID, b.Name} {
		fmt.Fprintf(&sb, "|%d:%s", len(field), field)
	}
	return []byte(sb.String())
}

type Key struct {
	priv *ecdh.PrivateKey
}

func NewKey() (*Key, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("credseal: generate key: %w", err)
	}
	return &Key{priv: priv}, nil
}

func (k *Key) PublicKey() []byte {
	return k.priv.PublicKey().Bytes()
}

func (k *Key) Open(b Binding, sealed []byte) ([]byte, error) {
	if len(sealed) > MaxSealedSize {
		return nil, errors.New("credseal: sealed credential too large")
	}
	priv, err := hpke.NewDHKEMPrivateKey(k.priv)
	if err != nil {
		return nil, fmt.Errorf("credseal: private key: %w", err)
	}
	plain, err := hpke.Open(priv, hpke.HKDFSHA256(), hpke.AES256GCM(), b.info(), sealed)
	if err != nil {
		return nil, fmt.Errorf("credseal: open: %w", err)
	}
	return plain, nil
}

func Seal(publicKey []byte, b Binding, plaintext []byte) ([]byte, error) {
	if len(publicKey) != PublicKeySize {
		return nil, ErrInvalidKey
	}
	ecdhPub, err := ecdh.X25519().NewPublicKey(publicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	pub, err := hpke.NewDHKEMPublicKey(ecdhPub)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	sealed, err := hpke.Seal(pub, hpke.HKDFSHA256(), hpke.AES256GCM(), b.info(), plaintext)
	if err != nil {
		return nil, fmt.Errorf("credseal: seal: %w", err)
	}
	return sealed, nil
}
