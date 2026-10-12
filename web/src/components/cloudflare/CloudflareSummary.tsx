import type { ReactNode } from "react";
import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { StateDot } from "@/components/common/StatusDot";
import { CloudflareLevelBadge, ConnectorBadge } from "@/components/common/TermBadges";
import { useSyncCloudflare } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { connectorLabel, connectorState, tunnelConnectionCount } from "@/lib/cloudflare";
import { formatRelativeTime } from "@/lib/utils";
import type { Cloudflare, CloudflareConnection } from "@/api/types";

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-sm">{children}</dd>
    </div>
  );
}

function tunnelState(status?: string): string {
  if (!status) return "unknown";
  if (status === "healthy") return "up";
  if (status === "inactive" || status === "down") return "down";
  return status;
}

export function CloudflareSummary({
  cloudflare,
  connection,
  canWrite,
}: {
  cloudflare: Cloudflare;
  connection: CloudflareConnection;
  canWrite: boolean;
}) {
  const sync = useSyncCloudflare();
  const status = cloudflare.status;
  const tunnel = status?.tunnel;
  const supervisor = status?.supervisor;
  const connections = tunnelConnectionCount(tunnel);
  const managed = cloudflare.level === "managed";

  const onSync = () =>
    sync.mutate(undefined, {
      onSuccess: () => toast({ title: managed ? "Synced with Cloudflare" : "Refreshed from the connector" }),
      onError: (err) => toast({ variant: "destructive", title: "Sync failed", description: errorMessage(err) }),
    });

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3 space-y-0">
        <div className="space-y-1.5">
          <CardTitle className="flex flex-wrap items-center gap-2">
            {connection.name}
            <CloudflareLevelBadge level={cloudflare.level} />
          </CardTitle>
          <CardDescription className="font-mono text-xs">tunnel {connection.tunnel_id}</CardDescription>
        </div>
        {canWrite && (
          <Button size="sm" variant="outline" onClick={onSync} disabled={sync.isPending}>
            <RefreshCw className={sync.isPending ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
            {sync.isPending ? (managed ? "Syncing…" : "Refreshing…") : managed ? "Sync now" : "Refresh"}
          </Button>
        )}
      </CardHeader>
      <CardContent className="space-y-4">
        <dl className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Fact label="Connector">
            <span className="flex flex-col gap-1">
              <ConnectorBadge connector={connection.connector} />
              <StateDot state={connectorState(status?.connector)} label={connectorLabel(status?.connector)} />
            </span>
          </Fact>
          <Fact label="Tunnel">
            {tunnel ? (
              <span className="flex flex-col gap-1">
                <StateDot state={tunnelState(tunnel.status)} label={tunnel.status || "unknown"} />
                <span className="text-xs text-muted-foreground">
                  {connections} connection{connections === 1 ? "" : "s"}
                </span>
              </span>
            ) : (
              <span className="text-muted-foreground">{managed ? "not read yet" : "needs an API token to read"}</span>
            )}
          </Fact>
          <Fact label="Edge locations">
            {status?.connector?.edge_locations?.length ? (
              status.connector.edge_locations.join(", ")
            ) : (
              <span className="text-muted-foreground">none</span>
            )}
          </Fact>
          <Fact label="Last sync">
            {status?.last_sync ? formatRelativeTime(status.last_sync) : <span className="text-muted-foreground">never</span>}
          </Fact>
        </dl>
        {supervisor && (
          <p className="text-xs text-muted-foreground">
            cloudflared {supervisor.running ? `running${supervisor.pid ? ` as pid ${supervisor.pid}` : ""}` : "not running"}
            {supervisor.restarts > 0 && ` · ${supervisor.restarts} restart${supervisor.restarts === 1 ? "" : "s"}`}
            {supervisor.last_exit && ` · last exit: ${supervisor.last_exit}`}
          </p>
        )}
        {status?.connector?.error && (
          <p className="break-words text-sm text-destructive">Connector: {status.connector.error}</p>
        )}
        {status?.last_error && (
          <p className="break-words rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
            {status.last_error}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
