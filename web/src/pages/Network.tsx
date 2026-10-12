import { PageHeader } from "@/components/common/PageHeader";
import { TopologyMap } from "@/components/network/TopologyMap";

export function Network() {
  return (
    <div>
      <PageHeader
        title="Network"
        description="How traffic reaches your services, from the Cloudflare tunnel to each endpoint and what it serves. Select a node to open it."
      />
      <TopologyMap />
    </div>
  );
}
