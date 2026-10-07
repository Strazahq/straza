import * as React from "react";
import { XIcon } from "lucide-react";
import { HEAD } from "@/components/data-table";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { ApprovalChoice, Choice, SECONDS, spanOf } from "@/components/approval-choice";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { RadioGroup } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { CAPS, CardGroup, HINT, OptionCard } from "@/components/wizard/parts";
import { SPONSOR, type Shape } from "@/lib/access-plan";
import type { PreviewEntry, RoleRow, ValidateAnswer } from "@/lib/api";
import { type Plain, type Who, buildRule, ruleView, slug } from "@/lib/policy-model";
import {
  ADD_PICK, ALLOW_HELP, CALLS_Q, DENY_LEDE, DEPENDS_ON_ROLE, EVERYONE, EVERYONE_SERVERS, HOW_Q,
  INTENT, LANE_LABEL, LINE_NUMBERS, NAME_BAD, NAME_FREE, NAME_HINT, NAME_TAKEN,
  NET_LINE, NO_TOOLS, NOT_REACHED_TODAY, OR_PICK, PATHS_EXAMPLE, PATHS_Q, PATTERNS_EXAMPLE, PATTERNS_Q, PICK_ALL_TITLE, REASON, REASON_HINT,
  RECORDING_NOTE, REVIEW_FACT, REVIEW_NAME, ROLE_CAP, ROLE_SEARCH_FROM, RUNS_TODAY, SELECTED_ROLE, SERVER, SERVERS_NARROWED_HELP, STORED_AS, SUBJECT_ROLES,
  TOOL_COLUMN, VALID_NOW, VALID_REVIEW_HELP, WHAT_Q, WHO_EVERYONE, WHO_EVERYONE_LINE, WHO_Q, WHO_ROLE,
  WHO_ROLE_HELP, WHO_ROLE_LINE, WHOLE_SERVER, WHOLE_SERVER_LINE, WRITE_NEW_INSTEAD, WRITE_YAML, addToExisting,
  advisoryLine, alreadyToday, existingSet, holdWords, holdersToday, howLede, pickedCount, reachLine, reasonApprove, reasonDeny, removePick,
  roleCount, searchRoles, sentence, serversNarrowed, singular, subjectOpening, subjectWords, ticketWords, validWords, whoWords,
} from "@/lib/policy-words";
import { cn } from "@/lib/utils";
import { holders } from "@/lib/words";

// The step bodies of the New policy wizard. The screen holds the draft,
// runs the reads and owns the footer;
// every step here draws what the draft says and hands a change back.

// Intent is what the policy should do, the answer of the first step.
export type Intent = "approve" | "deny" | "allow";

// Lane is the governed surface the rule matches.
export type Lane = "mcp" | "shell" | "files" | "net";

export type Unit = "seconds" | "minutes" | "hours" | "days";

// Span is a window as the person reads it, a number and a unit, never raw
// seconds.
export type Span = { n: number; unit: Unit };

// secondsOf is the window in the seconds the rule stores.
export const secondsOf = (s: Span): number => Math.max(1, Math.round(s.n)) * SECONDS[s.unit];

// Draft is the policy the wizard is writing: every answer, kept while the
// person walks back and forth between the steps.
export type Draft = {
  intent: Intent;
  // role is the application role the policy names, null for Everyone and
  // undefined while the Who step is unanswered.
  role: string | null | undefined;
  lane: Lane;
  app: string;
  tools: string[];
  wholeServer: boolean;
  patterns: string[];
  paths: string[];
  who: Who;
  how: "hold" | "ticket";
  hold: Span;
  ticket: Span;
  grant: Span;
  reason: string;
  // reasonTyped stops the offered reason from following the answers once
  // the person has written their own.
  reasonTyped: boolean;
  name: string;
  nameTyped: boolean;
  // into names the stored set the rules are added to, "" for a new policy.
  into: string;
};

export const EMPTY_DRAFT: Draft = {
  intent: "approve",
  role: undefined,
  lane: "mcp",
  app: "",
  tools: [],
  wholeServer: false,
  patterns: [],
  paths: [],
  who: { sponsor: true, roles: [] },
  how: "hold",
  hold: { n: 2, unit: "minutes" },
  ticket: { n: 1, unit: "days" },
  grant: { n: 1, unit: "hours" },
  reason: "",
  reasonTyped: false,
  name: "",
  nameTyped: false,
  into: "",
};

