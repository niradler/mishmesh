import {
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query";
import { api, apiRequest } from "./client";
import type {
  Agent,
  AuditEvent,
  AuthConfig,
  Capabilities,
  CreateAgentResponse,
  CreateEndpointRequest,
  Endpoint,
  Me,
  Invite,
  Member,
  Org,
  Policy,
  PolicyUpdate,
  Quota,
  QuotaUpdate,
  Role,
  RotateTokenResponse,
  Service,
  ServiceInput,
  Status,
  Token,
  Topology,
  TypeDescriptor,
  TypedConnection,
  ConnectionDescriptor,
  ConnectionInput,
  UpdateEndpointRequest,
} from "./types";

export const qk = {
  authConfig: ["auth", "config"] as const,
  me: ["auth", "me"] as const,
  status: (orgId?: string) => ["status", orgId] as const,
  agents: (orgId?: string) => ["agents", orgId] as const,
  agent: (id: string) => ["agents", "detail", id] as const,
  agentEndpoints: (id: string) => ["agents", id, "endpoints"] as const,
  agentTokens: (id: string) => ["agents", id, "tokens"] as const,
  endpoints: (orgId?: string) => ["endpoints", orgId] as const,
  endpoint: (id: string) => ["endpoints", "detail", id] as const,
  members: (orgId?: string) => ["members", orgId] as const,
  quota: (orgId?: string) => ["quota", orgId] as const,
  audit: (orgId?: string) => ["audit", orgId] as const,
  policy: (orgId?: string) => ["policy", orgId] as const,
  services: (orgId?: string) => ["services", orgId] as const,
  service: (id: string) => ["services", "detail", id] as const,
  capabilities: (orgId?: string) => ["capabilities", orgId] as const,
  topology: (orgId?: string) => ["topology", orgId] as const,
};

const NETWORK_QUERY_ROOTS = [
  "endpoints",
  "services",
  "topology",
  "connections",
  "status",
] as const;

function invalidateNetwork(qc: QueryClient) {
  for (const root of NETWORK_QUERY_ROOTS) {
    qc.invalidateQueries({ queryKey: [root] });
  }
  qc.invalidateQueries({ queryKey: ["agents"] });
}

const HEALTH_REFETCH_MS = 5000;
const NETWORK_REFETCH_MS = 10000;

export function useAuthConfig() {
  return useQuery({
    queryKey: qk.authConfig,
    queryFn: () => api.get<AuthConfig>("/auth/config"),
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
}

export function useMe(enabled = true) {
  return useQuery({
    queryKey: qk.me,
    queryFn: () => api.get<Me>("/auth/me"),
    enabled,
    retry: false,
  });
}

export function useLogout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.post<void>("/auth/logout"),
    onSuccess: () => qc.clear(),
  });
}

export function useLogin() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { email: string; password: string }) =>
      api.post<Me>("/auth/login", vars),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.me }),
  });
}

export function useRegister() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: {
      email: string;
      password: string;
      name: string;
      invite_token?: string;
    }) => api.post<Me>("/auth/register", vars),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.me }),
  });
}

export function useSwitchOrg() {
  return useMutation({
    mutationFn: (orgId: string) =>
      api.post<Me>("/auth/switch-org", { org_id: orgId }),
  });
}

export function useStatus(orgId?: string) {
  return useQuery({
    queryKey: qk.status(orgId),
    queryFn: () => api.get<Status>("/status", { org_id: orgId }),
  });
}

export function useAgents(
  orgId?: string,
  options?: Partial<UseQueryOptions<Agent[]>>,
) {
  return useQuery({
    queryKey: qk.agents(orgId),
    queryFn: () => api.get<Agent[]>("/agents", { org_id: orgId }),
    ...options,
  });
}

export function useAgent(id: string) {
  return useQuery({
    queryKey: qk.agent(id),
    queryFn: () => api.get<Agent>(`/agents/${id}`),
    enabled: !!id,
  });
}

export function useAgentEndpoints(id: string) {
  return useQuery({
    queryKey: qk.agentEndpoints(id),
    queryFn: () => api.get<Endpoint[]>(`/agents/${id}/endpoints`),
    enabled: !!id,
  });
}

export function useAgentTokens(id: string) {
  return useQuery({
    queryKey: qk.agentTokens(id),
    queryFn: () => api.get<Token[]>(`/agents/${id}/tokens`),
    enabled: !!id,
  });
}

export function useCreateAgent(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { name: string }) =>
      api.post<CreateAgentResponse>("/agents", {
        name: vars.name,
        org_id: orgId,
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.agents(orgId) });
      qc.invalidateQueries({ queryKey: qk.status(orgId) });
    },
  });
}

