import type { TypedField } from "@/api/types";

export type FieldValues = Record<string, unknown>;

export function fieldDefaults(fields: TypedField[]): FieldValues {
  const result: FieldValues = {};
  for (const field of fields) {
    if (field.default !== undefined)
      result[field.key] = structuredClone(field.default);
  }
  return result;
}

export function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === "string")
    : [];
}

export function stringMap(value: unknown): Record<string, string> {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    return {};
  return Object.fromEntries(
    Object.entries(value).filter(
      (entry): entry is [string, string] => typeof entry[1] === "string",
    ),
  );
}

export function validateFields(
  fields: TypedField[],
  values: FieldValues,
  presence: Record<string, boolean> = {},
): string | null {
  for (const field of fields) {
    const value = values[field.key];
    const empty =
      value === undefined ||
      value === "" ||
      value === null ||
      (Array.isArray(value) && value.length === 0);
    if (
      field.required &&
      empty &&
      !(field.kind === "secret" && presence[field.key])
    )
      return `${field.label} is required.`;
    if (
      !empty &&
      field.kind === "number" &&
      (typeof value !== "number" || !Number.isFinite(value))
    )
      return `${field.label} must be a number.`;
    if (
      !empty &&
      field.kind === "select" &&
      !field.options?.includes(String(value))
    )
      return `Choose a valid ${field.label}.`;
  }
  return null;
}

export function splitConnectionFields(
  fields: TypedField[],
  values: FieldValues,
): { config: FieldValues; secrets: Record<string, string> } {
  const config: FieldValues = {};
  const secrets: Record<string, string> = {};
  for (const field of fields) {
    const value = values[field.key];
    if (value === undefined) continue;
    if (field.kind === "secret") {
      if (typeof value === "string") secrets[field.key] = value;
    } else {
      config[field.key] = value;
    }
  }
  return { config, secrets };
}
