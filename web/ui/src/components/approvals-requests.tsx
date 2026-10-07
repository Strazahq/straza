import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { CircleCheckIcon, Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ApprovalDecide, type Ask } from "@/components/approval-decide";
import { ApprovalDialog, type DoorOpener, consoleDoors } from "@/components/approval-dialog";
import { RoleChip } from "@/components/status-badge";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError } from "@/components/error-state";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type ApprovalRow, type Page, type RoleRow, decideApproval, getApproval, listApprovals, listRoles, pageApprovals } from "@/lib/api";
import { type Call, type Seat, callOf, decidersOf, holdShare, kindOf, phaseOf, shortID, stuck, timeLeft } from "@/lib/approval-model";
import {
  APPROVE,
  DECIDES_HELP,
  DENY,
  EMPTY_DECIDED_BODY,
  EMPTY_DECIDED_TITLE,
  EMPTY_SEARCH,
  EMPTY_WAITING_BODY,
  EMPTY_WAITING_TITLE,
  FILTERS,
  FILTER_LABEL,
  type Filter,
  KIND_WORD,
  LOAD_MORE,
  NOBODY_CAN,
  NOBODY_HOLDS,
  OPEN_SELF_SERVICE,
  OWN_REQUEST,
  PHASE_WORD,
  READING_REQUESTS,
  REQUEST_COLUMN,
  SEARCH_LABEL,
  SEARCH_REQUESTS,
  SELF_SERVICE_PAGE,
  SUBJECT_REQUESTS,
  type Verdict,
  YOURS,
  decidedCount,
  decideAction,
  deciderWhy,
  deciderWords,
  decidesWord,
  laneWord,
  loadedLine,
  phaseTone,
  requestsCount,
  stateLine,
  waitingCount,
} from "@/lib/approval-words";
import { usePagedList } from "@/lib/paged";
import { readFailed } from "@/lib/say";
import { agoWord } from "@/lib/settings-words";
import { absTime } from "@/lib/words";
import { cn } from "@/lib/utils";

// RequestsTab is the queue of the Approvals area: every request, waiting or
// decided, in one table with a sheet
// behind each row and the decide dialog on the rows that are the seat's to
// decide.

const POLL_MS = 5000;
const TICK_MS = 1000;
const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
// The widths leave the Call column the rest of the page: 163 px on a
// 1280 px page and more on a wider one. The table's minimum width in
// console-layout.css keeps that column from collapsing on a narrow page,
// where the table scrolls instead.
const WIDTHS: Record<string, string> = { asked: "80px", who: "208px", decides: "192px", state: "207px", act: "160px" };
const LABELS: Record<string, string> = {
  asked: REQUEST_COLUMN.asked,
  who: REQUEST_COLUMN.who,
  call: REQUEST_COLUMN.call,
  decides: REQUEST_COLUMN.decides,
  state: REQUEST_COLUMN.state,
};

// RequestsSource is where the queue's rows come from, what a decision does
// with one, and which console areas the dialog may open. The Approvals
// area names none and the admin calls below stand; the self-service page
// hands in the approver lane, whose rows are one person's and whose
// decisions are signed by the browser.
export type RequestsSource = {
  // waiting answers the rows that wait, in one read.
  waiting: () => Promise<ApprovalRow[]>;
  // page answers one page of the decided rows for usePagedList's read.
  page: (q: string) => Promise<Page<ApprovalRow>>;
  // decide records the verdict on a row and answers the fresh record.
  decide: (row: ApprovalRow, verdict: Verdict, reason: string) => Promise<ApprovalRow>;
  // get re-reads one row after a 409, or null when the lane cannot.
  get: ((id: string) => Promise<ApprovalRow | null>) | null;
  // roles answers the roles list for the stuck mark, or null to skip it.
  roles: (() => Promise<RoleRow[]>) | null;
  // doors opens a console area for a fact of the dialog, or null to hide
  // every door.
  doors: DoorOpener | null;
  // wake subscribes the queue's own re-read to the lane's live signals and
  // answers the unsubscribe, or null where the poll is the only signal.
  wake: ((refetch: () => void) => () => void) | null;
};

