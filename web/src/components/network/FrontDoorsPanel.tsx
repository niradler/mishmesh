import { Link } from "react-router-dom";
import { Cloud, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { StateBadge } from "@/components/common/StateBadge";
import { StateDot } from "@/components/common/StatusDot";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { useEndpoints, useFrontDoors, useSyncCloudflare } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { ApiError } from "@/api/client";
import { endpointDisplayName } from "@/lib/health";
import { formatRelativeTime } from "@/lib/utils";
import type { CloudflareStatus, CloudflareSupervisor, Endpoint } from "@/api/types";

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</p>
      <div className="text-sm">{children}</div>
    </div>
  );
}

function SupervisorSummary({ supervisor }: { supervisor: CloudflareSupervisor }) {
  const state = supervisor.running ? (supervisor.ready ? "up" : "waking") : "down";
  const label = supervisor.running ? (supervisor.ready ? "running, ready" : "running, not ready") : "not running";
  return (
    <div className="space-y-1">
      <StateDot state={state} label={label} />
      <p className="text-xs text-muted-foreground">
        {supervisor.pid ? `pid ${supervisor.pid}, ` : ""}
        {supervisor.restarts} restarts
        {supervisor.last_exit ? `, last exit: ${supervisor.last_exit}` : ""}
      </p>
    </div>
  );
}

function CloudflareCard({
  status,
  endpointsById,
  canWrite,
  orgId,
}: {
  status: CloudflareStatus;
  endpointsById: Map<string, Endpoint>;
  canWrite: boolean;
  orgId?: string;
}) {
  const sync = useSyncCloudflare(orgId);
  const hostnames = status.hostnames ?? [];
  const connectionCount = status.tunnel?.connections?.length ?? 0;

  const onSync = () =>
    sync.mutate(undefined, {
      onSuccess: () => toast({ title: "Cloudflare synced" }),
      onError: (err) =>
        toast({
          variant: "destructive",
          title: "Sync failed",
          description: err instanceof ApiError ? err.message : "Unknown error",
        }),
    });

  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between gap-4 space-y-0">
        <div className="space-y-1.5">
          <CardTitle>Cloudflare</CardTitle>
          <CardDescription>Tunnel, DNS and Access reconciled for your custom domains.</CardDescription>
        </div>
        {canWrite && (
          <Button size="sm" variant="outline" onClick={onSync} disabled={sync.isPending}>
            <RefreshCw className={sync.isPending ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
            {sync.isPending ? "Syncing…" : "Sync now"}
          </Button>
        )}
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Field label="Tunnel">
            {status.tunnel ? (
              <div className="space-y-1">
                <p className="font-medium">{status.tunnel.name}</p>
                <StateDot state={status.tunnel.status} label={status.tunnel.status} />
                <p className="text-xs text-muted-foreground">
                  {connectionCount} {connectionCount === 1 ? "connection" : "connections"}
                </p>
              </div>
            ) : (
              <span className="text-muted-foreground">not created yet</span>
            )}
          </Field>
          <Field label="Origin">
            <span className="break-all font-mono text-xs">{status.origin || "n/a"}</span>
          </Field>
          <Field label="Last sync">
            <span className="text-muted-foreground">{formatRelativeTime(status.last_sync)}</span>
          </Field>
          {status.supervisor && (
            <Field label="Supervisor">
              <SupervisorSummary supervisor={status.supervisor} />
            </Field>
          )}
        </div>

        {status.last_error && (
          <p className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive" role="alert">
            {status.last_error}
          </p>
        )}

        {hostnames.length === 0 ? (
          <EmptyState
            title="No hostnames"
            description="Set front_door to cloudflare on an endpoint with a custom domain to publish it here."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Host</TableHead>
                <TableHead>Endpoint</TableHead>
                <TableHead>Ingress</TableHead>
                <TableHead>DNS</TableHead>
                <TableHead>Access</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Detail</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {hostnames.map((host) => {
                const endpoint = host.endpoint_id ? endpointsById.get(host.endpoint_id) : undefined;
                const detail = [host.dns_detail, host.detail].filter(Boolean).join(" | ");
                return (
                  <TableRow key={host.host}>
                    <TableCell className="font-mono text-xs">{host.host}</TableCell>
                    <TableCell>
                      {host.endpoint_id ? (
                        <Link to={`/endpoints/${host.endpoint_id}`} className="font-mono text-xs hover:underline">
                          {endpoint ? endpointDisplayName(endpoint) : host.endpoint_id}
                        </Link>
                      ) : (
                        <span className="text-muted-foreground">none</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <StateDot state={host.ingress ? "ok" : "missing"} label={host.ingress ? "ok" : "missing"} />
                    </TableCell>
                    <TableCell>
                      <StateBadge state={host.dns} />
                    </TableCell>
                    <TableCell>
                      <StateBadge state={host.access} />
                    </TableCell>
                    <TableCell>
                      <StateBadge state={host.state} />
                    </TableCell>
                    <TableCell className="max-w-xs break-words text-xs text-muted-foreground">{detail}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

export function FrontDoorsPanel() {
  const { currentOrgId, canWrite } = useSession();
  const frontDoors = useFrontDoors(currentOrgId);
  const endpoints = useEndpoints(currentOrgId);
  const endpointsById = new Map((endpoints.data ?? []).map((ep) => [ep.id, ep]));

  if (frontDoors.isLoading) return <LoadingState label="Loading front doors" />;
  if (frontDoors.isError) return <ErrorState error={frontDoors.error} />;
  if (!frontDoors.data?.cloudflare.enabled) {
    return (
      <EmptyState
        icon={<Cloud className="h-5 w-5" />}
        title="Cloudflare front door is not enabled"
        description="Configure the MISHMESH_CLOUDFLARE_* environment variables on the server and allowlist this organization to publish endpoints through a Cloudflare tunnel."
      />
    );
  }
  return (
    <CloudflareCard
      status={frontDoors.data.cloudflare}
      endpointsById={endpointsById}
      canWrite={canWrite}
      orgId={currentOrgId}
    />
  );
}
