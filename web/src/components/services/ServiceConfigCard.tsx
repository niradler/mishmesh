import type { ReactNode } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { HelpTip } from "@/components/common/HelpTip";
import { DRIVER_HELP, DRIVER_LABELS, HELP, type GlossaryEntry } from "@/lib/glossary";
import { formatDurationSeconds } from "@/lib/health";
import type { Service } from "@/api/types";

function Row({ label, help, children }: { label: string; help?: GlossaryEntry; children: ReactNode }) {
  return (
    <div className="grid gap-1 py-2 sm:grid-cols-[12rem_1fr] sm:gap-4">
      <dt className="flex items-center gap-1 text-sm text-muted-foreground">
        {label}
        <HelpTip entry={help} />
      </dt>
      <dd className="min-w-0 break-words text-sm">{children}</dd>
    </div>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-xs">{children}</span>;
}

function Argv({ argv }: { argv?: string[] }) {
  if (!argv || argv.length === 0) return <span className="text-muted-foreground">not set</span>;
  return <Mono>{argv.join(" ")}</Mono>;
}

function DriverRows({ service }: { service: Service }) {
  const { spec } = service;
  if (service.driver === "docker") {
    return spec.docker?.containers?.length ? (
      <Row label="Containers" help={HELP.dockerSelector}>
        <Mono>{spec.docker.containers.join(", ")}</Mono>
      </Row>
    ) : (
      <Row label="Label" help={HELP.dockerSelector}>
        <Mono>{spec.docker?.label ?? "not set"}</Mono>
      </Row>
    );
  }
  if (service.driver === "compose") {
    return (
      <>
        <Row label="Project">
          <Mono>{spec.compose?.project}</Mono>
        </Row>
        <Row label="Compose services">
          {spec.compose?.services?.length ? <Mono>{spec.compose.services.join(", ")}</Mono> : "all"}
        </Row>
        {spec.compose?.file && (
          <Row label="Compose file">
            <Mono>{spec.compose.file}</Mono>
          </Row>
        )}
        {spec.compose?.bin && (
          <Row label="Binary">
            <Mono>{spec.compose.bin}</Mono>
          </Row>
        )}
      </>
    );
  }
  const env = Object.entries(spec.exec?.env ?? {});
  return (
    <>
      <Row label="Start" help={HELP.execArgv}>
        <Argv argv={spec.exec?.start} />
      </Row>
      <Row label="Stop">
        <Argv argv={spec.exec?.stop} />
      </Row>
      <Row label="Status">
        <Argv argv={spec.exec?.status} />
      </Row>
      {spec.exec?.dir && (
        <Row label="Working directory">
          <Mono>{spec.exec.dir}</Mono>
        </Row>
      )}
      {env.length > 0 && (
        <Row label="Environment">
          <span className="flex flex-col gap-0.5">
            {env.map(([key, value]) => (
              <Mono key={key}>
                {key}={value}
              </Mono>
            ))}
          </span>
        </Row>
      )}
    </>
  );
}

export function ServiceConfigCard({ service }: { service: Service }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Configuration</CardTitle>
        <CardDescription>How mishmesh controls this service and decides it is ready. Use Edit to change it.</CardDescription>
      </CardHeader>
      <CardContent>
        <dl className="divide-y divide-border">
          <Row label="Driver" help={DRIVER_HELP[service.driver]}>
            {DRIVER_LABELS[service.driver] ?? service.driver}
          </Row>
          <DriverRows service={service} />
          <Row label="Readiness address" help={HELP.readyTcp}>
            {service.spec.ready_tcp ? <Mono>{service.spec.ready_tcp}</Mono> : <span className="text-muted-foreground">not set</span>}
          </Row>
          <Row label="Start timeout" help={HELP.startTimeout}>
            {formatDurationSeconds(service.start_timeout_seconds)}
          </Row>
        </dl>
      </CardContent>
    </Card>
  );
}
