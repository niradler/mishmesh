package controlplane

import (
	"net/http"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	nodeFrontDoor = "frontdoor"
	nodeHostname  = "hostname"
	nodeEndpoint  = "endpoint"
	nodeTarget    = "target"
	nodeService   = "service"
	nodeAgent     = "agent"

	edgeRoutes  = "routes"
	edgeServes  = "serves"
	edgeProxies = "proxies"
	edgeTunnels = "tunnels"
	edgeWakes   = "wakes"
)

type topologyNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
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
	agentNames := make(map[string]string, len(agents))
	for _, ag := range agents {
		agentNames[ag.ID] = ag.Name
	}
	b := newTopologyBuilder()
	fd, err := a.frontDoorStatus(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	hosts := a.addFrontDoor(b, fd)
	for _, ep := range eps {
		a.addEndpoint(b, ep, hosts, agentNames)
	}
	writeJSON(w, http.StatusOK, b.result())
}

func frontDoorNodeID() string { return nodeFrontDoor + ":" + store.FrontDoorCloudflare }

func (a *API) addFrontDoor(b *topologyBuilder, fd cloudflare.Status) map[string]cloudflare.HostStatus {
	hosts := make(map[string]cloudflare.HostStatus, len(fd.Hostnames))
	if !fd.Enabled {
		return hosts
	}
	state, detail := health.StateUnknown, fd.LastError
	label := "Cloudflare"
	if fd.Tunnel != nil {
		label = "Cloudflare tunnel " + fd.Tunnel.Name
		state = tunnelState(fd.Tunnel.Status)
	}
	if fd.LastError != "" {
		state = health.StateDegraded
	}
	b.node(topologyNode{ID: frontDoorNodeID(), Kind: nodeFrontDoor, Label: label, State: state, Detail: detail})
	for _, h := range fd.Hostnames {
		hosts[h.Host] = h
	}
	return hosts
}

func tunnelState(s string) string {
	switch s {
	case "healthy":
		return health.StateUp
	case "degraded":
		return health.StateDegraded
	case "down", "inactive":
		return health.StateDown
	}
	return health.StateUnknown
}

func hostState(h cloudflare.HostStatus) string {
	switch h.State {
	case "synced":
		return health.StateUp
	case "pending":
		return health.StateUnknown
	}
	return health.StateDegraded
}

func (a *API) addEndpoint(b *topologyBuilder, ep *store.Endpoint, hosts map[string]cloudflare.HostStatus, agentNames map[string]string) {
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
	b.node(topologyNode{ID: epID, Kind: nodeEndpoint, Label: label, State: state, Detail: detail})

	if ep.Policy != nil && ep.Policy.FrontDoor == store.FrontDoorCloudflare && ep.Domain != "" {
		hostID := nodeHostname + ":" + ep.Domain
		hs, ok := hosts[ep.Domain]
		hState, hDetail := health.StateUnknown, "not synced yet"
		if ok {
			hState, hDetail = hostState(hs), hs.Detail
		}
		b.node(topologyNode{ID: hostID, Kind: nodeHostname, Label: ep.Domain, State: hState, Detail: hDetail})
		b.edge(frontDoorNodeID(), hostID, edgeRoutes)
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
		b.node(topologyNode{ID: agID, Kind: nodeAgent, Label: name, State: agState})
		b.edge(epID, agID, edgeTunnels)
	}

	if name := endpointService(ep); name != "" {
		a.addService(b, name, epID)
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

func (a *API) addService(b *topologyBuilder, name, epID string) {
	id := nodeService + ":" + name
	state, detail := health.StateUnknown, "not configured on this server"
	if a.services != nil {
		if st, ok := a.services.Status(name); ok {
			state, detail = serviceNodeState(st.State), st.LastError
		}
	}
	b.node(topologyNode{ID: id, Kind: nodeService, Label: name, State: state, Detail: detail})
	b.edge(epID, id, edgeWakes)
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
