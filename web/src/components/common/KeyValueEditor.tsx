import { Plus, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export interface KVPair {
  key: string;
  value: string;
}

export function KeyValueEditor({
  pairs,
  onChange,
  keyPlaceholder = "Header",
  valuePlaceholder = "Value",
}: {
  pairs: KVPair[];
  onChange: (pairs: KVPair[]) => void;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
}) {
  const update = (i: number, patch: Partial<KVPair>) =>
    onChange(pairs.map((p, idx) => (idx === i ? { ...p, ...patch } : p)));
  const remove = (i: number) => onChange(pairs.filter((_, idx) => idx !== i));
  const add = () => onChange([...pairs, { key: "", value: "" }]);

  return (
    <div className="space-y-2">
      {pairs.map((pair, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            value={pair.key}
            onChange={(e) => update(i, { key: e.target.value })}
            placeholder={keyPlaceholder}
            className="font-mono text-xs"
          />
          <Input
            value={pair.value}
            onChange={(e) => update(i, { value: e.target.value })}
            placeholder={valuePlaceholder}
            className="font-mono text-xs"
          />
          <Button type="button" variant="ghost" size="icon" className="shrink-0" onClick={() => remove(i)}>
            <X className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" onClick={add}>
        <Plus className="h-3.5 w-3.5" /> Add
      </Button>
    </div>
  );
}

export function ListEditor({
  values,
  onChange,
  placeholder,
}: {
  values: string[];
  onChange: (values: string[]) => void;
  placeholder?: string;
}) {
  const update = (i: number, v: string) => onChange(values.map((x, idx) => (idx === i ? v : x)));
  const remove = (i: number) => onChange(values.filter((_, idx) => idx !== i));
  const add = () => onChange([...values, ""]);
  return (
    <div className="space-y-2">
      {values.map((v, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            value={v}
            onChange={(e) => update(i, e.target.value)}
            placeholder={placeholder}
            className="font-mono text-xs"
          />
          <Button type="button" variant="ghost" size="icon" className="shrink-0" onClick={() => remove(i)}>
            <X className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button type="button" variant="outline" size="sm" onClick={add}>
        <Plus className="h-3.5 w-3.5" /> Add
      </Button>
    </div>
  );
}

export function pairsToRecord(pairs: KVPair[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const p of pairs) {
    if (p.key.trim()) out[p.key.trim()] = p.value;
  }
  return out;
}

export function recordToPairs(record?: Record<string, string>): KVPair[] {
  if (!record) return [];
  return Object.entries(record).map(([key, value]) => ({ key, value }));
}
