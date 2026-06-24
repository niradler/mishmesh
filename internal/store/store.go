package store

import (
	"context"
	"errors"
	"net"
	"time"
)

var ErrNotFound = errors.New("store: not found")

func MethodOrDefault(m string) string {
	if m == "" {
		return MethodNative
	}
	return m
}

const (
	KindHTTP = "http"
	KindTCP  = "tcp"
	KindTLS  = "tls"

	LifecycleEphemeral = "ephemeral"
	LifecycleReserved  = "reserved"

	MethodNative     = "native"
	MethodSSH        = "ssh"
	MethodProxy      = "proxy"
	MethodTailscale  = "tailscale"
	MethodCloudflare = "cloudflare"
)

const (
	AgentActive   = "active"
	AgentDisabled = "disabled"
	AgentRevoked  = "revoked"
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
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

type OIDCEndpointAuth struct {
	Issuer         string   `json:"issuer"`
	ClientID       string   `json:"client_id"`
	ClientSecret   string   `json:"client_secret"`
	AllowedEmails  []string `json:"allowed_emails,omitempty"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

type EndpointPolicy struct {
	RequestHeadersAdd     map[string]string `json:"request_headers_add,omitempty"`
	RequestHeadersRemove  []string          `json:"request_headers_remove,omitempty"`
	ResponseHeadersAdd    map[string]string `json:"response_headers_add,omitempty"`
	ResponseHeadersRemove []string          `json:"response_headers_remove,omitempty"`
	HostHeader            string            `json:"host_header,omitempty"`
	StripPathPrefix       string            `json:"strip_path_prefix,omitempty"`
	AddPathPrefix         string            `json:"add_path_prefix,omitempty"`
	BasicAuthUser         string            `json:"basic_auth_user,omitempty"`
	BasicAuthHash         string            `json:"basic_auth_hash,omitempty"`
	IPAllow               []string          `json:"ip_allow,omitempty"`
	IPDeny                []string          `json:"ip_deny,omitempty"`
	ForceHTTPS            bool              `json:"force_https,omitempty"`
	MaxBodyBytes          int64             `json:"max_body_bytes,omitempty"`
	Compression           bool              `json:"compression,omitempty"`
	OIDC                  *OIDCEndpointAuth `json:"oidc,omitempty"`
	MTLS                  *MTLSConfig       `json:"mtls,omitempty"`
	ProxyTarget           string            `json:"proxy_target,omitempty"`
}

type MTLSConfig struct {
	ClientCAPEM string   `json:"client_ca_pem"`
	AllowedCNs  []string `json:"allowed_cns,omitempty"`
}

type Endpoint struct {
	ID        string
	AgentID   string
	OrgID     string
	Kind      string
	Method    string
	Lifecycle string
	Subdomain string
	Domain    string
	Port      int
	Policy    *EndpointPolicy
	CreatedAt time.Time
}

type Quota struct {
	OrgID             string
	MaxAgents         int
	MaxEndpoints      int
	MaxBandwidthBytes int64
	UpdatedAt         time.Time
}

type User struct {
	ID           string
	Email        string
	Name         string
	PasswordHash string
	GoogleSub    string
	CreatedAt    time.Time
}

type Membership struct {
	OrgID     string
	UserID    string
	Role      string
	CreatedAt time.Time
}

type Session struct {
	IDHash    string
	UserID    string
	OrgID     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type OrgPolicy struct {
	OrgID     string
	CedarSrc  string
	UpdatedAt time.Time
}

type DataStore interface {
	CreateOrg(ctx context.Context, o *Org) error
	GetOrg(ctx context.Context, id string) (*Org, error)
	ListOrgs(ctx context.Context) ([]*Org, error)

	CreateAgent(ctx context.Context, a *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context, orgID string) ([]*Agent, error)
	UpdateAgent(ctx context.Context, a *Agent) error
	DeleteAgent(ctx context.Context, id string) error
	TouchAgent(ctx context.Context, id string, seenAt time.Time) error
	CountAgents(ctx context.Context, orgID string) (int, error)

	CreateToken(ctx context.Context, t *Token) error
	GetTokenByHash(ctx context.Context, hash string) (*Token, error)
	ListTokensByAgent(ctx context.Context, agentID string) ([]*Token, error)
	RevokeToken(ctx context.Context, id string) error
	RevokeTokensByAgent(ctx context.Context, agentID string) error

	CreateEndpoint(ctx context.Context, e *Endpoint) error
	GetEndpoint(ctx context.Context, id string) (*Endpoint, error)
	GetEndpointBySubdomain(ctx context.Context, subdomain string) (*Endpoint, error)
	GetEndpointByDomain(ctx context.Context, domain string) (*Endpoint, error)
	ListEndpointsByAgent(ctx context.Context, agentID string) ([]*Endpoint, error)
	ListEndpointsByOrg(ctx context.Context, orgID string) ([]*Endpoint, error)
	UpdateEndpoint(ctx context.Context, e *Endpoint) error
	DeleteEndpoint(ctx context.Context, id string) error
	CountEndpoints(ctx context.Context, orgID string) (int, error)

	GetQuota(ctx context.Context, orgID string) (*Quota, error)
	SetQuota(ctx context.Context, q *Quota) error

	CreateUser(ctx context.Context, u *User) error
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByGoogleSub(ctx context.Context, sub string) (*User, error)
	UpdateUser(ctx context.Context, u *User) error
	CountUsers(ctx context.Context) (int, error)

	CreateMembership(ctx context.Context, m *Membership) error
	GetMembership(ctx context.Context, orgID, userID string) (*Membership, error)
	ListMembershipsByUser(ctx context.Context, userID string) ([]*Membership, error)
	ListMembershipsByOrg(ctx context.Context, orgID string) ([]*Membership, error)
	UpdateMembership(ctx context.Context, m *Membership) error
	DeleteMembership(ctx context.Context, orgID, userID string) error

	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, idHash string) (*Session, error)
	DeleteSession(ctx context.Context, idHash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error

	GetOrgPolicy(ctx context.Context, orgID string) (*OrgPolicy, error)
	SetOrgPolicy(ctx context.Context, p *OrgPolicy) error

	AppendAudit(ctx context.Context, e *AuditEvent) error
	ListAudit(ctx context.Context, orgID string, limit int) ([]*AuditEvent, error)

	Close() error
}

type AuditEvent struct {
	ID        string
	OrgID     string
	Actor     string
	Action    string
	Target    string
	Detail    string
	CreatedAt time.Time
}

type AgentConn interface {
	AgentID() string
	OpenStream(ctx context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error)
	Close() error
}

type ConnectionStore interface {
	AddAgent(conn AgentConn) (superseded AgentConn)
	RemoveAgent(conn AgentConn)
	GetAgent(agentID string) (AgentConn, bool)

	BindEndpoint(endpointID, agentID string)
	UnbindEndpoint(endpointID string)
	ResolveEndpoint(endpointID string) (AgentConn, bool)

	AddUsage(orgID string, bytes int64)
	Usage(orgID string) int64
}
