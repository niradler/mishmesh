package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/health"
	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	nodeConnection = "connection"
	nodeHostname   = "hostname"
	nodeEndpoint   = "endpoint"
	nodeTarget     = "target"
	nodeService    = "service"
	nodeAgent      = "agent"

	edgeRoutes   = "routes"
	edgeServes   = "serves"
	edgeProxies  = "proxies"
	edgeTunnels  = "tunnels"
	edgeWakes    = "wakes"
	edgeRequires = "requires"
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
	for _, svc := range svcs {
		a.addService(b, svc.ID, svc.Name, "")
	}
	for _, svc := range svcs {
		for _, required := range svc.Requires {
			if _, ok := serviceNames[required]; ok {
				b.edge(nodeService+":"+svc.ID, nodeService+":"+required, edgeRequires)
			}
		}
	}
	for _, ep := range eps {
		a.addEndpoint(b, ep, agentNames, serviceNames)
	}
	records, err := a.data.ListConnections(r.Context(), orgID)
	if a.handleErr(w, err) {
		return
	}
	for _, record := range records {
		view, err := a.buildConnectionView(record, a.connectionRecordStatus(record))
		if a.handleErr(w, err) {
			return
		}
		name := record.ID
		_ = json.Unmarshal(view.Name, &name)
		a.addConnection(b, record, name, view.Status, eps)
	}
	writeJSON(w, http.StatusOK, b.result())
}

func topologyHostnameID(connectionID, hostname string) string {
	return nodeHostname + ":" + connectionID + ":" + strings.ToLower(strings.TrimSuffix(hostname, "."))
}

func (a *API) addConnection(b *topologyBuilder, record *store.Connection, name string, status connections.Status, endpoints []*store.Endpoint) {
	connectionID := nodeConnection + ":" + record.ID
	state := status.Health
	if _, ok := stateRank[state]; !ok {
		state = health.StateUnknown
	}
	b.node(topologyNode{ID: connectionID, Kind: nodeConnection, Label: name, State: state, Detail: status.LastError, Ref: &topologyRef{Kind: "connection", ID: record.ID}})
	visible := map[string]*store.Endpoint{}
	for _, endpoint := range endpoints {
		visible[endpoint.ID] = endpoint
	}
	observed := map[string]bool{}
	for _, host := range status.Hostnames {
		if host.EndpointID != "" && visible[host.EndpointID] == nil {
			continue
		}
		observed[host.EndpointID] = true
		hostID := topologyHostnameID(record.ID, host.Host)
		ref := &topologyRef{Kind: "connection", ID: record.ID}
		if host.State == "unclaimed" && host.EndpointID != "" {
			ref = &topologyRef{Kind: "endpoint", ID: host.EndpointID}
		}
		b.node(topologyNode{ID: hostID, Kind: nodeHostname, Label: host.Host, State: accessHostState(host.State), Detail: host.Detail, Ref: ref})
		b.edge(connectionID, hostID, edgeRoutes)
		if host.EndpointID != "" {
			b.edge(hostID, nodeEndpoint+":"+host.EndpointID, edgeServes)
		}
	}
	for _, endpoint := range endpoints {
		if endpoint.Policy == nil {
			continue
		}
		for _, access := range endpoint.Policy.Access {
			if access.ConnectionID != record.ID || observed[endpoint.ID] {
				continue
			}
			hostname := access.Hostname
			if a.connectionRegistry != nil {
				if provider, ok := a.connectionRegistry.Lookup(record.Type); ok {
					if naming, ok := provider.(connections.AccessHostnameProvider); ok {
						if normalized, err := naming.AccessHostname(endpoint.Domain, hostname); err == nil {
							hostname = normalized
						}
					}
				}
			}
			if hostname == "" {
				hostname = endpoint.Domain
			}
			if hostname == "" {
				hostname = endpoint.ID
			}
			hostID := topologyHostnameID(record.ID, hostname)
			b.node(topologyNode{ID: hostID, Kind: nodeHostname, Label: hostname, State: health.StateUnknown, Detail: "not published yet", Ref: &topologyRef{Kind: "connection", ID: record.ID}})
			b.edge(connectionID, hostID, edgeRoutes)
			b.edge(hostID, nodeEndpoint+":"+endpoint.ID, edgeServes)
		}
	}
}

func accessHostState(state string) string {
	switch state {
	case "published":
		return health.StateUp
	case "pending", "unclaimed":
		return health.StateUnknown
	default:
		return health.StateDegraded
	}
}

func (a *API) addEndpoint(b *topologyBuilder, ep *store.Endpoint, agentNames, serviceNames map[string]string) {
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
			if detail != "" {
				detail = "service operation failed; check the service configuration and server diagnostics"
			}
		}
	}
	b.node(topologyNode{ID: nodeID, Kind: nodeService, Label: name, State: state, Detail: detail, Ref: &topologyRef{Kind: "service", ID: id}})
	if epID != "" {
		b.edge(epID, nodeID, edgeWakes)
	}
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
