import { useEffect, useState, type FormEvent } from "react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { FormError } from "@/components/common/ConfirmDialog";
import {
  TargetsField,
  UpstreamAdvancedFields,
  upstreamFormFromPolicy,
  upstreamPolicyFields,
  validateTargets,
  type UpstreamForm,
} from "@/components/common/UpstreamFields";
import { useUpdateEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { mergePolicy } from "@/lib/endpoints";
import type { Endpoint } from "@/api/types";

export function EditTargetsDialog({
  endpoint,
  open,
  onOpenChange,
}: {
  endpoint: Endpoint;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const update = useUpdateEndpoint();
  const [form, setForm] = useState<UpstreamForm>(() => upstreamFormFromPolicy(endpoint.policy));
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setForm(upstreamFormFromPolicy(endpoint.policy));
    setProblem(null);
    update.reset();
  }, [open]);

  const onChange = (next: UpstreamForm) => {
    setForm(next);
    setProblem(null);
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    const invalid = validateTargets(form);
    if (invalid) {
      setProblem(invalid);
      return;
    }
    update.mutate(
      { id: endpoint.id, policy: mergePolicy(endpoint.policy, upstreamPolicyFields(form)) },
      {
        onSuccess: () => {
          toast({ title: "Targets saved" });
          onOpenChange(false);
        },
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={onSubmit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Edit targets</DialogTitle>
            <DialogDescription>The backends this endpoint proxies to, how requests are spread and how https:// origins are verified.</DialogDescription>
          </DialogHeader>
          <TargetsField form={form} onChange={onChange} error={problem} />
          <div className="rounded-md border border-border p-3">
            <UpstreamAdvancedFields form={form} onChange={onChange} />
          </div>
          <FormError message={update.error ? errorMessage(update.error) : undefined} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={update.isPending}>
              {update.isPending ? "Saving…" : "Save targets"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
