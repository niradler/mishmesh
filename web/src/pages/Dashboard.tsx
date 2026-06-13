import { Link } from "react-router-dom";
import { Globe, Network, Radio, Server } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/common/StatusDot";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { useAgents, useStatus } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { formatBytes, formatRelativeTime } from "@/lib/utils";
import type { Status } from "@/api/types";

function StatCard({
  label,
  value,
  hint,
  icon,
}: {
  label: string;
  value: string;
  hint?: string;
  icon: React.ReactNode;
}) {
  return (
    <Card>
      <CardContent className="flex items-start justify-between gap-4 p-4">
        <div className="space-y-1">
          <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</p>
          <p className="text-2xl font-semibold tracking-heading">{value}</p>
          {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
        </div>
        <div className="flex h-9 w-9 items-center justify-center rounded-md bg-secondary text-foreground">
          {icon}
        </div>
      </CardContent>
    </Card>
  );
}

function BandwidthCard({ status }: { status: Status }) {
  const { used_bytes, limit_bytes } = status.bandwidth;
  const pct = limit_bytes > 0 ? Math.min(100, Math.round((used_bytes / limit_bytes) * 100)) : 0;
  const over = pct >= 90;
  return (
    <Card className="sm:col-span-2 lg:col-span-1">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">Bandwidth</p>
            <p className="text-2xl font-semibold tracking-heading">{formatBytes(used_bytes)}</p>
            <p className="text-xs text-muted-foreground">
              of {limit_bytes > 0 ? formatBytes(limit_bytes) : "unlimited"}
            </p>
          </div>
          <div className="flex h-9 w-9 items-center justify-center rounded-md bg-secondary text-foreground">
            <Network className="h-4 w-4" />
          </div>
        </div>
        {limit_bytes > 0 && (
          <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
            <div
              className={over ? "h-full bg-destructive" : "h-full bg-primary"}
              style={{ width: `${pct}%` }}
            />
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export function Dashboard() {
  const { currentOrgId } = useSession();
  const status = useStatus(currentOrgId);
  const agents = useAgents(currentOrgId, { refetchInterval: 5000 });

  return (
    <div>
      <PageHeader title="Dashboard" description="Live overview of your tunnels and agents." />

      {status.isLoading ? (
        <LoadingState label="Loading status" />
      ) : status.isError ? (
        <ErrorState error={status.error} />
      ) : status.data ? (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <StatCard
            label="Agents"
            value={`${status.data.agents_connected}/${status.data.agents_total}`}
            hint="connected / total"
            icon={<Server className="h-4 w-4" />}
          />
          <StatCard
            label="Endpoints"
            value={String(status.data.endpoints_total)}
            hint={`${status.data.endpoints_by_kind.http} http · ${status.data.endpoints_by_kind.tcp} tcp · ${status.data.endpoints_by_kind.tls} tls`}
            icon={<Globe className="h-4 w-4" />}
          />
          <StatCard
            label="HTTP"
            value={String(status.data.endpoints_by_kind.http)}
            hint="active routes"
            icon={<Radio className="h-4 w-4" />}
          />
          <BandwidthCard status={status.data} />
        </div>
      ) : null}

      <Card className="mt-6">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Agents</CardTitle>
          <Badge variant="muted" className="gap-1.5">
            <span className="h-1.5 w-1.5 rounded-full bg-success animate-pulse-dot" /> live
          </Badge>
        </CardHeader>
        <CardContent>
          {agents.isLoading ? (
            <LoadingState label="Loading agents" />
          ) : agents.isError ? (
            <ErrorState error={agents.error} />
          ) : !agents.data || agents.data.length === 0 ? (
            <EmptyState
              title="No agents yet"
              description="Create an agent to start tunneling traffic into your network."
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Connection</TableHead>
                  <TableHead>Last seen</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {agents.data.map((agent) => (
                  <TableRow key={agent.id}>
                    <TableCell>
                      <Link to={`/agents/${agent.id}`} className="font-medium hover:underline">
                        {agent.name}
                      </Link>
                      <p className="font-mono text-xs text-muted-foreground">{agent.id}</p>
                    </TableCell>
                    <TableCell>
                      <Badge variant={agent.status === "active" ? "outline" : "muted"}>{agent.status}</Badge>
                    </TableCell>
                    <TableCell>
                      <StatusDot online={agent.connected} label={agent.connected ? "connected" : "offline"} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatRelativeTime(agent.last_seen_at)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
