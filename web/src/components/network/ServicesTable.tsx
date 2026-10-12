import { Link } from "react-router-dom";
import { Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { StateBadge } from "@/components/common/StateBadge";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { useEndpoints, useServices, useStartService, useStopService } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { ApiError } from "@/api/client";
import { endpointDisplayName, formatDurationSeconds } from "@/lib/health";
import { formatRelativeTime } from "@/lib/utils";
import type { Endpoint, Service } from "@/api/types";

const STARTABLE_STATES = ["stopped", "sleeping", "partial", "error", "unknown"];
const STOPPABLE_STATES = ["running", "starting", "partial", "error", "unknown"];

function LinkedEndpoints({ ids, endpointsById }: { ids: string[]; endpointsById: Map<string, Endpoint> }) {
  if (ids.length === 0) return <span className="text-muted-foreground">none</span>;
  return (
    <div className="flex flex-col gap-0.5">
      {ids.map((id) => {
        const endpoint = endpointsById.get(id);
        return (
          <Link key={id} to={`/endpoints/${id}`} className="font-mono text-xs hover:underline">
            {endpoint ? endpointDisplayName(endpoint) : id}
          </Link>
        );
      })}
    </div>
  );
}

function ServiceRow({
  service,
  endpointsById,
  canWrite,
  orgId,
}: {
  service: Service;
  endpointsById: Map<string, Endpoint>;
  canWrite: boolean;
  orgId?: string;
}) {
  const start = useStartService(orgId);
  const stop = useStopService(orgId);
  const starting = start.isPending && start.variables === service.name;
  const stopping = stop.isPending && stop.variables === service.name;
  const busy = starting || stopping;

  const onError = (title: string) => (err: unknown) =>
    toast({
      variant: "destructive",
      title,
      description: err instanceof ApiError ? err.message : "Unknown error",
    });

  const onStart = () =>
    start.mutate(service.name, {
      onSuccess: () => toast({ title: "Service started", description: service.name }),
      onError: onError("Start failed"),
    });

  const onStop = () =>
    stop.mutate(service.name, {
      onSuccess: () => toast({ title: "Service stopped", description: service.name }),
      onError: onError("Stop failed"),
    });

  return (
    <TableRow>
      <TableCell className="font-medium">{service.name}</TableCell>
      <TableCell className="text-muted-foreground">{service.driver}</TableCell>
      <TableCell>
        <StateBadge state={service.state} />
      </TableCell>
      <TableCell className="tabular-nums">{service.active}</TableCell>
      <TableCell className="text-muted-foreground">{formatDurationSeconds(service.idle_timeout_seconds)}</TableCell>
      <TableCell className="text-muted-foreground">{formatRelativeTime(service.last_activity)}</TableCell>
      <TableCell className="max-w-xs break-words text-xs text-destructive">{service.last_error ?? ""}</TableCell>
      <TableCell>
        <LinkedEndpoints ids={service.endpoints ?? []} endpointsById={endpointsById} />
      </TableCell>
      {canWrite && (
        <TableCell>
          <div className="flex justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={onStart}
              disabled={busy || !STARTABLE_STATES.includes(service.state)}
            >
              <Play className="h-3.5 w-3.5" /> {starting ? "Starting…" : "Start"}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={onStop}
              disabled={busy || !STOPPABLE_STATES.includes(service.state)}
            >
              <Square className="h-3.5 w-3.5" /> {stopping ? "Stopping…" : "Stop"}
            </Button>
          </div>
        </TableCell>
      )}
    </TableRow>
  );
}

export function ServicesTable() {
  const { currentOrgId, canWrite } = useSession();
  const services = useServices(currentOrgId);
  const endpoints = useEndpoints(currentOrgId);
  const endpointsById = new Map((endpoints.data ?? []).map((ep) => [ep.id, ep]));

  return (
    <Card>
      <CardContent className="pt-4">
        {services.isLoading ? (
          <LoadingState label="Loading services" />
        ) : services.isError ? (
          <ErrorState error={services.error} />
        ) : !services.data || services.data.length === 0 ? (
          <EmptyState
            title="No services"
            description="Services are defined in the host-local services file (MISHMESH_SERVICES_FILE) and must be allowlisted for this organization."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Driver</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Active</TableHead>
                <TableHead>Idle timeout</TableHead>
                <TableHead>Last activity</TableHead>
                <TableHead>Last error</TableHead>
                <TableHead>Endpoints</TableHead>
                {canWrite && <TableHead className="text-right">Actions</TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {services.data.map((service) => (
                <ServiceRow
                  key={service.name}
                  service={service}
                  endpointsById={endpointsById}
                  canWrite={canWrite}
                  orgId={currentOrgId}
                />
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
