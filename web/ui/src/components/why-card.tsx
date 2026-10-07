import type * as React from "react";
import { ChevronRightIcon } from "lucide-react";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { HelpTip } from "@/components/help-tip";
import type { Decision } from "@/lib/api";
import type { Bucket, Who } from "@/lib/policy-model";
import { BUCKET_WORD, CONFIRM_DECIDES, ENGINE_FIELDS, ENGINE_FIELDS_HELP, decidesLine } from "@/lib/policy-words";
import { cn } from "@/lib/utils";

// The card that reads one decision back, shared by the two what-if sheets:
// the outcome, the sentence that says what decided the call, a context
// line, and the wire fields underneath. The sentences are the audit
// screen's sentences, so a simulated answer and an audit record tell one
// story.

export type CardTone = "ok" | "warn" | "danger" | "plain";

const BORDER: Record<CardTone, string> = {
  ok: "border-ok/40",
  warn: "border-warn/40",
  danger: "border-danger/40",
  plain: "border-border",
};

// The three words in their hues. A call that waits for a person keeps the
// plain text colour: the waiting is the news, not an alarm.
const OUTCOME_TEXT: Record<Bucket, string> = { deny: "text-danger", hum: "text-foreground", allow: "text-ok" };

// approveOf reads the approve block the engine sets on an allow that waits
// for a person, and answers null for every other decision.
function approveOf(d: Decision): Record<string, unknown> | null {
  return d.approve && typeof d.approve === "object" && !Array.isArray(d.approve) ? (d.approve as Record<string, unknown>) : null;
}

// confirms says the decision is mode confirm, where the requester is the
// only decider. The flag rides beside approve on the wire.
const confirms = (d: Decision) => (d as Decision & { confirm?: boolean }).confirm === true;

const names = (v: unknown): string[] => (Array.isArray(v) ? v.filter((s): s is string => typeof s === "string") : []);

// poolOf reads the decider pool off a decision's approve block, the rule
// policy-model.whoOf reads off a rule: nothing named means the requester's
// sponsor. It is read here rather than imported so that a page which only
// reads decisions does not load the YAML parser with the rules model.
function poolOf(approve: Record<string, unknown>): Who {
  const roles = names(approve.roles);
  const deciders = names(approve.deciders);
  return { sponsor: deciders.includes("sponsor") || (roles.length === 0 && deciders.length === 0 && approve.selfApproval !== true), roles };
}

// outcomeOf folds a decision into the three words of the area: refused,
// waiting for a person, or running.
function outcomeOf(d: Decision): Bucket {
  if (d.effect === "deny") return "deny";
  if (approveOf(d) || confirms(d)) return "hum";
  return "allow";
}

// Outcome is the decision in one word, in its tone.
export function Outcome({ d }: { d: Decision }) {
  const bucket = outcomeOf(d);
  return <span className={cn("text-base font-semibold", OUTCOME_TEXT[bucket])} data-outcome={bucket}>{BUCKET_WORD[bucket]}</span>;
}

// decidersLine says who resolves the gate this decision carries, and is
// empty for a decision that waits for nobody.
export function decidersLine(d: Decision): string {
  if (confirms(d)) return CONFIRM_DECIDES;
  const approve = approveOf(d);
  return approve ? decidesLine(poolOf(approve)) : "";
}

const stripDot = (s: string) => s.replace(/\.$/, "");

