import { useState } from "react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ConfirmDialog, FormError } from "@/components/common/ConfirmDialog";
import { HelpTip } from "@/components/common/HelpTip";
import {
  useAddConnectionHostname,
  useRemoveConnectionHostname,
  useUpdateEndpoint,
} from "@/api/hooks";
import { ApiError, errorMessage } from "@/api/client";
import { useSession } from "@/context/SessionContext";
import { HELP } from "@/lib/glossary";
import { newEndpointLink } from "@/lib/endpoints";
import type { AccessRef, Endpoint } from "@/api/types";

type Target = "endpoint" | "service";

export function AddHostnameDialog({
  open,
  onOpenChange,
  connectionId,
  endpoints,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connectionId: string;
  endpoints: Endpoint[];
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const addRoute = useAddConnectionHostname(currentOrgId);
  const updateEndpoint = useUpdateEndpoint();
  const [hostname, setHostname] = useState("");
  const [target, setTarget] = useState<Target>("endpoint");
  const [endpointId, setEndpointId] = useState("");
  const [service, setService] = useState("");
  const choices = endpoints.filter(
    (endpoint) =>
      endpoint.kind === "http" &&
      !endpoint.policy?.access?.some(
        (ref) => ref.connection_id === connectionId,
      ),
  );
  const pending = addRoute.isPending || updateEndpoint.isPending;
  const error = addRoute.error ?? updateEndpoint.error;
  const host = hostname.trim();

  function close(): void {
    addRoute.reset();
    updateEndpoint.reset();
    setHostname("");
    setEndpointId("");
    setService("");
    setTarget("endpoint");
    onOpenChange(false);
  }

  function submit(event: React.FormEvent): void {
    event.preventDefault();
    if (target === "service") {
      addRoute.mutate(
        { id: connectionId, hostname: host, service: service.trim() },
        { onSuccess: close },
      );
      return;
    }
    const endpoint = choices.find((row) => row.id === endpointId);
    if (!endpoint) return;
    updateEndpoint.mutate(
      {
        id: endpoint.id,
        policy: {
          ...endpoint.policy,
          access: [
            ...(endpoint.policy?.access ?? []),
            { connection_id: connectionId, hostname: host },
          ],
        },
      },
      { onSuccess: close },
    );
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => (next ? onOpenChange(true) : close())}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add hostname</DialogTitle>
          <DialogDescription>
            Publish a hostname on this tunnel. mishmesh creates the tunnel route
            and a proxied DNS record, and never replaces existing records.
          </DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={submit}>
          <div className="space-y-1">
            <Label htmlFor="add-hostname">Hostname</Label>
            <Input
              id="add-hostname"
              required
              autoFocus
              placeholder="app.example.com"
              value={hostname}
              disabled={pending}
              onChange={(event) => setHostname(event.target.value)}
            />
          </div>
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Routes to</legend>
            <div className="flex flex-wrap gap-4 text-sm">
              <label className="inline-flex items-center gap-2">
                <input
                  type="radio"
                  name="hostname-target"
                  checked={target === "endpoint"}
                  disabled={pending}
                  onChange={() => setTarget("endpoint")}
                />
                A mishmesh endpoint
                <HelpTip entry={HELP.hostnameEndpoint} />
              </label>
              <label className="inline-flex items-center gap-2">
                <input
                  type="radio"
                  name="hostname-target"
                  checked={target === "service"}
                  disabled={pending}
                  onChange={() => setTarget("service")}
                />
                Another service URL
                <HelpTip entry={HELP.hostnameService} />
              </label>
            </div>
          </fieldset>
          {target === "endpoint" ? (
            <div className="space-y-1">
              <Label htmlFor="add-hostname-endpoint">Endpoint</Label>
              <select
                id="add-hostname-endpoint"
                required
                className="h-10 w-full rounded-md border bg-background px-3"
                value={endpointId}
                disabled={pending}
                onChange={(event) => setEndpointId(event.target.value)}
              >
                <option value="" disabled>
                  {choices.length
                    ? "Choose an HTTP endpoint"
                    : "No HTTP endpoint without this connection"}
                </option>
                {choices.map((endpoint) => (
                  <option key={endpoint.id} value={endpoint.id}>
                    {endpoint.domain}
                  </option>
                ))}
              </select>
              <Link
                className="inline-block text-xs underline"
                to={newEndpointLink({
                  method: "proxy",
                  domain: host || undefined,
                  connectionId,
                })}
              >
                + New endpoint
              </Link>
            </div>
          ) : (
            <div className="space-y-1">
              <Label htmlFor="add-hostname-service">Service URL</Label>
              <Input
                id="add-hostname-service"
                required
                placeholder="http://localhost:8000"
                value={service}
                disabled={pending}
                onChange={(event) => setService(event.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                http, https, tcp, ssh or rdp, as seen from the tunnel host.
              </p>
            </div>
          )}
          <FormError message={error ? errorMessage(error) : undefined} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? "Adding…" : "Add hostname"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

interface InUseBody {
  endpoint_ids?: string[];
}

function sameHost(a: string, b: string): boolean {
  const normalize = (host: string) =>
    host.trim().toLowerCase().replace(/\.$/, "");
  return normalize(a) === normalize(b);
}

export function RemoveHostnameDialog({
  host,
  onClose,
  connectionId,
  endpoints,
}: {
  host?: { host: string; endpoint_id?: string };
  onClose: () => void;
  connectionId: string;
  endpoints: Endpoint[];
}): React.JSX.Element {
  const { currentOrgId } = useSession();
  const removeRoute = useRemoveConnectionHostname(currentOrgId);
  const updateEndpoint = useUpdateEndpoint();
  const [inUse, setInUse] = useState<string[]>([]);
  const endpointIds = [
    ...new Set([...(host?.endpoint_id ? [host.endpoint_id] : []), ...inUse]),
  ];
  const publishes = (endpoint: Endpoint, ref: AccessRef): boolean =>
    ref.connection_id === connectionId &&
    sameHost(ref.hostname || endpoint.domain || "", host?.host ?? "");
  const linked = endpointIds
    .map((id) => endpoints.find((endpoint) => endpoint.id === id))
    .filter((endpoint): endpoint is Endpoint =>
      Boolean(
        endpoint?.policy?.access?.some((ref) => publishes(endpoint, ref)),
      ),
    );
  const error = removeRoute.error ?? updateEndpoint.error;
  const pending = removeRoute.isPending || updateEndpoint.isPending;

  function close(): void {
    removeRoute.reset();
    updateEndpoint.reset();
    setInUse([]);
    onClose();
  }

  async function confirm(): Promise<void> {
    if (!host) return;
    try {
      for (const endpoint of linked) {
        await updateEndpoint.mutateAsync({
          id: endpoint.id,
          policy: {
            ...endpoint.policy,
            access: (endpoint.policy?.access ?? []).filter(
              (ref) => !publishes(endpoint, ref),
            ),
          },
        });
      }
      await removeRoute.mutateAsync({ id: connectionId, hostname: host.host });
      close();
    } catch (err) {
      if (err instanceof ApiError && err.status === 404 && linked.length) {
        close();
        return;
      }
      if (err instanceof ApiError && err.status === 409) {
        setInUse((err.body as InUseBody)?.endpoint_ids ?? []);
      }
    }
  }

  return (
    <ConfirmDialog
      open={Boolean(host)}
      onOpenChange={(open) => !open && close()}
      title={`Delete ${host?.host ?? "hostname"}?`}
      pending={pending}
      error={error ? errorMessage(error) : undefined}
      onConfirm={() => void confirm()}
      description={
        <div className="space-y-2 text-sm">
          <p className="inline-flex items-center gap-1">
            This removes from Cloudflare:{" "}
            <HelpTip entry={HELP.hostnameDelete} />
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>The tunnel route for {host?.host}</li>
            <li>DNS records pointing at this tunnel; other records stay</li>
            <li>The Access app domain, only if mishmesh added it</li>
            {linked.map((endpoint) => (
              <li key={endpoint.id}>
                Public access on endpoint{" "}
                <Link
                  className="underline"
                  to={`/endpoints/${encodeURIComponent(endpoint.id)}`}
                >
                  {endpoint.domain}
                </Link>
              </li>
            ))}
          </ul>
        </div>
      }
    />
  );
}
