import type {
  CloudflareConnector,
  CloudflareHostState,
  CloudflareLevel,
  EndpointKind,
  EndpointLifecycle,
  EndpointMethod,
  HealthState,
  ServiceDriver,
  ServiceState,
} from "@/api/types";

export interface GlossaryEntry {
  title: string;
  body: string;
}

export const KIND_LABELS: Record<EndpointKind, string> = {
  http: "HTTP",
  tcp: "TCP",
  udp: "UDP",
  tls: "TLS passthrough",
};

export const METHOD_LABELS: Record<EndpointMethod, string> = {
  native: "Agent",
  proxy: "Proxy",
  ssh: "SSH",
};

export const LIFECYCLE_LABELS: Record<EndpointLifecycle, string> = {
  reserved: "Reserved",
  ephemeral: "Ephemeral",
};

export const DRIVER_LABELS: Record<ServiceDriver, string> = {
  docker: "Docker containers",
  compose: "Docker Compose",
  command: "Host commands",
};

export const KIND_HELP: Record<EndpointKind, GlossaryEntry> = {
  udp: {
    title: "Kind · UDP",
    body: "Agent UDP traffic uses a reserved port. HTTP access providers do not publish UDP.",
  },
  http: {
    title: "Kind · HTTP",
    body: "Routed by hostname. mishmesh sees each request, so auth, headers, wake pages and Cloudflare publishing work.",
  },
  tcp: {
    title: "Kind · TCP",
    body: "Gets its own public port. Raw bytes: no auth, headers or wake page.",
  },
  tls: {
    title: "Kind · TLS passthrough",
    body: "Routed by hostname (SNI); encryption ends at your service, so mishmesh cannot apply HTTP features.",
  },
};

export const METHOD_HELP: Record<EndpointMethod, GlossaryEntry> = {
  native: {
    title: "Method · Agent",
    body: "Traffic travels through a mishmesh-agent's outbound tunnel. Use it when the service runs on a network the server cannot reach. Offline when the agent disconnects.",
  },
  proxy: {
    title: "Method · Proxy",
    body: "The server connects to the targets itself. Use it when the server can reach the service directly. No agent needed.",
  },
  ssh: {
    title: "Method · SSH",
    body: "Created by ssh -R; lives as long as that SSH session. Cannot be created here.",
  },
};

export const LIFECYCLE_HELP: Record<EndpointLifecycle, GlossaryEntry> = {
  reserved: {
    title: "Lifecycle · Reserved",
    body: "The address is permanently yours. It survives disconnects and restarts, and the agent reattaches to it. Everything created in the UI is reserved.",
  },
  ephemeral: {
    title: "Lifecycle · Ephemeral",
    body: "Created by an agent or SSH session and deleted when that connection drops. Do not publish it or link it to a service.",
  },
};

export const HEALTH_STATE_HELP: Record<HealthState, GlossaryEntry> = {
  up: {
    title: "Up",
    body: "Every target answers health checks, or the agent is connected. Nothing to do.",
  },
  down: {
    title: "Down",
    body: "No target answers. Check that the service is running and listening on the target address; the detail line shows the last error.",
  },
  degraded: {
    title: "Degraded",
    body: "Some targets are down but at least one still answers, so traffic keeps flowing. Fix or remove the failing targets.",
  },
  sleeping: {
    title: "Sleeping",
    body: "The linked service is stopped. The next request starts it automatically; nothing to do unless you want it running now.",
  },
  waking: {
    title: "Waking",
    body: "The linked service is starting. Requests wait until it is ready. If it stays here, check the service's readiness address and start timeout.",
  },
  offline: {
    title: "Offline",
    body: "No agent is attached to this endpoint. Start the mishmesh-agent (or SSH session) that serves it.",
  },
  unknown: {
    title: "Unknown",
    body: "No health check has run yet. Wait a few seconds; if it persists, the server is not probing this endpoint.",
  },
};

