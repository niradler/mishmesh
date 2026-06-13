import { cn } from "@/lib/utils";

export function StatusDot({
  online,
  label,
  className,
}: {
  online: boolean;
  label?: string;
  className?: string;
}) {
  return (
    <span className={cn("inline-flex items-center gap-2 text-sm", className)}>
      <span className="relative flex h-2 w-2">
        {online && (
          <span className="absolute inline-flex h-full w-full rounded-full bg-success opacity-60 animate-pulse-dot" />
        )}
        <span
          className={cn(
            "relative inline-flex h-2 w-2 rounded-full",
            online ? "bg-success" : "bg-muted-foreground/50",
          )}
        />
      </span>
      {label && <span className={online ? "text-foreground" : "text-muted-foreground"}>{label}</span>}
    </span>
  );
}
