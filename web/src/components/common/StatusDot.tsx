import { cn } from "@/lib/utils";

export type StateTone = "success" | "danger" | "warning" | "sleeping" | "waking" | "offline" | "unknown";

const SUCCESS_STATES = ["up", "running", "synced", "ok", "active", "connected", "healthy", "online", "published", "ready", "managed"];
const DANGER_STATES = ["down", "error", "conflict", "failed", "unhealthy"];
const WARNING_STATES = ["degraded", "partial", "pending", "orphaned", "missing", "unclaimed"];
const SLEEPING_STATES = ["sleeping", "stopped"];
const WAKING_STATES = ["waking", "starting"];

export function toneForState(state: string): StateTone {
  const normalized = state.toLowerCase();
  if (SUCCESS_STATES.includes(normalized)) return "success";
  if (DANGER_STATES.includes(normalized)) return "danger";
  if (WARNING_STATES.includes(normalized)) return "warning";
  if (SLEEPING_STATES.includes(normalized)) return "sleeping";
  if (WAKING_STATES.includes(normalized)) return "waking";
  if (normalized === "offline") return "offline";
  return "unknown";
}

export const TONE_TEXT: Record<StateTone, string> = {
  success: "text-success",
  danger: "text-destructive",
  warning: "text-amber-600 dark:text-amber-400",
  sleeping: "text-sky-600 dark:text-sky-400",
  waking: "text-sky-600 dark:text-sky-400",
  offline: "text-muted-foreground",
  unknown: "text-muted-foreground",
};

export const TONE_SURFACE: Record<StateTone, string> = {
  success: "border-success/40 bg-success/5",
  danger: "border-destructive/50 bg-destructive/5",
  warning: "border-amber-500/50 bg-amber-500/5",
  sleeping: "border-sky-500/40 bg-sky-500/5",
  waking: "border-sky-500/40 bg-sky-500/5",
  offline: "border-border bg-muted/40",
  unknown: "border-dashed border-border bg-card",
};

export const TONE_BADGE: Record<StateTone, string> = {
  success: "border-transparent bg-success/15 text-success",
  danger: "border-transparent bg-destructive/15 text-destructive",
  warning: "border-transparent bg-amber-500/15 text-amber-700 dark:text-amber-400",
  sleeping: "border-transparent bg-sky-500/15 text-sky-700 dark:text-sky-400",
  waking: "border-transparent bg-sky-500/15 text-sky-700 dark:text-sky-400",
  offline: "border-transparent bg-muted text-muted-foreground",
  unknown: "border-dashed border-border text-muted-foreground",
};

function ToneMarker({ tone }: { tone: StateTone }) {
  if (tone === "waking") {
    return (
      <span className="inline-flex h-2 w-2 animate-spin rounded-full border-[1.5px] border-sky-500 border-t-transparent" />
    );
  }
  if (tone === "sleeping") {
    return <span className="inline-flex h-2 w-2 rounded-full border-[1.5px] border-sky-500 bg-transparent" />;
  }
  if (tone === "unknown") {
    return <span className="inline-flex h-2 w-2 rounded-full border border-dashed border-muted-foreground" />;
  }
  const fill =
    tone === "success"
      ? "bg-success"
      : tone === "danger"
        ? "bg-destructive"
        : tone === "warning"
          ? "bg-amber-500"
          : "bg-muted-foreground/50";
  const pulses = tone === "success" || tone === "warning";
  return (
    <span className="relative flex h-2 w-2">
      {pulses && <span className={cn("absolute inline-flex h-full w-full rounded-full opacity-60 animate-pulse-dot", fill)} />}
      <span className={cn("relative inline-flex h-2 w-2 rounded-full", fill)} />
    </span>
  );
}

export function StateDot({
  state,
  label,
  className,
}: {
  state: string;
  label?: string;
  className?: string;
}) {
  const tone = toneForState(state);
  return (
    <span className={cn("inline-flex items-center gap-2 text-sm", className)}>
      <ToneMarker tone={tone} />
      {label && <span className={cn(tone === "success" ? "text-foreground" : TONE_TEXT[tone])}>{label}</span>}
    </span>
  );
}

export function StatusDot({
  online,
  label,
  className,
}: {
  online: boolean;
  label?: string;
  className?: string;
}) {
  return <StateDot state={online ? "up" : "offline"} label={label} className={className} />;
}
