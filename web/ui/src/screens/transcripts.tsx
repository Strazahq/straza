import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ArrowDownIcon, RefreshCwIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ConversationSheet, ToneBadge } from "@/components/conversation-sheet";
import { DataTable, HEAD, plain } from "@/components/data-table";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { type PickedUser, UserPicker } from "@/components/user-picker";
import { type ApiError, type CaptureConfig, type ConversationRow, type TurnRow, captureConfig, listConversations, query, searchTurns } from "@/lib/api";
import { canHash, sha256Hex } from "@/lib/chain";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { shortID } from "@/lib/session-words";
import {
  BODY_MISSING,
  CONTAINS_HINT,
  EMPTY_HITS,
  EMPTY_INBOX,
  EXACT_HINT,
  EXACT_NEEDS_SECURE,
  GOTO_LABEL,
  INBOX_LINE,
  MATCH_CONTAINS,
  MATCH_EXACT,
  READING_LIST,
  SEARCHING,
  TYPE_WHAT,
  captureLine,
  hitsWords,
  kindWord,
  loadedWords,
} from "@/lib/transcript-words";
import { cn } from "@/lib/utils";
import { absTime, relTimeText } from "@/lib/words";

const route = routeByKey("transcripts");

// The inbox is one page of the newest conversations and a search answers at
// most this many turns; both lists are read whole, so the table holds what
// the server sent.
const LIMIT = 200;

// PREVIEW is how much of a matching turn a hit row shows before the sheet.
const PREVIEW = 160;

type List =
  | { kind: "loading" }
  | { kind: "ready"; rows: ConversationRow[]; lastRead: Date; problem: string | null }
  | { kind: "error"; message: string };

type Hits =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; rows: TurnRow[] }
  | { kind: "error"; message: string };

type Capture = { config: CaptureConfig | null; failed: boolean };

// sortedHead marks the column the server already ordered by. It is not a
// control: this list has one order, newest activity first.
function sortedHead<T>(label: string): ColumnDef<T>["header"] {
  const render: ColumnDef<T>["header"] = () => (
    <span className={HEAD} data-sorted="desc">{label}<ArrowDownIcon className="ml-1 inline size-3.5 text-foreground" /></span>
  );
  return render;
}

