import { Button } from "@/components/ui/button";
import type { TypeDescriptor } from "@/api/types";

export function TypePicker({
  types,
  value,
  onChange,
  disabled,
}: {
  types: TypeDescriptor[];
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}): React.JSX.Element {
  return (
    <div className="grid gap-2 sm:grid-cols-3" role="group" aria-label="Type">
      {types.map((type) => (
        <Button
          key={type.type}
          type="button"
          variant={value === type.type ? "default" : "outline"}
          aria-pressed={value === type.type}
          disabled={disabled}
          onClick={() => onChange(type.type)}
        >
          {type.label}
        </Button>
      ))}
    </div>
  );
}
