package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/connect/proxy"
	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/egressca"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/orgsecret"
	"github.com/mishmesh/mishmesh/internal/ratelimit"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/services"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/workload"
)

type API struct {
	data               store.DataStore
	conns              store.ConnectionStore
	log                *slog.Logger
	adminToken         string
	defaultQuota       store.Quota
	baseDomain         string
	publicScheme       string
	disablePathRouting bool
	tlsPublicPort      int
	reachInEnabled     bool
	domainVerification bool
	resolver           DNSResolver
	draining           atomic.Bool
	auth               *authConfig
	limiter            ratelimit.Limiter
	trustedProxies     []*net.IPNet
	allowedOrigins     map[string]struct{}
	proxyGuard         *proxy.Guard
	maxOrgsPerUser     int
	services           ServiceControl
	servicesSem        chan struct{}
	memberMutations    orgLocks
	serviceRegistry    *services.Registry
	invalidateIngress  func()
	decisionFlush      func(orgID string)
	endpointHealth     HealthSource
	connections        ConnectionControl
	connectionRegistry *connections.Registry
	secretBox          *secrets.Box
	vault              *orgsecret.Vault
	egressCA           *egressca.Authority
	workload           *workload.Verifier
	hostCommands       bool
	identity           *identity.Service
	apiPublicURL       string

	defaultAuthz *authz.Authorizer
	authzMu      sync.Mutex
	authzCache   map[string]authzEntry
	authzGen     uint64
	authzLoads   map[authzLoadKey]*authzLoad
	denyAuthz    *authz.Authorizer
	policies     *authz.Cache
	now          func() time.Time

	background     sync.WaitGroup
	backgroundCtx  context.Context
	stopBackground context.CancelFunc
}

func (a *API) SetDraining(draining bool) {
	a.draining.Store(draining)
}

func (a *API) SetPublicConfig(baseDomain, scheme string) {
	a.baseDomain = baseDomain
	a.publicScheme = scheme
}

func (a *API) SetPathRouting(enabled bool) {
	a.disablePathRouting = !enabled
}

func (a *API) SetTLSPublicPort(port int) {
	a.tlsPublicPort = port
}

func New(data store.DataStore, conns store.ConnectionStore, adminToken string, log *slog.Logger) *API {
	if log == nil {
		log = slog.Default()
	}
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	return &API{
		serviceRegistry: services.NewBuiltinRegistry(),
		backgroundCtx:   backgroundCtx,
		stopBackground:  stopBackground,
		data:            data,
		conns:           conns,
		log:             log,
		adminToken:      adminToken,
		limiter:         ratelimit.NewMemory(),
		defaultAuthz:    authz.Default(),
		denyAuthz:       authz.DenyAll(),
		policies:        authz.NewCache(authz.DefaultCacheSize),
		authzCache:      make(map[string]authzEntry),
		authzLoads:      make(map[authzLoadKey]*authzLoad),
		servicesSem:     make(chan struct{}, 1),
		now:             time.Now,

		maxOrgsPerUser: DefaultMaxOrgsPerUser,
	}
}

func (a *API) spawn(fn func(context.Context)) {
	a.background.Add(1)
	go func() {
		defer a.background.Done()
		fn(a.backgroundCtx)
	}()
}

func (a *API) Close() {
	a.stopBackground()
	a.background.Wait()
}

type route struct {
	pattern string
	handler http.HandlerFunc
}

