import { useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { KeyValueEditor, ListEditor } from "@/components/common/KeyValueEditor";
import { LabelWithHelp } from "@/components/common/HelpTip";
import { FormError } from "@/components/common/ConfirmDialog";
import { useCapabilities, useCreateService, useUpdateService } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { DRIVER_HELP, DRIVER_LABELS, HELP } from "@/lib/glossary";
import {
  IDLE_PRESETS,
  emptyServiceForm,
  serviceFormFromService,
  serviceInputFromForm,
  validateServiceForm,
  type DockerSelector,
  type IdleUnit,
  type ServiceFormError,
  type ServiceFormState,
} from "@/lib/services";
import type { Service, ServiceDriver } from "@/api/types";

function FieldError({ error, field }: { error: ServiceFormError | null; field: ServiceFormError["field"] }) {
  if (error?.field !== field) return null;
  return (
    <p className="text-sm text-destructive" role="alert">
      {error.message}
    </p>
  );
}

function DockerFields({ form, set }: { form: ServiceFormState; set: SetField }) {
  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <LabelWithHelp entry={HELP.dockerSelector}>Pick containers by</LabelWithHelp>
        <Select value={form.dockerSelector} onValueChange={(v) => set("dockerSelector", v as DockerSelector)}>
          <SelectTrigger>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="containers">Container names</SelectItem>
            <SelectItem value="label">Label</SelectItem>
          </SelectContent>
        </Select>
      </div>
      {form.dockerSelector === "containers" ? (
        <div className="space-y-1.5">
          <Label>Containers</Label>
          <ListEditor values={form.containers} onChange={(v) => set("containers", v)} placeholder="portainer" />
        </div>
      ) : (
        <div className="space-y-1.5">
          <Label htmlFor="service-label">Label</Label>
          <Input
            id="service-label"
            value={form.label}
            onChange={(e) => set("label", e.target.value)}
            placeholder="mishmesh.service=portainer"
            className="font-mono text-xs"
          />
        </div>
      )}
    </div>
  );
}

function ComposeFields({ form, set, hostCommands }: { form: ServiceFormState; set: SetField; hostCommands: boolean }) {
  return (
    <div className="space-y-3">
      <div className="space-y-1.5">
        <Label htmlFor="compose-project">Project</Label>
        <Input
          id="compose-project"
          value={form.composeProject}
          onChange={(e) => set("composeProject", e.target.value)}
          placeholder="media"
        />
      </div>
      <div className="space-y-1.5">
        <Label>Services</Label>
        <ListEditor values={form.composeServices} onChange={(v) => set("composeServices", v)} placeholder="all services when empty" />
      </div>
      {(hostCommands || form.composeFile) && (
        <div className="space-y-1.5">
          <Label htmlFor="compose-file">Compose file</Label>
          <Input
            id="compose-file"
            value={form.composeFile}
            onChange={(e) => set("composeFile", e.target.value)}
            placeholder="/srv/media/compose.yaml"
            className="font-mono text-xs"
            disabled={!hostCommands}
          />
          <p className="text-xs text-muted-foreground">
            {hostCommands
              ? "Optional. With a file, mishmesh runs docker compose on the host."
              : "Host commands are not allowed for you, so this file cannot be changed."}
          </p>
        </div>
      )}
    </div>
  );
}

function ExecFields({ form, set }: { form: ServiceFormState; set: SetField }) {
  const argvField = (key: "execStart" | "execStop" | "execStatus", label: string, placeholder: string) => (
    <div className="space-y-1.5">
      <LabelWithHelp htmlFor={key} entry={HELP.execArgv}>
        {label}
      </LabelWithHelp>
      <Textarea
        id={key}
        value={form[key]}
        onChange={(e) => set(key, e.target.value)}
        placeholder={placeholder}
        className="min-h-[72px] font-mono text-xs"
      />
    </div>
  );
  return (
    <div className="space-y-3">
      {argvField("execStart", "Start command", "systemctl\nstart\nmyapp")}
      {argvField("execStop", "Stop command", "systemctl\nstop\nmyapp")}
      {argvField("execStatus", "Status command (optional)", "systemctl\nis-active\nmyapp")}
      <div className="space-y-1.5">
        <Label htmlFor="exec-dir">Working directory</Label>
        <Input id="exec-dir" value={form.execDir} onChange={(e) => set("execDir", e.target.value)} placeholder="optional" className="font-mono text-xs" />
      </div>
      <div className="space-y-1.5">
        <Label>Environment</Label>
        <KeyValueEditor pairs={form.execEnv} onChange={(v) => set("execEnv", v)} keyPlaceholder="NAME" valuePlaceholder="value" />
      </div>
    </div>
  );
}

