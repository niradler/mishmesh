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
import { StateDot } from "@/components/common/StatusDot";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import {
  UpstreamFields,
  emptyUpstreamForm,
  upstreamPolicyFields,
  validateUpstream,
  type UpstreamError,
} from "@/components/common/UpstreamFields";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ServicesTable } from "@/components/network/ServicesTable";
import { useTabParam } from "@/hooks/use-tab-param";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAgents, useCreateEndpoint, useEndpoints, useFrontDoors, useServices } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { ApiError } from "@/api/client";
import { endpointDisplayName, endpointHealthState } from "@/lib/health";
import type { CreateEndpointRequest, EndpointKind, EndpointMethod } from "@/api/types";

type CreateMethod = Extract<EndpointMethod, "native" | "proxy">;

function CreateEndpointDialog({ orgId }: { orgId?: string }) {
  const [open, setOpen] = useState(false);
  const [method, setMethod] = useState<CreateMethod>("native");
  const [agentId, setAgentId] = useState("");
  const [kind, setKind] = useState<EndpointKind>("http");
  const [subdomain, setSubdomain] = useState("");
  const [domain, setDomain] = useState("");
  const [port, setPort] = useState("");
  const [upstream, setUpstream] = useState(emptyUpstreamForm);
  const [validationError, setValidationError] = useState<UpstreamError | null>(null);
  const agents = useAgents(orgId);
  const services = useServices(orgId);
  const frontDoors = useFrontDoors(orgId);
  const create = useCreateEndpoint(orgId);
  const isProxy = method === "proxy";

  const reset = () => {
    setMethod("native");
    setAgentId("");
    setKind("http");
    setSubdomain("");
    setDomain("");
    setPort("");
    setUpstream(emptyUpstreamForm());
    setValidationError(null);
    create.reset();
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    setValidationError(null);
    const payload: CreateEndpointRequest = isProxy
      ? { kind: "http", method: "proxy", lifecycle: "reserved" }
      : { agent_id: agentId, kind, method: "native", lifecycle: "reserved" };
    if (isProxy || kind === "http") {
      if (subdomain) payload.subdomain = subdomain.trim();
      if (domain) payload.domain = domain.trim();
    } else if (kind === "tcp") {
      if (port) payload.port = Number(port);
    } else if (kind === "tls") {
      if (domain) payload.domain = domain.trim();
    }
    if (isProxy) {
      const problem = validateUpstream(upstream, true, domain);
      if (problem) {
        setValidationError(problem);
        return;
      }
      payload.policy = upstreamPolicyFields(upstream, true);
    }
    create.mutate(payload, {
      onSuccess: () => {
        toast({ title: "Endpoint reserved" });
        setOpen(false);
        reset();
      },
      onError: (err) =>
        toast({
          variant: "destructive",
          title: "Create failed",
          description: err instanceof ApiError ? err.message : "Unknown error",
        }),
    });
  };

  const serverError = create.error instanceof ApiError ? create.error.message : null;
    const needsAgent = !isProxy && !agentId;

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) reset();
      }}
    >
      <Button onClick={() => setOpen(true)}>
        <Plus className="h-4 w-4" /> Reserve endpoint
      </Button>
      <DialogContent className="max-h-[90vh] overflow-y-auto">
        <form onSubmit={onSubmit}>
          <DialogHeader>
            <DialogTitle>Reserve endpoint</DialogTitle>
            <DialogDescription>
              {isProxy
                ? "Create a stable endpoint that mishmesh proxies directly to your targets."
                : "Create a stable endpoint bound to an agent."}
            </DialogDescription>
          </DialogHeader>
          <div className="my-4 space-y-4">
            <div className="space-y-1.5">
              <Label>Method</Label>
              <Select value={method} onValueChange={(v) => setMethod(v as CreateMethod)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="native">Agent</SelectItem>
                  <SelectItem value="proxy">Proxy</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {!isProxy && (
              <>
                <div className="space-y-1.5">
                  <Label>Agent</Label>
                  <Select value={agentId} onValueChange={setAgentId}>
                    <SelectTrigger>
                      <SelectValue placeholder={agents.data && agents.data.length === 0 ? "Create an agent first" : "Select an agent"} />
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
              </>
            )}
            {(isProxy || kind === "http") && (
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
            {!isProxy && kind === "tcp" && (
              <div className="space-y-1.5">
                <Label htmlFor="port">Port</Label>
                <Input id="port" type="number" value={port} onChange={(e) => setPort(e.target.value)} placeholder="auto-assign if empty" />
              </div>
            )}
            {!isProxy && kind === "tls" && (
              <div className="space-y-1.5">
                <Label htmlFor="tls-domain">Domain</Label>
                <Input id="tls-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" />
              </div>
            )}
            {isProxy && (
              <UpstreamFields
                form={upstream}
                onChange={(next) => {
                  setUpstream(next);
                  setValidationError(null);
                }}
                isProxy
                services={services.data ?? []}
                frontDoorEnabled={frontDoors.data?.cloudflare.enabled ?? false}
                error={validationError}
              />
            )}
            {serverError && (
              <p className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive" role="alert">
                {serverError}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={needsAgent || create.isPending}>
              {create.isPending ? "Reserving…" : "Reserve"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

const TABS = ["endpoints", "services"] as const;

export function Endpoints() {
  const { currentOrgId, canWrite } = useSession();
  const endpoints = useEndpoints(currentOrgId);
  const services = useServices(currentOrgId);
  const [requestedTab, setTab] = useTabParam(TABS, "endpoints");
  const showServicesTab = services.isLoading
    ? requestedTab === "services"
    : services.isError || (services.data?.length ?? 0) > 0;
  const tab = showServicesTab ? requestedTab : "endpoints";

  const endpointsCard = (
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
                <TableHead>Method</TableHead>
                <TableHead>Lifecycle</TableHead>
                <TableHead>Health</TableHead>
                <TableHead></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {endpoints.data.map((ep) => {
                const state = endpointHealthState(ep);
                return (
                  <TableRow key={ep.id}>
                    <TableCell>
                      <div className="flex items-center gap-1.5">
                        <span className="font-mono text-xs">{endpointDisplayName(ep)}</span>
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
                    <TableCell className="text-muted-foreground">{ep.method ?? "native"}</TableCell>
                    <TableCell className="text-muted-foreground">{ep.lifecycle}</TableCell>
                    <TableCell>
                      <div className="space-y-0.5" title={ep.health?.detail}>
                        <StateDot state={state} label={state} />
                        {ep.health?.service && (
                          <p className="text-xs text-muted-foreground">service {ep.health.service}</p>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-right">
                      <Link to={`/endpoints/${ep.id}`} className="text-sm hover:underline">
                        Details
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
  );

  return (
    <div>
      <PageHeader
        title="Endpoints"
        description="Public bindings that map inbound traffic to your agents or proxy targets."
        actions={canWrite ? <CreateEndpointDialog orgId={currentOrgId} /> : undefined}
      />
      {showServicesTab ? (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="endpoints">Endpoints</TabsTrigger>
            <TabsTrigger value="services">Services</TabsTrigger>
          </TabsList>
          <TabsContent value="endpoints">{endpointsCard}</TabsContent>
          <TabsContent value="services">
            <p className="mb-3 text-sm text-muted-foreground">
              Scale-to-zero backends that wake on the first request and sleep when idle.
            </p>
            <ServicesTable />
          </TabsContent>
        </Tabs>
      ) : (
        endpointsCard
      )}
    </div>
  );
}
