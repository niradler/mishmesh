import { fieldDefaults, validateFields } from "@/lib/typed-fields";
import type {
  Readiness,
  Service,
  ServiceInput,
  TypeDescriptor,
} from "@/api/types";

export const SERVICE_NAME_PATTERN = /^[a-z0-9][a-z0-9-_.]{0,62}$/;
export const DEFAULT_START_TIMEOUT_SECONDS = 120;
export const IDLE_PRESETS = [
  { value: "-1", label: "When no dependent service needs it" },
  { value: "0", label: "Never stop automatically" },
  { value: "300", label: "After 5 minutes idle" },
  { value: "1800", label: "After 30 minutes idle" },
  { value: "7200", label: "After 2 hours idle" },
] as const;

export interface ServiceFormState {
  name: string;
  driver: string;
  config: Record<string, unknown>;
  readyMode: "driver" | "tcp" | "http";
  readyTcp: string;
  readyUrl: string;
  readyStatus: number;
  requires: string[];
  idleSeconds: number;
  startTimeout: number;
}

export function emptyServiceForm(
  descriptor?: TypeDescriptor,
): ServiceFormState {
  return {
    name: "",
    driver: descriptor?.type ?? "",
    config: fieldDefaults(descriptor?.fields ?? []),
    readyMode: "driver",
    readyTcp: "",
    readyUrl: "",
    readyStatus: 200,
    requires: [],
    idleSeconds: 1800,
    startTimeout: 120,
  };
}

export function serviceFormFromService(service: Service): ServiceFormState {
  let readyMode: ServiceFormState["readyMode"] = "driver";
  if (service.ready.tcp) readyMode = "tcp";
  else if (service.ready.http) readyMode = "http";
  return {
    name: service.name,
    driver: service.driver,
    config: structuredClone(service.config),
    readyMode,
    readyTcp: service.ready.tcp ?? "",
    readyUrl: service.ready.http?.url ?? "",
    readyStatus: service.ready.http?.status ?? 200,
    requires: [...service.requires],
    idleSeconds: service.idle_timeout_seconds,
    startTimeout: service.start_timeout_seconds,
  };
}

export function serviceInputFromForm(form: ServiceFormState): ServiceInput {
  let ready: Readiness = {};
  if (form.readyMode === "tcp") ready = { tcp: form.readyTcp };
  else if (form.readyMode === "http")
    ready = { http: { url: form.readyUrl, status: form.readyStatus } };
  return {
    name: form.name.trim(),
    driver: form.driver,
    config: form.config,
    ready,
    requires: form.requires,
    idle_timeout_seconds: form.idleSeconds,
    start_timeout_seconds: form.startTimeout,
  };
}

export function validateServiceForm(
  form: ServiceFormState,
  descriptor?: TypeDescriptor,
): string | null {
  if (!SERVICE_NAME_PATTERN.test(form.name.trim()))
    return "Name must contain 1–63 lowercase letters, digits, hyphens, underscores or periods.";
  if (!descriptor) return "Choose a driver.";
  const fieldError = validateFields(descriptor.fields, form.config);
  if (fieldError) return fieldError;
  if (
    !Number.isInteger(form.idleSeconds) ||
    form.idleSeconds < -1 ||
    form.idleSeconds > 604800
  )
    return "Choose an idle duration from 1 second to 7 days in whole seconds, or select a stopping mode.";
  if (
    !Number.isInteger(form.startTimeout) ||
    form.startTimeout < 0 ||
    form.startTimeout > 604800
  )
    return "Start timeout must be zero or a positive number of seconds up to seven days.";
  if (form.readyMode === "tcp" && !form.readyTcp.trim())
    return "Enter a TCP readiness address.";
  if (
    form.readyMode === "http" &&
    (!form.readyUrl.trim() ||
      !Number.isInteger(form.readyStatus) ||
      form.readyStatus < 100 ||
      form.readyStatus > 599)
  )
    return "Enter an HTTP readiness URL and status between 100 and 599.";
  return null;
}

export function runtimeConfigChanged(
  service: Service,
  input: ServiceInput,
): boolean {
  return (
    service.driver !== input.driver ||
    JSON.stringify(service.config) !== JSON.stringify(input.config) ||
    JSON.stringify(service.ready) !== JSON.stringify(input.ready) ||
    service.start_timeout_seconds !== input.start_timeout_seconds
  );
}

export function serviceSelectorSummary(
  service: Service,
  descriptor?: TypeDescriptor,
): string {
  if (!descriptor) return service.driver;
  return descriptor.fields
    .filter(
      (field) =>
        !field.sensitive &&
        field.kind !== "secret" &&
        service.config[field.key] !== undefined,
    )
    .map((field) => {
      const value = service.config[field.key];
      return `${field.label}: ${Array.isArray(value) ? value.join(", ") : String(value)}`;
    })
    .join(" · ");
}
