import { HEAD } from "@/components/data-table";
import { HelpTip } from "@/components/help-tip";
import { RoleChip } from "@/components/status-badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { WordBadge } from "@/components/users-table";
import type { PolicySetRow } from "@/lib/api";
import { type Bucket, posturesOf } from "@/lib/policy-model";
import { ALSO_OUTSIDE, ALSO_OUTSIDE_TITLE, APPLIES_HELP, BUCKET_HELP, COLUMN, DRAFT, DRAFT_TITLE, EDITED, EDITED_TITLE, EVERYONE, LIVE, LIVE_TITLE, NOT_PARSED, NOT_PARSED_TITLE, REC, REC_MASKED, REC_VERBATIM, STATUS_HELP, countTitle } from "@/lib/policy-words";
import { cn } from "@/lib/utils";
import { agoWord } from "@/lib/settings-words";
import { absTime } from "@/lib/words";

// One category of the Policies list. For
// everyone, For roles and Outside roles render this one column skeleton, so
// the Policy, Status and count edges line up down the whole page and a
// column the Columns menu hides leaves every table at once.

// WIDTHS fixes the layout. Every category renders the same colgroup.
export const WIDTHS: Record<string, string> = { policy: "25%", status: "10%", applies: "16%", deny: "10%", hum: "16%", allow: "10%", updated: "13%" };

// HIDEABLE are the columns the Columns menu offers. Policy is never
// hidden: it is the identifier the row is opened by.
export const HIDEABLE = ["status", "applies", "deny", "hum", "allow", "updated"] as const;
export type Hideable = (typeof HIDEABLE)[number];

// TONE paints a count: a denial in the danger hue, an allow in the ok hue,
// an approval gate in the plain text colour, and a zero muted.
const TONE: Record<Bucket, string> = { deny: "text-danger", hum: "text-foreground", allow: "text-ok" };

// NotReadable is the cell of a set whose stored text no longer parses, so
// its shape is unknown and a count would be a guess.
function NotReadable() {
  return <span className="text-[13px] italic text-muted-foreground" title={NOT_PARSED_TITLE}>{NOT_PARSED}</span>;
}

// PolicyStatus reads the row's state: live, off, or live with a draft that
// edits it.
function PolicyStatus({ row }: { row: PolicySetRow }) {
  if (row.status !== "active") return <WordBadge word={DRAFT} tone="plain" title={DRAFT_TITLE} attr="data-policy-status" />;
  if (row.drift) return <WordBadge word={EDITED} tone="warn" title={EDITED_TITLE} attr="data-policy-status" />;
  return <WordBadge word={LIVE} tone="ok" title={LIVE_TITLE} attr="data-policy-status" />;
}

// RecChip is the recording tag, the one orange badge of the area: recording
// is privacy-significant, so it is never a quiet word.
export function RecChip({ capture }: { capture: string }) {
  return <WordBadge word={REC} tone="warn" title={capture === "redact" ? REC_MASKED : REC_VERBATIM} mono attr="data-rec" />;
}

// AppliesCell names the roles the policy matches, or Everyone when it names
// none. A policy that also names users or identity carries the word for
// that after its chips.
function AppliesCell({ row }: { row: PolicySetRow }) {
  if (!row.summary) return <NotReadable />;
  const roles = row.summary.matchRoles || [];
  const other = row.summary.matchOther;
  if (roles.length === 0 && !other) return <WordBadge word={EVERYONE} tone="plain" attr="data-applies" />;
  return (
    <span className="inline-flex min-w-0 items-center whitespace-nowrap" data-applies={roles.join(",")}>
      {roles.map((r) => <RoleChip key={r} name={r} />)}
      {other && <span className="text-[13px] text-muted-foreground" title={ALSO_OUTSIDE_TITLE}>{ALSO_OUTSIDE}</span>}
    </span>
  );
}

