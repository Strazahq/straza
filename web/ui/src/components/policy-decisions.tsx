import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ArrowRightIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DataTable, plain } from "@/components/data-table";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { RecordSheet } from "@/components/record-sheet";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type AuditRow, listAudit } from "@/lib/api";
import { type Row, ceRow, ceWhat, effectTone, effectWord, parseCE, whyOf } from "@/lib/audit-words";
import type { Bucket } from "@/lib/policy-model";
import { ANY_OUTCOME, ANY_WHO, BUCKET_WORD, DEC_FILTER, DECISION_COLUMN, DECISIONS_FOOT, DECISIONS_HELP, DECISIONS_LINE, NO_DECISIONS, NO_DECISIONS_HERE, OPEN_IN_AUDIT, SINCE, SUBJECT_DECISIONS, TAB, decisionCall, decisionCount } from "@/lib/policy-words";
import { readFailed } from "@/lib/say";
import { agoWord } from "@/lib/settings-words";
import { absTime } from "@/lib/words";

// The Decisions tab of a policy's page: the audit
// chain filtered to the records this policy decided, newest first. A row
// opens the same record sheet the Audit screen opens, so one record reads
// identically wherever it is met.

// PAGE is how many records the tab reads. order=desc asks the audit route
// for the newest window, and never rides with after, which pages forward
// from a sequence instead.
const PAGE = 100;

// ANY is the value of a filter that narrows nothing. It is a sentinel
// rather than an empty string because a Select item needs a value.
const ANY = "*";

// SinceKey is one window of the Since filter, compared in the browser
// against the record's own time. all keeps every loaded row.
type SinceKey = keyof typeof SINCE;

const WINDOW: Record<SinceKey, number> = { day: 86400000, week: 604800000, month: 2592000000, all: 0 };

// Decision is one chain record as this tab reads it: the audit row, plus
// the call it decided, the outcome in the three words of the area, and the
// rule that reported it.
type Decision = {
  row: Row;
  call: string;
  word: string;
  tone: "ok" | "warn" | "danger" | "plain";
  // bucket is the outcome the filter matches on, and is null for a record
  // whose effect the why reader does not fold into the three words.
  bucket: Bucket | null;
  ruleId: string;
};

function decision(r: AuditRow): Decision {
  const row = ceRow(r);
  const ce = parseCE(r.ce);
  const d = (ce && ce.data) || {};
  const why = whyOf(ce);
  const bucket = why ? (why.tone === "danger" ? "deny" : why.tone === "warn" ? "hum" : "allow") : null;
  return {
    row,
    call: decisionCall(d as Record<string, unknown>) || ceWhat(ce),
    word: bucket ? BUCKET_WORD[bucket] : effectWord(row.effect),
    tone: why ? why.tone : effectTone(row.effect),
    bucket,
    ruleId: String(d.ruleId || ""),
  };
}

const LABEL: Record<string, string> = { when: DECISION_COLUMN.when, who: DECISION_COLUMN.who, call: DECISION_COLUMN.call, outcome: DECISION_COLUMN.outcome, rule: DECISION_COLUMN.rule };
const WIDTHS: Record<string, string> = { when: "110px", who: "180px", outcome: "150px", rule: "260px" };

const COLUMNS: ColumnDef<Decision>[] = [
  { id: "when", accessorFn: (d) => d.row.time, header: plain(DECISION_COLUMN.when), cell: ({ row }) => <span title={absTime(row.original.row.time)}>{agoWord(row.original.row.time)}</span> },
  { id: "who", accessorFn: (d) => d.row.user, header: plain(DECISION_COLUMN.who), cell: ({ row }) => <b className="block truncate font-semibold text-foreground" title={row.original.row.user}>{row.original.row.user}</b> },
  { id: "call", accessorFn: (d) => d.call, header: plain(DECISION_COLUMN.call), cell: ({ row }) => <span className="block truncate font-mono text-[13px]">{row.original.call}</span> },
  { id: "outcome", accessorFn: (d) => d.word, header: plain(DECISION_COLUMN.outcome), cell: ({ row }) => <WordBadge word={row.original.word} tone={row.original.tone} attr="data-outcome" /> },
  { id: "rule", accessorFn: (d) => d.ruleId, header: plain(DECISION_COLUMN.rule), cell: ({ row }) => <span className="block truncate font-mono text-[13px] text-muted-foreground">{row.original.ruleId}</span> },
];

const BUCKETS: Bucket[] = ["deny", "hum", "allow"];
const SINCE_KEYS: SinceKey[] = ["day", "week", "month", "all"];

