package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

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
	KindUDP  = "udp"

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
	RateLimit             *RateLimit        `json:"rate_limit,omitempty"`
	ProxyTargets          []string          `json:"proxy_targets,omitempty"`
	LoadBalance           string            `json:"load_balance,omitempty"`
	OriginTLS             *OriginTLS        `json:"origin_tls,omitempty"`
	ServiceID             string            `json:"service_id,omitempty"`
	Connect               *ConnectPolicy    `json:"connect,omitempty"`
	Access                []AccessRef       `json:"access,omitempty"`
	AccessPolicy          *AccessPolicy     `json:"access_policy,omitempty"`
	Identity              *EndpointIdentity `json:"identity,omitempty"`
}

type AccessRef struct {
	ConnectionID string        `json:"connection_id"`
	Hostname     string        `json:"hostname,omitempty"`
	Policy       *AccessPolicy `json:"policy,omitempty"`
}

type AccessPolicy struct {
	AllowEmails       []string `json:"allow_emails,omitempty"`
	AllowEmailDomains []string `json:"allow_email_domains,omitempty"`
	AllowGroups       []string `json:"allow_groups,omitempty"`
	RequireMFA        bool     `json:"require_mfa,omitempty"`
	SessionDuration   string   `json:"session_duration,omitempty"`
}

func (p *EndpointPolicy) EffectiveAccessPolicy(ref AccessRef) *AccessPolicy {
	if ref.Policy != nil {
		return ref.Policy
	}
	if p == nil {
		return nil
	}
	return p.AccessPolicy
}

type EndpointAlias struct {
	Hostname            string
	AccessHostname      string
	AccessHostnameKnown bool
	Kind                string
	EndpointID          string
	ConnectionID        string
	OrgID               string
}

const ConnectAnyAgent = "*"

type ConnectPolicy struct {
	Name        string   `json:"name,omitempty"`
	AllowAgents []string `json:"allow_agents,omitempty"`
}

func (p *ConnectPolicy) Allows(agent *Agent) bool {
	if p == nil || agent == nil {
		return false
	}
	for _, allowed := range p.AllowAgents {
		if allowed == ConnectAnyAgent || allowed == agent.ID || (agent.Name != "" && allowed == agent.Name) {
			return true
		}
	}
	return false
}

func PortKind(kind string) bool {
	return kind == KindTCP || kind == KindUDP
}

const (
	LoadBalanceRoundRobin = "round_robin"
	LoadBalanceFailover   = "failover"
	LoadBalanceSticky     = "sticky"
)

type OriginTLS struct {
	ServerName         string   `json:"server_name,omitempty"`
	CAPEM              string   `json:"ca_pem,omitempty"`
	PinSHA256          []string `json:"pin_sha256,omitempty"`
	InsecureSkipVerify bool     `json:"insecure_skip_verify,omitempty"`
}

type RateLimit struct {
	Requests      int    `json:"requests"`
	PeriodSeconds int    `json:"period_seconds"`
	Burst         int    `json:"burst,omitempty"`
	Scope         string `json:"scope,omitempty"`
}

const (
	RateLimitScopeIP       = "ip"
	RateLimitScopeEndpoint = "endpoint"

	maxRateLimitRequests = 1_000_000
	maxRateLimitPeriod   = 86400
)

func (r *RateLimit) Validate() error {
	switch {
	case r.Requests < 1 || r.Requests > maxRateLimitRequests:
		return fmt.Errorf("rate_limit.requests must be between 1 and %d", maxRateLimitRequests)
	case r.PeriodSeconds < 1 || r.PeriodSeconds > maxRateLimitPeriod:
		return fmt.Errorf("rate_limit.period_seconds must be between 1 and %d", maxRateLimitPeriod)
	case r.Burst < 0 || r.Burst > maxRateLimitRequests:
		return fmt.Errorf("rate_limit.burst must be between 0 and %d", maxRateLimitRequests)
	}
	switch r.Scope {
	case "", RateLimitScopeIP, RateLimitScopeEndpoint:
		return nil
	}
	return fmt.Errorf("rate_limit.scope must be %q or %q", RateLimitScopeIP, RateLimitScopeEndpoint)
}

func (r *RateLimit) PerEndpoint() bool {
	return r.Scope == RateLimitScopeEndpoint
}

type MTLSConfig struct {
	ClientCAPEM string   `json:"client_ca_pem"`
	AllowedCNs  []string `json:"allowed_cns,omitempty"`
}

type Endpoint struct {
	ID                  string
	AgentID             string
	OrgID               string
	Kind                string
	Method              string
	Lifecycle           string
	Subdomain           string
	Domain              string
	Port                int
	Policy              *EndpointPolicy
	ConnectionSnapshots []*Connection `json:"-"`
	CreatedAt           time.Time
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
	OrgID      string
	UserID     string
	Role       string
	CreatedAt  time.Time
	DisabledAt *time.Time
}

