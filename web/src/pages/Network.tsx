import { useEffect, useState } from "react";
import { Maximize2, Minimize2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { PageHeader } from "@/components/common/PageHeader";
import { TopologyMap } from "@/components/network/TopologyMap";

export function Network() {
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    function onKey(event: KeyboardEvent): void {
      if (event.key === "Escape") setExpanded(false);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  return (
    <div
      className={
        expanded
          ? "fixed inset-0 z-50 flex min-h-0 min-w-0 flex-col overflow-hidden bg-background p-4"
          : "flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"
      }
    >
      <div className="shrink-0">
        <PageHeader
          title="Network"
          description="How connections reach endpoints and services, including which services require others. Select a node to open it."
          actions={
            <Button
              variant="outline"
              size="icon"
              aria-label={expanded ? "Exit full screen" : "Expand Network"}
              onClick={() => setExpanded((value) => !value)}
            >
              {expanded ? (
                <Minimize2 className="h-4 w-4" />
              ) : (
                <Maximize2 className="h-4 w-4" />
              )}
            </Button>
          }
        />
      </div>
      <TopologyMap />
    </div>
  );
}