// onOpenAudit is the door to the Audit screen; the page owns it, so a
// departure with unpublished edits asks first.
type Props = { name: string; onOpenAudit: () => void };

export function PolicyDecisions({ name, onOpenAudit }: Props) {
  const [rows, setRows] = React.useState<Decision[]>([]);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [lastRead, setLastRead] = React.useState<Date | null>(null);
  const [record, setRecord] = React.useState<Row | null>(null);
  const [who, setWho] = React.useState(ANY);
  const [outcome, setOutcome] = React.useState(ANY);
  const [since, setSince] = React.useState<SinceKey>("day");

  React.useEffect(() => {
    let alive = true;
    listAudit("q=" + encodeURIComponent('"setName":"' + name + '"') + "&limit=" + PAGE + "&order=desc").then(
      (answer) => {
        if (!alive) return;
        setRows((answer || []).map(decision));
        setProblem(null);
        setLastRead(new Date());
      },
      (e) => {
        const err = e as ApiError;
        if (alive && err.status !== 401) setProblem(readFailed(SUBJECT_DECISIONS, err));
      },
    );
    return () => { alive = false; };
  }, [name]);

  // The three filters narrow the page the browser already holds, so a
  // choice answers at once and never re-reads the chain.
  const people = React.useMemo(() => [...new Set(rows.map((d) => d.row.user).filter(Boolean))].sort(), [rows]);
  const shown = React.useMemo(() => {
    const floor = WINDOW[since] ? Date.now() - WINDOW[since] : 0;
    return rows.filter((d) => {
      if (who !== ANY && d.row.user !== who) return false;
      if (outcome !== ANY && d.bucket !== outcome) return false;
      if (!floor) return true;
      const t = Date.parse(d.row.time);
      return Number.isNaN(t) || t >= floor;
    });
  }, [rows, who, outcome, since]);

  return (
    <div className="flex flex-col gap-3" data-decisions-tab>
      <div className="flex flex-wrap items-center gap-2 text-sm text-text-2">
        <span>{DECISIONS_LINE}</span>
        <HelpTip label={TAB.decisions} text={DECISIONS_HELP} />
        <Button variant="ghost" size="sm" className="ml-auto text-link" onClick={onOpenAudit}>{OPEN_IN_AUDIT} <ArrowRightIcon /></Button>
      </div>

      {problem && <FetchError subject={SUBJECT_DECISIONS} detail={problem} lastRead={lastRead} />}

      <DataTable
        rows={shown}
        columns={COLUMNS}
        labels={LABEL}
        widths={WIDTHS}
        rowKey={(d) => String(d.row.seq)}
        rowName={(d) => String(d.row.seq)}
        dataAttr="data-decision"
        onOpen={(d) => setRecord(d.row)}
        openKey={record ? String(record.seq) : null}
        count={decisionCount}
        emptyText={rows.length ? NO_DECISIONS_HERE : NO_DECISIONS}
        filterBar={
          <>
            <Select value={who} onValueChange={setWho}>
              <SelectTrigger size="sm" aria-label={DEC_FILTER.who} className="h-9 w-[180px]" data-dec-filter="who"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={ANY}>{ANY_WHO}</SelectItem>
                {people.map((u) => <SelectItem key={u} value={u}>{u}</SelectItem>)}
              </SelectContent>
            </Select>
            <Select value={outcome} onValueChange={setOutcome}>
              <SelectTrigger size="sm" aria-label={DEC_FILTER.outcome} className="h-9 w-[180px]" data-dec-filter="outcome"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={ANY}>{ANY_OUTCOME}</SelectItem>
                {BUCKETS.map((b) => <SelectItem key={b} value={b}>{BUCKET_WORD[b]}</SelectItem>)}
              </SelectContent>
            </Select>
            <Select value={since} onValueChange={(v) => setSince(v as SinceKey)}>
              <SelectTrigger size="sm" aria-label={DEC_FILTER.since} className="h-9 w-[180px]" data-dec-filter="since"><SelectValue /></SelectTrigger>
              <SelectContent>
                {SINCE_KEYS.map((k) => <SelectItem key={k} value={k}>{SINCE[k]}</SelectItem>)}
              </SelectContent>
            </Select>
          </>
        }
        foot={<p className="text-[13px] text-muted-foreground">{DECISIONS_FOOT}</p>}
      />

      {record && (
        <RecordSheet row={record} open={!!record} onOpenChange={(o) => { if (!o) setRecord(null); }} revoked={false} verified={false} />
      )}
    </div>
  );
}
