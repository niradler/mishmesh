package controlplane

import (
	"net/http"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	nodeTunnel   = "tunnel"
	nodeHostname = "hostname"
	nodeEndpoint = "endpoint"
	nodeTarget   = "target"
	nodeService  = "service"
	nodeAgent    = "agent"

	edgeRoutes  = "routes"
	edgeServes  = "serves"
	edgeProxies = "proxies"
	edgeTunnels = "tunnels"
	edgeWakes   = "wakes"
)

type topologyNode struct {
	ID     string       `json:"id"`
	Kind   string       `json:"kind"`
	Label  string       `json:"label"`
	State  string       `json:"state"`
	Detail string       `json:"detail,omitempty"`
	Ref    *topologyRef `json:"ref,omitempty"`
}

type topologyRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type topologyEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type topologyDTO struct {
	Nodes []topologyNode `json:"nodes"`
	Edges []topologyEdge `json:"edges"`
}

type topologyBuilder struct {
	nodes []topologyNode
	index map[string]int
	edges []topologyEdge
	seen  map[topologyEdge]bool
}

func newTopologyBuilder() *topologyBuilder {
	return &topologyBuilder{index: map[string]int{}, seen: map[topologyEdge]bool{}}
}

func (b *topologyBuilder) node(n topologyNode) {
	if i, ok := b.index[n.ID]; ok {
		if worseState(n.State, b.nodes[i].State) {
			b.nodes[i].State = n.State
			b.nodes[i].Detail = n.Detail
		}
		return
	}
	b.index[n.ID] = len(b.nodes)
	b.nodes = append(b.nodes, n)
}

func (b *topologyBuilder) has(id string) bool {
	_, ok := b.index[id]
	return ok
}

func (b *topologyBuilder) edge(from, to, kind string) {
	e := topologyEdge{From: from, To: to, Kind: kind}
	if b.seen[e] {
		return
	}
	b.seen[e] = true
	b.edges = append(b.edges, e)
}

func (b *topologyBuilder) result() topologyDTO {
	if b.nodes == nil {
		b.nodes = []topologyNode{}
	}
	if b.edges == nil {
		b.edges = []topologyEdge{}
	}
	return topologyDTO{Nodes: b.nodes, Edges: b.edges}
}

var stateRank = map[string]int{
	health.StateUp:       0,
	health.StateUnknown:  1,
	health.StateSleeping: 2,
	health.StateWaking:   2,
	health.StateDegraded: 3,
	health.StateOffline:  4,
	health.StateDown:     4,
}

func worseState(candidate, current string) bool {
	return stateRank[candidate] > stateRank[current]
}

