import { Link } from "react-router-dom";
import { Bot, Crosshair, Pencil, Waypoints } from "lucide-react";
import { Button } from "@/components/ui/button";
import { StateDot, toneForState } from "@/components/common/StatusDot";
import { HelpedStateBadge, KindBadge, LifecycleBadge, MethodBadge } from "@/components/common/TermBadges";
import { HelpTip } from "@/components/common/HelpTip";
import { PipelineCard } from "@/components/endpoints/PipelineCard";
import { useAgent } from "@/api/hooks";
import { HELP } from "@/lib/glossary";
import { endpointHealthState, formatLatency } from "@/lib/health";
import { formatRelativeTime } from "@/lib/utils";
import type { Endpoint, TargetHealth } from "@/api/types";

export function EndpointStage({ endpoint }: { endpoint: Endpoint }) {
  const state = endpointHealthState(endpoint);
  const health = endpoint.health;
  return (
    <PipelineCard
      stage="Endpoint"
      icon={Waypoints}
      state={state}
      title={<span className="font-mono text-xs text-muted-foreground">{endpoint.id}</span>}
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <KindBadge kind={endpoint.kind} />
        <MethodBadge method={endpoint.method} />
        <LifecycleBadge lifecycle={endpoint.lifecycle} />
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <HelpedStateBadge scope="endpoint" state={state} />
        {health?.latency_ms !== undefined && <span className="text-xs text-muted-foreground">{formatLatency(health.latency_ms)}</span>}
      </div>
      {health?.detail && <p className="text-xs text-muted-foreground">{health.detail}</p>}
      {health?.checked_at && <p className="text-xs text-muted-foreground">Checked {formatRelativeTime(health.checked_at)}</p>}
    </PipelineCard>
  );
}

function targetsState(targets: TargetHealth[], fallback: string): string {
  if (targets.length === 0) return fallback;
  const healthy = targets.filter((t) => t.healthy).length;
  if (healthy === targets.length) return "up";
  return healthy === 0 ? "down" : "degraded";
}

export function TargetsStage({
  endpoint,
  canWrite,
  onEdit,
}: {
  endpoint: Endpoint;
  canWrite: boolean;
  onEdit: () => void;
}) {
  const targets = endpoint.policy?.proxy_targets ?? [];
  const health = endpoint.health?.targets ?? [];
  const healthByTarget = new Map(health.map((h) => [h.target, h]));
  const endpointState = endpointHealthState(endpoint);
  const sleepy = toneForState(endpointState) === "sleeping" || toneForState(endpointState) === "waking";
  const state = sleepy ? endpointState : targetsState(health, endpointState);
  const loadBalance = endpoint.policy?.load_balance === "failover" ? "Failover" : "Round robin";
  const originTls = endpoint.policy?.origin_tls;
  const tlsSummary = originTls?.insecure_skip_verify
    ? "verification skipped"
    : originTls?.pin_sha256?.length
      ? "pinned"
      : originTls?.ca_pem
        ? "custom CA"
        : null;

  return (
    <PipelineCard
      stage={targets.length === 1 ? "Target" : `Targets · ${targets.length}`}
      icon={Crosshair}
      state={state}
      actions={
        canWrite && (
          <Button type="button" size="sm" variant="outline" onClick={onEdit}>
            <Pencil className="h-3.5 w-3.5" /> Edit targets
          </Button>
        )
      }
    >
      <ul className="space-y-1.5">
        {targets.map((target) => {
          const result = healthByTarget.get(target);
          return (
            <li key={target} className="space-y-0.5">
              <div className="flex items-center justify-between gap-2">
                <StateDot
                  state={result ? (result.healthy ? "up" : "down") : sleepy ? endpointState : "unknown"}
                  label={target}
                  className="min-w-0 break-all font-mono text-xs"
                />
                {result && <span className="shrink-0 text-xs text-muted-foreground">{formatLatency(result.latency_ms)}</span>}
              </div>
              {result?.error && <p className="break-words pl-4 text-xs text-destructive">{result.error}</p>}
            </li>
          );
        })}
      </ul>
      {targets.length > 1 && (
        <p className="flex items-center gap-1 text-xs text-muted-foreground">
          {loadBalance}
          <HelpTip entry={HELP.loadBalancing} />
        </p>
      )}
      {tlsSummary && (
        <p className="flex items-center gap-1 text-xs text-muted-foreground">
          Origin TLS: {tlsSummary}
          <HelpTip entry={HELP.originTls} />
        </p>
      )}
    </PipelineCard>
  );
}

export function AgentStage({ endpoint }: { endpoint: Endpoint }) {
  const agent = useAgent(endpoint.agent_id);
  if (!endpoint.agent_id) {
    return (
      <PipelineCard stage={endpoint.method === "ssh" ? "SSH session" : "Agent"} icon={Bot} state={endpoint.online ? "up" : "offline"}>
        <StateDot state={endpoint.online ? "up" : "offline"} label={endpoint.online ? "connected" : "no session attached"} />
      </PipelineCard>
    );
  }
  const connected = agent.data?.connected ?? endpoint.online;
  return (
    <PipelineCard
      stage={endpoint.method === "ssh" ? "SSH session" : "Agent"}
      icon={Bot}
      state={connected ? "up" : "offline"}
      title={
        <Link to={`/agents/${endpoint.agent_id}`} className="hover:underline">
          {agent.data?.name ?? endpoint.agent_id}
        </Link>
      }
    >
      <StateDot state={connected ? "up" : "offline"} label={connected ? "connected" : "disconnected"} />
      {!connected && agent.data?.last_seen_at && (
        <p className="text-xs text-muted-foreground">Last seen {formatRelativeTime(agent.data.last_seen_at)}</p>
      )}
      {agent.data && agent.data.status !== "active" && <p className="text-xs text-destructive">Agent is {agent.data.status}</p>}
    </PipelineCard>
  );
}
