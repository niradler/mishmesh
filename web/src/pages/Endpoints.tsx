import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ExternalLink, Plus } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { CopyButton } from "@/components/common/CopyButton";
import { StatusDot } from "@/components/common/StatusDot";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAgents, useCreateEndpoint, useEndpoints } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { ApiError } from "@/api/client";
import type { CreateEndpointRequest, EndpointKind } from "@/api/types";

function CreateEndpointDialog({ orgId }: { orgId?: string }) {
  const [open, setOpen] = useState(false);
  const [agentId, setAgentId] = useState("");
  const [kind, setKind] = useState<EndpointKind>("http");
  const [subdomain, setSubdomain] = useState("");
  const [domain, setDomain] = useState("");
  const [port, setPort] = useState("");
  const agents = useAgents(orgId);
  const create = useCreateEndpoint(orgId);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    const payload: CreateEndpointRequest = { agent_id: agentId, kind, lifecycle: "reserved" };
    if (kind === "http") {
      if (subdomain) payload.subdomain = subdomain.trim();
      if (domain) payload.domain = domain.trim();
    } else if (kind === "tcp") {
      if (port) payload.port = Number(port);
    } else if (kind === "tls") {
      if (domain) payload.domain = domain.trim();
    }
    create.mutate(payload, {
      onSuccess: () => {
        toast({ title: "Endpoint reserved" });
        setOpen(false);
        setSubdomain("");
        setDomain("");
        setPort("");
      },
      onError: (err) =>
        toast({
          variant: "destructive",
          title: "Create failed",
          description: err instanceof ApiError ? err.message : "Unknown error",
        }),
    });
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button onClick={() => setOpen(true)} disabled={!agents.data || agents.data.length === 0}>
        <Plus className="h-4 w-4" /> Reserve endpoint
      </Button>
      <DialogContent>
        <form onSubmit={onSubmit}>
          <DialogHeader>
            <DialogTitle>Reserve endpoint</DialogTitle>
            <DialogDescription>Create a stable endpoint bound to an agent.</DialogDescription>
          </DialogHeader>
          <div className="my-4 space-y-4">
            <div className="space-y-1.5">
              <Label>Agent</Label>
              <Select value={agentId} onValueChange={setAgentId}>
                <SelectTrigger>
                  <SelectValue placeholder="Select an agent" />
                </SelectTrigger>
                <SelectContent>
                  {(agents.data ?? []).map((ag) => (
                    <SelectItem key={ag.id} value={ag.id}>
                      {ag.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>Kind</Label>
              <Select value={kind} onValueChange={(v) => setKind(v as EndpointKind)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="http">HTTP</SelectItem>
                  <SelectItem value="tcp">TCP</SelectItem>
                  <SelectItem value="tls">TLS passthrough</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {kind === "http" && (
              <div className="grid grid-cols-2 gap-3">
                <div className="space-y-1.5">
                  <Label htmlFor="subdomain">Subdomain</Label>
                  <Input id="subdomain" value={subdomain} onChange={(e) => setSubdomain(e.target.value)} placeholder="api" />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="domain">Custom domain</Label>
                  <Input id="domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="optional" />
                </div>
              </div>
            )}
            {kind === "tcp" && (
              <div className="space-y-1.5">
                <Label htmlFor="port">Port</Label>
                <Input id="port" type="number" value={port} onChange={(e) => setPort(e.target.value)} placeholder="auto-assign if empty" />
              </div>
            )}
            {kind === "tls" && (
              <div className="space-y-1.5">
                <Label htmlFor="tls-domain">Domain</Label>
                <Input id="tls-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" />
              </div>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!agentId || create.isPending}>
              {create.isPending ? "Reserving…" : "Reserve"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function Endpoints() {
  const { currentOrgId, canWrite } = useSession();
  const endpoints = useEndpoints(currentOrgId);

  return (
    <div>
      <PageHeader
        title="Endpoints"
        description="Public bindings that map inbound traffic to your agents."
        actions={canWrite ? <CreateEndpointDialog orgId={currentOrgId} /> : undefined}
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
              action={canWrite ? <CreateEndpointDialog orgId={currentOrgId} /> : undefined}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Public URL</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Lifecycle</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {endpoints.data.map((ep) => {
                  const url = ep.public_url ?? ep.subdomain ?? ep.domain ?? (ep.port ? `:${ep.port}` : ep.id);
                  return (
                    <TableRow key={ep.id}>
                      <TableCell>
                        <div className="flex items-center gap-1.5">
                          <span className="font-mono text-xs">{url}</span>
                          {ep.public_url && <CopyButton value={ep.public_url} label="URL" />}
                          {ep.public_url && (
                            <a href={ep.public_url} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground">
                              <ExternalLink className="h-3.5 w-3.5" />
                            </a>
                          )}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant="muted">{ep.kind}</Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{ep.lifecycle}</TableCell>
                      <TableCell>
                        <StatusDot online={ep.online} label={ep.online ? "online" : "offline"} />
                      </TableCell>
                      <TableCell className="text-right">
                        <Link to={`/endpoints/${ep.id}`} className="text-sm hover:underline">
                          Policy
                        </Link>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
