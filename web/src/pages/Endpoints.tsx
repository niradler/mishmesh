import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Plus, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { HelpedStateDot, KindBadge, MethodBadge } from "@/components/common/TermBadges";
import { EndpointPath } from "@/components/endpoints/EndpointPath";
import { CreateEndpointDialog, type CreateMethod, type EndpointPrefill } from "@/components/endpoints/CreateEndpointDialog";
import { useAgents, useDeleteEndpoint, useEndpoints, useServices } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { endpointDisplayName, endpointHealthState } from "@/lib/health";
import type { Agent, Endpoint, Service } from "@/api/types";

const PREFILL_PARAMS = ["new", "method", "domain", "target", "service_id", "public_access"];

function prefillFromSearch(search: URLSearchParams): EndpointPrefill {
  const method = search.get("method");
  return {
    method: method === "native" || method === "proxy" ? (method as CreateMethod) : undefined,
    domain: search.get("domain") ?? undefined,
    target: search.get("target") ?? undefined,
    serviceId: search.get("service_id") ?? undefined,
    publicAccess: search.get("public_access") === "cloudflare",
  };
}

export function Endpoints() {
  const { currentOrgId, canWrite } = useSession();
  const endpoints = useEndpoints(currentOrgId);
  const agents = useAgents(currentOrgId);
  const services = useServices(currentOrgId);
  const remove = useDeleteEndpoint();
  const [search, setSearch] = useSearchParams();
  const [manualOpen, setManualOpen] = useState(false);
  const [pendingDelete, setPendingDelete] = useState<Endpoint | null>(null);

  const requestedFromLink = search.get("new") === "1";
  const createOpen = canWrite && (manualOpen || requestedFromLink);
  const prefill = useMemo(() => (requestedFromLink ? prefillFromSearch(search) : undefined), [requestedFromLink, search]);

  const agentsById = useMemo(() => new Map<string, Agent>((agents.data ?? []).map((a) => [a.id, a])), [agents.data]);
  const servicesById = useMemo(() => new Map<string, Service>((services.data ?? []).map((s) => [s.id, s])), [services.data]);

  const setCreateOpen = (open: boolean) => {
    setManualOpen(open);
    if (!open && requestedFromLink) {
      const next = new URLSearchParams(search);
      PREFILL_PARAMS.forEach((key) => next.delete(key));
      setSearch(next, { replace: true });
    }
  };

  const reserveButton = canWrite ? (
    <Button onClick={() => setCreateOpen(true)}>
      <Plus className="h-4 w-4" /> Reserve endpoint
    </Button>
  ) : undefined;

  const onConfirmDelete = () => {
    if (!pendingDelete) return;
    remove.mutate(pendingDelete.id, {
      onSuccess: () => {
        toast({ title: "Endpoint deleted" });
        setPendingDelete(null);
      },
    });
  };

  return (
    <div>
      <PageHeader
        title="Endpoints"
        description="Stable addresses that route inbound traffic to an agent or straight to proxy targets."
        actions={reserveButton}
      />
      <Card>
        <CardContent className="pt-4">
          {endpoints.isLoading ? (
            <LoadingState label="Loading endpoints" />
          ) : endpoints.isError ? (
            <ErrorState error={endpoints.error} />
          ) : !endpoints.data || endpoints.data.length === 0 ? (
            <EmptyState
              title="No endpoints"
              description="Reserve an endpoint to expose a service at a stable address."
              action={reserveButton}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Address</TableHead>
                  <TableHead>Path</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Method</TableHead>
                  <TableHead>Health</TableHead>
                  {canWrite && <TableHead className="w-10" />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {endpoints.data.map((ep) => (
                  <TableRow key={ep.id}>
                    <TableCell>
                      <Link to={`/endpoints/${ep.id}`} className="font-mono text-xs hover:underline">
                        {endpointDisplayName(ep)}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <EndpointPath endpoint={ep} agents={agentsById} services={servicesById} />
                    </TableCell>
                    <TableCell>
                      <KindBadge kind={ep.kind} />
                    </TableCell>
                    <TableCell>
                      <MethodBadge method={ep.method} />
                    </TableCell>
                    <TableCell title={ep.health?.detail}>
                      <HelpedStateDot scope="endpoint" state={endpointHealthState(ep)} />
                    </TableCell>
                    {canWrite && (
                      <TableCell className="text-right">
                        <Button
                          variant="ghost"
                          size="icon"
                          aria-label={`Delete ${endpointDisplayName(ep)}`}
                          onClick={() => {
                            remove.reset();
                            setPendingDelete(ep);
                          }}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <CreateEndpointDialog open={createOpen} onOpenChange={setCreateOpen} prefill={prefill} />
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => !open && setPendingDelete(null)}
        title="Delete endpoint?"
        description={
          pendingDelete && (
            <>
              <span className="font-mono">{endpointDisplayName(pendingDelete)}</span> stops accepting traffic and its address is
              released.
              {pendingDelete.policy?.public_access === "cloudflare" && " It is also unpublished from Cloudflare."} This cannot be
              undone.
            </>
          )
        }
        pending={remove.isPending}
        error={remove.error ? errorMessage(remove.error) : undefined}
        onConfirm={onConfirmDelete}
      />
    </div>
  );
}
