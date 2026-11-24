// The policy sets of a draft as its page reads them: each set item with its
// rules marked against the other side, the rules that name a server on a
// person's machine, the rules that name a role, and the fields an edited
// rule changes. No React here.
import type { DraftDetail, DraftGate, DraftItem, GainOutcome } from "./api";
import type { Outcome } from "./chip-words";
import { type ChangeWhat, type RuleMark, ruleMarks, sidesOf, whatOf } from "./drafts-model";
import { ASPECT_OTHER, ASPECT_OUTCOME, ASPECT_SERVER } from "./drafts-words";
import { machineServers } from "./machine-servers";
import { type Bucket, type RuleView, type SetView, openDoc, readSet } from "./policy-model";
import { BUCKET_OUTCOME, MATCH, REASON } from "./policy-words";

// GAIN_OUTCOME reads a gain's outcome as one of the four outcomes. unknown
// is none of them.
export const GAIN_OUTCOME: Partial<Record<GainOutcome, Outcome>> = { runs: "allowed", "needs-approval": "approval", denied: "denied", "not-reachable": "none" };

// OUTCOME_RANK is the order the page lists outcomes in: needs approval
// first, then allowed, then denied.
export const OUTCOME_RANK: Record<Outcome, number> = { approval: 0, allowed: 1, denied: 2, none: 3 };

// BUCKET_RANK orders the buckets by how much a call can do, so a rule that
// moves up loosens.
const BUCKET_RANK: Record<Bucket, number> = { deny: 0, hum: 1, allow: 2 };

export type Marked = { rule: RuleView; mark: RuleMark; was?: RuleView };

// SetReading is one policy set item: what the draft does to it, the set as
// it is after publishing, or before for a removal, and its rules marked
// against the other side.
export type SetReading = { item: DraftItem; what: ChangeWhat; set: SetView; marks: Marked[] };

// setReading reads one policy set item, or null when the item is withheld or
// its document does not parse.
export function setReading(d: DraftDetail, it: DraftItem): SetReading | null {
  if (it.kind !== "PolicySet" || it.withheld) return null;
  const sides = sidesOf(d, it);
  const what = whatOf({ op: sides.after.op, existed: !!sides.before });
  const text = what === "removed" ? sides.before?.doc : sides.after.doc;
  if (!text) return null;
  const doc = openDoc(text);
  if (doc.errors.length) return null;
  const marks = what === "removed" ? [] : ruleMarks(sides.before ? sides.before.doc : null, sides.after.doc);
  return { item: it, what, set: readSet(doc), marks };
}

// setReadings reads every policy set item of the draft that it can, in the
// draft's order.
export function setReadings(d: DraftDetail): SetReading[] {
  return d.draft.items.map((it) => setReading(d, it)).filter((s) => s !== null);
}

export const outcomeOf = (r: RuleView): Outcome => BUCKET_OUTCOME[r.bucket];

// loosens says whether an edited rule's outcome moved up: from denied to
// needs approval or allowed, or from needs approval to allowed.
export const loosens = (m: Marked) => m.mark === "edited" && !!m.was && BUCKET_RANK[m.rule.bucket] > BUCKET_RANK[m.was.bucket];

// MachineRow is one policy rule of the draft's sets that names a server on
// a person's machine: the set, the rule, the server and its outcome.
export type MachineRow = { set: string; rule: RuleView; server: string; outcome: Outcome };

// machineRows answers the rules of the sets that gate calls after
// publishing and name role, one row per server on a person's machine each
// rule names.
export function machineRows(sets: SetReading[], role: string, registered: string[]): MachineRow[] {
  const out: MachineRow[] = [];
  for (const s of sets) {
    if (s.what === "removed" || s.what === "off" || !s.set.roles.includes(role)) continue;
    for (const rule of s.set.rules) for (const server of machineServers(rule, registered)) out.push({ set: s.item.name, rule, server, outcome: outcomeOf(rule) });
  }
  return out;
}