// pickedNames are the tools, patterns or paths the rule matches; an empty
// list means every one on the lane.
export function pickedNames(d: Draft): string[] {
  if (d.lane === "mcp") return d.wholeServer ? [] : d.tools;
  if (d.lane === "shell") return d.patterns;
  if (d.lane === "files") return d.paths;
  return [];
}

// ruleIdOf names the rule after what it does, the way a hand-written rule
// is named.
export const ruleIdOf = (d: Draft): string => {
  const names = pickedNames(d);
  return slug(d.intent + "-" + (d.lane === "mcp" && d.app ? d.app : d.lane) + "-" + (names.length ? names.join("-") : "every"));
};

// ruleOf renders the answers so far as one plain rule.
export function ruleOf(d: Draft, id: string): Plain {
  const posture = d.intent === "deny" ? "deny" : d.intent === "allow" ? "allow" : d.how;
  return buildRule({
    id,
    lane: d.lane,
    posture,
    app: d.lane === "mcp" && d.app ? d.app : undefined,
    names: pickedNames(d),
    who: d.who,
    timeoutSeconds: secondsOf(d.hold),
    ticketTTLSeconds: secondsOf(d.ticket),
    grantTTLSeconds: secondsOf(d.grant),
    reason: d.reason,
  });
}

// viewOf reads the draft's rule the way the cards and the review read a
// stored one, so one sentence generator serves both.
export const viewOf = (d: Draft) => ruleView(ruleOf(d, ruleIdOf(d) || "rule"), 0);

// reasonDefault is the reason offered until the person writes their own.
export function reasonDefault(d: Draft): string {
  const view = viewOf(d);
  const subject = subjectWords(view);
  const one = singular(view);
  return d.intent === "deny" ? reasonDeny(subject, one) : reasonApprove(subject, one);
}

// RESERVED are the names the Policies address already uses, so a policy
// may not take them.
const RESERVED = ["new", "by-role"];

// nameProblem says why a name cannot be stored: taken by a policy or by
// the address, or outside the server's grammar.
export function nameProblem(name: string, taken: string[]): "" | "taken" | "bad" {
  if (!name || slug(name) !== name) return "bad";
  if (RESERVED.includes(name) || taken.includes(name)) return "taken";
  return "";
}

// Today is what one tool does for the picked role right now: the words and
// whether they are muted, the reading of the server's own preview.
export type Today = { text: string; muted: boolean };

export function todayWords(role: string | null, reached: boolean, entry: PreviewEntry | undefined): Today {
  if (role === null) return entry && entry.status === "visible" ? { text: RUNS_TODAY, muted: false } : { text: DEPENDS_ON_ROLE, muted: true };
  if (!reached) return { text: NOT_REACHED_TODAY, muted: true };
  if (entry && entry.status === "approve_gated") return { text: alreadyToday("needs approval", entry.setName || ""), muted: false };
  if (entry && entry.status === "hidden_policy") return { text: alreadyToday("denied", entry.setName || ""), muted: false };
  return { text: RUNS_TODAY, muted: false };
}

// Verdict is the server's reading of the document on the Review step.
export type Verdict = { ok: ValidateAnswer } | { bad: string };

const H2 = "text-base font-semibold text-foreground";
const CARDS = "max-w-[900px] grid-cols-[repeat(auto-fit,minmax(260px,1fr))]";

// Chips are the picked names, each one removable.
function Chips({ names, onRemove }: { names: string[]; onRemove: (name: string) => void }) {
  return (
    <>
      {names.map((n) => (
        <span key={n} data-pick={n} className="inline-flex items-center gap-1 rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] text-foreground">
          {n}
          <button type="button" aria-label={removePick(n)} className="text-muted-foreground hover:text-foreground" onClick={() => onRemove(n)}>
            <XIcon className="size-3" aria-hidden="true" />
          </button>
        </span>
      ))}
    </>
  );
}

// ChipInput takes one name at a time: Enter or Add turns what is typed
// into a chip.
function ChipInput({ label, hint, names, onChange }: { label: string; hint: string; names: string[]; onChange: (names: string[]) => void }) {
  const [text, setText] = React.useState("");
  const add = () => {
    const value = text.trim();
    if (!value || names.includes(value)) { setText(""); return; }
    onChange([...names, value]);
    setText("");
  };
  return (
    <div className="flex max-w-[760px] flex-col gap-2">
      <span className={CAPS}>{label}</span>
      <div className="flex items-center gap-2">
        <Input
          aria-label={label}
          value={text}
          placeholder={hint}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }}
          className="max-w-xs font-mono"
        />
        <Button variant="outline" size="sm" onClick={add}>{ADD_PICK}</Button>
      </div>
      <div className="flex flex-wrap items-center gap-1.5"><Chips names={names} onRemove={(n) => onChange(names.filter((x) => x !== n))} /></div>
      <span className={HINT}>{hint}</span>
    </div>
  );
}

