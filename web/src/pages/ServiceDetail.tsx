import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Pencil, Play, Square, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { ErrorState, LoadingState } from "@/components/common/States";
import { HelpTip } from "@/components/common/HelpTip";
import { DriverBadge, HelpedStateBadge } from "@/components/common/TermBadges";
import { ServiceFormDialog } from "@/components/services/ServiceFormDialog";
import { ServiceConfigCard } from "@/components/services/ServiceConfigCard";
import { WokenByCard } from "@/components/services/WokenByCard";
import { EndpointLinks } from "@/components/services/EndpointLinks";
import { useDeleteService, useEndpoints, useService, useServices, useStartService, useStopService } from "@/api/hooks";
import { ApiError, errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { HELP } from "@/lib/glossary";
import { formatDurationSeconds } from "@/lib/health";
import { formatRelativeTime } from "@/lib/utils";
import type { Endpoint, Service, ServiceDeleteConflict } from "@/api/types";

const RUNNING_STATES = ["running", "starting", "partial"];

function conflictEndpointIds(err: unknown): string[] {
  if (!(err instanceof ApiError) || err.status !== 409) return [];
  const body = err.body as ServiceDeleteConflict | undefined;
  return body?.endpoints ?? [];
}

export function ServiceDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { currentOrgId, canWrite } = useSession();
  const service = useService(id);
  const services = useServices(currentOrgId);
  const endpoints = useEndpoints(currentOrgId);
  const start = useStartService();
  const stop = useStopService();
  const remove = useDeleteService();
  const [editing, setEditing] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const endpointsById = useMemo(
    () => new Map<string, Endpoint>((endpoints.data ?? []).map((ep) => [ep.id, ep])),
    [endpoints.data],
  );
  const servicesById = useMemo(
    () => new Map<string, Service>((services.data ?? []).map((s) => [s.id, s])),
    [services.data],
  );

  if (service.isLoading) return <LoadingState label="Loading service" />;
  if (service.isError) return <ErrorState error={service.error} />;
  if (!service.data) return null;
  const svc = service.data;
  const state = svc.status.state;
  const running = RUNNING_STATES.includes(state);
  const blockingEndpoints = conflictEndpointIds(remove.error);

  const runAction = (action: "start" | "stop") => {
    const mutation = action === "start" ? start : stop;
    mutation.mutate(svc.id, {
      onSuccess: () => toast({ title: action === "start" ? "Service started" : "Service stopped", description: svc.name }),
      onError: (err) =>
        toast({ variant: "destructive", title: action === "start" ? "Start failed" : "Stop failed", description: errorMessage(err) }),
    });
  };

  const onDelete = () =>
    remove.mutate(svc.id, {
      onSuccess: () => {
        toast({ title: "Service deleted", description: svc.name });
        navigate("/services");
      },
    });

  return (
    <div className="space-y-6">
      <Link to="/services" className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-4 w-4" /> Services
      </Link>

      <PageHeader
        title={svc.name}
        description="Started on demand by its endpoints and stopped again once idle."
        actions={
          canWrite ? (
            <>
              {running ? (
                <Button variant="outline" disabled={stop.isPending} onClick={() => runAction("stop")}>
                  <Square className="h-4 w-4" /> {stop.isPending ? "Stopping…" : "Stop"}
                </Button>
              ) : (
                <Button variant="outline" disabled={start.isPending} onClick={() => runAction("start")}>
                  <Play className="h-4 w-4" /> {start.isPending ? "Starting…" : "Start"}
                </Button>
              )}
              <Button variant="outline" onClick={() => setEditing(true)}>
                <Pencil className="h-4 w-4" /> Edit
              </Button>
              <Button
                variant="outline"
                onClick={() => {
                  remove.reset();
                  setConfirmDelete(true);
                }}
              >
                <Trash2 className="h-4 w-4" /> Delete
              </Button>
            </>
          ) : undefined
        }
      />

      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
        <HelpedStateBadge scope="service" state={state} />
        <DriverBadge driver={svc.driver} />
        <span className="inline-flex items-center gap-1 text-muted-foreground">
          Idle timeout {svc.idle_timeout_seconds ? formatDurationSeconds(svc.idle_timeout_seconds) : "never"}
          <HelpTip entry={HELP.idleTimeout} />
        </span>
        <span className="text-muted-foreground">Active requests {svc.status.active}</span>
        {svc.status.last_activity && (
          <span className="text-muted-foreground">Last activity {formatRelativeTime(svc.status.last_activity)}</span>
        )}
        {svc.status.started_by && <span className="text-muted-foreground">Started by {svc.status.started_by}</span>}
      </div>
      {svc.status.last_error && (
        <p className="break-words rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
          {svc.status.last_error}
        </p>
      )}

      <WokenByCard service={svc} endpoints={endpoints.data ?? []} services={servicesById} canWrite={canWrite} />
      <ServiceConfigCard service={svc} />

      <ServiceFormDialog open={editing} onOpenChange={setEditing} service={svc} />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete service?"
        description={
          <>
            mishmesh forgets <span className="font-medium">{svc.name}</span>. Its containers or processes are left as they are. This
            cannot be undone.
          </>
        }
        pending={remove.isPending}
        error={
          remove.error ? (
            blockingEndpoints.length > 0 ? (
              <div className="space-y-1.5">
                <p>Unlink these endpoints first:</p>
                <EndpointLinks ids={blockingEndpoints} endpointsById={endpointsById} />
              </div>
            ) : (
              errorMessage(remove.error)
            )
          ) : undefined
        }
        onConfirm={onDelete}
      />
    </div>
  );
}
