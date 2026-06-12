package store

import (
	"context"
	"errors"
	"net"
	"time"
)

var ErrNotFound = errors.New("store: not found")

const (
	KindHTTP = "http"
	KindTCP  = "tcp"
	KindTLS  = "tls"

	LifecycleEphemeral = "ephemeral"
	LifecycleReserved  = "reserved"
)

const (
	AgentActive   = "active"
	AgentDisabled = "disabled"
	AgentRevoked  = "revoked"
)

type Org struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

type Agent struct {
	ID         string
	OrgID      string
	Name       string
	Status     string
	CreatedAt  time.Time
	LastSeenAt *time.Time
}

type Token struct {
	ID        string
	OrgID     string
	AgentID   string
	Hash      string
	CreatedAt time.Time
	RevokedAt *time.Time
}

type Endpoint struct {
	ID        string
	AgentID   string
	OrgID     string
	Kind      string
	Lifecycle string
	Subdomain string
	CreatedAt time.Time
}

type DataStore interface {
	CreateOrg(ctx context.Context, o *Org) error
	GetOrg(ctx context.Context, id string) (*Org, error)

	CreateAgent(ctx context.Context, a *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context, orgID string) ([]*Agent, error)
	TouchAgent(ctx context.Context, id string, seenAt time.Time) error

	CreateToken(ctx context.Context, t *Token) error
	GetTokenByHash(ctx context.Context, hash string) (*Token, error)
	RevokeToken(ctx context.Context, id string) error

	CreateEndpoint(ctx context.Context, e *Endpoint) error
	GetEndpoint(ctx context.Context, id string) (*Endpoint, error)
	GetEndpointBySubdomain(ctx context.Context, subdomain string) (*Endpoint, error)
	ListEndpointsByAgent(ctx context.Context, agentID string) ([]*Endpoint, error)
	DeleteEndpoint(ctx context.Context, id string) error

	Close() error
}

type AgentConn interface {
	AgentID() string
	OpenStream(ctx context.Context, endpointID, kind string) (net.Conn, error)
	Close() error
}

type ConnectionStore interface {
	AddAgent(conn AgentConn) (superseded AgentConn)
	RemoveAgent(conn AgentConn)
	GetAgent(agentID string) (AgentConn, bool)

	BindEndpoint(endpointID, agentID string)
	UnbindEndpoint(endpointID string)
	ResolveEndpoint(endpointID string) (AgentConn, bool)
}
