import * as React from "react";
import { CheckIcon, ChevronRightIcon, DownloadIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HEAD } from "@/components/data-table";
import { HelpTip } from "@/components/help-tip";
import { KindGlyph, markOf } from "@/components/role-kind";
import { RoleChip } from "@/components/status-badge";
import { WordBadge } from "@/components/users-table";
import type { BindingRow, RoleRow } from "@/lib/api";
import {
  CATEGORY, CATEGORY_COLUMN, COLUMN, COLUMN_HELP, type Kind, MCP_ADMIN, MINTED_FOLD_HELP, NOT_READ, NOT_READ_WHY, NO_DESCRIPTION, NO_ROLE_IN_VIEW, REACH, SUMMARY, TOOLS_NONE,
  exportLabel, exportTitle, holdersTitle, kindOf, mintedFold, notReadRefused, policiesTitle, summaryAdministers, summaryConsole, summaryIncludes, toolsCell, toolsWords,
} from "@/lib/role-words";
import { OWNED_BY, ownedChipTitle } from "@/lib/server-roles-words";
import { cn } from "@/lib/utils";

// One category of the Roles list. The
// four categories share one column skeleton, so Description, Holders,
// Policies and the export column start on the same vertical line down the
// whole page, and every row is one line high: what does not fit is
// truncated or folded behind a chip. The Application table trades Reach
// for Server and Tools, which are the two readings its rows need.

// Skeleton is a table's columns in order, each with its share of the width
// in percent.
export type Skeleton = { id: string; width: number }[];

// APP_SKELETON lays out the Application table, REACH_SKELETON the other
// three: Description takes the Server width there, so the Reach, Holders,
// Policies and export edges fall where the Application table puts them.
export const APP_SKELETON: Skeleton = [
  { id: "name", width: 25 },
  { id: "description", width: 11 },
  { id: "server", width: 20 },
  { id: "tools", width: 22 },
  { id: "holders", width: 7 },
  { id: "policies", width: 11 },
  { id: "export", width: 4 },
];
export const REACH_SKELETON: Skeleton = [
  { id: "name", width: 25 },
  { id: "description", width: 31 },
  { id: "reach", width: 22 },
  { id: "holders", width: 7 },
  { id: "policies", width: 11 },
  { id: "export", width: 4 },
];

// widths gives a hidden column's width to the nearest shown column on its
// left, so every edge to its right stays where the other tables put it.
// Name is never hidden, so there is always a column to take the width.
export function widths(skeleton: Skeleton, hidden: Record<string, boolean>): { id: string; width: string }[] {
  const shown: { id: string; width: number }[] = [];
  for (const col of skeleton) {
    if (hidden[col.id] && shown.length > 0) shown[shown.length - 1].width += col.width;
    else shown.push({ ...col });
  }
  return shown.map((c) => ({ id: c.id, width: c.width + "%" }));
}

// HIDEABLE are the columns the Columns menu offers, in the order it lists
// them. Name and the export column are never hidden: one is the
// identifier, the other the row action.
export const HIDEABLE = ["description", "server", "reach", "tools", "holders", "policies"] as const;

// Chip is one server an application role reaches: its name, what the role
// gets there in words, and whether that server owns the role.
export type Chip = { name: string; tools: string; owned?: boolean };

// serverChips names the servers an application role reaches: the server
// that owns it first, read off the role itself, then every access row to
// another server. totals counts the tools each server serves, so a glob
// reads as a number rather than a star. null means the reach could not be
// read, which is the access rows failing on a role no server owns.
export function serverChips(role: RoleRow, bindings: BindingRow[] | null, totals: Record<string, number>): Chip[] | null {
  const owned: Chip[] = role.server ? [{ name: role.server, tools: toolsWords(role.tools, totals[role.server] ?? null), owned: true }] : [];
  if (!bindings) return owned.length > 0 ? owned : null;
  const rest = bindings
    .filter((b) => b.role === role.name && b.app !== role.server)
    .map((b) => ({ name: b.app, tools: toolsWords(b.tools, totals[b.app] ?? null) }));
  return owned.concat(rest);
}

