package authz

import (
	"encoding/json"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"time"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"
)

const (
	OwnerDefault              = "default"
	DefaultDecisionTTLSeconds = 60
	DefaultMaxAuthAge         = 12 * time.Hour
)

type GroupRef struct {
	ID   string
	Name string
}

type Subject struct {
	Ref      EntityRef
	Email    string
	Name     string
	Role     string
	Groups   []GroupRef
	Disabled bool
}

type EndpointEntity struct {
	ID              string
	Kind            string
	Method          string
	ConnectAnyAgent bool
	ConnectAgents   []string
}

type AgentEntity struct {
	ID       string
	Name     string
	Disabled bool
}

type SessionFacts struct {
	ExpiresAt time.Time
	Revoked   bool
}

type Facts struct {
	Now        time.Time
	Subject    *Subject
	Endpoint   *EndpointEntity
	Agent      *AgentEntity
	Exit       *AgentEntity
	Session    *SessionFacts
	MaxAuthAge time.Duration
}

func (a *Authorizer) WithOwner(owner string) *Authorizer {
	clone := *a
	clone.owner = owner
	return &clone
}

func (a *Authorizer) Version() string {
	return a.version
}

func (a *Authorizer) Scope(action Action) CacheScope {
	return a.scopes[action]
}

func (a *Authorizer) Decide(req DecisionRequest, facts Facts) Decision {
	if facts.Now.IsZero() {
		facts.Now = time.Now().UTC()
	}
	base := Decision{Owner: a.owner, PolicyVersion: a.version, Scope: a.scopes[req.Action]}
	deny := func(reason Reason) Decision {
		d := base
		d.Reason = reason
		return d
	}
	if err := req.Validate(); err != nil {
		return deny(ReasonInvalidRequest)
	}
	clientIP, err := parseClientIP(req.Context.ClientIP)
	if err != nil {
		return deny(ReasonInvalidRequest)
	}
	var destination types.Entity
	if req.Resource.Type == EntityDestination {
		destination, err = destinationEntity(req.Resource.ID)
		if err != nil {
			return deny(ReasonInvalidRequest)
		}
	}
	if reason, ok := checkFacts(req, facts); !ok {
		return deny(reason)
	}

	entities := buildEntities(req, facts, destination)
	authTime, stripped := freshAuthTime(req.Context.AuthTime, facts)
	cedarReq := cedar.Request{
		Principal: entityUID(req.Principal),
		Action:    types.NewEntityUID("Action", types.String(string(req.Action))),
		Resource:  entityUID(req.Resource),
		Context:   buildContext(req, facts, clientIP, authTime),
	}
	decision, diag := cedar.Authorize(a.policies, entities, cedarReq)
	if len(diag.Errors) > 0 {
		return deny(ReasonPolicyError)
	}
	out := base
	out.TTLSeconds = DefaultDecisionTTLSeconds
	out.Policies = a.determining(diag.Reasons)
	switch {
	case decision == cedar.Allow:
		out.Allowed = true
		out.Reason = ReasonAllow
	case stripped:
		out.Reason = ReasonAuthStale
		out.TTLSeconds = 0
	case len(diag.Reasons) > 0:
		out.Reason = ReasonPolicyDeny
	default:
		out.Reason = ReasonNoPolicyMatch
	}
	return out
}

func checkFacts(req DecisionRequest, facts Facts) (Reason, bool) {
	if facts.Subject == nil || facts.Subject.Ref != req.Principal {
		return ReasonUnknownEntity, false
	}
	if req.Resource.Type == EntityEndpoint && (facts.Endpoint == nil || facts.Endpoint.ID != req.Resource.ID) {
		return ReasonUnknownEntity, false
	}
	needsAgent := req.Action == ActionAgentConnect || req.Action == ActionEgressConnect
	if needsAgent && facts.Agent == nil {
		return ReasonUnknownEntity, false
	}
	if req.Principal.Type == EntityAgent && (facts.Agent == nil || facts.Agent.ID != req.Principal.ID) {
		return ReasonUnknownEntity, false
	}
	if req.Context.Exit != "" && (facts.Exit == nil || facts.Exit.ID != req.Context.Exit) {
		return ReasonUnknownEntity, false
	}
	if facts.Exit != nil && facts.Exit.Disabled {
		return ReasonAgentDisabled, false
	}
	if facts.Agent != nil && facts.Agent.Disabled {
		return ReasonAgentDisabled, false
	}
	if facts.Subject.Disabled {
		if req.Principal.Type == EntityAgent {
			return ReasonAgentDisabled, false
		}
		return ReasonUserDisabled, false
	}
	if facts.Session != nil {
		if facts.Session.Revoked {
			return ReasonSessionRevoked, false
		}
		if !facts.Now.Before(facts.Session.ExpiresAt) {
			return ReasonSessionExpired, false
		}
	}
	return "", true
}

