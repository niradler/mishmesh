import { useState } from "react";
import { Link } from "react-router-dom";
import { Moon, Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { HelpTip } from "@/components/common/HelpTip";
import { DriverBadge, HelpedStateBadge } from "@/components/common/TermBadges";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { PipelineCard } from "@/components/endpoints/PipelineCard";
import {
  WakeServiceDialog,
  useServiceLink,
} from "@/components/endpoints/WakeServiceDialog";
import { useService, useStartService, useStopService } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { HELP } from "@/lib/glossary";
import { formatDurationSeconds } from "@/lib/health";
import { isEphemeral } from "@/lib/endpoints";
import type { Endpoint } from "@/api/types";

const RUNNING_STATES = ["running", "starting", "partial"];

export function ServiceStage({
  endpoint,
  canWrite,
}: {
  endpoint: Endpoint;
  canWrite: boolean;
}) {
  const serviceId = endpoint.policy?.service_id ?? "";
  const service = useService(serviceId);
  const start = useStartService();
  const stop = useStopService();
  const { unlink, pending: unlinking } = useServiceLink(endpoint);
  const [picking, setPicking] = useState(false);
  const [confirmUnlink, setConfirmUnlink] = useState(false);

  const dialog = (
    <WakeServiceDialog
      endpoint={endpoint}
      open={picking}
      onOpenChange={setPicking}
    />
  );

  if (!serviceId) {
    if (isEphemeral(endpoint) || endpoint.kind !== "http") {
      return (
        <PipelineCard stage="Service" icon={Moon} empty>
          <p className="text-xs text-muted-foreground">
            {isEphemeral(endpoint)
              ? "Ephemeral endpoints cannot wake a service."
              : "Only HTTP endpoints can wake a service, because the wake page needs HTTP."}
          </p>
        </PipelineCard>
      );
    }
    return (
      <>
        <PipelineCard
          stage="Service"
          icon={Moon}
          empty
          actions={
            canWrite && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => setPicking(true)}
              >
                Wake a service
              </Button>
            )
          }
        >
          <p className="flex items-center gap-1 text-xs text-muted-foreground">
            Routes directly to its backend. Link a service only if mishmesh
            should start and stop it automatically.
            <HelpTip entry={HELP.serviceLink} />
          </p>
        </PipelineCard>
        {dialog}
      </>
    );
  }

  const data = service.data;
  const state = data?.status.state ?? (service.isError ? "error" : "unknown");
  const running = RUNNING_STATES.includes(state);

  const runAction = (action: "start" | "stop") => {
    const mutation = action === "start" ? start : stop;
    mutation.mutate(serviceId, {
      onSuccess: () =>
        toast({
          title: action === "start" ? "Service started" : "Service stopped",
        }),
      onError: (err) =>
        toast({
          variant: "destructive",
          title: action === "start" ? "Start failed" : "Stop failed",
          description: errorMessage(err),
        }),
    });
  };

  return (
    <>
      <PipelineCard
        stage="Service"
        icon={Moon}
        state={state}
        title={
          <Link to={`/services/${serviceId}`} className="hover:underline">
            {data?.name ?? endpoint.health?.service ?? serviceId}
          </Link>
        }
        actions={
          canWrite && (
            <>
              {running ? (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={stop.isPending}
                  onClick={() => runAction("stop")}
                >
                  <Square className="h-3.5 w-3.5" />{" "}
                  {stop.isPending ? "Stopping…" : "Stop"}
                </Button>
              ) : (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={start.isPending || !data}
                  onClick={() => runAction("start")}
                >
                  <Play className="h-3.5 w-3.5" />{" "}
                  {start.isPending ? "Starting…" : "Start"}
                </Button>
              )}
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => setPicking(true)}
              >
                Change
              </Button>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => setConfirmUnlink(true)}
              >
                Unlink
              </Button>
            </>
          )
        }
      >
        {service.isError && (
          <p className="text-xs text-destructive">
            {errorMessage(service.error)}
          </p>
        )}
        {data && (
          <>
            <div className="flex flex-wrap items-center gap-1.5">
              <HelpedStateBadge scope="service" state={state} />
              <DriverBadge driver={data.driver} />
            </div>
            <p className="flex items-center gap-1 text-xs text-muted-foreground">
              Automatic scale-down:{" "}
              {data.idle_timeout_seconds === -1
                ? "when no dependent needs it"
                : data.idle_timeout_seconds
                  ? `after ${formatDurationSeconds(data.idle_timeout_seconds)} idle`
                  : "disabled"}
              <HelpTip entry={HELP.idleTimeout} />
            </p>
            {data.status.last_error && (
              <p className="break-words text-xs text-destructive">
                {data.status.last_error}
              </p>
            )}
          </>
        )}
      </PipelineCard>
      {dialog}
      <ConfirmDialog
        open={confirmUnlink}
        onOpenChange={setConfirmUnlink}
        title="Unlink service?"
        description={`Requests to this endpoint will no longer start ${data?.name ?? "the service"}. The service itself is not changed.`}
        confirmLabel="Unlink"
        pendingLabel="Unlinking…"
        pending={unlinking}
        onConfirm={() => unlink(() => setConfirmUnlink(false))}
      />
    </>
  );
}
