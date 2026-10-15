package connections

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type registeredProvider struct {
	provider   Provider
	descriptor Descriptor
}

type Registry struct {
	mu    sync.RWMutex
	types map[string]registeredProvider
}

func NewRegistry() *Registry { return &Registry{types: make(map[string]registeredProvider)} }

func (r *Registry) Register(provider Provider) error {
	if isNil(provider) {
		return errors.New("connection provider is required")
	}
	descriptor := cloneDescriptor(provider.Describe())
	if descriptor.Type == "" || strings.TrimSpace(descriptor.Type) != descriptor.Type {
		return errors.New("connection type ID is required without surrounding whitespace")
	}
	if descriptor.Role != "access" && descriptor.Role != "runtime" {
		return errors.New("connection role must be access or runtime")
	}
	if descriptor.Role == "runtime" && descriptor.Scope != "" || descriptor.Role == "access" && descriptor.Scope != "public" && descriptor.Scope != "private" {
		return errors.New("connection scope does not match its role")
	}
	fields := cloneDescriptor(descriptor).Fields
	for i := range fields {
		fields[i].Required = false
	}
	if err := typedfields.Validate(fields, json.RawMessage(`{}`)); err != nil {
		return errors.New("invalid connection field descriptor")
	}
	detectFields := cloneDescriptor(descriptor).DetectFields
	for index := range detectFields {
		detectFields[index].Required = false
	}
	if err := typedfields.Validate(detectFields, json.RawMessage(`{}`)); err != nil {
		return errors.New("invalid detection field descriptor")
	}
	protocols := map[string]bool{}
	for _, protocol := range descriptor.Protocols {
		if descriptor.Role != "access" || protocols[protocol] || protocol != store.KindHTTP && protocol != store.KindTCP && protocol != store.KindUDP && protocol != store.KindTLS {
			return errors.New("invalid access protocol descriptor")
		}
		protocols[protocol] = true
	}
	levels := make(map[string]bool)
	for _, level := range descriptor.Levels {
		if level == "" || strings.TrimSpace(level) != level || levels[level] {
			return errors.New("invalid connection levels")
		}
		levels[level] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.types[descriptor.Type]; exists {
		return errors.New("connection type already registered")
	}
	if r.types == nil {
		r.types = make(map[string]registeredProvider)
	}
	r.types[descriptor.Type] = registeredProvider{provider: provider, descriptor: descriptor}
	return nil
}

func (r *Registry) Lookup(name string) (Provider, bool) {
	entry, ok := r.lookup(name)
	return entry.provider, ok
}

func (r *Registry) lookup(name string) (registeredProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.types[name]
	entry.descriptor = cloneDescriptor(entry.descriptor)
	return entry, ok
}

func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Descriptor, 0, len(r.types))
	for _, entry := range r.types {
		result = append(result, cloneDescriptor(entry.descriptor))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type < result[j].Type })
	return result
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.Levels = append([]string{}, descriptor.Levels...)
	descriptor.Fields = append([]typedfields.Field{}, descriptor.Fields...)
	descriptor.Protocols = append([]string(nil), descriptor.Protocols...)
	descriptor.DetectFields = append([]typedfields.Field(nil), descriptor.DetectFields...)
	for i := range descriptor.Fields {
		descriptor.Fields[i].Options = append([]string(nil), descriptor.Fields[i].Options...)
		descriptor.Fields[i].Default = append(json.RawMessage(nil), descriptor.Fields[i].Default...)
	}
	for index := range descriptor.DetectFields {
		descriptor.DetectFields[index].Options = append([]string(nil), descriptor.DetectFields[index].Options...)
		descriptor.DetectFields[index].Default = append(json.RawMessage(nil), descriptor.DetectFields[index].Default...)
	}
	return descriptor
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}