export const SERVICE_STATE_HELP: Record<ServiceState, GlossaryEntry> = {
  running: {
    title: "Running",
    body: "The service is up and its readiness check passes. It stops after the idle timeout with no requests.",
  },
  stopped: {
    title: "Stopped",
    body: "Stopped outside mishmesh or never started. A request to a linked endpoint starts it, or press Start.",
  },
  sleeping: {
    title: "Sleeping",
    body: "Stopped by mishmesh after the idle timeout. The next request to a linked endpoint wakes it.",
  },
  starting: {
    title: "Starting",
    body: "mishmesh started the service and waits for the readiness address to accept connections.",
  },
  partial: {
    title: "Partial",
    body: "Only some of its containers are running. Press Start to bring the rest up, or Stop to stop them all.",
  },
  error: {
    title: "Error",
    body: "The last start or stop failed. Read the error, fix the driver settings or the host, then press Start again.",
  },
  unknown: {
    title: "Unknown",
    body: "mishmesh could not read the service's state yet. Check that Docker or the status command is reachable from the server.",
  },
};

export const HOSTNAME_STATE_HELP: Record<CloudflareHostState, GlossaryEntry> = {
  published: {
    title: "Published",
    body: "The tunnel routes this hostname to mishmesh and DNS points at the tunnel. Visitors reach the endpoint.",
  },
  pending: {
    title: "Pending",
    body: "The endpoint wants this hostname but Cloudflare is not routing it yet. Apply the fix shown, or sync when management is on.",
  },
  conflict: {
    title: "Conflict",
    body: "Cloudflare routes this hostname somewhere else, or a DNS record already uses it. Resolve it in the Cloudflare dashboard.",
  },
  error: {
    title: "Error",
    body: "Cloudflare rejected the last change for this hostname. Read the detail, fix the cause and sync again.",
  },
  unclaimed: {
    title: "Unclaimed",
    body: "Live on the tunnel, but no endpoint claims it. Create an endpoint with this domain to manage it here.",
  },
};

export const CLOUDFLARE_LEVEL_HELP: Record<CloudflareLevel, GlossaryEntry> = {
  none: {
    title: "Not connected",
    body: "No Cloudflare tunnel is connected. Import one to publish endpoints on the internet.",
  },
  imported: {
    title: "Imported",
    body: "Imported reads your tunnel and shows what to change in Cloudflare. Managed makes the changes for you and needs an API token.",
  },
  managed: {
    title: "Managed",
    body: "Managed makes the changes for you: ingress rules, DNS records and Access hostnames. Imported only reads your tunnel and shows what to change.",
  },
};

export const CONNECTOR_HELP: Record<CloudflareConnector, GlossaryEntry> = {
  external: {
    title: "Connector · External",
    body: "A cloudflared you already run. mishmesh only reads its metrics address and never starts or stops it.",
  },
  managed: {
    title: "Connector · Managed",
    body: "mishmesh runs cloudflared itself with the stored tunnel token. Stop any cloudflared you run for this tunnel first.",
  },
};

export const DRIVER_HELP: Record<ServiceDriver, GlossaryEntry> = {
  docker: {
    title: "Driver · Docker",
    body: "Starts and stops existing containers, picked by name or by a label. The containers must already exist.",
  },
  compose: {
    title: "Driver · Compose",
    body: "Runs docker compose for a project. With a compose file mishmesh runs commands on the host, which needs host command permission.",
  },
  command: {
    title: "Driver · Host commands",
    body: "Runs your start, stop and status commands on the server host. Needs MISHMESH_HOST_COMMANDS and the host:exec permission.",
  },
};