export function useUpdateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { id: string; name?: string; status?: string }) =>
      api.patch<Agent>(`/agents/${vars.id}`, {
        name: vars.name,
        status: vars.status,
      }),
    onSuccess: (agent) => {
      qc.invalidateQueries({ queryKey: qk.agents(agent.org_id) });
      qc.invalidateQueries({ queryKey: qk.agent(agent.id) });
    },
  });
}

export function useRotateToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api.post<RotateTokenResponse>(`/agents/${id}/rotate`),
    onSuccess: (_data, id) =>
      qc.invalidateQueries({ queryKey: qk.agentTokens(id) }),
  });
}

export function useRevokeAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api.post<{ status: string }>(`/agents/${id}/revoke`),
    onSuccess: (_data, id) => {
      qc.invalidateQueries({ queryKey: qk.agents() });
      qc.invalidateQueries({ queryKey: qk.agent(id) });
      qc.invalidateQueries({ queryKey: qk.agentTokens(id) });
    },
  });
}

export function useDeleteAgent(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<void>(`/agents/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.agents(orgId) }),
  });
}

export function useEndpoints(orgId?: string) {
  return useQuery({
    queryKey: qk.endpoints(orgId),
    queryFn: () => api.get<Endpoint[]>("/endpoints", { org_id: orgId }),
    refetchInterval: HEALTH_REFETCH_MS,
  });
}

export function useEndpoint(id: string) {
  return useQuery({
    queryKey: qk.endpoint(id),
    queryFn: () => api.get<Endpoint>(`/endpoints/${id}`),
    enabled: !!id,
    refetchInterval: HEALTH_REFETCH_MS,
  });
}

export function useCreateEndpoint() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: CreateEndpointRequest) =>
      api.post<Endpoint>("/endpoints", vars),
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useUpdateEndpoint() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...body }: UpdateEndpointRequest & { id: string }) =>
      api.patch<Endpoint>(`/endpoints/${id}`, body),
    onSuccess: (ep) => {
      qc.setQueryData(qk.endpoint(ep.id), ep);
      invalidateNetwork(qc);
    },
  });
}

export function useDeleteEndpoint() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<void>(`/endpoints/${id}`),
    onSuccess: (_data, id) => {
      qc.removeQueries({ queryKey: qk.endpoint(id) });
      invalidateNetwork(qc);
    },
  });
}

export function useMembers(orgId?: string) {
  return useQuery({
    queryKey: qk.members(orgId),
    queryFn: () => api.get<Member[]>("/members", { org_id: orgId }),
  });
}

export function useAddMember(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { email: string; role: Role }) =>
      api.post<Invite>("/members", vars),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.members(orgId) }),
  });
}

export function useUpdateMemberRole(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { id: string; role: Role }) =>
      api.patch<Member>(`/members/${vars.id}`, { role: vars.role }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.members(orgId) }),
  });
}

export function useRemoveMember(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<void>(`/members/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.members(orgId) }),
  });
}

export function useQuota(orgId?: string) {
  return useQuery({
    queryKey: qk.quota(orgId),
    queryFn: () => api.get<Quota>("/quota", { org_id: orgId }),
  });
}

export function useUpdateQuota(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (quota: QuotaUpdate) =>
      apiRequest<Quota>("/quota", {
        method: "PUT",
        body: quota,
        query: { org_id: orgId },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.quota(orgId) }),
  });
}

export function usePolicy(orgId?: string) {
  return useQuery({
    queryKey: qk.policy(orgId),
    queryFn: () => api.get<Policy>("/policy", { org_id: orgId }),
  });
}

export function useUpdatePolicy(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: PolicyUpdate) =>
      apiRequest<Policy>("/policy", {
        method: "PUT",
        body,
        query: { org_id: orgId },
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.policy(orgId) }),
  });
}

export function useAudit(orgId?: string) {
  return useQuery({
    queryKey: qk.audit(orgId),
    queryFn: () => api.get<AuditEvent[]>("/audit", { org_id: orgId }),
    refetchInterval: 10000,
  });
}

export function useServices(orgId?: string) {
  return useQuery({
    queryKey: qk.services(orgId),
    queryFn: () => api.get<Service[]>("/services", { org_id: orgId }),
    refetchInterval: HEALTH_REFETCH_MS,
  });
}

export function useService(id: string) {
  return useQuery({
    queryKey: qk.service(id),
    queryFn: () => api.get<Service>(`/services/${encodeURIComponent(id)}`),
    enabled: !!id,
    refetchInterval: HEALTH_REFETCH_MS,
  });
}

