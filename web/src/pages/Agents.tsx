import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { Plus, ShieldCheck } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { CodeBlock } from "@/components/common/CodeBlock";
import { StatusDot } from "@/components/common/StatusDot";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAgents, useCreateAgent } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { formatRelativeTime } from "@/lib/utils";
import { ApiError } from "@/api/client";

function agentRunCommand(token: string): string {
  return `mishmesh-agent --token ${token} http 3000`;
}

function CreateAgentDialog({ orgId }: { orgId?: string }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [issuedToken, setIssuedToken] = useState<string | null>(null);
  const create = useCreateAgent(orgId);

  const reset = () => {
    setName("");
    setIssuedToken(null);
    create.reset();
  };

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate(
      { name: name.trim() || "agent" },
      {
        onSuccess: (res) => {
          setIssuedToken(res.token);
          toast({ title: "Agent created", description: `${res.agent.name} is ready.` });
        },
        onError: (err) =>
          toast({
            variant: "destructive",
            title: "Create failed",
            description: err instanceof ApiError ? err.message : "Unknown error",
          }),
      },
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) reset();
      }}
    >
      <Button onClick={() => setOpen(true)}>
        <Plus className="h-4 w-4" /> New agent
      </Button>
      <DialogContent>
        {issuedToken ? (
          <>
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                <ShieldCheck className="h-5 w-5 text-success" /> Agent token
              </DialogTitle>
              <DialogDescription>
                Copy this token now — it is shown only once and cannot be retrieved later.
              </DialogDescription>
            </DialogHeader>
            <div className="min-w-0 space-y-3">
              <div className="min-w-0 space-y-1.5">
                <Label>Authtoken</Label>
                <CodeBlock value={issuedToken} label="token" />
              </div>
              <div className="min-w-0 space-y-1.5">
                <Label>Run the agent</Label>
                <CodeBlock value={agentRunCommand(issuedToken)} label="command" />
              </div>
            </div>
            <DialogFooter>
              <Button variant="outline" onClick={() => { reset(); }}>
                Create another
              </Button>
              <Button onClick={() => setOpen(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={onSubmit}>
            <DialogHeader>
              <DialogTitle>New agent</DialogTitle>
              <DialogDescription>
                Agents connect over WSS and host endpoints. A one-time token is issued on creation.
              </DialogDescription>
            </DialogHeader>
            <div className="my-4 space-y-1.5">
              <Label htmlFor="agent-name">Name</Label>
              <Input
                id="agent-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="edge-01"
                autoFocus
              />
            </div>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => setOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={create.isPending}>
                {create.isPending ? "Creating…" : "Create agent"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

export function Agents() {
  const { currentOrgId, canWrite } = useSession();
  const agents = useAgents(currentOrgId, { refetchInterval: 8000 });

  return (
    <div>
      <PageHeader
        title="Agents"
        description="Credentialed tunnel clients that reach into your network."
        actions={canWrite ? <CreateAgentDialog orgId={currentOrgId} /> : undefined}
      />
      <Card>
        <CardContent className="pt-4">
          {agents.isLoading ? (
            <LoadingState label="Loading agents" />
          ) : agents.isError ? (
            <ErrorState error={agents.error} />
          ) : !agents.data || agents.data.length === 0 ? (
            <EmptyState
              title="No agents"
              description="Create your first agent to begin."
              action={canWrite ? <CreateAgentDialog orgId={currentOrgId} /> : undefined}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Connection</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>Last seen</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {agents.data.map((agent) => (
                  <TableRow key={agent.id}>
                    <TableCell>
                      <Link to={`/agents/${agent.id}`} className="font-medium hover:underline">
                        {agent.name}
                      </Link>
                      <p className="font-mono text-xs text-muted-foreground">{agent.id}</p>
                    </TableCell>
                    <TableCell>
                      <Badge variant={agent.status === "active" ? "outline" : "muted"}>{agent.status}</Badge>
                    </TableCell>
                    <TableCell>
                      <StatusDot online={agent.connected} label={agent.connected ? "connected" : "offline"} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatRelativeTime(agent.created_at)}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {formatRelativeTime(agent.last_seen_at)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
