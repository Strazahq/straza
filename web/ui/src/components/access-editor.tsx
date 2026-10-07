import * as React from "react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { ApprovalChoice } from "@/components/approval-choice";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { CardGroup, Code, Fold, OptionCard } from "@/components/wizard/parts";
import {
  type Choice, type FixedRule, type OnCall, type Plan, type Reach, allowOffered, choiceOf, denyOffered, inGrant, narrowed, newToolsRun, planProblems,
  planSentence, policyWord, rulesFor, rulesText, shapeOf, withCall, withChoice, withOwn, withReach,
} from "@/lib/access-plan";
import type { Unread } from "@/lib/access-read";
import type { AppRow, BindingRow, RoleRow, ToolRow } from "@/lib/api";
import type { Plain } from "@/lib/policy-model";
import {
  APPROVAL_FOR_HELP, CALL_CHOICE, CHOICE_LABEL, DENY_NEEDS_LATER, EDITOR_HEAD, EVERY_NOTE, FILTER_TOOLS, LATER_LOCKED, NAMES_ONLY, NAME_FIRST, NOT_READABLE, NOT_REACHED,
  NO_ALLOW_UNDER_EVERY, NO_TOOL_DESCRIPTION, NO_TOOL_MATCHES, ON_A_CALL, ON_A_CALL_HELP, OTHERS_UNREAD, PATTERN_NOTE, REACH_CHOICE, SET_FOR_TOOL, THE_NEW_ROLE,
  USE_SERVER_SETTING, WHICH_TOOLS, WHICH_TOOLS_HELP, approvalFor, choiceGroup, fixedHelp, fixedWords, forToolOnly, hasPattern, isGlob, newToolsRunWords,
  policyReadOnly, policyUnread, rulesFoldIn, shapeLine, shownWords, toolTick,
} from "@/lib/role-words";
import { cn } from "@/lib/utils";
import { runWord } from "@/lib/words";

// The tool table of one server for one role: which
// tools the access row reaches, what happens on a call, and who approves a
// call that needs it. The caller holds the plan and every change goes
// through the model's transitions, so the New role wizard, the role page
// and the server page draw the same editor over their own draft.

export type AccessEditorProps = {
  role: string;
  app: AppRow;
  tools: ToolRow[];
  plan: Plan;
  onPlan: (update: (p: Plan) => Plan) => void;
  // fixed are the rules the editor shows and does not change: a rule in
  // another set, or one in the role's own set it cannot say.
  fixed: Record<string, FixedRule>;
  // approvers are the approver roles, with their holder counts.
  approvers: RoleRow[];
  // ownSet is the role's own set, named in the fold title.
  ownSet: string;
  // taken are the rule ids the own set keeps for other rules.
  taken?: string[];
  stored?: BindingRow | null;
  // readOnly draws the Policy column as words, for a server admin who may
  // change the tools and not policy.
  readOnly?: boolean;
  // namesOnly is set for a server admin, from whom the server refuses the
  // glob on an access row, so the third Which tools card is off.
  namesOnly?: boolean;
  // unread says which reads failed. A read-only column then says a call is
  // not readable here, and an editor without the preview says its rows
  // show only the role's own rules.
  unread?: Unread;
  // written are this server's rules as a save would store them in the own
  // set, for a door that read the set's text. The fold shows them, and
  // without them the rules the plan writes into a new set.
  written?: Plain[] | null;
  // unnamed is set while a new role has no name: the sentence and the fold
  // wait for it and say so.
  unnamed?: boolean;
};

const REACHES: Reach[] = ["tick", "today", "later"];
const CALLS: OnCall[] = ["allow", "every", "per"];
const CHOICES: Choice[] = ["allow", "approve", "deny"];
const CARDS = "grid-cols-[repeat(auto-fit,minmax(180px,1fr))]";
const SEG = "inline-flex h-8 items-center border-r border-border px-3 text-sm text-text-2 outline-none last:border-r-0 hover:bg-accent focus-visible:z-10 focus-visible:ring-2 focus-visible:ring-ring/50 aria-disabled:cursor-not-allowed aria-disabled:opacity-40 aria-disabled:hover:bg-transparent";
const SEG_ON: Record<Choice, string> = { allow: "bg-ok-bg font-semibold text-ok", approve: "bg-warn-bg font-semibold text-warn", deny: "bg-danger-bg font-semibold text-danger" };
const WORD_HUE: Record<Choice, string> = { allow: "text-text-2", approve: "text-warn", deny: "text-danger" };
const FIXED_HUE: Record<FixedRule["status"], string> = { visible: "text-text-2", approve_gated: "text-warn", hidden_policy: "text-danger" };
const LINK = "h-auto p-0 text-[13px] text-link";
// CELL tops every cell with one line as tall as the choice control, so a
// row's name lines up with its choice when an approval opens under it. A
// cell that sets a font size names CELL after it, because cn drops a line
// height that comes before a font size.
const CELL = "align-top leading-8";

