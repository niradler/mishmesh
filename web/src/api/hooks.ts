import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryOptions,
} from "@tanstack/react-query";
import { api, apiRequest } from "./client";
import type {
  Agent,
  AuditEvent,
  AuthConfig,
  CreateAgentResponse,
  CreateEndpointRequest,
  Endpoint,
  EndpointPolicy,
  Me,
  Member,
  Org,
  Quota,
  QuotaUpdate,
  Role,
  RotateTokenResponse,
  Status,
  Token,
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
};

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

export function useStatus(orgId?: string) {
  return useQuery({
    queryKey: qk.status(orgId),
    queryFn: () => api.get<Status>("/status", { org_id: orgId }),
  });
}

export function useAgents(orgId?: string, options?: Partial<UseQueryOptions<Agent[]>>) {
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
      api.post<CreateAgentResponse>("/agents", { name: vars.name, org_id: orgId }),
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
      api.patch<Agent>(`/agents/${vars.id}`, { name: vars.name, status: vars.status }),
    onSuccess: (agent) => {
      qc.invalidateQueries({ queryKey: qk.agents(agent.org_id) });
      qc.invalidateQueries({ queryKey: qk.agent(agent.id) });
    },
  });
}

export function useRotateToken() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post<RotateTokenResponse>(`/agents/${id}/rotate`),
    onSuccess: (_data, id) => qc.invalidateQueries({ queryKey: qk.agentTokens(id) }),
  });
}

export function useRevokeAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.post<{ status: string }>(`/agents/${id}/revoke`),
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
  });
}

export function useEndpoint(id: string) {
  return useQuery({
    queryKey: qk.endpoint(id),
    queryFn: () => api.get<Endpoint>(`/endpoints/${id}`),
    enabled: !!id,
  });
}

export function useCreateEndpoint(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: CreateEndpointRequest) => api.post<Endpoint>("/endpoints", vars),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.endpoints(orgId) });
      qc.invalidateQueries({ queryKey: qk.status(orgId) });
    },
  });
}

export function useUpdateEndpointPolicy(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (vars: { id: string; policy: EndpointPolicy }) =>
      api.patch<Endpoint>(`/endpoints/${vars.id}`, { policy: vars.policy }),
    onSuccess: (ep) => {
      qc.invalidateQueries({ queryKey: qk.endpoint(ep.id) });
      qc.invalidateQueries({ queryKey: qk.endpoints(orgId) });
    },
  });
}

export function useDeleteEndpoint(orgId?: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.del<void>(`/endpoints/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.endpoints(orgId) }),
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
      api.post<Member>("/members", { ...vars, org_id: orgId }),
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
      apiRequest<Quota>("/quota", { method: "PUT", body: quota, query: { org_id: orgId } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.quota(orgId) }),
  });
}

export function useAudit(orgId?: string) {
  return useQuery({
    queryKey: qk.audit(orgId),
    queryFn: () => api.get<AuditEvent[]>("/audit", { org_id: orgId }),
    refetchInterval: 10000,
  });
}

export function useOrg(id: string) {
  return useQuery({
    queryKey: ["orgs", id],
    queryFn: () => api.get<Org>(`/orgs/${id}`),
    enabled: !!id,
  });
}
