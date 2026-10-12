import { Link } from "react-router-dom";
import { Cloud, ExternalLink, Globe, Pencil } from "lucide-react";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/common/CopyButton";
import { HelpTip } from "@/components/common/HelpTip";
import { CloudflareLevelBadge, HelpedStateBadge } from "@/components/common/TermBadges";
import { StateDot } from "@/components/common/StatusDot";
import { PipelineCard } from "@/components/endpoints/PipelineCard";
import { PublishControl, usePublicAccessMutation } from "@/components/endpoints/PublishControl";
import { HELP } from "@/lib/glossary";
import { connectorLabel, connectorState, hostnameRecord, isCloudflareConnected } from "@/lib/cloudflare";
import { endpointHostname, isEphemeral, supportsPublicAccess } from "@/lib/endpoints";
import type { Cloudflare, Endpoint } from "@/api/types";

export function CloudflareStage({
  endpoint,
  cloudflare,
  canWrite,
}: {
  endpoint: Endpoint;
  cloudflare?: Cloudflare;
  canWrite: boolean;
}) {
  const { unpublish, pending } = usePublicAccessMutation(endpoint);
  const published = endpoint.policy?.public_access === "cloudflare";
  const connected = isCloudflareConnected(cloudflare);

  if (published) {
    const connector = cloudflare?.status?.connector;
    const state = connected ? connectorState(connector) : "down";
    return (
      <PipelineCard
        stage="Cloudflare"
        icon={Cloud}
        state={state}
        title={
          <Link to="/cloudflare" className="hover:underline">
            {cloudflare?.connection?.name ?? "Cloudflare"}
          </Link>
        }
        actions={
          canWrite && (
            <Button type="button" size="sm" variant="outline" disabled={pending} onClick={unpublish}>
              {pending ? "Unpublishing…" : "Unpublish"}
            </Button>
          )
        }
      >
        {connected && cloudflare ? (
          <>
            <CloudflareLevelBadge level={cloudflare.level} />
            <StateDot state={state} label={connectorLabel(connector)} />
          </>
        ) : (
          <p className="text-xs text-destructive">
            Published, but the org has no Cloudflare connection. <Link to="/cloudflare" className="underline">Connect Cloudflare</Link>.
          </p>
        )}
      </PipelineCard>
    );
  }

  if (isEphemeral(endpoint)) {
    return (
      <PipelineCard stage="Cloudflare" icon={Cloud} empty>
        <p className="text-xs text-muted-foreground">Ephemeral endpoints cannot be published. Reserve one to publish it.</p>
      </PipelineCard>
    );
  }

  if (!connected) {
    return (
      <PipelineCard
        stage="Cloudflare"
        icon={Cloud}
        empty
        actions={
          <Button asChild size="sm" variant="outline">
            <Link to="/cloudflare">Connect Cloudflare</Link>
          </Button>
        }
      >
        <p className="flex items-center gap-1 text-xs text-muted-foreground">
          Not published. Connect a Cloudflare tunnel to put this on the internet.
          <HelpTip entry={HELP.publicAccess} />
        </p>
      </PipelineCard>
    );
  }

  return (
    <PipelineCard stage="Cloudflare" icon={Cloud} empty actions={canWrite && <PublishControl endpoint={endpoint} />}>
      <p className="flex items-center gap-1 text-xs text-muted-foreground">
        Not published through{" "}
        <Link to="/cloudflare" className="font-medium text-foreground hover:underline">
          {cloudflare?.connection?.name}
        </Link>
        <HelpTip entry={HELP.publicAccess} />
      </p>
    </PipelineCard>
  );
}

export function HostnameStage({
  endpoint,
  cloudflare,
  canWrite,
  onEditAddress,
}: {
  endpoint: Endpoint;
  cloudflare?: Cloudflare;
  canWrite: boolean;
  onEditAddress: () => void;
}) {
  const published = endpoint.policy?.public_access === "cloudflare";
  const record = published ? hostnameRecord(cloudflare, endpoint.domain) : undefined;
  const hostname = endpoint.kind === "tcp" ? (endpoint.public_url ?? (endpoint.port ? `:${endpoint.port}` : "")) : endpointHostname(endpoint);
  const showPublish =
    canWrite && !published && isCloudflareConnected(cloudflare) && supportsPublicAccess(endpoint) && !isEphemeral(endpoint);
  const hostState = published ? (record?.state ?? "pending") : undefined;

  return (
    <PipelineCard
      stage={endpoint.kind === "tcp" ? "Address" : "Hostname"}
      icon={Globe}
      state={hostState}
      title={
        hostname ? (
          <span className="flex items-center gap-1">
            <span className="truncate font-mono text-xs">{hostname}</span>
            {endpoint.public_url && <CopyButton value={endpoint.public_url} label="URL" />}
            {endpoint.public_url && endpoint.kind === "http" && (
              <a href={endpoint.public_url} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground" aria-label="Open">
                <ExternalLink className="h-3.5 w-3.5" />
              </a>
            )}
          </span>
        ) : (
          <span className="text-muted-foreground">No address yet</span>
        )
      }
      actions={
        canWrite && (
          <>
            {!isEphemeral(endpoint) && (
              <Button type="button" size="sm" variant="outline" onClick={onEditAddress}>
                <Pencil className="h-3.5 w-3.5" /> Edit address
              </Button>
            )}
            {showPublish && <PublishControl endpoint={endpoint} />}
          </>
        )
      }
    >
      {published ? (
        <>
          <HelpedStateBadge scope="hostname" state={hostState ?? "pending"} />
          {record?.detail && <p className="text-xs text-muted-foreground">{record.detail}</p>}
          {cloudflare?.level === "imported" && record && record.state !== "published" && record.fix && (
            <p className="rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs">
              <span className="font-medium">Fix in Cloudflare: </span>
              {record.fix}
            </p>
          )}
          {!record && <p className="text-xs text-muted-foreground">Waiting for the tunnel to report this hostname.</p>}
        </>
      ) : (
        supportsPublicAccess(endpoint) && <p className="text-xs text-muted-foreground">Reachable through mishmesh ingress only.</p>
      )}
    </PipelineCard>
  );
}
