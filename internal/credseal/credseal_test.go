package credseal

import (
	"bytes"
	"errors"
	"testing"
)

func TestShouldRoundTripSealedCredential(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	b := Binding{OrgID: "org_1", AgentID: "agt_1", Name: "billing"}
	sealed, err := Seal(k.PublicKey(), b, []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("s3cret")) {
		t.Fatal("sealed output contains plaintext")
	}
	plain, err := k.Open(b, sealed)
	if err != nil || string(plain) != "s3cret" {
		t.Fatalf("open = %q, %v", plain, err)
	}
}

func TestShouldRefuseOpeningWithWrongBindingOrKey(t *testing.T) {
	k, _ := NewKey()
	other, _ := NewKey()
	b := Binding{OrgID: "org_1", AgentID: "agt_1", Name: "billing"}
	sealed, err := Seal(k.PublicKey(), b, []byte("s3cret"))
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		key     *Key
		binding Binding
	}{
		"other key":   {other, b},
		"other org":   {k, Binding{OrgID: "org_2", AgentID: "agt_1", Name: "billing"}},
		"other agent": {k, Binding{OrgID: "org_1", AgentID: "agt_2", Name: "billing"}},
		"other name":  {k, Binding{OrgID: "org_1", AgentID: "agt_1", Name: "audit"}},
		"field shift": {k, Binding{OrgID: "org_1|agt_1", AgentID: "", Name: "billing"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.key.Open(tc.binding, sealed); err == nil {
				t.Fatal("opened with mismatched key or binding")
			}
		})
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := k.Open(b, tampered); err == nil {
		t.Fatal("opened tampered ciphertext")
	}
}

func TestShouldReportWrongKeySizeAsInvalidKey(t *testing.T) {
	if _, err := Seal(make([]byte, 31), Binding{}, []byte("x")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("seal err = %v", err)
	}
}

func TestShouldRejectInvalidPublicKeys(t *testing.T) {
	for name, pub := range map[string][]byte{
		"empty":     nil,
		"short":     make([]byte, 31),
		"long":      make([]byte, 33),
		"low order": make([]byte, 32),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Seal(pub, Binding{}, []byte("x")); err == nil {
				t.Fatalf("seal err = %v", err)
			}
		})
	}
}
