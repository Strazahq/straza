import * as React from "react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { HelpTip } from "@/components/help-tip";
import { PolicyMatching } from "@/components/policy-matching";
import { CAPS, HINT } from "@/components/wizard/parts";
import type { EventSupport, RoleRow } from "@/lib/api";
import { type Bucket, type Doc, HOLD_DEFAULT, type RuleView, setApprove, setPosture, setReason } from "@/lib/policy-model";
import {
  BUCKET_WORD, CANCEL, HOW, HOW_HELP, HOW_HOLD, HOW_HOLD_LINE, HOW_TICKET, HOW_TICKET_END, HOW_TICKET_LINE, HOW_TICKET_MID,
  NO_APPROVER_ROLE, REASON, REASON_HINT, REASON_STALE, REASON_UNUSED, REMOVE, REMOVE_RULE, REMOVE_RULE_BODY, UNITS, UNSET_SPAN, W1,
  WHAT_HAPPENS, WHO_BOTH, WHO_DECIDES, WHO_HELP, WHO_SPONSOR, WHO_TEAM, WHO_TEAM_LINE, allowRemoves, appliesToAll,
  consequenceWords, removeRuleTitle, unitWord,
} from "@/lib/policy-words";
import { kindOf } from "@/lib/role-words";
import { cn } from "@/lib/utils";

// RuleEditor is the open rule card of the Rules tab: what happens, who
// decides and how, the reason the agent
// reads, and the matching fold. Every control writes one model function
// into the page's document and reads the fresh rule back, so the card holds
// no copy of the rule beyond a number or a sentence being typed.

export type RuleEditorProps = {
  doc: Doc;
  rule: RuleView;
  // events is the harness matrix, read once by the page; null while the
  // read is in flight or after it failed.
  events: EventSupport | null;
  // roles are every role the deployment holds, for the approver team
  // picker.
  roles: RoleRow[];
  // onEdit applies one change to the document and names what it touched
  // for the unpublished bar.
  onEdit: (touched: string, fn: (doc: Doc) => void) => void;
  // onRemove drops the rule from the page; absent on a rule that is not
  // on the page yet, so the sheet's Cancel is its only way out.
  onRemove?: (id: string) => void;
  // onSplit moves one call the rule names into a rule of its own. The
  // matching fold offers the link only where the page passes it.
  onSplit?: (name: string) => void;
};

type Unit = keyof typeof UNITS;
const SECONDS: Record<Unit, number> = { seconds: 1, minutes: 60, hours: 3600, days: 86400 };
const UNIT_ORDER: Unit[] = ["days", "hours", "minutes", "seconds"];

// spanOf reads a window in the largest unit that divides it evenly, so 120
// seconds is 2 minutes and 86400 is 1 day.
function spanOf(seconds: number): { n: number; unit: Unit } {
  for (const unit of UNIT_ORDER) if (seconds > 0 && seconds % SECONDS[unit] === 0) return { n: seconds / SECONDS[unit], unit };
  return { n: seconds, unit: "seconds" };
}

const BUCKETS: Bucket[] = ["deny", "hum", "allow"];
const SEG = "inline-flex h-8 items-center border-r border-border px-3.5 text-[13px] text-text-2 last:border-r-0 hover:bg-accent focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50";
const SEG_ON: Record<Bucket, string> = { deny: "bg-accent-bg font-semibold text-danger", hum: "bg-accent-bg font-semibold text-foreground", allow: "bg-accent-bg font-semibold text-ok" };
// W1 is the warning line under How: a team paged for a window shorter than
// this expires to a denial more often than it decides.
const SHORT_HOLD = 120;
// REFUSAL_WORDS are the words a denial's reason carries that an approval no
// longer means, the signal that the sentence is stale after the switch.
const REFUSAL_WORDS = /blocked|denied|refused/i;

// Head is a caps label with its help icon, the shape every question of the
// card opens with.
function Head({ label, help }: { label: string; help?: string }) {
  return (
    <span className={cn(CAPS, "flex items-center gap-1.5")}>
      {label}
      {help && <HelpTip label={label} text={help} />}
    </span>
  );
}

