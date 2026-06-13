import { PageHeader } from "@/components/common/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { EmptyState, ErrorState, LoadingState } from "@/components/common/States";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useAudit } from "@/api/hooks";
import { useSession } from "@/context/SessionContext";
import { formatRelativeTime } from "@/lib/utils";

export function Audit() {
  const { currentOrgId } = useSession();
  const audit = useAudit(currentOrgId);

  return (
    <div>
      <PageHeader title="Audit log" description="Recent activity in this organization." />
      <Card>
        <CardContent className="pt-4">
          {audit.isLoading ? (
            <LoadingState label="Loading audit log" />
          ) : audit.isError ? (
            <ErrorState error={audit.error} />
          ) : !audit.data || audit.data.length === 0 ? (
            <EmptyState title="No events" description="Activity will appear here as it happens." />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>When</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {audit.data.map((ev) => (
                  <TableRow key={ev.id}>
                    <TableCell className="text-muted-foreground">{formatRelativeTime(ev.created_at)}</TableCell>
                    <TableCell>{ev.actor}</TableCell>
                    <TableCell>
                      <Badge variant="muted" className="font-mono">{ev.action}</Badge>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">{ev.target}</TableCell>
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
