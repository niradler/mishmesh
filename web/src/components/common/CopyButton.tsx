import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { copyToClipboard } from "@/lib/utils";
import { toast } from "@/hooks/use-toast";
import { cn } from "@/lib/utils";

interface CopyButtonProps extends Omit<ButtonProps, "onClick" | "children"> {
  value: string;
  label?: string;
}

export function CopyButton({ value, label, className, ...props }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);
  const onCopy = async () => {
    const ok = await copyToClipboard(value);
    if (ok) {
      setCopied(true);
      toast({ title: `Copied ${label ?? "to clipboard"}` });
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast({ variant: "destructive", title: "Copy failed", description: "Clipboard unavailable." });
    }
  };
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className={cn("h-7 w-7 text-muted-foreground", className)}
      onClick={onCopy}
      aria-label={`Copy ${label ?? "value"}`}
      {...props}
    >
      {copied ? <Check className="text-success" /> : <Copy />}
    </Button>
  );
}
