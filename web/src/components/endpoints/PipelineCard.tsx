import { Fragment, type ReactNode } from "react";
import { ArrowDown, ArrowRight, type LucideIcon } from "lucide-react";
import { TONE_SURFACE, toneForState } from "@/components/common/StatusDot";
import { cn } from "@/lib/utils";

export function PipelineCard({
  stage,
  icon: Icon,
  title,
  state,
  empty = false,
  children,
  actions,
}: {
  stage: string;
  icon: LucideIcon;
  title?: ReactNode;
  state?: string;
  empty?: boolean;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  const surface = empty ? "border-dashed border-border bg-muted/20" : state ? TONE_SURFACE[toneForState(state)] : "border-border bg-card";
  return (
    <div className={cn("flex min-w-0 flex-1 flex-col gap-3 rounded-card border p-4 lg:min-w-[11rem]", surface)}>
      <div className="flex items-center gap-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
        <Icon className="h-3.5 w-3.5" />
        {stage}
      </div>
      {title && <div className="min-w-0 break-words text-sm font-medium">{title}</div>}
      {children && <div className="space-y-2 text-sm">{children}</div>}
      {actions && <div className="mt-auto flex flex-wrap gap-1.5 pt-1">{actions}</div>}
    </div>
  );
}

export function Pipeline({ children }: { children: ReactNode[] }) {
  const stages = children.filter(Boolean);
  return (
    <div className="flex flex-col items-stretch gap-2 lg:flex-row">
      {stages.map((stage, index) => (
        <Fragment key={index}>
          {index > 0 && (
            <div className="flex items-center justify-center text-muted-foreground" aria-hidden="true">
              <ArrowDown className="h-4 w-4 lg:hidden" />
              <ArrowRight className="hidden h-4 w-4 lg:block" />
            </div>
          )}
          {stage}
        </Fragment>
      ))}
    </div>
  );
}
