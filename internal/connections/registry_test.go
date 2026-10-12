package connections

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type fakeProvider struct {
	descriptor Descriptor
	validate   func(Config) error
	open       func(Config, Deps) (Instance, error)
}

func (p *fakeProvider) Describe() Descriptor { return p.descriptor }
func (p *fakeProvider) Validate(cfg Config) error {
	if p.validate != nil {
		return p.validate(cfg)
	}
	return nil
}
func (p *fakeProvider) Detect(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}
func (p *fakeProvider) Open(cfg Config, deps Deps) (Instance, error) { return p.open(cfg, deps) }

func testProvider(open func(Config, Deps) (Instance, error)) *fakeProvider {
	return &fakeProvider{descriptor: Descriptor{Type: "fake", Label: "Fake", Role: "access", Scope: "public", Levels: []string{"managed"}}, open: open}
}

func TestShouldRejectDuplicateAndUnknownProviders(t *testing.T) {
	registry := NewRegistry()
	if len(registry.Descriptors()) != 0 {
		t.Fatal("registry did not start empty")
	}
	if _, ok := registry.Lookup("missing"); ok {
		t.Fatal("unknown provider found")
	}
	provider := testProvider(nil)
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(provider); err == nil {
		t.Fatal("duplicate accepted")
	}
	var absent *fakeProvider
	for _, provider := range []Provider{nil, absent} {
		if err := registry.Register(provider); err == nil {
			t.Fatal("nil accepted")
		}
	}
}

func TestShouldFreezeAndCloneDeterministicDescriptors(t *testing.T) {
	registry := NewRegistry()
	provider := testProvider(nil)
	provider.descriptor.Fields = []typedfields.Field{{Key: "mode", Kind: "select", Options: []string{"a"}, Default: json.RawMessage(`"a"`)}}
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	other := testProvider(nil)
	other.descriptor.Type = "aaa"
	if err := registry.Register(other); err != nil {
		t.Fatal(err)
	}
	provider.descriptor.Levels[0] = "changed"
	provider.descriptor.Fields[0].Options[0] = "changed"
	provider.descriptor.Fields[0].Default[1] = 'b'
	first := registry.Descriptors()
	if first[0].Type != "aaa" || first[1].Levels[0] != "managed" || first[1].Fields[0].Options[0] != "a" || string(first[1].Fields[0].Default) != `"a"` {
		t.Fatalf("descriptors=%+v", first)
	}
	first[1].Levels[0] = "caller"
	first[1].Fields[0].Default[1] = 'c'
	if reflect.DeepEqual(first, registry.Descriptors()) {
		t.Fatal("caller mutated descriptors")
	}
}

func TestShouldValidateProviderDescriptors(t *testing.T) {
	for _, descriptor := range []Descriptor{
		{Type: " ", Role: "access", Scope: "public"},
		{Type: "fake", Role: "other"},
		{Type: "fake", Role: "runtime", Scope: "public"},
		{Type: "fake", Role: "access", Scope: ""},
		{Type: "fake", Role: "access", Scope: "public", Fields: []typedfields.Field{{Key: "a", Kind: "invalid"}}},
		{Type: "fake", Role: "access", Scope: "public", Fields: []typedfields.Field{{Key: "a", Kind: "text"}, {Key: "a", Kind: "secret"}}},
	} {
		if err := NewRegistry().Register(&fakeProvider{descriptor: descriptor}); err == nil {
			t.Fatalf("accepted %+v", descriptor)
		}
	}
}
