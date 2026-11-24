import * as React from "react";
import { ChevronRightIcon, ClockIcon, PowerIcon, ServerIcon, TicketIcon, UserRoundIcon } from "lucide-react";
import { ChangeChip, OutcomeChip, Pill, ToolChip } from "@/components/chip";
import { Facts, FiveMore, ObjectTag } from "@/components/draft-parts";
import { Calls } from "@/components/policy-rules";
import { Button } from "@/components/ui/button";
import type { DraftGate } from "@/lib/api";
import type { Kind } from "@/lib/chip-words";
import type { ChangeWhat } from "@/lib/drafts-model";
import { type Aspect, type Marked, type SetReading, gateOf, hasPatterns, namesMove, outcomeOf, ruleAspects } from "@/lib/drafts-rules";
import { CARD, EVERYONE_PILL, NOT_READABLE_DOC, OFF_PILL, REMOVED_PILL, SEE_YAML, SET_CHANGE_COLUMN, SET_COLUMN, STAYS_STORED, rulesUnchanged, showPattern } from "@/lib/drafts-words";
import { machineServers } from "@/lib/machine-servers";
import type { RuleView } from "@/lib/policy-model";
import {
  ALSO_OUTSIDE, ALSO_OUTSIDE_TITLE, ANY_ARGUMENTS, CHECKED_FIRST, EVERY_SERVER, EVERY_WORD, NO_RECORDING, REQUESTER_CONFIRMS, REQUESTER_PILL, SPONSOR_WORDS, TICKET_PILL, WHERE_WORD,
  durationWords, eventsWords, recWord, withinWords,
} from "@/lib/policy-words";
import { cn } from "@/lib/utils";

// The body of a policy set's card on a draft's page: who the set applies
// to and whether it records, then its policy rules. A new set lists every
// policy rule, a changed set only the ones the draft adds, edits or
// removes, one line per field that changes, and folds the rest. A set
// turned off or removed says so in one row.

const TH = "px-3 py-2 text-left text-caption font-semibold text-muted-foreground";
const TD = "px-3 py-2 align-top";
const NAME = "font-mono text-caption font-semibold break-all text-foreground";

type Ctx = {
  // registered is the registered server list after publishing, null where
  // the page cannot tell a server on a person's machine apart.
  registered: string[] | null;
  // roleKind is the kind icon of a role the draft holds, null for another.
  roleKind: (role: string) => Kind | null;
};

// Servers names the servers an MCP rule matches: a server on a person's
// machine with its own icon where the page can tell, and every server
// where the rule names none. A rule on another lane names no server.
function Servers({ rule, registered }: { rule: RuleView; registered: string[] | null }) {
  if (rule.lane !== "mcp") return null;
  if (!rule.app) return <Pill icon={ServerIcon}>{EVERY_SERVER}</Pill>;
  const machine = registered ? machineServers(rule, registered) : [];
  return <>{rule.app.split(", ").map((s) => <ObjectTag key={s} kind={machine.includes(s) ? "local-server" : "server"} name={s} />)}</>;
}

// CallsCell is the Calls cell of the policy page with the servers an MCP
// rule matches before it.
function CallsCell({ rule, registered }: { rule: RuleView; registered: string[] | null }) {
  return (
    <span className="flex max-w-full min-w-0 flex-wrap items-center gap-1.5">
      <Servers rule={rule} registered={registered} />
      <Calls rule={rule} />
    </span>
  );
}

// GatePills are the facts of a gate after its outcome chip, as the policy
// card and the checks draw them: the requester confirming, who decides, a
// ticket, how long a person has, and the checks before the call runs. The
// window to use or retry an approval is the hover of its time.
export function GatePills({ gate: g }: { gate: DraftGate }) {
  return (
    <>
      {g.confirm && <Pill>{REQUESTER_CONFIRMS}</Pill>}
      {g.sponsor && <Pill icon={UserRoundIcon}>{SPONSOR_WORDS}</Pill>}
      {(g.roles || []).map((r) => <ObjectTag key={r} kind="role-approver" name={r} />)}
      {g.requester && <Pill icon={UserRoundIcon}>{REQUESTER_PILL}</Pill>}
      {g.class === "ticket" && <Pill icon={TicketIcon}>{TICKET_PILL}</Pill>}
      {!!g.seconds && <Pill icon={ClockIcon} title={g.within_seconds ? withinWords(g.class === "ticket", g.within_seconds) : undefined}>{durationWords(g.seconds)}</Pill>}
      {g.any_arguments && <Pill>{ANY_ARGUMENTS}</Pill>}
      {(g.classifier || g.server_check) && <Pill>{CHECKED_FIRST}</Pill>}
    </>
  );
}

