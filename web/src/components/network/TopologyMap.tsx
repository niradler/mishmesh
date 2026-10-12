import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ComponentType } from "react";
import { Cloud, Globe, KeyRound, Network, Power, Server, Target } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { StateDot, TONE_SURFACE, toneForState } from "@/components/common/StatusDot";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { useTopology } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { cn } from "@/lib/utils";
import type { Topology, TopologyEdge, TopologyEdgeKind, TopologyNode, TopologyNodeKind } from "@/api/types";

interface NodeBox {
  x: number;
  y: number;
  width: number;
  height: number;
}

interface MeasuredLayout {
  boxes: Record<string, NodeBox>;
  width: number;
  height: number;
}

interface ColumnSpec {
  title: string;
  kinds: TopologyNodeKind[];
}

const COLUMNS: ColumnSpec[] = [
  { title: "Front door", kinds: ["frontdoor"] },
  { title: "Hostnames", kinds: ["hostname"] },
  { title: "Endpoints", kinds: ["endpoint"] },
  { title: "Upstreams", kinds: ["target", "service", "agent"] },
];

const KIND_ICONS: Record<TopologyNodeKind, ComponentType<{ className?: string }>> = {
  frontdoor: Cloud,
  hostname: Globe,
  endpoint: Network,
  target: Target,
  service: Power,
  agent: KeyRound,
};

const EDGE_DASH: Record<TopologyEdgeKind, string | undefined> = {
  routes: undefined,
  serves: undefined,
  proxies: undefined,
  tunnels: "7 4",
  wakes: "2 4",
};

const EDGE_LEGEND: { kind: TopologyEdgeKind; description: string }[] = [
  { kind: "routes", description: "front door routes to hostname" },
  { kind: "serves", description: "hostname serves endpoint" },
  { kind: "proxies", description: "endpoint proxies to target" },
  { kind: "tunnels", description: "endpoint tunnels to agent" },
  { kind: "wakes", description: "endpoint wakes service" },
];

const EMPTY_LAYOUT: MeasuredLayout = { boxes: {}, width: 0, height: 0 };

function edgePath(from: NodeBox, to: NodeBox): string {
  const startX = from.x + from.width;
  const startY = from.y + from.height / 2;
  const endX = to.x;
  const endY = to.y + to.height / 2;
  const curve = Math.max(40, Math.abs(endX - startX) / 2);
  return `M ${startX} ${startY} C ${startX + curve} ${startY}, ${endX - curve} ${endY}, ${endX} ${endY}`;
}

function NodeCard({
  node,
  register,
}: {
  node: TopologyNode;
  register: (id: string, element: HTMLDivElement | null) => void;
}) {
  const Icon = KIND_ICONS[node.kind];
  const tone = toneForState(node.state);
  return (
    <div
      ref={(element) => {
        register(node.id, element);
      }}
      title={node.detail ? `${node.label}: ${node.detail}` : node.label}
      className={cn("relative z-10 w-56 rounded-lg border p-3 shadow-sm", TONE_SURFACE[tone])}
    >
      <div className="flex items-center gap-2">
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="truncate text-sm font-medium">{node.label}</span>
      </div>
      <div className="mt-1.5 flex items-center justify-between gap-2">
        <StateDot state={node.state} label={node.state} className="text-xs" />
        <span className="text-[10px] uppercase tracking-wide text-muted-foreground">{node.kind}</span>
      </div>
      {node.detail && <p className="mt-1.5 line-clamp-2 break-words text-xs text-muted-foreground">{node.detail}</p>}
    </div>
  );
}

