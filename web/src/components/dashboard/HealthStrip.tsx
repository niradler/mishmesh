import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { Card, CardContent } from "@/components/ui/card";
import { StateDot } from "@/components/common/StatusDot";
import { connectorState, isCloudflareConnected } from "@/lib/cloudflare";
import { endpointHealthState } from "@/lib/health";
import type { Cloudflare, Endpoint, HealthState, Service, ServiceState } from "@/api/types";

const HEALTH_ORDER: HealthState[] = ["up", "down", "degraded", "sleeping", "waking", "offline", "unknown"];
const SERVICE_ORDER: ServiceState[] = ["running", "starting", "sleeping", "stopped", "partial", "error", "unknown"];

function StripLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className="inline-flex items-center rounded-md px-2 py-1 text-sm hover:bg-accent">
      {children}
    </Link>
  );
}

function StripGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-1">
      <span className="pr-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</span>
      {children}
    </div>
  );
}

function countBy<T, K>(items: T[], key: (item: T) => K): Map<K, number> {
  const counts = new Map<K, number>();
  items.forEach((item) => {
    const value = key(item);
    counts.set(value, (counts.get(value) ?? 0) + 1);
  });
  return counts;
}

export function HealthStrip({
  endpoints,
  services,
  cloudflare,
}: {
  endpoints: Endpoint[];
  services: Service[];
  cloudflare?: Cloudflare;
}) {
  const endpointCounts = countBy(endpoints, endpointHealthState);
  const serviceCounts = countBy(services, (service) => service.status.state);
  const endpointStates = HEALTH_ORDER.filter((state) => endpointCounts.has(state));
  const serviceStates = SERVICE_ORDER.filter((state) => serviceCounts.has(state));
  const connected = isCloudflareConnected(cloudflare);
  const hostnames = cloudflare?.status?.hostnames ?? [];
  const published = hostnames.filter((host) => host.state === "published").length;

  if (endpointStates.length === 0 && serviceStates.length === 0 && !connected) return null;

  return (
    <Card className="mb-4">
      <CardContent className="flex flex-wrap items-center gap-x-6 gap-y-2 p-3">
        {endpointStates.length > 0 && (
          <StripGroup label="Endpoints">
            {endpointStates.map((state) => (
              <StripLink key={state} to="/endpoints">
                <StateDot state={state} label={`${endpointCounts.get(state)} ${state}`} />
              </StripLink>
            ))}
          </StripGroup>
        )}
        {serviceStates.length > 0 && (
          <StripGroup label="Services">
            {serviceStates.map((state) => (
              <StripLink key={state} to="/services">
                <StateDot state={state} label={`${serviceCounts.get(state)} ${state}`} />
              </StripLink>
            ))}
          </StripGroup>
        )}
        {connected && (
          <StripGroup label="Cloudflare">
            <StripLink to="/cloudflare">
              <StateDot
                state={connectorState(cloudflare?.status?.connector)}
                label={`${cloudflare?.connection?.name} · ${published}/${hostnames.length} published`}
              />
            </StripLink>
          </StripGroup>
        )}
      </CardContent>
    </Card>
  );
}