// Question is the head of one of the editor's questions, with its help.
function Question({ label, help }: { label: string; help: string }) {
  return (
    <span className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
      {label}
      <HelpTip label={label} text={help} />
    </span>
  );
}

// offWhy says why a choice is not offered on this plan, empty when it is.
const offWhy = (plan: Plan, c: Choice): string => (c === "allow" && !allowOffered(plan) ? NO_ALLOW_UNDER_EVERY : c === "deny" && !denyOffered(plan) ? DENY_NEEDS_LATER : "");

export function AccessEditor({ role, app, tools, plan, onPlan, fixed, approvers, ownSet, taken = [], stored, readOnly, namesOnly, unread, written, unnamed }: AccessEditorProps) {
  const [filter, setFilter] = React.useState("");
  // opened is the tool whose own approval was just opened, scrolled into
  // view once it draws, so its name and its How show together.
  const opened = React.useRef("");
  // locked is a row a global admin stored as the glob, opened by a server
  // admin: the third card stays on and greyed, its question shows even to a
  // reader of policy, and a tick taken off narrows the row to the names
  // left ticked. Nothing changed sends nothing.
  const locked = !!namesOnly && !!stored && isGlob(stored.tools);
  const names = tools.map((t) => t.name);
  const needle = filter.trim().toLowerCase();
  const shown = tools.filter((t) => !needle || t.name.toLowerCase().includes(needle) || (t.description || "").toLowerCase().includes(needle));
  const ticking = plan.reach === "tick";
  const tickable = ticking || (locked && plan.reach === "later");
  const needs = plan.call === "every" || names.some((t) => choiceOf(plan, t) === "approve");
  const problems = planProblems(plan, names);
  const rules = written || rulesFor(plan, app.name, names, role, taken);
  const dark = !!readOnly && !!unread && (unread.own || unread.others);
  const who = unnamed ? THE_NEW_ROLE : role;

  const toggle = (name: string) => onPlan((p) => (p.reach === "tick" ? { ...p, picked: { ...p.picked, [name]: !p.picked[name] } } : narrowed(p, names, name)));
  // reveal scrolls a tool's own approval into view the first time it draws
  // after Set for this tool, by the least the sheet or the page that holds
  // the editor can move.
  const reveal = (el: HTMLElement | null, t: string) => {
    if (!el || opened.current !== t) return;
    opened.current = "";
    el.scrollIntoView?.({ block: "nearest" });
  };
  // A choice the plan does not offer stays focusable so its reason can be
  // read, and picking it changes nothing.
  const pick = (name: string, c: Choice) => onPlan((p) => (offWhy(p, c) ? p : withChoice(p, name, c)));

  const policyCell = (t: string) => {
    const f = fixed[t];
    if (f) {
      // runWord names the bare word when the rule gave none; fixedWords puts the set in brackets once.
      const set = f.set || "a policy";
      return (
        <span className={cn("inline-flex items-center gap-1.5 text-[13px]", FIXED_HUE[f.status])} data-fixed={t}>
          {fixedWords(f.word || runWord({ status: f.status }), set)}
          <HelpTip label={t} text={fixedHelp(set)} />
        </span>
      );
    }
    const c = choiceOf(plan, t);
    if (c === null) return <span className="text-[13px] text-muted-foreground">{NOT_REACHED}</span>;
    if (dark) return <span className="text-[13px] text-muted-foreground" data-policy-word={t}>{NOT_READABLE}</span>;
    if (readOnly) return <span className={cn("text-[13px]", WORD_HUE[c])} data-policy-word={t}>{policyWord(plan, t)}</span>;
    const own = plan.own[t];
    return (
      <>
        <RadioGroupPrimitive.Root
          aria-label={choiceGroup(t)}
          value={c}
          onValueChange={(v) => pick(t, v as Choice)}
          loop
          className="inline-flex w-fit overflow-hidden rounded-md border border-border"
        >
          {CHOICES.map((k) => {
            const why = offWhy(plan, k);
            return (
              <RadioGroupPrimitive.Item key={k} value={k} aria-disabled={why ? true : undefined} title={why || undefined} className={cn(SEG, c === k && SEG_ON[k])}>
                {CHOICE_LABEL[k]}
              </RadioGroupPrimitive.Item>
            );
          })}
        </RadioGroupPrimitive.Root>
        {c === "approve" && !own && (
          <div className="mt-1 flex flex-wrap items-center gap-x-1.5 text-[13px] leading-snug text-muted-foreground" data-shape-line={t}>
            {shapeLine(shapeOf(plan, t))}
            <Button variant="link" size="sm" className={LINK} onClick={() => { opened.current = t; onPlan((p) => withOwn(p, t, { ...p.shape })); }}>{SET_FOR_TOOL}</Button>
          </div>
        )}
        {c === "approve" && own && (
          <div ref={(el) => reveal(el, t)} className="mt-2 flex flex-col gap-2.5 border-l-2 border-warn py-1 pl-2.5 leading-snug" data-own={t}>
            <span className="flex flex-wrap items-center gap-x-2 text-[13px]">
              <span className="font-semibold text-foreground">{forToolOnly(t)}</span>
              <Button variant="link" size="sm" className={LINK} onClick={() => onPlan((p) => withOwn(p, t, null))}>{USE_SERVER_SETTING}</Button>
            </span>
            <ApprovalChoice compact shape={own} approvers={approvers} onShape={(s) => onPlan((p) => withOwn(p, t, s))} />
          </div>
        )}
      </>
    );
  };

  return (
    <div className="flex flex-col gap-4" data-access-editor={app.name}>
      {(!readOnly || locked) && (
        <div className="flex flex-col gap-2" data-reach={plan.reach}>
          <Question label={WHICH_TOOLS} help={WHICH_TOOLS_HELP} />
          <CardGroup label={WHICH_TOOLS} value={plan.reach} onPick={(v) => (namesOnly && v === "later" ? undefined : onPlan((p) => withReach(p, v as Reach)))} className={CARDS}>
            {REACHES.map((r) => (
              <OptionCard
                key={r}
                value={r}
                label={REACH_CHOICE[r].label}
                name={REACH_CHOICE[r].label}
                tag=""
                on={plan.reach === r}
                off={namesOnly && r === "later"}
                line={r === "today" ? REACH_CHOICE.today.line(tools.length) : namesOnly && r === "later" ? (locked ? LATER_LOCKED : NAMES_ONLY) : REACH_CHOICE[r].line}
              />
            ))}
          </CardGroup>
        </div>
      )}

      {!readOnly && (
        <>
          <div className="flex flex-col gap-2" data-call={plan.call}>
            <Question label={ON_A_CALL} help={ON_A_CALL_HELP} />
            <CardGroup label={ON_A_CALL} value={plan.call} onPick={(v) => onPlan((p) => withCall(p, v as OnCall))} className={CARDS}>
              {CALLS.map((k) => <OptionCard key={k} value={k} label={CALL_CHOICE[k].label} name={CALL_CHOICE[k].label} tag="" on={plan.call === k} line={CALL_CHOICE[k].line} />)}
            </CardGroup>
            {newToolsRun(plan) && <p className="m-0 max-w-[80ch] text-[13px] leading-snug text-warn" data-new-tools>{newToolsRunWords(app.name)}</p>}
          </div>

          {needs && (
            <div className="flex flex-col gap-2" data-approval={app.name}>
              <Question label={approvalFor(app.name)} help={APPROVAL_FOR_HELP} />
              <div className="rounded-md border border-border bg-background px-3.5 py-3">
                <ApprovalChoice shape={plan.shape} approvers={approvers} onShape={(shape) => onPlan((p) => ({ ...p, shape }))} />
              </div>
              {problems.map((m) => <p key={m} className="m-0 max-w-[80ch] text-[13px] leading-snug text-danger" data-problem>{m}</p>)}
              {plan.call === "every" && <p className="m-0 max-w-[90ch] text-[13px] leading-snug text-muted-foreground" data-every-note>{EVERY_NOTE}</p>}
            </div>
          )}
        </>
      )}

      {readOnly && (
        <p className="m-0 max-w-[90ch] border-l-[3px] border-warn bg-card px-3.5 py-2.5 text-sm text-text-2" data-read-only>
          {dark && unread ? policyUnread(who, ownSet, unread.own, unread.others) : policyReadOnly(who)}
        </p>
      )}

      {/* The sentence that says what the choices add up to leads the table, in a box as wide as the table with its text held to a reading measure. */}
      {!readOnly && (
        <div className="rounded-md border border-border bg-card px-4 py-3">
          <p className={cn("m-0 max-w-[90ch] text-base leading-relaxed", unnamed ? "text-muted-foreground" : "text-foreground")} data-grant-sentence>
            {unnamed ? NAME_FIRST : planSentence(plan, app.name, names, role)}
          </p>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Input aria-label={FILTER_TOOLS} placeholder={FILTER_TOOLS} value={filter} onChange={(e) => setFilter(e.target.value)} className="h-8 w-[220px]" />
        <span className="text-[13px] text-muted-foreground" data-shown>{shownWords(shown.length, tools.length)}</span>
      </div>

      {!readOnly && unread && unread.others && <p role="status" className="m-0 max-w-[80ch] text-[13px] leading-snug text-warn" data-others-unread>{OTHERS_UNREAD}</p>}

      {stored && hasPattern(stored.tools) && (
        <p role="status" className="m-0 max-w-[75ch] text-[13px] leading-snug text-warn" data-pattern-note>{PATTERN_NOTE(stored.tools || [])}</p>
      )}

      {/* The table is as tall as its rows, and the sheet or the page that holds the editor scrolls. On a phone the table scrolls sideways rather than squeezing the choice. */}
      <div className="overflow-hidden rounded-md border border-border" data-tool-table>
        <Table className="min-w-[640px] table-fixed">
          <colgroup>
            <col className="w-[34px]" />
            <col className="w-[22%]" />
            <col />
            <col className="w-[44%]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead className="px-2" />
              <TableHead>{EDITOR_HEAD.tool}</TableHead>
              <TableHead>{EDITOR_HEAD.what}</TableHead>
              <TableHead>
                <span className="inline-flex items-center gap-1.5">{EDITOR_HEAD.policy}<HelpTip label={EDITOR_HEAD.policy} text={ON_A_CALL_HELP} /></span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="text-muted-foreground">{NO_TOOL_MATCHES}</TableCell>
              </TableRow>
            )}
            {shown.map((t) => {
              const on = inGrant(plan, t.name);
              return (
                <TableRow key={t.name} data-tool={t.name} className={cn(on ? "bg-accent-bg" : "opacity-70")} onClick={() => (tickable ? toggle(t.name) : undefined)}>
                  <TableCell className={cn(CELL, "px-2")}>
                    <input
                      type="checkbox"
                      aria-label={toolTick(t.name)}
                      checked={on}
                      disabled={!tickable}
                      onClick={(e) => e.stopPropagation()}
                      onChange={() => toggle(t.name)}
                      className="size-3.5 align-middle accent-[var(--link)]"
                    />
                  </TableCell>
                  <TableCell className={cn("truncate font-mono text-[13px] text-foreground", CELL)} title={t.name}>{t.name}</TableCell>
                  <TableCell className={cn(CELL, "truncate text-text-2")} title={t.description || NO_TOOL_DESCRIPTION}>{t.description || NO_TOOL_DESCRIPTION}</TableCell>
                  <TableCell className={cn(CELL, "whitespace-normal")} onClick={(e) => e.stopPropagation()}>{policyCell(t.name)}</TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </div>

      {!readOnly && !unnamed && rules.length > 0 && (
        <Fold title={rulesFoldIn(rules.length, ownSet)}>
          <Code className="rounded-none border-0">{rulesText(rules)}</Code>
        </Fold>
      )}
    </div>
  );
}
