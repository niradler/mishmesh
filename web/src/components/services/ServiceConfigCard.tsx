import { Link } from "react-router-dom";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { DescriptorDetails } from "@/components/types/DescriptorDetails";
import { useServiceDrivers, useServices } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { formatDurationSeconds } from "@/lib/health";
import type { Service } from "@/api/types";

export function ServiceConfigCard({
  service,
}: {
  service: Service;
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const drivers = useServiceDrivers(currentOrgId);
  const services = useServices(currentOrgId);
  const descriptor = drivers.data?.find(
    (driver) => driver.type === service.driver,
  );
  const names = new Map((services.data ?? []).map((row) => [row.id, row.name]));
  return (
    <Card>
      <CardHeader>
        <CardTitle>Configuration</CardTitle>
        <CardDescription>
          {descriptor?.label ?? service.driver}: {descriptor?.help}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <DescriptorDetails
          fields={descriptor?.fields ?? []}
          values={service.config}
        />
        <div className="text-sm">
          <p>
            Readiness:{" "}
            {service.ready.tcp ??
              service.ready.http?.url ??
              "Driver reports running"}
          </p>
          {service.ready.http && (
            <p>Expected HTTP status: {service.ready.http.status}</p>
          )}
          <p>
            Start timeout:{" "}
            {formatDurationSeconds(service.start_timeout_seconds)}
          </p>
        </div>
        <div>
          <h3 className="text-sm font-medium">Requires</h3>
          {service.requires.length === 0 && (
            <p className="text-sm text-muted-foreground">None</p>
          )}
          {service.requires.map((id) => (
            <Link
              key={id}
              className="block text-sm underline"
              to={`/services/${encodeURIComponent(id)}`}
            >
              {names.get(id) ?? id}
            </Link>
          ))}
        </div>
        <div>
          <h3 className="text-sm font-medium">Required by</h3>
          {service.required_by.length === 0 && (
            <p className="text-sm text-muted-foreground">None</p>
          )}
          {service.required_by.map((id) => (
            <Link
              key={id}
              className="block text-sm underline"
              to={`/services/${encodeURIComponent(id)}`}
            >
              {names.get(id) ?? id}
            </Link>
          ))}
        </div>
      </CardContent>
    </Card>
  );
}
