export type Role = "owner" | "admin" | "member";

export type AgentStatus = "active" | "disabled" | "revoked";

export type EndpointKind = "http" | "tcp" | "tls";

export type EndpointLifecycle = "ephemeral" | "reserved";

export type EndpointMethod = "native" | "ssh" | "proxy";

export type LoadBalanceMode = "" | "round_robin" | "failover";

export type PublicAccess = "" | "cloudflare";

export type HealthState = "up" | "down" | "degraded" | "sleeping" | "waking" | "offline" | "unknown";

export type ServiceState = "running" | "stopped" | "sleeping" | "starting" | "partial" | "error" | "unknown";

export type TopologyNodeKind = "tunnel" | "hostname" | "endpoint" | "target" | "service" | "agent";

export type TopologyEdgeKind = "routes" | "serves" | "proxies" | "tunnels" | "wakes";

export interface AuthConfig {
  bootstrap_required?: boolean;
  auth_enabled: boolean;
  password_enabled: boolean;
  password_signup: boolean;
  google_enabled: boolean;
  signup_mode: "org" | "invite";
}

export interface Policy {
  is_default: boolean;
  roles: string[];
  actions: string[];
  matrix: Record<string, Record<string, boolean>>;
  cedar_src: string;
}

export interface PolicyUpdate {
  matrix: Record<string, string[]>;
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
  active_org_id: string;
  role: Role;
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

export interface Invite {
  id: string;
  email: string;
  role: Role;
  invited_by: string;
  created_at: string;
  expires_at: string;
  invite_token?: string;
  invite_url?: string;
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

export interface OriginTLS {
  server_name?: string;
  ca_pem?: string;
  pin_sha256?: string[];
  insecure_skip_verify?: boolean;
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
  basic_auth_hash?: string;
  ip_allow?: string[];
  ip_deny?: string[];
  force_https?: boolean;
  max_body_bytes?: number;
  compression?: boolean;
  oidc?: EndpointOIDC | null;
  proxy_targets?: string[];
  load_balance?: LoadBalanceMode;
  origin_tls?: OriginTLS | null;
  mtls?: EndpointMTLS | null;
  rate_limit?: EndpointRateLimit | null;
  service_id?: string;
  public_access?: PublicAccess;
}

export interface EndpointMTLS {
  client_ca_pem: string;
  allowed_cns?: string[];
}

export interface EndpointRateLimit {
  requests: number;
  period_seconds: number;
  burst?: number;
  scope?: "ip" | "endpoint";
}

export interface EndpointOIDC {
  enabled: boolean;
  issuer: string;
  client_id: string;
  client_secret: string;
  allowed_emails: string[];
  allowed_domains: string[];
}

export interface TargetHealth {
  target: string;
  healthy: boolean;
  latency_ms: number;
  error?: string;
  checked_at: string;
}

export interface EndpointHealth {
  state: HealthState;
  detail?: string;
  checked_at: string;
  latency_ms?: number;
  targets?: TargetHealth[];
  service?: string;
}

export interface Endpoint {
  id: string;
  agent_id: string;
  org_id: string;
  kind: EndpointKind;
  method: EndpointMethod;
  lifecycle: EndpointLifecycle;
  subdomain?: string;
  domain?: string;
  port?: number;
  public_url?: string;
  online: boolean;
  created_at: string;
  policy?: EndpointPolicy | null;
  health?: EndpointHealth | null;
}

export interface UpdateEndpointRequest {
  subdomain?: string;
  domain?: string;
  port?: number;
  policy?: EndpointPolicy;
}

export interface CreateEndpointRequest {
  agent_id?: string;
  kind: EndpointKind;
  method?: EndpointMethod;
  policy?: EndpointPolicy;
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

export type ServiceDriver = "docker" | "compose" | "exec";

export interface DockerSpec {
  containers?: string[];
  label?: string;
}

export interface ComposeSpec {
  project: string;
  services?: string[];
  file?: string;
  bin?: string;
}

export interface ExecSpec {
  start: string[];
  stop: string[];
  status?: string[];
  dir?: string;
  env?: Record<string, string>;
}

export interface ServiceSpec {
  docker?: DockerSpec;
  compose?: ComposeSpec;
  exec?: ExecSpec;
  ready_tcp?: string;
}

export interface ServiceStatus {
  state: ServiceState;
  active: number;
  last_activity?: string;
  last_error?: string;
  started_by?: string;
  updated_at?: string;
}

export interface Service {
  id: string;
  name: string;
  driver: ServiceDriver;
  spec: ServiceSpec;
  idle_timeout_seconds: number;
  start_timeout_seconds: number;
  created_at: string;
  updated_at: string;
  status: ServiceStatus;
  endpoints: string[] | null;
}

export interface ServiceInput {
  name: string;
  driver: ServiceDriver;
  spec: ServiceSpec;
  idle_timeout_seconds: number;
  start_timeout_seconds: number;
}

export interface ServiceDeleteConflict {
  error: string;
  endpoints?: string[];
}

export interface Capabilities {
  host_commands: boolean;
}

export type CloudflareLevel = "none" | "imported" | "managed";

export type CloudflareConnector = "external" | "managed";

export type CloudflareHostState = "published" | "pending" | "conflict" | "error" | "unclaimed";

export interface CloudflareConnection {
  name: string;
  account_id: string;
  tunnel_id: string;
  access_app_id?: string;
  origin: string;
  connector: CloudflareConnector;
  metrics_addr: string;
  prune: boolean;
  api_token_set: boolean;
  tunnel_token_set: boolean;
  updated_at: string;
}

export interface CloudflareTunnel {
  id: string;
  name: string;
  status: string;
  connections: number;
}

export interface CloudflareConnectorStatus {
  reachable: boolean;
  ready_connections: number;
  connector_id?: string;
  version?: string;
  edge_locations: string[] | null;
  error?: string;
}

export interface CloudflareSupervisor {
  running: boolean;
  pid?: number;
  restarts: number;
  last_exit?: string;
  ready: boolean;
}

export interface CloudflareHostname {
  host: string;
  endpoint_id?: string;
  state: CloudflareHostState;
  ingress: boolean;
  service?: string;
  dns: string;
  dns_detail?: string;
  access: string;
  detail?: string;
  fix?: string;
}

export interface CloudflareStatus {
  level: CloudflareLevel;
  tunnel?: CloudflareTunnel;
  origin?: string;
  connector: CloudflareConnectorStatus;
  supervisor?: CloudflareSupervisor;
  last_sync?: string;
  last_error?: string;
  hostnames: CloudflareHostname[] | null;
}

export interface Cloudflare {
  level: CloudflareLevel;
  connection?: CloudflareConnection;
  status: CloudflareStatus;
}

export interface CloudflareInput {
  name: string;
  tunnel_token?: string;
  api_token?: string;
  clear_api_token?: boolean;
  access_app_id?: string;
  origin?: string;
  connector: CloudflareConnector;
  metrics_addr?: string;
  prune?: boolean;
}

export interface CloudflareDetectRequest {
  metrics_addr: string;
  tunnel_token?: string;
}

export interface CloudflareDetectResult {
  account_id?: string;
  tunnel_id?: string;
  connector?: CloudflareConnectorStatus;
  hostnames: string[] | null;
}

export type TopologyRefKind = "cloudflare" | "endpoint" | "service" | "agent";

export interface TopologyRef {
  kind: TopologyRefKind;
  id?: string;
}

export interface TopologyNode {
  id: string;
  kind: TopologyNodeKind;
  label: string;
  state: string;
  detail?: string;
  ref?: TopologyRef;
}

export interface TopologyEdge {
  from: string;
  to: string;
  kind: TopologyEdgeKind;
}

export interface Topology {
  nodes: TopologyNode[] | null;
  edges: TopologyEdge[] | null;
}

export interface ApiErrorBody {
  error: string;
}