func freshAuthTime(authTime *time.Time, facts Facts) (*time.Time, bool) {
	if authTime == nil {
		return nil, false
	}
	maxAge := facts.MaxAuthAge
	if maxAge <= 0 {
		maxAge = DefaultMaxAuthAge
	}
	if facts.Now.Sub(*authTime) > maxAge {
		return nil, true
	}
	return authTime, false
}

func parseClientIP(s string) (types.IPAddr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return types.IPAddr{}, err
	}
	return types.IPAddr(netip.PrefixFrom(addr, addr.BitLen())), nil
}

func destinationEntity(id string) (types.Entity, error) {
	normalized, err := NormalizeDestination(id)
	if err != nil {
		return types.Entity{}, err
	}
	if normalized != id {
		return types.Entity{}, invalid("destination %q is not normalized", truncate(id))
	}
	host, portStr, _ := net.SplitHostPort(id)
	port, _ := strconv.Atoi(portStr)
	uid := types.NewEntityUID(types.EntityType(EntityDestination), types.String(id))
	return types.Entity{UID: uid, Attributes: types.NewRecord(types.RecordMap{
		"host": types.String(host),
		"port": types.Long(port),
	})}, nil
}

func entityUID(ref EntityRef) types.EntityUID {
	return types.NewEntityUID(types.EntityType(ref.Type), types.String(ref.ID))
}

func buildEntities(req DecisionRequest, facts Facts, destination types.Entity) types.EntityMap {
	entities := types.EntityMap{}
	subject := facts.Subject
	principal := types.Entity{UID: entityUID(subject.Ref)}
	var parents []types.EntityUID
	if subject.Role != "" && subject.Ref.Type != EntityAgent && subject.Ref.Type != EntityAnonymous {
		role := types.NewEntityUID(types.EntityType(EntityRole), types.String(subject.Role))
		parents = append(parents, role)
		entities[role] = types.Entity{UID: role}
	}
	for _, g := range subject.Groups {
		group := types.NewEntityUID(types.EntityType(EntityGroup), types.String(g.ID))
		parents = append(parents, group)
		entities[group] = types.Entity{UID: group, Attributes: types.NewRecord(types.RecordMap{"name": types.String(g.Name)})}
	}
	principal.Parents = types.NewEntityUIDSet(parents...)
	switch subject.Ref.Type {
	case EntityUser:
		principal.Attributes = types.NewRecord(types.RecordMap{"email": types.String(subject.Email)})
	case EntityServiceAccount, EntityAgent:
		principal.Attributes = types.NewRecord(types.RecordMap{"name": types.String(subject.Name)})
	}
	entities[principal.UID] = principal

	if facts.Agent != nil {
		agent := types.NewEntityUID(types.EntityType(EntityAgent), types.String(facts.Agent.ID))
		if _, exists := entities[agent]; !exists {
			entities[agent] = types.Entity{UID: agent, Attributes: types.NewRecord(types.RecordMap{"name": types.String(facts.Agent.Name)})}
		}
	}
	if facts.Exit != nil && req.Context.Exit != "" {
		exit := types.NewEntityUID(types.EntityType(EntityAgent), types.String(facts.Exit.ID))
		if _, exists := entities[exit]; !exists {
			entities[exit] = types.Entity{UID: exit, Attributes: types.NewRecord(types.RecordMap{"name": types.String(facts.Exit.Name)})}
		}
	}
	if req.Resource.Type == EntityEndpoint && facts.Endpoint != nil {
		ep := facts.Endpoint
		agents := make([]types.Value, len(ep.ConnectAgents))
		for i, id := range ep.ConnectAgents {
			agents[i] = types.NewEntityUID(types.EntityType(EntityAgent), types.String(id))
		}
		uid := entityUID(req.Resource)
		entities[uid] = types.Entity{UID: uid, Attributes: types.NewRecord(types.RecordMap{
			"kind":              types.String(ep.Kind),
			"method":            types.String(ep.Method),
			"connect_any_agent": types.Boolean(ep.ConnectAnyAgent),
			"connect_agents":    types.NewSet(agents...),
		})}
	}
	if req.Resource.Type == EntityDestination {
		entities[destination.UID] = destination
	}
	return entities
}

