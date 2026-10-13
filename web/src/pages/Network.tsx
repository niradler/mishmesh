import { PageHeader } from "@/components/common/PageHeader";
import { TopologyMap } from "@/components/network/TopologyMap";

export function Network() {
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
      <div className="shrink-0">
        <PageHeader
          title="Network"
          description="How traffic reaches your services, from the Cloudflare tunnel to each endpoint and what it serves. Select a node to open it."
        />
      </div>
      <TopologyMap />
    </div>
  );
}