func (a *API) routes() []route {
	routes := []route{
		{"GET /api/v1/orgs", a.guard(a.listOrgsHandler)},
		{"POST /api/v1/orgs", a.guard(a.createOrgHandler)},
		{"GET /api/v1/orgs/{id}", a.guard(a.getOrgHandler)},

		{"GET /api/v1/members", a.require(authz.ActionMemberRead, a.listMembersHandler)},
		{"POST /api/v1/members", a.require(authz.ActionMemberManage, a.addMemberHandler)},
		{"PATCH /api/v1/members/{user_id}", a.require(authz.ActionMemberManage, a.updateMemberHandler)},
		{"DELETE /api/v1/members/{user_id}", a.require(authz.ActionMemberManage, a.removeMemberHandler)},
		{"GET /api/v1/invites", a.require(authz.ActionMemberManage, a.listInvitesHandler)},
		{"DELETE /api/v1/invites/{id}", a.require(authz.ActionMemberManage, a.revokeInviteHandler)},

		{"GET /api/v1/groups", a.require(authz.ActionMemberRead, a.listGroupsHandler)},
		{"POST /api/v1/groups", a.require(authz.ActionMemberManage, a.createGroupHandler)},
		{"GET /api/v1/groups/{id}", a.require(authz.ActionMemberRead, a.getGroupHandler)},
		{"PATCH /api/v1/groups/{id}", a.require(authz.ActionMemberManage, a.renameGroupHandler)},
		{"DELETE /api/v1/groups/{id}", a.require(authz.ActionMemberManage, a.deleteGroupHandler)},
		{"POST /api/v1/groups/{id}/members", a.require(authz.ActionMemberManage, a.addGroupMemberHandler)},
		{"DELETE /api/v1/groups/{id}/members/{type}/{member_id}", a.require(authz.ActionMemberManage, a.removeGroupMemberHandler)},

		{"POST /api/v1/agents", a.require(authz.ActionAgentWrite, a.createAgentHandler)},
		{"GET /api/v1/agents", a.require(authz.ActionAgentRead, a.listAgentsHandler)},
		{"GET /api/v1/agents/{id}", a.require(authz.ActionAgentRead, a.getAgentHandler)},
		{"PATCH /api/v1/agents/{id}", a.require(authz.ActionAgentWrite, a.patchAgentHandler)},
		{"DELETE /api/v1/agents/{id}", a.require(authz.ActionAgentWrite, a.deleteAgentHandler)},
		{"POST /api/v1/agents/{id}/rotate", a.require(authz.ActionAgentWrite, a.rotateTokenHandler)},
		{"POST /api/v1/agents/{id}/revoke", a.require(authz.ActionAgentWrite, a.revokeAgentHandler)},
		{"GET /api/v1/agents/{id}/endpoints", a.require(authz.ActionEndpointRead, a.listEndpointsHandler)},
		{"GET /api/v1/agents/{id}/tokens", a.require(authz.ActionAgentRead, a.listTokensHandler)},

		{"GET /api/v1/endpoints", a.require(authz.ActionEndpointRead, a.listOrgEndpointsHandler)},
		{"POST /api/v1/endpoints", a.require(authz.ActionEndpointWrite, a.createEndpointHandler)},
		{"GET /api/v1/endpoints/{id}", a.require(authz.ActionEndpointRead, a.getEndpointHandler)},
		{"PATCH /api/v1/endpoints/{id}", a.require(authz.ActionEndpointWrite, a.patchEndpointHandler)},
		{"DELETE /api/v1/endpoints/{id}", a.require(authz.ActionEndpointWrite, a.deleteEndpointHandler)},
		{"GET /api/v1/endpoints/{id}/sessions", a.require(authz.ActionEndpointRead, a.listEndpointSessionsHandler)},
		{"DELETE /api/v1/endpoints/{id}/sessions", a.require(authz.ActionEndpointWrite, a.revokeEndpointSessionsHandler)},
		{"DELETE /api/v1/endpoints/{id}/sessions/{session_id}", a.require(authz.ActionEndpointWrite, a.revokeEndpointSessionHandler)},

		{"GET /api/v1/capabilities", a.guard(a.capabilitiesHandler)},

		{"GET /api/v1/services", a.require(authz.ActionServiceRead, a.listServicesHandler)},
		{"GET /api/v1/service-drivers", a.require(authz.ActionServiceRead, a.serviceDriversHandler)},
		{"POST /api/v1/services", a.require(authz.ActionServiceWrite, a.createServiceHandler)},
		{"GET /api/v1/services/{id}", a.require(authz.ActionServiceRead, a.getServiceHandler)},
		{"PUT /api/v1/services/{id}", a.require(authz.ActionServiceWrite, a.updateServiceHandler)},
		{"DELETE /api/v1/services/{id}", a.require(authz.ActionServiceWrite, a.deleteServiceHandler)},
		{"POST /api/v1/services/{id}/start", a.require(authz.ActionServiceWrite, a.startServiceHandler)},
		{"POST /api/v1/services/{id}/stop", a.require(authz.ActionServiceWrite, a.stopServiceHandler)},

		{"GET /api/v1/connection-types", a.require(authz.ActionEndpointRead, a.connectionTypesHandler)},
		{"GET /api/v1/connections", a.require(authz.ActionEndpointRead, a.listConnectionsHandler)},
		{"POST /api/v1/connections", a.require(authz.ActionConnectionManage, a.createConnectionHandler)},
		{"GET /api/v1/connections/{id}", a.require(authz.ActionEndpointRead, a.getConnectionHandler)},
		{"PUT /api/v1/connections/{id}", a.require(authz.ActionConnectionManage, a.updateConnectionHandler)},
		{"DELETE /api/v1/connections/{id}", a.require(authz.ActionConnectionManage, a.deleteConnectionHandler)},
		{"POST /api/v1/connections/detect", a.require(authz.ActionConnectionManage, a.detectConnectionHandler)},
		{"POST /api/v1/connections/{id}/sync", a.require(authz.ActionConnectionManage, a.syncConnectionHandler)},
		{"POST /api/v1/connections/{id}/hostnames", a.require(authz.ActionConnectionManage, a.addConnectionHostnameHandler)},
		{"DELETE /api/v1/connections/{id}/hostnames/{hostname}", a.require(authz.ActionConnectionManage, a.removeConnectionHostnameHandler)},
		{"GET /api/v1/topology", a.require(authz.ActionEndpointRead, a.topologyHandler)},

		{"GET /api/v1/quota", a.require(authz.ActionQuotaRead, a.getQuotaHandler)},
		{"PUT /api/v1/quota", a.require(authz.ActionQuotaWrite, a.putQuotaHandler)},

		{"GET /api/v1/audit", a.require(authz.ActionAuditRead, a.listAuditHandler)},
		{"GET /api/v1/access-log", a.require(authz.ActionAuditRead, a.listAccessLogHandler)},
		{"GET /api/v1/status", a.require(authz.ActionStatusRead, a.statusHandler)},

		{"GET /api/v1/service-accounts", a.require(authz.ActionMemberRead, a.listServiceAccountsHandler)},
		{"POST /api/v1/service-accounts", a.require(authz.ActionMemberManage, a.createServiceAccountHandler)},
		{"GET /api/v1/service-accounts/{id}", a.require(authz.ActionMemberRead, a.getServiceAccountHandler)},
		{"PATCH /api/v1/service-accounts/{id}", a.require(authz.ActionMemberManage, a.patchServiceAccountHandler)},
		{"DELETE /api/v1/service-accounts/{id}", a.require(authz.ActionMemberManage, a.deleteServiceAccountHandler)},
		{"POST /api/v1/service-accounts/{id}/rotate", a.require(authz.ActionMemberManage, a.rotateServiceAccountHandler)},
		{"GET /api/v1/service-accounts/{id}/workload-identities", a.require(authz.ActionMemberRead, a.listWorkloadIdentitiesHandler)},
		{"POST /api/v1/service-accounts/{id}/workload-identities", a.require(authz.ActionMemberManage, a.createWorkloadIdentityHandler)},
		{"DELETE /api/v1/service-accounts/{id}/workload-identities/{wid}", a.require(authz.ActionMemberManage, a.deleteWorkloadIdentityHandler)},

		{"GET /api/v1/policy", a.require(authz.ActionPolicyRead, a.getPolicyHandler)},
		{"PUT /api/v1/policy", a.require(authz.ActionPolicyWrite, a.putPolicyHandler)},
		{"GET /api/v1/secrets", a.require(authz.ActionPolicyRead, a.listSecretsHandler)},
		{"PUT /api/v1/secrets/{name}", a.require(authz.ActionPolicyWrite, a.putSecretHandler)},
		{"DELETE /api/v1/secrets/{name}", a.require(authz.ActionPolicyWrite, a.deleteSecretHandler)},
		{"GET /api/v1/egress/credentials", a.require(authz.ActionPolicyRead, a.listEgressCredentialsHandler)},
		{"GET /api/v1/egress/credentials/{name}", a.require(authz.ActionPolicyRead, a.getEgressCredentialHandler)},
		{"PUT /api/v1/egress/credentials/{name}", a.require(authz.ActionPolicyWrite, a.putEgressCredentialHandler)},
		{"DELETE /api/v1/egress/credentials/{name}", a.require(authz.ActionPolicyWrite, a.deleteEgressCredentialHandler)},
		{"GET /api/v1/egress/ca", a.require(authz.ActionPolicyRead, a.getEgressCAHandler)},
		{"GET /api/v1/egress/decrypt", a.require(authz.ActionPolicyRead, a.getEgressDecryptHandler)},
		{"PUT /api/v1/egress/decrypt", a.require(authz.ActionPolicyWrite, a.putEgressDecryptHandler)},
	}
	if a.domainVerification {
		routes = append(routes,
			route{"GET /api/v1/domains", a.require(authz.ActionEndpointRead, a.listDomainsHandler)},
			route{"POST /api/v1/domains", a.require(authz.ActionEndpointWrite, a.createDomainHandler)},
			route{"POST /api/v1/domains/{id}/verify", a.require(authz.ActionEndpointWrite, a.verifyDomainHandler)},
			route{"DELETE /api/v1/domains/{id}", a.require(authz.ActionEndpointWrite, a.deleteDomainHandler)},
		)
	}
	if a.reachInEnabled {
		routes = append(routes,
			route{"POST /api/v1/reach/{agent_id}/http", a.requireMachine(authz.ActionAgentWrite, a.reachInHTTPHandler)},
			route{"GET /api/v1/reach/{agent_id}/stream", a.requireMachine(authz.ActionAgentWrite, a.reachInStreamHandler)},
		)
	}
	return routes
}

func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /readyz", a.ready)

	a.registerAuthRoutes(mux)
	a.registerOAuthRoutes(mux)

	for _, rt := range a.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	mux.HandleFunc("/api/", apiNotFound)
}

func apiNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) ready(w http.ResponseWriter, _ *http.Request) {
	if a.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) handleStoreErr(w http.ResponseWriter, err error, conflictMsg string) bool {
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, conflictMsg)
		return true
	}
	return a.handleErr(w, err)
}

func (a *API) handleErr(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return true
	}
	a.log.Warn("control api error", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
