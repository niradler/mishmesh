import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { FormError } from "@/components/common/ConfirmDialog";
import { ServiceForm } from "@/components/services/ServiceForm";
import { useServices, useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { DRIVER_LABELS } from "@/lib/glossary";
import { mergePolicy } from "@/lib/endpoints";
import type { Endpoint, Service } from "@/api/types";

export function useServiceLink(endpoint: Endpoint) {
  const update = useUpdateEndpoint();
  const link = (service: Pick<Service, "id" | "name">, onDone?: () => void) =>
    update.mutate(
      { id: endpoint.id, policy: mergePolicy(endpoint.policy, { service_id: service.id }) },
      {
        onSuccess: () => {
          toast({ title: `Linked ${service.name}`, description: "Requests to this endpoint now wake it." });
          onDone?.();
        },
        onError: (err) => toast({ variant: "destructive", title: "Link failed", description: errorMessage(err) }),
      },
    );
  const unlink = (onDone?: () => void) =>
    update.mutate(
      { id: endpoint.id, policy: mergePolicy(endpoint.policy, { service_id: undefined }) },
      {
        onSuccess: () => {
          toast({ title: "Service unlinked" });
          onDone?.();
        },
        onError: (err) => toast({ variant: "destructive", title: "Unlink failed", description: errorMessage(err) }),
      },
    );
  return { link, unlink, pending: update.isPending, error: update.error };
}

export function WakeServiceDialog({
  endpoint,
  open,
  onOpenChange,
}: {
  endpoint: Endpoint;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { currentOrgId } = useSession();
  const services = useServices(currentOrgId);
  const { link, pending, error } = useServiceLink(endpoint);
  const currentId = endpoint.policy?.service_id;
  const choices = (services.data ?? []).filter((s) => s.id !== currentId);
  const [tab, setTab] = useState<"existing" | "new">("existing");
  const [selected, setSelected] = useState("");

  useEffect(() => {
    if (!open) return;
    setSelected("");
    setTab(services.data && choices.length === 0 ? "new" : "existing");
  }, [open]);

  const chosen = choices.find((s) => s.id === selected);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{currentId ? "Change service" : "Wake a service"}</DialogTitle>
          <DialogDescription>
            A request to this endpoint starts the service when it is stopped; it stops again after its idle timeout.
          </DialogDescription>
        </DialogHeader>
        <Tabs value={tab} onValueChange={(v) => setTab(v as "existing" | "new")}>
          <TabsList>
            <TabsTrigger value="existing">Existing service</TabsTrigger>
            <TabsTrigger value="new">New service</TabsTrigger>
          </TabsList>
          <TabsContent value="existing" className="space-y-4">
            {choices.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                {services.isLoading ? "Loading services…" : "No other services yet. Create one in the New service tab."}
              </p>
            ) : (
              <div className="space-y-1.5">
                <Label>Service</Label>
                <Select value={selected} onValueChange={setSelected}>
                  <SelectTrigger>
                    <SelectValue placeholder="Select a service" />
                  </SelectTrigger>
                  <SelectContent>
                    {choices.map((service) => (
                      <SelectItem key={service.id} value={service.id}>
                        {service.name} · {DRIVER_LABELS[service.driver]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
            <FormError message={error ? errorMessage(error) : undefined} />
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="button" disabled={!chosen || pending} onClick={() => chosen && link(chosen, () => onOpenChange(false))}>
                {pending ? "Linking…" : "Link service"}
              </Button>
            </DialogFooter>
          </TabsContent>
          <TabsContent value="new">
            {open && tab === "new" && (
              <ServiceForm
                submitLabel="Create and link"
                onCancel={() => onOpenChange(false)}
                onSaved={(service) => {
                  setSelected(service.id);
                  setTab("existing");
                  link(service, () => onOpenChange(false));
                }}
              />
            )}
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>
  );
}
