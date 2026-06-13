import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowLeft, ExternalLink } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CopyButton } from "@/components/common/CopyButton";
import { StatusDot } from "@/components/common/StatusDot";
import { ErrorState, LoadingState } from "@/components/common/States";
import {
  KeyValueEditor,
  ListEditor,
  pairsToRecord,
  recordToPairs,
  type KVPair,
} from "@/components/common/KeyValueEditor";
import { useEndpoint, useUpdateEndpointPolicy } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { ApiError } from "@/api/client";
import type { EndpointPolicy } from "@/api/types";

interface PolicyForm {
  reqAdd: KVPair[];
  reqRemove: string[];
  resAdd: KVPair[];
  resRemove: string[];
  hostHeader: string;
  stripPathPrefix: string;
  addPathPrefix: string;
  basicAuthUser: string;
  basicAuthPassword: string;
  ipAllow: string[];
  ipDeny: string[];
  forceHttps: boolean;
  maxBodyBytes: string;
  compression: boolean;
  oidcEnabled: boolean;
  oidcIssuer: string;
  oidcClientId: string;
  oidcClientSecret: string;
  oidcAllowedEmails: string[];
  oidcAllowedDomains: string[];
}

function fromPolicy(p?: EndpointPolicy | null): PolicyForm {
  return {
    reqAdd: recordToPairs(p?.request_headers_add),
    reqRemove: p?.request_headers_remove ?? [],
    resAdd: recordToPairs(p?.response_headers_add),
    resRemove: p?.response_headers_remove ?? [],
    hostHeader: p?.host_header ?? "",
    stripPathPrefix: p?.strip_path_prefix ?? "",
    addPathPrefix: p?.add_path_prefix ?? "",
    basicAuthUser: p?.basic_auth_user ?? "",
    basicAuthPassword: p?.basic_auth_password ?? "",
    ipAllow: p?.ip_allow ?? [],
    ipDeny: p?.ip_deny ?? [],
    forceHttps: p?.force_https ?? false,
    maxBodyBytes: p?.max_body_bytes ? String(p.max_body_bytes) : "",
    compression: p?.compression ?? false,
    oidcEnabled: p?.oidc?.enabled ?? false,
    oidcIssuer: p?.oidc?.issuer ?? "",
    oidcClientId: p?.oidc?.client_id ?? "",
    oidcClientSecret: p?.oidc?.client_secret ?? "",
    oidcAllowedEmails: p?.oidc?.allowed_emails ?? [],
    oidcAllowedDomains: p?.oidc?.allowed_domains ?? [],
  };
}

function toPolicy(f: PolicyForm): EndpointPolicy {
  const policy: EndpointPolicy = {
    request_headers_add: pairsToRecord(f.reqAdd),
    request_headers_remove: f.reqRemove.filter(Boolean),
    response_headers_add: pairsToRecord(f.resAdd),
    response_headers_remove: f.resRemove.filter(Boolean),
    host_header: f.hostHeader || undefined,
    strip_path_prefix: f.stripPathPrefix || undefined,
    add_path_prefix: f.addPathPrefix || undefined,
    basic_auth_user: f.basicAuthUser || undefined,
    basic_auth_password: f.basicAuthPassword || undefined,
    ip_allow: f.ipAllow.filter(Boolean),
    ip_deny: f.ipDeny.filter(Boolean),
    force_https: f.forceHttps,
    max_body_bytes: f.maxBodyBytes ? Number(f.maxBodyBytes) : undefined,
    compression: f.compression,
  };
  if (f.oidcEnabled || f.oidcIssuer || f.oidcClientId) {
    policy.oidc = {
      enabled: f.oidcEnabled,
      issuer: f.oidcIssuer,
      client_id: f.oidcClientId,
      client_secret: f.oidcClientSecret,
      allowed_emails: f.oidcAllowedEmails.filter(Boolean),
      allowed_domains: f.oidcAllowedDomains.filter(Boolean),
    };
  }
  return policy;
}

function Section({ title, description, children }: { title: string; description?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-3 border-b border-border pb-6 last:border-0 last:pb-0">
      <div>
        <h3 className="text-sm font-medium tracking-heading">{title}</h3>
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
      </div>
      {children}
    </div>
  );
}

