package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type DriverDescriptor struct {
	Type   string              `json:"type"`
	Label  string              `json:"label"`
	Help   string              `json:"help"`
	Fields []typedfields.Field `json:"fields"`
}

type DriverType interface {
	Describe() DriverDescriptor
	Validate(json.RawMessage) error
	New(json.RawMessage, Deps) (Driver, error)
}

type Deps struct {
	OrgID        string
	Docker       func() (*dockerClient, error)
	HostCommands bool
}

type Registry struct {
	mu    sync.RWMutex
	types map[string]DriverType
}

func NewRegistry() *Registry {
	return &Registry{types: make(map[string]DriverType)}
}

func NewBuiltinRegistry() *Registry {
	return builtinRegistry()
}

func (r *Registry) Register(driver DriverType) error {
	if driver == nil || (reflect.ValueOf(driver).Kind() == reflect.Pointer && reflect.ValueOf(driver).IsNil()) {
		return errors.New("driver type is required")
	}
	descriptor := driver.Describe()
	if descriptor.Type == "" || strings.TrimSpace(descriptor.Type) != descriptor.Type {
		return errors.New("driver type ID is required and must not contain surrounding whitespace")
	}
	fields := make([]typedfields.Field, len(descriptor.Fields))
	copy(fields, descriptor.Fields)
	for i := range fields {
		fields[i].Required = false
	}
	if err := typedfields.Validate(fields, json.RawMessage(`{}`)); err != nil {
		return fmt.Errorf("driver descriptor: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.types[descriptor.Type]; exists {
		return errors.New("driver type already registered")
	}
	if r.types == nil {
		r.types = make(map[string]DriverType)
	}
	r.types[descriptor.Type] = driver
	return nil
}

func (r *Registry) Lookup(name string) (DriverType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	driver, ok := r.types[name]
	return driver, ok
}

func (r *Registry) Descriptors() []DriverDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]DriverDescriptor, 0, len(r.types))
	for _, driver := range r.types {
		descriptor := driver.Describe()
		descriptor.Fields = append([]typedfields.Field{}, descriptor.Fields...)
		for i := range descriptor.Fields {
			descriptor.Fields[i].Options = append([]string(nil), descriptor.Fields[i].Options...)
			descriptor.Fields[i].Default = append(json.RawMessage(nil), descriptor.Fields[i].Default...)
		}
		result = append(result, descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Type < result[j].Type })
	return result
}

func validateHostConfig(descriptor DriverDescriptor, config json.RawMessage, deps Deps) error {
	host, err := typedfields.UsesHostCommands(descriptor.Fields, config)
	if err != nil {
		return err
	}
	if host && !deps.HostCommands {
		return ErrHostCommandsOff
	}
	return nil
}