func (a *API) topologyHandler(w http.ResponseWriter, r *http.Request) {
	orgID := a.orgScope(r)
	eps, err := a.data.ListEndpointsByOrg(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	agents, err := a.data.ListAgents(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	svcs, err := a.data.ListServices(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	agentNames := make(map[string]string, len(agents))
	for _, ag := range agents {
		agentNames[ag.ID] = ag.Name
	}
	serviceNames := make(map[string]string, len(svcs))
	for _, svc := range svcs {
		serviceNames[svc.ID] = svc.Name
	}
	b := newTopologyBuilder()
	hosts := addTunnel(b, a.cloudflareStatus(orgID))
	for _, ep := range eps {
		a.addEndpoint(b, ep, hosts, agentNames, serviceNames)
	}
	writeJSON(w, http.StatusOK, b.result())
}

const tunnelNodeID = nodeTunnel + ":cloudflare"

func addTunnel(b *topologyBuilder, st cloudflare.Status) map[string]cloudflare.HostStatus {
	hosts := make(map[string]cloudflare.HostStatus, len(st.Hostnames))
	if st.Level == cloudflare.LevelNone {
		return hosts
	}
	state, detail := health.StateUnknown, st.LastError
	label := "Cloudflare tunnel"
	if st.Tunnel != nil {
		if st.Tunnel.Name != "" {
			label = "Cloudflare tunnel " + st.Tunnel.Name
		}
		state = tunnelState(st.Tunnel.Status)
	}
	if st.LastError != "" && state == health.StateUp {
		state = health.StateDegraded
	}
	b.node(topologyNode{ID: tunnelNodeID, Kind: nodeTunnel, Label: label, State: state, Detail: detail, Ref: &topologyRef{Kind: "cloudflare"}})
	for _, h := range st.Hostnames {
		hosts[h.Host] = h
		if h.State == cloudflare.StateUnclaimed {
			id := nodeHostname + ":" + h.Host
			ref := &topologyRef{Kind: "cloudflare"}
			if h.EndpointID != "" {
				ref = &topologyRef{Kind: "endpoint", ID: h.EndpointID}
			}
			b.node(topologyNode{ID: id, Kind: nodeHostname, Label: h.Host, State: health.StateUnknown, Detail: h.Detail, Ref: ref})
			b.edge(tunnelNodeID, id, edgeRoutes)
			if h.EndpointID != "" {
				b.edge(id, nodeEndpoint+":"+h.EndpointID, edgeServes)
			}
		}
	}
	return hosts
}

func tunnelState(s string) string {
	switch s {
	case cloudflare.TunnelHealthy:
		return health.StateUp
	case "degraded":
		return health.StateDegraded
	case cloudflare.TunnelDown, "inactive":
		return health.StateDown
	}
	return health.StateUnknown
}

func hostState(h cloudflare.HostStatus) string {
	switch h.State {
	case cloudflare.StatePublished:
		return health.StateUp
	case cloudflare.StatePending, cloudflare.StateUnclaimed:
		return health.StateUnknown
	}
	return health.StateDegraded
}

func (a *API) addEndpoint(b *topologyBuilder, ep *store.Endpoint, hosts map[string]cloudflare.HostStatus, agentNames, serviceNames map[string]string) {
	epID := nodeEndpoint + ":" + ep.ID
	h := a.healthOf(ep.ID)
	state, detail := health.StateUnknown, ""
	if h != nil {
		state, detail = h.State, h.Detail
	}
	label := a.publicURL(ep)
	if label == "" {
		label = ep.ID
	}
	b.node(topologyNode{ID: epID, Kind: nodeEndpoint, Label: label, State: state, Detail: detail, Ref: &topologyRef{Kind: "endpoint", ID: ep.ID}})

	if endpointPublic(ep) {
		hostID := nodeHostname + ":" + ep.Domain
		hs, ok := hosts[ep.Domain]
		hState, hDetail := health.StateUnknown, "not published yet"
		if ok {
			hState, hDetail = hostState(hs), hs.Detail
		}
		b.node(topologyNode{ID: hostID, Kind: nodeHostname, Label: ep.Domain, State: hState, Detail: hDetail, Ref: &topologyRef{Kind: "cloudflare"}})
		if b.has(tunnelNodeID) {
			b.edge(tunnelNodeID, hostID, edgeRoutes)
		}
		b.edge(hostID, epID, edgeServes)
	}

	if ep.Method == store.MethodProxy {
		a.addTargets(b, ep, epID, h)
	} else {
		agID := nodeAgent + ":" + ep.AgentID
		agState := health.StateOffline
		if a.conns != nil {
			if _, ok := a.conns.ResolveEndpoint(ep.ID); ok {
				agState = health.StateUp
			}
		}
		name := agentNames[ep.AgentID]
		if name == "" {
			name = ep.AgentID
		}
		b.node(topologyNode{ID: agID, Kind: nodeAgent, Label: name, State: agState, Ref: &topologyRef{Kind: "agent", ID: ep.AgentID}})
		b.edge(epID, agID, edgeTunnels)
	}

	if id := endpointServiceID(ep); id != "" {
		a.addService(b, id, serviceNames[id], epID)
	}
}

func (a *API) addTargets(b *topologyBuilder, ep *store.Endpoint, epID string, h *health.EndpointHealth) {
	if ep.Policy == nil {
		return
	}
	byTarget := map[string]health.TargetStatus{}
	if h != nil {
		for _, t := range h.Targets {
			byTarget[t.Target] = t
		}
	}
	for _, target := range ep.Policy.ProxyTargets {
		id := nodeTarget + ":" + target
		if parsed, err := proxy.ParseTarget(target); err == nil {
			id = nodeTarget + ":" + parsed.Scheme + "://" + parsed.Addr()
		}
		state, detail := health.StateUnknown, ""
		if t, ok := byTarget[target]; ok {
			state, detail = health.StateDown, t.Error
			switch {
			case t.Healthy:
				state = health.StateUp
			case h.State == health.StateSleeping || h.State == health.StateWaking:
				state, detail = h.State, h.Detail
			}
		}
		b.node(topologyNode{ID: id, Kind: nodeTarget, Label: target, State: state, Detail: detail})
		b.edge(epID, id, edgeProxies)
	}
}

func (a *API) addService(b *topologyBuilder, id, name, epID string) {
	nodeID := nodeService + ":" + id
	state, detail := health.StateUnknown, "service is not loaded"
	if name == "" {
		name, detail = id, "service no longer exists"
	}
	if a.services != nil {
		if st, ok := a.services.Status(id); ok {
			state, detail = serviceNodeState(st.State), st.LastError
		}
	}
	b.node(topologyNode{ID: nodeID, Kind: nodeService, Label: name, State: state, Detail: detail, Ref: &topologyRef{Kind: "service", ID: id}})
	b.edge(epID, nodeID, edgeWakes)
}

func serviceNodeState(s string) string {
	switch s {
	case health.ServiceRunning:
		return health.StateUp
	case health.ServiceSleeping, health.ServiceStopped:
		return health.StateSleeping
	case health.ServiceStarting:
		return health.StateWaking
	case health.ServicePartial:
		return health.StateDegraded
	case health.ServiceError:
		return health.StateDown
	}
	return health.StateUnknown
}