// WhatStep asks what should happen, the answer that shapes every step
// after it.
export function WhatStep({ intent, onIntent, onWriteYAML }: { intent: Intent; onIntent: (i: Intent) => void; onWriteYAML: () => void }) {
  const cards: Intent[] = ["approve", "deny", "allow"];
  return (
    <div className="flex flex-col gap-4" data-step="what">
      <h2 className={H2}>{WHAT_Q}</h2>
      <CardGroup label={WHAT_Q} value={intent} onPick={(v) => onIntent(v as Intent)} className={CARDS}>
        {cards.map((key) => (
          <OptionCard
            key={key}
            value={key}
            label={INTENT[key].title}
            name={INTENT[key].title}
            tag=""
            on={intent === key}
            line={INTENT[key].line}
          >
            {key === "allow" && <HelpTip label={INTENT.allow.title} text={ALLOW_HELP} className="self-start" />}
          </OptionCard>
        ))}
      </CardGroup>
      <p className="flex max-w-[80ch] flex-wrap items-center gap-1.5 text-[13px] text-muted-foreground">
        {RECORDING_NOTE}
        <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={onWriteYAML}>{WRITE_YAML}</Button>
      </p>
    </div>
  );
}

type WhoProps = {
  draft: Draft;
  // roles are the application roles, the only kind a policy names.
  roles: RoleRow[];
  problem: string | null;
  serversOf: (role: string) => string[];
  onRole: (role: string | null | undefined) => void;
};

// WhoStep asks whose sessions the policy governs: everyone, or one
// application role.
export function WhoStep({ draft, roles, problem, serversOf, onRole }: WhoProps) {
  const [needle, setNeedle] = React.useState("");
  const q = needle.trim().toLowerCase();
  const matched = roles.filter((r) => !q || r.name.toLowerCase().includes(q) || (r.description || "").toLowerCase().includes(q));
  const shown = matched.slice(0, ROLE_CAP);
  const selectedHidden = draft.role && !shown.some((r) => r.name === draft.role);
  const which = draft.role === undefined ? "" : draft.role === null ? "everyone" : "role";

  return (
    <div className="flex flex-col gap-4" data-step="who">
      <h2 className={H2}>{WHO_Q}</h2>
      {problem && <FetchError subject={SUBJECT_ROLES} detail={problem} />}
      <RadioGroup value={which} onValueChange={(v) => onRole(v === "everyone" ? null : roles.length ? roles[0].name : "")} className="max-w-[900px] gap-2.5">
        <Choice id="who-everyone" value="everyone" label={WHO_EVERYONE} line={WHO_EVERYONE_LINE} />
        <Choice id="who-role" value="role" label={WHO_ROLE} line={<>{WHO_ROLE_LINE} <HelpTip label={WHO_ROLE} text={WHO_ROLE_HELP} /></>} />
      </RadioGroup>

      {which === "role" && (
        <div className="flex flex-col gap-2.5">
          {roles.length >= ROLE_SEARCH_FROM && (
            <div className="flex items-center gap-2">
              <Input
                value={needle}
                onChange={(e) => setNeedle(e.target.value)}
                aria-label={searchRoles(roles.length)}
                placeholder={searchRoles(roles.length)}
                className="h-9 max-w-xs"
              />
              <span className="text-[13px] text-muted-foreground" data-role-count>{roleCount(shown.length, roles.length)}</span>
            </div>
          )}
          <CardGroup label={WHO_ROLE} value={draft.role || ""} onPick={onRole} className={CARDS}>
            {shown.map((r) => (
              <OptionCard
                key={r.name}
                value={r.name}
                label={r.name}
                name={r.name}
                tag={holders(r.holder_count || 0)}
                on={draft.role === r.name}
                line={(r.description ? r.description + " " : "") + reachLine(serversOf(r.name))}
              />
            ))}
          </CardGroup>
          {selectedHidden && <p className="text-sm text-text-2" data-selected-role>{SELECTED_ROLE} <strong className="font-semibold text-foreground">{draft.role}</strong></p>}
        </div>
      )}
    </div>
  );
}

