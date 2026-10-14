import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  useConnections,
  useConnectionTypes,
  useUpdateEndpoint,
} from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import type { AccessRef, Endpoint, EndpointKind } from "@/api/types";

export function AccessRows({
  kind,
  refs,
  onChange,
  disabled = false,
  embedded = false,
}: {
  kind: EndpointKind;
  refs: AccessRef[];
  onChange: (refs: AccessRef[]) => void;
  disabled?: boolean;
  embedded?: boolean;
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const connections = useConnections(currentOrgId);
  const types = useConnectionTypes(currentOrgId);
  const descriptors = new Map(
    (types.data ?? []).map((type) => [type.type, type]),
  );
  const choices = (connections.data ?? []).filter((connection) => {
    const descriptor = descriptors.get(connection.type);
    return (
      descriptor?.role === "access" && descriptor.protocols?.includes(kind)
    );
  });
  const available = choices.filter(
    (connection) => !refs.some((ref) => ref.connection_id === connection.id),
  );
  function update(index: number, patch: Partial<AccessRef>): void {
    onChange(
      refs.map((ref, position) =>
        position === index ? { ...ref, ...patch } : ref,
      ),
    );
  }
  return (
    <fieldset className="space-y-3">
      <legend className={embedded ? "sr-only" : "text-sm font-medium"}>
        Access
      </legend>
      <p className="text-xs text-muted-foreground">
        Choose Public or Private connections for this app. Leave Hostname empty
        to use the connection’s default.
      </p>
      {connections.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorMessage(connections.error)}
        </p>
      )}
      {types.error && (
        <p role="alert" className="text-sm text-destructive">
          {errorMessage(types.error)}
        </p>
      )}
      {refs.map((ref, index) => {
        const connection = connections.data?.find(
          (row) => row.id === ref.connection_id,
        );
        const descriptor = connection
          ? descriptors.get(connection.type)
          : undefined;
        const supported =
          descriptor?.role === "access" && descriptor.protocols?.includes(kind);
        const options = choices.filter(
          (row) =>
            row.id === ref.connection_id ||
            !refs.some((other) => other.connection_id === row.id),
        );
        return (
          <div key={index} className="space-y-2 rounded-md border p-3">
            <div>
              <Label htmlFor={`access-connection-${index}`}>
                Connection {index + 1}
              </Label>
              <select
                id={`access-connection-${index}`}
                className="h-10 w-full rounded-md border bg-background px-3"
                disabled={disabled}
                value={ref.connection_id}
                onChange={(event) =>
                  update(index, {
                    connection_id: event.target.value,
                    hostname: "",
                  })
                }
              >
                {!options.some((row) => row.id === ref.connection_id) && (
                  <option value={ref.connection_id}>
                    {connection?.name ?? ref.connection_id}
                  </option>
                )}
                {options.map((row) => (
                  <option key={row.id} value={row.id}>
                    {row.name}
                  </option>
                ))}
              </select>
            </div>
            <p className="text-xs">
              {descriptor?.scope === "public"
                ? "Public"
                : descriptor?.scope === "private"
                  ? "Private"
                  : "Unknown scope"}
              {connection && (
                <>
                  {" "}
                  ·{" "}
                  <Link
                    className="underline"
                    to={`/connections/${encodeURIComponent(connection.id)}`}
                  >
                    Connection details
                  </Link>
                </>
              )}
            </p>
            <div>
              <Label htmlFor={`access-hostname-${index}`}>
                Hostname {index + 1}
              </Label>
              <Input
                id={`access-hostname-${index}`}
                value={ref.hostname}
                disabled={disabled}
                placeholder="Default from endpoint domain"
                onChange={(event) =>
                  update(index, { hostname: event.target.value })
                }
              />
            </div>
            {!supported && !connections.isLoading && !types.isLoading && (
              <p role="alert" className="text-sm text-destructive">
                This connection does not support {kind.toUpperCase()} app
                access. Remove this ref or choose a compatible connection before
                saving. Its values have been retained.
              </p>
            )}
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={disabled}
              onClick={() =>
                onChange(refs.filter((_, position) => position !== index))
              }
            >
              Remove access {index + 1}
            </Button>
          </div>
        );
      })}
      <Button
        type="button"
        variant="outline"
        disabled={disabled || available.length === 0}
        onClick={() =>
          onChange([...refs, { connection_id: available[0].id, hostname: "" }])
        }
      >
        Add access
      </Button>
      {available.length === 0 && (
        <p className="text-xs text-muted-foreground">
          No additional compatible access connections.{" "}
          <Link className="underline" to="/connections">
            Manage connections
          </Link>
          .
        </p>
      )}
    </fieldset>
  );
}

export function AccessSection({
  endpoint,
  canWrite,
}: {
  endpoint: Endpoint;
  canWrite: boolean;
}): React.JSX.Element {
  const update = useUpdateEndpoint();
  const [refs, setRefs] = useState(endpoint.policy?.access ?? []);
  useEffect(
    () => setRefs(endpoint.policy?.access ?? []),
    [endpoint.policy?.access],
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle>Access</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="space-y-3"
          onSubmit={(event) => {
            event.preventDefault();
            update.mutate({
              id: endpoint.id,
              policy: { ...endpoint.policy, access: refs },
            });
          }}
        >
          <AccessRows
            embedded
            kind={endpoint.kind}
            refs={refs}
            onChange={setRefs}
            disabled={!canWrite || update.isPending}
          />
          {update.error && (
            <p role="alert" className="text-sm text-destructive">
              {errorMessage(update.error)}
            </p>
          )}
          {canWrite && (
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? "Saving…" : "Save access"}
            </Button>
          )}
        </form>
      </CardContent>
    </Card>
  );
}
