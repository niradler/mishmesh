import { PageHeader } from "@/components/common/PageHeader";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TopologyMap } from "@/components/network/TopologyMap";
import { FrontDoorsPanel } from "@/components/network/FrontDoorsPanel";
import { useTabParam } from "@/hooks/use-tab-param";

const TABS = ["map", "frontdoors"] as const;

export function Network() {
  const [tab, setTab] = useTabParam(TABS, "map");

  return (
    <div>
      <PageHeader
        title="Network"
        description="How traffic reaches your services, and the front doors that publish it on the public internet."
      />
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="map">Map</TabsTrigger>
          <TabsTrigger value="frontdoors">Front doors</TabsTrigger>
        </TabsList>
        <TabsContent value="map">
          <TopologyMap />
        </TabsContent>
        <TabsContent value="frontdoors">
          <FrontDoorsPanel />
        </TabsContent>
      </Tabs>
    </div>
  );
}