// rulesNaming answers, per set of the draft that is not removed and names
// role in its match, the ids of its rules.
export function rulesNaming(sets: SetReading[], role: string): { set: string; rules: string[] }[] {
  return sets.filter((s) => s.what !== "removed" && s.set.roles.includes(role) && s.set.rules.length).map((s) => ({ set: s.item.name, rules: s.set.rules.map((r) => r.id) }));
}

// Aspect is one field an edited rule changes, as the changed set's table
// shows it: the outcome with who decides and the time to answer, the
// servers, the call type, the names it matches, the events and the reason.
// other stands for a rule that changes only a field the table does not
// show.
export type Aspect = "outcome" | "server" | "lane" | "names" | "events" | "reason" | "other";

// window is how long a person has to answer a rule that needs approval.
export const windowOf = (r: RuleView) => (r.posture === "hold" ? r.timeoutSeconds : r.posture === "ticket" ? r.ticketTTLSeconds : 0);

// gateOf reads a rule that needs approval as the gate the server sends for
// a gain row, so the policy card and the checks draw it alike. It is null
// for a rule that allows or denies.
export function gateOf(r: RuleView): DraftGate | null {
  if (r.bucket !== "hum") return null;
  if (r.posture === "confirm") return { confirm: true };
  if (r.posture === "check") return { server_check: true };
  return { class: r.posture === "ticket" ? "ticket" : "hold", seconds: windowOf(r), roles: r.who.roles, sponsor: r.who.sponsor };
}

const outcomeKey = (r: RuleView) => JSON.stringify([r.posture, r.bucket === "hum" ? [r.who.sponsor, r.who.roles, windowOf(r)] : null]);
const same = (a: string[] | null, b: string[] | null) => JSON.stringify(a) === JSON.stringify(b);

// namesLabel is the label the rule sheet gives the names a rule matches on
// its lane.
export const namesLabel = (r: RuleView) => (r.lane === "shell" ? MATCH.patterns : r.lane === "files" ? MATCH.paths : MATCH.tools);

// ruleAspects answers the fields that differ between the live and the
// drafted reading of one rule, each with the label the policy page's rule
// sheet gives it, in the table's order.
export function ruleAspects(was: RuleView, now: RuleView): { aspect: Aspect; label: string }[] {
  const out: { aspect: Aspect; label: string }[] = [];
  if (outcomeKey(was) !== outcomeKey(now)) out.push({ aspect: "outcome", label: ASPECT_OUTCOME });
  if ((was.app || "") !== (now.app || "")) out.push({ aspect: "server", label: ASPECT_SERVER });
  if (was.lane !== now.lane) out.push({ aspect: "lane", label: MATCH.lane });
  if (!same(was.names, now.names)) out.push({ aspect: "names", label: namesLabel(now) });
  if (!same(was.events, now.events)) out.push({ aspect: "events", label: MATCH.events });
  if (was.reason !== now.reason) out.push({ aspect: "reason", label: REASON });
  return out.length ? out : [{ aspect: "other", label: ASPECT_OTHER }];
}

// namesMove is what an edited rule's names do: the ones that leave and the
// ones that join. A side that names nothing matches every call on its lane,
// so it moves whole, as its lane's every word.
export function namesMove(was: RuleView, now: RuleView, every: (r: RuleView) => string): { leave: string[]; join: string[] } {
  if (!was.names || !now.names) return { leave: was.names || [every(was)], join: now.names || [every(now)] };
  const before = was.names;
  const after = now.names;
  return { leave: before.filter((n) => !after.includes(n)), join: after.filter((n) => !before.includes(n)) };
}

// hasPatterns says whether a rule matches patterns outside the MCP lane,
// whose patterns the changed set's table shows on demand.
export const hasPatterns = (r: RuleView) => (r.lane === "shell" || r.lane === "files") && !!r.names && r.names.length > 0;
