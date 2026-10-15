import { useId } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { stringList, stringMap, type FieldValues } from "@/lib/typed-fields";
import type { TypedConnection, TypedField } from "@/api/types";

function MapField({
  field,
  value,
  onChange,
  disabled,
}: {
  field: TypedField;
  value: unknown;
  onChange: (value: unknown) => void;
  disabled: boolean;
}): React.JSX.Element {
  const entries = Object.entries(stringMap(value));
  function update(index: number, key: string, entry: string): void {
    if (
      entries.some(
        ([otherKey], position) => position !== index && otherKey === key,
      )
    )
      return;
    onChange(
      Object.fromEntries(
        entries.map((pair, position) =>
          position === index ? [key, entry] : pair,
        ),
      ),
    );
  }
  return (
    <div className="space-y-2" role="group" aria-label={field.label}>
      {entries.map(([key, entry], index) => (
        <div key={index} className="flex gap-2">
          <Input
            aria-label={`${field.label} key ${index + 1}`}
            value={key}
            disabled={disabled}
            onChange={(event) => update(index, event.target.value, entry)}
          />
          <Input
            aria-label={`${field.label} value ${index + 1}`}
            type={field.sensitive ? "password" : "text"}
            autoComplete={field.sensitive ? "new-password" : "off"}
            value={entry === "<redacted>" ? "" : entry}
            placeholder={entry === "<redacted>" ? "Unchanged" : "Value"}
            disabled={disabled}
            onChange={(event) => update(index, key, event.target.value)}
          />
          <Button
            type="button"
            variant="ghost"
            aria-label={`Remove ${field.label} entry ${index + 1}`}
            disabled={disabled}
            onClick={() =>
              onChange(
                Object.fromEntries(
                  entries.filter((_, position) => position !== index),
                ),
              )
            }
          >
            Remove
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={disabled || entries.some(([key]) => key === "")}
        onClick={() => onChange(Object.fromEntries([...entries, ["", ""]]))}
      >
        Add {field.label.toLowerCase()} entry
      </Button>
    </div>
  );
}

export function DescriptorFields({
  fields,
  values,
  onChange,
  connections = [],
  hostCommands = false,
  secretPresence = {},
}: {
  fields: TypedField[];
  values: FieldValues;
  onChange: (values: FieldValues) => void;
  connections?: TypedConnection[];
  hostCommands?: boolean;
  secretPresence?: Record<string, boolean>;
}): React.JSX.Element {
  const prefix = useId();
  function set(key: string, value: unknown): void {
    onChange({ ...values, [key]: value });
  }
  return (
    <div className="space-y-4">
      {fields.map((field) => {
        const id = `${prefix}-${field.key}`;
        const value = values[field.key];
        const disabled = Boolean(field.host_command && !hostCommands);
        const helpId = `${id}-help`;
        let control: React.JSX.Element;
        if (field.kind === "bool") {
          control = (
            <input
              id={id}
              aria-label={field.label}
              aria-required={field.required}
              type="checkbox"
              checked={value === true}
              disabled={disabled}
              aria-describedby={helpId}
              onChange={(event) => set(field.key, event.target.checked)}
            />
          );
        } else if (field.kind === "select" || field.kind === "connection") {
          const options =
            field.kind === "connection"
              ? connections
                  .filter((connection) => connection.type === field.conn_type)
                  .map((connection) => ({
                    value: connection.id,
                    label: connection.name,
                  }))
              : (field.options ?? []).map((option) => ({
                  value: option,
                  label: option,
                }));
          control = (
            <select
              id={id}
              aria-label={field.label}
              aria-required={field.required}
              className="h-10 w-full rounded-md border bg-background px-3 text-sm"
              value={typeof value === "string" ? value : ""}
              disabled={disabled}
              aria-describedby={helpId}
              onChange={(event) => set(field.key, event.target.value)}
            >
              <option value="">Choose…</option>
              {options.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
          );
        } else if (field.kind === "list") {
          control = (
            <Textarea
              id={id}
              aria-label={field.label}
              aria-required={field.required}
              value={stringList(value).join("\n")}
              placeholder={field.placeholder ?? "One argument or item per line"}
              disabled={disabled}
              aria-describedby={helpId}
              onChange={(event) =>
                set(
                  field.key,
                  event.target.value === ""
                    ? []
                    : event.target.value.replace(/\r\n/g, "\n").split("\n"),
                )
              }
            />
          );
        } else if (field.kind === "keyvalue") {
          control = (
            <MapField
              field={field}
              value={value}
              disabled={disabled}
              onChange={(next) => set(field.key, next)}
            />
          );
        } else {
          const secret = field.kind === "secret" || field.sensitive;
          const present = secretPresence[field.key] || value === "<redacted>";
          let inputType = "text";
          if (secret) inputType = "password";
          else if (field.kind === "number") inputType = "number";
          let inputValue: string | number = "";
          if (
            value !== "<redacted>" &&
            (typeof value === "number" || typeof value === "string")
          )
            inputValue = value;
          control = (
            <div className="space-y-2">
              <Input
                id={id}
                aria-label={field.label}
                aria-required={field.required}
                type={inputType}
                autoComplete={secret ? "new-password" : "off"}
                value={inputValue}
                placeholder={
                  present && (value === undefined || value === "<redacted>")
                    ? "Unchanged"
                    : field.placeholder
                }
                disabled={disabled}
                aria-describedby={helpId}
                onChange={(event) => {
                  let next: unknown = event.target.value;
                  if (field.kind === "number")
                    next =
                      event.target.value === ""
                        ? undefined
                        : Number(event.target.value);
                  set(field.key, next);
                }}
              />
              {secret && present && (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={disabled}
                  onClick={() => set(field.key, "")}
                >
                  Clear {field.label.toLowerCase()}
                </Button>
              )}
            </div>
          );
        }
        return (
          <div key={field.key} className="space-y-1.5">
            <Label htmlFor={field.kind === "keyvalue" ? undefined : id}>
              {field.label}
              {field.required && <span aria-hidden="true"> *</span>}
            </Label>
            {control}
            <p id={helpId} className="text-xs text-muted-foreground">
              {field.help}
              {disabled ? " Host commands are not allowed for you." : ""}
            </p>
          </div>
        );
      })}
    </div>
  );
}
