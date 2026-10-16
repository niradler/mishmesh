import { Link } from "react-router-dom";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { HelpedStateBadge } from "@/components/common/TermBadges";
import { useEndpoints, useServices } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { endpointHealthState, formatLatency } from "@/lib/health";
import { newEndpointLink } from "@/lib/endpoints";
import type { ConnectionStatus } from "@/api/types";

export function HostnamesTable({
  hostnames,
  connectionId,
  canWrite,
}: {
  hostnames: NonNullable<ConnectionStatus["hostnames"]>;
  connectionId: string;
  canWrite: boolean;
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const endpoints = useEndpoints(currentOrgId);
  const services = useServices(currentOrgId);
  const endpointsById = new Map(
    (endpoints.data ?? []).map((endpoint) => [endpoint.id, endpoint]),
  );
  const servicesById = new Map(
    (services.data ?? []).map((service) => [service.id, service]),
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          Hostnames{" "}
          <span className="text-sm font-normal text-muted-foreground">
            ({hostnames.length})
          </span>
        </CardTitle>
        <p className="text-xs text-muted-foreground">
          Publication shows the tunnel route. Endpoint health shows whether its
          backend is available.
        </p>
      </CardHeader>
      <CardContent>
        {hostnames.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No observed hostnames.
          </p>
        ) : (
          <div>
            {endpoints.isError && (
              <p role="alert" className="mb-3 text-sm text-destructive">
                Endpoint health could not be loaded. Hostname publication is
                still shown.
              </p>
            )}
            <div
              className="hidden grid-cols-[minmax(0,1.2fr)_8rem_9rem_minmax(0,1fr)] gap-4 border-b pb-2 text-xs text-muted-foreground lg:grid"
              aria-hidden="true"
            >
              <span>Hostname</span>
              <span>Publication</span>
              <span>Endpoint health</span>
              <span>Routes to</span>
            </div>
            <ul className="divide-y">
              {hostnames.map((host) => {
                const endpoint = endpointsById.get(host.endpoint_id ?? "");
                const serviceId = endpoint?.policy?.service_id;
                const service = servicesById.get(serviceId ?? "");
                const targets = endpoint?.policy?.proxy_targets?.join(", ");
                let unavailableHealth = "No endpoint";
                if (host.endpoint_id)
                  unavailableHealth = endpoints.isLoading
                    ? "Loading health…"
                    : "Health unavailable";
                return (
                  <li
                    key={`${host.host}:${host.endpoint_id ?? ""}`}
                    className="grid min-w-0 grid-cols-2 gap-x-4 gap-y-2 py-3 text-sm lg:grid-cols-[minmax(0,1.2fr)_8rem_9rem_minmax(0,1fr)]"
                  >
                    <div className="col-span-2 min-w-0 self-center sm:col-span-1">
                      {host.endpoint_id ? (
                        <Link
                          className="block truncate font-medium hover:underline focus-visible:underline"
                          title={host.host}
                          aria-label={`Open endpoint ${host.host}`}
                          to={`/endpoints/${encodeURIComponent(host.endpoint_id)}`}
                        >
                          {host.host}
                        </Link>
                      ) : (
                        <span
                          className="block truncate font-medium"
                          title={host.host}
                        >
                          {host.host}
                        </span>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="sr-only">Publication: </span>
                      <HelpedStateBadge scope="hostname" state={host.state} />
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="sr-only">Endpoint health: </span>
                      {endpoint ? (
                        <>
                          <HelpedStateBadge
                            scope="endpoint"
                            state={endpointHealthState(endpoint)}
                          />
                          {endpoint.health?.latency_ms !== undefined && (
                            <span className="text-xs text-muted-foreground">
                              {formatLatency(endpoint.health.latency_ms)}
                            </span>
                          )}
                        </>
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          {unavailableHealth}
                        </span>
                      )}
                    </div>
                    <div className="col-span-2 min-w-0 self-center text-xs text-muted-foreground sm:col-span-1">
                      {endpoint && (
                        <>
                          <p
                            className="truncate"
                            title={targets || endpoint.agent_id}
                          >
                            <span className="uppercase">{endpoint.kind}</span>{" "}
                            {endpoint.method} {targets || endpoint.agent_id}
                          </p>
                          {serviceId ? (
                            <Link
                              className="inline-block max-w-full truncate hover:underline"
                              to={`/services/${encodeURIComponent(serviceId)}`}
                              title={service?.name ?? serviceId}
                            >
                              Service:{" "}
                              {service?.name ??
                                endpoint.health?.service ??
                                serviceId}
                            </Link>
                          ) : (
                            <span>Direct backend · no auto-start</span>
                          )}
                        </>
                      )}
                      {host.state === "unclaimed" && canWrite && (
                        <Link
                          className="underline"
                          to={newEndpointLink({
                            method: "proxy",
                            domain: host.host,
                            connectionId,
                          })}
                        >
                          Reserve endpoint
                        </Link>
                      )}
                    </div>
                    {(host.detail || host.fix) && (
                      <p className="col-span-2 break-words text-xs text-muted-foreground lg:col-span-4">
                        {host.detail}
                        {host.detail && host.fix ? " " : ""}
                        {host.fix}
                      </p>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
