import { Link } from "react-router-dom";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { newEndpointLink } from "@/lib/endpoints";
import type { ConnectionStatus } from "@/api/types";

export function HostnamesTable({
  hostnames,
  connectionId,
  canWrite,
}: {
  hostnames: NonNullable<ConnectionStatus["hostnames"]>;
  connectionId: string;
  canWrite: boolean;
}): React.JSX.Element {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Hostnames</CardTitle>
      </CardHeader>
      <CardContent>
        {hostnames.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No observed hostnames.
          </p>
        ) : (
          <div className="space-y-3">
            {hostnames.map((host) => (
              <div
                key={`${host.host}:${host.endpoint_id ?? ""}`}
                className="rounded-md border p-3 text-sm"
              >
                <p className="break-all font-medium">{host.host}</p>
                <p>{host.state}</p>
                {host.endpoint_id && (
                  <Link
                    className="underline"
                    to={`/endpoints/${encodeURIComponent(host.endpoint_id)}`}
                  >
                    Open endpoint
                  </Link>
                )}
                {host.state === "unclaimed" && canWrite && (
                  <Link
                    className="underline"
                    to={newEndpointLink({
                      method: "proxy",
                      domain: host.host,
                      connectionId,
                    })}
                  >
                    Reserve endpoint
                  </Link>
                )}
                {host.detail && <p>{host.detail}</p>}
                {host.fix && <p>{host.fix}</p>}
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}
