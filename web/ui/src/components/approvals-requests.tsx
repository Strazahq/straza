import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { CircleCheckIcon, HourglassIcon, Loader2Icon, TriangleAlertIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ApprovalDialog, type BeforeSetup, type DoorOpener, consoleDoors } from "@/components/approval-dialog";
import { RoleChip } from "@/components/status-badge";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError } from "@/components/error-state";
import { Chip, OutcomeChip, Tag } from "@/components/chip";
import { KIND_ICON, KindTile } from "@/components/kind-icon";
import { Segmented } from "@/components/segmented";
import { type ApiError, type ApprovalRow, type Page, type RoleRow, decideApproval, getApproval, listApprovals, listRoles, pageApprovals } from "@/lib/api";
import { type Call, type Phase, type Seat, callOf, decidersOf, holdShare, kindOf, phaseOf, requesterKindOf, shortID, stuck, timeLeft } from "@/lib/approval-model";
import {
  APPROVE,
  DECIDES_HELP,
  DENY,
  EMPTY_DECIDED_TITLE,
  EMPTY_SEARCH,
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
  YOU_SPONSOR,
  askVerb,
  atZero,
  decidedCount,
  decidedLine,
  decideAction,
  deciderWhy,
  deciderWords,
  decidesWord,
  loadedLine,
  outcomeOf,
  phaseTone,
  requestsCount,
  ON_SERVER,
  runsWhere,
  runsWhereAlone,
  sponsoredWords,
  stateLine,
  waitingCount,
} from "@/lib/approval-words";
import { usePagedList } from "@/lib/paged";
import { readFailed } from "@/lib/say";
import { absTime, agoWord } from "@/lib/words";
import { cn } from "@/lib/utils";

// RequestsTab is the queue of requests, waiting or decided, that the
// Approvals area and the self-service page share. A row opens the request
// dialog, and so do its own Deny and Approve, since the dialog is the one
// place a request is decided. Below the sm breakpoint each request is a
// card.

const POLL_MS = 5000;
const TICK_MS = 1000;
const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
// The widths leave the Call column the rest of the page: 163 px on a
// 1280 px page and more on a wider one. The table's floor keeps that column
// from collapsing on a narrower page, where the table scrolls instead.
const WIDTHS: Record<string, string> = { asked: "80px", who: "208px", decides: "192px", state: "207px", act: "160px" };
const FLOOR = "992px";
// The self-service page's columns fit a 768 px window with room for Call.
const SELF_WIDTHS: Record<string, string> = { asked: "80px", who: "184px", state: "168px", act: "168px" };
const SELF_FLOOR = "700px";
const LABELS: Record<string, string> = {
  asked: REQUEST_COLUMN.asked,
  who: REQUEST_COLUMN.who,
  call: REQUEST_COLUMN.call,
  decides: REQUEST_COLUMN.decides,
  state: REQUEST_COLUMN.state,
};
// NARROW is the window under Tailwind's sm breakpoint, where a request draws
// as a card instead of a table row.
const NARROW = "(max-width: 39.98rem)";
// HOME_DECIDED is how many decided requests the home view shows.
const HOME_DECIDED = 3;

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
  // words are the page's own words for an empty Waiting filter and a failed
  // read, or null for the console's. A lane with words of its own throws
  // each failed read as a whole sentence, which shows as one plain banner
  // and is never wrapped in the console's failed-read sentence.
  words: { emptyTitle: string; emptyBody: string; lastRead: (at: string) => string } | null;
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
  words: null,
};

// QueuePage is the page that draws the queue. The console keeps its five
// columns. The self-service page draws the ones a person reads: who asked
// with its tile, the call, the time left or the outcome.
export type QueuePage = "console" | "self-service";

