import type { ComponentType } from "react";
import { ManagementSection } from "@/components/cloudflare/ManagementSection";
import type { TypedConnection } from "@/api/types";

function TailscalePanel({
  connection,
}: {
  connection: TypedConnection;
}): React.JSX.Element {
  return (
    <section className="space-y-2 text-sm">
      <h3 className="font-medium">Tailscale host</h3>
      <p>
        HTTPS app access uses an existing tagged Tailscale host. Joining the
        tailnet, host approval, and access grants are configured in Tailscale.
      </p>
      <pre className="overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 text-xs">
        {JSON.stringify(connection.status.details ?? {}, null, 2)}
      </pre>
    </section>
  );
}

const PANELS: Record<string, ComponentType<{ connection: TypedConnection }>> = {
  cloudflare: ManagementSection,
  tailscale: TailscalePanel,
};

export function ConnectionPanel({
  connection,
}: {
  connection: TypedConnection;
}): React.JSX.Element | null {
  const Panel = PANELS[connection.type];
  return Panel ? <Panel connection={connection} /> : null;
}
