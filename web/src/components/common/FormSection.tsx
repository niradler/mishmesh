import { useId, useState } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";

export function FormSection({
  title,
  description,
  summary,
  collapsible = true,
  defaultOpen = false,
  hasError = false,
  nested = false,
  children,
}: {
  title: string;
  description?: string;
  summary?: string;
  collapsible?: boolean;
  defaultOpen?: boolean;
  hasError?: boolean;
  nested?: boolean;
  children: React.ReactNode;
}) {
  const panelId = useId();
  const wantsOpen = defaultOpen || hasError;
  const [open, setOpen] = useState(wantsOpen);
  const [previousWantsOpen, setPreviousWantsOpen] = useState(wantsOpen);

  if (wantsOpen !== previousWantsOpen) {
    setPreviousWantsOpen(wantsOpen);
    if (wantsOpen) setOpen(true);
  }

  const heading = (
    <>
      <span className={cn("font-medium tracking-heading", nested ? "text-xs" : "text-sm")}>{title}</span>
      {description && !collapsible && <span className="block text-xs font-normal text-muted-foreground">{description}</span>}
    </>
  );

  if (!collapsible) {
    return (
      <section className="space-y-3 border-b border-border pb-4 last:border-0 last:pb-0">
        <div>{heading}</div>
        {children}
      </section>
    );
  }


  return (
    <section className={cn(nested ? "space-y-3" : "border-b border-border pb-4 last:border-0 last:pb-0")}>
      <h3>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => setOpen(!open)}
          className="flex w-full items-center gap-2 rounded-sm py-1 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ChevronRight
            className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
            aria-hidden="true"
          />
          <span className={cn("font-medium tracking-heading", nested ? "text-xs" : "text-sm")}>{title}</span>
          {summary && !open && (
            <span className="ml-auto truncate pl-3 text-xs font-normal text-muted-foreground">{summary}</span>
          )}
        </button>
      </h3>
      <div id={panelId} hidden={!open} className="space-y-3 pt-2">
        {description && <p className="text-xs text-muted-foreground">{description}</p>}
        {children}
      </div>
    </section>
  );
}