// TimeCell reads a stamp the operator way, with the full stamp on the
// tooltip. A row older than a day reads as the whole stamp, which wraps
// inside its column.
function TimeCell({ iso, text }: { iso: string; text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default whitespace-normal text-text-2">{text}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

// UserCell names who spoke: the username the server resolved, or the id
// prefix when the row carries no name.
function UserCell({ username, id }: { username?: string; id?: string }) {
  if (username) return <span className="block truncate font-mono text-foreground" title={username}>{username}</span>;
  return <span className="font-mono text-muted-foreground">{id ? shortID(id) : "not set"}</span>;
}

const INBOX_LABELS: Record<string, string> = {
  last_at: "Last activity",
  username: "User",
  turns: "Turns",
  preview: "Latest turn",
  session_id: "Session",
};

// The widths leave the Latest turn column the rest of the page, so the
// preview is cut at the column's edge and the table never grows past it.
const INBOX_WIDTHS: Record<string, string> = { last_at: "120px", username: "256px", turns: "56px", session_id: "136px" };

const INBOX_COLUMNS: ColumnDef<ConversationRow>[] = [
  {
    accessorKey: "last_at",
    header: sortedHead("Last activity"),
    cell: ({ row }) => <TimeCell iso={row.original.last_at} text={relTimeText(row.original.last_at)} />,
    enableHiding: false,
  },
  {
    accessorKey: "username",
    header: plain("User"),
    cell: ({ row }) => <UserCell username={row.original.username} id={row.original.user_id} />,
  },
  {
    accessorKey: "turns",
    header: () => <span className={cn(HEAD, "block text-right")}>Turns</span>,
    cell: ({ row }) => <span className="block text-right tabular-nums">{row.original.turns}</span>,
  },
  {
    accessorKey: "preview",
    header: plain("Latest turn"),
    // One line that runs to the column's edge and is cut there with an
    // ellipsis. The whole preview sits on hover, and the conversation is
    // one click away.
    cell: ({ row }) => <span className="block truncate text-foreground" title={row.original.preview}>{row.original.preview}</span>,
  },
  {
    accessorKey: "session_id",
    header: plain("Session"),
    cell: ({ row }) => <span className="font-mono text-muted-foreground">{shortID(row.original.session_id)}</span>,
  },
];

const HIT_LABELS: Record<string, string> = {
  at: "Time",
  username: "User",
  kind: "Kind",
  content: "Content",
  session_id: "Session",
};

const HIT_COLUMNS: ColumnDef<TurnRow>[] = [
  {
    accessorKey: "at",
    header: plain("Time"),
    // Hits span sessions and days, so a hit carries its whole stamp.
    cell: ({ row }) => <span className="font-mono whitespace-nowrap text-text-2">{absTime(row.original.at)}</span>,
    enableHiding: false,
  },
  {
    accessorKey: "username",
    header: plain("User"),
    cell: ({ row }) => <UserCell username={row.original.username} id={row.original.user_id} />,
  },
  {
    accessorKey: "kind",
    header: plain("Kind"),
    cell: ({ row }) => <ToneBadge tone={row.original.kind === "prompt" ? "accent" : "ok"}>{kindWord(row.original)}</ToneBadge>,
  },
  {
    accessorKey: "content",
    header: plain("Content"),
    cell: ({ row }) => (row.original.body_missing
      ? <ToneBadge tone="danger" title={BODY_MISSING}>body missing</ToneBadge>
      : <span className="block max-w-[56ch] truncate text-muted-foreground" title={row.original.content.slice(0, PREVIEW)}>{row.original.content.slice(0, PREVIEW) + (row.original.content.length > PREVIEW ? "…" : "")}</span>),
  },
  {
    accessorKey: "session_id",
    header: plain("Session"),
    cell: ({ row }) => <span className="font-mono text-muted-foreground">{shortID(row.original.session_id || "")}</span>,
  },
];

// Transcripts is the captured conversations screen: the inbox of sessions,
// the hunt for a value across every captured turn, and the conversation
// itself in a sheet. session opens one conversation at once, for a link
// from a session or an audit record.
export function Transcripts({ session }: { session?: string }) {
  const [list, setList] = React.useState<List>({ kind: "loading" });
  const [capture, setCapture] = React.useState<Capture>({ config: null, failed: false });
  const [hits, setHits] = React.useState<Hits>({ kind: "idle" });
  const [text, setText] = React.useState("");
  const [match, setMatch] = React.useState(MATCH_CONTAINS);
  const [user, setUser] = React.useState<PickedUser | null>(null);
  const [miss, setMiss] = React.useState(false);
  const [goto, setGoto] = React.useState("");
  const [openID, setOpenID] = React.useState<string | null>(session || null);
  const box = React.useRef<HTMLInputElement>(null);
  const [, tick] = React.useState(0);

  const load = React.useCallback(async () => {
    setList((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    captureConfig().then(
      (c) => setCapture({ config: c?.capture || {}, failed: false }),
      () => setCapture({ config: null, failed: true }),
    );
    try {
      const rows = await listConversations(LIMIT);
      setList({ kind: "ready", rows, lastRead: new Date(), problem: null });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const message = readFailed("The recorded conversations", err);
      setList((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
    }
  }, []);

  React.useEffect(() => { void load(); }, [load]);
  React.useEffect(() => { if (session) setOpenID(session); }, [session]);
  // Relative times move on their own; a re-render every 30 s keeps "6 m
  // ago" honest.
  React.useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30000);
    return () => clearInterval(t);
  }, []);

  // An exact search hashes the hunted text here and sends the hash alone,
  // so the value itself never leaves this browser.
  const search = async () => {
    const needle = text.trim();
    if (!needle) {
      setMiss(true);
      box.current?.focus();
      return;
    }
    setMiss(false);
    setHits({ kind: "loading" });
    try {
      const params: Record<string, string | number | undefined> = match === MATCH_EXACT
        ? { hash: "sha256:" + (await sha256Hex(needle)) }
        : { q: needle };
      params.user = user?.username;
      params.limit = LIMIT;
      const rows = await searchTurns(query(params));
      setHits({ kind: "ready", rows });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setHits({ kind: "error", message: readFailed("The matching turns", err) });
    }
  };

  const back = () => {
    setHits({ kind: "idle" });
    setText("");
    setMiss(false);
  };

  const searching = hits.kind !== "idle";
  const posture = captureLine(capture.config, capture.failed);
  // The match count sits in the filter row, so the hint says only how the
  // search reads.
  const hint = searching ? (match === MATCH_EXACT ? EXACT_HINT : CONTAINS_HINT) : INBOX_LINE;

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <Button variant="ghost" size="sm" onClick={() => void load()} aria-label="Reload the list">
            <RefreshCwIcon /> Reload
          </Button>
        }
      />
      <div className="flex flex-col gap-3 px-6 py-5">
        <div className="flex flex-wrap items-center gap-2" data-capture-line>
          <span className="text-[13px] text-muted-foreground">Recording policy</span>
          <ToneBadge tone={posture.tone}>{posture.word}</ToneBadge>
          <span className="max-w-[90ch] text-sm text-text-2">{posture.text}</span>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Input
            ref={box}
            value={text}
            onChange={(e) => { setText(e.target.value); setMiss(false); }}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); void search(); } }}
            placeholder="Search recorded prompts and replies"
            aria-label="Search transcripts"
            aria-invalid={miss || undefined}
            aria-describedby={miss ? "search-miss" : undefined}
            className="h-9 min-w-[320px] flex-1 sm:max-w-sm"
          />
          <Label htmlFor="match" className="text-[13px] font-normal text-muted-foreground">Match</Label>
          <Select value={match} onValueChange={setMatch}>
            <SelectTrigger id="match" aria-label="Match" className="w-[150px]"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={MATCH_CONTAINS} title={CONTAINS_HINT}>{MATCH_CONTAINS}</SelectItem>
              <SelectItem value={MATCH_EXACT} disabled={!canHash} title={canHash ? EXACT_HINT : EXACT_NEEDS_SECURE}>{MATCH_EXACT}</SelectItem>
            </SelectContent>
          </Select>
          <UserPicker value={user} onChange={setUser} label="User" className="w-56" />
          <Button size="sm" className="h-9" onClick={() => void search()}>Search</Button>
          {searching && <Button variant="outline" size="sm" className="h-9" onClick={back}>Back to conversations</Button>}
          <span className="ml-auto flex items-center gap-2 border-l border-border pl-3">
            <Label htmlFor="goto" className="text-[13px] font-normal text-muted-foreground">{GOTO_LABEL}</Label>
            <Input
              id="goto"
              value={goto}
              onChange={(e) => setGoto(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && goto.trim()) { e.preventDefault(); setOpenID(goto.trim()); } }}
              placeholder="session id"
              aria-label="Session id"
              className="h-9 w-[180px] font-mono"
            />
          </span>
        </div>

        {miss && <p id="search-miss" className="text-[13px] text-danger">{TYPE_WHAT}</p>}
        <p className="max-w-[90ch] text-[13px] text-muted-foreground" data-hint>{hint}</p>

        {list.kind === "loading" && !searching && <p className="text-sm text-muted-foreground">{READING_LIST}</p>}
        {list.kind === "error" && !searching && (
          <div className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-sm text-foreground" role="alert">
            {list.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => void load()}>Reload now</Button>
          </div>
        )}
        {list.kind === "ready" && list.problem && !searching && (
          <FetchError subject={route.label} detail={list.problem} lastRead={list.lastRead} />
        )}
        {list.kind === "ready" && !searching && (
          <DataTable
            rows={list.rows}
            columns={INBOX_COLUMNS}
            labels={INBOX_LABELS}
            rowKey={(r) => r.session_id}
            rowName={(r) => r.session_id}
            dataAttr="data-session"
            onOpen={(r) => setOpenID(r.session_id)}
            openKey={openID}
            serverSort={{ state: { id: "last_at", desc: true }, onChange: () => {} }}
            widths={INBOX_WIDTHS}
            count={(shown) => loadedWords(shown)}
            emptyText={EMPTY_INBOX}
          />
        )}

        {hits.kind === "loading" && <p className="text-sm text-muted-foreground">{SEARCHING}</p>}
        {hits.kind === "error" && <FetchError subject={route.label} detail={hits.message} />}
        {hits.kind === "ready" && (
          <DataTable
            rows={hits.rows}
            columns={HIT_COLUMNS}
            labels={HIT_LABELS}
            rowKey={(t) => (t.session_id || "") + "|" + t.at + "|" + t.content_hash}
            rowName={(t) => t.session_id || ""}
            dataAttr="data-session"
            onOpen={(t) => setOpenID(t.session_id || null)}
            rowClassName={(t) => (t.session_id && t.session_id === openID ? "bg-accent-bg" : "")}
            count={(shown) => hitsWords(shown)}
            emptyText={EMPTY_HITS}
          />
        )}
      </div>

      <ConversationSheet
        sessionID={openID || ""}
        open={openID !== null}
        onOpenChange={(next) => { if (!next) setOpenID(null); }}
        onOpenSession={(id) => navigate("sessions", [id])}
        onOpenAudit={(id) => navigate("audit", [id])}
      />
    </>
  );
}