// adminSource is the Approvals area's own lane, the calls this tab has
// always made.
export const adminSource: RequestsSource = {
  waiting: async () => (await listApprovals("pending")).approvals || [],
  page: pageApprovals,
  decide: async (row, verdict, reason) => (await decideApproval(row.id, verdict, reason)).approval,
  get: async (id) => (await getApproval(id)).approval,
  roles: listRoles,
  doors: consoleDoors,
  wake: null,
};

export type RequestsProps = {
  seat: Seat;
  // source is the lane behind the queue; the admin calls stand where a
  // caller names none.
  source?: RequestsSource;
  // isMine says whether the seat may decide a record, the page's reading of
  // the server's rule; the buttons render only where it answers true.
  isMine: (r: ApprovalRow) => boolean;
  // needsDevice says a record is the seat's own and this lane cannot sign
  // it, so the buttons give way to the door to the page that signs. A lane
  // that signs names none.
  needsDevice?: (r: ApprovalRow) => boolean;
  // openID is a request named in the address; its sheet opens once the
  // list holds the row.
  openID?: string;
  // onChanged tells the page a decision landed, so the counts re-read.
  onChanged: () => void;
};

// WhereChip names where the call goes: a server in the link hue so it
// reads as a server, and the hook's shell, files or network in the plain
// role chip. The lane sentence sits on the tooltip, since the chip is the
// only place a row says where the call runs.
function WhereChip({ where, kind, lane }: { where: string; kind: Call["whereKind"]; lane: string }) {
  if (!where) return null;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default" data-where={where}>
          {kind === "server"
            ? <span className="mr-1 inline-block rounded-md border border-link/40 bg-background px-1.5 py-px font-mono text-[13px] text-link">{where}</span>
            : <RoleChip name={where} />}
        </span>
      </TooltipTrigger>
      <TooltipContent>{laneWord(lane)}</TooltipContent>
    </Tooltip>
  );
}

