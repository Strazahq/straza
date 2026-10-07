import * as React from "react";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HEAD } from "@/components/data-table";
import { WordBadge } from "@/components/users-table";
import type { Bucket, Lane, RuleView } from "@/lib/policy-model";
import {
  ALL_RULES, BUCKET_WORD, EVERY_SERVER, EVERY_WORD, MARK, MCP_SERVER_TITLE, NO_REASON_YET, NO_RULE_MATCH, RESTATES_ACCESS,
  RULES_FOOT, RULES_SEARCH, RULE_COLUMN, RULE_ID_TITLE, WHERE_WORD, howLine, openRule, rulesMatch, sentence,
} from "@/lib/policy-words";
import { cn } from "@/lib/utils";

// The Rules tab of a policy's page: one row per
// rule, read left to right as where the call goes, which calls, what
// happens to them and why. A row opens the rule's sheet and the table
// stays put, so the reading order of the policy never moves under the
// person editing it.

// Mark is what the page says about a row it has not published yet.
export type Mark = "edited" | "new";

type Props = {
  rules: RuleView[];
  // changed are the rule ids the page edited and added are the ids it
  // added; both are unpublished, and the Rule cell says which.
  changed: string[];
  added: string[];
  // openId is the rule whose sheet is open, or null.
  openId: string | null;
  onOpen: (id: string) => void;
};

const COLUMNS = ["where", "calls", "what", "reason", "rule"] as const;
const WIDTHS: Record<string, string> = { where: "14%", calls: "26%", what: "19%", reason: "27%", rule: "14%" };

// TONE paints the outcome word in its reserved hue.
const TONE: Record<Bucket, string> = { deny: "text-danger", hum: "text-warn", allow: "text-ok" };

// LANE_ORDER sorts the table by where the call goes: the MCP servers
// first, then the local lanes in the order the enterprise profile meets
// them.
const LANE_ORDER: Record<Lane, number> = { mcp: 0, shell: 1, files: 2, net: 3, tools: 4, other: 5, all: 6 };

const CHIP = "inline-flex items-center rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] whitespace-nowrap";

const FILTERS: (Bucket | "all")[] = ["all", "deny", "hum", "allow"];
const FILTER_WORD = (f: Bucket | "all") => (f === "all" ? ALL_RULES : BUCKET_WORD[f]);
const SEG = "inline-flex h-8 items-center border-r border-border px-3 text-[13px] text-text-2 last:border-r-0 hover:bg-accent focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50";

// whereKey is the sort key of one row: the lane, the server of an MCP
// rule, and the id to break the tie.
const whereKey = (r: RuleView) => [LANE_ORDER[r.lane], r.lane === "mcp" ? r.app || "" : "", r.id].join(" ");

// hit says whether a rule answers the search, over the words the operator
// has in hand: the id, the reason, the calls and the server.
function hit(r: RuleView, q: string): boolean {
  if (!q) return true;
  return [r.id, r.reason, r.app || "", ...(r.names || [])].some((t) => t.toLowerCase().includes(q));
}

// Where is the first cell: the server an MCP rule matches in the accent,
// and the lane's own word for every other rule.
function Where({ rule }: { rule: RuleView }) {
  if (rule.lane === "mcp") {
    return <span className={cn(CHIP, "border-link/55 bg-accent-bg font-semibold text-foreground")} title={MCP_SERVER_TITLE}>{rule.app || EVERY_SERVER}</span>;
  }
  return <span className={cn(CHIP, "font-sans text-[12px] text-muted-foreground")}>{WHERE_WORD[rule.lane]}</span>;
}

// Calls names what the rule matches on its lane, or the lane's every-call
// word when it names nothing.
function Calls({ rule }: { rule: RuleView }) {
  const names = rule.names && rule.names.length ? rule.names : null;
  return (
    <span className="flex flex-wrap items-center gap-1">
      {(names || [EVERY_WORD[rule.lane]]).map((n) => (
        <span key={n} className={cn(CHIP, names && "font-semibold text-foreground")}>{n}</span>
      ))}
    </span>
  );
}

