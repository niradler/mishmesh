import { Badge } from "@/components/ui/badge";
import { HelpTip } from "@/components/common/HelpTip";
import { StateBadge } from "@/components/common/StateBadge";
import { StateDot } from "@/components/common/StatusDot";
import { useServiceDrivers } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import {
  CLOUDFLARE_LEVEL_HELP,
  CONNECTOR_HELP,
  DRIVER_HELP,
  KIND_HELP,
  KIND_LABELS,
  LIFECYCLE_HELP,
  LIFECYCLE_LABELS,
  METHOD_HELP,
  METHOD_LABELS,
  stateHelp,
  type GlossaryEntry,
} from "@/lib/glossary";
import { cn } from "@/lib/utils";
import type {
  CloudflareConnector,
  CloudflareLevel,
  EndpointKind,
  EndpointLifecycle,
  EndpointMethod,
  ServiceDriver,
} from "@/api/types";

function TermBadge({
  label,
  entry,
  className,
}: {
  label: string;
  entry?: GlossaryEntry;
  className?: string;
}) {
  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      <Badge variant="muted">{label}</Badge>
      <HelpTip entry={entry} />
    </span>
  );
}

export function KindBadge({ kind }: { kind: EndpointKind }) {
  return (
    <TermBadge label={KIND_LABELS[kind] ?? kind} entry={KIND_HELP[kind]} />
  );
}

export function MethodBadge({ method }: { method?: EndpointMethod }) {
  const value = method ?? "native";
  return (
    <TermBadge
      label={METHOD_LABELS[value] ?? value}
      entry={METHOD_HELP[value]}
    />
  );
}

export function LifecycleBadge({
  lifecycle,
}: {
  lifecycle: EndpointLifecycle;
}) {
  return (
    <TermBadge
      label={LIFECYCLE_LABELS[lifecycle] ?? lifecycle}
      entry={LIFECYCLE_HELP[lifecycle]}
    />
  );
}

export function DriverBadge({ driver }: { driver: ServiceDriver }) {
  const { currentOrgId } = useSession();
  const descriptors = useServiceDrivers(currentOrgId);
  return (
    <TermBadge
      label={
        descriptors.data?.find((descriptor) => descriptor.type === driver)
          ?.label ?? driver
      }
      entry={DRIVER_HELP[driver]}
    />
  );
}

export function ConnectorBadge({
  connector,
}: {
  connector: CloudflareConnector;
}) {
  return <TermBadge label={connector} entry={CONNECTOR_HELP[connector]} />;
}

export function CloudflareLevelBadge({ level }: { level: CloudflareLevel }) {
  const label = level === "none" ? "not connected" : level;
  return (
    <span className="inline-flex items-center gap-1">
      <Badge
        variant={level === "managed" ? "success" : "outline"}
        className="capitalize"
      >
        {label}
      </Badge>
      <HelpTip entry={CLOUDFLARE_LEVEL_HELP[level]} />
    </span>
  );
}

type StateScope = "endpoint" | "service" | "hostname";

export function HelpedStateBadge({
  scope,
  state,
  label,
}: {
  scope: StateScope;
  state: string;
  label?: string;
}) {
  return (
    <span className="inline-flex items-center gap-1">
      <StateBadge state={state} label={label} />
      <HelpTip entry={stateHelp(scope, state)} />
    </span>
  );
}

export function HelpedStateDot({
  scope,
  state,
  label,
}: {
  scope: StateScope;
  state: string;
  label?: string;
}) {
  return (
    <span className="inline-flex items-center gap-1">
      <StateDot state={state} label={label ?? state} />
      <HelpTip entry={stateHelp(scope, state)} />
    </span>
  );
}