// Home is the self-service home view, drawn in place of the filter, the
// search and the count: the waiting requests under one heading, the newest
// decided ones under another, and a link to the whole list.
export type Home = { waitingHead: string; decidedHead: string; allLabel: string; allHref: string; onAll: () => void };

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
  // list holds the row, and again each time the address names another one.
  openID?: string;
  // onClose tells the page the request dialog closed, so an address that
  // names the request can let it go.
  onClose?: () => void;
  // beforeSetup is the page's hook while no device of the reader is set up:
  // a row the reader may decide ends in the page's words, the dialog offers
  // the setup, and only the waiting requests are listed, with no filter.
  beforeSetup?: BeforeSetup;
  // onChanged tells the page a decision landed, so the counts re-read.
  onChanged: () => void;
  // page is the page that draws the queue, the console where none is named.
  page?: QueuePage;
  // here is this browser's id as an approver device, which the self-service
  // page's decided line names as this browser.
  here?: string;
  // home draws the self-service home view instead of the filtered list.
  home?: Home;
};

// WhereChip names where the call goes: a server, or the hook's shell,
// files or network, each as a tag. Where the call runs sits on the
// tooltip, since the chip is the only place a row says it.
function WhereChip({ where, kind, runs }: { where: string; kind: Call["whereKind"]; runs: string }) {
  if (!where) return null;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default" data-where={where}>
          {kind === "server"
            ? <Tag icon={KIND_ICON.server} className="mr-1">{where}</Tag>
            : <RoleChip name={where} />}
        </span>
      </TooltipTrigger>
      <TooltipContent>{runs}</TooltipContent>
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

// Drain is the thin bar that drains with the time left. It repeats the time
// beside it, so it is hidden from assistive technology, and it steps with
// the queue's clock.
function Drain({ share }: { share: number }) {
  return (
    <span className="block h-1 overflow-hidden rounded-pill bg-border" aria-hidden="true" data-drain>
      <span className="block h-full bg-warn" style={{ width: share + "%" }} />
    </span>
  );
}

// useNarrow says whether the window is under the sm breakpoint, and follows
// the window as it changes.
function useNarrow(): boolean {
  const [narrow, setNarrow] = React.useState(() => window.matchMedia(NARROW).matches);
  React.useEffect(() => {
    const mq = window.matchMedia(NARROW);
    const on = () => setNarrow(mq.matches);
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);
  return narrow;
}

// rowName is a request's name on its row and its card: "Open <rowName>".
const rowName = (r: ApprovalRow) => callOf(r.summary).call + " for " + (r.username || r.user);

// matches reads a search the way the table's filter does, over the values
// of the console's columns, so a card finds what a row finds.
function matches(r: ApprovalRow, search: string): boolean {
  const q = search.toLowerCase();
  const c = callOf(r.summary);
  const d = decidersOf(r);
  return [r.createdAt, r.username || r.user, c.where + " " + c.call, [...d.users, ...d.roles, r.rule, r.set].join(" "), PHASE_WORD[phaseOf(r)]]
    .some((v) => v.toLowerCase().includes(q));
}

// RequesterTile is the kind tile before who asked, drawn where the record
// says whether an AI agent or a person asked.
function RequesterTile({ row }: { row: ApprovalRow }) {
  const kind = requesterKindOf(row);
  return kind ? <KindTile kind={kind} size="sm" /> : null;
}

export function RequestsTab({ seat, source = adminSource, isMine, needsDevice, openID, onClose, beforeSetup, onChanged, page = "console", here = "", home }: RequestsProps) {
  const [waiting, setWaiting] = React.useState<ApprovalRow[] | null>(null);
  const [lastRead, setLastRead] = React.useState<Date | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  // pageWhy is the lane's own sentence of the last failed history read, for
  // a lane with words of its own.
  const [pageWhy, setPageWhy] = React.useState<string | null>(null);
  const [holders, setHolders] = React.useState<Record<string, number>>({});
  // fresh holds the records a decision handed back, so a decided row reads
  // as decided before either list is read again.
  const [fresh, setFresh] = React.useState<Record<string, ApprovalRow>>({});
  const [filter, setFilter] = React.useState<Filter>(openID ? "all" : "waiting");
  // view is the filter the list follows. Before setup no decided request is
  // read, so it is Waiting alone.
  const view: Filter = beforeSetup ? "waiting" : filter;
  const [search, setSearch] = React.useState("");
  const [openRow, setOpenRow] = React.useState<ApprovalRow | null>(null);
  const [now, setNow] = React.useState(() => Date.now());
  const narrow = useNarrow();
  const self = page === "self-service";
  const words = source.words;

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
      setProblem(source.words ? err.message : readFailed(SUBJECT_REQUESTS, err));
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

  // The paged list words its own failure through the console's sentence,
  // so the lane's sentence is kept here for a lane with words of its own.
  const readPage = React.useCallback(async (q: string) => {
    try {
      return await source.page(q);
    } catch (e) {
      setPageWhy((e as Error).message);
      throw e;
    }
  }, [source]);

  const { state, busy, reload, loadMore } = usePagedList<ApprovalRow>({
    read: readPage,
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
  const rows = view === "waiting" ? pending : view === "decided" ? decided : [...pending, ...paged.filter((r) => !held.has(r.id))];
  const mineCount = pending.filter((r) => isMine(r)).length;
  const more = state.kind === "ready" && state.more;

  // wanted is the request the address named, waiting for its row to load
  // under any filter. named is the last name the address gave, so each new
  // name opens its request once, and a name that is already open opens
  // nothing.
  const wanted = React.useRef(openID || "");
  const named = React.useRef(openID || "");
  React.useEffect(() => {
    if ((openID || "") !== named.current) {
      named.current = openID || "";
      wanted.current = openID && !(openRow && openRow.id === openID) ? openID : "";
    }
    if (!wanted.current) return;
    const hit = [...pending, ...paged].find((r) => r.id === wanted.current);
    if (hit) { wanted.current = ""; setOpenRow(hit); }
  }, [pending, paged, openID, openRow]);

  // A decision of ours closes the dialog from inside it. A decision someone
  // else made first hands its record back too, and the dialog stays open on
  // the answer that says so.
  const handleDecided = (row: ApprovalRow) => {
    setFresh((f) => ({ ...f, [row.id]: row }));
    void readWaiting();
    void reload();
    onChanged();
  };

  // decideButton is a row's or a card's own Deny or Approve. It opens the
  // request dialog, which shows what runs and what each answer does.
  const decideButton = (r: ApprovalRow, v: Verdict, className?: string) => (
    <Button
      variant={v === "deny" ? "outline" : "default"}
      size="sm"
      className={cn(v === "deny" && DANGER_BUTTON, className)}
      aria-label={decideAction(v, r)}
      onClick={(e) => { e.stopPropagation(); setOpenRow(r); }}
    >
      {v === "deny" ? DENY : APPROVE}
    </Button>
  );

  // lastCell is what ends a waiting row or card: the two buttons where the
  // seat may decide, the door to the page that signs for the seat's own
  // request, the page's words before setup, or who decides.
  const lastCell = (r: ApprovalRow, card: boolean): React.ReactNode => {
    if (r.state !== "pending") return null;
    const side = card ? "text-left" : "text-right";
    if (isMine(r) && needsDevice && needsDevice(r)) {
      return (
        <span className={cn("block text-caption leading-snug whitespace-normal text-muted-foreground", side)} data-own-request>
          {OWN_REQUEST} <a href={SELF_SERVICE_PAGE} className="text-link underline-offset-4 hover:underline" onClick={(e) => e.stopPropagation()}>{OPEN_SELF_SERVICE}</a>
        </span>
      );
    }
    if (isMine(r) && beforeSetup) {
      return <span className={cn("block text-caption leading-snug whitespace-normal text-muted-foreground", side)} data-setup-to-answer>{beforeSetup.words.row}</span>;
    }
    if (isMine(r)) {
      return card
        ? <span className="grid grid-cols-2 gap-2">{decideButton(r, "deny", "h-11 w-full")}{decideButton(r, "approve", "h-11 w-full")}</span>
        : <span className="flex items-center justify-end gap-1.5">{decideButton(r, "deny")}{decideButton(r, "approve")}</span>;
    }
    const d = decidersOf(r);
    const first = [...d.users, ...d.roles][0] || deciderWords(d);
    return <span className={cn("block text-caption text-muted-foreground", side)} data-decides>{isStuck(r) ? NOBODY_CAN : decidesWord(first)}</span>;
  };

  // sponsorWords is the muted part after the requester's name on the
  // self-service page and the cards: who sponsors it, when the record names
  // a sponsor.
  const sponsorWords = (r: ApprovalRow) => {
    const d = decidersOf(r);
    const sponsor = d.kind === "sponsor" || d.kind === "both" ? d.users[0] : "";
    return sponsor ? (sponsor === seat.user ? YOU_SPONSOR : sponsoredWords(sponsor)) : "";
  };

  // outcomeLine is a decided request's outcome as the page reads it: the
  // phase word and the State line on the console, the outcome chip and who
  // answered from where on the self-service page.
  const outcomeLine = (r: ApprovalRow, phase: Phase) => (self ? (
    <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 whitespace-normal" data-outcome-cell>
      <OutcomeChip outcome={outcomeOf(phase)} />
      <span className="text-caption leading-snug text-muted-foreground" data-outcome-line>{decidedLine(r, phase, seat.user, here)}</span>
    </div>
  ) : (
    <div className="flex flex-col items-start gap-0.5 whitespace-normal">
      <Chip tone={phaseTone(phase)} data-phase={phase}>{PHASE_WORD[phase]}</Chip>
      <span className="text-caption leading-snug text-muted-foreground" data-state-line>{stateLine(r, phase, now)}</span>
    </div>
  ));

  const asked: ColumnDef<ApprovalRow> = {
    id: "asked",
    accessorFn: (r) => r.createdAt,
    header: plain<ApprovalRow>(REQUEST_COLUMN.asked),
    cell: ({ row }) => <Stamp iso={row.original.createdAt} />,
  };
  const call: ColumnDef<ApprovalRow> = {
    id: "call",
    accessorFn: (r) => { const c = callOf(r.summary); return c.where + " " + c.call; },
    header: plain<ApprovalRow>(REQUEST_COLUMN.call),
    cell: ({ row }) => {
      const c = callOf(row.original.summary);
      return (
        <span className="flex flex-wrap items-center gap-1 whitespace-normal [overflow-wrap:anywhere]">
          <WhereChip where={c.where} kind={c.whereKind} runs={runsWhereAlone(row.original)} />
          <span className="font-mono">{c.call}</span>
        </span>
      );
    },
  };
  const act = (search: boolean): ColumnDef<ApprovalRow> => ({
    id: "act",
    // The self-service page has no Who decides column, so its last column
    // carries the deciders, the rule and the policy for the search.
    ...(search ? { accessorFn: (r: ApprovalRow) => { const d = decidersOf(r); return [...d.users, ...d.roles, r.rule, r.set].join(" "); } } : {}),
    header: plain<ApprovalRow>(""),
    enableHiding: false,
    cell: ({ row }) => lastCell(row.original, false),
  });

  const consoleColumns: ColumnDef<ApprovalRow>[] = [
    asked,
    {
      id: "who",
      accessorFn: (r) => r.username || r.user,
      header: plain<ApprovalRow>(REQUEST_COLUMN.who),
      cell: ({ row }) => (
        <div className="min-w-0">
          <div className="whitespace-normal font-semibold text-foreground [overflow-wrap:anywhere]">{row.original.username || row.original.user}</div>
          <div className="truncate font-mono text-caption text-muted-foreground">{shortID(row.original.session)}</div>
        </div>
      ),
    },
    call,
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
              {r.state === "pending" && isMine(r) && <Chip tone="accent" data-yours={YOURS}>{YOURS}</Chip>}
              {isStuck(r) && <Chip tone="warn" data-stuck={NOBODY_HOLDS}>{NOBODY_HOLDS}</Chip>}
            </span>
            <span className="text-caption leading-snug text-muted-foreground">{deciderWhy(d, r.username || r.user)}</span>
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
                ? <Chip tone={hold ? "warn" : "plain"} icon={KIND_ICON[kindOf(r)]} data-kind={KIND_WORD[kindOf(r)]}>{KIND_WORD[kindOf(r)]}</Chip>
                : <Chip tone={phaseTone(phase)}>{PHASE_WORD[phase]}</Chip>}
              {waits && (
                <span className={cn("text-body leading-6 font-semibold tabular-nums", hold ? "text-warn" : "text-foreground")} data-left>
                  {timeLeft(r, now)}
                </span>
              )}
            </span>
            {waits && hold && (
              // The bar repeats the time left beside it, so it is hidden
              // from assistive technology. It steps with the tab's clock.
              <span className="my-0.5 block h-1 overflow-hidden rounded-pill bg-border" aria-hidden="true" data-drain>
                <span className="block h-full bg-warn" style={{ width: holdShare(r, now) + "%" }} />
              </span>
            )}
            <span className="text-caption leading-snug text-muted-foreground" data-state-line>{stateLine(r, phase, now)}</span>
          </div>
        );
      },
    },
    act(false),
  ];

  // The self-service page's columns. The fourth holds the time left of a
  // waiting row and the outcome of a decided one, under the header the
  // shown rows need.
  const selfWho: ColumnDef<ApprovalRow> = {
    id: "who",
    accessorFn: (r) => r.username || r.user,
    header: plain<ApprovalRow>(REQUEST_COLUMN.who),
    cell: ({ row }) => {
      const r = row.original;
      const sponsor = sponsorWords(r);
      return (
        <div className="flex min-w-0 items-start gap-2 whitespace-normal">
          <RequesterTile row={r} />
          <div className="min-w-0">
            <div className="font-mono text-foreground [overflow-wrap:anywhere]" data-who-name>{r.username || r.user}</div>
            {sponsor && <div className="text-caption leading-snug text-muted-foreground" data-sponsor>{sponsor}</div>}
          </div>
        </div>
      );
    },
  };
  const selfState = (head: string): ColumnDef<ApprovalRow> => ({
    id: "state",
    accessorFn: (r) => PHASE_WORD[phaseOf(r)],
    header: plain<ApprovalRow>(head),
    cell: ({ row }) => {
      const r = row.original;
      const phase = phaseOf(r, now);
      if (phase !== "waiting") return outcomeLine(r, phase);
      return (
        <div className="flex flex-col gap-1 whitespace-normal" data-phase={phase}>
          <span className="flex items-center gap-1.5 tabular-nums text-foreground">
            <HourglassIcon className="size-4 shrink-0 text-warn" aria-hidden="true" />
            <span data-left>{timeLeft(r, now)}</span>
          </span>
          <Drain share={holdShare(r, now)} />
        </div>
      );
    },
  });
  const selfLabels = (head: string) => ({ asked: REQUEST_COLUMN.asked, who: REQUEST_COLUMN.who, call: REQUEST_COLUMN.call, state: head });
  const stateHead = view === "waiting" ? REQUEST_COLUMN.left : REQUEST_COLUMN.outcome;
  const columns = self ? [asked, selfWho, call, selfState(stateHead), act(true)] : consoleColumns;
  const labels = self ? selfLabels(stateHead) : LABELS;

  // card is one request on a narrow window, read top to bottom: who asked,
  // what it wants to run and where, the time left, then the two buttons. A
  // decided request's card keeps who asked, the call and the outcome.
  const card = (r: ApprovalRow, muted = false) => {
    const phase = phaseOf(r, now);
    const c = callOf(r.summary);
    const name = rowName(r);
    const open = () => setOpenRow(r);
    return (
      <div
        key={r.id}
        role="button"
        tabIndex={0}
        aria-label={"Open " + name}
        data-request={name}
        data-card
        className={cn("flex min-w-0 cursor-pointer flex-col gap-3 rounded-md border border-border bg-card p-4 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50", muted && "text-muted-foreground", openRow && openRow.id === r.id && "bg-accent-bg")}
        onClick={open}
        onKeyDown={(e) => { if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); open(); } }}
      >
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <RequesterTile row={r} />
          <span className="min-w-0 font-mono font-semibold text-foreground [overflow-wrap:anywhere]">{r.username || r.user}</span>
          <span className="text-dense text-muted-foreground" data-card-meta>{[sponsorWords(r), agoWord(r.createdAt)].filter(Boolean).join(" · ")}</span>
        </div>
        {phase === "waiting" && <span className="-mb-2 text-body text-text-2" data-card-ask>{askVerb(r)}</span>}
        <span className="line-clamp-3 font-mono text-section break-all text-foreground" title={c.call} data-card-call>{c.call}</span>
        {phase === "waiting" ? (
          <>
            <span className="-mt-2 text-dense text-muted-foreground [overflow-wrap:anywhere]" data-card-where>{c.whereKind === "server" ? ON_SERVER + " " + c.where : [c.where, runsWhere(r)].filter(Boolean).join(", ")}</span>
            <div className="flex flex-col gap-1.5 rounded-md border border-warn bg-warn-bg px-3 py-2" data-card-time>
              <span className="flex items-center gap-2 text-body text-foreground">
                <HourglassIcon className="size-4 shrink-0 text-warn" aria-hidden="true" />
                <span>{timeLeft(r, now) + ", " + atZero(r)}</span>
              </span>
              <Drain share={holdShare(r, now)} />
            </div>
            {lastCell(r, true)}
          </>
        ) : outcomeLine(r, phase)}
      </div>
    );
  };

  const dialog = openRow && (
    <ApprovalDialog
      row={fix(openRow)}
      open
      onOpenChange={(o) => { if (!o) { setOpenRow(null); if (onClose) onClose(); } }}
      seat={seat}
      holders={holders}
      isMine={isMine(openRow)}
      needsDevice={!!needsDevice && needsDevice(openRow)}
      isStuck={isStuck(openRow)}
      decide={source.decide}
      get={source.get}
      onDecided={handleDecided}
      doors={source.doors}
      beforeSetup={beforeSetup}
    />
  );

  // banner is a failed read: the console's block with its subject, or on a
  // page with words of its own one plain banner with the lane's sentence and
  // the time of the last read that worked.
  const banner = (sentence: string, at: Date | null) => (words ? (
    <div role="alert" className="flex items-start gap-2.5 rounded-md border border-input bg-secondary px-3.5 py-2.5 text-dense leading-relaxed text-text-2" data-read-problem>
      <TriangleAlertIcon className="mt-1 size-4 shrink-0" aria-hidden="true" />
      <p className="m-0">{sentence}{at ? " " + words.lastRead(at.toLocaleTimeString([], { hour12: false })) : ""}</p>
    </div>
  ) : <FetchError subject={SUBJECT_REQUESTS} detail={sentence} lastRead={at} />);

  const filters = (
    <>
      {!beforeSetup && <Segmented label={FILTER_LABEL} value={filter} onChange={setFilter} options={FILTERS.map((f) => ({ value: f.key, label: f.label }))} />}
      <Input
        id="requests-search"
        value={search}
        onChange={(e) => setSearch(e.target.value)}
        placeholder={self ? SEARCH_LABEL : SEARCH_REQUESTS}
        aria-label={SEARCH_LABEL}
        className="h-9 w-full max-w-sm"
      />
    </>
  );

  const count = (shown: number) => (view === "waiting" ? waitingCount(pending.length, mineCount) : view === "decided" ? decidedCount(decided.length) : requestsCount(shown));
  const searching = search.trim() !== "";
  const empties = view === "decided"
    ? { title: EMPTY_DECIDED_TITLE, body: "" }
    : words ? { title: words.emptyTitle, body: words.emptyBody } : { title: EMPTY_WAITING_TITLE, body: "" };

  if (waiting === null && problem) return banner(problem, null);
  if (waiting === null) return <p className="text-dense text-muted-foreground">{READING_REQUESTS}</p>;

  const pageProblem = state.kind === "error" ? state.message : state.kind === "ready" ? state.problem : null;
  const listProblem = problem || (pageProblem && words ? pageWhy || pageProblem : pageProblem);

  const table = (shown: ApprovalRow[], cols: ColumnDef<ApprovalRow>[], extra: Partial<React.ComponentProps<typeof DataTable<ApprovalRow>>> = {}) => (
    <DataTable<ApprovalRow>
      rows={shown}
      columns={cols}
      labels={labels}
      rowKey={(r) => r.id}
      rowName={rowName}
      dataAttr="data-request"
      onOpen={setOpenRow}
      openKey={openRow ? openRow.id : null}
      emptyText={searching ? EMPTY_SEARCH : empties.body}
      widths={self ? SELF_WIDTHS : WIDTHS}
      minWidth={self ? SELF_FLOOR : FLOOR}
      {...extra}
    />
  );

  if (home && words) {
    const lately = decided.slice(0, HOME_DECIDED);
    return (
      <div className="flex flex-col gap-4">
        {listProblem && banner(listProblem, lastRead)}
        {/* With nothing waiting the part is left out, since the Requests card above already says so. */}
        {pending.length > 0 && (
          <section className="flex flex-col gap-3" data-home-waiting>
            <h2 className="text-section font-semibold text-foreground">{home.waitingHead}</h2>
            {narrow
              ? <div className="flex flex-col gap-3">{pending.map((r) => card(r))}</div>
              : table(pending, [asked, selfWho, call, selfState(REQUEST_COLUMN.left), act(true)], { labels: selfLabels(REQUEST_COLUMN.left), columnsMenu: false })}
          </section>
        )}
        {lately.length > 0 && (
          <section className="flex flex-col gap-3" data-home-decided>
            <h2 className="text-section font-semibold text-foreground">{home.decidedHead}</h2>
            {narrow
              ? <div className="flex flex-col gap-3">{lately.map((r) => card(r, true))}</div>
              : table(lately, [asked, selfWho, call, selfState(REQUEST_COLUMN.outcome)], { labels: selfLabels(REQUEST_COLUMN.outcome), rowClassName: () => "text-muted-foreground", columnsMenu: false })}
            <a href={home.allHref} className="self-start text-dense text-link underline-offset-4 hover:underline" onClick={(e) => { e.preventDefault(); home.onAll(); }} data-all-requests>{home.allLabel}</a>
          </section>
        )}
        {dialog}
      </div>
    );
  }

  // The self-service page leaves the loaded line out, since the count over
  // the table already says how many rows are shown.
  const foot = view === "waiting" || (self && !more) ? undefined : (
    <div className="flex flex-wrap items-center justify-center gap-3">
      {!self && <span className="text-caption text-muted-foreground" data-loaded>{loadedLine(paged.length)}</span>}
      {more && (
        <Button variant="outline" size="sm" onClick={() => void loadMore()} disabled={busy}>
          {busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
        </Button>
      )}
    </div>
  );
  const found = narrow ? rows.filter((r) => matches(r, search)) : rows;

  return (
    <div className="flex flex-col gap-4">
      {listProblem && banner(listProblem, lastRead)}

      {rows.length === 0 && !searching ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {filters}
            <span className="text-caption text-muted-foreground" data-row-count>{count(0)}</span>
          </div>
          <EmptyState icon={CircleCheckIcon} title={empties.title}>{empties.body}</EmptyState>
        </>
      ) : narrow ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {filters}
            <span className="text-caption text-muted-foreground" data-row-count>{count(found.length)}</span>
          </div>
          {found.length === 0
            ? <p className="py-4 text-center text-dense text-muted-foreground" data-empty-text>{EMPTY_SEARCH}</p>
            : <div className="flex flex-col gap-3" data-cards>{found.map((r) => card(r))}</div>}
          {foot}
        </>
      ) : table(rows, columns, { filterBar: filters, count, globalFilter: search, foot, columnsMenu: !self })}

      {dialog}
    </div>
  );
}