// CountCell is one of the three outcome counts, with the split and the
// lanes behind its hover.
function CountCell({ row, bucket }: { row: PolicySetRow; bucket: Bucket }) {
  if (!row.summary) return <NotReadable />;
  const p = posturesOf(row.summary.postures);
  const n = p[bucket];
  return (
    <span className={cn("font-mono tabular-nums", n ? TONE[bucket] : "text-muted-foreground")} title={countTitle(bucket, p, row.summary.lanes)} data-count={bucket}>
      {n}
    </span>
  );
}

// Head is one column header: the label, and the help icon carrying the
// sentence that says what the column holds.
function Head({ id, right }: { id: Hideable | "policy"; right?: boolean }) {
  const help = id === "status" ? STATUS_HELP : id === "applies" ? APPLIES_HELP : id === "deny" || id === "hum" || id === "allow" ? BUCKET_HELP[id] : null;
  return (
    <span className={cn(HEAD, "inline-flex items-center gap-1", right && "justify-end")}>
      {COLUMN[id]}
      {help && <HelpTip label={COLUMN[id]} text={help} />}
    </span>
  );
}

type Props = {
  rows: PolicySetRow[];
  hidden: Record<string, boolean>;
  onOpen: (row: PolicySetRow) => void;
};

// PoliciesTable renders one category. A row is a button named "Open
// <name>", so a click, Enter or Space opens the policy's page.
export function PoliciesTable({ rows, hidden, onOpen }: Props) {
  const shown = (id: Hideable) => !hidden[id];
  const cols = ["policy", ...HIDEABLE.filter(shown)];
  const right = "text-right";
  return (
    <div className="overflow-x-auto rounded-md border border-border bg-card">
      <Table className="table-fixed">
        <colgroup>
          {cols.map((id) => <col key={id} style={{ width: WIDTHS[id] }} />)}
        </colgroup>
        <TableHeader>
          <TableRow className="hover:bg-transparent">
            <TableHead className="h-10"><Head id="policy" /></TableHead>
            {shown("status") && <TableHead className="h-10"><Head id="status" /></TableHead>}
            {shown("applies") && <TableHead className="h-10"><Head id="applies" /></TableHead>}
            {shown("deny") && <TableHead className={cn("h-10", right)}><Head id="deny" right /></TableHead>}
            {shown("hum") && <TableHead className={cn("h-10", right)}><Head id="hum" right /></TableHead>}
            {shown("allow") && <TableHead className={cn("h-10", right)}><Head id="allow" right /></TableHead>}
            {shown("updated") && <TableHead className="h-10"><Head id="updated" /></TableHead>}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row) => (
            <TableRow
              key={row.name}
              data-policy={row.name}
              role="button"
              tabIndex={0}
              aria-label={"Open " + row.name}
              className="cursor-pointer text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
              onClick={() => onOpen(row)}
              onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(row); } }}
            >
              <TableCell>
                <div className="flex min-w-0 items-center gap-2">
                  <span className="truncate font-mono font-semibold text-foreground">{row.name}</span>
                  {row.summary?.capture && <RecChip capture={row.summary.capture} />}
                </div>
                {row.summary?.description && (
                  <div className="truncate text-[13px] text-muted-foreground" title={row.summary.description}>{row.summary.description}</div>
                )}
              </TableCell>
              {shown("status") && <TableCell><PolicyStatus row={row} /></TableCell>}
              {shown("applies") && <TableCell className="overflow-hidden"><AppliesCell row={row} /></TableCell>}
              {shown("deny") && <TableCell className={right}><CountCell row={row} bucket="deny" /></TableCell>}
              {shown("hum") && <TableCell className={right}><CountCell row={row} bucket="hum" /></TableCell>}
              {shown("allow") && <TableCell className={right}><CountCell row={row} bucket="allow" /></TableCell>}
              {shown("updated") && (
                <TableCell className="whitespace-nowrap text-text-2" title={absTime(row.updated_at)}>{agoWord(row.updated_at)}</TableCell>
              )}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