export const HELP = {
  connection: {
    title: "Connection",
    body: "A saved integration with an existing network or workload platform. Each connection has its own credentials, health and supported capabilities.",
  },
  access: {
    title: "Access",
    body: "How an endpoint is published through one or more connections. Public and Private access can point to the same app.",
  },
  driver: {
    title: "Driver",
    body: "How a service starts and stops its workload. Driver fields come from the server’s registered service types.",
  },
  requires: {
    title: "Requires",
    body: "Services that start before this service and stay running while it needs them. Stop dependents before stopping a required service.",
  },
  pinned: {
    title: "Pinned",
    body: "The target's certificate key must match the pin, so a self-signed origin is verified, not trusted blindly.",
  },
  loadBalancing: {
    title: "Load balancing",
    body: "Round robin spreads requests across healthy targets. Failover uses the first healthy target in order.",
  },
  originTls: {
    title: "Origin TLS",
    body: "How mishmesh verifies https:// targets. A pin or a CA certificate verifies a self-signed origin; skipping verification trusts anything.",
  },
  idleTimeout: {
    title: "Idle timeout",
    body: "After this long with no requests, mishmesh stops the service. The next request starts it again. Never keeps it running.",
  },
  startTimeout: {
    title: "Start timeout",
    body: "How long a start may take before mishmesh gives up and marks the service as failed. Waiting requests then get an error.",
  },
  readyTcp: {
    title: "Readiness address",
    body: "A host:port that accepts TCP connections once the service is ready. mishmesh holds requests until it does.",
  },
  proxyTargets: {
    title: "Targets",
    body: "Backends the server connects to directly, as host:port, http://host:port or https://host:port. The port is required.",
  },
  serviceLink: {
    title: "Wake a service",
    body: "A request to this endpoint starts the linked service when it is stopped, and the service stops again after its idle timeout.",
  },
  publicAccess: {
    title: "Public access",
    body: "Publishes the endpoint's custom domain through your Cloudflare tunnel, so it is reachable on the internet without opening ports.",
  },
  subdomain: {
    title: "Subdomain",
    body: "A label under the platform's base domain, such as api. Letters, digits and hyphens only.",
  },
  domain: {
    title: "Custom domain",
    body: "A hostname you own, such as app.example.com. Required for Cloudflare publishing; it becomes the published hostname.",
  },
  port: {
    title: "Port",
    body: "The public TCP port for this endpoint. Leave empty to have one assigned.",
  },
  tunnelToken: {
    title: "Tunnel token",
    body: "Identifies the tunnel and its account. It is stored encrypted and only used when mishmesh runs cloudflared itself.",
  },
  metricsAddr: {
    title: "Metrics address",
    body: "The loopback host:port where cloudflared serves /ready, /config and /metrics. Start cloudflared with --metrics 127.0.0.1:20241.",
  },
  apiToken: {
    title: "API token",
    body: "A Cloudflare API token that lets mishmesh create ingress rules, DNS records and Access hostnames for you. Stored encrypted.",
  },
  accessApp: {
    title: "Access application",
    body: "Optional Cloudflare Access application ID. Published hostnames are added to it so Access protects them.",
  },
  prune: {
    title: "Prune",
    body: "Remove tunnel hostnames that no endpoint wants. Off by default: mishmesh never deletes rules it did not create.",
  },
  origin: {
    title: "Origin",
    body: "Where cloudflared sends traffic for published hostnames: the mishmesh ingress address. Defaults to the server's ingress.",
  },
  dockerSelector: {
    title: "Containers or label",
    body: "List container names, or give one label as key=value to control every container that carries it.",
  },
  execArgv: {
    title: "Commands",
    body: "One argument per line, no shell. The first line is the program. Status must exit 0 while the service runs.",
  },
} satisfies Record<string, GlossaryEntry>;

export function stateHelp(
  scope: "endpoint" | "service" | "hostname",
  state: string,
): GlossaryEntry | undefined {
  if (scope === "endpoint") return HEALTH_STATE_HELP[state as HealthState];
  if (scope === "service") return SERVICE_STATE_HELP[state as ServiceState];
  return HOSTNAME_STATE_HELP[state as CloudflareHostState];
}
