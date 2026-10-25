import { useState } from "react";
import { Link } from "react-router-dom";
import { LogOut } from "lucide-react";
import {
  useEndpointSessions,
  useRevokeEndpointSession,
  useRevokeEndpointSessions,
} from "@/api/hooks";
import { errorMessage } from "@/api/client";
import type { Endpoint, EndpointSession } from "@/api/types";
import { ConfirmDialog } from "@/components/common/ConfirmDialog";
import { HelpTip } from "@/components/common/HelpTip";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { toast } from "@/hooks/use-toast";
import { HELP } from "@/lib/glossary";
import { formatRelativeTime, formatTimeUntil } from "@/lib/utils";

function SessionWho({ session }: { session: EndpointSession }) {
  const label = session.email || session.subject_id;
  if (session.subject_type === "user") {
    return (
      <Link to="/members" className="hover:underline">
        {label}
      </Link>
    );
  }
  return <span>{label}</span>;
}

export function BrowserSessionsSection({
  endpoint,
  canWrite,
}: {
  endpoint: Endpoint;
  canWrite: boolean;
}): React.JSX.Element {
  const sessions = useEndpointSessions(endpoint.id);
  const revokeOne = useRevokeEndpointSession(endpoint.id);
  const revokeAll = useRevokeEndpointSessions(endpoint.id);
  const [confirmAll, setConfirmAll] = useState(false);
  const count = sessions.data?.length ?? 0;

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between gap-4 space-y-0">
        <div className="space-y-1">
          <CardTitle className="flex items-center gap-2">
            Browser sessions <HelpTip entry={HELP.browserSessions} />
          </CardTitle>
          <p className="text-sm text-muted-foreground">
            People signed in to this endpoint in a browser. Revoking ends the
            session on its next request.
          </p>
        </div>
        {canWrite && count > 0 && (
          <Button variant="outline" onClick={() => setConfirmAll(true)}>
            <LogOut className="h-4 w-4" /> Revoke all
          </Button>
        )}
      </CardHeader>
      <CardContent>
        {sessions.isLoading ? (
          <LoadingState />
        ) : sessions.isError ? (
          <ErrorState error={sessions.error} />
        ) : count === 0 ? (
          <EmptyState
            title="No one is signed in"
            description="No one is signed in to this endpoint."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Who</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Signed in</TableHead>
                <TableHead>Expires</TableHead>
                {canWrite && <TableHead></TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {sessions.data?.map((s) => (
                <TableRow key={s.id}>
                  <TableCell>
                    <SessionWho session={s} />
                  </TableCell>
                  <TableCell>
                    <Badge variant="muted">
                      {s.subject_type === "user" ? "Member" : "Guest"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {formatRelativeTime(s.created_at)}
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {formatTimeUntil(s.expires_at)}
                  </TableCell>
                  {canWrite && (
                    <TableCell className="text-right">
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={revokeOne.isPending && revokeOne.variables === s.id}
                        onClick={() =>
                          revokeOne.mutate(s.id, {
                            onSuccess: () => toast({ title: "Session revoked" }),
                            onError: (e) =>
                              toast({
                                variant: "destructive",
                                title: "Revoke failed",
                                description: errorMessage(e),
                              }),
                          })
                        }
                      >
                        Revoke
                      </Button>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
      <ConfirmDialog
        open={confirmAll}
        onOpenChange={setConfirmAll}
        title="Sign out everyone?"
        description={
          <>
            {count === 1 ? "1 session ends" : `${count} sessions end`} on the
            next request. People can sign in again if access still allows.
          </>
        }
        confirmLabel="Revoke all"
        pendingLabel="Revoking…"
        pending={revokeAll.isPending}
        error={revokeAll.error ? errorMessage(revokeAll.error) : undefined}
        onConfirm={() =>
          revokeAll.mutate(undefined, {
            onSuccess: (res) => {
              setConfirmAll(false);
              toast({
                title:
                  res.revoked === 1
                    ? "1 session revoked"
                    : `${res.revoked} sessions revoked`,
              });
            },
          })
        }
      />
    </Card>
  );
}
