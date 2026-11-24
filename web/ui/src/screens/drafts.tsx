import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { FilePenLineIcon, Loader2Icon } from "lucide-react";
import { DataTable, plain } from "@/components/data-table";
import { Chip } from "@/components/chip";
import { EmptyState } from "@/components/empty-state";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { Segmented } from "@/components/segmented";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { DraftSummary } from "@/lib/api";
import { listDrafts, waitingLabel } from "@/lib/drafts-api";
import {
  CHECKS_HELP,
  COLUMN,
  EMPTY,
  EMPTY_MINE,
  EMPTY_SEARCH,
  FILTER_LABEL,
  FILTER_PLACEHOLDER,
  LOAD_MORE,
  NOTHING_TO_ACK,
  NOT_CHECKED_HELP,
  NOT_CHECKED_YET,
  READING_LIST,
  SUBJECT_LIST,
  TABS,
  type Tab,
  WHOSE,
  WHOSE_LABEL,
  decidedWords,
  doorWords,
  fileOf,
  loadedLine,
  proposerWords,
  refusedCount,
  shownCount,
  uncheckedCount,
  warningCount,
  widenCount,
} from "@/lib/drafts-words";
import { usePagedList } from "@/lib/paged";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { snapshot } from "@/lib/session";
import { absTime, agoWord } from "@/lib/words";

// The Drafts queue, built like the Approvals
// queue: the tab in the address, a Mine and All toggle, a filter over the
// loaded rows, and one row per draft with what changes, who drafted it,
// how it came in, the counts of its last check and how long it waits. The
// Published tab is the change record. The list reads no verdict, so a row
// shows the counts the server stored with its last check.

const route = routeByKey("drafts");
const POLL_MS = 15000;
const WIDTHS: Record<string, string> = { id: "72px", who: "230px", door: "160px", checks: "230px", waiting: "104px", decided: "210px" };

