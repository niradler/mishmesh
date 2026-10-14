import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { Cloud, Globe, Moon, Network, Server } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/common/StatusDot";
import { HealthStrip } from "@/components/dashboard/HealthStrip";
import { NeedsAttention } from "@/components/dashboard/NeedsAttention";
import {
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/common/States";
import {
  useAgents,
  useConnections,
  useEndpoints,
  useServices,
  useStatus,
} from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { formatBytes, formatRelativeTime } from "@/lib/utils";
import type { Status } from "@/api/types";

function StatCard({
  label,
  value,
  hint,
  icon,
  to,
}: {
  label: string;
  value: string;
  hint?: string;
  icon: ReactNode;
  to?: string;
}) {
  const body = (
    <Card
      className={to ? "h-full transition-colors hover:bg-accent/40" : "h-full"}
    >
      <CardContent className="flex items-start justify-between gap-4 p-4">
        <div className="space-y-1">
          <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
            {label}
          </p>
          <p className="text-2xl font-semibold tracking-heading">{value}</p>
          {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
        </div>
        <div className="flex h-9 w-9 items-center justify-center rounded-md bg-secondary text-foreground">
          {icon}
        </div>
      </CardContent>
    </Card>
  );
  return to ? (
    <Link
      to={to}
      className="block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {body}
    </Link>
  ) : (
    body
  );
}

function BandwidthCard({ status }: { status: Status }) {
  const used_bytes = status.usage_bytes;
  const limit_bytes = status.quota?.max_bandwidth_bytes ?? 0;
  const pct =
    limit_bytes > 0
      ? Math.min(100, Math.round((used_bytes / limit_bytes) * 100))
      : 0;
  const over = pct >= 90;
  return (
    <Card className="col-span-2 lg:col-span-1">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
              Bandwidth
            </p>
            <p className="text-2xl font-semibold tracking-heading">
              {formatBytes(used_bytes)}
            </p>
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
  const endpoints = useEndpoints(currentOrgId);
  const services = useServices(currentOrgId);
  const connections = useConnections(currentOrgId);
  const endpointList = endpoints.data ?? [];
  const serviceList = services.data ?? [];
  const runningServices = serviceList.filter(
    (service) => service.status.state === "running",
  ).length;
  const connectionList = connections.data ?? [];
  const connected = connectionList.length > 0;
  const hostnames = connectionList.flatMap(
    (connection) => connection.status.hostnames ?? [],
  );
  const publishedHosts = hostnames.filter(
    (host) => host.state === "published",
  ).length;

  return (
    <div>
      <PageHeader
        title="Dashboard"
        description="Live overview of your endpoints, services, tunnel and agents."
      />

      <HealthStrip
        endpoints={endpointList}
        services={serviceList}
        connections={connectionList}
      />

      {status.isLoading ? (
        <LoadingState label="Loading status" />
      ) : status.isError ? (
        <ErrorState error={status.error} />
      ) : status.data ? (
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-5">
          <StatCard
            label="Agents"
            value={`${status.data.agents.connected}/${status.data.agents.total}`}
            hint="connected / total"
            icon={<Server className="h-4 w-4" />}
            to="/agents"
          />
          <StatCard
            label="Endpoints"
            value={String(status.data.endpoints.total)}
            hint={`${status.data.endpoints.by_kind.http} http · ${status.data.endpoints.by_kind.tcp} tcp · ${status.data.endpoints.by_kind.udp ?? 0} udp · ${status.data.endpoints.by_kind.tls} tls`}
            icon={<Globe className="h-4 w-4" />}
            to="/endpoints"
          />
          <StatCard
            label="Services"
            value={`${runningServices}/${serviceList.length}`}
            hint="running / total"
            icon={<Moon className="h-4 w-4" />}
            to="/services"
          />
          <StatCard
            label="Connections"
            value={connected ? `${publishedHosts}/${hostnames.length}` : "off"}
            hint={connected ? "hostnames published" : "not connected"}
            icon={<Cloud className="h-4 w-4" />}
            to="/connections"
          />
          <BandwidthCard status={status.data} />
        </div>
      ) : null}

      <NeedsAttention
        endpoints={endpointList}
        services={serviceList}
        connections={connectionList}
      />

      <Card className="mt-6">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle>Agents</CardTitle>
          <Badge variant="muted" className="gap-1.5">
            <span className="h-1.5 w-1.5 rounded-full bg-success animate-pulse-dot" />{" "}
            live
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
                      <Link
                        to={`/agents/${agent.id}`}
                        className="font-medium hover:underline"
                      >
                        {agent.name}
                      </Link>
                      <p className="font-mono text-xs text-muted-foreground">
                        {agent.id}
                      </p>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          agent.status === "active" ? "outline" : "muted"
                        }
                      >
                        {agent.status}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <StatusDot
                        online={agent.connected}
                        label={agent.connected ? "connected" : "offline"}
                      />
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