type CallsProps = {
  draft: Draft;
  // servers are the servers the rule may name: what the role reaches, or
  // every server for Everyone.
  servers: string[];
  totalServers: number;
  tools: string[];
  // preview is what the server says each tool does today, by tool name.
  preview: Record<string, PreviewEntry>;
  reaches: (tool: string) => boolean;
  onChange: (patch: Partial<Draft>) => void;
};

// CallsStep asks which calls the rule matches: the lane first, then the
// server and its tools, or the patterns and paths of a local lane.
export function CallsStep({ draft, servers, totalServers, tools, preview, reaches, onChange }: CallsProps) {
  const lanes: Lane[] = ["mcp", "shell", "files", "net"];
  const narrowed = draft.role != null && servers.length < totalServers;
  const picked = draft.tools;
  const allShown = tools.length > 0 && tools.every((t) => picked.includes(t));

  return (
    <div className="flex flex-col gap-4" data-step="calls">
      <h2 className={H2}>{CALLS_Q}</h2>
      <div role="group" aria-label={CALLS_Q} className="flex flex-wrap items-center gap-1.5">
        {lanes.map((l) => (
          <Button
            key={l}
            variant="outline"
            size="sm"
            aria-pressed={draft.lane === l}
            data-lane={l}
            className={cn("h-8", draft.lane === l && "border-link bg-accent-bg text-foreground")}
            onClick={() => onChange({ lane: l })}
          >
            {LANE_LABEL[l]}
          </Button>
        ))}
      </div>

      {draft.lane === "mcp" && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Select value={draft.app} onValueChange={(v) => onChange({ app: v, tools: [] })}>
              <SelectTrigger aria-label={SERVER} className="h-9 gap-1.5">
                <span className="text-muted-foreground">{SERVER}</span>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {servers.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
              </SelectContent>
            </Select>
            {draft.role === null && <span className="text-[13px] text-muted-foreground">{EVERYONE_SERVERS}</span>}
            {narrowed && (
              <span className="flex items-center gap-1.5 text-[13px] text-muted-foreground" data-narrowed>
                {serversNarrowed(draft.role as string, servers.length, totalServers)}
                <HelpTip label={SERVER} text={SERVERS_NARROWED_HELP} />
              </span>
            )}
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Switch id="whole-server" checked={draft.wholeServer} onCheckedChange={(v) => onChange({ wholeServer: v })} />
            <label htmlFor="whole-server" className="text-sm text-foreground">{WHOLE_SERVER}</label>
            {!draft.wholeServer && <>
              <span className="text-[13px] text-muted-foreground">{OR_PICK}</span>
              <Chips names={picked} onRemove={(t) => onChange({ tools: picked.filter((x) => x !== t) })} />
            </>}
            <span className="text-[13px] text-muted-foreground" data-picked-count>{draft.wholeServer ? WHOLE_SERVER_LINE : pickedCount(picked.length, tools.length)}</span>
          </div>

          <div className="max-w-[760px] overflow-x-auto rounded-md border border-border bg-card">
            <Table className="table-fixed">
              <colgroup><col style={{ width: "44px" }} /><col style={{ width: "220px" }} /><col /></colgroup>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="h-10 px-2">
                    <input
                      type="checkbox"
                      aria-label={PICK_ALL_TITLE}
                      title={PICK_ALL_TITLE}
                      checked={draft.wholeServer || allShown}
                      ref={(node) => { if (node) node.indeterminate = !draft.wholeServer && !allShown && tools.some((t) => picked.includes(t)); }}
                      disabled={draft.wholeServer || tools.length === 0}
                      onChange={() => onChange({ tools: allShown ? [] : tools.slice() })}
                      className="size-4 accent-primary"
                    />
                  </TableHead>
                  <TableHead className="h-10"><span className={HEAD}>{TOOL_COLUMN.tool}</span></TableHead>
                  <TableHead className="h-10"><span className={HEAD}>{TOOL_COLUMN.today + " " + (draft.role === null ? EVERYONE : draft.role || "")}</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {tools.length === 0 && (
                  <TableRow><TableCell colSpan={3} className="text-sm text-muted-foreground">{NO_TOOLS}</TableCell></TableRow>
                )}
                {tools.map((t) => {
                  const today = todayWords(draft.role === null ? null : draft.role || "", reaches(t), preview[t]);
                  return (
                    <TableRow key={t} data-tool={t} className="text-sm">
                      <TableCell className="px-2">
                        <input
                          type="checkbox"
                          aria-label={t}
                          checked={draft.wholeServer || picked.includes(t)}
                          disabled={draft.wholeServer}
                          onChange={() => onChange({ tools: picked.includes(t) ? picked.filter((x) => x !== t) : [...picked, t] })}
                          className="size-4 accent-primary"
                        />
                      </TableCell>
                      <TableCell className="truncate font-mono text-[13px] text-foreground">{t}</TableCell>
                      <TableCell className={cn("whitespace-normal text-[13px]", today.muted ? "text-muted-foreground" : "text-text-2")} data-today={t}>{today.text}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        </>
      )}

      {draft.lane === "shell" && <ChipInput label={PATTERNS_Q} hint={PATTERNS_EXAMPLE} names={draft.patterns} onChange={(patterns) => onChange({ patterns })} />}
      {draft.lane === "files" && <ChipInput label={PATHS_Q} hint={PATHS_EXAMPLE} names={draft.paths} onChange={(paths) => onChange({ paths })} />}
      {draft.lane === "net" && <p className="max-w-[80ch] text-sm text-text-2">{NET_LINE}</p>}
    </div>
  );
}

type HowProps = {
  draft: Draft;
  // approvers are the roles that may hold the decision: the approver kind,
  // and the console's own administrators.
  approvers: RoleRow[];
  onChange: (patch: Partial<Draft>) => void;
};

// HowStep asks who decides and for how long. A denial has no decider, so
// it asks for the reason alone.
export function HowStep({ draft, approvers, onChange }: HowProps) {
  const view = viewOf(draft);
  // The shared approval piece works in seconds over one pool; the draft
  // keeps spans and a Who, so the answers convert here and only what
  // changed is patched back.
  const shape: Shape = { pool: draft.who.roles[0] || SPONSOR, how: draft.how, hold: secondsOf(draft.hold), ticket: secondsOf(draft.ticket), grant: secondsOf(draft.grant) };
  const onShape = (next: Shape) => {
    const patch: Partial<Draft> = {};
    if (next.pool !== shape.pool) patch.who = next.pool === SPONSOR ? { sponsor: true, roles: [] } : { sponsor: false, roles: [next.pool] };
    if (next.how !== shape.how) patch.how = next.how;
    if (next.hold !== shape.hold) patch.hold = spanOf(next.hold);
    if (next.ticket !== shape.ticket) patch.ticket = spanOf(next.ticket);
    if (next.grant !== shape.grant) patch.grant = spanOf(next.grant);
    onChange(patch);
  };

  const field = (
    <div className="flex max-w-[760px] flex-col gap-1">
      <label htmlFor="policy-reason" className={CAPS}>{REASON}</label>
      <Input
        id="policy-reason"
        value={draft.reason}
        aria-label={REASON}
        onChange={(e) => onChange({ reason: e.target.value, reasonTyped: true })}
        className="font-mono text-[13px]"
      />
      <span className={HINT}>{REASON_HINT}</span>
    </div>
  );

  if (draft.intent === "deny") {
    return (
      <div className="flex flex-col gap-4" data-step="how">
        <h2 className={H2}>{HOW_Q}</h2>
        <p className="max-w-[80ch] text-sm text-text-2">{DENY_LEDE(subjectOpening(view))}</p>
        {field}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-5" data-step="how">
      <div className="flex flex-col gap-1">
        <h2 className={H2}>{HOW_Q}</h2>
        <p className="max-w-[80ch] text-sm text-text-2">{howLede(subjectOpening(view))}</p>
      </div>

      <div className="max-w-[900px]">
        <ApprovalChoice shape={shape} approvers={approvers} onShape={onShape} />
      </div>

      {field}
    </div>
  );
}

type ReviewProps = {
  draft: Draft;
  // taken are the stored policy names, the check the name field runs as
  // you type.
  taken: string[];
  // exists is the live set that already matches exactly this role, offered
  // as the home for the rules.
  exists: string | null;
  // people are up to two holders of the role today; null when the read
  // failed, and the fact then names the role alone.
  people: string[] | null;
  yaml: string;
  verdict: Verdict | null;
  nameBox: React.RefObject<HTMLInputElement | null>;
  onName: (name: string) => void;
  onInto: (set: string) => void;
};

// ReviewStep asks the name once, offers an existing set as the home for
// the rules, and shows the document beside what it will do.
export function ReviewStep({ draft, taken, exists, people, yaml, verdict, nameBox, onName, onInto }: ReviewProps) {
  const view = viewOf(draft);
  const problem = nameProblem(draft.name, taken);
  const lines = yaml.split("\n");

  const facts: [string, React.ReactNode][] = [
    [
      REVIEW_FACT.applies,
      <span key="applies" className="flex flex-wrap items-center gap-1.5">
        <span className="rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] text-foreground">{draft.role === null ? EVERYONE : draft.role}</span>
        {draft.role != null && people && <span className="text-[13px] text-muted-foreground">{holdersToday(people)}</span>}
      </span>,
    ],
    [draft.intent === "approve" ? REVIEW_FACT.waits : draft.intent === "deny" ? REVIEW_FACT.refused : REVIEW_FACT.allowed, subjectWords(view)],
  ];
  if (draft.intent === "approve") {
    facts.push([REVIEW_FACT.who, whoWords(draft.who)]);
    facts.push([REVIEW_FACT.how, draft.how === "hold" ? holdWords(secondsOf(draft.hold)) : ticketWords(secondsOf(draft.ticket), secondsOf(draft.grant))]);
  }
  facts.push([REVIEW_FACT.reason, <span key="reason" className="font-mono text-[13px]">{draft.reason}</span>]);

  return (
    <div className="flex flex-col gap-4" data-step="review">
      <div className="flex max-w-[560px] flex-col gap-1">
        <label htmlFor="policy-name" className={CAPS}>{REVIEW_NAME}</label>
        <Input
          id="policy-name"
          ref={nameBox}
          value={draft.name}
          aria-label={REVIEW_NAME}
          onChange={(e) => onName(e.target.value)}
          className="font-mono"
        />
        <span className={cn("text-[13px]", problem ? "text-danger" : "text-ok")} data-name-state={problem || "free"}>
          {problem === "taken" ? NAME_TAKEN : problem === "bad" ? NAME_BAD : NAME_FREE}
        </span>
        <span className={HINT}>{NAME_HINT}</span>
      </div>

      {exists && draft.role && (
        <div className="flex max-w-[900px] flex-wrap items-center gap-3 rounded-md border border-link/40 bg-accent-bg px-3 py-2 text-sm text-text-2" data-existing={exists}>
          <span>{existingSet(exists, draft.role)}</span>
          <Button variant="outline" size="sm" className="ml-auto" onClick={() => onInto(draft.into ? "" : exists)}>
            {draft.into ? WRITE_NEW_INSTEAD : addToExisting(exists)}
          </Button>
        </div>
      )}

      <div className="grid gap-5 lg:grid-cols-[2fr_3fr]">
        <div className="flex flex-col gap-3">
          <h2 className={H2}>{sentence(view)}</h2>
          <dl className="grid grid-cols-[130px_1fr] gap-x-3 gap-y-1.5 text-sm" data-review-facts>
            {facts.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="min-w-0 break-words text-foreground">{v}</dd>
              </div>
            ))}
          </dl>
        </div>

        <div className="flex min-w-0 flex-col gap-2">
          <span className={CAPS}>{STORED_AS}</span>
          <div className="grid max-h-[420px] grid-cols-[44px_1fr] overflow-auto rounded-md border border-border bg-background font-mono text-[13px] leading-[1.5]">
            <pre className="m-0 select-none whitespace-pre border-r border-border py-2.5 pr-2 text-right text-muted-foreground" aria-label={LINE_NUMBERS}>
              {lines.map((_, i) => <span key={i} className="block">{i + 1}</span>)}
            </pre>
            <pre className="m-0 min-w-0 overflow-x-auto px-3 py-2.5 text-text-2" data-policy-yaml>{yaml}</pre>
          </div>

          {verdict && "ok" in verdict && (
            <div className="flex flex-col gap-1.5">
              <div role="status" className="flex items-center gap-1.5 rounded-md border border-ok/40 bg-ok-bg px-3 py-2 text-sm text-text-2" data-verdict="ok">
                {validWords(verdict.ok, VALID_NOW)}
                <HelpTip label={STORED_AS} text={VALID_REVIEW_HELP} />
              </div>
              {(verdict.ok.advisories || []).map((a, i) => (
                <div key={i} className="rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-relaxed text-text-2" data-advisory={a.code}>
                  {advisoryLine(a.rule, a.text)}
                </div>
              ))}
            </div>
          )}
          {verdict && "bad" in verdict && (
            <div role="alert" className="rounded-md border border-danger/40 bg-danger-bg px-3 py-2 text-sm leading-relaxed text-text-2" data-verdict="bad">{verdict.bad}</div>
          )}
        </div>
      </div>
    </div>
  );
}
