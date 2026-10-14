import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { DescriptorFields } from "@/components/types/DescriptorFields";
import { TypePicker } from "@/components/types/TypePicker";
import { ErrorState, LoadingState } from "@/components/common/States";
import {
  useCapabilities,
  useConnections,
  useCreateService,
  useServiceDrivers,
  useServices,
  useUpdateService,
} from "@/api/hooks";
import { ApiError, errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { fieldDefaults } from "@/lib/typed-fields";
import {
  IDLE_PRESETS,
  emptyServiceForm,
  runtimeConfigChanged,
  serviceFormFromService,
  serviceInputFromForm,
  validateServiceForm,
  type ServiceFormState,
} from "@/lib/services";
import type { Service, ServiceDeleteConflict } from "@/api/types";

export function ServiceForm({
  service,
  onSaved,
  onCancel,
  submitLabel,
}: {
  service?: Service;
  onSaved: (service: Service) => void;
  onCancel: () => void;
  submitLabel?: string;
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const drivers = useServiceDrivers(currentOrgId);
  const capabilities = useCapabilities(currentOrgId);
  const connections = useConnections(currentOrgId);
  const services = useServices(currentOrgId);
  const create = useCreateService();
  const update = useUpdateService();
  const [form, setForm] = useState<ServiceFormState>(() =>
    service ? serviceFormFromService(service) : emptyServiceForm(),
  );
  const [error, setError] = useState<string | null>(null);
  const mutation = service ? update : create;
  const descriptor = drivers.data?.find(
    (driver) => driver.type === form.driver,
  );
  const hostCommands = capabilities.data?.host_commands ?? false;
  const driverChoices = (drivers.data ?? []).filter(
    (driver) =>
      hostCommands ||
      driver.type === form.driver ||
      !driver.fields.some((field) => field.host_command && field.required),
  );
  useEffect(() => {
    const initial = drivers.data?.find(
      (driver) =>
        hostCommands ||
        !driver.fields.some((field) => field.host_command && field.required),
    );
    if (!form.driver && initial)
      setForm((previous) => ({
        ...previous,
        driver: initial.type,
        config: fieldDefaults(initial.fields),
      }));
  }, [drivers.data, form.driver, hostCommands]);
  function set<K extends keyof ServiceFormState>(
    key: K,
    value: ServiceFormState[K],
  ): void {
    setForm((previous) => ({ ...previous, [key]: value }));
    setError(null);
    mutation.reset();
  }
  function submit(restart: boolean): void {
    const problem = validateServiceForm(form, descriptor);
    if (problem) {
      setError(problem);
      return;
    }
    const input = serviceInputFromForm(form);
    const options = { onSuccess: onSaved };
    if (service) update.mutate({ id: service.id, input, restart }, options);
    else create.mutate(input, options);
  }
  function onSubmit(event: FormEvent): void {
    event.preventDefault();
    submit(false);
  }
  if (drivers.isLoading) return <LoadingState label="Loading drivers" />;
  if (drivers.isError) return <ErrorState error={drivers.error} />;
  const input = serviceInputFromForm(form);
  const restartNeeded = Boolean(
    service &&
    ["running", "starting", "partial"].includes(service.status.state) &&
    runtimeConfigChanged(service, input),
  );
  const failure =
    mutation.error instanceof ApiError
      ? (mutation.error.body as ServiceDeleteConflict)
      : undefined;
  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="space-y-1.5">
        <Label htmlFor="service-name">Name</Label>
        <Input
          id="service-name"
          value={form.name}
          onChange={(event) => set("name", event.target.value)}
          autoComplete="off"
        />
      </div>
      <TypePicker
        types={driverChoices}
        value={form.driver}
        onChange={(type) => {
          const driver = drivers.data?.find((entry) => entry.type === type);
          setForm((previous) => ({
            ...previous,
            driver: type,
            config: fieldDefaults(driver?.fields ?? []),
          }));
          mutation.reset();
        }}
      />
      <p className="text-xs text-muted-foreground">{descriptor?.help}</p>
      <DescriptorFields
        fields={descriptor?.fields ?? []}
        values={form.config}
        onChange={(values) => set("config", values)}
        connections={connections.data ?? []}
        hostCommands={hostCommands}
      />
      <fieldset className="space-y-3 rounded-md border p-3">
        <legend className="px-1 text-sm font-medium">Readiness</legend>
        <Label htmlFor="ready-mode">Ready when</Label>
        <select
          id="ready-mode"
          className="h-10 w-full rounded-md border bg-background px-3"
          value={form.readyMode}
          onChange={(event) =>
            set(
              "readyMode",
              event.target.value as ServiceFormState["readyMode"],
            )
          }
        >
          <option value="driver">Driver reports running</option>
          <option value="tcp">TCP connection succeeds</option>
          <option value="http">HTTP response matches</option>
        </select>
        {form.readyMode === "tcp" && (
          <div>
            <Label htmlFor="ready-tcp">TCP address</Label>
            <Input
              id="ready-tcp"
              value={form.readyTcp}
              onChange={(event) => set("readyTcp", event.target.value)}
              placeholder="127.0.0.1:8080"
            />
          </div>
        )}
        {form.readyMode === "http" && (
          <div className="space-y-2">
            <Label htmlFor="ready-url">HTTP URL</Label>
            <Input
              id="ready-url"
              value={form.readyUrl}
              onChange={(event) => set("readyUrl", event.target.value)}
            />
            <Label htmlFor="ready-status">Expected HTTP status</Label>
            <Input
              id="ready-status"
              type="number"
              value={form.readyStatus}
              onChange={(event) =>
                set("readyStatus", Number(event.target.value))
              }
            />
          </div>
        )}
      </fieldset>
      <fieldset className="space-y-2 rounded-md border p-3">
        <legend className="px-1 text-sm font-medium">Requires</legend>
        <p className="text-xs text-muted-foreground">
          Required services start first and stay running while this service uses
          them.
        </p>
        {(services.data ?? [])
          .filter((candidate) => candidate.id !== service?.id)
          .map((candidate) => (
            <label
              key={candidate.id}
              className="flex items-center gap-2 text-sm"
            >
              <input
                type="checkbox"
                checked={form.requires.includes(candidate.id)}
                onChange={(event) =>
                  set(
                    "requires",
                    event.target.checked
                      ? [...form.requires, candidate.id]
                      : form.requires.filter((id) => id !== candidate.id),
                  )
                }
              />
              {candidate.name}
            </label>
          ))}
      </fieldset>
      <div className="space-y-2">
        <Label htmlFor="idle-preset">Idle behavior</Label>
        <select
          id="idle-preset"
          className="h-10 w-full rounded-md border bg-background px-3"
          value={
            IDLE_PRESETS.some(
              (preset) => Number(preset.value) === form.idleSeconds,
            )
              ? String(form.idleSeconds)
              : "custom"
          }
          onChange={(event) => {
            if (event.target.value === "custom") set("idleSeconds", 60);
            else set("idleSeconds", Number(event.target.value));
          }}
        >
          {IDLE_PRESETS.map((preset) => (
            <option key={preset.value} value={preset.value}>
              {preset.label}
            </option>
          ))}
          <option value="custom">Custom seconds</option>
        </select>
        <Label htmlFor="idle-seconds">Idle timeout seconds</Label>
        <Input
          id="idle-seconds"
          type="number"
          value={form.idleSeconds}
          onChange={(event) => set("idleSeconds", Number(event.target.value))}
        />
      </div>
      <div>
        <Label htmlFor="start-timeout">Start timeout seconds</Label>
        <Input
          id="start-timeout"
          type="number"
          value={form.startTimeout}
          onChange={(event) => set("startTimeout", Number(event.target.value))}
        />
      </div>
      {restartNeeded && (
        <p className="rounded-md border p-3 text-sm">
          These startup changes need a restart. Save and restart briefly
          interrupts this service. Running dependents must be stopped separately
          first.
        </p>
      )}
      {(error || mutation.error) && (
        <div className="space-y-2 text-sm text-destructive" role="alert">
          <p>{error ?? errorMessage(mutation.error)}</p>
          {failure?.phase && (
            <p>
              {failure.saved
                ? "The new configuration is saved."
                : "The saved configuration is unchanged."}{" "}
              Failed phase: {failure.phase}.
            </p>
          )}
          {failure?.dependents?.map((dependent) => (
            <Link
              className="block underline"
              key={dependent.id}
              to={`/services/${encodeURIComponent(dependent.id)}`}
            >
              {dependent.name} ({dependent.state})
            </Link>
          ))}
        </div>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending
            ? "Saving…"
            : (submitLabel ?? (service ? "Save" : "Create service"))}
        </Button>
        {restartNeeded && (
          <Button
            type="button"
            disabled={mutation.isPending}
            onClick={() => submit(true)}
          >
            Save and restart
          </Button>
        )}
      </div>
    </form>
  );
}
