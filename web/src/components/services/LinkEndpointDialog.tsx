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
import { FormError } from "@/components/common/ConfirmDialog";
import { useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { endpointDisplayName } from "@/lib/health";
import { isEphemeral, mergePolicy } from "@/lib/endpoints";
import type { Endpoint, Service } from "@/api/types";

export function linkableEndpoints(endpoints: Endpoint[], service: Pick<Service, "id">): Endpoint[] {
  return endpoints.filter((ep) => ep.kind === "http" && !isEphemeral(ep) && ep.policy?.service_id !== service.id);
}

export function LinkEndpointDialog({
  service,
  endpoints,
  services,
  open,
  onOpenChange,
}: {
  service: Service;
  endpoints: Endpoint[];
  services: Map<string, Service>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const update = useUpdateEndpoint();
  const [endpointId, setEndpointId] = useState("");
  const candidates = linkableEndpoints(endpoints, service);
  const selected = candidates.find((ep) => ep.id === endpointId);
  const replacedServiceId = selected?.policy?.service_id;
  const replacedService = replacedServiceId ? services.get(replacedServiceId) : undefined;

  useEffect(() => {
    if (!open) return;
    setEndpointId("");
    update.reset();
  }, [open]);

  const onLink = () => {
    if (!selected) return;
    update.mutate(
      { id: selected.id, policy: mergePolicy(selected.policy, { service_id: service.id }) },
      {
        onSuccess: () => {
          toast({ title: `Linked ${endpointDisplayName(selected)}`, description: `Requests to it now wake ${service.name}.` });
          onOpenChange(false);
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Link an existing endpoint</DialogTitle>
          <DialogDescription>A request to the endpoint starts {service.name} when it is stopped.</DialogDescription>
        </DialogHeader>
        {candidates.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No eligible endpoints. Only reserved HTTP endpoints can wake a service.
          </p>
        ) : (
          <div className="space-y-1.5">
            <Label>Endpoint</Label>
            <Select value={endpointId} onValueChange={setEndpointId}>
              <SelectTrigger>
                <SelectValue placeholder="Select an endpoint" />
              </SelectTrigger>
              <SelectContent>
                {candidates.map((ep) => (
                  <SelectItem key={ep.id} value={ep.id}>
                    {endpointDisplayName(ep)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {replacedServiceId && (
              <p className="text-xs text-amber-600 dark:text-amber-400">
                This endpoint currently wakes {replacedService?.name ?? replacedServiceId}. Linking replaces it.
              </p>
            )}
          </div>
        )}
        <FormError message={update.error ? errorMessage(update.error) : undefined} />
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="button" disabled={!selected || update.isPending} onClick={onLink}>
            {update.isPending ? "Linking…" : "Link"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
