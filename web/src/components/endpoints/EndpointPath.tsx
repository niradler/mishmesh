import { Fragment, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { ChevronRight, Cloud } from "lucide-react";
import { StateDot } from "@/components/common/StatusDot";
import type { Agent, Endpoint, Service } from "@/api/types";

export function EndpointPath({
  endpoint,
  agents,
  services,
}: {
  endpoint: Endpoint;
  agents: Map<string, Agent>;
  services: Map<string, Service>;
}) {
  const steps: ReactNode[] = [];
  if (endpoint.policy?.public_access === "cloudflare") {
    steps.push(
      <Link key="cf" to="/cloudflare" className="text-muted-foreground hover:text-foreground" aria-label="Published via Cloudflare" title="Published via Cloudflare">
        <Cloud className="h-3.5 w-3.5" />
      </Link>,
    );
  }
  if (endpoint.method === "proxy") {
    const targets = endpoint.policy?.proxy_targets ?? [];
    steps.push(
      <span key="targets" className="font-mono" title={targets.join("\n")}>
        {targets.length === 1 ? targets[0] : `${targets.length} targets`}
      </span>,
    );
  } else if (endpoint.agent_id) {
    const agent = agents.get(endpoint.agent_id);
    steps.push(
      <Link key="agent" to={`/agents/${endpoint.agent_id}`} className="hover:underline">
        {agent?.name ?? "agent"}
      </Link>,
    );
  } else if (endpoint.method === "ssh") {
    steps.push(<span key="ssh">ssh session</span>);
  }
  const serviceId = endpoint.policy?.service_id;
  if (serviceId) {
    const service = services.get(serviceId);
    steps.push(
      <Link key="service" to={`/services/${serviceId}`} className="inline-flex items-center gap-1 hover:underline">
        {service ? <StateDot state={service.status.state} /> : null}
        {service?.name ?? endpoint.health?.service ?? "service"}
      </Link>,
    );
  }
  if (steps.length === 0) return <span className="text-xs text-muted-foreground">none</span>;
  return (
    <span className="inline-flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
      {steps.map((step, index) => (
        <Fragment key={index}>
          {index > 0 && <ChevronRight className="h-3 w-3 shrink-0" aria-hidden="true" />}
          <span className="text-foreground">{step}</span>
        </Fragment>
      ))}
    </span>
  );
}
