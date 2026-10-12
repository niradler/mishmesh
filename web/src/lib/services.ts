import { pairsToRecord, recordToPairs, type KVPair } from "@/components/common/KeyValueEditor";
import type { Service, ServiceDriver, ServiceInput, ServiceSpec } from "@/api/types";

export const SERVICE_NAME_PATTERN = /^[a-z0-9][a-z0-9-_.]{0,62}$/;
const CONTAINER_NAME_PATTERN = /^\/?[a-zA-Z0-9][a-zA-Z0-9_.-]*$/;
const LABEL_PATTERN = /^[^=\s]+=.*$/;
const MAX_TIMEOUT_SECONDS = 7 * 24 * 60 * 60;
export const DEFAULT_START_TIMEOUT_SECONDS = 120;

export const IDLE_PRESETS = [
  { value: "0", label: "Never" },
  { value: "300", label: "5 minutes" },
  { value: "1800", label: "30 minutes" },
  { value: "7200", label: "2 hours" },
] as const;

export type IdleUnit = "seconds" | "minutes";
export type DockerSelector = "containers" | "label";

export interface ServiceFormState {
  name: string;
  driver: ServiceDriver;
  dockerSelector: DockerSelector;
  containers: string[];
  label: string;
  composeProject: string;
  composeServices: string[];
  composeFile: string;
  composeBin: string;
  execStart: string;
  execStop: string;
  execStatus: string;
  execDir: string;
  execEnv: KVPair[];
  readyTcp: string;
  idlePreset: string;
  idleCustom: string;
  idleUnit: IdleUnit;
  startTimeout: string;
}

function argvToText(argv?: string[]): string {
  return (argv ?? []).join("\n");
}

function textToArgv(text: string): string[] {
  return text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}

function isPreset(seconds: number): boolean {
  return IDLE_PRESETS.some((preset) => preset.value === String(seconds));
}

export function emptyServiceForm(): ServiceFormState {
  return {
    name: "",
    driver: "docker",
    dockerSelector: "containers",
    containers: [""],
    label: "",
    composeProject: "",
    composeServices: [],
    composeFile: "",
    composeBin: "",
    execStart: "",
    execStop: "",
    execStatus: "",
    execDir: "",
    execEnv: [],
    readyTcp: "",
    idlePreset: "1800",
    idleCustom: "",
    idleUnit: "minutes",
    startTimeout: String(DEFAULT_START_TIMEOUT_SECONDS),
  };
}

export function serviceFormFromService(service: Service): ServiceFormState {
  const base = emptyServiceForm();
  const { spec } = service;
  const idle = service.idle_timeout_seconds;
  const customMinutes = idle % 60 === 0;
  return {
    ...base,
    name: service.name,
    driver: service.driver,
    dockerSelector: spec.docker?.label && !(spec.docker.containers?.length ?? 0) ? "label" : "containers",
    containers: spec.docker?.containers?.length ? spec.docker.containers : [""],
    label: spec.docker?.label ?? "",
    composeProject: spec.compose?.project ?? "",
    composeServices: spec.compose?.services ?? [],
    composeFile: spec.compose?.file ?? "",
    composeBin: spec.compose?.bin ?? "",
    execStart: argvToText(spec.exec?.start),
    execStop: argvToText(spec.exec?.stop),
    execStatus: argvToText(spec.exec?.status),
    execDir: spec.exec?.dir ?? "",
    execEnv: recordToPairs(spec.exec?.env),
    readyTcp: spec.ready_tcp ?? "",
    idlePreset: isPreset(idle) ? String(idle) : "custom",
    idleCustom: isPreset(idle) ? "" : String(customMinutes ? idle / 60 : idle),
    idleUnit: customMinutes ? "minutes" : "seconds",
    startTimeout: String(service.start_timeout_seconds || DEFAULT_START_TIMEOUT_SECONDS),
  };
}

export function idleSeconds(form: ServiceFormState): number {
  if (form.idlePreset !== "custom") return Number(form.idlePreset);
  const value = Number(form.idleCustom);
  return form.idleUnit === "minutes" ? Math.round(value * 60) : Math.round(value);
}

