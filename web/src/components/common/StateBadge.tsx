import { Badge } from "@/components/ui/badge";
import { TONE_BADGE, toneForState } from "@/components/common/StatusDot";
import { cn } from "@/lib/utils";

export function StateBadge({ state, label, className }: { state: string; label?: string; className?: string }) {
  return (
    <Badge variant="outline" className={cn(TONE_BADGE[toneForState(state)], className)}>
      {label ?? state}
    </Badge>
  );
}
