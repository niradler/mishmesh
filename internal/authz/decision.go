package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	ActionEndpointAccess Action = "endpoint:access"
	ActionHTTPRequest    Action = "http:request"
	ActionAgentConnect   Action = "agent:connect"
	ActionEgressConnect  Action = "egress:connect"
)

func DataPlaneActions() []Action {
	return []Action{ActionEndpointAccess, ActionHTTPRequest, ActionAgentConnect, ActionEgressConnect}
}

type EntityType string

const (
	EntityOrg            EntityType = "Org"
	EntityRole           EntityType = "Role"
	EntityGroup          EntityType = "Group"
	EntityUser           EntityType = "User"
	EntityServiceAccount EntityType = "ServiceAccount"
	EntityAgent          EntityType = "Agent"
	EntityAnonymous      EntityType = "Anonymous"
	EntityEndpoint       EntityType = "Endpoint"
	EntityDestination    EntityType = "Destination"
)

const AnonymousID = "anonymous"

type AuthMethod string

const (
	AuthMethodNone              AuthMethod = "none"
	AuthMethodPassword          AuthMethod = "password"
	AuthMethodOIDC              AuthMethod = "oidc"
	AuthMethodBasic             AuthMethod = "basic"
	AuthMethodMTLS              AuthMethod = "mtls"
	AuthMethodClientCredentials AuthMethod = "client_credentials"
	AuthMethodAgentToken        AuthMethod = "agent_token"
)

var knownAuthMethods = map[AuthMethod]bool{
	AuthMethodNone: true, AuthMethodPassword: true, AuthMethodOIDC: true, AuthMethodBasic: true,
	AuthMethodMTLS: true, AuthMethodClientCredentials: true, AuthMethodAgentToken: true,
}

type Reason string

const (
	ReasonAllow          Reason = "allow"
	ReasonInvalidRequest Reason = "invalid_request"
	ReasonUnknownEntity  Reason = "unknown_entity"
	ReasonAgentDisabled  Reason = "agent_disabled"
	ReasonUserDisabled   Reason = "user_disabled"
	ReasonSessionRevoked Reason = "session_revoked"
	ReasonSessionExpired Reason = "session_expired"
	ReasonPolicyDeny     Reason = "policy_deny"
	ReasonNoPolicyMatch  Reason = "no_policy_match"
	ReasonPolicyError    Reason = "policy_error"
	ReasonAuthStale      Reason = "auth_stale"
	ReasonUnavailable    Reason = "unavailable"
)

const (
	MaxDecisionRequestBytes = 8 << 10
	maxPathLength           = 2048
	maxHostLength           = 253
	maxFieldLength          = 256
)

var ErrInvalidRequest = errors.New("invalid decision request")

type EntityRef struct {
	Type EntityType `json:"type"`
	ID   string     `json:"id"`
}

type RequestContext struct {
	ClientIP   string     `json:"client_ip"`
	Host       string     `json:"host,omitempty"`
	TLS        bool       `json:"tls,omitempty"`
	Method     string     `json:"method,omitempty"`
	Path       string     `json:"path,omitempty"`
	Protocol   string     `json:"protocol,omitempty"`
	SNI        string     `json:"sni,omitempty"`
	AuthMethod AuthMethod `json:"auth_method"`
	AuthTime   *time.Time `json:"auth_time,omitempty"`
}

type DecisionRequest struct {
	Principal EntityRef      `json:"principal"`
	Action    Action         `json:"action"`
	Resource  EntityRef      `json:"resource"`
	Context   RequestContext `json:"context"`
}

