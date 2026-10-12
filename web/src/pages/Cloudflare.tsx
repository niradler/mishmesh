import { useMemo, useState } from "react";
import { Unplug } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { FormSection } from "@/components/common/FormSection";
import { ErrorState, LoadingState } from "@/components/common/States";
import { ConnectForm } from "@/components/cloudflare/ConnectForm";
import { CloudflareSummary } from "@/components/cloudflare/CloudflareSummary";
import { HostnamesTable } from "@/components/cloudflare/HostnamesTable";
import { ManagementSection } from "@/components/cloudflare/ManagementSection";
import { useCloudflare, useDeleteCloudflare, useEndpoints } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { cloudflareHostnames, isCloudflareConnected } from "@/lib/cloudflare";
import type { Endpoint } from "@/api/types";

const DESCRIPTION = "Publish endpoints on the internet through a Cloudflare tunnel, without opening any ports.";

export function Cloudflare() {
  const { currentOrgId, canWrite } = useSession();
  const cloudflare = useCloudflare(currentOrgId);
  const endpoints = useEndpoints(currentOrgId);
  const disconnect = useDeleteCloudflare();
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const [editKey, setEditKey] = useState(0);
  const endpointsById = useMemo(
    () => new Map<string, Endpoint>((endpoints.data ?? []).map((ep) => [ep.id, ep])),
    [endpoints.data],
  );

  if (cloudflare.isLoading) return <LoadingState label="Loading Cloudflare" />;
  if (cloudflare.isError) return <ErrorState error={cloudflare.error} />;

  const data = cloudflare.data;
  const connection = data?.connection;

  if (!data || !connection || !isCloudflareConnected(data)) {
    return (
      <div>
        <PageHeader title="Cloudflare" description={DESCRIPTION} />
        <Card className="max-w-2xl">
          <CardHeader>
            <CardTitle>Import a tunnel</CardTitle>
            <CardDescription>
              Start with the tunnel token and the metrics address of the cloudflared that serves it. mishmesh only reads until you
              add an API token.
            </CardDescription>
          </CardHeader>
          <CardContent>
            {canWrite ? (
              <ConnectForm />
            ) : (
              <p className="text-sm text-muted-foreground">An owner or admin can connect Cloudflare for this organization.</p>
            )}
          </CardContent>
        </Card>
      </div>
    );
  }

  const onDisconnect = () =>
    disconnect.mutate(undefined, {
      onSuccess: () => {
        setConfirmDisconnect(false);
        toast({ title: "Cloudflare disconnected" });
      },
    });

  return (
    <div className="space-y-6">
      <PageHeader
        title="Cloudflare"
        description={DESCRIPTION}
        actions={
          canWrite ? (
            <Button
              variant="outline"
              onClick={() => {
                disconnect.reset();
                setConfirmDisconnect(true);
              }}
            >
              <Unplug className="h-4 w-4" /> Disconnect
            </Button>
          ) : undefined
        }
      />

      <CloudflareSummary cloudflare={data} connection={connection} canWrite={canWrite} />
      <HostnamesTable
        hostnames={cloudflareHostnames(data)}
        level={data.level}
        endpointsById={endpointsById}
        canWrite={canWrite}
      />

      {canWrite && (
        <Card>
          <CardHeader>
            <CardTitle>Settings</CardTitle>
            <CardDescription>Management level and connection details.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <ManagementSection connection={connection} level={data.level} />
            <FormSection title="Edit connection" summary={`${connection.connector} connector · ${connection.metrics_addr}`}>
              <ConnectForm key={editKey} connection={connection} onSaved={() => setEditKey((k) => k + 1)} />
            </FormSection>
          </CardContent>
        </Card>
      )}

      <ConfirmDialog
        open={confirmDisconnect}
        onOpenChange={setConfirmDisconnect}
        title="Disconnect Cloudflare?"
        description={
          <>
            mishmesh forgets <span className="font-medium">{connection.name}</span> and its stored tokens. Nothing changes in
            Cloudflare: the tunnel, DNS records and ingress rules stay. Endpoints keep their publish setting but are no longer
            published.
          </>
        }
        confirmLabel="Disconnect"
        pendingLabel="Disconnecting…"
        pending={disconnect.isPending}
        error={disconnect.error ? errorMessage(disconnect.error) : undefined}
        onConfirm={onDisconnect}
      />
    </div>
  );
}
