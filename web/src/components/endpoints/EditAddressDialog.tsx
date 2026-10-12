import { useEffect, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { LabelWithHelp } from "@/components/common/HelpTip";
import { FormError } from "@/components/common/ConfirmDialog";
import { useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { HELP } from "@/lib/glossary";
import type { Endpoint, UpdateEndpointRequest } from "@/api/types";

export function EditAddressDialog({
  endpoint,
  open,
  onOpenChange,
}: {
  endpoint: Endpoint;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const update = useUpdateEndpoint();
  const [subdomain, setSubdomain] = useState("");
  const [domain, setDomain] = useState("");
  const [port, setPort] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const showHostFields = endpoint.kind !== "tcp";
  const showSubdomain = endpoint.kind === "http";
  const published = endpoint.policy?.public_access === "cloudflare";

  useEffect(() => {
    if (!open) return;
    setSubdomain(endpoint.subdomain ?? "");
    setDomain(endpoint.domain ?? "");
    setPort(endpoint.port ? String(endpoint.port) : "");
    setProblem(null);
    update.reset();
  }, [open, endpoint.subdomain, endpoint.domain, endpoint.port]);

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    const body: UpdateEndpointRequest = {};
    const nextSubdomain = subdomain.trim().toLowerCase();
    const nextDomain = domain.trim().toLowerCase();
    if (showSubdomain && nextSubdomain !== (endpoint.subdomain ?? "")) body.subdomain = nextSubdomain;
    if (showHostFields && nextDomain !== (endpoint.domain ?? "")) body.domain = nextDomain;
    if (endpoint.kind === "tcp" && port.trim() && Number(port) !== endpoint.port) {
      const value = Number(port);
      if (!Number.isInteger(value) || value < 1 || value > 65535) {
        setProblem("Port must be between 1 and 65535.");
        return;
      }
      body.port = value;
    }
    if (published && showHostFields && !nextDomain) {
      setProblem("This endpoint is published via Cloudflare, which needs a custom domain. Unpublish it first.");
      return;
    }
    if (Object.keys(body).length === 0) {
      onOpenChange(false);
      return;
    }
    update.mutate(
      { id: endpoint.id, ...body },
      {
        onSuccess: () => {
          toast({ title: "Address updated" });
          onOpenChange(false);
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form onSubmit={onSubmit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Edit address</DialogTitle>
            <DialogDescription>Change where this endpoint is reachable. Empty fields are cleared.</DialogDescription>
          </DialogHeader>
          {showSubdomain && (
            <div className="space-y-1.5">
              <LabelWithHelp htmlFor="edit-subdomain" entry={HELP.subdomain}>
                Subdomain
              </LabelWithHelp>
              <Input id="edit-subdomain" value={subdomain} onChange={(e) => setSubdomain(e.target.value)} placeholder="api" />
            </div>
          )}
          {showHostFields && (
            <div className="space-y-1.5">
              <LabelWithHelp htmlFor="edit-domain" entry={HELP.domain}>
                {endpoint.kind === "tls" ? "Domain" : "Custom domain"}
              </LabelWithHelp>
              <Input id="edit-domain" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.example.com" />
            </div>
          )}
          {endpoint.kind === "tcp" && (
            <div className="space-y-1.5">
              <LabelWithHelp htmlFor="edit-port" entry={HELP.port}>
                Port
              </LabelWithHelp>
              <Input id="edit-port" type="number" value={port} onChange={(e) => setPort(e.target.value)} />
            </div>
          )}
          <FormError message={problem ?? (update.error ? errorMessage(update.error) : undefined)} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? "Saving…" : "Save address"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
