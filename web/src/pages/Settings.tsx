import { useEffect, useState, type FormEvent } from "react";
import { CheckCircle2, XCircle } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ErrorState, LoadingState } from "@/components/common/States";
import { usePolicy, useQuota, useUpdatePolicy, useUpdateQuota } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { ApiError } from "@/api/client";
import { formatBytes } from "@/lib/utils";
import type { Policy, Quota, QuotaUpdate } from "@/api/types";

function Flag({ label, on }: { label: string; on: boolean }) {
  return (
    <div className="flex items-center justify-between rounded-md border border-border px-3 py-2.5">
      <span className="text-sm">{label}</span>
      {on ? (
        <span className="inline-flex items-center gap-1.5 text-sm text-success">
          <CheckCircle2 className="h-4 w-4" /> enabled
        </span>
      ) : (
        <span className="inline-flex items-center gap-1.5 text-sm text-muted-foreground">
          <XCircle className="h-4 w-4" /> disabled
        </span>
      )}
    </div>
  );
}

function QuotaForm({ orgId, quota }: { orgId?: string; quota: Quota }) {
  const update = useUpdateQuota(orgId);
  const { isOwnerOrAdmin } = useSession();
  const [form, setForm] = useState<QuotaUpdate>({
    max_agents: quota.max_agents,
    max_endpoints: quota.max_endpoints,
    max_bandwidth_bytes: quota.max_bandwidth_bytes,
  });
  useEffect(
    () =>
      setForm({
        max_agents: quota.max_agents,
        max_endpoints: quota.max_endpoints,
        max_bandwidth_bytes: quota.max_bandwidth_bytes,
      }),
    [quota],
  );

  const num = (k: keyof QuotaUpdate) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setForm((f) => ({ ...f, [k]: Number(e.target.value) }));

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    update.mutate(form, {
      onSuccess: () => toast({ title: "Quota saved" }),
      onError: (err) =>
        toast({ variant: "destructive", title: "Save failed", description: err instanceof ApiError ? err.message : "Unknown error" }),
    });
  };

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5">
          <Label>Max agents</Label>
          <Input type="number" value={form.max_agents} onChange={num("max_agents")} disabled={!isOwnerOrAdmin} />
        </div>
        <div className="space-y-1.5">
          <Label>Max endpoints</Label>
          <Input type="number" value={form.max_endpoints} onChange={num("max_endpoints")} disabled={!isOwnerOrAdmin} />
        </div>
        <div className="space-y-1.5">
          <Label>Max bandwidth</Label>
          <Input type="number" value={form.max_bandwidth_bytes} onChange={num("max_bandwidth_bytes")} disabled={!isOwnerOrAdmin} />
          <p className="text-xs text-muted-foreground">
            {form.max_bandwidth_bytes > 0 ? formatBytes(form.max_bandwidth_bytes) : "unlimited"}
          </p>
        </div>
      </div>
      {isOwnerOrAdmin && (
        <div className="flex justify-end">
          <Button type="submit" disabled={update.isPending}>
            {update.isPending ? "Saving…" : "Save quota"}
          </Button>
        </div>
      )}
    </form>
  );
}

function PermissionMatrix({ orgId, policy }: { orgId?: string; policy: Policy }) {
  const update = useUpdatePolicy(orgId);
  const { isOwnerOrAdmin } = useSession();
  const [matrix, setMatrix] = useState(policy.matrix);
  useEffect(() => setMatrix(policy.matrix), [policy]);

  const toggle = (role: string, action: string) =>
    setMatrix((m) => ({ ...m, [role]: { ...m[role], [action]: !m[role]?.[action] } }));

  const onSave = () => {
    const body = {
      matrix: Object.fromEntries(
        policy.roles.map((role) => [role, policy.actions.filter((a) => matrix[role]?.[a])]),
      ),
    };
    update.mutate(body, {
      onSuccess: () => toast({ title: "Permissions saved" }),
      onError: (err) =>
        toast({ variant: "destructive", title: "Save failed", description: err instanceof ApiError ? err.message : "Unknown error" }),
    });
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        {policy.is_default ? (
          <Badge variant="secondary">Default policy</Badge>
        ) : (
          <Badge>Customized</Badge>
        )}
        <p className="text-xs text-muted-foreground">Cedar-backed. Members are read-only by default.</p>
      </div>
      <div className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Action</TableHead>
              {policy.roles.map((role) => (
                <TableHead key={role} className="text-center capitalize">{role}</TableHead>
              ))}
            </TableRow>
          </TableHeader>
          <TableBody>
            {policy.actions.map((action) => (
              <TableRow key={action}>
                <TableCell className="font-mono text-xs">{action}</TableCell>
                {policy.roles.map((role) => (
                  <TableCell key={role} className="text-center">
                    <Switch
                      checked={!!matrix[role]?.[action]}
                      onCheckedChange={() => toggle(role, action)}
                      disabled={!isOwnerOrAdmin}
                    />
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {isOwnerOrAdmin && (
        <div className="flex justify-end">
          <Button onClick={onSave} disabled={update.isPending}>
            {update.isPending ? "Saving…" : "Save permissions"}
          </Button>
        </div>
      )}
    </div>
  );
}

export function Settings() {
  const { authConfig, currentOrgId } = useSession();
  const quota = useQuota(currentOrgId);
  const policy = usePolicy(currentOrgId);

  return (
    <div className="space-y-6">
      <PageHeader title="Settings" description="Server configuration and organization quota." />

      <Card>
        <CardHeader>
          <CardTitle>Effective configuration</CardTitle>
          <CardDescription>Feature flags reported by the server.</CardDescription>
        </CardHeader>
        <CardContent className="grid gap-2 sm:grid-cols-3">
          <Flag label="Authentication" on={authConfig.auth_enabled} />
          <Flag label="Password login" on={authConfig.password_enabled} />
          <Flag label="Google login" on={authConfig.google_enabled} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Quota</CardTitle>
          <CardDescription>Per-organization usage limits.</CardDescription>
        </CardHeader>
        <CardContent>
          {quota.isLoading ? (
            <LoadingState label="Loading quota" />
          ) : quota.isError ? (
            <ErrorState error={quota.error} />
          ) : quota.data ? (
            <QuotaForm orgId={currentOrgId} quota={quota.data} />
          ) : null}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Permissions</CardTitle>
          <CardDescription>Role × action matrix enforced on the control API.</CardDescription>
        </CardHeader>
        <CardContent>
          {policy.isLoading ? (
            <LoadingState label="Loading permissions" />
          ) : policy.isError ? (
            <ErrorState error={policy.error} />
          ) : policy.data ? (
            <PermissionMatrix orgId={currentOrgId} policy={policy.data} />
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
