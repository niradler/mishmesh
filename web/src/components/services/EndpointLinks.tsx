import { Link } from "react-router-dom";
import { endpointDisplayName } from "@/lib/health";
import type { Endpoint } from "@/api/types";

export function EndpointLinks({ ids, endpointsById }: { ids: string[]; endpointsById: Map<string, Endpoint> }) {
  if (ids.length === 0) return <span className="text-xs text-muted-foreground">none</span>;
  return (
    <span className="flex flex-col gap-0.5">
      {ids.map((id) => {
        const endpoint = endpointsById.get(id);
        return (
          <Link key={id} to={`/endpoints/${id}`} className="font-mono text-xs hover:underline">
            {endpoint ? endpointDisplayName(endpoint) : id}
          </Link>
        );
      })}
    </span>
  );
}
