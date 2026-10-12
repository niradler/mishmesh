import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { DriverBadge, HelpedStateBadge } from "@/components/common/TermBadges";
import { EndpointLinks } from "@/components/services/EndpointLinks";
import { ServiceFormDialog } from "@/components/services/ServiceFormDialog";
import { useEndpoints, useServices } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { formatDurationSeconds } from "@/lib/health";
import { serviceSelectorSummary } from "@/lib/services";
import type { Endpoint } from "@/api/types";

export function Services() {
  const { currentOrgId, canWrite } = useSession();
  const navigate = useNavigate();
  const services = useServices(currentOrgId);
  const endpoints = useEndpoints(currentOrgId);
  const [creating, setCreating] = useState(false);
  const endpointsById = useMemo(
    () => new Map<string, Endpoint>((endpoints.data ?? []).map((ep) => [ep.id, ep])),
    [endpoints.data],
  );

  const newButton = canWrite ? (
    <Button onClick={() => setCreating(true)}>
      <Plus className="h-4 w-4" /> New service
    </Button>
  ) : undefined;

  return (
    <div>
      <PageHeader
        title="Services"
        description="Backends mishmesh starts on the first request and stops when idle: Docker containers, Compose projects or host commands."
        actions={newButton}
      />
      <Card>
        <CardContent className="pt-4">
          {services.isLoading ? (
            <LoadingState label="Loading services" />
          ) : services.isError ? (
            <ErrorState error={services.error} />
          ) : !services.data || services.data.length === 0 ? (
            <EmptyState
              title="No services"
              description="Define a service, then link it to an HTTP endpoint so requests wake it."
              action={newButton}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Driver</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Idle timeout</TableHead>
                  <TableHead>Woken by</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {services.data.map((service) => (
                  <TableRow key={service.id}>
                    <TableCell>
                      <Link to={`/services/${service.id}`} className="font-medium hover:underline">
                        {service.name}
                      </Link>
                      <p className="max-w-xs truncate text-xs text-muted-foreground" title={serviceSelectorSummary(service)}>
                        {serviceSelectorSummary(service)}
                      </p>
                    </TableCell>
                    <TableCell>
                      <DriverBadge driver={service.driver} />
                    </TableCell>
                    <TableCell>
                      <HelpedStateBadge scope="service" state={service.status.state} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {service.idle_timeout_seconds ? formatDurationSeconds(service.idle_timeout_seconds) : "never"}
                    </TableCell>
                    <TableCell>
                      <EndpointLinks ids={service.endpoints ?? []} endpointsById={endpointsById} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      <ServiceFormDialog open={creating} onOpenChange={setCreating} onSaved={(saved) => navigate(`/services/${saved.id}`)} />
    </div>
  );
}