export function EndpointDetail() {
  const { id = "" } = useParams();
  const { currentOrgId, canWrite } = useSession();
  const endpoint = useEndpoint(id);
  const update = useUpdateEndpointPolicy(currentOrgId);
  const [form, setForm] = useState<PolicyForm>(() => fromPolicy(null));

  const initial = useMemo(() => fromPolicy(endpoint.data?.policy), [endpoint.data?.policy]);
  useEffect(() => setForm(initial), [initial]);

  const set = <K extends keyof PolicyForm>(key: K, value: PolicyForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  if (endpoint.isLoading) return <LoadingState label="Loading endpoint" />;
  if (endpoint.isError) return <ErrorState error={endpoint.error} />;
  if (!endpoint.data) return null;
  const ep = endpoint.data;

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    update.mutate(
      { id, policy: toPolicy(form) },
      {
        onSuccess: () => toast({ title: "Policy saved" }),
        onError: (err) =>
          toast({
            variant: "destructive",
            title: "Save failed",
            description: err instanceof ApiError ? err.message : "Unknown error",
          }),
      },
    );
  };

  return (
    <div className="space-y-6">
      <Link to="/endpoints" className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-4 w-4" /> Endpoints
      </Link>

      <PageHeader
        title="Endpoint policy"
        description={ep.id}
        actions={
          <div className="flex items-center gap-2">
            <Badge variant="muted">{ep.kind}</Badge>
            <StatusDot online={ep.online} label={ep.online ? "online" : "offline"} />
          </div>
        }
      />

      {ep.public_url && (
        <Card>
          <CardContent className="flex items-center justify-between gap-3 p-4">
            <code className="truncate font-mono text-sm">{ep.public_url}</code>
            <div className="flex items-center gap-1">
              <CopyButton value={ep.public_url} label="URL" />
              <a href={ep.public_url} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground">
                <ExternalLink className="h-4 w-4" />
              </a>
            </div>
          </CardContent>
        </Card>
      )}

      <form onSubmit={onSubmit}>
        <Tabs defaultValue="routing">
          <TabsList>
            <TabsTrigger value="routing">Routing</TabsTrigger>
            <TabsTrigger value="headers">Headers</TabsTrigger>
            <TabsTrigger value="security">Security</TabsTrigger>
            <TabsTrigger value="oidc">OIDC</TabsTrigger>
          </TabsList>

          <TabsContent value="routing">
            <Card>
              <CardHeader>
                <CardTitle>Routing & limits</CardTitle>
                <CardDescription>Rewrite paths and host, control body size and compression.</CardDescription>
              </CardHeader>
              <CardContent className="space-y-6">
                <Section title="Host header" description="Override the Host header sent to your service.">
                  <Input value={form.hostHeader} onChange={(e) => set("hostHeader", e.target.value)} placeholder="internal.svc.local" disabled={!canWrite} />
                </Section>
                <Section title="Path prefixes">
                  <div className="grid gap-3 sm:grid-cols-2">
                    <div className="space-y-1.5">
                      <Label>Strip prefix</Label>
                      <Input value={form.stripPathPrefix} onChange={(e) => set("stripPathPrefix", e.target.value)} placeholder="/api" disabled={!canWrite} />
                    </div>
                    <div className="space-y-1.5">
                      <Label>Add prefix</Label>
                      <Input value={form.addPathPrefix} onChange={(e) => set("addPathPrefix", e.target.value)} placeholder="/v1" disabled={!canWrite} />
                    </div>
                  </div>
                </Section>
                <Section title="Body & transport">
                  <div className="space-y-1.5">
                    <Label>Max body bytes</Label>
                    <Input type="number" value={form.maxBodyBytes} onChange={(e) => set("maxBodyBytes", e.target.value)} placeholder="unlimited" disabled={!canWrite} />
                  </div>
                  <div className="flex items-center justify-between rounded-md border border-border p-3">
                    <div>
                      <p className="text-sm font-medium">Compression</p>
                      <p className="text-xs text-muted-foreground">gzip responses at the edge.</p>
                    </div>
                    <Switch checked={form.compression} onCheckedChange={(v) => set("compression", v)} disabled={!canWrite} />
                  </div>
                  <div className="flex items-center justify-between rounded-md border border-border p-3">
                    <div>
                      <p className="text-sm font-medium">Force HTTPS</p>
                      <p className="text-xs text-muted-foreground">Redirect HTTP requests to HTTPS.</p>
                    </div>
                    <Switch checked={form.forceHttps} onCheckedChange={(v) => set("forceHttps", v)} disabled={!canWrite} />
                  </div>
                </Section>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="headers">
            <Card>
              <CardHeader>
                <CardTitle>Header rewriting</CardTitle>
                <CardDescription>Add or remove headers on request and response.</CardDescription>
              </CardHeader>
              <CardContent className="space-y-6">
                <Section title="Request headers — add">
                  <KeyValueEditor pairs={form.reqAdd} onChange={(p) => set("reqAdd", p)} />
                </Section>
                <Section title="Request headers — remove">
                  <ListEditor values={form.reqRemove} onChange={(v) => set("reqRemove", v)} placeholder="X-Forwarded-For" />
                </Section>
                <Section title="Response headers — add">
                  <KeyValueEditor pairs={form.resAdd} onChange={(p) => set("resAdd", p)} />
                </Section>
                <Section title="Response headers — remove">
                  <ListEditor values={form.resRemove} onChange={(v) => set("resRemove", v)} placeholder="Server" />
                </Section>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="security">
            <Card>
              <CardHeader>
                <CardTitle>Access control</CardTitle>
                <CardDescription>Basic auth and IP allow / deny lists.</CardDescription>
              </CardHeader>
              <CardContent className="space-y-6">
                <Section title="Basic auth">
                  <div className="grid gap-3 sm:grid-cols-2">
                    <div className="space-y-1.5">
                      <Label>Username</Label>
                      <Input value={form.basicAuthUser} onChange={(e) => set("basicAuthUser", e.target.value)} autoComplete="off" disabled={!canWrite} />
                    </div>
                    <div className="space-y-1.5">
                      <Label>Password</Label>
                      <Input type="password" value={form.basicAuthPassword} onChange={(e) => set("basicAuthPassword", e.target.value)} autoComplete="new-password" disabled={!canWrite} />
                    </div>
                  </div>
                </Section>
                <Section title="IP allow list" description="CIDRs permitted to reach this endpoint.">
                  <ListEditor values={form.ipAllow} onChange={(v) => set("ipAllow", v)} placeholder="10.0.0.0/8" />
                </Section>
                <Section title="IP deny list" description="CIDRs blocked from this endpoint.">
                  <ListEditor values={form.ipDeny} onChange={(v) => set("ipDeny", v)} placeholder="192.168.1.0/24" />
                </Section>
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="oidc">
            <Card>
              <CardHeader>
                <CardTitle>OIDC authentication</CardTitle>
                <CardDescription>Require visitors to authenticate via an OIDC provider.</CardDescription>
              </CardHeader>
              <CardContent className="space-y-6">
                <div className="flex items-center justify-between rounded-md border border-border p-3">
                  <div>
                    <p className="text-sm font-medium">Enable OIDC</p>
                    <p className="text-xs text-muted-foreground">Gate this endpoint behind a login.</p>
                  </div>
                  <Switch checked={form.oidcEnabled} onCheckedChange={(v) => set("oidcEnabled", v)} disabled={!canWrite} />
                </div>
                <Section title="Provider">
                  <div className="space-y-3">
                    <div className="space-y-1.5">
                      <Label>Issuer</Label>
                      <Input value={form.oidcIssuer} onChange={(e) => set("oidcIssuer", e.target.value)} placeholder="https://accounts.google.com" disabled={!canWrite} />
                    </div>
                    <div className="grid gap-3 sm:grid-cols-2">
                      <div className="space-y-1.5">
                        <Label>Client ID</Label>
                        <Input value={form.oidcClientId} onChange={(e) => set("oidcClientId", e.target.value)} disabled={!canWrite} />
                      </div>
                      <div className="space-y-1.5">
                        <Label>Client secret</Label>
                        <Input type="password" value={form.oidcClientSecret} onChange={(e) => set("oidcClientSecret", e.target.value)} autoComplete="new-password" disabled={!canWrite} />
                      </div>
                    </div>
                  </div>
                </Section>
                <Section title="Allowed emails">
                  <ListEditor values={form.oidcAllowedEmails} onChange={(v) => set("oidcAllowedEmails", v)} placeholder="alice@example.com" />
                </Section>
                <Section title="Allowed domains">
                  <ListEditor values={form.oidcAllowedDomains} onChange={(v) => set("oidcAllowedDomains", v)} placeholder="example.com" />
                </Section>
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>

        {canWrite && (
          <div className="mt-6 flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={() => setForm(initial)}>
              Reset
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? "Saving…" : "Save policy"}
            </Button>
          </div>
        )}
      </form>
    </div>
  );
}