// Choice is one radio: the dot, the label, what the choice needs answered
// beside it, and the line under it that says what it means.
function Choice({ id, value, label, line, children }: { id: string; value: string; label: string; line?: string; children?: React.ReactNode }) {
  return (
    <div className="flex items-start gap-2.5">
      <RadioGroupItem value={value} id={id} aria-label={label} className="mt-1" />
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2 text-sm text-foreground">
          <label htmlFor={id}>{label}</label>
          {children}
        </div>
        {line && <span className={HINT}>{line}</span>}
      </div>
    </div>
  );
}

// Duration is a window as a number and a unit. The box keeps what was
// typed so it can be emptied and retyped; a window changed anywhere else
// in the document reseeds it. A window the rule does not use reads empty
// and takes nothing, because the seconds behind it are a default the text
// never stored.
function Duration({ label, seconds, on, onChange }: { label: string; seconds: number; on: boolean; onChange: (seconds: number) => void }) {
  const [typed, setTyped] = React.useState<{ text: string; unit: Unit; seconds: number } | null>(null);
  const read = spanOf(seconds);
  const span = typed && typed.seconds === seconds ? { text: typed.text, unit: typed.unit } : { text: String(read.n), unit: read.unit };
  const write = (text: string, unit: Unit) => {
    const n = Number.parseInt(text, 10);
    const next = Number.isFinite(n) && n > 0 ? n * SECONDS[unit] : seconds;
    setTyped({ text, unit, seconds: next });
    if (next !== seconds) onChange(next);
  };
  return (
    <span className="inline-flex items-center gap-1.5">
      <Input
        type="number"
        min={1}
        aria-label={label}
        value={on ? span.text : ""}
        placeholder={on ? undefined : UNSET_SPAN}
        disabled={!on}
        onChange={(e) => write(e.target.value, span.unit)}
        className="h-8 w-16"
      />
      <Select value={on ? span.unit : ""} disabled={!on} onValueChange={(u) => write(span.text, u as Unit)}>
        <SelectTrigger aria-label={label + " unit"} className="h-8 w-28"><SelectValue placeholder={UNSET_SPAN} /></SelectTrigger>
        <SelectContent>
          {UNIT_ORDER.map((u) => <SelectItem key={u} value={u}>{unitWord(Number.parseInt(span.text, 10) || 0, u)}</SelectItem>)}
        </SelectContent>
      </Select>
    </span>
  );
}

// Reason is the sentence the agent and the approver both read. It lands in
// the document on blur and after a pause, so a keystroke never rewrites
// the whole text. A rule that allows keeps its sentence on screen and out
// of reach, because nothing reads it while the call runs at once.
function Reason({ rule, unused, stale, onEdit }: { rule: RuleView; unused: boolean; stale: boolean; onEdit: RuleEditorProps["onEdit"] }) {
  const [text, setText] = React.useState(rule.reason);
  const sent = React.useRef(rule.reason);
  const timer = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  React.useEffect(() => {
    if (rule.reason === sent.current) return;
    sent.current = rule.reason;
    setText(rule.reason);
  }, [rule.reason]);
  React.useEffect(() => () => { if (timer.current) clearTimeout(timer.current); }, []);
  const write = (value: string) => {
    if (timer.current) { clearTimeout(timer.current); timer.current = null; }
    if (value === sent.current) return;
    sent.current = value;
    onEdit(rule.id, (doc) => setReason(doc, rule.id, value));
  };
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={"reason-" + rule.id} className={CAPS}>{REASON}</label>
      <Input
        id={"reason-" + rule.id}
        value={text}
        spellCheck={false}
        disabled={unused}
        className="h-8 max-w-[640px] font-mono"
        onChange={(e) => {
          setText(e.target.value);
          if (timer.current) clearTimeout(timer.current);
          const value = e.target.value;
          timer.current = setTimeout(() => write(value), 300);
        }}
        onBlur={(e) => write(e.target.value)}
      />
      {unused
        ? <span className={HINT} data-reason-unused>{REASON_UNUSED}</span>
        : <span className={HINT}>{REASON_HINT}</span>}
      {stale && <span className="text-[13px] leading-relaxed text-warn" data-reason-stale>{REASON_STALE}</span>}
    </div>
  );
}

