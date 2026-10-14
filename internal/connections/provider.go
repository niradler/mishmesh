package connections

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type Provider interface {
	Describe() Descriptor
	Validate(Config) error
	Detect(context.Context, json.RawMessage) (json.RawMessage, error)
	Open(Config, Deps) (Instance, error)
}

type AccessHostnameProvider interface {
	AccessHostname(endpointDomain, requested string) (string, error)
}

type HostCommandProvider interface {
	UsesHostCommands(Config) bool
}

type Instance interface {
	Run(context.Context)
	Sync(context.Context) (Status, error)
	Status() Status
	Close()
}

type Descriptor struct {
	Type              string              `json:"type"`
	Label             string              `json:"label"`
	Role              string              `json:"role"`
	Scope             string              `json:"scope"`
	Help              string              `json:"help"`
	Levels            []string            `json:"levels"`
	Fields            []typedfields.Field `json:"fields"`
	Detect            bool                `json:"detect"`
	DetectHostCommand bool                `json:"detect_host_command,omitempty"`
}

type Config struct {
	Record       store.Connection
	Secrets      map[string]string
	HostCommands bool
}

type Deps struct {
	Data         store.DataStore
	Origin       string
	Log          *slog.Logger
	Box          *secrets.Box
	HostCommands bool
}

type HostStatus struct {
	Host       string `json:"host"`
	State      string `json:"state"`
	EndpointID string `json:"endpoint_id"`
	Detail     string `json:"detail"`
	Fix        string `json:"fix"`
}

type Status struct {
	hostDiagnostic HostDiagnostic
	Level          string         `json:"level"`
	Health         string         `json:"health"`
	LastError      string         `json:"last_error"`
	LastSync       time.Time      `json:"last_sync"`
	Hostnames      []HostStatus   `json:"hostnames"`
	Details        map[string]any `json:"details"`
}
