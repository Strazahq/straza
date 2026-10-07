import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { DownloadIcon, Loader2Icon, RefreshCwIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { DataTable, plain } from "@/components/data-table";
import { FetchError, RefusedError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { ToneBadge, RecordSheet } from "@/components/record-sheet";
import { type PickedUser, UserPicker } from "@/components/user-picker";
import { type ApiError, type AuditRow, listAudit, overview, query, revokeSession } from "@/lib/api";
import {
  CHAIN_TIP, CHAIN_UNVERIFIED, CHAIN_WORD, CHECKING_TEXT, CHECKING_TIP, CHECKING_WORD, EMPTY_CHAIN, EMPTY_LENS, EMPTY_LOADING, EMPTY_SEARCH, EMPTY_UNREAD, LENSES, type Row,
  SEARCH_TIP, SEARCH_WORD, TAIL_KEEP, TAIL_WINDOW, UNREAD_TEXT, UNREAD_WORD, UNVERIFIED_WORD,
  ceRow, chainBroken, chainVerified, csvOf, effectTone, effectWord, foldAdjacent, jsonlOf, loadedWords,
  matchWords, revokeBody, revokedWords, searchLine, sevTone, shortType, shownWords, typeMatches,
} from "@/lib/audit-words";
import { canHash, verifyChain } from "@/lib/chain";
import { take } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { routeByKey } from "@/lib/routes";
import { readFailed, refused } from "@/lib/say";
import { NONE, absTime } from "@/lib/words";
import { cn, downloadText } from "@/lib/utils";

// The Audit screen: the signed chain
// as a live tail that this browser re-hashes as it arrives, a lens and a
// server search over it, and one door per record. The audit endpoint
// answers a bare array rather than the paged envelope, so the tail state
// lives here instead of in usePagedList.

const route = routeByKey("audit");

// The tail polls every second and takes up to 1000 new records a tick,
// which is more than a busy deployment writes in that time. A search reads
// the whole chain, so it is read again every 3 s only.
const TAIL_MS = 1000;
const SEARCH_MS = 3000;
const TAIL_TICK = 1000;

// A tail batch comes on screen in at most this many steps, which end inside
// one tail interval. A batch with more records moves several a step.
const REVEAL_STEPS = 10;

const CAPS = "text-[13px] text-muted-foreground";

const LABEL: Record<string, string> = { seq: "Seq", time: "Time", who: "Who", type: "Type", effect: "Effect", what: "What", reason: "Reason" };

// WIDTHS fixes the columns, so the table fits a 1280 px viewport and the
// header boxes stay put while rows arrive. Reason takes what is left.
const WIDTHS: Record<string, string> = { seq: "68px", time: "84px", who: "236px", type: "88px", effect: "96px", what: "28%" };

const EFFECTS: { key: string; label: string }[] = [
  { key: "", label: "any effect" },
  { key: "allow", label: "allow" },
  { key: "deny", label: "deny" },
];

// Tail is the verified window: the records in ascending order, the head of
// the chain the count reads, and the first record that failed re-hashing.
type Tail = { rows: AuditRow[]; head: number; broken: number; lastRead: Date | null; problem: string | null };
type Hits = { rows: AuditRow[]; lastRead: Date | null; problem: string | null };

const NO_TAIL: Tail = { rows: [], head: 0, broken: 0, lastRead: null, problem: null };
const NO_HITS: Hits = { rows: [], lastRead: null, problem: null };

// clockTime is the wall clock part of a stamp in the active zone.
const clockTime = (iso: string) => absTime(iso).slice(11, 19);

// checked re-hashes a batch the way internal/audit.Link wrote it and
// answers the seq of the first record that fails, 0 when it holds. A
// browser without WebCrypto checks nothing, and the status line says so.
async function checked(rows: AuditRow[], startPrev: string | null): Promise<number> {
  if (!canHash || rows.length === 0) return 0;
  const v = await verifyChain(rows, startPrev);
  return v.ok ? 0 : v.brokenSeq;
}

// oneType is the type parameter the server search takes when the lens
// names exactly one prefix. A wider lens narrows in the browser instead,
// because the endpoint takes one type.
function oneType(lensKey: string): string | undefined {
  const lens = LENSES.find((l) => l.key === lensKey);
  return lens && lens.prefixes && lens.prefixes.length === 1 ? lens.prefixes[0] : undefined;
}

const noteOf = (lensKey: string) => LENSES.find((l) => l.key === lensKey)?.note || "";

// StatusLine is the chain or search state above the filter row: one word
// as a badge in its tone, a short count beside it, and the sentence that
// explains both on hover.
function StatusLine({ tone, word, text, tip }: { tone: "ok" | "unknown"; word: string; text: string; tip: string }) {
  return (
    <div role="status" data-chain-line={tone} title={tip} className="flex flex-wrap items-center gap-2 text-[13px]">
      <ToneBadge tone={tone}>{word}</ToneBadge>
      <span className="text-muted-foreground">{text}</span>
    </div>
  );
}

// quiet says a record decided nothing: a prompt, a reply, an admin action
// or a sign-in. Its row sits back in the muted tone, so a decision is the
// brightest thing in the table.
const quiet = (r: Row) => !r.sentinel && !r.effect;

type Cells = {
  onUser: (username: string) => void;
  onFold: (seq: number) => void;
  onRevoke: (row: Row) => void;
  revoked: Set<string>;
};

// columnsOf builds the seven columns. The Who name and the fold toggle are
// buttons inside a row that is itself a door, so each stops the click from
// reaching the row.
function columnsOf(cells: Cells): ColumnDef<Row>[] {
  return [
    {
      id: "seq",
      header: plain("Seq"),
      cell: ({ row }) => <div className={cn("text-right font-mono tabular-nums", quiet(row.original) && "text-muted-foreground")}>{row.original.seq}</div>,
      enableHiding: false,
    },
    {
      id: "time",
      header: plain("Time"),
      cell: ({ row }) => {
        const iso = row.original.time;
        if (!iso) return <span className="text-muted-foreground">{NONE}</span>;
        return (
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="cursor-default font-mono whitespace-nowrap text-text-2">{clockTime(iso)}</span>
            </TooltipTrigger>
            <TooltipContent>{absTime(iso)}</TooltipContent>
          </Tooltip>
        );
      },
    },
    {
      id: "who",
      header: plain("Who"),
      cell: ({ row }) => {
        const name = row.original.username;
        if (!name) return <span className="text-muted-foreground">{NONE}</span>;
        return (
          <Button
            variant="link"
            size="sm"
            className={cn("h-auto max-w-full justify-start p-0 text-left font-mono text-[13px] whitespace-normal [overflow-wrap:anywhere]", quiet(row.original) ? "text-muted-foreground" : "text-link")}
            title="Filter by this user"
            onClick={(e) => { e.stopPropagation(); cells.onUser(name); }}
          >
            {name}
          </Button>
        );
      },
    },
    {
      id: "type",
      header: plain("Type"),
      cell: ({ row }) => <span className="font-mono text-[13px] whitespace-normal text-muted-foreground [overflow-wrap:anywhere]">{shortType(row.original.type)}</span>,
    },
    {
      id: "effect",
      header: plain("Effect"),
      cell: ({ row }) => {
        const r = row.original;
        if (r.sentinel) return <ToneBadge tone={sevTone(r.severity)}>{r.severity}</ToneBadge>;
        const tone = effectTone(r.effect);
        if (tone === "plain") return <span className="text-muted-foreground">{effectWord(r.effect)}</span>;
        // The chip may wrap, so "needs approval" stays inside its column.
        return <ToneBadge tone={tone} className="min-w-15 whitespace-normal">{effectWord(r.effect)}</ToneBadge>;
      },
    },
    {
      id: "what",
      header: plain("What"),
      cell: ({ row }) => {
        const r = row.original;
        return (
          <div className={cn("max-w-[46ch] whitespace-normal [overflow-wrap:anywhere]", quiet(r) ? "text-muted-foreground" : !r.sentinel && "font-semibold text-foreground")}>
            {r.sentinel ? (
              <>
                <b className="font-mono font-semibold text-foreground">{r.detector}</b>{" "}
                <span className="text-muted-foreground">{"sentinel verdict · evidence ×" + r.evidence}</span>
              </>
            ) : (
              <>
                {r.tool && <span className="font-mono text-[13px] font-normal text-muted-foreground">{r.tool + " "}</span>}
                {r.what}
              </>
            )}
            {r.foldN && (
              <Button
                variant="link"
                size="sm"
                className="ml-1.5 h-auto p-0 align-baseline text-[13px] text-link"
                onClick={(e) => { e.stopPropagation(); cells.onFold(r.seq); }}
              >
                {r.foldOpen ? "collapse" : "×" + r.foldN + " identical · show all"}
              </Button>
            )}
          </div>
        );
      },
    },
    {
      id: "reason",
      header: plain("Reason"),
      cell: ({ row }) => {
        const r = row.original;
        const critical = r.sentinel && r.severity === "critical" && !!r.session;
        return (
          <div className="max-w-[44ch] whitespace-normal text-muted-foreground [overflow-wrap:anywhere]">
            {r.reason}
            {critical && (cells.revoked.has(r.session)
              ? <ToneBadge tone="danger" className="ml-1.5">session revoked</ToneBadge>
              : (
                <Button
                  variant="outline"
                  size="sm"
                  className="ml-1.5 border-danger/40 text-danger hover:bg-danger-bg"
                  onClick={(e) => { e.stopPropagation(); cells.onRevoke(r); }}
                >
                  Revoke session
                </Button>
              ))}
          </div>
        );
      },
    },
  ];
}

const denied = (r: Row) => r.effect === "deny" || r.effect === "denied";

// A deny and a critical sentinel wear the danger edge, so a refusal is
// found by shape as well as by hue.
const rowEdge = (r: Row) => (denied(r) || (r.sentinel && r.severity === "critical") ? "border-l-[3px] border-l-danger" : "");

// A denied row is tinted as well. The fill names the stripe and the hover
// too, so the tint holds on an even row and under the pointer.
const DENIED_FILL = "bg-danger-bg even:bg-danger-bg hover:bg-danger/20";

// A row of the batch being revealed fades in, unless the person asked for
// reduced motion.
const ENTER = "motion-safe:animate-in motion-safe:fade-in";

// still says the person asked the system for reduced motion.
const still = () => window.matchMedia("(prefers-reduced-motion: reduce)").matches;

type Preset = { user?: string; session?: string; q?: string };

type Props = {
  // preset opens the screen on one subject, for the doors on the Sessions
  // and Users screens: as a prop from the address, or through the
  // hand-off box when a sheet sends the person here.
  preset?: Preset;
};

// Audit is the chain screen: the live tail with its check, the lens, the
// server search, and the record sheet.
export function Audit({ preset }: Props) {
  const [seed] = React.useState<Preset>(() => preset || take<Preset>("audit") || {});
  const [lens, setLens] = React.useState("all");
  const [effect, setEffect] = React.useState("");
  const [typed, setTyped] = React.useState(seed.q || seed.session || "");
  const [q, setQ] = React.useState(typed.trim());
  const [user, setUser] = React.useState<PickedUser | null>(seed.user ? { id: seed.user, username: seed.user } : null);
  const [tail, setTail] = React.useState<Tail>(NO_TAIL);
  const [hits, setHits] = React.useState<Hits>(NO_HITS);
  const [open, setOpen] = React.useState<Record<number, boolean>>({});
  const [record, setRecord] = React.useState<Row | null>(null);
  const [ask, setAsk] = React.useState<Row | null>(null);
  const [revoked, setRevoked] = React.useState<Set<string>>(new Set());
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [older, setOlder] = React.useState(false);
  const [nonce, setNonce] = React.useState(0);

  // seen is the newest seq on screen. A tail batch is checked and counted
  // when it lands, and its rows then come on screen a few at a time.
  // entered is where that batch began, so its rows alone fade in.
  const [seen, setSeen] = React.useState(0);
  const [entered, setEntered] = React.useState(0);

  const searching = !!(q || effect || user);
  const tailRef = React.useRef(tail);
  tailRef.current = tail;
  const seenRef = React.useRef(seen);
  seenRef.current = seen;
  const pace = React.useRef<ReturnType<typeof setInterval> | undefined>(undefined);
  const reading = React.useRef(false);

  const fail = React.useCallback((e: unknown, subject: string, where: "tail" | "hits") => {
    const err = e as ApiError;
    if (err.status === 401) return;
    const sentence = readFailed(subject, err);
    if (where === "tail") setTail((t) => ({ ...t, problem: sentence }));
    else setHits((h) => ({ ...h, problem: sentence }));
  }, []);

  // reveal moves the screen from the record it shows up to seq to. Paced,
  // it walks there in equal steps whose last one lands before the next
  // tail read, so a batch never waits behind another. Otherwise, and under
  // reduced motion, every record is on screen at once.
  const reveal = React.useCallback((to: number, paced: boolean) => {
    clearInterval(pace.current);
    let at = seenRef.current;
    if (!paced || to <= at || still()) {
      setEntered(to);
      setSeen(to);
      return;
    }
    setEntered(at);
    const steps = Math.min(to - at, REVEAL_STEPS);
    const stride = Math.ceil((to - at) / steps);
    const step = () => {
      at = Math.min(to, at + stride);
      setSeen(at);
      if (at >= to) clearInterval(pace.current);
    };
    step();
    if (at < to) pace.current = setInterval(step, TAIL_MS / steps);
  }, []);

  React.useEffect(() => () => clearInterval(pace.current), []);

  // The first window is read newest first and turned around, because the
  // chain is only checkable in the order it was written.
  const bootstrap = React.useCallback(async () => {
    try {
      const batch = await listAudit(query({ order: "desc", limit: TAIL_WINDOW }));
      const rows = (batch || []).slice().reverse();
      const broken = await checked(rows, null);
      let head = rows.length ? rows[rows.length - 1].seq : 0;
      try {
        const answer = await overview();
        if (answer && answer.audit && answer.audit.head_seq) head = answer.audit.head_seq;
      } catch {
        // The head count falls back to the newest record this window holds.
      }
      setTail({ rows, head, broken, lastRead: new Date(), problem: null });
      reveal(rows.length ? rows[rows.length - 1].seq : 0, false);
    } catch (e) {
      fail(e, "The audit chain", "tail");
    }
  }, [fail, reveal]);

  // A tick reads what landed after the newest record held and checks that
  // it links to it, so an altered record shows up as the tail arrives. The
  // check runs on the whole batch before any of it is revealed, and a batch
  // that fails it is shown at once beside the alert. A tick that finds the
  // read before it still unanswered does nothing, so no record lands twice.
  const poll = React.useCallback(async () => {
    const cur = tailRef.current;
    if (reading.current || !cur.rows.length || cur.broken) return;
    const newest = cur.rows[cur.rows.length - 1];
    reading.current = true;
    try {
      const batch = await listAudit(query({ after: newest.seq, limit: TAIL_TICK }));
      const fresh = batch || [];
      if (!fresh.length) {
        setTail((t) => ({ ...t, lastRead: new Date(), problem: null }));
        return;
      }
      const broken = await checked(fresh, newest.hash || "");
      setTail((t) => ({
        ...t,
        rows: t.rows.concat(fresh).slice(-TAIL_KEEP),
        head: Math.max(t.head, fresh[fresh.length - 1].seq),
        broken: t.broken || broken,
        lastRead: new Date(),
        problem: null,
      }));
      reveal(fresh[fresh.length - 1].seq, !broken);
    } catch (e) {
      fail(e, "The audit chain", "tail");
    } finally {
      reading.current = false;
    }
  }, [fail, reveal]);

  React.useEffect(() => { void bootstrap(); }, [bootstrap, nonce]);

  React.useEffect(() => {
    if (searching) return;
    const t = setInterval(() => { if (!document.hidden) void poll(); }, TAIL_MS);
    return () => clearInterval(t);
  }, [searching, poll]);

  // The server search reads the whole chain, so its answer is not
  // contiguous and is never re-hashed. The tail underneath stays warm.
  React.useEffect(() => {
    if (!searching) { setHits(NO_HITS); return; }
    let alive = true;
    const read = async () => {
      try {
        const batch = await listAudit(query({ order: "desc", limit: TAIL_WINDOW, q, effect, user: user?.username, type: oneType(lens) }));
        if (alive) setHits({ rows: batch || [], lastRead: new Date(), problem: null });
      } catch (e) {
        if (alive) fail(e, "The search", "hits");
      }
    };
    void read();
    const t = setInterval(() => { if (!document.hidden) void read(); }, SEARCH_MS);
    return () => { alive = false; clearInterval(t); };
  }, [searching, q, effect, user, lens, nonce, fail]);

  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), 300);
    return () => clearTimeout(t);
  }, [typed]);

  const loadOlder = async () => {
    const cur = tailRef.current;
    if (older || !cur.rows.length) return;
    setOlder(true);
    const oldest = cur.rows[0];
    try {
      const batch = await listAudit(query({ after: Math.max(0, oldest.seq - TAIL_WINDOW - 1), limit: TAIL_WINDOW }));
      const rows = batch || [];
      let broken = await checked(rows, null);
      // The older window is contiguous with itself. Its join to the window
      // already held is the last hash against the oldest record's link.
      if (!broken && canHash && rows.length && oldest.prevHash && rows[rows.length - 1].hash !== oldest.prevHash) broken = oldest.seq;
      setTail((t) => ({ ...t, rows: rows.concat(t.rows), broken: t.broken || broken, lastRead: new Date(), problem: null }));
    } catch (e) {
      fail(e, "The older records", "tail");
    } finally {
      setOlder(false);
    }
  };

  const revoke = async (row: Row) => {
    setBusy(true);
    setRefusal(null);
    try {
      await revokeSession(row.session);
      setRevoked((s) => new Set(s).add(row.session));
      notify.ok(revokedWords(row.session));
      setAsk(null);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) {
        setRefusal(refused(err));
        notify.failed(refused(err));
      }
    } finally {
      setBusy(false);
    }
  };

  const base = React.useMemo(() => (searching ? hits.rows : tail.rows.slice().reverse()).map(ceRow), [searching, hits.rows, tail.rows]);
  const shown = React.useMemo(() => base.filter((r) => typeMatches(r.type, lens)), [base, lens]);
  // The table holds back the tail records the reveal has not reached. The
  // counts of loaded records and the export read every loaded one.
  const folded = React.useMemo(() => foldAdjacent(searching ? shown : shown.filter((r) => r.seq <= seen), open), [shown, open, searching, seen]);

  // Every callback here only sets state, so the columns are rebuilt when
  // the revoked set moves and at no other time.
  const columns = React.useMemo(() => columnsOf({
    onUser: (username) => setUser({ id: username, username }),
    onFold: (seq) => setOpen((o) => ({ ...o, [seq]: !o[seq] })),
    onRevoke: (row) => { setRefusal(null); setAsk(row); },
    revoked,
  }), [revoked]);

  const status: { tone: "ok" | "unknown"; word: string; text: string; tip: string } | null = searching
    ? { tone: "unknown", word: SEARCH_WORD, text: searchLine(q, effect, user?.username || "", hits.rows.length >= TAIL_WINDOW), tip: SEARCH_TIP }
    : !tail.lastRead
      ? { tone: "unknown", word: tail.problem ? UNREAD_WORD : CHECKING_WORD, text: tail.problem ? UNREAD_TEXT : CHECKING_TEXT, tip: CHECKING_TIP }
      : !canHash
      ? { tone: "unknown", word: UNVERIFIED_WORD, text: CHAIN_UNVERIFIED, tip: CHAIN_UNVERIFIED }
      : tail.broken
        ? null
        : { tone: "ok", word: CHAIN_WORD, text: chainVerified(tail.rows.length, tail.head), tip: CHAIN_TIP };
  const problem = searching ? hits.problem : tail.problem;
  const lastRead = searching ? hits.lastRead : tail.lastRead;
  const note = noteOf(lens);
  // rowLook is a row's edge, tint and enter transition. The open row keeps
  // the fill of the selected state, so it is not tinted.
  const rowLook = (r: Row) => cn(rowEdge(r), denied(r) && record?.seq !== r.seq && DENIED_FILL, !searching && r.seq > entered && ENTER);

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm"><DownloadIcon /> Export loaded rows</Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onSelect={() => downloadText("straza-audit.csv", csvOf(shown), "text/csv")}>CSV</DropdownMenuItem>
                <DropdownMenuItem onSelect={() => downloadText("straza-audit.jsonl", jsonlOf(shown), "application/x-ndjson")}>JSONL</DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
            <Button variant="ghost" size="sm" onClick={() => setNonce((n) => n + 1)} aria-label="Reload the chain">
              <RefreshCwIcon /> Reload
            </Button>
          </>
        }
      />
      <div className="flex flex-col gap-3 px-6 py-5">
        {!searching && tail.broken > 0 && (
          <div role="alert" className="border-l-[3px] border-danger bg-danger-bg px-4 py-3 text-sm leading-relaxed text-foreground">
            {chainBroken(tail.broken)}
          </div>
        )}
        {problem && <FetchError subject={route.label} detail={problem} lastRead={lastRead} />}
        {status && <StatusLine tone={status.tone} word={status.word} text={status.text} tip={status.tip} />}

        <DataTable
          rows={folded}
          columns={columns}
          labels={LABEL}
          rowKey={(r) => String(r.seq)}
          rowName={(r) => String(r.seq)}
          dataAttr="data-seq"
          onOpen={(r) => setRecord(r)}
          openKey={record ? String(record.seq) : null}
          widths={WIDTHS}
          rowClassName={rowLook}
          filterBar={
            <>
              <Select value={lens} onValueChange={setLens}>
                <SelectTrigger size="sm" aria-label="Lens" className="h-9 min-w-44"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {LENSES.map((l) => <SelectItem key={l.key} value={l.key}>{l.label}</SelectItem>)}
                </SelectContent>
              </Select>
              <span className="inline-flex items-center gap-1">
                {EFFECTS.map((e) => (
                  <Button
                    key={e.label}
                    variant="outline"
                    size="sm"
                    aria-pressed={effect === e.key}
                    className={cn("h-9", effect === e.key && "border-link bg-accent-bg text-link")}
                    onClick={() => setEffect(e.key)}
                  >
                    {e.label}
                  </Button>
                ))}
              </span>
              <Input
                value={typed}
                onChange={(ev) => setTyped(ev.target.value)}
                aria-label="Search the chain"
                placeholder="Search the whole chain: command, path, user, rule"
                className="h-9 w-full max-w-sm"
              />
              <UserPicker value={user} onChange={setUser} label="User" />
              {/* The lens note takes the whole last line of the filter row, so it sits under the controls and above the table. */}
              {note && <p className={cn(CAPS, "order-last w-full")}>{note}</p>}
            </>
          }
          count={(n) => (searching ? matchWords(n) : shownWords(n))}
          emptyText={!lastRead ? (problem ? EMPTY_UNREAD : EMPTY_LOADING) : searching ? EMPTY_SEARCH : base.length ? EMPTY_LENS : EMPTY_CHAIN}
          foot={searching || !tail.lastRead ? undefined : (
            <div className="flex flex-wrap items-center gap-3">
              <Button variant="outline" size="sm" onClick={() => void loadOlder()}>
                {older && <Loader2Icon className="animate-spin" />} Load older
              </Button>
              <span className={CAPS}>{loadedWords(tail.rows.length, tail.head)}</span>
            </div>
          )}
        />
      </div>

      {record && (
        <RecordSheet
          row={record}
          open={!!record}
          onOpenChange={(o) => { if (!o) setRecord(null); }}
          onRevoke={(r) => { setRefusal(null); setAsk(r); }}
          revoked={revoked.has(record.session)}
          verified={!searching && canHash && !!tail.lastRead && tail.broken === 0}
        />
      )}

      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !busy) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke session?</AlertDialogTitle>
            <AlertDialogDescription>{ask ? revokeBody(ask.session, ask.detector) : ""}</AlertDialogDescription>
          </AlertDialogHeader>
          {refusal && <RefusedError subject="Revoke session" message={refusal} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-white hover:bg-danger/90"
              onClick={(e) => { e.preventDefault(); if (ask) void revoke(ask); }}
            >
              {busy && <Loader2Icon className="animate-spin" />} Revoke session
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
