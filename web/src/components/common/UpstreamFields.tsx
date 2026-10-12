import { AlertTriangle } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ListEditor } from "@/components/common/KeyValueEditor";
import { FormSection } from "@/components/common/FormSection";
import type { EndpointPolicy, FrontDoorKind, LoadBalanceMode, Service } from "@/api/types";

export interface UpstreamForm {
  targets: string[];
  loadBalance: LoadBalanceMode;
  originServerName: string;
  originPins: string[];
  originCaPem: string;
  originInsecure: boolean;
  service: string;
  frontDoor: FrontDoorKind;
}

export interface UpstreamError {
  field: "targets" | "frontDoor";
  message: string;
}

const NONE_VALUE = "__none__";

export function emptyUpstreamForm(): UpstreamForm {
  return {
    targets: [""],
    loadBalance: "",
    originServerName: "",
    originPins: [],
    originCaPem: "",
    originInsecure: false,
    service: "",
    frontDoor: "",
  };
}

export function upstreamFormFromPolicy(policy?: EndpointPolicy | null): UpstreamForm {
  const targets = policy?.proxy_targets ?? [];
  return {
    targets: targets.length > 0 ? targets : [""],
    loadBalance: policy?.load_balance ?? "",
    originServerName: policy?.origin_tls?.server_name ?? "",
    originPins: policy?.origin_tls?.pin_sha256 ?? [],
    originCaPem: policy?.origin_tls?.ca_pem ?? "",
    originInsecure: policy?.origin_tls?.insecure_skip_verify ?? false,
    service: policy?.service ?? "",
    frontDoor: policy?.front_door ?? "",
  };
}

export function cleanTargets(form: UpstreamForm): string[] {
  return form.targets.map((t) => t.trim()).filter(Boolean);
}

export function hasHttpsTarget(form: UpstreamForm): boolean {
  return cleanTargets(form).some((t) => t.toLowerCase().startsWith("https://"));
}

export function upstreamPolicyFields(form: UpstreamForm, isProxy: boolean): Partial<EndpointPolicy> {
  const fields: Partial<EndpointPolicy> = {
    service: form.service || undefined,
    front_door: form.frontDoor || undefined,
  };
  if (!isProxy) return fields;
  const targets = cleanTargets(form);
  fields.proxy_targets = targets;
  fields.load_balance = targets.length > 1 && form.loadBalance ? form.loadBalance : undefined;
  const pins = form.originPins.map((p) => p.trim()).filter(Boolean);
  const hasOriginTls =
    form.originServerName.trim() || pins.length > 0 || form.originCaPem.trim() || form.originInsecure;
  fields.origin_tls =
    hasHttpsTarget(form) && hasOriginTls
      ? {
          server_name: form.originServerName.trim() || undefined,
          pin_sha256: pins.length > 0 ? pins : undefined,
          ca_pem: form.originCaPem.trim() || undefined,
          insecure_skip_verify: form.originInsecure || undefined,
        }
      : undefined;
  return fields;
}

export function validateUpstream(form: UpstreamForm, isProxy: boolean, domain: string): UpstreamError | null {
  if (isProxy && cleanTargets(form).length === 0) {
    return { field: "targets", message: "Add at least one proxy target." };
  }
  if (form.frontDoor && !domain.trim()) {
    return { field: "frontDoor", message: "A front door requires a custom domain on the endpoint." };
  }
  return null;
}

function SectionError({ message }: { message: string }) {
  return (
    <p className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive" role="alert">
      {message}
    </p>
  );
}

function advancedSummary(form: UpstreamForm, multipleTargets: boolean, httpsTarget: boolean): string {
  const parts: string[] = [];
  if (multipleTargets) parts.push(form.loadBalance === "failover" ? "failover" : "round robin");
  if (httpsTarget) {
    if (form.originInsecure) parts.push("verification skipped");
    else if (form.originPins.some((pin) => pin.trim())) parts.push("pinned TLS");
    else if (form.originCaPem.trim()) parts.push("custom CA");
    else if (form.originServerName.trim()) parts.push("custom server name");
  }
  return parts.length > 0 ? parts.join(" · ") : "Defaults";
}

