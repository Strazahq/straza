import * as React from "react";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HEAD } from "@/components/data-table";
import { ChangeChip, OutcomeChip, Pill, Tag, ToolChip } from "@/components/chip";
import { ObjectTag } from "@/components/draft-parts";
import { KIND_ICON } from "@/components/kind-icon";
import { Segmented } from "@/components/segmented";
import type { Outcome } from "@/lib/chip-words";
import { ON_MACHINES, ON_MACHINES_TITLE } from "@/lib/harness-words";
import { machineServers, onMachines } from "@/lib/machine-servers";
import type { Bucket, Lane, RuleView } from "@/lib/policy-model";
import {
  ALL_RULES, BUCKET_OUTCOME, BUCKET_WORD, EVERY_SERVER, EVERY_WORD, MCP_SERVER_TITLE, NO_REASON_YET, NO_RULE_MATCH, RESTATES_ACCESS,
  RULES_FILTER, RULES_SEARCH, RULE_COLUMN, RULE_ID_TITLE, WHERE_WORD, howLine, openRule, rulesMatch, sentence,
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
  // registered are the names of the registered MCP servers, or null while
  // they are unread, and then no row says a server is on people's machines.
  registered?: string[] | null;
};

const COLUMNS = ["where", "calls", "what", "reason", "rule"] as const;
const WIDTHS: Record<string, string> = { where: "17%", calls: "26%", what: "19%", reason: "24%", rule: "14%" };

// LANE_ORDER sorts the table by where the call goes: the MCP servers
// first, then the local lanes in the order the enterprise profile meets
// them.
const LANE_ORDER: Record<Lane, number> = { mcp: 0, shell: 1, files: 2, net: 3, harness: 4, tools: 5, other: 6, all: 7 };

// TOOL_LANES are the lanes whose names are tool names, which wear the
// policy rule's outcome. A command pattern and a path are not tools.
const TOOL_LANES: Lane[] = ["mcp", "harness", "tools"];

const FILTERS: (Bucket | "all")[] = ["all", "deny", "hum", "allow"];
const FILTER_WORD = (f: Bucket | "all") => (f === "all" ? ALL_RULES : BUCKET_WORD[f]);

// whereKey is the sort key of one row: the lane, the server of an MCP
// rule, and the id to break the tie.
const whereKey = (r: RuleView) => [LANE_ORDER[r.lane], r.lane === "mcp" ? r.app || "" : "", r.id].join(" ");

// hit says whether a rule answers the search, over the words the operator
// has in hand: the id, the reason, the calls and the server.
function hit(r: RuleView, q: string): boolean {
  if (!q) return true;
  return [r.id, r.reason, r.app || "", ...(r.names || [])].some((t) => t.toLowerCase().includes(q));
}

// Where is the first cell: the servers an MCP rule matches, each as a tag
// of its kind, a registered server or one on a person's machine, and the
// lane's own word for every other rule. The checks of a draft show it in
// their table of loosened rules.
export function Where({ rule, registered }: { rule: RuleView; registered?: string[] | null }) {
  if (rule.lane === "mcp") {
    const machine = registered ? machineServers(rule, registered) : [];
    return (
      <span className="flex min-w-0 flex-wrap items-center gap-1">
        {rule.app
          ? rule.app.split(", ").map((s) => <ObjectTag key={s} kind={machine.includes(s) ? "local-server" : "server"} name={s} />)
          : <Tag icon={KIND_ICON.server} title={MCP_SERVER_TITLE}>{EVERY_SERVER}</Tag>}
        {onMachines(rule, registered) && (
          <Pill title={ON_MACHINES_TITLE} className="max-w-full" data-on-machines><span className="min-w-0 truncate">{ON_MACHINES}</span></Pill>
        )}
      </span>
    );
  }
  return <Pill className="max-w-full"><span className="min-w-0 truncate">{WHERE_WORD[rule.lane]}</span></Pill>;
}

// Calls names what the rule matches on its lane, or the lane's every-call
// word when it names nothing. A tool name wears the outcome when one is
// given. The draft page shows it in its rule tables, without the outcome.
export function Calls({ rule, outcome }: { rule: RuleView; outcome?: Outcome }) {
  const names = rule.names && rule.names.length ? rule.names : null;
  const wears = names && TOOL_LANES.includes(rule.lane) ? outcome : undefined;
  return (
    <span className="flex max-w-full min-w-0 flex-wrap items-center gap-1">
      {(names || [EVERY_WORD[rule.lane]]).map((n) => (
        // A long name or pattern is cut inside its chip, whole on hover.
        <ToolChip key={n} outcome={wears}>{n}</ToolChip>
      ))}
    </span>
  );
}

// Reason reads the sentence the agent gets. A rule that allows an MCP call
// carries none, because the outcome only restates the access the role
// already has; a denial or an approval without one is a gap.
function Reason({ rule, registered }: { rule: RuleView; registered?: string[] | null }) {
  if (rule.reason) return <span className="block truncate text-text-2" title={rule.reason}>{rule.reason}</span>;
  if (rule.bucket === "allow") {
    if (rule.lane !== "mcp" || onMachines(rule, registered)) return null;
    return <span className="text-muted-foreground">{RESTATES_ACCESS}</span>;
  }
  return <span className="text-danger">{NO_REASON_YET}</span>;
}

export function PolicyRules({ rules, changed, added, openId, onOpen, registered }: Props) {
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
        <Segmented label={RULES_FILTER} value={filter} onChange={setFilter} options={FILTERS.map((f) => ({ value: f, label: FILTER_WORD(f) }))} data-rules-filter={filter} />
        <Input
          aria-label={RULES_SEARCH}
          placeholder={RULES_SEARCH}
          value={search}
          spellCheck={false}
          className="h-8 w-72"
          onChange={(e) => setSearch(e.target.value)}
        />
        {q !== "" && <span className="text-caption text-muted-foreground" data-rules-match>{rulesMatch(shown.length, rules.length)}</span>}
      </div>

      <div className="overflow-x-auto rounded-md border border-border bg-card" data-rules={shown.length}>
        {/* The floor keeps the outcome chip inside What happens and a
            server's tag inside Call type; below it the table scrolls in
            its box. */}
        <Table className="min-w-[880px] table-fixed">
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
                  className={cn("cursor-pointer align-top text-dense focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50", openId === rule.id && "bg-accent-bg")}
                  onClick={() => onOpen(rule.id)}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(rule.id); } }}
                >
                  <TableCell className="overflow-hidden align-top whitespace-normal"><Where rule={rule} registered={registered} /></TableCell>
                  <TableCell className="overflow-hidden align-top whitespace-normal"><Calls rule={rule} outcome={BUCKET_OUTCOME[rule.bucket]} /></TableCell>
                  <TableCell className="align-top whitespace-normal">
                    <OutcomeChip outcome={BUCKET_OUTCOME[rule.bucket]} />
                    {how !== "" && <span className="block text-caption leading-snug text-muted-foreground">{how}</span>}
                  </TableCell>
                  <TableCell className="overflow-hidden align-top"><Reason rule={rule} registered={registered} /></TableCell>
                  <TableCell className="overflow-hidden align-top">
                    <span className="flex min-w-0 flex-wrap items-center gap-1.5">
                      <span className="block max-w-full truncate font-mono text-caption text-muted-foreground" title={rule.id + ": " + RULE_ID_TITLE}>{rule.id}</span>
                      {badge && <ChangeChip change={badge === "new" ? "new" : "changed"} />}
                    </span>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </div>
  );
}
