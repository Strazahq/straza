import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ChevronRightIcon, Loader2Icon, MonitorIcon, PowerIcon, RefreshCwIcon } from "lucide-react";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { DataTable, type SortState, plain, sortable } from "@/components/data-table";
import { FetchError, RefusedError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { SessionBadge, SessionSheet } from "@/components/session-sheet";
import { UserPicker, type PickedUser } from "@/components/user-picker";
import { type ApiError, type OverviewAnswer, type SessionRow, listSessions, overview, revokeSession, revokeSessions } from "@/lib/api";
import { take } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { usePagedList } from "@/lib/paged";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { refused } from "@/lib/say";
import { snapshot } from "@/lib/session";
import {
  ATT_LEGEND,
  ATT_TITLE,
  EMPTY_ACTIVE,
  EMPTY_FILTERED,
  OWN_SESSION_WARNING,
  READING,
  WIRING_TITLE,
  activeWords,
  attTone,
  countWords,
  groupActive,
  groupWords,
  hideGroup,
  pushLine,
  revokeBody,
  revokeLabel,
  revokedWords,
  selectedWords,
  shortHash,
  shortID,
  showGroup,
  statusTone,
  wiringTone,
  worstAttestation,
} from "@/lib/session-words";
import { NONE, absTime, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// The session list: one table the server sorts and filters, grouped under
// one header per person while it is sorted by user, a bulk bar that
// replaces the filter row while rows are ticked, and one confirm dialog
// shared by the row's Revoke and the bulk Revoke.

const route = routeByKey("sessions");

// The status segment. "all" is the absence of a status filter, so it sends
// nothing.
const STATUSES = ["active", "revoked", "closed", "all"] as const;

const LABELS: Record<string, string> = {
  session: "Sessions",
  user: "User",
  harness: "Harness",
  client: "Client",
  attestation: "Attestation",
  wiring: "Hook configuration",
  status: "Status",
  started: "Started",
  last_seen: "Last seen",
};

// Stamp is a relative time with the absolute stamp on hover, the way every
// table in the console reads a time.
function Stamp({ iso }: { iso: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default text-text-2">{relTimeText(iso)}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

type Props = {
  user?: { id: string; username: string };
  // openID is a session named in the address (sessions/<id>), from a
  // transcript or an audit record: the list opens on every status and the
  // row's sheet opens once the page holds it.
  openID?: string;
};

// Sessions is the screen. user presets the User filter when another screen
// hands a person over, as a prop or through the hand-off box.
export function Sessions({ user, openID }: Props) {
  const [status, setStatus] = React.useState<string>(openID ? "all" : "active");
  const [picked, setPicked] = React.useState<PickedUser | null>(() => user || take<PickedUser>("sessions"));
  const [sort, setSort] = React.useState<SortState>({ id: "user", desc: false });
  // closed is the set of folded groups once the person has touched one;
  // before that, every group is folded when more than one person is
  // listed, so a fleet opens as a list of people.
  const [closed, setClosed] = React.useState<Set<string> | null>(null);
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [openRow, setOpenRow] = React.useState<SessionRow | null>(null);
  const [ask, setAsk] = React.useState<SessionRow[] | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [revoking, setRevoking] = React.useState(false);
  const [health, setHealth] = React.useState<OverviewAnswer | null>(null);

  const params = { status: status === "all" ? undefined : status, user: picked ? picked.id : undefined };
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
  const wanted = React.useRef(openID || "");
  React.useEffect(() => {
    if (!wanted.current) return;
    const hit = rows.find((r) => r.id === wanted.current);
    if (hit) { wanted.current = ""; setOpenRow(hit); }
  }, [rows]);
  const here = snapshot();
  const ownID = here ? here.sessionID : null;

  const chosen = rows.filter((r) => selected.has(r.id));
  const names = new Set(chosen.map((r) => r.username || r.user_id));
  const oneName = names.size === 1 ? [...names][0] : "";

  const groupKey = (r: SessionRow) => r.username || r.user_id;
  const people = [...new Set(rows.map(groupKey))];
  const collapsed = closed ?? new Set(people.length > 1 ? people : []);
  const toggle = (key: string) => setClosed(() => {
    const next = new Set(collapsed);
    if (next.has(key)) next.delete(key); else next.add(key);
    return next;
  });
  // A group lies on the table's columns. Its door is the chevron in the
  // checkbox column, and the name is the door's label, so a click on either
  // folds or opens the group. Every other column answers for the person's
  // sessions together.
  const door = React.useId();
  const groupCell = (id: string, key: string, members: SessionRow[]) => {
    const doorID = door + people.indexOf(key);
    const open = !collapsed.has(key);
    const said = (values: (string | undefined)[]) => [...new Set(values.filter((v): v is string => !!v))];
    switch (id) {
      case "select":
        return (
          <button
            type="button"
            id={doorID}
            aria-expanded={open}
            aria-label={open ? hideGroup(key) : showGroup(key)}
            onClick={(e) => { e.stopPropagation(); toggle(key); }}
            className="-m-2 flex p-2"
          >
            <ChevronRightIcon className={cn("size-4 text-muted-foreground transition-transform", open && "rotate-90")} aria-hidden="true" />
          </button>
        );
      case "user":
        return <label htmlFor={doorID} className="block cursor-pointer truncate font-mono font-semibold text-foreground" title={key}>{key}</label>;
      case "session":
        return <span className="text-text-2 tabular-nums">{groupWords(members.length)}</span>;
      case "harness":
      case "client": {
        const words = said(members.map((m) => (id === "harness" ? m.harness : m.client_version))).join(", ");
        return words
          ? <span className="block truncate font-mono text-muted-foreground" title={words}>{words}</span>
          : <span className="text-muted-foreground">{NONE}</span>;
      }
      case "attestation": {
        const worst = worstAttestation(members.map((m) => m.attestation));
        return <SessionBadge tone={attTone(worst)} word={worst} title={ATT_TITLE[worst]} />;
      }
      case "wiring": {
        const kinds = said(members.map((m) => m.wiring_status));
        return kinds.length
          ? <span className="flex flex-wrap gap-1">{kinds.map((w) => <SessionBadge key={w} tone={wiringTone(w)} word={w} title={WIRING_TITLE[w]} />)}</span>
          : <span className="text-muted-foreground">{NONE}</span>;
      }
      case "status": {
        const active = members.filter((m) => m.status === "active").length;
        return active
          ? <SessionBadge tone="ok" word={groupActive(active)} />
          : <span className="text-muted-foreground">{groupActive(active)}</span>;
      }
      case "started":
        return <Stamp iso={members.reduce((t, m) => (m.started_at < t ? m.started_at : t), members[0].started_at)} />;
      case "last_seen":
        return <Stamp iso={members.reduce((t, m) => (m.last_seen > t ? m.last_seen : t), "")} />;
    }
    return null;
  };

  const columns: ColumnDef<SessionRow>[] = [
    {
      id: "user",
      header: sortable<SessionRow>("User"),
      cell: ({ row }) => (row.original.username
        ? <span className="block truncate font-mono" title={row.original.username}>{row.original.username}</span>
        : <span className="block truncate font-mono text-muted-foreground">{shortID(row.original.user_id)}</span>),
    },
    {
      id: "session",
      header: plain<SessionRow>("Sessions"),
      // The console's own session wears a marked icon, not the badge the
      // sheet carries, so the row stays one line high in the fixed column.
      cell: ({ row }) => (
        <span className="flex items-center gap-1.5 whitespace-nowrap">
          <span className="font-mono">{shortID(row.original.id)}</span>
          {row.original.id === ownID && (
            <Tooltip>
              <TooltipTrigger asChild>
                <MonitorIcon role="img" aria-label="this console" className="size-4 shrink-0 text-link" data-this-console />
              </TooltipTrigger>
              <TooltipContent>The session this console is signed in with.</TooltipContent>
            </Tooltip>
          )}
        </span>
      ),
    },
    { id: "harness", header: plain<SessionRow>("Harness"), cell: ({ row }) => <span className="block truncate font-mono text-muted-foreground" title={row.original.harness}>{row.original.harness}</span> },
    {
      id: "client",
      header: plain<SessionRow>("Client"),
      cell: ({ row }) => (row.original.client_version
        ? <span className="font-mono">{row.original.client_version}</span>
        : <span className="text-muted-foreground">{NONE}</span>),
    },
    {
      id: "attestation",
      header: sortable<SessionRow>("Attestation"),
      cell: ({ row }) => <SessionBadge tone={attTone(row.original.attestation)} word={row.original.attestation} title={ATT_TITLE[row.original.attestation]} />,
    },
    {
      id: "wiring",
      header: plain<SessionRow>("Wiring"),
      cell: ({ row }) => (row.original.wiring_status
        ? <SessionBadge tone={wiringTone(row.original.wiring_status)} word={row.original.wiring_status} title={WIRING_TITLE[row.original.wiring_status] + (row.original.wiring_hash ? " " + shortHash(row.original.wiring_hash) : "")} />
        : <span className="text-muted-foreground">{NONE}</span>),
    },
    { id: "status", header: sortable<SessionRow>("Status"), cell: ({ row }) => <SessionBadge tone={statusTone(row.original.status)} word={row.original.status} /> },
    { id: "started", header: sortable<SessionRow>("Started"), cell: ({ row }) => <Stamp iso={row.original.started_at} /> },
    { id: "last_seen", header: sortable<SessionRow>("Last seen"), cell: ({ row }) => <Stamp iso={row.original.last_seen} /> },
  ];

  const filters = (
    <>
      <div role="group" aria-label="Status" className="flex items-center">
        {STATUSES.map((s) => (
          <Button
            key={s}
            variant="outline"
            size="sm"
            aria-pressed={status === s}
            onClick={() => setStatus(s)}
            className={cn("-ml-px h-9 rounded-none first:ml-0 first:rounded-l-md last:rounded-r-md", status === s && "bg-accent-bg text-foreground")}
          >
            {s}
          </Button>
        ))}
      </div>
      <UserPicker value={picked} onChange={setPicked} label="User" className="w-64" />
    </>
  );

  // The count leads selectedWords, so the words file keeps the sentence and
  // the bar only sets its first clause in bold.
  const lead = selected.size + " selected";
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
  const line = pushLine(health ? health.push : undefined, active || 0);
  const laneUp = !!(health && health.push && health.push.lane_up);

  const asked = ask || [];
  const askNames = new Set(asked.map((r) => r.username || r.user_id));
  const askName = askNames.size === 1 ? [...askNames][0] : "";
  const askOwn = asked.some((r) => r.id === ownID);

  const revoke = async () => {
    if (!ask || revoking) return;
    const ids = ask.map((r) => r.id);
    setRevoking(true);
    setProblem(null);
    try {
      if (ids.length === 1) await revokeSession(ids[0]); else await revokeSessions(ids);
      setAsk(null);
      setSelected(new Set());
      setOpenRow(null);
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
          <Button variant="ghost" size="sm" aria-label="Reload the list" onClick={() => { void reload(); readHealth(); }}>
            <RefreshCwIcon /> Reload
          </Button>
        }
      />
      <div className="flex flex-col gap-3 px-6 py-5">
        {(active !== null || line) && (
          <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-2">
            {active !== null && (
              <p className="flex items-baseline gap-2.5" data-active-count>
                <b className="text-[24px] leading-none font-semibold text-foreground tabular-nums">{active}</b>{" "}
                <span className="text-lg leading-none font-semibold text-text-2">{activeWords(active)}</span>
              </p>
            )}
            {line && (
              <p className="flex items-center gap-2 text-[14px] text-text-2" data-push-line>
                <span className={cn("size-2 rounded-full", laneUp ? "bg-ok" : "bg-warn")} aria-hidden="true" />
                {line}
              </p>
            )}
          </div>
        )}

        {state.kind === "loading" && <p className="text-sm text-muted-foreground">{READING}</p>}
        {state.kind === "error" && (
          <div role="alert" className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-sm text-foreground">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => { void reload(); readHealth(); }}>Reload now</Button>
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
            onOpen={setOpenRow}
            openKey={openRow ? openRow.id : null}
            // Only an active session can be revoked, so only an active row
            // can be ticked.
            selection={{ selected, onChange: setSelected, label: (r) => "Select session " + shortID(r.id), selectable: (r) => r.status === "active" }}
            // The groups follow the sort by user, the order the server hands
            // the runs in; another sort interleaves people and reads flat.
            groups={sort && sort.id === "user" ? { key: groupKey, cell: groupCell, collapsed } : undefined}
            // Fixed widths keep the header still while groups fold and open.
            widths={{ select: "36px", user: "25%", session: "14%", harness: "16%", client: "12%", attestation: "11%", wiring: "10.5%", status: "9.5%", started: "12%", last_seen: "10%" }}
            filterBar={selected.size ? bulk : filters}
            count={(shown) => countWords(shown, more)}
            emptyText={status === "active" && !picked ? EMPTY_ACTIVE : EMPTY_FILTERED}
            serverSort={{ state: sort, onChange: setSort }}
            hiddenByDefault={["client", "started"]}
            rowClassName={(r) => (r.attestation === "none" ? "shadow-[inset_3px_0_0_var(--danger)]" : "")}
            foot={
              <>
                <p className="text-[13px] text-muted-foreground" data-att-legend>{ATT_LEGEND}</p>
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

      {openRow && (
        <SessionSheet
          session={openRow}
          open
          onOpenChange={(o) => { if (!o) setOpenRow(null); }}
          onRevoke={(s) => { setProblem(null); setAsk([s]); }}
          onOpenUser={(id) => navigate("users", [id])}
          onOpenTranscript={(id) => navigate("transcripts", [id])}
          onOpenAudit={(id) => navigate("audit", [id])}
        />
      )}

      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !revoking) { setAsk(null); setProblem(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{revokeLabel(asked.length) + "?"}</AlertDialogTitle>
            <AlertDialogDescription>{revokeBody(asked.length, askName)}</AlertDialogDescription>
          </AlertDialogHeader>
          {askOwn && <p className="text-sm font-semibold text-foreground">{OWN_SESSION_WARNING}</p>}
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
