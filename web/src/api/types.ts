export type Role = "owner" | "admin" | "member";

export type AgentStatus = "active" | "disabled" | "revoked";

export type EndpointKind = "http" | "tcp" | "tls";

export type EndpointLifecycle = "ephemeral" | "reserved";

export interface AuthConfig {
  auth_enabled: boolean;
  password_enabled: boolean;
  google_enabled: boolean;
}

export interface Membership {
  org_id: string;
  org_name: string;
  role: Role;
}

export interface Me {
  id: string;
  email: string;
  name: string;
  memberships: Membership[];
}

export interface Org {
  id: string;
  name: string;
  created_at: string;
}

export interface Member {
  user: { id: string; email: string; name: string };
  role: Role;
  created_at: string;
}

export interface Agent {
  id: string;
  org_id: string;
  name: string;
  status: AgentStatus;
  connected: boolean;
  created_at: string;
  last_seen_at?: string | null;
}

export interface CreateAgentResponse {
  agent: Agent;
  token: string;
}

export interface RotateTokenResponse {
  token: string;
}

export interface Token {
  id: string;
  created_at: string;
  revoked_at?: string | null;
}

export interface EndpointPolicy {
  request_headers_add?: Record<string, string>;
  request_headers_remove?: string[];
  response_headers_add?: Record<string, string>;
  response_headers_remove?: string[];
  host_header?: string;
  strip_path_prefix?: string;
  add_path_prefix?: string;
  basic_auth_user?: string;
  basic_auth_password?: string;
  ip_allow?: string[];
  ip_deny?: string[];
  force_https?: boolean;
  max_body_bytes?: number;
  compression?: boolean;
  oidc?: EndpointOIDC | null;
}

export interface EndpointOIDC {
  enabled: boolean;
  issuer: string;
  client_id: string;
  client_secret: string;
  allowed_emails: string[];
  allowed_domains: string[];
}

export interface Endpoint {
  id: string;
  agent_id: string;
  org_id: string;
  kind: EndpointKind;
  lifecycle: EndpointLifecycle;
  subdomain?: string;
  domain?: string;
  port?: number;
  public_url?: string;
  online: boolean;
  created_at: string;
  policy?: EndpointPolicy | null;
}

export interface CreateEndpointRequest {
  agent_id: string;
  kind: EndpointKind;
  lifecycle?: EndpointLifecycle;
  subdomain?: string;
  domain?: string;
  port?: number;
}

export interface EndpointKindCount {
  http: number;
  tcp: number;
  tls: number;
}

export interface StatusQuota {
  max_agents: number;
  max_endpoints: number;
  max_bandwidth_bytes: number;
}

export interface Status {
  agents: { total: number; connected: number };
  endpoints: { total: number; online: number; by_kind: EndpointKindCount };
  usage_bytes: number;
  quota?: StatusQuota;
}

export interface QuotaUsage {
  agents: number;
  endpoints: number;
  bandwidth_bytes: number;
}

export interface Quota {
  max_agents: number;
  max_endpoints: number;
  max_bandwidth_bytes: number;
  usage?: QuotaUsage;
}

export interface QuotaUpdate {
  max_agents: number;
  max_endpoints: number;
  max_bandwidth_bytes: number;
}

export interface AuditEvent {
  id: string;
  actor: string;
  action: string;
  target: string;
  detail: string;
  created_at: string;
}

export interface ApiErrorBody {
  error: string;
}
