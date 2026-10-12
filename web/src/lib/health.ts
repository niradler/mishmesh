import type { Endpoint, HealthState } from "@/api/types";

export function endpointHealthState(endpoint: Pick<Endpoint, "health" | "online">): HealthState {
  if (endpoint.health) return endpoint.health.state;
  return endpoint.online ? "up" : "offline";
}

export function formatLatency(latencyMs?: number): string {
  if (latencyMs === undefined || latencyMs === null) return "n/a";
  return `${latencyMs} ms`;
}

export function formatDurationSeconds(totalSeconds: number): string {
  if (!totalSeconds || totalSeconds <= 0) return "none";
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainderMinutes = minutes % 60;
  return remainderMinutes ? `${hours}h ${remainderMinutes}m` : `${hours}h`;
}

export function endpointDisplayName(endpoint: Pick<Endpoint, "public_url" | "subdomain" | "domain" | "port" | "id">): string {
  return endpoint.public_url || endpoint.subdomain || endpoint.domain || (endpoint.port ? `:${endpoint.port}` : endpoint.id);
}
