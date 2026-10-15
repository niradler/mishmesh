import type { TypedConnection } from "@/api/types";

const TOKEN_PERMISSIONS = [
  "Account · Cloudflare Tunnel · Edit",
  "Zone · DNS · Edit",
  "Zone · Zone · Read",
  "Account · Access: Apps and Policies · Edit",
];

export function ManagementSection({
  connection,
}: {
  connection: TypedConnection;
}): React.JSX.Element {
  return (
    <section className="space-y-2 text-sm">
      <h3 className="font-medium">Cloudflare management</h3>
      <p>
        Adding an API token enables management of owned ingress, DNS and Access
        hostnames. An imported connection observes the existing tunnel.
      </p>
      <p>Required API token permissions:</p>
      <ul className="list-inside list-disc text-xs">
        {TOKEN_PERMISSIONS.map((permission) => (
          <li key={permission}>{permission}</li>
        ))}
      </ul>
      <p>
        Prune is {connection.config.prune ? "enabled" : "disabled"}. Review the
        Prune field before enabling it.
      </p>
      <pre className="overflow-auto whitespace-pre-wrap break-all rounded-md bg-muted p-3 text-xs">
        {JSON.stringify(connection.status.details ?? {}, null, 2)}
      </pre>
    </section>
  );
}
