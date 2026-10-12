import { useState, type FormEvent } from "react";
import { CloudUpload } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { mergePolicy } from "@/lib/endpoints";
import type { Endpoint } from "@/api/types";

export function usePublicAccessMutation(endpoint: Endpoint) {
  const update = useUpdateEndpoint();

  const publish = (domain?: string, onDone?: () => void) => {
    update.mutate(
      {
        id: endpoint.id,
        ...(domain ? { domain } : {}),
        policy: mergePolicy(endpoint.policy, { public_access: "cloudflare" }),
      },
      {
        onSuccess: () => {
          toast({ title: "Publishing via Cloudflare", description: domain ?? endpoint.domain });
          onDone?.();
        },
        onError: (err) => toast({ variant: "destructive", title: "Publish failed", description: errorMessage(err) }),
      },
    );
  };

  const unpublish = () => {
    update.mutate(
      { id: endpoint.id, policy: mergePolicy(endpoint.policy, { public_access: undefined }) },
      {
        onSuccess: () => toast({ title: "Unpublished from Cloudflare" }),
        onError: (err) => toast({ variant: "destructive", title: "Unpublish failed", description: errorMessage(err) }),
      },
    );
  };

  return { publish, unpublish, pending: update.isPending };
}

export function PublishControl({ endpoint, size = "sm" }: { endpoint: Endpoint; size?: "sm" | "default" }) {
  const { publish, pending } = usePublicAccessMutation(endpoint);
  const [asking, setAsking] = useState(false);
  const [domain, setDomain] = useState("");

  if (asking) {
    const onSubmit = (event: FormEvent) => {
      event.preventDefault();
      const trimmed = domain.trim().toLowerCase();
      if (!trimmed) return;
      publish(trimmed, () => {
        setAsking(false);
        setDomain("");
      });
    };
    return (
      <form onSubmit={onSubmit} className="w-full space-y-2">
        <p className="text-xs text-muted-foreground">Publishing needs a custom domain. It becomes the public hostname.</p>
        <Input
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          placeholder="app.example.com"
          className="h-8 font-mono text-xs"
          autoFocus
        />
        <div className="flex gap-1.5">
          <Button type="submit" size="sm" disabled={pending || !domain.trim()}>
            {pending ? "Publishing…" : "Publish"}
          </Button>
          <Button type="button" size="sm" variant="ghost" onClick={() => setAsking(false)}>
            Cancel
          </Button>
        </div>
      </form>
    );
  }

  return (
    <Button
      type="button"
      size={size}
      disabled={pending}
      onClick={() => (endpoint.domain ? publish() : setAsking(true))}
    >
      <CloudUpload className="h-3.5 w-3.5" />
      {pending ? "Publishing…" : "Turn on public access"}
    </Button>
  );
}
