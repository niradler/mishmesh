import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { CircleCheck } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { StateDot } from "@/components/common/StatusDot";
import { endpointDisplayName, endpointHealthState } from "@/lib/health";
import { newEndpointLink } from "@/lib/endpoints";
import type { TypedConnection, Endpoint, Service } from "@/api/types";

const PROBLEM_ENDPOINT_STATES = ["down", "offline"];

interface AttentionItem {
  key: string;
  state: string;
  to: string;
  title: ReactNode;
  reason: string;
}

function collectItems(
  endpoints: Endpoint[],
  services: Service[],
  connections: TypedConnection[],
): AttentionItem[] {
  const items: AttentionItem[] = [];
  endpoints.forEach((ep) => {
    const state = endpointHealthState(ep);
    if (!PROBLEM_ENDPOINT_STATES.includes(state)) return;
    items.push({
      key: `endpoint-${ep.id}`,
      state,
      to: `/endpoints/${ep.id}`,
      title: (
        <span className="font-mono text-xs">{endpointDisplayName(ep)}</span>
      ),
      reason: ep.health?.detail || `Endpoint is ${state}.`,
    });
  });
  services.forEach((service) => {
    if (service.status.state !== "error") return;
    items.push({
      key: `service-${service.id}`,
      state: "error",
      to: `/services/${service.id}`,
      title: service.name,
      reason: service.status.last_error || "Service is in an error state.",
    });
  });
  for (const connection of connections) {
    if (
      connection.status.last_error ||
      ["down", "degraded"].includes(connection.status.health)
    )
      items.push({
        key: `connection-${connection.id}`,
        state: connection.status.health,
        to: `/connections/${encodeURIComponent(connection.id)}`,
        title: connection.name,
        reason: connection.status.last_error || "Connection needs attention.",
      });
    for (const host of connection.status.hostnames ?? []) {
      if (["conflict", "error", "pending"].includes(host.state))
        items.push({
          key: `host-${connection.id}-${host.host}`,
          state: host.state,
          to: `/connections/${encodeURIComponent(connection.id)}`,
          title: host.host,
          reason: host.fix || host.detail || `Hostname is ${host.state}.`,
        });
      else if (host.state === "unclaimed")
        items.push({
          key: `host-${connection.id}-${host.host}`,
          state: host.state,
          to: newEndpointLink({
            method: "proxy",
            domain: host.host,
            connectionId: connection.id,
          }),
          title: host.host,
          reason:
            "Observed by this connection but no endpoint claims it. Create an endpoint for it.",
        });
    }
  }
  return items;
}

export function NeedsAttention({
  endpoints,
  services,
  connections,
}: {
  endpoints: Endpoint[];
  services: Service[];
  connections: TypedConnection[];
}) {
  const items = collectItems(endpoints, services, connections);
  return (
    <Card className="mt-6">
      <CardHeader>
        <CardTitle>Needs attention</CardTitle>
        <CardDescription>
          Endpoints that are down, services that failed and access hostnames
          that are not published.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {items.length === 0 ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <CircleCheck className="h-4 w-4 text-success" /> Everything is
            healthy
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {items.map((item) => (
              <li key={item.key}>
                <Link
                  to={item.to}
                  className="flex flex-wrap items-start gap-x-3 gap-y-1 rounded-md px-2 py-2 hover:bg-accent"
                >
                  <StateDot
                    state={item.state}
                    label={item.state}
                    className="w-28 shrink-0 text-xs"
                  />
                  <span className="min-w-0 flex-1">
                    <span className="block text-sm font-medium">
                      {item.title}
                    </span>
                    <span className="block break-words text-xs text-muted-foreground">
                      {item.reason}
                    </span>
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  );
}
