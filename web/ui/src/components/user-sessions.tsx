import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Chip, StatusChip, isStatus } from "@/components/chip";
import { Waiting } from "@/components/user-parts";
import { listSessions, query } from "@/lib/api";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { dataOf, useRead } from "@/lib/user-read";
import { sessionHarness } from "@/lib/session-words";
import { NO_SESSION, READING_SESSIONS, REVOKE_ALL_SESSIONS, SESSION_COLUMN, SUBJECT_SESSIONS, allSessionsOf, openSession } from "@/lib/user-words";
import { needsArea } from "@/lib/say";
import { absTime, agoWord } from "@/lib/words";

type Props = {
  id: string;
  name: string;
  // allowed says the reader's role includes the sessions area.
  allowed: boolean;
  active: number;
  nonce: number;
  onRevokeAll: () => void;
};

// UserSessions is the Sessions tab: the user's sessions of every status,
// newest first, each row opening the session's page. It reads when the tab
// opens.
export function UserSessions({ id, name, allowed, active, nonce, onRevokeAll }: Props) {
  const read = useRead(allowed, () => listSessions(query({ user: id, limit: 50 })), SUBJECT_SESSIONS, nonce);
  if (!allowed) return <p className="m-0 text-dense text-text-2" data-needs-area>{needsArea("The sessions are", "sessions")}</p>;
  const page = dataOf(read);
  const rows = page?.items || [];
  return (
    <div className="flex flex-col gap-3" data-user-sessions>
      {active > 0 && (
        <div className="flex">
          <Button variant="outline" size="sm" className="ml-auto border-danger text-danger hover:bg-danger-bg" onClick={onRevokeAll}>{REVOKE_ALL_SESSIONS}</Button>
        </div>
      )}
      <Waiting read={read} word={READING_SESSIONS} subject={SUBJECT_SESSIONS} />
      {page && rows.length === 0 && <p className="m-0 text-dense text-text-2" data-no-session>{NO_SESSION}</p>}
      {rows.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border">
          <Table className="min-w-[560px] table-fixed">
            <colgroup>
              <col className="w-[28%]" />
              <col />
              <col className="w-[130px]" />
              <col className="w-[120px]" />
            </colgroup>
            <TableHeader>
              <TableRow>
                <TableHead>{SESSION_COLUMN.session}</TableHead>
                <TableHead>{SESSION_COLUMN.harness}</TableHead>
                <TableHead>{SESSION_COLUMN.seen}</TableHead>
                <TableHead>{SESSION_COLUMN.status}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((s) => {
                const short = s.id.slice(0, 8);
                const open = () => navigate("sessions", [s.id]);
                return (
                  <TableRow
                    key={s.id}
                    role="button"
                    tabIndex={0}
                    aria-label={openSession(short)}
                    className="cursor-pointer text-dense focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
                    onClick={open}
                    onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } }}
                    data-session={s.id}
                  >
                    <TableCell className="font-mono" title={s.id}>{short}</TableCell>
                    <TableCell className="truncate">{sessionHarness(s.harness)}</TableCell>
                    <TableCell>
                      <Tooltip>
                        <TooltipTrigger asChild><span>{agoWord(s.last_seen)}</span></TooltipTrigger>
                        <TooltipContent>{absTime(s.last_seen)}</TooltipContent>
                      </Tooltip>
                    </TableCell>
                    <TableCell>{isStatus(s.status) ? <StatusChip status={s.status} /> : <Chip tone="danger">{s.status}</Chip>}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
      {page && page.next_cursor && (
        <div>
          <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => navigate("sessions", [], false, () => put("sessions", { id, username: name }))}>{allSessionsOf(name)}</Button>
        </div>
      )}
    </div>
  );
}
