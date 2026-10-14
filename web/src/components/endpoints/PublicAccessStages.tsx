import { Globe, Pencil } from "lucide-react";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/common/CopyButton";
import { PipelineCard } from "@/components/endpoints/PipelineCard";
import { endpointHostname, isEphemeral } from "@/lib/endpoints";
import type { Endpoint } from "@/api/types";

export function HostnameStage({
  endpoint,
  canWrite,
  onEditAddress,
}: {
  endpoint: Endpoint;
  canWrite: boolean;
  onEditAddress: () => void;
}): React.JSX.Element {
  const portKind = endpoint.kind === "tcp" || endpoint.kind === "udp";
  const address = portKind
    ? (endpoint.public_url ?? String(endpoint.port ?? ""))
    : endpointHostname(endpoint);
  return (
    <PipelineCard
      stage={portKind ? "Address" : "Hostname"}
      icon={Globe}
      title={
        <span className="break-all font-mono text-xs">
          {address || "No address yet"}
        </span>
      }
      actions={
        canWrite && !isEphemeral(endpoint) ? (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onEditAddress}
          >
            <Pencil className="h-3.5 w-3.5" />
            Edit address
          </Button>
        ) : undefined
      }
    >
      {endpoint.public_url && (
        <CopyButton value={endpoint.public_url} label="URL" />
      )}
      <p className="text-xs text-muted-foreground">
        {endpoint.policy?.access?.length ?? 0} access connections
      </p>
    </PipelineCard>
  );
}
