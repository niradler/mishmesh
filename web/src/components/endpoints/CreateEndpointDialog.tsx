import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { AccessRows } from "@/components/endpoints/AccessSection";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { FormSection } from "@/components/common/FormSection";
import { FormError } from "@/components/common/ConfirmDialog";
import { HelpTip, LabelWithHelp } from "@/components/common/HelpTip";
import {
  TargetsField,
  UpstreamAdvancedFields,
  cleanTargets,
  hasHttpsTarget,
  emptyUpstreamForm,
  upstreamAdvancedSummary,
  upstreamPolicyFields,
  validateTargets,
  type UpstreamForm,
} from "@/components/common/UpstreamFields";
import { useAgents, useCreateEndpoint, useServices } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { HELP, KIND_HELP, KIND_LABELS, METHOD_HELP } from "@/lib/glossary";
import type {
  AccessRef,
  CreateEndpointRequest,
  EndpointKind,
  EndpointPolicy,
} from "@/api/types";

export type CreateMethod = "native" | "proxy";

export interface EndpointPrefill {
  method?: CreateMethod;
  domain?: string;
  target?: string;
  serviceId?: string;
  connectionId?: string;
  hostname?: string;
}

const NO_SERVICE = "__none__";

function ChoiceButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <Button
      type="button"
      variant={active ? "default" : "outline"}
      size="sm"
      onClick={onClick}
      aria-pressed={active}
    >
      {children}
    </Button>
  );
}

