// What the review page says about each item of a draft in two places, the
// row of In this draft and the head of the item's card: its kind, the
// server that owns a role, the change, who holds a role, the outcomes of a
// policy set, the summary chips of a changed role, and one line of what
// publishing does. An open draft reads live state and the verdict, a
// published one its change record. No React here.
import { coversKey } from "./access-covers";
import { PINNED_TYPE, coversOn } from "./access-object-words";
import type { DraftDetail, DraftItem } from "./api";
import { coverageFor, coverageNouns, objectsMove, roleHolders } from "./drafts-access";
import { type ChangeWhat, type RoleView, type Sides, type ToolsMove, appRows, manifestOf, objectOf, remoteApp, roleOf, ruleMarks, setOutcomes, sidesOf, toolsMoves, whatOf } from "./drafts-model";
import {
  DESCRIPTION,
  IMPLIES,
  ITEM_KIND,
  NO_FIELD_CHANGE,
  NO_ROLE_CHANGE,
  OFF_LINE,
  REMOVED_LINE,
  REMOVED_LINE_PAST,
  type Sign,
  changesLine,
  everyChip,
  objectsChip,
  outcomesWords,
  reachesLine,
  roleKind,
  rulesLine,
  startsLine,
  toolsChip,
} from "./drafts-words";
import { nounsOf } from "./object-words";
import { list } from "./policy-words";
import { NONE } from "./words";

export type SummaryChip = { sign: Sign; word: string };

// ItemFacts is one item as the row and the card head read it. kind names
// the kind without the owner, role and before are a role's two readings,
// live says the object has a page to open now, holders is null where the
// verdict says nothing, outcomes is empty where a set gives none after
// publishing, and line is empty where the summary chips say it.
export type ItemFacts = {
  item: DraftItem;
  object: string;
  sides: Sides;
  what: ChangeWhat;
  kind: string;
  owner: string;
  role: RoleView | null;
  before: RoleView | null;
  tools: ToolsMove[];
  live: boolean;
  holders: number | null;
  outcomes: string;
  summary: SummaryChip[];
  line: string;
};

// cardId is the element id of an item's card, which its row jumps to.
export const cardId = (object: string) => "draft-item-" + object;

const count = (chips: SummaryChip[], sign: Sign, n: number, word: (sign: Sign, n: number) => string) => {
  if (n) chips.push({ sign, word: word(sign, n) });
};

// roleSummary sums up what a changed role gains and drops: tools first, then
// the objects of each server that pins a type, read from the verdict's
// coverage entries. Every tool counts by its name, so a row's every tool
// counts as one.
function roleSummary(d: DraftDetail, name: string, tools: ToolsMove[]): SummaryChip[] {
  const out: SummaryChip[] = [];
  count(out, "+", tools.reduce((n, m) => n + m.added.length, 0), toolsChip);
  count(out, "-", tools.reduce((n, m) => n + m.removed.length, 0), toolsChip);
  for (const m of tools) {
    const c = coverageFor(d.verdict, name, m.server);
    if (!c) continue;
    const w = coverageNouns(c);
    const move = objectsMove(c);
    if (move.every === "gained") out.push({ sign: "+", word: everyChip("+", w) });
    if (move.every === "lost") out.push({ sign: "-", word: everyChip("-", w) });
    count(out, "+", move.added.length, (s, n) => objectsChip(s, n, w));
    count(out, "-", move.dropped.length, (s, n) => objectsChip(s, n, w));
  }
  return out;
}

// roleFields names what else changes on a live role: its description, the
// roles it implies, and a covers line where the verdict sends no coverage
// entry for its server.
function roleFields(d: DraftDetail, name: string, before: RoleView, after: RoleView): string[] {
  const out: string[] = [];
  if (before.description !== after.description) out.push(DESCRIPTION);
  if (list(before.implies) !== list(after.implies)) out.push(IMPLIES);
  for (const app of [...new Set([...after.bindings, ...before.bindings].map((b) => b.app))]) {
    if (coverageFor(d.verdict, name, app)) continue;
    const was = before.bindings.find((b) => b.app === app)?.covers;
    const now = after.bindings.find((b) => b.app === app)?.covers;
    if (coversKey(was) !== coversKey(now)) out.push(coversOn(app));
  }
  return out;
}

// appLine says what publishing does to a server: it starts a new one, and
// on a live one it pins the type a first pin names and changes the fields
// the Now and After rows name.
function appLine(d: DraftDetail, it: DraftItem, sides: Sides, past: boolean): string {
  const m = manifestOf(sides.after.doc);
  if (!sides.before) {
    const seen = (d.contacted || {})[objectOf(it)];
    return startsLine(remoteApp(sides.after.doc), seen ? seen.tools.length : null, past);
  }
  const rows = m ? appRows(manifestOf(sides.before.doc) || {}, m) : [];
  const first = rows.find((r) => r[0] === PINNED_TYPE && r[1] === NONE);
  const pins = first && m?.straza?.objects ? nounsOf(m.straza.objects).plural : "";
  return changesLine(pins, rows.filter((r) => r !== first).map((r) => r[0]), past, NO_FIELD_CHANGE);
}

// itemFacts reads one item of d.
export function itemFacts(d: DraftDetail, it: DraftItem): ItemFacts {
  const open = d.draft.state === "open";
  const past = d.draft.state === "published";
  const sides = sidesOf(d, it);
  const what = whatOf({ op: sides.after.op, existed: !!sides.before });
  const isRole = it.kind === "Role";
  const role = isRole ? roleOf(sides.after.doc || sides.before?.doc) : null;
  const before = isRole && sides.before ? roleOf(sides.before.doc) : null;
  const tools = isRole && before && role && sides.after.op !== "remove" ? toolsMoves(before, role) : [];
  const facts: ItemFacts = {
    item: it, object: objectOf(it), sides, what, role, before, tools,
    kind: role ? roleKind(role.kind) : ITEM_KIND[it.kind],
    owner: role?.server || "",
    live: !it.withheld && (past ? sides.after.op !== "remove" : it.existed),
    holders: open && isRole ? roleHolders(d.verdict, it.name) : null,
    outcomes: it.kind === "PolicySet" && sides.after.op === "put" && !it.withheld ? outcomesWords(setOutcomes(sides.after.doc)) : "",
    summary: [],
    line: "",
  };
  if (it.withheld) facts.line = it.withheld;
  else if (sides.after.op === "remove") facts.line = past ? REMOVED_LINE_PAST : REMOVED_LINE;
  else if (sides.after.op === "off") facts.line = OFF_LINE;
  else if (it.kind === "App") facts.line = appLine(d, it, sides, past);
  else if (isRole && role && before) {
    facts.summary = roleSummary(d, it.name, tools);
    if (!facts.summary.length) facts.line = changesLine("", roleFields(d, it.name, before, role), past, NO_ROLE_CHANGE);
  } else if (isRole) facts.line = role ? reachesLine(role.bindings, role.implies) : "";
  else {
    const marks = ruleMarks(sides.before ? sides.before.doc : null, sides.after.doc);
    const n = (mark: string) => marks.filter((r) => r.mark === mark).length;
    facts.line = rulesLine({ new: n("new"), edited: n("edited"), removed: n("removed") });
  }
  return facts;
}