function TopologyGraph({ topology }: { topology: Topology }) {
  const containerRef = useRef<HTMLDivElement>(null);
  const nodeElements = useRef(new Map<string, HTMLDivElement>());
  const [layout, setLayout] = useState<MeasuredLayout>(EMPTY_LAYOUT);

  const nodes = useMemo(() => topology.nodes ?? [], [topology.nodes]);
  const edges = useMemo(() => topology.edges ?? [], [topology.edges]);

  const columns = useMemo(
    () =>
      COLUMNS.map((spec) => ({
        title: spec.title,
        nodes: nodes
          .filter((node) => spec.kinds.includes(node.kind))
          .sort((a, b) => spec.kinds.indexOf(a.kind) - spec.kinds.indexOf(b.kind) || a.label.localeCompare(b.label)),
      })).filter((column) => column.nodes.length > 0),
    [nodes],
  );

  const register = useCallback((id: string, element: HTMLDivElement | null) => {
    if (element) nodeElements.current.set(id, element);
    else nodeElements.current.delete(id);
  }, []);

  const measure = useCallback(() => {
    const container = containerRef.current;
    if (!container) return;
    const origin = container.getBoundingClientRect();
    const boxes: Record<string, NodeBox> = {};
    nodeElements.current.forEach((element, id) => {
      const rect = element.getBoundingClientRect();
      boxes[id] = { x: rect.left - origin.left, y: rect.top - origin.top, width: rect.width, height: rect.height };
    });
    setLayout({ boxes, width: origin.width, height: origin.height });
  }, []);

  useLayoutEffect(measure, [measure, columns]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, [measure]);

  const drawableEdges = edges.filter(
    (edge: TopologyEdge) => layout.boxes[edge.from] !== undefined && layout.boxes[edge.to] !== undefined,
  );

  return (
    <div className="space-y-4">
      <div className="overflow-x-auto rounded-md border border-border bg-background/50">
        <div ref={containerRef} className="relative flex min-w-max gap-24 p-6">
          <svg
            className="pointer-events-none absolute left-0 top-0 z-0 text-muted-foreground/70"
            width={layout.width}
            height={layout.height}
            aria-hidden="true"
          >
            <defs>
              <marker id="topology-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
                <path d="M 0 0 L 8 4 L 0 8 z" fill="currentColor" />
              </marker>
            </defs>
            {drawableEdges.map((edge) => (
              <path
                key={`${edge.kind}-${edge.from}-${edge.to}`}
                d={edgePath(layout.boxes[edge.from], layout.boxes[edge.to])}
                fill="none"
                stroke="currentColor"
                strokeWidth={1.5}
                strokeDasharray={EDGE_DASH[edge.kind]}
                markerEnd="url(#topology-arrow)"
              />
            ))}
          </svg>
          {columns.map((column) => (
            <div key={column.title} className="flex shrink-0 flex-col gap-4">
              <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{column.title}</p>
              {column.nodes.map((node) => (
                <NodeCard key={node.id} node={node} register={register} />
              ))}
            </div>
          ))}
        </div>
      </div>
      <div className="flex flex-wrap gap-x-6 gap-y-2 text-xs text-muted-foreground">
        {EDGE_LEGEND.map(({ kind, description }) => (
          <span key={kind} className="inline-flex items-center gap-2">
            <svg width="28" height="6" aria-hidden="true" className="text-muted-foreground/70">
              <line x1="0" y1="3" x2="28" y2="3" stroke="currentColor" strokeWidth={1.5} strokeDasharray={EDGE_DASH[kind]} />
            </svg>
            <span>
              <span className="font-medium text-foreground">{kind}</span> {description}
            </span>
          </span>
        ))}
      </div>
    </div>
  );
}

export function TopologyMap() {
  const { currentOrgId } = useSession();
  const topology = useTopology(currentOrgId);
  const hasNodes = (topology.data?.nodes ?? []).length > 0;

  return (
    <Card>
      <CardContent className="pt-4">
        {topology.isLoading ? (
          <LoadingState label="Loading topology" />
        ) : topology.isError ? (
          <ErrorState error={topology.error} />
        ) : !topology.data || !hasNodes ? (
          <EmptyState
            icon={<Server className="h-5 w-5" />}
            title="Nothing to show yet"
            description="Reserve an endpoint to see how traffic reaches your services."
          />
        ) : (
          <TopologyGraph topology={topology.data} />
        )}
      </CardContent>
    </Card>
  );
}
