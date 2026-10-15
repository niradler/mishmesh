import { stringList, stringMap } from "@/lib/typed-fields";
import type { TypedField } from "@/api/types";

function detail(field: TypedField, value: unknown, present?: boolean): string {
  if (field.kind === "secret")
    return (present ?? (value !== undefined && value !== ""))
      ? "Configured"
      : "Not configured";
  if (field.sensitive) return value === undefined ? "None" : "Values hidden";
  if (field.kind === "list") return stringList(value).join("\n") || "None";
  if (field.kind === "keyvalue")
    return (
      Object.entries(stringMap(value))
        .map(([key, entry]) => `${key}=${entry}`)
        .join("\n") || "None"
    );
  if (field.kind === "bool") return value ? "Yes" : "No";
  return value === undefined || value === "" ? "None" : String(value);
}

export function DescriptorDetails({
  fields,
  values,
  secretPresence = {},
}: {
  fields: TypedField[];
  values: Record<string, unknown>;
  secretPresence?: Record<string, boolean>;
}): React.JSX.Element {
  return (
    <dl className="space-y-3">
      {fields.map((field) => (
        <div key={field.key}>
          <dt className="text-xs text-muted-foreground">{field.label}</dt>
          <dd className="mt-1 whitespace-pre-wrap break-all font-mono text-sm">
            {detail(field, values[field.key], secretPresence[field.key])}
          </dd>
        </div>
      ))}
    </dl>
  );
}
