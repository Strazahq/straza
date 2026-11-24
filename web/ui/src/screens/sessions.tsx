import * as React from "react";
import { ChevronRightIcon, Loader2Icon, PowerIcon, RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { DataTable, type SortState } from "@/components/data-table";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { KindTile } from "@/components/kind-icon";
import { PageHead } from "@/components/page-head";
import { KindPills, sessionColumns } from "@/components/session-list-parts";
import { Segmented } from "@/components/segmented";
import { type ApiError, type OverviewAnswer, type SessionRow, listSessions, overview, revokeSession, revokeSessions } from "@/lib/api";
import { STATUS_LABEL } from "@/lib/chip-words";
import { take } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { usePagedList } from "@/lib/paged";
import { RELOAD, RELOAD_LIST, RELOAD_NOW } from "@/lib/role-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { refused } from "@/lib/say";
import { snapshot } from "@/lib/session";
import {
  CLEAR_USER,
  EMPTY_ACTIVE,
  EMPTY_FILTERED,
  FILTER_ALL,
  FILTER_LABEL,
  GROUPS_OPEN_MAX,
  LIVE_HELP_NAME,
  LIVE_OFF,
  LIVE_ON,
  READING,
  SEARCH_LABEL,
  SEARCH_PLACEHOLDER,
  SESSION_LABELS,
  WAITING_COLUMN,
  WHERE_COLUMN,
  activeWords,
  countWords,
  cutID,
  groupLine,
  healthOf,
  hideGroup,
  liveHelp,
  revokeBody,
  revokeLabel,
  revokedWords,
  selectName,
  selectedWords,
  showGroup,
  sponsoredBy,
  userFilter,
  userKindOf,
} from "@/lib/session-words";
import { cn } from "@/lib/utils";

// The session list: one table the server sorts, filters and searches,
// grouped under one header per person while it is sorted by user, a bulk
// bar that replaces the filter row while rows are ticked, and the confirm
// dialog of the bulk Revoke. A row opens the session's page.

const route = routeByKey("sessions");
const DEBOUNCE_MS = 300;
const LABELS: Record<string, string> = { ...SESSION_LABELS, waiting: WAITING_COLUMN, where: WHERE_COLUMN };

// The status segment. "all" is the absence of a status filter, so it sends
// nothing.
const STATUSES = [
  { value: "active", label: STATUS_LABEL.active },
  { value: "revoked", label: STATUS_LABEL.revoked },
  { value: "closed", label: STATUS_LABEL.closed },
  { value: "all", label: FILTER_ALL },
];

// WIDTHS fixes every column but Harness, which takes the rest. The Session
// column holds the cut id with the this console chip and Status the revoked
// chip. Where cuts its two lines inside the cell, and a long Health word
// wraps onto a second line beside its icon. The default columns of the
// grouped list fit a table of 1010 px, a 1280 px window with the rail open.
// The flat list adds the User column, so there the table scrolls inside its
// box.
const WIDTHS: Record<string, string> = {
  select: "36px", user: "140px", session: "200px", where: "166px", waiting: "68px", health: "190px", last_seen: "104px", status: "106px",
  client: "112px", attestation: "116px", wiring: "140px", started: "208px",
};

// HARNESS_FLOOR is the room Harness keeps for "Claude Code 2.2.0".
const HARNESS_FLOOR = 140;

// minWidth is the table's floor for the columns that are shown. Below it the
// table scrolls inside its box, so no chip or word paints over the next cell.
const minWidth = (shown: string[]) => shown.reduce((sum, id) => sum + parseInt(WIDTHS[id] || "0", 10), shown.includes("harness") ? HARNESS_FLOOR : 0) + "px";

// groupKey is the person a session belongs to, the key of its group.
const groupKey = (r: SessionRow) => r.username || r.user_id;

type Picked = { id: string; username: string };

type Props = {
  user?: Picked;
};

// Sessions is the screen. user narrows the list to one person when another
// screen hands a person over, as a prop or through the hand-off box.
export function Sessions({ user }: Props) {
  // handed is the user the link "All sessions of" on a user's page puts in
  // the hand-off box. That link asks for all of them, so the list opens
  // under All for it, and under Active otherwise.
  const [handed] = React.useState(() => (user ? null : take<Picked>("sessions")));
  const [status, setStatus] = React.useState<string>(handed ? "all" : "active");
  const [picked, setPicked] = React.useState<Picked | null>(user || handed);
  const [typed, setTyped] = React.useState("");
  // searchBox takes the focus when the user filter is cleared, since its
  // chip leaves the page with the button that was pressed.
  const searchBox = React.useRef<HTMLInputElement>(null);
  const [q, setQ] = React.useState("");
  const [sort, setSort] = React.useState<SortState>({ id: "user", desc: false });
  // opened holds the groups a person folded or opened by hand, by key, and
  // keeps them across polls. Every other group is open while the whole list
  // is loaded and short, and folded otherwise.
  const [opened, setOpened] = React.useState<ReadonlyMap<string, boolean>>(new Map());
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [ask, setAsk] = React.useState<SessionRow[] | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [revoking, setRevoking] = React.useState(false);
  const [health, setHealth] = React.useState<OverviewAnswer | null>(null);

  // The search box asks the server, so it waits for the typing to settle.
  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  const params = { status: status === "all" ? undefined : status, user: picked ? picked.id : undefined, q };
  const { state, busy, reload, loadMore } = usePagedList<SessionRow>({
    read: listSessions,
    subject: "The session list",
    params,
    sorting: sort ? { key: sort.id, order: sort.desc ? "desc" : "asc" } : null,
    limit: 100,
    pollMs: 10000,
  });

  // The push counts ride the same read as the list, and a failed read
  // leaves the line off rather than guessing at the lane's state.
  const readHealth = React.useCallback(() => { void overview().then(setHealth, () => setHealth(null)); }, []);
  React.useEffect(() => { readHealth(); }, [readHealth]);

  const rows = state.kind === "ready" ? state.rows : [];
  const more = state.kind === "ready" && state.more;
  const here = snapshot();
  const ownID = here ? here.sessionID : null;

  const chosen = rows.filter((r) => selected.has(r.id));
  const names = new Set(chosen.map((r) => r.username || r.user_id));
  const oneName = names.size === 1 ? [...names][0] : "";

  // The groups follow the sort by user, the order the server hands the runs
  // in; another sort interleaves people and reads flat, with the user
  // column drawn instead. The rows shown keep the sort they were read in
  // until a new sort's page lands, so the groups follow that sort, and the
  // sort asked for only while no row is shown yet.
  const shownSort = state.kind === "ready" && rows.length ? state.sorting?.key : sort?.id;
  const grouped = shownSort === "user";
  const short = rows.length <= GROUPS_OPEN_MAX && !more;
  const collapsed = React.useMemo(
    () => new Set(rows.map(groupKey).filter((k) => !(opened.get(k) ?? short))),
    [rows, opened, short],
  );
  const toggle = (key: string) => setOpened((m) => new Map(m).set(key, !(m.get(key) ?? short)));
  // A group is one line across the table. Its door holds the chevron and
  // the name, so a click on either folds or opens the group.
  const groupHead = (key: string, members: SessionRow[]) => {
    const open = !collapsed.has(key);
    const name = members[0].username || cutID(members[0].user_id);
    const active = members.filter((m) => m.status === "active").length;
    const kind = userKindOf(members[0].user_kind);
    const sponsor = members[0].sponsor;
    return (
      <span className="flex min-w-0 items-center gap-2">
        <button
          type="button"
          aria-expanded={open}
          aria-label={open ? hideGroup(name) : showGroup(name)}
          onClick={(e) => { e.stopPropagation(); toggle(key); }}
          className="flex min-w-0 items-center gap-2 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <ChevronRightIcon className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} aria-hidden="true" />
          {kind && <KindTile kind={kind} size="sm" />}
          <span className="truncate font-mono font-semibold text-foreground" title={name}>{name}</span>
        </button>
        <span className="min-w-0 truncate text-muted-foreground">
          {[sponsor ? sponsoredBy(sponsor) : "", groupLine(members.length, active, status === "active")].filter(Boolean).join(" · ")}
        </span>
      </span>
    );
  };

  const columns = sessionColumns(grouped, ownID);

  const filters = (
    <>
      <Segmented label={FILTER_LABEL} value={status} onChange={setStatus} options={STATUSES} />
      <div className="relative w-full max-w-xs">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
        <Input ref={searchBox} value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={SEARCH_PLACEHOLDER} aria-label={SEARCH_LABEL} className="h-9 pl-8" />
      </div>
      {picked && (
        <span className="inline-flex h-9 items-center gap-1.5 rounded-md border border-border bg-background pl-2.5 pr-1 text-dense" data-user-chip={picked.id}>
          {userFilter(picked.username)}
          <Button variant="ghost" size="icon-xs" aria-label={CLEAR_USER} onClick={() => { setPicked(null); searchBox.current?.focus(); }}><XIcon /></Button>
        </span>
      )}
    </>
  );

  // The count leads selectedWords, so the words file keeps the sentence and
  // the bar only sets the count in bold.
  const lead = String(selected.size);
  const bulk = (
    <div className="flex flex-1 flex-wrap items-center gap-2" data-bulk-bar>
      <input type="checkbox" checked readOnly aria-hidden="true" tabIndex={-1} className="size-4 accent-primary" />
      <b className="font-semibold text-foreground">{lead}</b>
      <span className="text-text-2">{selectedWords(selected.size, rows.length, oneName).slice(lead.length)}</span>
      <span className="ml-auto flex items-center gap-2">
        <Button variant="outline" size="sm" className="h-9" onClick={() => setSelected(new Set())}>Clear</Button>
        <Button variant="outline" size="sm" className="h-9 border-danger/40 text-danger hover:bg-danger-bg hover:text-danger" onClick={() => { setProblem(null); setAsk(chosen); }}>
          <PowerIcon /> {revokeLabel(selected.size)}
        </Button>
      </span>
    </div>
  );

  // The headline is the server's own count of active sessions, whatever
  // page and filter the list holds.
  const active = health && health.sessions ? health.sessions.active || 0 : null;
  const push = health ? health.push : undefined;
  // The kind pills count loaded rows, so they show only while those are
  // every active session.
  const pills = state.kind === "ready" && status === "active" && !more && !q && !picked;

  const asked = ask || [];
  const askNames = new Set(asked.map((r) => r.username || r.user_id));
  const askName = askNames.size === 1 ? [...askNames][0] : "";

  const revoke = async () => {
    if (!ask || revoking) return;
    const ids = ask.map((r) => r.id);
    setRevoking(true);
    setProblem(null);
    try {
      if (ids.length === 1) await revokeSession(ids[0]); else await revokeSessions(ids);
      setAsk(null);
      setSelected(new Set());
      notify.ok(revokedWords(ids.length));
      void reload();
    } catch (e) {
      setProblem(refused(e as ApiError));
    } finally {
      setRevoking(false);
    }
  };

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <Button variant="outline" size="sm" aria-label={RELOAD_LIST} onClick={() => { void reload(); readHealth(); }}>
            <RefreshCwIcon /> {RELOAD}
          </Button>
        }
      />
      <div className="flex flex-col gap-3 px-6 py-5">
        {(active !== null || push) && (
          <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
            {active !== null && (
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <p className="flex items-baseline gap-2.5" data-active-count>
                  <b className="text-stat leading-none font-semibold text-foreground tabular-nums">{active}</b>{" "}
                  <span className="text-section leading-none font-semibold text-text-2">{activeWords(active)}</span>
                </p>
                {pills && <KindPills rows={rows} />}
              </div>
            )}
            {push && (
              <p className="flex items-center gap-2 text-dense text-text-2" data-live={push.lane_up ? "on" : "off"}>
                <span className={cn("size-2 rounded-full", push.lane_up ? "bg-ok" : "bg-warn")} aria-hidden="true" />
                {push.lane_up ? LIVE_ON : LIVE_OFF}
                <HelpTip label={LIVE_HELP_NAME} text={liveHelp(push, active || 0)} />
              </p>
            )}
          </div>
        )}

        {state.kind === "loading" && <p className="text-dense text-muted-foreground">{READING}</p>}
        {state.kind === "error" && (
          <div role="alert" className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-dense text-foreground">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => { void reload(); readHealth(); }}>{RELOAD_NOW}</Button>
          </div>
        )}
        {state.kind === "ready" && state.problem && <FetchError subject={route.label} detail={state.problem} lastRead={state.lastRead} />}

        {state.kind === "ready" && (
          <DataTable<SessionRow>
            rows={rows}
            columns={columns}
            labels={LABELS}
            rowKey={(r) => r.id}
            rowName={(r) => r.id}
            dataAttr="data-session"
            onOpen={(r) => navigate("sessions", [r.id])}
            // Only an active session can be revoked, and the console's own
            // session is revoked from its page, so only another active row
            // can be ticked.
            selection={{ selected, onChange: setSelected, label: (r) => selectName(cutID(r.id)), selectable: (r) => r.status === "active" && r.id !== ownID }}
            groups={grouped ? { key: groupKey, header: groupHead, collapsed } : undefined}
            // Fixed widths keep the header still while groups fold and open.
            widths={WIDTHS}
            minWidth={minWidth}
            filterBar={selected.size ? bulk : filters}
            count={(shown) => countWords(shown, more)}
            emptyText={status === "active" && !picked && !q ? EMPTY_ACTIVE : EMPTY_FILTERED}
            serverSort={{ state: sort, onChange: setSort }}
            hiddenByDefault={["client", "attestation", "wiring", "started"]}
            rowClassName={(r) => (healthOf(r).level === "danger" ? "shadow-[inset_3px_0_0_var(--danger)]" : "")}
            foot={
              <>
                {more && (
                  <div className="flex justify-center">
                    <Button variant="outline" size="sm" onClick={() => void loadMore()} disabled={busy}>
                      {busy && <Loader2Icon className="animate-spin" />} Load more
                    </Button>
                  </div>
                )}
              </>
            }
          />
        )}
      </div>

      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !revoking) { setAsk(null); setProblem(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{revokeLabel(asked.length) + "?"}</AlertDialogTitle>
            <AlertDialogDescription>{revokeBody(asked.length, askName)}</AlertDialogDescription>
          </AlertDialogHeader>
          {problem && <RefusedError subject={revokeLabel(asked.length)} message={problem} />}
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <Button variant="destructive" onClick={() => void revoke()} disabled={revoking}>
              {revoking && <Loader2Icon className="animate-spin" />} {revokeLabel(asked.length)}
            </Button>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
