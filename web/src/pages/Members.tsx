import { useState, type FormEvent } from "react";
import { Plus, Trash2 } from "lucide-react";
import { PageHeader } from "@/components/common/PageHeader";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAddMember, useMembers, useRemoveMember, useUpdateMemberRole } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { toast } from "@/hooks/use-toast";
import { formatRelativeTime } from "@/lib/utils";
import { ApiError } from "@/api/client";
import type { Role } from "@/api/types";

const ROLES: Role[] = ["owner", "admin", "member"];

function errMsg(err: unknown): string {
  return err instanceof ApiError ? err.message : "Unexpected error";
}

function AddMemberDialog({ orgId }: { orgId?: string }) {
  const [open, setOpen] = useState(false);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("member");
  const add = useAddMember(orgId);

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    add.mutate(
      { email: email.trim(), role },
      {
        onSuccess: () => {
          toast({ title: "Member added" });
          setOpen(false);
          setEmail("");
        },
        onError: (err) => toast({ variant: "destructive", title: "Add failed", description: errMsg(err) }),
      },
    );
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <Button onClick={() => setOpen(true)}>
        <Plus className="h-4 w-4" /> Add member
      </Button>
      <DialogContent>
        <form onSubmit={onSubmit}>
          <DialogHeader>
            <DialogTitle>Add member</DialogTitle>
            <DialogDescription>Invite a user to this organization by email.</DialogDescription>
          </DialogHeader>
          <div className="my-4 space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="member-email">Email</Label>
              <Input id="member-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
            </div>
            <div className="space-y-1.5">
              <Label>Role</Label>
              <Select value={role} onValueChange={(v) => setRole(v as Role)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ROLES.map((r) => (
                    <SelectItem key={r} value={r}>
                      {r}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={!email || add.isPending}>
              {add.isPending ? "Adding…" : "Add"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

export function Members() {
  const { currentOrgId, isOwnerOrAdmin } = useSession();
  const members = useMembers(currentOrgId);
  const updateRole = useUpdateMemberRole(currentOrgId);
  const remove = useRemoveMember(currentOrgId);

  return (
    <div>
      <PageHeader
        title="Members"
        description="People with access to this organization."
        actions={isOwnerOrAdmin ? <AddMemberDialog orgId={currentOrgId} /> : undefined}
      />
      <Card>
        <CardContent className="pt-4">
          {members.isLoading ? (
            <LoadingState label="Loading members" />
          ) : members.isError ? (
            <ErrorState error={members.error} />
          ) : !members.data || members.data.length === 0 ? (
            <EmptyState title="No members" description="Add a teammate to collaborate." />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>User</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>Joined</TableHead>
                  <TableHead></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {members.data.map((m) => (
                  <TableRow key={m.id}>
                    <TableCell>
                      <p className="font-medium">{m.name || m.email}</p>
                      <p className="text-xs text-muted-foreground">{m.email}</p>
                    </TableCell>
                    <TableCell>
                      {isOwnerOrAdmin ? (
                        <Select
                          value={m.role}
                          onValueChange={(v) =>
                            updateRole.mutate(
                              { id: m.id, role: v as Role },
                              {
                                onSuccess: () => toast({ title: "Role updated" }),
                                onError: (e) => toast({ variant: "destructive", title: "Update failed", description: errMsg(e) }),
                              },
                            )
                          }
                        >
                          <SelectTrigger className="h-8 w-32">
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {ROLES.map((r) => (
                              <SelectItem key={r} value={r}>
                                {r}
                              </SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      ) : (
                        <Badge variant="muted">{m.role}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-muted-foreground">{formatRelativeTime(m.created_at)}</TableCell>
                    <TableCell className="text-right">
                      {isOwnerOrAdmin && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="text-destructive"
                          onClick={() =>
                            remove.mutate(m.id, {
                              onSuccess: () => toast({ title: "Member removed" }),
                              onError: (e) => toast({ variant: "destructive", title: "Remove failed", description: errMsg(e) }),
                            })
                          }
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      )}
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
