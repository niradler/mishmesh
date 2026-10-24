import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { PageHeader } from "@/components/common/PageHeader";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { ErrorState, LoadingState } from "@/components/common/States";
import { ConnectionForm } from "@/components/connections/ConnectionForm";
import { HostnamesTable } from "@/components/cloudflare/HostnamesTable";
import { connectionScope } from "@/lib/connections";
import { ConnectionPanel } from "@/components/connections/ConnectionPanels";
import { DescriptorDetails } from "@/components/types/DescriptorDetails";
import { StateBadge } from "@/components/common/StateBadge";
import {
  useConnection,
  useConnectionTypes,
  useDeleteConnection,
  useSyncConnection,
} from "@/api/hooks";
import { ApiError, errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import type { TypedConnection } from "@/api/types";

interface ReferenceConflict {
  references?: { kind: string; id: string; name: string }[];
}

function hostnameManagement(
  row: TypedConnection,
): "enabled" | "needs-token" | undefined {
  if (row.type !== "cloudflare") return undefined;
  return row.has_api_token ? "enabled" : "needs-token";
}

export function ConnectionDetail(): React.JSX.Element | null {
  const { id = "" } = useParams();
  const { currentOrgId, canWrite } = useSession();
  const connection = useConnection(id, currentOrgId);
  const types = useConnectionTypes(currentOrgId);
  const sync = useSyncConnection(currentOrgId);
  const remove = useDeleteConnection(currentOrgId);
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const [deleting, setDeleting] = useState(false);
  if (connection.isLoading) return <LoadingState label="Loading connection" />;
  if (connection.isError) return <ErrorState error={connection.error} />;
  if (!connection.data) return null;
  const row = connection.data;
  const descriptor = types.data?.find((type) => type.type === row.type);
  const secretPresence = Object.fromEntries(
    (descriptor?.fields ?? [])
      .filter((field) => field.kind === "secret")
      .map((field) => [field.key, row[`has_${field.key}`] ?? false]),
  );
  const conflict =
    remove.error instanceof ApiError && remove.error.status === 409
      ? (remove.error.body as ReferenceConflict)
      : undefined;
  return (
    <div className="space-y-4">
      <Link className="text-sm underline" to="/connections">
        Connections
      </Link>
      <PageHeader
        title={row.name}
        description={`${descriptor?.label ?? row.type} · ${descriptor?.role ?? "Unknown"} · ${connectionScope(descriptor)}`}
        actions={
          canWrite ? (
            <>
              <Button
                variant="outline"
                disabled={sync.isPending}
                onClick={() => sync.mutate(id)}
              >
                {sync.isPending ? "Syncing…" : "Sync now"}
              </Button>
              <Button variant="outline" onClick={() => setEditing(true)}>
                Edit connection
              </Button>
              <Button
                variant="outline"
                onClick={() => {
                  remove.reset();
                  setDeleting(true);
                }}
              >
                Delete connection
              </Button>
            </>
          ) : undefined
        }
      />
      <div className="flex flex-wrap gap-4 text-sm">
        <span>Level: {row.status.level || "Unknown"}</span>
        <span className="inline-flex items-center gap-2">
          Health <StateBadge state={row.status.health || "unknown"} />
        </span>
        <span>
          Last sync:{" "}
          {row.status.last_sync
            ? new Date(row.status.last_sync).toLocaleString()
            : "Never"}
        </span>
      </div>
      {(row.status.last_error || sync.error) && (
        <p
          role="alert"
          className="rounded-md border p-3 text-sm text-destructive"
        >
          {sync.error ? errorMessage(sync.error) : row.status.last_error}
        </p>
      )}
      <HostnamesTable
        hostnames={row.status.hostnames ?? []}
        connectionId={row.id}
        canWrite={canWrite}
        manage={hostnameManagement(row)}
      />
      <Card>
        <CardHeader>
          <CardTitle>Configuration</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <DescriptorDetails
            fields={descriptor?.fields ?? []}
            values={row.config}
            secretPresence={secretPresence}
          />
          <ConnectionPanel connection={row} />
        </CardContent>
      </Card>
      <Dialog open={editing} onOpenChange={setEditing}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>Edit {row.name}</DialogTitle>
            <DialogDescription>
              Secret fields are unchanged until replaced or explicitly cleared.
            </DialogDescription>
          </DialogHeader>
          {editing && (
            <ConnectionForm
              key={`${currentOrgId}:${id}`}
              connection={row}
              onCancel={() => setEditing(false)}
              onSaved={() => setEditing(false)}
            />
          )}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title="Delete connection?"
        description="Remove this connection and its saved credentials. Referenced connections cannot be deleted. Review provider-owned resources and stop using this connection first."
        pending={remove.isPending}
        error={
          remove.error ? (
            <div>
              {errorMessage(remove.error)}
              {conflict?.references?.map((reference) => (
                <Link
                  className="block underline"
                  key={`${reference.kind}:${reference.id}`}
                  to={`/${reference.kind}s/${encodeURIComponent(reference.id)}`}
                >
                  {reference.name}
                </Link>
              ))}
            </div>
          ) : undefined
        }
        onConfirm={() =>
          remove.mutate(id, { onSuccess: () => navigate("/connections") })
        }
      />
    </div>
  );
}