// areaNames names the console areas of a straza role once each, read off
// the stored "<area>:<level>" grants.
function areaNames(areas: string[]): string[] {
  return areas.map((a) => a.split(":")[0]).filter((a, i, xs) => a && xs.indexOf(a) === i);
}

// Fold shows the first chips and folds the rest behind a +N chip whose
// hover names them, so the cell stays on one line: two in the Server cell,
// one in the Reach cell, where a word precedes the chips. draw renders one
// chip, since the Server cell marks the server that owns the role.
function Fold({ names, show = 2, draw }: { names: string[]; show?: number; draw?: (name: string) => React.ReactNode }) {
  const rest = names.slice(show);
  return (
    <span className="inline-flex items-center whitespace-nowrap">
      {names.slice(0, show).map((n) => <React.Fragment key={n}>{draw ? draw(n) : <RoleChip name={n} />}</React.Fragment>)}
      {rest.length > 0 && (
        <span title={rest.join(", ")} data-more-reach={rest.length}><RoleChip name={"+" + rest.length} /></span>
      )}
    </span>
  );
}

// Word is the muted reading of a cell that has no chips; title says why
// when the cell's own read failed.
function Word({ text, muted, title }: { text: string; muted?: boolean; title?: string }) {
  return <span className={muted ? "text-muted-foreground" : "text-text-2"} title={title}>{text}</span>;
}

// ServerCell names every server an application role reaches. The owned
// server reads "owned by <server>" behind a check, so the mark explains
// itself in the cell, with the ownership sentence on hover: its admin
// defined the role there and it reaches that server alone.
function ServerCell({ chips, why }: { chips: Chip[] | null; why: string }) {
  if (!chips) return <Word text={NOT_READ} title={why} muted />;
  if (chips.length === 0) return <Word text={REACH.noServer} muted />;
  const draw = (name: string) => {
    const owned = chips.some((c) => c.name === name && c.owned);
    return (
      <span data-server={name} className={cn("inline-flex align-middle", owned && "mr-1")}>
        {owned
          ? <WordBadge word={name} tone="teal" title={ownedChipTitle(name)} mono attr="data-owned-by" lead={<><CheckIcon aria-hidden="true" /><span className="font-sans">{OWNED_BY + " "}</span></>} />
          : <RoleChip name={name} />}
      </span>
    );
  };
  return <Fold names={chips.map((c) => c.name)} draw={draw} />;
}

// ToolsCell says what the role gets on each server it reaches, in the
// words the access rows themselves read in.
function ToolsCell({ chips, why }: { chips: Chip[] | null; why: string }) {
  if (!chips) return <Word text={NOT_READ} title={why} muted />;
  if (chips.length === 0) return <Word text={TOOLS_NONE} muted />;
  const words = toolsCell(chips.map((c) => ({ server: c.name, words: c.tools })));
  return <span className="block truncate text-text-2" title={words}>{words}</span>;
}

// Head is one column header: the label, and the help icon carrying the one
// sentence that says what the column holds, for the columns that need one.
// A right-aligned header is a flex row aligned to its end, so a word wider
// than a narrow number column hangs left into the empty end of the header
// before it rather than right over the next header.
function Head({ id, right }: { id: keyof typeof COLUMN; right?: boolean }) {
  const help = COLUMN_HELP[id];
  return (
    <span className={cn(HEAD, "items-center gap-1", right ? "flex justify-end" : "inline-flex")}>
      {COLUMN[id]}
      {help && <HelpTip label={COLUMN[id]} text={help} />}
    </span>
  );
}

