import { CopyButton } from "./CopyButton";
import { cn } from "@/lib/utils";

export function CodeBlock({
  value,
  label,
  className,
  mask,
}: {
  value: string;
  label?: string;
  className?: string;
  mask?: boolean;
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-2 rounded-md border border-border bg-muted/50 px-3 py-2",
        className,
      )}
    >
      <code
        className={cn(
          "min-w-0 flex-1 truncate font-mono text-xs text-foreground",
          mask && "tracking-widest",
        )}
      >
        {value}
      </code>
      <CopyButton value={value} label={label} />
    </div>
  );
}