// RuleOutcome is a rule's outcome chip, and after needs approval the pills
// of its gate.
export function RuleOutcome({ rule }: { rule: RuleView }) {
  const gate = gateOf(rule);
  return <><OutcomeChip outcome={outcomeOf(rule)} />{gate && <GatePills gate={gate} />}</>;
}

const nameChips = (names: string[]) => <FiveMore items={names.map((n) => <ToolChip key={n}>{n}</ToolChip>)} />;

// Cells are the old and the new value of one field of an edited rule, as
// the policy page shows that field. Names show the ones that leave and the
// ones that join.
function Cells({ aspect, was, now, registered }: { aspect: Aspect; was: RuleView; now: RuleView; registered: string[] | null }) {
  let old: React.ReactNode = null;
  let next: React.ReactNode = <Pill>{SEE_YAML}</Pill>;
  if (aspect === "outcome") [old, next] = [<RuleOutcome key="o" rule={was} />, <RuleOutcome key="n" rule={now} />];
  else if (aspect === "server") [old, next] = [<Servers key="o" rule={was} registered={registered} />, <Servers key="n" rule={now} registered={registered} />];
  else if (aspect === "lane") [old, next] = [<Pill key="o">{WHERE_WORD[was.lane]}</Pill>, <Pill key="n">{WHERE_WORD[now.lane]}</Pill>];
  else if (aspect === "names") {
    const m = namesMove(was, now, (r) => EVERY_WORD[r.lane]);
    [old, next] = [nameChips(m.leave), nameChips(m.join)];
  } else if (aspect === "events") [old, next] = [<span key="o" className="text-text-2">{eventsWords(was.events, was.lane)}</span>, <span key="n">{eventsWords(now.events, now.lane)}</span>];
  else if (aspect === "reason") [old, next] = [<span key="o" className="text-text-2">{was.reason}</span>, <span key="n">{now.reason}</span>];
  return (
    <>
      <td className={TD}><Cell>{old}</Cell></td>
      <td className={TD}><Cell>{next}</Cell></td>
    </>
  );
}

// RuleName is a policy rule's name, and under a rule that matches patterns
// outside the MCP lane a quiet button that shows them in place.
function RuleName({ rule }: { rule: RuleView }) {
  const [open, setOpen] = React.useState(false);
  const names = rule.names || [];
  return (
    <span className="flex flex-col items-start gap-1">
      <span className={NAME}>{rule.id}</span>
      {hasPatterns(rule) && (
        <Button variant="ghost" size="xs" className="text-link" aria-expanded={open} onClick={() => setOpen((o) => !o)} data-patterns={rule.id}>
          {showPattern(names.length, open)}
        </Button>
      )}
      {open && names.map((n) => <span key={n} className="font-mono text-caption break-all text-text-2" data-pattern>{n}</span>)}
    </span>
  );
}

const Cell = ({ children }: { children: React.ReactNode }) => <span className="flex max-w-full min-w-0 flex-wrap items-center gap-1.5">{children}</span>;

// ChangedRows are the lines of one new, edited or removed rule.
function ChangedRows({ m, registered }: { m: Marked; registered: string[] | null }) {
  const body = <Cell><OutcomeChip outcome={outcomeOf(m.rule)} /><CallsCell rule={m.rule} registered={registered} /></Cell>;
  if (m.mark === "new" || m.mark === "removed") {
    return (
      <tr className="border-b border-border last:border-b-0" data-rule={m.rule.id} data-rule-mark={m.mark}>
        <td className={TD}><RuleName rule={m.rule} /></td>
        <td className={TD}><ChangeChip change={m.mark} /></td>
        <td className={TD}>{m.mark === "removed" ? body : null}</td>
        <td className={TD}>{m.mark === "new" ? body : null}</td>
      </tr>
    );
  }
  const was = m.was as RuleView;
  const lines = ruleAspects(was, m.rule);
  return (
    <>
      {lines.map((l, i) => {
        return (
          <tr key={l.aspect} className={cn("border-border", i === lines.length - 1 && "border-b last:border-b-0")} data-rule={m.rule.id} data-rule-mark="edited" data-aspect={l.aspect}>
            {i === 0 && <td rowSpan={lines.length} className={TD}><RuleName rule={m.rule} /></td>}
            <td className={cn(TD, "text-dense text-text-2")}>{l.label}</td>
            <Cells aspect={l.aspect} was={was} now={m.rule} registered={registered} />
          </tr>
        );
      })}
    </>
  );
}