type Props = {
  // kind picks the skeleton: the Application table carries Server and
  // Tools where the other three carry Reach.
  kind: Kind | "all";
  rows: RoleRow[];
  // folded are the rows that sit under the fold at the end of the table,
  // the minted roles nobody holds. Empty for every other category.
  folded?: RoleRow[];
  // chips names the servers each role reaches, by role name; a null entry
  // is a reach that could not be read.
  chips: Record<string, Chip[] | null>;
  // administers names the servers each role administers, by role id; null
  // when the servers list could not be read.
  administers: Record<string, string[]> | null;
  // facets counts the policy sets naming each role, by role name; null
  // when the policies could not be read.
  facets: Record<string, number> | null;
  // refused says which side reads the server refused with a 403, so their
  // cells name the grant; any other failure keeps the reload words.
  refused?: { apps: boolean; policy: boolean };
  hidden: Record<string, boolean>;
  onOpen: (role: RoleRow) => void;
  onExport: (role: RoleRow) => void;
};

// RolesTable renders one category. A row is a button named "Open <name>",
// so a click, Enter or Space opens the role's page; the export button at
// the row end keeps its click to itself.
export function RolesTable({ kind, rows, folded = [], chips, administers, facets, refused = { apps: false, policy: false }, hidden, onOpen, onExport }: Props) {
  const [open, setOpen] = React.useState(false);
  const allSkeleton: Skeleton = [{ id: "name", width: 28 }, { id: "category", width: 15 }, { id: "reach", width: 31 }, { id: "holders", width: 10 }, { id: "policies", width: 10 }, { id: "export", width: 6 }];
  const cols = widths(kind === "all" ? allSkeleton : kind === "application" ? APP_SKELETON : REACH_SKELETON, hidden);
  const shown = (id: string) => cols.some((c) => c.id === id);
  const appsWhy = refused.apps ? notReadRefused("apps:read") : NOT_READ_WHY;
  // rowOf renders one role: the rows of the table and, under the fold, the
  // minted roles nobody holds, so both read the same.
  const rowOf = (r: RoleRow) => {
    const approver = kindOf(r) === "approver";
    const deciders = r.decider_in || [];
    const facet = facets ? facets[r.name] || 0 : null;
    const reach = chips[r.name] ?? null;
    return (
      <TableRow
        key={r.id}
        data-role={r.name}
        role="button"
        tabIndex={0}
        aria-label={"Open " + r.name}
        className="text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 cursor-pointer"
        onClick={() => onOpen(r)}
        onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(r); } }}
      >
        <TableCell className="overflow-hidden">
          <span className="flex min-w-0 items-center gap-2">
            <KindGlyph mark={markOf(r, administers ? administers[r.id] || [] : null)} />
            <span className="truncate font-mono text-foreground">{r.name}</span>
          </span>
        </TableCell>
        {shown("category") && <TableCell><span className="role-category">{CATEGORY[kindOf(r)].title}</span></TableCell>}
        {shown("description") && (
          <TableCell className="overflow-hidden">
            {r.description
              ? <span className="block truncate text-[13px] text-muted-foreground" title={r.description}>{r.description}</span>
              : <span className="text-[13px] italic text-muted-foreground">{NO_DESCRIPTION}</span>}
          </TableCell>
        )}
        {shown("server") && <TableCell className="overflow-hidden"><ServerCell chips={reach} why={appsWhy} /></TableCell>}
        {shown("tools") && <TableCell className="overflow-hidden"><ToolsCell chips={reach} why={appsWhy} /></TableCell>}
        {shown("reach") && <TableCell className="overflow-hidden"><span className="block truncate text-[13px] text-text-2" title={reachSummary(r, reach, administers, refused.apps)}>{reachSummary(r, reach, administers, refused.apps)}</span></TableCell>}
        {shown("holders") && (
          <TableCell className="text-right tabular-nums" title={holdersTitle(r.holder_count || 0, r.assigned_count || 0)}>
            <span className={r.holder_count ? "text-foreground" : "text-muted-foreground"}>{r.holder_count || 0}</span>
          </TableCell>
        )}
        {shown("policies") && (
          <TableCell className="text-right tabular-nums" title={approver ? policiesTitle(deciders.length, deciders) : facet === null ? undefined : policiesTitle(facet)}>
            {approver
              ? <span className={deciders.length ? "text-foreground" : "text-muted-foreground"}>{deciders.length}</span>
              : facet === null
                ? <span className="text-[13px] text-muted-foreground" title={refused.policy ? notReadRefused("policy:read") : NOT_READ_WHY}>{NOT_READ}</span>
                : <span className={facet ? "text-foreground" : "text-muted-foreground"}>{facet}</span>}
          </TableCell>
        )}
        <TableCell className="text-right">
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={exportLabel(r.name)}
            title={exportTitle(r.name)}
            onClick={(e) => { e.stopPropagation(); onExport(r); }}
            onKeyDown={(e) => e.stopPropagation()}
          >
            <DownloadIcon />
          </Button>
        </TableCell>
      </TableRow>
    );
  };

  return (
    <div className="overflow-x-auto rounded-md border border-border bg-card">
      <Table className="table-fixed min-w-[720px]">
        <colgroup>
          {cols.map((c) => <col key={c.id} style={{ width: c.width }} />)}
        </colgroup>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-10"><Head id="name" /></TableHead>
            {shown("category") && <TableHead className="h-10">{CATEGORY_COLUMN}</TableHead>}
            {shown("description") && <TableHead className="h-10"><Head id="description" /></TableHead>}
            {shown("server") && <TableHead className="h-10"><Head id="server" /></TableHead>}
            {shown("tools") && <TableHead className="h-10"><Head id="tools" /></TableHead>}
            {shown("reach") && <TableHead className="h-10"><Head id="reach" /></TableHead>}
            {shown("holders") && <TableHead className="h-10 text-right"><Head id="holders" right /></TableHead>}
            {shown("policies") && <TableHead className="h-10 text-right"><Head id="policies" right /></TableHead>}
            <TableHead className="h-10" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.length === 0 && folded.length === 0 && <TableRow><TableCell colSpan={cols.length} className="py-6 text-muted-foreground">{NO_ROLE_IN_VIEW}</TableCell></TableRow>}
          {rows.map(rowOf)}
          {folded.length > 0 && (
            <TableRow className="hover:bg-transparent">
              <TableCell colSpan={cols.length} className="p-0">
                <button
                  type="button"
                  aria-expanded={open}
                  title={MINTED_FOLD_HELP}
                  onClick={() => setOpen(!open)}
                  data-minted-fold
                  className="flex w-full items-center gap-1.5 px-4 py-2 text-left text-[13px] text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  <ChevronRightIcon aria-hidden="true" className={cn("size-3.5 transition-transform", open && "rotate-90")} />
                  {mintedFold(folded.length)}
                </button>
              </TableCell>
            </TableRow>
          )}
          {open && folded.map(rowOf)}
        </TableBody>
      </Table>
    </div>
  );
}

// Reach describes configuration, never a promise that policy will allow a call.
// appsRefused says the server refused the access rows or the servers with
// a 403, so an empty read names the grant rather than a failure.
export function reachSummary(role: RoleRow, chips: Chip[] | null, administers: Record<string, string[]> | null, appsRefused = false): string {
  const kind = kindOf(role);
  if (kind === "application") return chips === null ? (appsRefused ? SUMMARY.toolsRefused : SUMMARY.toolsNotRead) : chips.length ? chips.map((c) => c.name + ": " + c.tools).join(" · ") : SUMMARY.noAccess;
  if (kind === "business") return role.implies?.length ? summaryIncludes(role.implies) : SUMMARY.noIncluded;
  if (kind === "approver") return SUMMARY.approver;
  if (role.name === MCP_ADMIN) return SUMMARY.mcpAdmin;
  if (role.areas?.includes("full")) return SUMMARY.allAreas;
  const servers = administers?.[role.id] || [];
  if (servers.length) return summaryAdministers(servers);
  const areas = areaNames(role.areas || []);
  if (areas.length) return summaryConsole(areas);
  return administers === null ? (appsRefused ? SUMMARY.adminRefused : SUMMARY.adminNotRead) : SUMMARY.noAreas;
}