func buildContext(req DecisionRequest, facts Facts, clientIP types.IPAddr, authTime *time.Time) types.Record {
	c := req.Context
	m := types.RecordMap{
		"client_ip":   clientIP,
		"now":         types.NewDatetime(facts.Now),
		"auth_method": types.String(string(c.AuthMethod)),
	}
	if authTime != nil {
		m["auth_time"] = types.NewDatetime(*authTime)
	}
	switch req.Action {
	case ActionEndpointAccess, ActionHTTPRequest:
		m["host"] = types.String(c.Host)
		m["tls"] = types.Boolean(c.TLS)
		if req.Action == ActionHTTPRequest {
			m["method"] = types.String(c.Method)
			m["path"] = types.String(c.Path)
		}
	case ActionAgentConnect, ActionEgressConnect:
		m["agent"] = types.NewEntityUID(types.EntityType(EntityAgent), types.String(facts.Agent.ID))
		if req.Action == ActionAgentConnect {
			m["protocol"] = types.String(c.Protocol)
		} else {
			if c.SNI != "" {
				m["sni"] = types.String(c.SNI)
			}
			if c.Exit != "" {
				m["exit"] = types.NewEntityUID(types.EntityType(EntityAgent), types.String(c.Exit))
			}
		}
	}
	return types.NewRecord(m)
}

func (a *Authorizer) determining(reasons []cedar.DiagnosticReason) []DeterminingPolicy {
	if len(reasons) == 0 {
		return nil
	}
	out := make([]DeterminingPolicy, 0, len(reasons))
	for _, r := range reasons {
		p := a.policies.Get(r.PolicyID)
		if p == nil {
			continue
		}
		dp := DeterminingPolicy{ID: policyName(r.PolicyID, p)}
		for k, v := range p.Annotations() {
			if k == "id" {
				continue
			}
			if dp.Annotations == nil {
				dp.Annotations = map[string]string{}
			}
			dp.Annotations[string(k)] = string(v)
		}
		out = append(out, dp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func computeScopes(ps *cedar.PolicySet) map[Action]CacheScope {
	scopes := map[Action]CacheScope{}
	for _, p := range ps.All() {
		pj, err := parsePolicyJSON(p)
		if err != nil {
			for _, act := range DataPlaneActions() {
				scopes[act] = CacheScope{Path: true, Method: true}
			}
			return scopes
		}
		var refs CacheScope
		for _, cond := range pj.Conditions {
			var node any
			if err := json.Unmarshal(cond, &node); err != nil {
				refs = CacheScope{Path: true, Method: true}
				break
			}
			collectContextRefs(node, &refs)
		}
		if refs == (CacheScope{}) {
			continue
		}
		scope := scopeFromJSON(pj)
		for _, act := range DataPlaneActions() {
			if scope.covers(act) {
				s := scopes[act]
				s.Path = s.Path || refs.Path
				s.Method = s.Method || refs.Method
				scopes[act] = s
			}
		}
	}
	return scopes
}

func collectContextRefs(node any, refs *CacheScope) {
	switch n := node.(type) {
	case map[string]any:
		if isContextVar(n) {
			refs.Path, refs.Method = true, true
			return
		}
		for _, key := range []string{".", "has"} {
			access, ok := n[key].(map[string]any)
			if !ok || len(n) != 1 {
				continue
			}
			if left, ok := access["left"].(map[string]any); ok && isContextVar(left) {
				switch access["attr"] {
				case "path":
					refs.Path = true
				case "method":
					refs.Method = true
				}
				return
			}
		}
		for _, v := range n {
			collectContextRefs(v, refs)
		}
	case []any:
		for _, v := range n {
			collectContextRefs(v, refs)
		}
	}
}

func isContextVar(n map[string]any) bool {
	v, ok := n["Var"]
	return ok && len(n) == 1 && v == "context"
}
