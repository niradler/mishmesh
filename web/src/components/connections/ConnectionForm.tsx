import { useRef, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { DescriptorFields } from "@/components/types/DescriptorFields";
import { TypePicker } from "@/components/types/TypePicker";
import { ErrorState, LoadingState } from "@/components/common/States";
import {
  useCapabilities,
  useConnectionTypes,
  useDetectConnection,
  useSaveConnection,
} from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import {
  fieldDefaults,
  splitConnectionFields,
  validateFields,
} from "@/lib/typed-fields";
import type { TypedConnection } from "@/api/types";

export function ConnectionForm({
  connection,
  onSaved,
  onCancel,
}: {
  connection?: TypedConnection;
  onSaved: (connection: TypedConnection) => void;
  onCancel: () => void;
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const types = useConnectionTypes(currentOrgId);
  const capabilities = useCapabilities(currentOrgId);
  const save = useSaveConnection(currentOrgId);
  const detect = useDetectConnection(currentOrgId);
  const [type, setType] = useState(connection?.type ?? "");
  const [name, setName] = useState(connection?.name ?? "");
  const [values, setValues] = useState<Record<string, unknown>>(
    connection ? structuredClone(connection.config) : {},
  );
  const [problem, setProblem] = useState<string | null>(null);
  const [detectProblem, setDetectProblem] = useState<string | null>(null);
  const [detected, setDetected] = useState(false);
  const identity = `${currentOrgId ?? ""}:${type}`;
  const active = useRef({ identity, revision: 0 });
  if (active.current.identity !== identity) {
    active.current = { identity, revision: active.current.revision + 1 };
  }
  const descriptor = types.data?.find((entry) => entry.type === type);
  const presence: Record<string, boolean> = {};
  for (const field of descriptor?.fields ?? [])
    presence[field.key] = connection?.[`has_${field.key}`] ?? false;
  function submit(event: FormEvent): void {
    event.preventDefault();
    if (!name.trim() || !descriptor) {
      setProblem("Enter a name and choose a connection type.");
      return;
    }
    const error = validateFields(descriptor.fields, values, presence);
    if (error) {
      setProblem(error);
      return;
    }
    const payload = splitConnectionFields(descriptor.fields, values);
    save.mutate(
      { id: connection?.id, input: { type, name: name.trim(), ...payload } },
      { onSuccess: onSaved },
    );
  }
  function detectConfiguration(): void {
    const requested = { ...active.current };
    setDetectProblem(null);
    setDetected(false);
    const input = Object.fromEntries(
      (descriptor?.detect_fields ?? [])
        .filter((field) => values[field.key] !== undefined)
        .map((field) => [field.key, values[field.key]]),
    );
    detect.mutate(
      { type, input },
      {
        onSuccess: (result) => {
          if (
            active.current.identity !== requested.identity ||
            active.current.revision !== requested.revision
          )
            return;
          const detected = Object.fromEntries(
            (descriptor?.fields ?? [])
              .filter(
                (field) =>
                  field.kind !== "secret" && result[field.key] !== undefined,
              )
              .map((field) => [field.key, result[field.key]]),
          );
          setValues((previous) => ({ ...previous, ...detected }));
          setDetected(true);
        },
        onError: (error) => {
          if (
            active.current.identity === requested.identity &&
            active.current.revision === requested.revision
          )
            setDetectProblem(errorMessage(error));
        },
      },
    );
  }
  function changed(): void {
    active.current.revision += 1;
    setDetected(false);
    setDetectProblem(null);
  }
  if (types.isLoading) return <LoadingState label="Loading connection types" />;
  if (types.isError) return <ErrorState error={types.error} />;
  const hostCommands = capabilities.data?.host_commands ?? false;
  return (
    <form className="space-y-4" onSubmit={submit}>
      <div>
        <Label htmlFor="connection-name">Name</Label>
        <Input
          id="connection-name"
          autoComplete="off"
          value={name}
          onChange={(event) => {
            setName(event.target.value);
            setProblem(null);
            save.reset();
          }}
        />
      </div>
      <TypePicker
        types={types.data ?? []}
        value={type}
        disabled={Boolean(connection)}
        onChange={(next) => {
          changed();
          setType(next);
          setValues(
            fieldDefaults(
              types.data?.find((entry) => entry.type === next)?.fields ?? [],
            ),
          );
          detect.reset();
          save.reset();
          setProblem(null);
        }}
      />
      <p className="text-sm text-muted-foreground">{descriptor?.help}</p>
      {descriptor?.detect && (
        <Button
          type="button"
          variant="outline"
          disabled={
            detect.isPending ||
            Boolean(descriptor.detect_host_command && !hostCommands)
          }
          onClick={detectConfiguration}
        >
          {detect.isPending ? "Detecting…" : "Detect"}
        </Button>
      )}
      {descriptor?.detect_host_command && !hostCommands && (
        <p className="text-xs text-muted-foreground">
          Detect requires host command permission.
        </p>
      )}
      {detectProblem && (
        <p role="alert" className="text-sm text-destructive">
          {detectProblem}
        </p>
      )}
      {detected && (
        <p className="text-sm">
          Detected available connection fields. Review them before saving.
        </p>
      )}
      <DescriptorFields
        fields={descriptor?.fields ?? []}
        values={values}
        onChange={(next) => {
          changed();
          setValues(next);
          setProblem(null);
          save.reset();
        }}
        hostCommands={hostCommands}
        secretPresence={presence}
      />
      {(problem || save.error) && (
        <p role="alert" className="text-sm text-destructive">
          {problem ?? errorMessage(save.error)}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={save.isPending || !descriptor}>
          {save.isPending
            ? "Saving…"
            : connection
              ? "Save connection"
              : "Create connection"}
        </Button>
      </div>
    </form>
  );
}