export function RuleEditor({ rule, events, roles, onEdit, onRemove, onSplit }: RuleEditorProps) {
  const [ask, setAsk] = React.useState(false);
  // The two warnings a switch of what happens raises: the reason that
  // still describes a refusal, and the denial the switch to Allowed takes
  // away. Both are held from the switch that raised them, so a rule opened
  // as it stands says neither.
  const [staleReason, setStaleReason] = React.useState<string | null>(null);
  const [droppedDenial, setDroppedDenial] = React.useState(false);
  const edit = (fn: (doc: Doc) => void) => onEdit(rule.id, fn);

  // The pool the rule already names decides which radio is marked. All
  // three are offered, so the sponsor and a team can be named together
  // without writing the text by hand.
  const pool = rule.who.roles.length ? (rule.who.sponsor ? "both" : "team") : "sponsor";
  const approvers = roles.filter((r) => kindOf(r) === "approver" || r.name === "straza-admin");
  const teams = [...new Set([...approvers.map((r) => r.name), ...rule.who.roles])];
  const team = rule.who.roles[0] || teams[0] || "";
  const holders = roles.find((r) => r.name === team)?.holder_count || 0;
  const short = rule.posture === "hold" && rule.who.roles.length > 0 && rule.timeoutSeconds < SHORT_HOLD;
  const names = rule.names ? rule.names.length : 0;

  const pick = (bucket: Bucket) => {
    if (bucket === rule.bucket) return;
    const wasDenial = rule.bucket === "deny";
    setStaleReason(wasDenial && bucket === "hum" && REFUSAL_WORDS.test(rule.reason) ? rule.reason : null);
    setDroppedDenial(wasDenial && bucket === "allow");
    if (bucket === "hum") { edit((doc) => setPosture(doc, rule.id, "hold", { who: { sponsor: true, roles: [] }, timeoutSeconds: HOLD_DEFAULT })); return; }
    edit((doc) => setPosture(doc, rule.id, bucket === "deny" ? "deny" : "allow"));
  };

  // A deployment with no approver role has no team to name, so the row
  // says so and the pool stays where it is rather than writing an empty
  // role into the text.
  const setPool = (next: string) => {
    if (next === "sponsor") { edit((doc) => setApprove(doc, rule.id, { who: { sponsor: true, roles: [] } })); return; }
    if (!team) return;
    edit((doc) => setApprove(doc, rule.id, { who: { sponsor: next === "both", roles: [team] } }));
  };

  const setHow = (next: string) => {
    if (next === rule.posture) return;
    if (next === "hold") { edit((doc) => setPosture(doc, rule.id, "hold", { who: rule.who, timeoutSeconds: rule.timeoutSeconds })); return; }
    edit((doc) => setPosture(doc, rule.id, "ticket", { who: rule.who, ticketTTLSeconds: rule.ticketTTLSeconds, grantTTLSeconds: rule.grantTTLSeconds }));
  };

  return (
    <div className="flex flex-col gap-3.5" data-rule-editor={rule.id}>
      <div className="flex flex-col gap-1.5">
        <Head label={WHAT_HAPPENS} />
        <div role="group" aria-label={WHAT_HAPPENS} className="inline-flex w-fit overflow-hidden rounded-md border border-border" data-posture={rule.bucket}>
          {BUCKETS.map((b) => (
            <button key={b} type="button" aria-pressed={rule.bucket === b} className={cn(SEG, rule.bucket === b && SEG_ON[b])} onClick={() => pick(b)}>
              {BUCKET_WORD[b]}
            </button>
          ))}
        </div>
        <span className="text-sm text-text-2" data-consequence>{consequenceWords(rule.posture)}</span>
        {names > 1 && <span className={HINT} data-applies-all>{appliesToAll(names)}</span>}
        {droppedDenial && rule.bucket === "allow" && (
          <span className="text-[13px] leading-relaxed text-warn" data-allow-removes>{allowRemoves(names || 1)}</span>
        )}
      </div>

      {rule.bucket === "hum" && (
        <div className="flex flex-col gap-2">
          <Head label={WHO_DECIDES} help={WHO_HELP} />
          <RadioGroup value={pool} onValueChange={setPool} className="gap-2.5" data-who={pool}>
            <Choice id={"who-sponsor-" + rule.id} value="sponsor" label={WHO_SPONSOR} />
            <Choice id={"who-team-" + rule.id} value="team" label={WHO_TEAM} line={team ? WHO_TEAM_LINE : NO_APPROVER_ROLE}>
              {team && (
                <Select value={team} onValueChange={(v) => edit((doc) => setApprove(doc, rule.id, { who: { sponsor: pool === "both", roles: [v] } }))}>
                  <SelectTrigger aria-label={WHO_TEAM} className="h-8 w-56"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {teams.map((name) => <SelectItem key={name} value={name}>{name}</SelectItem>)}
                  </SelectContent>
                </Select>
              )}
            </Choice>
            <Choice id={"who-both-" + rule.id} value="both" label={WHO_BOTH} />
          </RadioGroup>
        </div>
      )}

      {rule.bucket === "hum" && (
        <div className="flex flex-col gap-2">
          <Head label={HOW} help={HOW_HELP} />
          <RadioGroup value={rule.posture === "ticket" ? "ticket" : "hold"} onValueChange={setHow} className="gap-2.5" data-how={rule.posture}>
            <Choice id={"how-hold-" + rule.id} value="hold" label={HOW_HOLD} line={HOW_HOLD_LINE}>
              <Duration label={HOW_HOLD} seconds={rule.timeoutSeconds} on={rule.posture !== "ticket"} onChange={(s) => edit((doc) => setApprove(doc, rule.id, { timeoutSeconds: s }))} />
            </Choice>
            <Choice id={"how-ticket-" + rule.id} value="ticket" label={HOW_TICKET} line={HOW_TICKET_LINE}>
              <Duration label={HOW_TICKET} seconds={rule.ticketTTLSeconds} on={rule.posture === "ticket"} onChange={(s) => edit((doc) => setApprove(doc, rule.id, { ticketTTLSeconds: s }))} />
              <span className="text-sm text-foreground">{HOW_TICKET_MID}</span>
              <Duration label={HOW_TICKET_END} seconds={rule.grantTTLSeconds} on={rule.posture === "ticket"} onChange={(s) => edit((doc) => setApprove(doc, rule.id, { grantTTLSeconds: s }))} />
              <span className="text-sm text-foreground">{HOW_TICKET_END}</span>
            </Choice>
          </RadioGroup>
          {short && <p className="max-w-[80ch] text-[13px] leading-relaxed text-warn" data-w1>{W1(holders, rule.timeoutSeconds)}</p>}
        </div>
      )}

      <Reason
        rule={rule}
        unused={rule.bucket === "allow"}
        stale={rule.bucket === "hum" && staleReason !== null && rule.reason === staleReason}
        onEdit={onEdit}
      />

      <PolicyMatching rule={rule} events={events} onEdit={onEdit} onSplit={onSplit} />

      <div className="flex border-t border-border pt-3">
        <Button variant="outline" className="border-danger/40 text-danger hover:bg-danger-bg hover:text-danger" onClick={() => setAsk(true)}>{REMOVE_RULE}</Button>
      </div>

      <AlertDialog open={ask} onOpenChange={setAsk}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{removeRuleTitle(rule.id)}</AlertDialogTitle>
            <AlertDialogDescription>{REMOVE_RULE_BODY}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={() => onRemove && onRemove(rule.id)}>{REMOVE}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
