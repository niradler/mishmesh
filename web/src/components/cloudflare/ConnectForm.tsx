import { useState, type FormEvent, type ReactNode } from "react";
import { Radar } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { FormSection } from "@/components/common/FormSection";
import { FormError } from "@/components/common/ConfirmDialog";
import { HelpTip, LabelWithHelp } from "@/components/common/HelpTip";
import { StateDot } from "@/components/common/StatusDot";
import { useDetectCloudflare, useSaveCloudflare } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { CONNECTOR_HELP, HELP } from "@/lib/glossary";
import {
  DEFAULT_CONNECTION_NAME,
  DEFAULT_METRICS_ADDR,
  connectorLabel,
  connectorState,
  inputFromConnection,
} from "@/lib/cloudflare";
import type { CloudflareConnection, CloudflareConnector, CloudflareDetectResult } from "@/api/types";

const CONNECTORS: { value: CloudflareConnector; label: string }[] = [
  { value: "external", label: "I run cloudflared" },
  { value: "managed", label: "mishmesh runs cloudflared" },
];

function PreviewRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-0.5 sm:grid-cols-[10rem_1fr] sm:gap-3">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words text-sm">{children}</dd>
    </div>
  );
}

function DetectPreview({ result }: { result: CloudflareDetectResult }) {
  const hostnames = result.hostnames ?? [];
  const edges = result.connector?.edge_locations ?? [];
  return (
    <dl className="space-y-2 rounded-md border border-border bg-muted/30 p-3">
      <PreviewRow label="Account">
        <span className="font-mono text-xs">{result.account_id || "not in the token"}</span>
      </PreviewRow>
      <PreviewRow label="Tunnel">
        <span className="font-mono text-xs">{result.tunnel_id || "not in the token"}</span>
      </PreviewRow>
      <PreviewRow label="Connector">
        <StateDot state={connectorState(result.connector)} label={connectorLabel(result.connector)} />
        {result.connector?.error && <p className="text-xs text-destructive">{result.connector.error}</p>}
      </PreviewRow>
      <PreviewRow label="Edge locations">{edges.length > 0 ? edges.join(", ") : <span className="text-muted-foreground">none</span>}</PreviewRow>
      <PreviewRow label="Hostnames">
        {hostnames.length > 0 ? (
          <span className="flex flex-col gap-0.5">
            {hostnames.map((host) => (
              <span key={host} className="font-mono text-xs">
                {host}
              </span>
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground">none on the tunnel yet</span>
        )}
      </PreviewRow>
    </dl>
  );
}

export function ConnectForm({
  connection,
  onSaved,
  onCancel,
}: {
  connection?: CloudflareConnection;
  onSaved?: () => void;
  onCancel?: () => void;
}) {
  const editing = !!connection;
  const save = useSaveCloudflare();
  const detect = useDetectCloudflare();
  const [name, setName] = useState(connection?.name ?? DEFAULT_CONNECTION_NAME);
  const [tunnelToken, setTunnelToken] = useState("");
  const [metricsAddr, setMetricsAddr] = useState(connection?.metrics_addr ?? DEFAULT_METRICS_ADDR);
  const [connector, setConnector] = useState<CloudflareConnector>(connection?.connector ?? "external");
  const [origin, setOrigin] = useState(connection?.origin ?? "");
  const [problem, setProblem] = useState<string | null>(null);

  const onDetect = () => {
    setProblem(null);
    detect.mutate({ metrics_addr: metricsAddr.trim() || DEFAULT_METRICS_ADDR, tunnel_token: tunnelToken.trim() || undefined });
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setProblem(null);
    if (!name.trim()) {
      setProblem("Give the connection a name.");
      return;
    }
    if (!editing && !tunnelToken.trim()) {
      setProblem("Paste the tunnel token.");
      return;
    }
    const changes = {
      name: name.trim(),
      connector,
      metrics_addr: metricsAddr.trim() || DEFAULT_METRICS_ADDR,
      origin: origin.trim() || undefined,
      tunnel_token: tunnelToken.trim() || undefined,
    };
    const input = connection ? inputFromConnection(connection, changes) : changes;
    save.mutate(input, {
      onSuccess: () => {
        toast({ title: editing ? "Connection saved" : "Cloudflare connected" });
        setTunnelToken("");
        onSaved?.();
      },
    });
  };

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="cf-name">Name</Label>
        <Input id="cf-name" value={name} onChange={(e) => setName(e.target.value)} placeholder={DEFAULT_CONNECTION_NAME} />
      </div>
      <div className="space-y-1.5">
        <LabelWithHelp htmlFor="cf-tunnel-token" entry={HELP.tunnelToken}>
          Tunnel token
        </LabelWithHelp>
        <Input
          id="cf-tunnel-token"
          type="password"
          value={tunnelToken}
          onChange={(e) => setTunnelToken(e.target.value)}
          placeholder={editing && connection?.tunnel_token_set ? "unchanged" : "eyJhIjoi…"}
          autoComplete="off"
        />
        <p className="text-xs text-muted-foreground">
          Paste the contents of <span className="font-mono">~/.cloudflared/&lt;name&gt;.token</span>, or the token from Zero Trust →
          Networks → Tunnels → Configure.
        </p>
      </div>
      <div className="space-y-1.5">
        <LabelWithHelp htmlFor="cf-metrics" entry={HELP.metricsAddr}>
          Metrics address
        </LabelWithHelp>
        <Input id="cf-metrics" value={metricsAddr} onChange={(e) => setMetricsAddr(e.target.value)} placeholder={DEFAULT_METRICS_ADDR} />
      </div>
      <div className="space-y-1.5">
        <Label>Connector</Label>
        <div className="flex flex-wrap items-center gap-3">
          {CONNECTORS.map((option) => (
            <span key={option.value} className="inline-flex items-center gap-1">
              <Button
                type="button"
                size="sm"
                variant={connector === option.value ? "default" : "outline"}
                aria-pressed={connector === option.value}
                onClick={() => setConnector(option.value)}
              >
                {option.label}
              </Button>
              <HelpTip entry={CONNECTOR_HELP[option.value]} />
            </span>
          ))}
        </div>
      </div>
      <FormSection title="Advanced" summary={origin.trim() ? "custom origin" : "Defaults"} defaultOpen={!!connection?.origin && editing}>
        <div className="space-y-1.5">
          <LabelWithHelp htmlFor="cf-origin" entry={HELP.origin}>
            Origin
          </LabelWithHelp>
          <Input id="cf-origin" value={origin} onChange={(e) => setOrigin(e.target.value)} placeholder="defaults to the mishmesh ingress" />
        </div>
      </FormSection>

      <div className="space-y-2">
        <Button type="button" variant="outline" size="sm" onClick={onDetect} disabled={detect.isPending}>
          <Radar className="h-3.5 w-3.5" /> {detect.isPending ? "Detecting…" : "Detect"}
        </Button>
        <p className="text-xs text-muted-foreground">Reads the token and the running cloudflared without saving anything.</p>
        {detect.data && <DetectPreview result={detect.data} />}
        <FormError message={detect.error ? errorMessage(detect.error) : undefined} />
      </div>

      <FormError message={problem ?? (save.error ? errorMessage(save.error) : undefined)} />
      <div className="flex justify-end gap-2">
        {onCancel && (
          <Button type="button" variant="outline" onClick={onCancel}>
            Cancel
          </Button>
        )}
        <Button type="submit" disabled={save.isPending}>
          {save.isPending ? "Saving…" : editing ? "Save connection" : "Save"}
        </Button>
      </div>
    </form>
  );
}
