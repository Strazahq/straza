import * as React from "react";
import { FileIcon, GlobeIcon, type LucideIcon, RefreshCwIcon, ScrollTextIcon, TerminalIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { OutcomeChip } from "@/components/chip";
import { FetchError } from "@/components/error-state";
import { KIND_ICON } from "@/components/kind-icon";
import { RecordSheet } from "@/components/record-sheet";
import { Segmented } from "@/components/segmented";
import { SessionCard } from "@/components/session-card";
import { PageLink } from "@/components/user-parts";
import type { AuditRow } from "@/lib/api";
import { type Row, ceRow } from "@/lib/audit-words";
import { onMachines } from "@/lib/machine-servers";
import { type RuleView, TOOL_WORD } from "@/lib/policy-model";
import { navigate } from "@/lib/router";
import { needsArea } from "@/lib/say";
import { type ActivityCall, type ActivityFilter, type ActivityRow, countOf, inFilter } from "@/lib/session-activity";
import {
  ACTIVITY, ACTIVITY_FILTER, ACTIVITY_FILTER_LABEL, NEWEST_RECORDS, NO_ACTIVITY, NO_ACTIVITY_HERE, OPEN_REQUEST, READING_ACTIVITY, RELOAD_ACTIVITY, SUBJECT_ACTIVITY, allRecords,
} from "@/lib/session-words";
import { clockTime } from "@/lib/transcript-words";
import type { Read } from "@/lib/user-read";
import { absTime } from "@/lib/words";

const MONO = "font-mono text-caption [overflow-wrap:anywhere]";
const FILTERS: ActivityFilter[] = ["all", "waiting", "denied"];

// callIcon is the icon a call leads with: the server's kind, or a server
// on a person's machine when the list of registered servers can tell,
// then the terminal, a file, the network or a tool.
function callIcon(call: ActivityCall, registered: string[] | null): LucideIcon {
  if (call.kind === "mcp") {
    const machine = !!call.server && onMachines({ lane: "mcp", app: call.server } as RuleView, registered);
    return KIND_ICON[machine ? "local-server" : "server"];
  }
  if (call.kind === "shell") return TerminalIcon;
  if (call.kind === "file") return FileIcon;
  if (call.kind === "net") return GlobeIcon;
  return KIND_ICON.tool;
}

// CallWords is the call in mono: the server then the tool, shell then the
// command, or the tool's word then its path or host.
function CallWords({ call }: { call: ActivityCall }) {
  const parts = call.kind === "mcp" ? [call.server, call.tool] : call.kind === "shell" ? [TOOL_WORD["shell.exec"], call.command] : [call.word, call.target];
  return <>{parts.filter(Boolean).map((p, i) => <React.Fragment key={i}>{i > 0 && " "}<span className={MONO}>{p}</span></React.Fragment>)}</>;
}

function ActivityLine({ row, registered, approvals, onOpen }: { row: ActivityRow; registered: string[] | null; approvals: boolean; onOpen: (r: AuditRow) => void }) {
  const Icon = callIcon(row.call, registered);
  return (
    <li className="flex min-w-0 gap-3 border-b border-border py-3 last:border-b-0" data-activity={row.seq}>
      <span className="w-[4.5rem] shrink-0 pt-0.5 font-mono text-caption text-muted-foreground" title={absTime(row.time)}>{clockTime(row.time)}</span>
      <div className="flex min-w-0 flex-1 flex-col items-start gap-1">
        <div className="flex min-w-0 max-w-full flex-wrap items-center gap-x-2 gap-y-1">
          <button type="button" onClick={() => onOpen(row.record)} className="flex min-w-0 items-start gap-1.5 rounded-md text-left text-foreground outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50" data-activity-call>
            <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <span className="min-w-0">
              <CallWords call={row.call} />
            </span>
          </button>
          <OutcomeChip outcome={row.outcome} />
        </div>
        {row.line && <p className="m-0 text-dense text-text-2 [overflow-wrap:anywhere]" data-activity-line>{row.line}</p>}
        {row.waiting && row.approval && approvals && (
          <Button variant="outline" size="sm" onClick={() => navigate("approvals", ["requests", row.approval])} data-open-request={row.approval}>{OPEN_REQUEST}</Button>
        )}
        {row.note && <span className="text-caption text-muted-foreground [overflow-wrap:anywhere]" data-activity-note>{row.note}</span>}
      </div>
    </li>
  );
}

type Props = {
  id: string;
  // allowed says the reader's role includes the audit area, and approvals
  // the approvals area, which Open the request needs.
  allowed: boolean;
  approvals: boolean;
  read: Read<AuditRow[]>;
  // rows are the calls once the read answered. records counts the
  // session's records read, and full says the read came back at its limit.
  rows: ActivityRow[] | null;
  records: number;
  full: boolean;
  filter: ActivityFilter;
  onFilter: (f: ActivityFilter) => void;
  onReload: () => void;
  // registered names the registered servers, null while unread or when the
  // reader's seat does not read them all.
  registered: string[] | null;
};

// SessionActivity is the Activity card: what the session did, newest first,
// with a filter, each call opening its audit record, and a door to all of
// the session's records in Audit.
export function SessionActivity({ id, allowed, approvals, read, rows, records, full, filter, onFilter, onReload, registered }: Props) {
  const [record, setRecord] = React.useState<Row | null>(null);
  if (!allowed) {
    return (
      <SessionCard title={ACTIVITY} data-activity-card>
        <p className="m-0 text-dense text-text-2" data-needs-area>{needsArea("The activity is", "audit")}</p>
      </SessionCard>
    );
  }
  const shown = rows ? rows.filter((r) => inFilter(r, filter)) : [];
  const options = FILTERS.map((f) => ({ value: f, label: ACTIVITY_FILTER[f], count: rows ? countOf(rows, f) : 0, "data-activity-filter": f }));
  return (
    <SessionCard
      title={ACTIVITY}
      data-activity-card
      extra={
        <>
          <span className="ml-auto flex min-w-0 max-w-full items-center gap-2">
            {rows && rows.length > 0 && <Segmented label={ACTIVITY_FILTER_LABEL} value={filter} onChange={onFilter} options={options} className="min-w-0" />}
            <Button variant="ghost" size="icon-sm" aria-label={RELOAD_ACTIVITY} title={RELOAD_ACTIVITY} onClick={onReload}><RefreshCwIcon /></Button>
          </span>
        </>
      }
    >
      {read.kind === "loading" && <p className="m-0 text-dense text-muted-foreground" role="status">{READING_ACTIVITY}</p>}
      {read.kind === "failed" && <FetchError subject={SUBJECT_ACTIVITY} detail={read.message} />}
      {rows && rows.length === 0 && <p className="m-0 text-dense text-text-2" data-no-activity>{NO_ACTIVITY}</p>}
      {rows && rows.length > 0 && shown.length === 0 && <p className="m-0 text-dense text-text-2" data-no-activity-here>{NO_ACTIVITY_HERE}</p>}
      {shown.length > 0 && (
        <ol className="m-0 flex list-none flex-col p-0">
          {shown.map((r) => <ActivityLine key={r.seq} row={r} registered={registered} approvals={approvals} onOpen={(a) => setRecord(ceRow(a))} />)}
        </ol>
      )}
      {rows && records > 0 && (
        <PageLink to="audit" rest={[id]} className="inline-flex items-center gap-1.5 self-start text-dense text-link underline-offset-4 hover:underline" data-activity-foot>
          <ScrollTextIcon className="size-4 shrink-0" aria-hidden="true" />
          {full ? NEWEST_RECORDS : allRecords(records)}
        </PageLink>
      )}
      {record && <RecordSheet row={record} open={!!record} onOpenChange={(o) => { if (!o) setRecord(null); }} revoked={false} verified={false} />}
    </SessionCard>
  );
}
