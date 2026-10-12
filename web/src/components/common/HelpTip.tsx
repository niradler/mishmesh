import { Info } from "lucide-react";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { GlossaryEntry } from "@/lib/glossary";
import { cn } from "@/lib/utils";

export function HelpTip({ entry, className }: { entry?: GlossaryEntry; className?: string }) {
  if (!entry) return null;
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label={`About ${entry.title}`}
          onClick={(event) => event.stopPropagation()}
          className={cn(
            "inline-flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            className,
          )}
        >
          <Info className="h-3.5 w-3.5" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="space-y-1" onClick={(event) => event.stopPropagation()}>
        <p className="text-sm font-medium tracking-heading">{entry.title}</p>
        <p className="text-xs leading-relaxed text-muted-foreground">{entry.body}</p>
      </PopoverContent>
    </Popover>
  );
}

export function LabelWithHelp({
  htmlFor,
  children,
  entry,
}: {
  htmlFor?: string;
  children: React.ReactNode;
  entry?: GlossaryEntry;
}) {
  return (
    <div className="flex items-center gap-1.5">
      <Label htmlFor={htmlFor}>{children}</Label>
      <HelpTip entry={entry} />
    </div>
  );
}