// Provenance is the one sentence that says what decided the call. A
// simulate answer always carries the set name field, so the record-age
// branch of the audit reader is not needed here.
function Provenance({ d, profile }: { d: Decision; profile: string }) {
  if (!d.ruleId) {
    if (d.approve || confirms(d) || d.serverCheck || d.classify || !["allow", "deny"].includes(d.effect)) {
      return <span>No policy rule was identified in the response.{d.reason ? " " + d.reason : ""}</span>;
    }
    const rest = d.effect === "deny"
      ? (profile === "enterprise" ? "Denied by the enterprise profile default: a call nothing allows is denied." : "Denied by the profile default: no policy allowed this call.")
      : /allowed by role access/i.test(d.reason || "") ? "Allowed by role access. No policy rule gates this tool." : "Allowed by the profile default: no policy matched.";
    return <span><b className="font-semibold text-foreground">No policy matched this call.</b>{" " + rest}</span>;
  }
  const reason = d.reason ? ": " + stripDot(d.reason) : "";
  return (
    <span>
      {"Decided by rule "}<b className="font-semibold text-foreground">{d.ruleId}</b>
      {d.setName ? <>{" in policy "}<b className="font-semibold text-foreground">{d.setName}</b></> : null}
      {reason + "."}
    </span>
  );
}

// wireLine is the decision in the engine's own field names, for an operator
// who reads the YAML: the effect, the gate it carries, the rule and policy
// that fired, and the snapshot it was decided against.
export function wireLine(d: Decision, snapshot: string, which: "live" | "draft"): string {
  const approve = approveOf(d);
  const mode = confirms(d) ? "confirm" : approve ? "approve" : "";
  const klass = approve && typeof approve.class === "string" ? approve.class : "";
  const snap = which === "draft" ? "(draft)" : snapshot ? snapshot.slice(0, 8) + " (live)" : "none";
  return [
    "effect=" + (d.effect || "none"),
    mode && "mode=" + mode,
    klass && "class=" + klass,
    "ruleId=" + (d.ruleId || "none"),
    "setName=" + (d.setName || "none"),
    "snapshot=" + snap,
  ].filter(Boolean).join(" · ");
}

// EngineFields folds the engine's own field names away, so a card leads
// with the sentence an operator reads and the wire line stays one click
// behind it. The fold is closed on every open, and its content leaves the
// document while it is closed.
export function EngineFields({ line }: { line: string }) {
  return (
    <Collapsible className="mt-2 border-t border-dashed border-border pt-2" data-engine-fields>
      <div className="flex items-center gap-1">
        <CollapsibleTrigger className="group inline-flex items-center gap-1 rounded-sm text-[13px] text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
          <ChevronRightIcon className="size-3.5 transition-transform group-data-[state=open]:rotate-90" aria-hidden="true" />
          {ENGINE_FIELDS}
        </CollapsibleTrigger>
        <HelpTip label={ENGINE_FIELDS} text={ENGINE_FIELDS_HELP} />
      </div>
      <CollapsibleContent className="pt-1.5 font-mono text-[13px] break-all text-muted-foreground">{line}</CollapsibleContent>
    </Collapsible>
  );
}

export type WhyCardProps = {
  d: Decision;
  // profile words the no-rule sentence; an empty profile keeps the generic
  // one, since the sheet claims nothing it did not read.
  profile: string;
  // mark is what a walk finds this card by: data-why-card="<mark>".
  mark: string;
  caption?: string;
  // head is the outcome word and whatever the sheet puts beside it.
  head: React.ReactNode;
  context?: React.ReactNode;
  wire?: string;
  tone?: CardTone;
};

// WhyCard frames one decision.
export function WhyCard({ d, profile, mark, caption, head, context, wire, tone = "plain" }: WhyCardProps) {
  return (
    <div role="status" data-why-card={mark} className={cn("flex flex-col gap-1.5 rounded-md border bg-card px-4 py-3 text-sm", BORDER[tone])}>
      {caption && <span className="text-[12px] font-semibold uppercase tracking-[.06em] text-muted-foreground">{caption}</span>}
      <div className="flex flex-wrap items-center gap-2">{head}</div>
      <p className="leading-relaxed text-text-2"><Provenance d={d} profile={profile} /></p>
      {context && <p className="text-[13px] leading-relaxed text-muted-foreground">{context}</p>}
      {wire && <EngineFields line={wire} />}
    </div>
  );
}
