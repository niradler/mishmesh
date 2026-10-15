import type { ConnectionDescriptor, TypedConnection } from "@/api/types";

export function connectionScope(descriptor?: ConnectionDescriptor): string {
  if (descriptor?.scope === "public") return "Public";
  if (descriptor?.scope === "private") return "Private";
  return "Runtime";
}

export function connectionSecretPresence(
  connection: TypedConnection,
  descriptor: ConnectionDescriptor,
): Record<string, boolean> {
  return Object.fromEntries(
    descriptor.fields
      .filter((field) => field.kind === "secret")
      .map((field) => [field.key, Boolean(connection[`has_${field.key}`])]),
  );
}