export function useCreateService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (input: ServiceInput) => api.post<Service>("/services", input),
    onSuccess: () => invalidateNetwork(qc),
  });
}

export function useUpdateService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      input,
      restart = false,
    }: {
      id: string;
      input: ServiceInput;
      restart?: boolean;
    }) =>
      apiRequest<Service>(`/services/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: input,
        query: { restart: restart ? "true" : undefined },
      }),
    onSuccess: (service) => {
      qc.setQueryData(qk.service(service.id), service);
      invalidateNetwork(qc);
    },
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useServiceDrivers(orgId?: string) {
  return useQuery({
    queryKey: ["service-drivers", orgId],
    queryFn: () =>
      api.get<TypeDescriptor[]>("/service-drivers", { org_id: orgId }),
  });
}

export function useDeleteService() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api.del<void>(`/services/${encodeURIComponent(id)}`),
    onSuccess: (_data, id) => {
      qc.removeQueries({ queryKey: qk.service(id) });
      invalidateNetwork(qc);
    },
  });
}

function useServiceAction(action: "start" | "stop") {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api.post<Service>(`/services/${encodeURIComponent(id)}/${action}`),
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useStartService() {
  return useServiceAction("start");
}

export function useStopService() {
  return useServiceAction("stop");
}

export function useCapabilities(orgId?: string) {
  return useQuery({
    queryKey: qk.capabilities(orgId),
    queryFn: () => api.get<Capabilities>("/capabilities", { org_id: orgId }),
    staleTime: 60 * 1000,
  });
}

export function useTopology(orgId?: string) {
  return useQuery({
    queryKey: qk.topology(orgId),
    queryFn: () => api.get<Topology>("/topology", { org_id: orgId }),
    refetchInterval: NETWORK_REFETCH_MS,
  });
}

export function useConnectionTypes(orgId?: string) {
  return useQuery({
    queryKey: ["connection-types", orgId],
    queryFn: () =>
      api.get<ConnectionDescriptor[]>("/connection-types", { org_id: orgId }),
  });
}

export function useConnections(orgId?: string) {
  return useQuery({
    queryKey: ["connections", orgId],
    queryFn: () =>
      api.get<TypedConnection[]>("/connections", { org_id: orgId }),
    refetchInterval: NETWORK_REFETCH_MS,
  });
}

export function useConnection(id: string, orgId?: string) {
  return useQuery({
    queryKey: ["connections", orgId, id],
    queryFn: () =>
      api.get<TypedConnection>(`/connections/${encodeURIComponent(id)}`, {
        org_id: orgId,
      }),
    enabled: Boolean(id),
    refetchInterval: NETWORK_REFETCH_MS,
  });
}

export function useSaveConnection(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, input }: { id?: string; input: ConnectionInput }) =>
      apiRequest<TypedConnection>(
        id ? `/connections/${encodeURIComponent(id)}` : "/connections",
        { method: id ? "PUT" : "POST", body: input, query: { org_id: orgId } },
      ),
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useDeleteConnection(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiRequest<void>(`/connections/${encodeURIComponent(id)}`, {
        method: "DELETE",
        query: { org_id: orgId },
      }),
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useSyncConnection(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiRequest<TypedConnection>(
        `/connections/${encodeURIComponent(id)}/sync`,
        { method: "POST", query: { org_id: orgId } },
      ),
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useAddConnectionHostname(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      hostname,
      service,
    }: {
      id: string;
      hostname: string;
      service: string;
    }) =>
      apiRequest<TypedConnection>(
        `/connections/${encodeURIComponent(id)}/hostnames`,
        { method: "POST", body: { hostname, service }, query: { org_id: orgId } },
      ),
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useRemoveConnectionHostname(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, hostname }: { id: string; hostname: string }) =>
      apiRequest<TypedConnection>(
        `/connections/${encodeURIComponent(id)}/hostnames/${encodeURIComponent(hostname)}`,
        { method: "DELETE", query: { org_id: orgId } },
      ),
    onSettled: () => invalidateNetwork(qc),
  });
}

export function useDetectConnection(orgId?: string) {
  return useMutation({
    mutationKey: ["connection-detect", orgId],
    mutationFn: (input: { type: string; input: Record<string, unknown> }) =>
      apiRequest<Record<string, unknown>>("/connections/detect", {
        method: "POST",
        body: input,
        query: { org_id: orgId },
      }),
  });
}

export function useOrg(id: string) {
  return useQuery({
    queryKey: ["orgs", id],
    queryFn: () => api.get<Org>(`/orgs/${id}`),
    enabled: !!id,
  });
}