const (
	GroupMemberUser           = "user"
	GroupMemberServiceAccount = "service_account"
)

type Group struct {
	ID        string
	OrgID     string
	Name      string
	CreatedAt time.Time
}

type GroupMember struct {
	OrgID      string
	GroupID    string
	MemberType string
	MemberID   string
	CreatedAt  time.Time
}

type ServiceAccount struct {
	ID         string
	OrgID      string
	Name       string
	Role       string
	ClientID   string
	SecretHash string
	Disabled   bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Invite struct {
	ID        string
	TokenHash string
	OrgID     string
	Email     string
	Role      string
	InvitedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type Session struct {
	IDHash     string
	UserID     string
	OrgID      string
	AuthMethod string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

type OrgPolicy struct {
	OrgID     string
	CedarSrc  string
	UpdatedAt time.Time
}

type Domain struct {
	ID         string
	OrgID      string
	Name       string
	Token      string
	VerifiedAt *time.Time
	CreatedAt  time.Time
}

type Service struct {
	ID                  string
	OrgID               string
	Name                string
	Driver              string
	Config              json.RawMessage
	Ready               json.RawMessage
	Requires            []string
	ConnectionIDs       []string `json:"-"`
	IdleTimeoutSeconds  int
	StartTimeoutSeconds int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type Connection struct {
	ID         string
	OrgID      string
	Type       string
	Name       string
	Config     json.RawMessage
	State      json.RawMessage `json:"-"`
	SecretsEnc []byte          `json:"-"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func SameConnectionInput(left, right Connection) bool {
	return left.ID == right.ID && left.OrgID == right.OrgID && left.Type == right.Type && left.Name == right.Name && bytes.Equal(left.Config, right.Config) && bytes.Equal(left.SecretsEnc, right.SecretsEnc) && left.UpdatedAt.Equal(right.UpdatedAt)
}

type ConnectionReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ConnectionInUseError struct {
	References []ConnectionReference
}

func (e *ConnectionInUseError) Error() string {
	return "connection is referenced by endpoints or services"
}
func (e *ConnectionInUseError) Unwrap() error { return ErrConflict }

type OrgStore interface {
	CreateOrg(ctx context.Context, o *Org) error
	GetOrg(ctx context.Context, id string) (*Org, error)
	ListOrgs(ctx context.Context) ([]*Org, error)
}

type AgentStore interface {
	CreateAgent(ctx context.Context, a *Agent) error
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context, orgID string) ([]*Agent, error)
	UpdateAgent(ctx context.Context, a *Agent) error
	DeleteAgent(ctx context.Context, id string) error
	TouchAgent(ctx context.Context, id string, seenAt time.Time) error
	CountAgents(ctx context.Context, orgID string) (int, error)
}

type TokenStore interface {
	CreateToken(ctx context.Context, t *Token) error
	GetTokenByHash(ctx context.Context, hash string) (*Token, error)
	ListTokensByAgent(ctx context.Context, agentID string) ([]*Token, error)
	RevokeToken(ctx context.Context, id string) error
	RevokeTokensByAgent(ctx context.Context, agentID string) error
}

type EndpointStore interface {
	CreateEndpoint(ctx context.Context, e *Endpoint) error
	GetEndpoint(ctx context.Context, id string) (*Endpoint, error)
	GetEndpointBySubdomain(ctx context.Context, subdomain string) (*Endpoint, error)
	GetEndpointByDomain(ctx context.Context, domain string) (*Endpoint, error)
	GetEndpointByAccessHostname(ctx context.Context, hostname string) (*Endpoint, error)
	ReplaceConnectionAliases(ctx context.Context, orgID, connectionID string, aliases []EndpointAlias) error
	ReplaceConnectionAliasesIfCurrent(ctx context.Context, expected *Connection, aliases []EndpointAlias) error
	ListEndpointsByAgent(ctx context.Context, agentID string) ([]*Endpoint, error)
	ListEndpointsByOrg(ctx context.Context, orgID string) ([]*Endpoint, error)
	UpdateEndpoint(ctx context.Context, e *Endpoint) error
	DeleteEndpoint(ctx context.Context, id string) error
	CountEndpoints(ctx context.Context, orgID string) (int, error)
}

type DomainStore interface {
	CreateDomain(ctx context.Context, d *Domain) error
	GetDomain(ctx context.Context, orgID, name string) (*Domain, error)
	GetVerifiedDomain(ctx context.Context, name string) (*Domain, error)
	ListDomainsByOrg(ctx context.Context, orgID string) ([]*Domain, error)
	SetDomainVerified(ctx context.Context, id string, at time.Time) error
	DeleteDomain(ctx context.Context, orgID, id string) error
}

type QuotaStore interface {
	GetQuota(ctx context.Context, orgID string) (*Quota, error)
	SetQuota(ctx context.Context, q *Quota) error
}

type UserStore interface {
	CreateUser(ctx context.Context, u *User) error
	GetUserByID(ctx context.Context, id string) (*User, error)
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	GetUserByGoogleSub(ctx context.Context, sub string) (*User, error)
	UpdateUser(ctx context.Context, u *User) error
	CountUsers(ctx context.Context) (int, error)
}

type MembershipStore interface {
	CreateMembership(ctx context.Context, m *Membership) error
	GetMembership(ctx context.Context, orgID, userID string) (*Membership, error)
	ListMembershipsByUser(ctx context.Context, userID string) ([]*Membership, error)
	ListMembershipsByOrg(ctx context.Context, orgID string) ([]*Membership, error)
	UpdateMembership(ctx context.Context, m *Membership) error
	DeleteMembership(ctx context.Context, orgID, userID string) error
}

type GroupStore interface {
	CreateGroup(ctx context.Context, g *Group) error
	GetGroup(ctx context.Context, orgID, id string) (*Group, error)
	ListGroups(ctx context.Context, orgID string) ([]*Group, error)
	RenameGroup(ctx context.Context, orgID, id, name string) error
	DeleteGroup(ctx context.Context, orgID, id string) error
	AddGroupMember(ctx context.Context, m *GroupMember) error
	RemoveGroupMember(ctx context.Context, orgID, groupID, memberType, memberID string) error
	ListGroupMembers(ctx context.Context, orgID, groupID string) ([]*GroupMember, error)
	ListMemberGroups(ctx context.Context, orgID, memberType, memberID string) ([]*Group, error)
}

type ServiceAccountStore interface {
	CreateServiceAccount(ctx context.Context, sa *ServiceAccount) error
	GetServiceAccount(ctx context.Context, orgID, id string) (*ServiceAccount, error)
	GetServiceAccountByClientID(ctx context.Context, clientID string) (*ServiceAccount, error)
	ListServiceAccounts(ctx context.Context, orgID string) ([]*ServiceAccount, error)
	UpdateServiceAccount(ctx context.Context, sa *ServiceAccount) error
	DeleteServiceAccount(ctx context.Context, orgID, id string) error
}

type WorkloadIdentityStore interface {
	CreateWorkloadIdentity(ctx context.Context, w *WorkloadIdentity) error
	ListWorkloadIdentities(ctx context.Context, orgID, serviceAccountID string) ([]*WorkloadIdentity, error)
	ListWorkloadIdentitiesByIssuer(ctx context.Context, issuer string) ([]*WorkloadIdentity, error)
	DeleteWorkloadIdentity(ctx context.Context, orgID, serviceAccountID, id string) error
}

type InviteStore interface {
	CreateInvite(ctx context.Context, i *Invite) error
	GetInviteByTokenHash(ctx context.Context, tokenHash string) (*Invite, error)
	ListInvitesByOrg(ctx context.Context, orgID string) ([]*Invite, error)
	ListInvitesByEmail(ctx context.Context, email string) ([]*Invite, error)
	DeleteInvite(ctx context.Context, orgID, id string) error
}

type WebSessionStore interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, idHash string) (*Session, error)
	DeleteSession(ctx context.Context, idHash string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) error
	DeleteUserSessions(ctx context.Context, orgID, userID string) error
	SetMembershipDisabled(ctx context.Context, orgID, userID string, at *time.Time) error
}

type OrgPolicyStore interface {
	GetOrgPolicy(ctx context.Context, orgID string) (*OrgPolicy, error)
	SetOrgPolicy(ctx context.Context, p *OrgPolicy) error
}

type OrgSecretStore interface {
	PutOrgSecret(ctx context.Context, s *OrgSecret) error
	GetOrgSecret(ctx context.Context, orgID, name string) (*OrgSecret, error)
	ListOrgSecrets(ctx context.Context, orgID string) ([]*OrgSecret, error)
	DeleteOrgSecret(ctx context.Context, orgID, name string) error
}

type EgressCredentialStore interface {
	PutEgressCredential(ctx context.Context, c *EgressCredential) error
	GetEgressCredential(ctx context.Context, orgID, name string) (*EgressCredential, error)
	ListEgressCredentials(ctx context.Context, orgID string) ([]*EgressCredential, error)
	DeleteEgressCredential(ctx context.Context, orgID, name string) error
}

type OrgCryptoStore interface {
	EnsureOrgCA(ctx context.Context, ca *OrgCA) (*OrgCA, error)
	GetOrgCA(ctx context.Context, orgID string) (*OrgCA, error)
	GetOrgDecrypt(ctx context.Context, orgID string) (*OrgDecrypt, error)
	SetOrgDecrypt(ctx context.Context, d *OrgDecrypt) error
}

type ServiceStore interface {
	CreateService(ctx context.Context, svc *Service) error
	GetService(ctx context.Context, id string) (*Service, error)
	ListServices(ctx context.Context, orgID string) ([]*Service, error)
	ListServiceDependents(ctx context.Context, orgID, serviceID string) ([]*Service, error)
	ListAllServices(ctx context.Context) ([]*Service, error)
	UpdateService(ctx context.Context, svc *Service) error
	DeleteService(ctx context.Context, id string) error
}

type ConnectionRecordStore interface {
	CreateConnection(ctx context.Context, c *Connection) error
	GetConnection(ctx context.Context, id string) (*Connection, error)
	ListConnections(ctx context.Context, orgID string) ([]*Connection, error)
	ListAllConnections(ctx context.Context) ([]*Connection, error)
	UpdateConnection(ctx context.Context, c *Connection) error
	UpdateConnectionIfCurrent(ctx context.Context, c, expected *Connection) error
	UpdateConnectionState(ctx context.Context, orgID, connectionID string, state json.RawMessage) error
	DeleteConnection(ctx context.Context, id string) error
	DeleteConnectionIfCurrent(ctx context.Context, expected *Connection) error
}

type AuditStore interface {
	AppendAudit(ctx context.Context, e *AuditEvent) error
	ListAudit(ctx context.Context, orgID string, limit int) ([]*AuditEvent, error)
}

type AccessLogStore interface {
	AppendAccessEvents(ctx context.Context, events []*AccessEvent) error
	ListAccessEvents(ctx context.Context, orgID string, q AccessLogQuery) ([]*AccessEvent, error)
	PruneAccessEvents(ctx context.Context, before time.Time) (int64, error)
}

type DataStore interface {
	OrgStore
	AgentStore
	TokenStore
	EndpointStore
	DomainStore
	QuotaStore
	UserStore
	MembershipStore
	GroupStore
	ServiceAccountStore
	WorkloadIdentityStore
	InviteStore
	WebSessionStore
	OrgPolicyStore
	OrgSecretStore
	EgressCredentialStore
	OrgCryptoStore
	ServiceStore
	ConnectionRecordStore
	AuditStore
	AccessLogStore

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

const (
	AccessSourceIngress = "ingress"
	AccessSourceConnect = "connect"
	AccessSourceEgress  = "egress"

	AccessAllow = "allow"
	AccessDeny  = "deny"

	MaxAccessLogLimit     = 1000
	DefaultAccessLogLimit = 200
)

type AccessEvent struct {
	ID            string
	OrgID         string
	At            time.Time
	Source        string
	Decision      string
	Reason        string
	PrincipalType string
	PrincipalID   string
	SessionID     string
	AgentID       string
	ExitAgentID   string
	EndpointID    string
	Destination   string
	PolicyIDs     []string
	ClientIP      string
	Method        string
	Path          string
	Status        int
	Protocol      string
	BytesIn       int64
	BytesOut      int64
	DurationMs    int64
	LatencyMs     int64
}

type AccessLogQuery struct {
	Decision    string
	Source      string
	PrincipalID string
	EndpointID  string
	Since       time.Time
	Until       time.Time
	Limit       int
}

func (q AccessLogQuery) EffectiveLimit() int {
	if q.Limit <= 0 {
		return DefaultAccessLogLimit
	}
	return min(q.Limit, MaxAccessLogLimit)
}

type AgentConn interface {
	AgentID() string
	OpenStream(ctx context.Context, endpointID, kind string, meta map[string]string) (net.Conn, error)
	Close() error
}

type ClusterLocal interface {
	ClusterLocal() bool
}

type ConnectionStore interface {
	AddAgent(conn AgentConn) (superseded AgentConn)
	RemoveAgent(conn AgentConn)
	GetAgent(agentID string) (AgentConn, bool)
	OwnedElsewhere(agentID string) bool

	BindEndpoint(endpointID, agentID string)
	UnbindEndpoint(endpointID string)
	ResolveEndpoint(endpointID string) (AgentConn, bool)

	AddUsage(orgID string, bytes int64)
	Usage(orgID string) int64
}

func FindConnectEndpoint(ctx context.Context, ds DataStore, orgID, ref string) (*Endpoint, error) {
	if ep, err := ds.GetEndpoint(ctx, ref); err == nil {
		if ep.OrgID != orgID {
			return nil, ErrNotFound
		}
		return ep, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	eps, err := ds.ListEndpointsByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	for _, ep := range eps {
		if ep.Policy != nil && ep.Policy.Connect != nil && ep.Policy.Connect.Name == ref {
			return ep, nil
		}
	}
	return nil, ErrNotFound
}