// Stamp is a time the operator way: the relative reading, in days once it
// is older than a day, the absolute stamp with its zone on hover.
function Stamp({ iso }: { iso: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default tabular-nums text-text-2">{agoWord(iso)}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

export function RequestsTab({ seat, source = adminSource, isMine, needsDevice, openID, onChanged }: RequestsProps) {
  const [waiting, setWaiting] = React.useState<ApprovalRow[] | null>(null);
  const [lastRead, setLastRead] = React.useState<Date | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [holders, setHolders] = React.useState<Record<string, number>>({});
  // fresh holds the records a decision handed back, so a decided row reads
  // as decided before either list is read again.
  const [fresh, setFresh] = React.useState<Record<string, ApprovalRow>>({});
  const [filter, setFilter] = React.useState<Filter>(openID ? "all" : "waiting");
  const [search, setSearch] = React.useState("");
  const [openRow, setOpenRow] = React.useState<ApprovalRow | null>(null);
  const [ask, setAsk] = React.useState<Ask | null>(null);
  const [now, setNow] = React.useState(() => Date.now());

  const readRoles = React.useCallback(() => {
    // A lane with no roles read marks no row as stuck, and so does a failed
    // one; neither breaks anything else.
    if (!source.roles) return;
    void source.roles().then(
      (rows) => setHolders(Object.fromEntries((rows || []).map((r) => [r.name, r.holder_count || 0]))),
      () => undefined,
    );
  }, [source]);

  // A lane that cannot read who holds a role knows nothing about it, and
  // "nobody can decide this" is a claim, never a guess: without the read no
  // row is marked.
  const isStuck = React.useCallback((r: ApprovalRow) => !!source.roles && stuck(r, holders), [source.roles, holders]);

  const readWaiting = React.useCallback(async () => {
    try {
      const rows = await source.waiting();
      setWaiting(rows);
      setLastRead(new Date());
      setProblem(null);
      // Who holds an approver role is the one read the stuck mark needs,
      // so it rides the polls that carry a role-routed row and no others.
      if (rows.some((r) => decidersOf(r).kind === "role")) readRoles();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setProblem(readFailed(SUBJECT_REQUESTS, err));
    }
  }, [source, readRoles]);

  React.useEffect(() => { readRoles(); }, [readRoles]);

  React.useEffect(() => {
    void readWaiting();
    const t = setInterval(() => { if (document.visibilityState !== "hidden") void readWaiting(); }, POLL_MS);
    return () => clearInterval(t);
  }, [readWaiting]);

  // One clock for the whole tab, so every waiting row counts down on the
  // same second and no row carries a timer of its own.
  React.useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(t);
  }, []);

  // A lane with live signals of its own re-reads through the same call the
  // poll makes, so a relayed push lands as rows without a reload.
  React.useEffect(() => (source.wake ? source.wake(() => { void readWaiting(); }) : undefined), [source, readWaiting]);

  const { state, busy, reload, loadMore } = usePagedList<ApprovalRow>({
    read: source.page,
    subject: SUBJECT_REQUESTS,
    params: { state: "all" },
    sorting: null,
    limit: 100,
    pollMs: POLL_MS,
  });

  // A record a decision handed back replaces the stored one until a read
  // catches up with it.
  const fix = React.useCallback((r: ApprovalRow) => {
    const f = fresh[r.id];
    return f && r.state === "pending" ? f : r;
  }, [fresh]);

  const pending = (waiting || []).map(fix).filter((r) => r.state === "pending").sort((a, b) => Date.parse(a.expiresAt) - Date.parse(b.expiresAt));
  const paged = state.kind === "ready" ? state.rows.map(fix) : [];
  const decided = paged.filter((r) => r.state !== "pending");
  const held = new Set(pending.map((r) => r.id));
  const rows = filter === "waiting" ? pending : filter === "decided" ? decided : [...pending, ...paged.filter((r) => !held.has(r.id))];
  const mineCount = pending.filter((r) => isMine(r)).length;
  const more = state.kind === "ready" && state.more;

  const wanted = React.useRef(openID || "");
  React.useEffect(() => {
    if (!wanted.current) return;
    const hit = rows.find((r) => r.id === wanted.current);
    if (hit) { wanted.current = ""; setOpenRow(hit); }
  }, [rows]);

  const handleDecided = (row: ApprovalRow) => {
    setFresh((f) => ({ ...f, [row.id]: row }));
    setOpenRow((o) => (o && o.id === row.id ? null : o));
    void readWaiting();
    void reload();
    onChanged();
  };

  const columns: ColumnDef<ApprovalRow>[] = [
    {
      id: "asked",
      accessorFn: (r) => r.createdAt,
      header: plain<ApprovalRow>(REQUEST_COLUMN.asked),
      cell: ({ row }) => <Stamp iso={row.original.createdAt} />,
    },
    {
      id: "who",
      accessorFn: (r) => r.username || r.user,
      header: plain<ApprovalRow>(REQUEST_COLUMN.who),
      cell: ({ row }) => (
        <div className="min-w-0">
          <div className="whitespace-normal font-semibold text-foreground [overflow-wrap:anywhere]">{row.original.username || row.original.user}</div>
          <div className="truncate font-mono text-[12.5px] text-muted-foreground">{shortID(row.original.session)}</div>
        </div>
      ),
    },
    {
      id: "call",
      accessorFn: (r) => { const c = callOf(r.summary); return c.where + " " + c.call; },
      header: plain<ApprovalRow>(REQUEST_COLUMN.call),
      cell: ({ row }) => {
        const c = callOf(row.original.summary);
        return (
          <span className="flex flex-wrap items-center gap-1 whitespace-normal [overflow-wrap:anywhere]">
            <WhereChip where={c.where} kind={c.whereKind} lane={row.original.lane} />
            <span className="font-mono">{c.call}</span>
          </span>
        );
      },
    },
    {
      id: "decides",
      accessorFn: (r) => { const d = decidersOf(r); return [...d.users, ...d.roles, r.rule, r.set].join(" "); },
      header: plain<ApprovalRow>(REQUEST_COLUMN.decides, DECIDES_HELP),
      cell: ({ row }) => {
        const r = row.original;
        const d = decidersOf(r);
        return (
          <div className="flex flex-col gap-0.5 whitespace-normal">
            <span className="flex flex-wrap items-center gap-1">
              <span className="text-foreground">{deciderWords(d)}</span>
              {r.state === "pending" && isMine(r) && <WordBadge word={YOURS} tone="accent" attr="data-yours" />}
              {isStuck(r) && <WordBadge word={NOBODY_HOLDS} tone="warn" attr="data-stuck" />}
            </span>
            <span className="text-[12.5px] leading-snug text-muted-foreground">{deciderWhy(d, r.username || r.user)}</span>
          </div>
        );
      },
    },
    {
      id: "state",
      accessorFn: (r) => PHASE_WORD[phaseOf(r)],
      header: plain<ApprovalRow>(REQUEST_COLUMN.state),
      cell: ({ row }) => {
        const r = row.original;
        const phase = phaseOf(r, now);
        const waits = phase === "waiting";
        const hold = kindOf(r) === "hold";
        // A waiting row leads with its kind, since a hold blocks a call
        // right now and a ticket does not. A decided row leads with its
        // phase word.
        return (
          <div className="flex flex-col gap-0.5 whitespace-normal">
            <span className="flex flex-wrap items-center gap-1.5" data-phase={phase}>
              {waits
                ? <WordBadge word={KIND_WORD[kindOf(r)]} tone={hold ? "warn" : "teal"} mono attr="data-kind" />
                : <WordBadge word={PHASE_WORD[phase]} tone={phaseTone(phase)} />}
              {waits && (
                <span className={cn("text-base font-semibold tabular-nums", hold ? "text-warn" : "text-foreground")} data-left>
                  {timeLeft(r, now)}
                </span>
              )}
            </span>
            {waits && hold && (
              // The bar repeats the time left beside it, so it is hidden
              // from assistive technology. It steps with the tab's clock.
              <span className="my-0.5 block h-1 overflow-hidden rounded-full bg-border" aria-hidden="true" data-drain>
                <span className="block h-full bg-warn" style={{ width: holdShare(r, now) + "%" }} />
              </span>
            )}
            <span className="text-[12.5px] leading-snug text-muted-foreground" data-state-line>{stateLine(r, phase, now)}</span>
          </div>
        );
      },
    },
    {
      id: "act",
      header: plain<ApprovalRow>(""),
      enableHiding: false,
      cell: ({ row }) => {
        const r = row.original;
        if (r.state !== "pending") return null;
        if (isMine(r) && needsDevice && needsDevice(r)) {
          return (
            <span className="block text-right text-[12.5px] leading-snug whitespace-normal text-muted-foreground" data-own-request>
              {OWN_REQUEST} <a href={SELF_SERVICE_PAGE} className="text-link underline-offset-4 hover:underline" onClick={(e) => e.stopPropagation()}>{OPEN_SELF_SERVICE}</a>
            </span>
          );
        }
        if (isMine(r)) {
          return (
            <span className="flex items-center justify-end gap-1.5">
              <Button variant="outline" size="sm" className={DANGER_BUTTON} aria-label={decideAction("deny", r)} onClick={(e) => { e.stopPropagation(); setAsk({ row: r, verdict: "deny" }); }}>{DENY}</Button>
              <Button size="sm" aria-label={decideAction("approve", r)} onClick={(e) => { e.stopPropagation(); setAsk({ row: r, verdict: "approve" }); }}>{APPROVE}</Button>
            </span>
          );
        }
        const d = decidersOf(r);
        const first = [...d.users, ...d.roles][0] || deciderWords(d);
        return <span className="block text-right text-[12.5px] text-muted-foreground" data-decides>{isStuck(r) ? NOBODY_CAN : decidesWord(first)}</span>;
      },
    },
  ];

  const filters = (
    <>
      <div role="group" aria-label={FILTER_LABEL} className="flex items-center">
        {FILTERS.map((f) => (
          <Button
            key={f.key}
            variant="outline"
            size="sm"
            aria-pressed={filter === f.key}
            onClick={() => setFilter(f.key)}
            className={cn("-ml-px h-9 rounded-none first:ml-0 first:rounded-l-md last:rounded-r-md", filter === f.key && "bg-accent-bg text-foreground")}
          >
            {f.label}
          </Button>
        ))}
      </div>
      <Input
        id="requests-search"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        placeholder={SEARCH_REQUESTS}
        aria-label={SEARCH_LABEL}
        className="h-9 w-full max-w-sm"
      />
    </>
  );

  const count = (shown: number) => (filter === "waiting" ? waitingCount(pending.length, mineCount) : filter === "decided" ? decidedCount(decided.length) : requestsCount(shown));
  const searching = search.trim() !== "";
  const empties = filter === "decided"
    ? { title: EMPTY_DECIDED_TITLE, body: EMPTY_DECIDED_BODY }
    : { title: EMPTY_WAITING_TITLE, body: EMPTY_WAITING_BODY };

  if (waiting === null && problem) return <FetchError subject={SUBJECT_REQUESTS} detail={problem} />;
  if (waiting === null) return <p className="text-sm text-muted-foreground">{READING_REQUESTS}</p>;

  const listProblem = problem || (state.kind === "error" ? state.message : state.kind === "ready" ? state.problem : null);

  return (
    <div className="flex flex-col gap-4">
      {listProblem && <FetchError subject={SUBJECT_REQUESTS} detail={listProblem} lastRead={lastRead} />}

      {rows.length === 0 && !searching ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {filters}
            <span className="text-[13px] text-muted-foreground" data-row-count>{count(0)}</span>
          </div>
          <EmptyState icon={CircleCheckIcon} title={empties.title}>{empties.body}</EmptyState>
        </>
      ) : (
        <DataTable<ApprovalRow>
          rows={rows}
          columns={columns}
          labels={LABELS}
          rowKey={(r) => r.id}
          rowName={(r) => callOf(r.summary).call + " for " + (r.username || r.user)}
          dataAttr="data-request"
          onOpen={setOpenRow}
          openKey={openRow ? openRow.id : null}
          filterBar={filters}
          count={count}
          emptyText={searching ? EMPTY_SEARCH : empties.body}
          globalFilter={search}
          widths={WIDTHS}
          foot={filter === "waiting" ? undefined : (
            <div className="flex flex-wrap items-center justify-center gap-3">
              <span className="text-[13px] text-muted-foreground" data-loaded>{loadedLine(paged.length)}</span>
              {more && (
                <Button variant="outline" size="sm" onClick={() => void loadMore()} disabled={busy}>
                  {busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
                </Button>
              )}
            </div>
          )}
        />
      )}

      {openRow && (
        <ApprovalDialog
          row={fix(openRow)}
          open
          onOpenChange={(o) => { if (!o) setOpenRow(null); }}
          seat={seat}
          holders={holders}
          isMine={isMine(openRow)}
          needsDevice={!!needsDevice && needsDevice(openRow)}
          isStuck={isStuck(openRow)}
          onDecide={(r, verdict) => setAsk({ row: r, verdict })}
          doors={source.doors}
        />
      )}

      <ApprovalDecide ask={ask} onClose={() => setAsk(null)} onDecided={handleDecided} decide={source.decide} get={source.get} />
    </div>
  );
}
