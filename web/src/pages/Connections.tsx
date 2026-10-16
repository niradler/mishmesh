import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { PageHeader } from "@/components/common/PageHeader";
import {
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/common/States";
import { ConnectionForm } from "@/components/connections/ConnectionForm";
import { StateBadge } from "@/components/common/StateBadge";
import { useConnections, useConnectionTypes } from "@/api/hooks";
import { connectionScope } from "@/lib/connections";
import { useSession } from "@/context/SessionContext";

export function Connections(): React.JSX.Element {
  const { currentOrgId, canWrite } = useSession();
  const connections = useConnections(currentOrgId);
  const types = useConnectionTypes(currentOrgId);
  const navigate = useNavigate();
  const [adding, setAdding] = useState(false);
  const names = new Map((types.data ?? []).map((type) => [type.type, type]));
  return (
    <div className="space-y-4">
      <PageHeader
        title="Connections"
        description="App access over existing networks. Public access through Cloudflare and private HTTPS access through Tailscale."
        actions={
          canWrite ? (
            <Button onClick={() => setAdding(true)}>Add connection</Button>
          ) : undefined
        }
      />
      <Card>
        <CardContent className="pt-4">
          {connections.isLoading ? (
            <LoadingState label="Loading connections" />
          ) : connections.isError ? (
            <ErrorState error={connections.error} />
          ) : !connections.data?.length ? (
            <EmptyState
              title="No connections"
              description="Add an existing access connection to publish your apps."
            />
          ) : (
            <div className="overflow-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b">
                    <th className="p-3">Name</th>
                    <th>Type</th>
                    <th>Role</th>
                    <th>Scope</th>
                    <th>Level</th>
                    <th>Health</th>
                  </tr>
                </thead>
                <tbody>
                  {connections.data.map((connection) => {
                    const descriptor = names.get(connection.type);
                    return (
                      <tr key={connection.id} className="border-b">
                        <td className="p-3">
                          <Link
                            className="font-medium underline"
                            to={`/connections/${encodeURIComponent(connection.id)}`}
                          >
                            {connection.name}
                          </Link>
                        </td>
                        <td>{descriptor?.label ?? connection.type}</td>
                        <td>{descriptor?.role ?? "Unknown"}</td>
                        <td>{connectionScope(descriptor)}</td>
                        <td>{connection.status.level}</td>
                        <td>
                          <StateBadge
                            state={connection.status.health || "unknown"}
                          />
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>
      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>Add connection</DialogTitle>
            <DialogDescription>
              Configure an existing network connection for app access.
            </DialogDescription>
          </DialogHeader>
          {adding && (
            <ConnectionForm
              key={currentOrgId}
              onCancel={() => setAdding(false)}
              onSaved={(connection) => {
                setAdding(false);
                navigate(`/connections/${encodeURIComponent(connection.id)}`);
              }}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