type DeterminingPolicy struct {
	ID          string            `json:"id"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type CacheScope struct {
	Path   bool `json:"path"`
	Method bool `json:"method"`
}

type Decision struct {
	Allowed       bool                `json:"allowed"`
	Reason        Reason              `json:"reason"`
	Policies      []DeterminingPolicy `json:"policies,omitempty"`
	Owner         string              `json:"owner,omitempty"`
	PolicyVersion string              `json:"policy_version,omitempty"`
	TTLSeconds    int                 `json:"ttl_seconds"`
	Scope         CacheScope          `json:"scope"`
}

type Decider interface {
	Decide(ctx context.Context, req DecisionRequest) (Decision, error)
}

type actionShape struct {
	principals []EntityType
	resource   EntityType
	needsHTTP  bool
	needsProto bool
}

var actionShapes = map[Action]actionShape{
	ActionEndpointAccess: {principals: []EntityType{EntityUser, EntityServiceAccount, EntityAnonymous}, resource: EntityEndpoint},
	ActionHTTPRequest:    {principals: []EntityType{EntityUser, EntityServiceAccount, EntityAnonymous}, resource: EntityEndpoint, needsHTTP: true},
	ActionAgentConnect:   {principals: []EntityType{EntityUser, EntityServiceAccount, EntityAgent}, resource: EntityEndpoint, needsProto: true},
	ActionEgressConnect:  {principals: []EntityType{EntityUser, EntityServiceAccount, EntityAgent}, resource: EntityDestination},
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

func (r DecisionRequest) Validate() error {
	shape, ok := actionShapes[r.Action]
	if !ok {
		return invalid("action %q is not a data-plane action", truncate(string(r.Action)))
	}
	if !containsType(shape.principals, r.Principal.Type) {
		return invalid("principal type %q not allowed for %s", truncate(string(r.Principal.Type)), r.Action)
	}
	if r.Resource.Type != shape.resource {
		return invalid("resource type %q not allowed for %s", truncate(string(r.Resource.Type)), r.Action)
	}
	for _, f := range []struct {
		name  string
		value string
		max   int
	}{
		{"principal.id", r.Principal.ID, maxFieldLength},
		{"resource.id", r.Resource.ID, maxFieldLength},
		{"client_ip", r.Context.ClientIP, maxFieldLength},
	} {
		if f.value == "" {
			return invalid("%s is required", f.name)
		}
		if len(f.value) > f.max {
			return invalid("%s exceeds %d bytes", f.name, f.max)
		}
	}
	for _, f := range []struct {
		name  string
		value string
		max   int
	}{
		{"host", r.Context.Host, maxHostLength},
		{"path", r.Context.Path, maxPathLength},
		{"method", r.Context.Method, maxFieldLength},
		{"protocol", r.Context.Protocol, maxFieldLength},
		{"sni", r.Context.SNI, maxHostLength},
	} {
		if len(f.value) > f.max {
			return invalid("%s exceeds %d bytes", f.name, f.max)
		}
	}
	if shape.needsHTTP && (r.Context.Method == "" || r.Context.Path == "") {
		return invalid("method and path are required for %s", r.Action)
	}
	if shape.needsProto && r.Context.Protocol == "" {
		return invalid("protocol is required for %s", r.Action)
	}
	if !knownAuthMethods[r.Context.AuthMethod] {
		return invalid("unknown auth_method %q", truncate(string(r.Context.AuthMethod)))
	}
	return nil
}

func DecodeDecisionRequest(raw []byte) (DecisionRequest, error) {
	var req DecisionRequest
	if len(raw) > MaxDecisionRequestBytes {
		return req, invalid("request exceeds %d bytes", MaxDecisionRequestBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, invalid("decode: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return req, invalid("trailing data after request")
	}
	return req, nil
}

func containsType(types []EntityType, t EntityType) bool {
	for _, candidate := range types {
		if candidate == t {
			return true
		}
	}
	return false
}

func truncate(s string) string {
	if len(s) > 64 {
		return s[:64] + "..."
	}
	return s
}

const ReauthRequiredReason = "reauth_required"

func (d Decision) NeedsReauth() bool {
	if d.Allowed {
		return false
	}
	if d.Reason == ReasonAuthStale {
		return true
	}
	for _, p := range d.Policies {
		if p.Annotations["reason"] == ReauthRequiredReason {
			return true
		}
	}
	return false
}
