import { Link } from "react-router-dom";
import { Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HelpedStateBadge } from "@/components/common/TermBadges";
import { endpointDisplayName } from "@/lib/health";
import { newEndpointLink } from "@/lib/endpoints";
import type { CloudflareHostname, CloudflareLevel, Endpoint } from "@/api/types";

function HostDetail({ host, level }: { host: CloudflareHostname; level: CloudflareLevel }) {
  const privateEndpoint = host.state === "unclaimed" && !!host.endpoint_id;
  const lines = [host.detail, host.dns_detail].filter(Boolean);
  return (
    <div className="space-y-1">
      {lines.map((line) => (
        <p key={line} className="text-xs text-muted-foreground">
          {line}
        </p>
      ))}
      {host.fix && (level === "imported" || privateEndpoint) && (
        <p className="rounded border border-amber-500/40 bg-amber-500/5 px-2 py-1 text-xs">
          <span className="font-medium">{privateEndpoint ? "Fix in mishmesh: " : "Fix in Cloudflare: "}</span>
          {host.fix}
        </p>
      )}
    </div>
  );
}

export function HostnamesTable({
  hostnames,
  level,
  endpointsById,
  canWrite,
}: {
  hostnames: CloudflareHostname[];
  level: CloudflareLevel;
  endpointsById: Map<string, Endpoint>;
  canWrite: boolean;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Hostnames</CardTitle>
        <CardDescription>
          Every hostname on the tunnel and every endpoint that asks to be published.
          {level === "imported" && " At the imported level mishmesh only reads, so each problem lists the change to make yourself."}
        </CardDescription>
      </CardHeader>
      <CardContent>
        {hostnames.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No hostnames yet. Turn on Public access on an HTTP endpoint with a custom domain.
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Hostname</TableHead>
                <TableHead>Endpoint</TableHead>
                <TableHead>State</TableHead>
                <TableHead>Detail</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {hostnames.map((host) => {
                const endpoint = host.endpoint_id ? endpointsById.get(host.endpoint_id) : undefined;
                return (
                  <TableRow key={host.host}>
                    <TableCell>
                      <a href={`https://${host.host}`} target="_blank" rel="noreferrer" className="font-mono text-xs hover:underline">
                        {host.host}
                      </a>
                    </TableCell>
                    <TableCell>
                      {host.endpoint_id ? (
                        <span className="flex flex-wrap items-center gap-2">
                          <Link to={`/endpoints/${host.endpoint_id}`} className="font-mono text-xs hover:underline">
                            {endpoint ? endpointDisplayName(endpoint) : host.endpoint_id}
                          </Link>
                          {host.state === "unclaimed" && <span className="text-xs text-muted-foreground">Public access off</span>}
                        </span>
                      ) : (
                        <span className="flex flex-wrap items-center gap-2">
                          <span className="text-xs text-muted-foreground">unclaimed</span>
                          {canWrite && (
                            <Button size="sm" variant="outline" asChild>
                              <Link to={newEndpointLink({ method: "proxy", domain: host.host, publicAccess: true })}>
                                <Plus className="h-3.5 w-3.5" /> Create endpoint
                              </Link>
                            </Button>
                          )}
                        </span>
                      )}
                    </TableCell>
                    <TableCell>
                      <HelpedStateBadge scope="hostname" state={host.state} />
                    </TableCell>
                    <TableCell className="max-w-md">
                      <HostDetail host={host} level={level} />
                    </TableCell>
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