function buildSpec(form: ServiceFormState): ServiceSpec {
  const spec: ServiceSpec = { ready_tcp: form.readyTcp.trim() || undefined };
  if (form.driver === "docker") {
    const containers = form.containers.map((c) => c.trim()).filter(Boolean);
    spec.docker =
      form.dockerSelector === "label" ? { label: form.label.trim() } : { containers };
  } else if (form.driver === "compose") {
    const file = form.composeFile.trim();
    const services = form.composeServices.map((s) => s.trim()).filter(Boolean);
    spec.compose = {
      project: form.composeProject.trim(),
      services: services.length > 0 ? services : undefined,
      file: file || undefined,
      bin: file && form.composeBin ? form.composeBin : undefined,
    };
  } else {
    const env = pairsToRecord(form.execEnv);
    const status = textToArgv(form.execStatus);
    spec.exec = {
      start: textToArgv(form.execStart),
      stop: textToArgv(form.execStop),
      status: status.length > 0 ? status : undefined,
      dir: form.execDir.trim() || undefined,
      env: env && Object.keys(env).length > 0 ? env : undefined,
    };
  }
  return spec;
}

export function serviceInputFromForm(form: ServiceFormState): ServiceInput {
  return {
    name: form.name.trim(),
    driver: form.driver,
    spec: buildSpec(form),
    idle_timeout_seconds: idleSeconds(form),
    start_timeout_seconds: Number(form.startTimeout) || DEFAULT_START_TIMEOUT_SECONDS,
  };
}

export type ServiceFormField = "name" | "driver" | "readyTcp" | "idle" | "startTimeout";

export interface ServiceFormError {
  field: ServiceFormField;
  message: string;
}

function isHostPort(value: string): boolean {
  const match = /^(\[[^\]]+\]|[^:\s]+):(\d{1,5})$/.exec(value);
  return !!match && Number(match[2]) > 0 && Number(match[2]) <= 65535;
}

function validTimeout(value: number): boolean {
  return Number.isFinite(value) && value >= 0 && value <= MAX_TIMEOUT_SECONDS;
}

export function validateServiceForm(form: ServiceFormState, hostCommands: boolean): ServiceFormError | null {
  const name = form.name.trim();
  if (!SERVICE_NAME_PATTERN.test(name)) {
    return {
      field: "name",
      message: "Name must be 1-63 lowercase letters, digits, '-', '_' or '.', starting with a letter or digit.",
    };
  }
  if (form.driver === "docker") {
    if (form.dockerSelector === "label") {
      if (!LABEL_PATTERN.test(form.label.trim())) {
        return { field: "driver", message: "Give the label as key=value." };
      }
    } else {
      const containers = form.containers.map((c) => c.trim()).filter(Boolean);
      if (containers.length === 0) return { field: "driver", message: "Add at least one container name." };
      const invalid = containers.find((c) => !CONTAINER_NAME_PATTERN.test(c));
      if (invalid) return { field: "driver", message: `Invalid container name "${invalid}".` };
    }
  }
  if (form.driver === "compose") {
    if (!form.composeProject.trim()) return { field: "driver", message: "Compose services need a project name." };
    if (form.composeFile.trim() && !hostCommands) {
      return { field: "driver", message: "A compose file runs host commands, which this server or your role does not allow." };
    }
  }
  if (form.driver === "exec") {
    if (!hostCommands) {
      return { field: "driver", message: "Host commands are disabled on this server or not allowed for your role." };
    }
    if (textToArgv(form.execStart).length === 0 || textToArgv(form.execStop).length === 0) {
      return { field: "driver", message: "Host command services need start and stop commands." };
    }
  }
  const readyTcp = form.readyTcp.trim();
  if (readyTcp && !isHostPort(readyTcp)) {
    return { field: "readyTcp", message: "The readiness address must be host:port." };
  }
  if (form.idlePreset === "custom" && (form.idleCustom.trim() === "" || !validTimeout(idleSeconds(form)))) {
    return { field: "idle", message: "Idle timeout must be between 0 seconds and 7 days." };
  }
  const startTimeout = Number(form.startTimeout);
  if (form.startTimeout.trim() === "" || !validTimeout(startTimeout)) {
    return { field: "startTimeout", message: "Start timeout must be between 0 seconds and 7 days." };
  }
  return null;
}

export function serviceSelectorSummary(service: Pick<Service, "driver" | "spec">): string {
  const { spec } = service;
  if (service.driver === "docker") {
    if (spec.docker?.containers?.length) return spec.docker.containers.join(", ");
    return spec.docker?.label ? `label ${spec.docker.label}` : "";
  }
  if (service.driver === "compose") {
    const services = spec.compose?.services?.length ? ` (${spec.compose.services.join(", ")})` : "";
    return `${spec.compose?.project ?? ""}${services}`;
  }
  return spec.exec?.start?.join(" ") ?? "";
}