// Table is a rule table that scrolls inside its own box and keeps a floor
// width.
function Table({ cols, head, children }: { cols: string[]; head: string[]; children: React.ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-md border border-border">
      <table className="w-full table-fixed border-collapse text-dense" style={{ minWidth: 560 }}>
        <colgroup>{cols.map((w, i) => <col key={i} style={w ? { width: w } : undefined} />)}</colgroup>
        <thead className="border-b border-border bg-secondary/40"><tr>{head.map((h) => <th key={h} className={TH}>{h}</th>)}</tr></thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// Unchanged is the closed disclosure of the rules a changed set keeps.
function Unchanged({ rules }: { rules: RuleView[] }) {
  if (!rules.length) return null;
  return (
    <details className="group" data-unchanged-rules={rules.length}>
      <summary className="flex cursor-pointer list-none items-center gap-1 text-dense text-link [&::-webkit-details-marker]:hidden">
        <ChevronRightIcon className="size-4 transition-transform group-open:rotate-90" aria-hidden="true" />
        {rulesUnchanged(rules.length)}
      </summary>
      <div className="mt-2 flex flex-col gap-1.5 pl-5">
        {rules.map((r) => <span key={r.id} className="flex flex-wrap items-center gap-2" data-unchanged-rule={r.id}><span className="font-mono text-caption break-all text-foreground">{r.id}</span><OutcomeChip outcome={outcomeOf(r)} /></span>)}
      </div>
    </details>
  );
}

// SetBody is the body of a set's card. reading is null where the set's
// document does not read, which a set turned off or removed does not need.
export function SetBody({ what, reading: s, past, ctx }: { what: ChangeWhat; reading: SetReading | null; past: boolean; ctx: Ctx }) {
  if (what === "off" || what === "removed") {
    const value = what === "off" ? <><Pill icon={PowerIcon}>{OFF_PILL}</Pill><Pill>{STAYS_STORED}</Pill></> : <Pill>{REMOVED_PILL}</Pill>;
    return <Facts rows={[{ label: past ? CARD.afterPast : CARD.after, value }]} />;
  }
  if (!s) return <p className="m-0 text-dense text-text-2">{NOT_READABLE_DOC}</p>;
  const set = s.set;
  const applies = (
    <>
      {set.roles.map((r) => <ObjectTag key={r} kind={ctx.roleKind(r)} name={r} />)}
      {!set.roles.length && !set.matchOther && <Pill>{EVERYONE_PILL}</Pill>}
      {set.matchOther && <Pill title={ALSO_OUTSIDE_TITLE}>{ALSO_OUTSIDE}</Pill>}
    </>
  );
  const facts = <Facts rows={[{ label: CARD.applies, value: applies }, { label: CARD.recording, value: <Pill>{set.capture ? recWord(set.capture) : NO_RECORDING}</Pill> }]} />;
  if (what === "new") {
    return (
      <>
        {facts}
        {set.rules.length > 0 && (
          <Table cols={["32%", "", "150px"]} head={[SET_COLUMN.rule, SET_COLUMN.calls, SET_COLUMN.outcome]}>
            {set.rules.map((r) => (
              <tr key={r.id} className="border-b border-border last:border-b-0" data-rule={r.id}>
                <td className={TD}><span className={NAME}>{r.id}</span></td>
                <td className={TD}><CallsCell rule={r} registered={ctx.registered} /></td>
                <td className={TD}><OutcomeChip outcome={outcomeOf(r)} /></td>
              </tr>
            ))}
          </Table>
        )}
      </>
    );
  }
  const col = SET_CHANGE_COLUMN[past ? "published" : "open"];
  const changed = s.marks.filter((m) => m.mark !== "same");
  return (
    <>
      {facts}
      {changed.length > 0 && (
        <Table cols={["26%", "18%", "", ""]} head={[col.rule, col.what, col.now, col.after]}>
          {changed.map((m) => <ChangedRows key={m.rule.id + m.mark} m={m} registered={ctx.registered} />)}
        </Table>
      )}
      <Unchanged rules={s.marks.filter((m) => m.mark === "same").map((m) => m.rule)} />
    </>
  );
}
