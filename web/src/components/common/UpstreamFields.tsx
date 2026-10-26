import { AlertTriangle } from "lucide-react";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ListEditor } from "@/components/common/KeyValueEditor";
import { FormSection } from "@/components/common/FormSection";
import { HelpTip, LabelWithHelp } from "@/components/common/HelpTip";
import { HELP } from "@/lib/glossary";
import type { EndpointPolicy, LoadBalanceMode } from "@/api/types";

export interface UpstreamForm {
  targets: string[];
  loadBalance: LoadBalanceMode;
  originServerName: string;
  originPins: string[];
  originCaPem: string;
  originInsecure: boolean;
}

export function emptyUpstreamForm(): UpstreamForm {
  return {
    targets: [""],
    loadBalance: "",
    originServerName: "",
    originPins: [],
    originCaPem: "",
    originInsecure: false,
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
  };
}

export function cleanTargets(form: UpstreamForm): string[] {
  return form.targets.map((t) => t.trim()).filter(Boolean);
}

export function hasHttpsTarget(form: UpstreamForm): boolean {
  return cleanTargets(form).some((t) => t.toLowerCase().startsWith("https://"));
}

export function upstreamPolicyFields(form: UpstreamForm): Partial<EndpointPolicy> {
  const targets = cleanTargets(form);
  const pins = form.originPins.map((p) => p.trim()).filter(Boolean);
  const hasOriginTls =
    form.originServerName.trim() || pins.length > 0 || form.originCaPem.trim() || form.originInsecure;
  return {
    proxy_targets: targets,
    load_balance: targets.length > 1 && form.loadBalance ? form.loadBalance : undefined,
    origin_tls:
      hasHttpsTarget(form) && hasOriginTls
        ? {
            server_name: form.originServerName.trim() || undefined,
            pin_sha256: pins.length > 0 ? pins : undefined,
            ca_pem: form.originCaPem.trim() || undefined,
            insecure_skip_verify: form.originInsecure || undefined,
          }
        : undefined,
  };
}

export function validateTargets(form: UpstreamForm): string | null {
  return cleanTargets(form).length === 0 ? "Add at least one proxy target." : null;
}

export function upstreamAdvancedSummary(form: UpstreamForm): string {
  const parts: string[] = [];
  if (cleanTargets(form).length > 1) parts.push(form.loadBalance === "failover" ? "failover" : "round robin");
  if (hasHttpsTarget(form)) {
    if (form.originInsecure) parts.push("verification skipped");
    else if (form.originPins.some((pin) => pin.trim())) parts.push("pinned TLS");
    else if (form.originCaPem.trim()) parts.push("custom CA");
    else if (form.originServerName.trim()) parts.push("custom server name");
  }
  return parts.length > 0 ? parts.join(" · ") : "Defaults";
}

export function TargetsField({
  form,
  onChange,
  error,
}: {
  form: UpstreamForm;
  onChange: (form: UpstreamForm) => void;
  error?: string | null;
}) {
  return (
    <div className="space-y-1.5">
      <LabelWithHelp entry={HELP.proxyTargets}>Targets</LabelWithHelp>
      <ListEditor
        values={form.targets}
        onChange={(targets) => onChange({ ...form, targets })}
        placeholder="127.0.0.1:8080 or https://127.0.0.1:9443"
      />
      {error && (
        <p className="text-sm text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

export function UpstreamAdvancedFields({
  form,
  onChange,
  disabled,
}: {
  form: UpstreamForm;
  onChange: (form: UpstreamForm) => void;
  disabled?: boolean;
}) {
  const set = <K extends keyof UpstreamForm>(key: K, value: UpstreamForm[K]) => onChange({ ...form, [key]: value });
  const multipleTargets = cleanTargets(form).length > 1;
  const httpsTarget = hasHttpsTarget(form);

  if (!multipleTargets && !httpsTarget) {
    return (
      <p className="text-sm text-muted-foreground">
        Load balancing appears with two or more targets; origin TLS appears with an https:// target.
      </p>
    );
  }

  return (
    <div className="space-y-4">
      {multipleTargets && (
        <div className="space-y-1.5">
          <LabelWithHelp entry={HELP.loadBalancing}>Load balancing</LabelWithHelp>
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
              <SelectItem value="sticky">Sticky (by client IP)</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )}

      {httpsTarget && (
        <div className="space-y-3">
          <div>
            <div className="flex items-center gap-1.5">
              <h4 className="text-sm font-medium tracking-heading">Origin TLS</h4>
              <HelpTip entry={HELP.originTls} />
            </div>
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
            <LabelWithHelp entry={HELP.pinned}>Pinned certificate SHA-256 (base64 SPKI)</LabelWithHelp>
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
                <Switch checked={form.originInsecure} onCheckedChange={(v) => set("originInsecure", v)} disabled={disabled} />
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
    </div>
  );
}
