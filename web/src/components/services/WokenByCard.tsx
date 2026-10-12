import { useState } from "react";
import { Link } from "react-router-dom";
import { Link2, Plus, Unlink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { HelpedStateDot } from "@/components/common/TermBadges";
import { LinkEndpointDialog } from "@/components/services/LinkEndpointDialog";
import { useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { endpointDisplayName, endpointHealthState } from "@/lib/health";
import { mergePolicy, newEndpointLink } from "@/lib/endpoints";
import type { Endpoint, Service } from "@/api/types";

export function WokenByCard({
  service,
  endpoints,
  services,
  canWrite,
}: {
  service: Service;
  endpoints: Endpoint[];
  services: Map<string, Service>;
  canWrite: boolean;
}) {
  const update = useUpdateEndpoint();
  const [linking, setLinking] = useState(false);
  const [pendingUnlink, setPendingUnlink] = useState<Endpoint | null>(null);
  const linkedIds = new Set(service.endpoints ?? []);
  const linked = endpoints.filter((ep) => linkedIds.has(ep.id) || ep.policy?.service_id === service.id);
  const missingIds = [...linkedIds].filter((id) => !linked.some((ep) => ep.id === id));

  const onUnlink = () => {
    if (!pendingUnlink) return;
    update.mutate(
      { id: pendingUnlink.id, policy: mergePolicy(pendingUnlink.policy, { service_id: undefined }) },
      {
        onSuccess: () => {
          toast({ title: "Endpoint unlinked" });
          setPendingUnlink(null);
        },
      },
    );
  };

  const newEndpointHref = newEndpointLink({ method: "proxy", target: service.spec.ready_tcp, serviceId: service.id });

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3 space-y-0">
        <div className="space-y-1.5">
          <CardTitle>Woken by</CardTitle>
          <CardDescription>HTTP endpoints whose requests start this service when it is stopped.</CardDescription>
        </div>
        {canWrite && (
          <div className="flex flex-wrap gap-2">
            <Button size="sm" variant="outline" onClick={() => setLinking(true)}>
              <Link2 className="h-3.5 w-3.5" /> Link existing endpoint
            </Button>
            <Button size="sm" variant="outline" asChild>
              <Link to={newEndpointHref}>
                <Plus className="h-3.5 w-3.5" /> New endpoint for this service
              </Link>
            </Button>
          </div>
        )}
      </CardHeader>
      <CardContent>
        {linked.length === 0 && missingIds.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No endpoint wakes this service yet, so it only starts and stops from here.
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {linked.map((ep) => (
              <li key={ep.id} className="flex flex-wrap items-center justify-between gap-3 py-2">
                <div className="flex flex-wrap items-center gap-3">
                  <Link to={`/endpoints/${ep.id}`} className="font-mono text-sm hover:underline">
                    {endpointDisplayName(ep)}
                  </Link>
                  <HelpedStateDot scope="endpoint" state={endpointHealthState(ep)} />
                </div>
                {canWrite && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      update.reset();
                      setPendingUnlink(ep);
                    }}
                  >
                    <Unlink className="h-3.5 w-3.5" /> Unlink
                  </Button>
                )}
              </li>
            ))}
            {missingIds.map((id) => (
              <li key={id} className="py-2">
                <Link to={`/endpoints/${id}`} className="font-mono text-sm hover:underline">
                  {id}
                </Link>
              </li>
            ))}
          </ul>
        )}
      </CardContent>
      <LinkEndpointDialog service={service} endpoints={endpoints} services={services} open={linking} onOpenChange={setLinking} />
      <ConfirmDialog
        open={pendingUnlink !== null}
        onOpenChange={(open) => !open && setPendingUnlink(null)}
        title="Unlink endpoint?"
        description={
          pendingUnlink && (
            <>
              Requests to <span className="font-mono">{endpointDisplayName(pendingUnlink)}</span> will no longer start{" "}
              {service.name}. The endpoint keeps serving traffic.
            </>
          )
        }
        confirmLabel="Unlink"
        pendingLabel="Unlinking…"
        pending={update.isPending}
        error={update.error ? errorMessage(update.error) : undefined}
        onConfirm={onUnlink}
      />
    </Card>
  );
}
