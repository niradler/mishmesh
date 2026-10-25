import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { ErrorState, LoadingState } from "@/components/common/States";
import { Pipeline } from "@/components/endpoints/PipelineCard";
import { HostnameStage } from "@/components/endpoints/PublicAccessStages";
import {
  AgentStage,
  EndpointStage,
  TargetsStage,
} from "@/components/endpoints/EndpointStages";
import { ServiceStage } from "@/components/endpoints/ServiceStage";
import { EditAddressDialog } from "@/components/endpoints/EditAddressDialog";
import { EditTargetsDialog } from "@/components/endpoints/EditTargetsDialog";
import { EndpointPolicyForm } from "@/components/endpoints/EndpointPolicyForm";
import { useDeleteEndpoint, useEndpoint } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { endpointDisplayName } from "@/lib/health";
import { AccessSection } from "@/components/endpoints/AccessSection";
import { BrowserSessionsSection } from "@/components/endpoints/BrowserSessionsSection";

export function EndpointDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { canWrite } = useSession();
  const endpoint = useEndpoint(id);
  const remove = useDeleteEndpoint();
  const [editingAddress, setEditingAddress] = useState(false);
  const [editingTargets, setEditingTargets] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  if (endpoint.isLoading) return <LoadingState label="Loading endpoint" />;
  if (endpoint.isError) return <ErrorState error={endpoint.error} />;
  if (!endpoint.data) return null;
  const ep = endpoint.data;
  const isProxy = ep.method === "proxy";

  const onDelete = () =>
    remove.mutate(ep.id, {
      onSuccess: () => {
        toast({ title: "Endpoint deleted" });
        navigate("/endpoints");
      },
    });

  return (
    <div className="space-y-6">
      <Link
        to="/endpoints"
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="h-4 w-4" /> Endpoints
      </Link>

      <PageHeader
        title={endpointDisplayName(ep)}
        description="The request path, from the internet to your service. Each stage holds its own actions."
        actions={
          canWrite && (
            <Button variant="outline" onClick={() => setConfirmDelete(true)}>
              <Trash2 className="h-4 w-4" /> Delete
            </Button>
          )
        }
      />

      <Pipeline>
        {[
          <HostnameStage
            key="hostname"
            endpoint={ep}
            canWrite={canWrite}
            onEditAddress={() => setEditingAddress(true)}
          />,
          <EndpointStage key="endpoint" endpoint={ep} />,
          isProxy ? (
            <TargetsStage
              key="targets"
              endpoint={ep}
              canWrite={canWrite}
              onEdit={() => setEditingTargets(true)}
            />
          ) : (
            <AgentStage key="agent" endpoint={ep} />
          ),
          <ServiceStage key="service" endpoint={ep} canWrite={canWrite} />,
        ]}
      </Pipeline>

      <AccessSection endpoint={ep} canWrite={canWrite} />
      <BrowserSessionsSection endpoint={ep} canWrite={canWrite} />
      <EndpointPolicyForm endpoint={ep} canWrite={canWrite} />

      <EditAddressDialog
        endpoint={ep}
        open={editingAddress}
        onOpenChange={setEditingAddress}
      />
      {isProxy && (
        <EditTargetsDialog
          endpoint={ep}
          open={editingTargets}
          onOpenChange={setEditingTargets}
        />
      )}
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete endpoint?"
        description={
          <>
            <span className="font-mono">{endpointDisplayName(ep)}</span> stops
            accepting traffic and its address is released.
            {Boolean(ep.policy?.access?.length) &&
              " Its Access refs are also removed."}{" "}
            This cannot be undone.
          </>
        }
        pending={remove.isPending}
        error={remove.error ? errorMessage(remove.error) : undefined}
        onConfirm={onDelete}
      />
    </div>
  );
}
