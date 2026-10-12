import type {
  Cloudflare,
  CloudflareConnection,
  CloudflareConnectorStatus,
  CloudflareInput,
  CloudflareHostname,
  CloudflareTunnel,
} from "@/api/types";

export const ATTENTION_HOST_STATES = ["pending", "conflict", "error"] as const;

export function isCloudflareConnected(cloudflare?: Cloudflare | null): boolean {
  return !!cloudflare && cloudflare.level !== "none" && !!cloudflare.connection;
}

export function tunnelConnectionCount(tunnel?: CloudflareTunnel | null): number {
  return tunnel?.connections ?? 0;
}

export function connectorState(connector?: CloudflareConnectorStatus | null): string {
  if (!connector) return "unknown";
  if (!connector.reachable) return "down";
  return connector.ready_connections > 0 ? "connected" : "degraded";
}

export function connectorLabel(connector?: CloudflareConnectorStatus | null): string {
  if (!connector) return "no data";
  if (!connector.reachable) return "unreachable";
  const count = connector.ready_connections;
  return `${count} ready connection${count === 1 ? "" : "s"}`;
}

export function cloudflareHostnames(cloudflare?: Cloudflare | null): CloudflareHostname[] {
  return cloudflare?.status?.hostnames ?? [];
}

export function hostnameRecord(cloudflare: Cloudflare | null | undefined, host?: string): CloudflareHostname | undefined {
  if (!host) return undefined;
  const wanted = host.toLowerCase();
  return cloudflareHostnames(cloudflare).find((h) => h.host.toLowerCase() === wanted);
}

export function needsAttention(host: CloudflareHostname): boolean {
  return (ATTENTION_HOST_STATES as readonly string[]).includes(host.state);
}

export const DEFAULT_METRICS_ADDR = "127.0.0.1:20241";

export const DEFAULT_CONNECTION_NAME = "lab";

export function inputFromConnection(connection: CloudflareConnection, changes: Partial<CloudflareInput> = {}): CloudflareInput {
  return {
    name: connection.name,
    connector: connection.connector,
    metrics_addr: connection.metrics_addr,
    origin: connection.origin,
    access_app_id: connection.access_app_id ?? "",
    prune: connection.prune,
    ...changes,
  };
}