export function CreateEndpointDialog({
  open,
  onOpenChange,
  prefill,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  prefill?: EndpointPrefill;
}) {
  const navigate = useNavigate();
  const { currentOrgId } = useSession();
  const agents = useAgents(currentOrgId);
  const services = useServices(currentOrgId);
  const create = useCreateEndpoint();
  const [method, setMethod] = useState<CreateMethod>("proxy");
  const [agentId, setAgentId] = useState("");
  const [kind, setKind] = useState<EndpointKind>("http");
  const [subdomain, setSubdomain] = useState("");
  const [domain, setDomain] = useState("");
  const [port, setPort] = useState("");
  const [upstream, setUpstream] = useState<UpstreamForm>(emptyUpstreamForm);
  const [serviceId, setServiceId] = useState("");
  const [access, setAccess] = useState<AccessRef[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [targetsProblem, setTargetsProblem] = useState<string | null>(null);

  useEffect(() => {
    if (!open) return;
    setMethod(prefill?.method ?? "proxy");
    setAgentId("");
    setKind("http");
    setSubdomain("");
    setDomain(prefill?.domain ?? "");
    setPort("");
    setUpstream(
      prefill?.target
        ? { ...emptyUpstreamForm(), targets: [prefill.target] }
        : emptyUpstreamForm(),
    );
    setServiceId(prefill?.serviceId ?? "");
    setAccess(
      prefill?.connectionId
        ? [
            {
              connection_id: prefill.connectionId,
              hostname: prefill.hostname ?? "",
            },
          ]
        : [],
    );
    setProblem(null);
    setTargetsProblem(null);
    create.reset();
  }, [
    open,
    prefill?.method,
    prefill?.domain,
    prefill?.target,
    prefill?.serviceId,
    prefill?.connectionId,
    prefill?.hostname,
  ]);

  const isProxy = method === "proxy";
  const effectiveKind: EndpointKind = isProxy ? "http" : kind;
  const isHttp = effectiveKind === "http";
  const showAdvanced =
    isProxy && (cleanTargets(upstream).length > 1 || hasHttpsTarget(upstream));
  const serviceList = services.data ?? [];
  const agentList = agents.data ?? [];

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setProblem(null);
    if (!isProxy && !agentId) {
      setProblem("Pick the agent that serves this endpoint.");
      return;
    }
    const payload: CreateEndpointRequest = isProxy
      ? { kind: "http", method: "proxy" }
      : { agent_id: agentId, kind, method: "native" };
    if (isHttp) {
      if (subdomain.trim()) payload.subdomain = subdomain.trim().toLowerCase();
      if (domain.trim()) payload.domain = domain.trim().toLowerCase();
    } else if (kind === "tcp" || kind === "udp") {
      if (port.trim()) {
        const value = Number(port);
        if (!Number.isInteger(value) || value < 1 || value > 65535) {
          setProblem("Port must be between 1 and 65535.");
          return;
        }
        payload.port = value;
      }
    } else if (domain.trim()) {
      payload.domain = domain.trim().toLowerCase();
    }
    const policy: EndpointPolicy = {};
    if (isProxy) {
      const invalid = validateTargets(upstream);
      if (invalid) {
        setTargetsProblem(invalid);
        return;
      }
      Object.assign(policy, upstreamPolicyFields(upstream));
    }
    if (isHttp && serviceId) policy.service_id = serviceId;
    if (access.length > 0) policy.access = access;
    if (Object.keys(policy).length > 0) payload.policy = policy;
    create.mutate(payload, {
      onSuccess: (endpoint) => {
        toast({ title: "Endpoint reserved" });
        onOpenChange(false);
        navigate(`/endpoints/${endpoint.id}`);
      },
    });
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={onSubmit} className="space-y-4">
          <DialogHeader>
            <DialogTitle>Reserve endpoint</DialogTitle>
            <DialogDescription>
              A stable address that routes to your service. It survives restarts
              and disconnects.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-1.5">
            <Label>Method</Label>
            <div className="flex flex-wrap items-center gap-3">
              <span className="inline-flex items-center gap-1">
                <ChoiceButton
                  active={isProxy}
                  onClick={() => setMethod("proxy")}
                >
                  Proxy
                </ChoiceButton>
                <HelpTip entry={METHOD_HELP.proxy} />
              </span>
              <span className="inline-flex items-center gap-1">
                <ChoiceButton
                  active={!isProxy}
                  onClick={() => setMethod("native")}
                >
                  Agent
                </ChoiceButton>
                <HelpTip entry={METHOD_HELP.native} />
              </span>
            </div>
          </div>

          {!isProxy && (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label>Agent</Label>
                <Select value={agentId} onValueChange={setAgentId}>
                  <SelectTrigger>
                    <SelectValue
                      placeholder={
                        agents.data && agentList.length === 0
                          ? "Create an agent first"
                          : "Select an agent"
                      }
                    />
                  </SelectTrigger>
                  <SelectContent>
                    {agentList.map((agent) => (
                      <SelectItem key={agent.id} value={agent.id}>
                        {agent.name}
                        {agent.connected ? "" : " (offline)"}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <LabelWithHelp entry={KIND_HELP[kind]}>Kind</LabelWithHelp>
                <Select
                  value={kind}
                  onValueChange={(v) => setKind(v as EndpointKind)}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(Object.keys(KIND_LABELS) as EndpointKind[]).map(
                      (value) => (
                        <SelectItem key={value} value={value}>
                          {KIND_LABELS[value]}
                        </SelectItem>
                      ),
                    )}
                  </SelectContent>
                </Select>
              </div>
            </div>
          )}

          {isHttp && (
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <LabelWithHelp htmlFor="new-subdomain" entry={HELP.subdomain}>
                  Subdomain
                </LabelWithHelp>
                <Input
                  id="new-subdomain"
                  value={subdomain}
                  onChange={(e) => setSubdomain(e.target.value)}
                  placeholder="api"
                />
              </div>
              <div className="space-y-1.5">
                <LabelWithHelp htmlFor="new-domain" entry={HELP.domain}>
                  Custom domain
                </LabelWithHelp>
                <Input
                  id="new-domain"
                  value={domain}
                  onChange={(e) => setDomain(e.target.value)}
                  placeholder="optional"
                />
              </div>
            </div>
          )}
          {!isProxy && (kind === "tcp" || kind === "udp") && (
            <div className="space-y-1.5">
              <LabelWithHelp htmlFor="new-port" entry={HELP.port}>
                Port
              </LabelWithHelp>
              <Input
                id="new-port"
                type="number"
                value={port}
                onChange={(e) => setPort(e.target.value)}
                placeholder="auto-assign if empty"
              />
            </div>
          )}
          {!isProxy && kind === "tls" && (
            <div className="space-y-1.5">
              <LabelWithHelp htmlFor="new-tls-domain" entry={HELP.domain}>
                Domain
              </LabelWithHelp>
              <Input
                id="new-tls-domain"
                value={domain}
                onChange={(e) => setDomain(e.target.value)}
                placeholder="app.example.com"
              />
            </div>
          )}

          {isProxy && (
            <>
              <TargetsField
                form={upstream}
                onChange={(next) => {
                  setUpstream(next);
                  setTargetsProblem(null);
                }}
                error={targetsProblem}
              />
              {showAdvanced && (
                <FormSection
                  title="Load balancing & origin TLS"
                  summary={upstreamAdvancedSummary(upstream)}
                >
                  <UpstreamAdvancedFields
                    form={upstream}
                    onChange={setUpstream}
                  />
                </FormSection>
              )}
            </>
          )}

          {isHttp && (serviceList.length > 0 || serviceId) && (
            <div className="space-y-1.5">
              <LabelWithHelp entry={HELP.serviceLink}>
                Wake a service (optional)
              </LabelWithHelp>
              <Select
                value={serviceId || NO_SERVICE}
                onValueChange={(v) => setServiceId(v === NO_SERVICE ? "" : v)}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_SERVICE}>None</SelectItem>
                  {serviceId &&
                    !serviceList.some((s) => s.id === serviceId) && (
                      <SelectItem value={serviceId}>{serviceId}</SelectItem>
                    )}
                  {serviceList.map((service) => (
                    <SelectItem key={service.id} value={service.id}>
                      {service.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <AccessRows kind={effectiveKind} refs={access} onChange={setAccess} />

          <FormError
            message={
              problem ?? (create.error ? errorMessage(create.error) : undefined)
            }
          />

          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={create.isPending}>
              {create.isPending ? "Reserving…" : "Reserve"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
