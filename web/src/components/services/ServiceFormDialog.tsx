import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ServiceForm } from "@/components/services/ServiceForm";
import type { Service } from "@/api/types";

export function ServiceFormDialog({
  open,
  onOpenChange,
  service,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  service?: Service;
  onSaved?: (service: Service) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{service ? `Edit ${service.name}` : "New service"}</DialogTitle>
          <DialogDescription>
            Something mishmesh starts on demand and stops when idle: Docker containers, a Compose project or host commands.
          </DialogDescription>
        </DialogHeader>
        {open && (
          <ServiceForm
            service={service}
            onCancel={() => onOpenChange(false)}
            onSaved={(saved) => {
              onOpenChange(false);
              onSaved?.(saved);
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}
