import {
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { KeyValueEditor, ListEditor } from "@/components/common/KeyValueEditor";
import { UpstreamAdvancedFields } from "@/components/common/UpstreamFields";
import {
  policyFormFromPolicy,
  policyFromForm,
  oidcSecretIsStored,
  type PolicyForm,
} from "@/components/endpoints/policyForm";
import { useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { useTabParam } from "@/hooks/use-tab-param";
import type { Endpoint } from "@/api/types";

const TABS = ["routing", "headers", "security", "advanced"] as const;

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className="space-y-3 border-b border-border pb-6 last:border-0 last:pb-0">
      <div>
        <h3 className="text-sm font-medium tracking-heading">{title}</h3>
        {description && (
          <p className="text-xs text-muted-foreground">{description}</p>
        )}
      </div>
      {children}
    </div>
  );
}

function ToggleRow({
  title,
  description,
  checked,
  onChange,
  disabled,
}: {
  title: string;
  description: string;
  checked: boolean;
  onChange: (value: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border border-border p-3">
      <div>
        <p className="text-sm font-medium">{title}</p>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
      <Switch
        checked={checked}
        onCheckedChange={onChange}
        disabled={disabled}
      />
    </div>
  );
}

export function EndpointPolicyForm({
  endpoint,
  canWrite,
}: {
  endpoint: Endpoint;
  canWrite: boolean;
}) {
  const update = useUpdateEndpoint();
  const isProxy = endpoint.method === "proxy";
  const isHttp = endpoint.kind === "http";
  const initial = useMemo(
    () => policyFormFromPolicy(endpoint.policy),
    [endpoint.policy],
  );
  const [form, setForm] = useState<PolicyForm>(initial);
  const [tab, setTab] = useTabParam(TABS, "routing");
  const disabled = !canWrite;

  useEffect(() => setForm(initial), [initial]);

  const set = <K extends keyof PolicyForm>(key: K, value: PolicyForm[K]) =>
    setForm((f) => ({ ...f, [key]: value }));

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (
      form.maxBodyBytes &&
      (!Number.isInteger(Number(form.maxBodyBytes)) ||
        Number(form.maxBodyBytes) < 0)
    ) {
      toast({
        variant: "destructive",
        title: "Save failed",
        description: "Max body bytes must be a whole number.",
      });
      return;
    }
    update.mutate(
      {
        id: endpoint.id,
        policy: policyFromForm(form, isProxy, endpoint.policy),
      },
      {
        onSuccess: () => toast({ title: "Policy saved" }),
        onError: (err) =>
          toast({
            variant: "destructive",
            title: "Save failed",
            description: errorMessage(err),
          }),
      },
    );
  };

  return (
    <form onSubmit={onSubmit}>
      {!isHttp && (
        <p className="mb-3 rounded-md border border-border bg-muted/40 p-3 text-sm text-muted-foreground">
          This is a {endpoint.kind.toUpperCase()} endpoint. mishmesh passes raw
          bytes, so HTTP routing, headers and authentication below do not apply.
        </p>
      )}
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="routing">Routing</TabsTrigger>
          <TabsTrigger value="headers">Headers</TabsTrigger>
          <TabsTrigger value="security">Security</TabsTrigger>
          <TabsTrigger value="advanced">Advanced</TabsTrigger>
        </TabsList>

        <TabsContent value="routing">
          <Card>
            <CardHeader>
              <CardTitle>Routing & limits</CardTitle>
              <CardDescription>
                Rewrite paths and host, control body size and compression.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-6">
              <Section
                title="Agent Connect"
                description="Keep an existing private Connect name and its allowed agents independent of app Access."
              >
                <Label htmlFor="connect-name">Connect name</Label>
                <Input
                  id="connect-name"
                  value={form.connectName}
                  disabled={disabled}
                  onChange={(event) => set("connectName", event.target.value)}
                />
                <Label>Allowed agent IDs</Label>
                <ListEditor
                  values={form.connectAgents}
                  disabled={disabled}
                  onChange={(values) => set("connectAgents", values)}
                  placeholder="Agent ID or *"
                />
              </Section>
              <Section
                title="Host header"
                description="Override the Host header sent to your service."
              >
                <Input
                  value={form.hostHeader}
                  onChange={(e) => set("hostHeader", e.target.value)}
                  placeholder="internal.svc.local"
                  disabled={disabled}
                />
              </Section>
              <Section title="Path prefixes">
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label>Strip prefix</Label>
                    <Input
                      value={form.stripPathPrefix}
                      onChange={(e) => set("stripPathPrefix", e.target.value)}
                      placeholder="/api"
                      disabled={disabled}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>Add prefix</Label>
                    <Input
                      value={form.addPathPrefix}
                      onChange={(e) => set("addPathPrefix", e.target.value)}
                      placeholder="/v1"
                      disabled={disabled}
                    />
                  </div>
                </div>
              </Section>
              <Section title="Body & transport">
                <div className="space-y-1.5">
                  <Label>Max body bytes</Label>
                  <Input
                    type="number"
                    min={0}
                    value={form.maxBodyBytes}
                    onChange={(e) => set("maxBodyBytes", e.target.value)}
                    placeholder="unlimited"
                    disabled={disabled}
                  />
                </div>
                <ToggleRow
                  title="Compression"
                  description="gzip responses at the edge."
                  checked={form.compression}
                  onChange={(v) => set("compression", v)}
                  disabled={disabled}
                />
                <ToggleRow
                  title="Force HTTPS"
                  description="Redirect HTTP requests to HTTPS."
                  checked={form.forceHttps}
                  onChange={(v) => set("forceHttps", v)}
                  disabled={disabled}
                />
              </Section>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="headers">
          <Card>
            <CardHeader>
              <CardTitle>Header rewriting</CardTitle>
              <CardDescription>
                Add or remove headers on request and response.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-6">
              <Section title="Request headers · add">
                <KeyValueEditor
                  pairs={form.reqAdd}
                  onChange={(p) => set("reqAdd", p)}
                />
              </Section>
              <Section title="Request headers · remove">
                <ListEditor
                  values={form.reqRemove}
                  onChange={(v) => set("reqRemove", v)}
                  placeholder="X-Forwarded-For"
                />
              </Section>
              <Section title="Response headers · add">
                <KeyValueEditor
                  pairs={form.resAdd}
                  onChange={(p) => set("resAdd", p)}
                />
              </Section>
              <Section title="Response headers · remove">
                <ListEditor
                  values={form.resRemove}
                  onChange={(v) => set("resRemove", v)}
                  placeholder="Server"
                />
              </Section>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="security">
          <Card>
            <CardHeader>
              <CardTitle>Access control</CardTitle>
              <CardDescription>
                Basic auth, IP allow and deny lists, and OIDC login.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-6">
              <Section
                title="Basic auth"
                description={
                  form.basicAuthSet
                    ? "A password is set. Leave the password empty to keep it."
                    : "Require a username and password."
                }
              >
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label>Username</Label>
                    <Input
                      value={form.basicAuthUser}
                      onChange={(e) => set("basicAuthUser", e.target.value)}
                      autoComplete="off"
                      disabled={disabled}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>Password</Label>
                    <Input
                      type="password"
                      value={form.basicAuthPassword}
                      onChange={(e) => set("basicAuthPassword", e.target.value)}
                      autoComplete="new-password"
                      placeholder={form.basicAuthSet ? "unchanged" : ""}
                      disabled={disabled}
                    />
                  </div>
                </div>
              </Section>
              <Section
                title="IP allow list"
                description="CIDRs permitted to reach this endpoint."
              >
                <ListEditor
                  values={form.ipAllow}
                  onChange={(v) => set("ipAllow", v)}
                  placeholder="10.0.0.0/8"
                />
              </Section>
              <Section
                title="IP deny list"
                description="CIDRs blocked from this endpoint."
              >
                <ListEditor
                  values={form.ipDeny}
                  onChange={(v) => set("ipDeny", v)}
                  placeholder="192.168.1.0/24"
                />
              </Section>
              <Section
                title="OIDC login"
                description="Require visitors to sign in with an OIDC provider."
              >
                <ToggleRow
                  title="Enable OIDC"
                  description="Gate this endpoint behind a login."
                  checked={form.oidcEnabled}
                  onChange={(v) => set("oidcEnabled", v)}
                  disabled={disabled}
                />
                <div className="space-y-1.5">
                  <Label>Issuer</Label>
                  <Input
                    value={form.oidcIssuer}
                    onChange={(e) => set("oidcIssuer", e.target.value)}
                    placeholder="https://accounts.google.com"
                    disabled={disabled}
                  />
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label>Client ID</Label>
                    <Input
                      value={form.oidcClientId}
                      onChange={(e) => set("oidcClientId", e.target.value)}
                      disabled={disabled}
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>Client secret</Label>
                    <Input
                      type="password"
                      value={
                        oidcSecretIsStored(form) ? "" : form.oidcClientSecret
                      }
                      onChange={(e) =>
                        set(
                          "oidcClientSecret",
                          e.target.value ||
                            (initial.oidcClientSecret === "set" ? "set" : ""),
                        )
                      }
                      placeholder={oidcSecretIsStored(form) ? "unchanged" : ""}
                      autoComplete="new-password"
                      disabled={disabled}
                    />
                  </div>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <Label>Allowed emails</Label>
                    <ListEditor
                      values={form.oidcAllowedEmails}
                      onChange={(v) => set("oidcAllowedEmails", v)}
                      placeholder="alice@example.com"
                    />
                  </div>
                  <div className="space-y-1.5">
                    <Label>Allowed domains</Label>
                    <ListEditor
                      values={form.oidcAllowedDomains}
                      onChange={(v) => set("oidcAllowedDomains", v)}
                      placeholder="example.com"
                    />
                  </div>
                </div>
              </Section>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="advanced">
          <Card>
            <CardHeader>
              <CardTitle>Advanced</CardTitle>
              <CardDescription>
                Load balancing across targets and how https:// origins are
                verified.
              </CardDescription>
            </CardHeader>
            <CardContent>
              {isProxy ? (
                <UpstreamAdvancedFields
                  form={form.upstream}
                  onChange={(v) => set("upstream", v)}
                  disabled={disabled}
                />
              ) : (
                <p className="text-sm text-muted-foreground">
                  Load balancing and origin TLS apply to proxy endpoints. This
                  endpoint reaches its service through an agent.
                </p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      {canWrite && (
        <div className="mt-6 flex justify-end gap-2">
          <Button
            type="button"
            variant="outline"
            onClick={() => setForm(initial)}
          >
            Reset
          </Button>
          <Button type="submit" disabled={update.isPending}>
            {update.isPending ? "Saving…" : "Save policy"}
          </Button>
        </div>
      )}
    </form>
  );
}