// Reason reads the sentence the agent gets. A rule that allows an MCP call
// carries none, because the outcome only restates the access the role
// already has; a denial or an approval without one is a gap.
function Reason({ rule }: { rule: RuleView }) {
  if (rule.reason) return <span className="block truncate text-text-2" title={rule.reason}>{rule.reason}</span>;
  if (rule.bucket === "allow") {
    if (rule.lane !== "mcp") return null;
    return <span className="text-muted-foreground">{RESTATES_ACCESS}</span>;
  }
  return <span className="text-danger">{NO_REASON_YET}</span>;
}

export function PolicyRules({ rules, changed, added, openId, onOpen }: Props) {
  const [filter, setFilter] = React.useState<Bucket | "all">("all");
  const [search, setSearch] = React.useState("");

  const q = search.trim().toLowerCase();
  const mark = (id: string): Mark | null => (added.includes(id) ? "new" : changed.includes(id) ? "edited" : null);
  const shown = rules
    .filter((r) => (filter === "all" || r.bucket === filter) && hit(r, q))
    .sort((a, b) => {
      // A rule added on this page sits at the top until it is published,
      // so the row the operator just made is the row they land on.
      const first = (added.includes(a.id) ? 0 : 1) - (added.includes(b.id) ? 0 : 1);
      return first || whereKey(a).localeCompare(whereKey(b));
    });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label={RULE_COLUMN.what} className="inline-flex w-fit overflow-hidden rounded-md border border-border" data-rules-filter={filter}>
          {FILTERS.map((f) => (
            <button
              key={f}
              type="button"
              aria-pressed={filter === f}
              className={cn(SEG, filter === f && "bg-accent-bg font-semibold text-foreground")}
              onClick={() => setFilter(f)}
            >
              {FILTER_WORD(f)}
            </button>
          ))}
        </div>
        <Input
          aria-label={RULES_SEARCH}
          placeholder={RULES_SEARCH}
          value={search}
          spellCheck={false}
          className="h-8 w-72"
          onChange={(e) => setSearch(e.target.value)}
        />
        {q !== "" && <span className="text-[13px] text-muted-foreground" data-rules-match>{rulesMatch(shown.length, rules.length)}</span>}
      </div>

      <div className="overflow-x-auto rounded-md border border-border bg-card" data-rules={shown.length}>
        <Table className="table-fixed">
          <colgroup>
            {COLUMNS.map((id) => <col key={id} style={{ width: WIDTHS[id] }} />)}
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              {COLUMNS.map((id) => <TableHead key={id} className="h-10"><span className={HEAD}>{RULE_COLUMN[id]}</span></TableHead>)}
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.length === 0 && (
              <TableRow>
                <TableCell colSpan={COLUMNS.length} className="h-16 text-center text-muted-foreground" data-empty-text>{NO_RULE_MATCH}</TableCell>
              </TableRow>
            )}
            {shown.map((rule) => {
              const how = howLine(rule);
              const badge = mark(rule.id);
              return (
                <TableRow
                  key={rule.id}
                  data-rule={rule.id}
                  data-group={rule.bucket}
                  data-mark={badge || undefined}
                  data-rule-open={openId === rule.id ? rule.id : undefined}
                  role="button"
                  tabIndex={0}
                  aria-label={openRule(rule.id)}
                  title={sentence(rule)}
                  className={cn("cursor-pointer align-top text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50", openId === rule.id && "bg-accent-bg")}
                  onClick={() => onOpen(rule.id)}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(rule.id); } }}
                >
                  <TableCell className="align-top"><Where rule={rule} /></TableCell>
                  <TableCell className="overflow-hidden align-top whitespace-normal"><Calls rule={rule} /></TableCell>
                  <TableCell className="align-top whitespace-normal">
                    <span className={cn("font-semibold", TONE[rule.bucket])}>{BUCKET_WORD[rule.bucket]}</span>
                    {how !== "" && <span className="block text-[13px] leading-snug text-muted-foreground">{how}</span>}
                  </TableCell>
                  <TableCell className="overflow-hidden align-top"><Reason rule={rule} /></TableCell>
                  <TableCell className="overflow-hidden align-top">
                    <span className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="block max-w-full truncate font-mono text-[13px] text-muted-foreground" title={rule.id + ": " + RULE_ID_TITLE}>{rule.id}</span>
                      {badge && <WordBadge word={MARK[badge]} tone={badge === "new" ? "accent" : "warn"} />}
                    </span>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>

      <p className="text-[13px] text-muted-foreground">{RULES_FOOT}</p>
    </div>
  );
}
