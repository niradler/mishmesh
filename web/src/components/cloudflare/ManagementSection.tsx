import { useEffect, useState, type FormEvent } from "react";
import { KeyRound } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { FormSection } from "@/components/common/FormSection";
import { ConfirmDialog, FormError } from "@/components/common/ConfirmDialog";
import { HelpTip, LabelWithHelp } from "@/components/common/HelpTip";
import { useSaveCloudflare } from "@/api/hooks";
import { errorMessage } from "@/api/client";
import { toast } from "@/hooks/use-toast";
import { HELP } from "@/lib/glossary";
import { inputFromConnection } from "@/lib/cloudflare";
import type { CloudflareConnection, CloudflareLevel } from "@/api/types";

const TOKEN_PERMISSIONS = [
  "Account · Cloudflare Tunnel · Edit",
  "Zone · DNS · Edit",
  "Account · Access: Apps and Policies · Edit",
];

export function ManagementSection({ connection, level }: { connection: CloudflareConnection; level: CloudflareLevel }) {
  const save = useSaveCloudflare();
  const remove = useSaveCloudflare();
  const [apiToken, setApiToken] = useState("");
  const [accessAppId, setAccessAppId] = useState(connection.access_app_id ?? "");
  const [prune, setPrune] = useState(connection.prune);
  const [problem, setProblem] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const managed = level === "managed";

  useEffect(() => {
    setAccessAppId(connection.access_app_id ?? "");
    setPrune(connection.prune);
  }, [connection.access_app_id, connection.prune]);

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setProblem(null);
    if (!connection.api_token_set && !apiToken.trim()) {
      setProblem("Paste an API token to enable management.");
      return;
    }
    save.mutate(
      inputFromConnection(connection, {
        api_token: apiToken.trim() || undefined,
        access_app_id: accessAppId.trim(),
        prune,
      }),
      {
        onSuccess: () => {
          setApiToken("");
          toast({ title: connection.api_token_set ? "Management settings saved" : "Management enabled" });
        },
      },
    );
  };

  const onRemoveToken = () =>
    remove.mutate(inputFromConnection(connection, { clear_api_token: true }), {
      onSuccess: () => {
        setConfirmRemove(false);
        toast({ title: "API token removed", description: "The connection is back to the imported level." });
      },
    });

  return (
    <FormSection
      title={managed ? "Management" : "Enable management"}
      summary={managed ? "API token set" : "imported only"}
      defaultOpen={!managed}
    >
      <form onSubmit={onSubmit} className="space-y-4">
        <p className="text-sm text-muted-foreground">
          With an API token mishmesh creates the tunnel ingress rules, DNS records and Access hostnames for published endpoints.
        </p>
        <div className="space-y-1.5">
          <LabelWithHelp htmlFor="cf-api-token" entry={HELP.apiToken}>
            API token
          </LabelWithHelp>
          <Input
            id="cf-api-token"
            type="password"
            value={apiToken}
            onChange={(e) => setApiToken(e.target.value)}
            placeholder={connection.api_token_set ? "unchanged" : ""}
            autoComplete="off"
          />
          <div className="text-xs text-muted-foreground">
            <p>Create it under My Profile → API Tokens with these permissions:</p>
            <ul className="mt-1 list-inside list-disc font-mono">
              {TOKEN_PERMISSIONS.map((permission) => (
                <li key={permission}>{permission}</li>
              ))}
            </ul>
          </div>
        </div>
        <div className="space-y-1.5">
          <LabelWithHelp htmlFor="cf-access-app" entry={HELP.accessApp}>
            Access application ID (optional)
          </LabelWithHelp>
          <Input id="cf-access-app" value={accessAppId} onChange={(e) => setAccessAppId(e.target.value)} autoComplete="off" />
        </div>
        <div className="flex items-center justify-between gap-3 rounded-md border border-border p-3">
          <div>
            <p className="flex items-center gap-1 text-sm font-medium">
              Prune unclaimed hostnames
              <HelpTip entry={HELP.prune} />
            </p>
            <p className="text-xs text-muted-foreground">
              Remove tunnel hostnames that no endpoint claims. Leave off if this tunnel also serves things mishmesh does not know about.
            </p>
          </div>
          <Switch checked={prune} onCheckedChange={setPrune} />
        </div>
        <FormError message={problem ?? (save.error ? errorMessage(save.error) : undefined)} />
        <div className="flex flex-wrap justify-end gap-2">
          {connection.api_token_set && (
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                remove.reset();
                setConfirmRemove(true);
              }}
            >
              Remove API token
            </Button>
          )}
          <Button type="submit" disabled={save.isPending}>
            <KeyRound className="h-4 w-4" />
            {save.isPending ? "Saving…" : connection.api_token_set ? "Save" : "Enable management"}
          </Button>
        </div>
      </form>
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title="Remove API token?"
        description="mishmesh stops changing Cloudflare and returns to the imported level. Existing rules and records stay as they are."
        confirmLabel="Remove token"
        pendingLabel="Removing…"
        pending={remove.isPending}
        error={remove.error ? errorMessage(remove.error) : undefined}
        onConfirm={onRemoveToken}
      />
    </FormSection>
  );
}