type SetField = <K extends keyof ServiceFormState>(key: K, value: ServiceFormState[K]) => void;

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
}) {
  const { currentOrgId } = useSession();
  const capabilities = useCapabilities(currentOrgId);
  const hostCommands = capabilities.data?.host_commands ?? false;
  const create = useCreateService();
  const update = useUpdateService();
  const [form, setForm] = useState<ServiceFormState>(() => (service ? serviceFormFromService(service) : emptyServiceForm()));
  const [error, setError] = useState<ServiceFormError | null>(null);
  const mutation = service ? update : create;

  const set: SetField = (key, value) => {
    setForm((current) => ({ ...current, [key]: value }));
    setError(null);
  };

  const drivers: ServiceDriver[] = hostCommands || form.driver === "exec" ? ["docker", "compose", "exec"] : ["docker", "compose"];

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    event.stopPropagation();
    const problem = validateServiceForm(form, hostCommands);
    if (problem) {
      setError(problem);
      return;
    }
    const input = serviceInputFromForm(form);
    const handlers = { onSuccess: (saved: Service) => onSaved(saved) };
    if (service) update.mutate({ id: service.id, input }, handlers);
    else create.mutate(input, handlers);
  };

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label htmlFor="service-name">Name</Label>
          <Input
            id="service-name"
            value={form.name}
            onChange={(e) => set("name", e.target.value)}
            placeholder="portainer"
            autoComplete="off"
          />
          <FieldError error={error} field="name" />
        </div>
        <div className="space-y-1.5">
          <LabelWithHelp entry={DRIVER_HELP[form.driver]}>Driver</LabelWithHelp>
          <Select value={form.driver} onValueChange={(v) => set("driver", v as ServiceDriver)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {drivers.map((driver) => (
                <SelectItem key={driver} value={driver} disabled={driver === "exec" && !hostCommands}>
                  {DRIVER_LABELS[driver]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <div className="rounded-md border border-border p-3">
        {form.driver === "docker" && <DockerFields form={form} set={set} />}
        {form.driver === "compose" && <ComposeFields form={form} set={set} hostCommands={hostCommands} />}
        {form.driver === "exec" &&
          (hostCommands ? (
            <ExecFields form={form} set={set} />
          ) : (
            <p className="text-sm text-muted-foreground">
              Host commands are disabled on this server (MISHMESH_HOST_COMMANDS) or your role lacks host:exec, so this
              service cannot be edited here.
            </p>
          ))}
        <div className="mt-3">
          <FieldError error={error} field="driver" />
        </div>
      </div>

      <div className="space-y-1.5">
        <LabelWithHelp htmlFor="ready-tcp" entry={HELP.readyTcp}>
          Readiness address
        </LabelWithHelp>
        <Input
          id="ready-tcp"
          value={form.readyTcp}
          onChange={(e) => set("readyTcp", e.target.value)}
          placeholder="127.0.0.1:9000"
          className="font-mono text-xs"
        />
        <FieldError error={error} field="readyTcp" />
      </div>

      <div className="grid gap-3 sm:grid-cols-2">
        <div className="space-y-1.5">
          <LabelWithHelp entry={HELP.idleTimeout}>Idle timeout</LabelWithHelp>
          <Select value={form.idlePreset} onValueChange={(v) => set("idlePreset", v)}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {IDLE_PRESETS.map((preset) => (
                <SelectItem key={preset.value} value={preset.value}>
                  {preset.label}
                </SelectItem>
              ))}
              <SelectItem value="custom">Custom</SelectItem>
            </SelectContent>
          </Select>
          {form.idlePreset === "custom" && (
            <div className="flex gap-2">
              <Input
                type="number"
                min={0}
                value={form.idleCustom}
                onChange={(e) => set("idleCustom", e.target.value)}
                aria-label="Custom idle timeout"
              />
              <Select value={form.idleUnit} onValueChange={(v) => set("idleUnit", v as IdleUnit)}>
                <SelectTrigger className="w-32">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="minutes">minutes</SelectItem>
                  <SelectItem value="seconds">seconds</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}
          <FieldError error={error} field="idle" />
        </div>
        <div className="space-y-1.5">
          <LabelWithHelp htmlFor="start-timeout" entry={HELP.startTimeout}>
            Start timeout (seconds)
          </LabelWithHelp>
          <Input
            id="start-timeout"
            type="number"
            min={0}
            value={form.startTimeout}
            onChange={(e) => set("startTimeout", e.target.value)}
          />
          <FieldError error={error} field="startTimeout" />
        </div>
      </div>

      <FormError message={mutation.error ? errorMessage(mutation.error) : undefined} />

      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={mutation.isPending || (form.driver === "exec" && !hostCommands)}>
          {mutation.isPending ? "Saving…" : (submitLabel ?? (service ? "Save service" : "Create service"))}
        </Button>
      </div>
    </form>
  );
}
