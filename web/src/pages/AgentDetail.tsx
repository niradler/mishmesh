import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ArrowLeft, KeyRound, RotateCcw, Trash2, Ban } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
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
import {
  useAgent,
  useAgentEndpoints,
  useAgentTokens,
  useDeleteAgent,
  useRevokeAgent,
  useRotateToken,
} from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { formatRelativeTime } from "@/lib/utils";
import { ApiError } from "@/api/client";

function errMsg(err: unknown): string {
  return err instanceof ApiError ? err.message : "Unexpected error";
}

export function AgentDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const { currentOrgId, canWrite } = useSession();
  const agent = useAgent(id);
  const endpoints = useAgentEndpoints(id);
  const tokens = useAgentTokens(id);
  const rotate = useRotateToken();
  const revoke = useRevokeAgent();
  const del = useDeleteAgent(currentOrgId);

  const [rotated, setRotated] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  if (agent.isLoading) return <LoadingState label="Loading agent" />;
  if (agent.isError) return <ErrorState error={agent.error} />;
  if (!agent.data) return null;
  const a = agent.data;
  const isRevoked = a.status === "revoked";

  return (
    <div className="space-y-6">
      <Link to="/agents" className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground">
        <ArrowLeft className="h-4 w-4" /> Agents
      </Link>

      <PageHeader
        title={a.name}
        description={a.id}
        actions={
          canWrite ? (
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                disabled={rotate.isPending || isRevoked}
                onClick={() =>
                  rotate.mutate(id, {
                    onSuccess: (res) => {
                      setRotated(res.token);
                      toast({ title: "Token rotated" });
                    },
                    onError: (e) => toast({ variant: "destructive", title: "Rotate failed", description: errMsg(e) }),
                  })
                }
              >
                <RotateCcw className="h-4 w-4" /> Rotate token
              </Button>
              {!isRevoked ? (
                <Button
                  variant="outline"
                  className="text-destructive"
                  disabled={revoke.isPending}
                  onClick={() =>
                    revoke.mutate(id, {
                      onSuccess: () => toast({ title: "Agent revoked" }),
                      onError: (e) => toast({ variant: "destructive", title: "Revoke failed", description: errMsg(e) }),
                    })
                  }
                >
                  <Ban className="h-4 w-4" /> Revoke
                </Button>
              ) : (
                <Button variant="destructive" onClick={() => setConfirmDelete(true)}>
                  <Trash2 className="h-4 w-4" /> Delete
                </Button>
              )}
            </div>
          ) : undefined
        }
      />

      <div className="grid gap-3 sm:grid-cols-3">
        <Card>
          <CardContent className="space-y-1 p-4">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">Status</p>
            <Badge variant={a.status === "active" ? "outline" : "muted"}>{a.status}</Badge>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="space-y-1 p-4">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">Connection</p>
            <StatusDot online={a.connected} label={a.connected ? "connected" : "offline"} />
          </CardContent>
        </Card>
        <Card>
          <CardContent className="space-y-1 p-4">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">Last seen</p>
            <p className="text-sm">{formatRelativeTime(a.last_seen_at)}</p>
          </CardContent>
        </Card>
      </div>

      {rotated && (
        <Card className="border-success/40">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-success">
              <KeyRound className="h-4 w-4" /> New token issued
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            <Label>Shown once — copy it now</Label>
            <CodeBlock value={rotated} label="token" />
          </CardContent>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Endpoints</CardTitle>
        </CardHeader>
        <CardContent>
          {endpoints.isLoading ? (
            <LoadingState />
          ) : endpoints.isError ? (
            <ErrorState error={endpoints.error} />
          ) : !endpoints.data || endpoints.data.length === 0 ? (
            <EmptyState title="No endpoints" description="This agent has not declared any endpoints." />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Public URL</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Lifecycle</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {endpoints.data.map((ep) => (
                  <TableRow key={ep.id}>
                    <TableCell className="font-mono text-xs">
                      {ep.public_url ?? ep.subdomain ?? ep.domain ?? (ep.port ? `:${ep.port}` : ep.id)}
                    </TableCell>
                    <TableCell>
                      <Badge variant="muted">{ep.kind}</Badge>
                    </TableCell>
                    <TableCell className="text-muted-foreground">{ep.lifecycle}</TableCell>
                    <TableCell className="text-right">
                      <Link to={`/endpoints/${ep.id}`} className="text-sm hover:underline">
                        Configure
                      </Link>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Tokens</CardTitle>
        </CardHeader>
        <CardContent>
          {tokens.isLoading ? (
            <LoadingState />
          ) : tokens.isError ? (
            <ErrorState error={tokens.error} />
          ) : !tokens.data || tokens.data.length === 0 ? (
            <EmptyState title="No tokens" description="Rotate to issue a fresh token." />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>ID</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>State</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tokens.data.map((t) => (
                  <TableRow key={t.id}>
                    <TableCell className="font-mono text-xs">{t.id}</TableCell>
                    <TableCell className="text-muted-foreground">{formatRelativeTime(t.created_at)}</TableCell>
                    <TableCell>
                      {t.revoked_at ? (
                        <Badge variant="destructive">revoked</Badge>
                      ) : (
                        <Badge variant="success">active</Badge>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete agent</DialogTitle>
            <DialogDescription>
              This permanently removes <span className="font-medium">{a.name}</span>. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmDelete(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={del.isPending}
              onClick={() =>
                del.mutate(id, {
                  onSuccess: () => {
                    toast({ title: "Agent deleted" });
                    navigate("/agents");
                  },
                  onError: (e) => toast({ variant: "destructive", title: "Delete failed", description: errMsg(e) }),
                })
              }
            >
              {del.isPending ? "Deleting…" : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