export function UpstreamFields({
  form,
  onChange,
  isProxy,
  services,
  frontDoorEnabled,
  disabled,
  error,
}: {
  form: UpstreamForm;
  onChange: (form: UpstreamForm) => void;
  isProxy: boolean;
  services: Service[];
  frontDoorEnabled: boolean;
  disabled?: boolean;
  error?: UpstreamError | null;
}) {
  const set = <K extends keyof UpstreamForm>(key: K, value: UpstreamForm[K]) =>
    onChange({ ...form, [key]: value });
  const multipleTargets = cleanTargets(form).length > 1;
  const httpsTarget = hasHttpsTarget(form);
  const showServiceSelect = services.length > 0 || form.service !== "";
  const showFrontDoorSelect = frontDoorEnabled || form.frontDoor !== "";
  const showAdvanced = isProxy && (multipleTargets || httpsTarget);
  const serviceNames = services.map((s) => s.name);
  const unlistedService = form.service !== "" && !serviceNames.includes(form.service);

  if (!isProxy && !showServiceSelect && !showFrontDoorSelect) {
    return (
      <p className="text-sm text-muted-foreground">
        No services or front doors are configured for this organization.
      </p>
    );
  }

  return (
    <div className="space-y-4">
      {isProxy && (
        <FormSection
          title="Proxy targets"
          description="Backends mishmesh reverse-proxies to directly, without an agent."
          collapsible={false}
        >
          <ListEditor
            values={form.targets}
            onChange={(v) => set("targets", v)}
            placeholder="127.0.0.1:8080 or https://127.0.0.1:9443"
          />
          {error?.field === "targets" && <SectionError message={error.message} />}
        </FormSection>
      )}

      {showServiceSelect && (
        <FormSection
          title="Scale to zero"
          description="Wake a service on demand and scale it to zero when idle."
          summary={form.service || "Off"}
          defaultOpen={form.service !== ""}
        >
          <Select
            value={form.service || NONE_VALUE}
            onValueChange={(v) => set("service", v === NONE_VALUE ? "" : v)}
            disabled={disabled}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE_VALUE}>None</SelectItem>
              {unlistedService && <SelectItem value={form.service}>{form.service}</SelectItem>}
              {serviceNames.map((name) => (
                <SelectItem key={name} value={name}>
                  {name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </FormSection>
      )}

      {showFrontDoorSelect && (
        <FormSection
          title="Exposure"
          description="Publish this domain through a managed front door. Requires a custom domain."
          summary={form.frontDoor === "cloudflare" ? "Cloudflare" : "Off"}
          defaultOpen={form.frontDoor !== ""}
          hasError={error?.field === "frontDoor"}
        >
          <Select
            value={form.frontDoor || NONE_VALUE}
            onValueChange={(v) => set("frontDoor", v === NONE_VALUE ? "" : (v as FrontDoorKind))}
            disabled={disabled}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={NONE_VALUE}>None</SelectItem>
              <SelectItem value="cloudflare">Cloudflare</SelectItem>
            </SelectContent>
          </Select>
          {error?.field === "frontDoor" && <SectionError message={error.message} />}
        </FormSection>
      )}

      {showAdvanced && (
        <FormSection title="Advanced" summary={advancedSummary(form, multipleTargets, httpsTarget)}>
          {multipleTargets && (
            <div className="space-y-1.5">
              <Label>Load balancing</Label>
              <Select
                value={form.loadBalance || "round_robin"}
                onValueChange={(v) => set("loadBalance", v as LoadBalanceMode)}
                disabled={disabled}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="round_robin">Round robin</SelectItem>
                  <SelectItem value="failover">Failover (first healthy target)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}

          {httpsTarget && (
            <div className="space-y-3">
              <div>
                <h4 className="text-sm font-medium tracking-heading">Origin TLS</h4>
                <p className="text-xs text-muted-foreground">How mishmesh verifies the certificate of https:// targets.</p>
              </div>
              <div className="space-y-1.5">
                <Label>Server name</Label>
                <Input
                  value={form.originServerName}
                  onChange={(e) => set("originServerName", e.target.value)}
                  placeholder="defaults to the target host"
                  disabled={disabled}
                />
              </div>
              <div className="space-y-1.5">
                <Label>Pinned certificate SHA-256 (base64 SPKI)</Label>
                <ListEditor values={form.originPins} onChange={(v) => set("originPins", v)} placeholder="base64 of the 32-byte hash" />
              </div>
              <FormSection
                title="More TLS options"
                summary={form.originInsecure ? "verification skipped" : form.originCaPem.trim() ? "custom CA" : "Defaults"}
                defaultOpen={form.originInsecure || form.originCaPem.trim() !== ""}
                nested
              >
                <div className="space-y-1.5">
                  <Label>CA certificate (PEM)</Label>
                  <Textarea
                    value={form.originCaPem}
                    onChange={(e) => set("originCaPem", e.target.value)}
                    placeholder="-----BEGIN CERTIFICATE-----"
                    className="font-mono text-xs"
                    disabled={disabled}
                  />
                </div>
                <div className="space-y-2 rounded-md border border-border p-3">
                  <div className="flex items-center justify-between gap-3">
                    <div>
                      <p className="text-sm font-medium">Skip certificate verification</p>
                      <p className="text-xs text-muted-foreground">Accept any certificate presented by the origin.</p>
                    </div>
                    <Switch
                      checked={form.originInsecure}
                      onCheckedChange={(v) => set("originInsecure", v)}
                      disabled={disabled}
                    />
                  </div>
                  {form.originInsecure && (
                    <p className="flex items-start gap-2 text-xs text-amber-600 dark:text-amber-400">
                      <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                      Traffic to the origin can be intercepted. Use a pin or CA certificate instead whenever possible.
                    </p>
                  )}
                </div>
              </FormSection>
            </div>
          )}
        </FormSection>
      )}
    </div>
  );
}