// Stamp is a time the operator way: relative, with the stamp on hover.
function Stamp({ iso }: { iso: string | undefined }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild><span className="cursor-default tabular-nums text-text-2">{agoWord(iso)}</span></TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

// ChecksCell reads the counts of the server's last check, or says that the
// revision on the row was not checked yet.
function ChecksCell({ r }: { r: DraftSummary }) {
  const c = r.checks;
  if (!c || c.revision !== r.revision) return <Chip tone="unknown" title={NOT_CHECKED_HELP}>{NOT_CHECKED_YET}</Chip>;
  return (
    <span className="flex flex-wrap gap-1">
      {c.refused > 0 && <Chip tone="danger">{refusedCount(c.refused)}</Chip>}
      {c.risks > 0 && <Chip tone="warn">{widenCount(c.risks)}</Chip>}
      {!c.refused && !c.risks && <Chip tone="plain">{NOTHING_TO_ACK}</Chip>}
      {c.warnings > 0 && <Chip tone="plain">{warningCount(c.warnings)}</Chip>}
      {c.unchecked > 0 && <Chip tone="unknown">{uncheckedCount(c.unchecked)}</Chip>}
    </span>
  );
}

export function Drafts({ tab }: { tab?: string }) {
  const current: Tab = TABS.some((t) => t.key === tab) ? (tab as Tab) : "waiting";
  const state = (TABS.find((t) => t.key === current) || TABS[0]).state;
  const [whose, setWhose] = React.useState<"all" | "mine">("all");
  const [search, setSearch] = React.useState("");
  const [waiting, setWaiting] = React.useState("");
  const me = snapshot()?.user || "";

  // The Waiting count rides its own read, so it shows on every tab; a count
  // that could not be read is left off the label.
  React.useEffect(() => {
    let alive = true;
    waitingLabel().then((n) => { if (alive) setWaiting(n); }, () => undefined);
    return () => { alive = false; };
  }, []);

  const list = usePagedList<DraftSummary>({
    read: listDrafts,
    subject: SUBJECT_LIST,
    params: { state, mine: whose === "mine" ? "true" : undefined },
    sorting: null,
    limit: 100,
    pollMs: POLL_MS,
  });

  const decided = current !== "waiting";
  const columns: ColumnDef<DraftSummary>[] = [
    { id: "id", accessorFn: (r) => r.id, header: plain<DraftSummary>(COLUMN.id), cell: ({ row }) => <span className="font-mono text-foreground">{row.original.id}</span> },
    { id: "what", accessorFn: (r) => r.title, header: plain<DraftSummary>(COLUMN.what), cell: ({ row }) => <span className="whitespace-normal text-foreground">{row.original.title}</span> },
    {
      id: "who",
      accessorFn: (r) => { const w = proposerWords(r.proposer, me, r.source); return w.name + " " + w.line; },
      header: plain<DraftSummary>(COLUMN.who),
      cell: ({ row }) => {
        const w = proposerWords(row.original.proposer, me, row.original.source);
        return (
          <div className="min-w-0 whitespace-normal">
            <div className="truncate font-semibold text-foreground">{w.name}</div>
            {w.line && <div className="text-caption text-muted-foreground">{w.line}</div>}
          </div>
        );
      },
    },
    {
      id: "door",
      accessorFn: (r) => doorWords(r.door) + " " + (r.source || ""),
      header: plain<DraftSummary>(COLUMN.door),
      cell: ({ row }) => (
        <div className="whitespace-normal text-text-2">
          {doorWords(row.original.door)}
          {row.original.source && <div className="truncate font-mono text-caption text-muted-foreground">{fileOf(row.original.source)}</div>}
        </div>
      ),
    },
    ...(decided
      ? [{
        id: "decided",
        accessorFn: (r: DraftSummary) => decidedWords(r.state, r.decided_by?.username),
        header: plain<DraftSummary>(COLUMN.decided),
        cell: ({ row }: { row: { original: DraftSummary } }) => (
          <div className="whitespace-normal">
            <div className="text-foreground">{decidedWords(row.original.state, row.original.decided_by?.username)}</div>
            <Stamp iso={row.original.decided_at || row.original.updated_at} />
          </div>
        ),
      } as ColumnDef<DraftSummary>]
      : [
        { id: "checks", header: plain<DraftSummary>(COLUMN.checks, CHECKS_HELP), cell: ({ row }: { row: { original: DraftSummary } }) => <ChecksCell r={row.original} /> } as ColumnDef<DraftSummary>,
        { id: "waiting", accessorFn: (r: DraftSummary) => r.created_at, header: plain<DraftSummary>(COLUMN.waiting), cell: ({ row }: { row: { original: DraftSummary } }) => <Stamp iso={row.original.created_at} /> } as ColumnDef<DraftSummary>,
      ]),
  ];
  const labels: Record<string, string> = { ...COLUMN };

  const filters = (
    <>
      <Segmented label={WHOSE_LABEL} value={whose} onChange={setWhose} options={(["all", "mine"] as const).map((k) => ({ value: k, label: WHOSE[k] }))} />
      <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder={FILTER_PLACEHOLDER} aria-label={FILTER_LABEL} className="h-9 w-full max-w-sm" />
    </>
  );

  const ls = list.state;
  const rows = ls.kind === "ready" ? ls.rows : [];
  const searching = search.trim() !== "";
  const empty = whose === "mine" ? EMPTY_MINE : EMPTY[current];

  let body: React.ReactNode;
  if (ls.kind === "loading") body = <p className="text-dense text-muted-foreground">{READING_LIST}</p>;
  else if (ls.kind === "error") body = <FetchError subject={SUBJECT_LIST} detail={ls.message} />;
  else if (!rows.length && !searching) {
    body = (
      <>
        <div className="flex flex-wrap items-center gap-2">{filters}</div>
        <EmptyState icon={FilePenLineIcon} title={empty.title}>{empty.body}</EmptyState>
      </>
    );
  } else {
    body = (
      <DataTable<DraftSummary>
        rows={rows}
        columns={columns}
        labels={labels}
        rowKey={(r) => r.id}
        rowName={(r) => "draft " + r.id}
        dataAttr="data-draft"
        onOpen={(r) => navigate("drafts", [r.id])}
        filterBar={filters}
        count={(shown) => shownCount(shown, rows.length)}
        emptyText={searching ? EMPTY_SEARCH : empty.body}
        globalFilter={search}
        widths={WIDTHS}
        foot={
          <div className="flex flex-wrap items-center justify-center gap-3">
            <span className="text-caption text-muted-foreground">{loadedLine(rows.length)}</span>
            {ls.more && (
              <Button variant="outline" size="sm" onClick={() => void list.loadMore()} disabled={list.busy}>
                {list.busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
              </Button>
            )}
          </div>
        }
      />
    );
  }

  return (
    <>
      <PageHead label={route.label} description={route.description} />
      <div className="flex flex-col gap-4 px-6 py-5">
        <Tabs value={current} onValueChange={(v) => navigate("drafts", v === "waiting" ? [] : [v], true)}>
          <div className="flex items-center border-b border-border">
            <TabsList variant="line">
              {TABS.map((t) => (
                <TabsTrigger key={t.key} value={t.key} data-tab={t.key}>
                  {t.label} {t.key === "waiting" && waiting && <span className="font-mono text-caption leading-4 text-muted-foreground" data-count>{waiting}</span>}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>
        </Tabs>
        {ls.kind === "ready" && ls.problem && <FetchError subject={SUBJECT_LIST} detail={ls.problem} lastRead={ls.lastRead} />}
        {body}
      </div>
    </>
  );
}
