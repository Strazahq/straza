// In short: the few rows at the top of a draft's page that say what
// publishing does, each a label and a row of pills. One function builds the
// rows from the draft's detail and the page draws them. Where a row could
// be wrong it builds none at all, and the page shows the In this draft
// table instead, because a wrong summary on the page a person publishes
// from is worse than none. No React here.
import type { DraftDetail, DraftItem, RoleRow } from "./api";
import type { Kind, Outcome } from "./chip-words";
import { roleHolders } from "./drafts-access";
import { type ChangeWhat, gainMark, iconKind, impliedOf, roleOf, sidesOf, whatOf } from "./drafts-model";
import { GAIN_OUTCOME, OUTCOME_RANK, type SetReading, loosens, outcomeOf, setReading } from "./drafts-rules";
import {
  EVERYONE_PILL,
  EVERY_TOOL_PILL,
  NOBODY_YET,
  ON,
  SHORT_AFFECTS,
  SHORT_ALSO,
  SHORT_DOES,
  SHORT_GIVES,
  SHORT_HOLDERS_NOW,
  SHORT_LOOSENS,
  holderCount,
  ruleCount,
  rulesChanged,
  toolCount,
} from "./drafts-words";
import { machineServers } from "./machine-servers";

// ShortValue is one pill of a row: a tag that names a thing with its kind
// icon, a round pill with a count or a fact, an outcome chip, the arrow
// between a before and an after, a muted word, a policy rule's name, and
// the button that assigns a role the draft added.
export type ShortValue =
  | { t: "tag"; kind: Kind | null; name: string }
  | { t: "pill"; text: string; icon?: "pencil" | "person" | "wrench" }
  | { t: "outcome"; outcome: Outcome }
  | { t: "arrow" }
  | { t: "muted"; text: string }
  | { t: "rule"; name: string }
  | { t: "assign"; role: string; id: string };

export type ShortRow = { key: string; label: string; values: ShortValue[] };

// ShortContext is what the page read besides the draft: the registered
// servers after publishing, null where it cannot tell a server on a
// person's machine apart; whether a later publish replaced the draft; and
// the live role list of a published draft, null while unread.
export type ShortContext = { registered: string[] | null; replaced: boolean; roles: RoleRow[] | null };

type Read = { item: DraftItem; what: ChangeWhat; kind: Kind | null; set: SetReading | null };

const WHATS: ChangeWhat[] = ["new", "changed", "on", "off", "removed"];

const tag = (kind: Kind | null, name: string): ShortValue => ({ t: "tag", kind, name });
const holdersPill = (n: number): ShortValue => (n === 0 ? { t: "pill", text: NOBODY_YET } : { t: "pill", text: holderCount(n), icon: "person" });

// readItem reads one item, or null for one In short has no rule for: a
// withheld item, a kind or a change intake does not accept, or a document
// that does not read.
function readItem(d: DraftDetail, it: DraftItem): Read | null {
  if (it.withheld) return null;
  const sides = sidesOf(d, it);
  const what = whatOf({ op: sides.after.op, existed: !!sides.before, was: sides.before?.op });
  if (it.kind === "App" || it.kind === "Role") {
    if (what === "off") return null;
    const role = it.kind === "Role" ? roleOf(sides.after.doc || sides.before?.doc) : null;
    if (it.kind === "Role" && what !== "removed" && !role) return null;
    return { item: it, what, kind: iconKind(it.kind, role), set: null };
  }
  if (it.kind !== "PolicySet") return null;
  const set = setReading(d, it);
  if (!set && what !== "removed") return null;
  return { item: it, what, kind: "policy-set", set };
}

// setPills follow a set's tag: the share of its policy rules a change
// touches, or the policy rules a set turned off keeps stored or a set
// turned on brings back.
function setPills(r: Read): ShortValue[] {
  if (!r.set) return [];
  if (r.what === "changed") {
    const n = r.set.marks.filter((m) => m.mark !== "same").length;
    return n ? [{ t: "pill", icon: "pencil", text: rulesChanged(n, r.set.marks.length) }] : [];
  }
  const n = r.set.set.rules.length;
  return (r.what === "off" || r.what === "on") && n ? [{ t: "pill", text: ruleCount(n) }] : [];
}

// Along is one object the removals take along without naming it as an
// item: its Kind/Name, its name and its kind icon.
export type Along = { object: string; name: string; kind: Kind | null };

// impliedSplit sorts what the removals take along into what goes and what
// only changes. A published draft's change record says which. An open one
// has live state only, where a role its removed server owns goes and every
// other one changes.
export function impliedSplit(d: DraftDetail): { gone: Along[]; changed: Along[] } {
  const removedApps = new Set(d.draft.items.filter((it) => it.kind === "App" && it.op === "remove").map((it) => it.name));
  const gone: Along[] = [];
  const changed: Along[] = [];
  for (const object of impliedOf(d)) {
    const i = object.indexOf("/");
    const kind = object.slice(0, i);
    const name = object.slice(i + 1);
    const row = (d.changes || []).find((c) => c.implied && c.kind === kind && c.name === name);
    const doc = d.draft.state === "published" ? row?.before_doc : (d.live || {})[object]?.doc;
    const role = kind === "Role" ? roleOf(doc) : null;
    const removed = d.draft.state === "published" ? row?.after_op === "remove" : !!role && removedApps.has(role.server);
    (removed ? gone : changed).push({ object, name, kind: iconKind(kind, role) });
  }
  return { gone, changed };
}

// alsoRows name what the removals take along that is not an item.
function alsoRows(d: DraftDetail, tense: 0 | 1): ShortRow[] {
  const { gone, changed } = impliedSplit(d);
  const out: ShortRow[] = [];
  if (gone.length) out.push({ key: "also-removed", label: SHORT_ALSO.removed[tense], values: gone.map((a) => tag(a.kind, a.name)) });
  if (changed.length) out.push({ key: "also-changed", label: SHORT_ALSO.changed[tense], values: changed.map((a) => tag(a.kind, a.name)) });
  return out;
}

type Give = { role: string; server: string; machine: boolean; outcome: Outcome; tools: Set<string>; every: boolean };

// givesRows say what a role reaches after publishing: the gain rows of an
// open draft, and the new and edited policy rules of the draft's sets that
// name a server on a person's machine, which no gain row carries. One row
// per role, server and outcome, needs approval first.
function givesRows(d: DraftDetail, reads: Read[], registered: string[] | null): ShortRow[] {
  const gives = new Map<string, Give>();
  const add = (role: string, server: string, machine: boolean, outcome: Outcome, names: string[] | null) => {
    const key = [role, server, outcome].join("\n");
    const g = gives.get(key) || { role, server, machine, outcome, tools: new Set<string>(), every: false };
    if (names) for (const n of names) g.tools.add(n);
    else g.every = true;
    gives.set(key, g);
  };
  if (d.draft.state === "open") {
    for (const g of d.verdict.gains || []) {
      const o = GAIN_OUTCOME[g.after];
      if (gainMark(g) === "gains" && o) add(g.role, g.server, false, o, [g.tool]);
    }
  }
  if (registered) {
    for (const r of reads) {
      const s = r.set;
      if (!s || s.what === "removed" || s.what === "off" || !s.set.roles.length) continue;
      for (const m of s.marks) {
        if (m.mark !== "new" && m.mark !== "edited") continue;
        for (const server of machineServers(m.rule, registered)) for (const role of s.set.roles) add(role, server, true, outcomeOf(m.rule), m.rule.names);
      }
    }
  }
  const kindOf = (role: string) => reads.find((r) => r.item.kind === "Role" && r.item.name === role)?.kind ?? null;
  return [...gives.entries()]
    .sort((a, b) => OUTCOME_RANK[a[1].outcome] - OUTCOME_RANK[b[1].outcome])
    .map(([key, g]) => ({
      key: "gives-" + key,
      label: SHORT_GIVES,
      values: [
        tag(kindOf(g.role), g.role), { t: "arrow" }, { t: "outcome", outcome: g.outcome },
        { t: "pill", icon: "wrench", text: g.every ? EVERY_TOOL_PILL : toolCount(g.tools.size) },
        { t: "muted", text: ON }, tag(g.machine ? "local-server" : "server", g.server),
      ],
    }));
}

// loosensRows name each edited policy rule whose outcome moved up, with its
// outcome before and after.
function loosensRows(reads: Read[], tense: 0 | 1): ShortRow[] {
  return reads.flatMap((r) => (r.set ? r.set.marks.filter(loosens).map((m) => ({
    key: "loosens-" + r.item.name + "/" + m.rule.id,
    label: SHORT_LOOSENS[tense],
    values: [{ t: "rule", name: m.rule.id }, { t: "outcome", outcome: outcomeOf(m.was!) }, { t: "arrow" }, { t: "outcome", outcome: outcomeOf(m.rule) }] as ShortValue[],
  })) : []));
}

// affectsRow says who holds the roles an open draft names, as role items or
// in a set's match, where the verdict tells, and everyone for a set with no
// match.
function affectsRow(d: DraftDetail, reads: Read[]): ShortRow[] {
  const names = [...new Set(reads.flatMap((r) => (r.item.kind === "Role" ? [r.item.name] : r.set ? r.set.set.roles : [])))];
  const known = names.map((name) => ({ name, n: roleHolders(d.verdict, name) })).filter((k) => k.n !== null);
  const kindOf = (role: string) => reads.find((r) => r.item.kind === "Role" && r.item.name === role)?.kind ?? null;
  const values: ShortValue[] = known.flatMap((k) => [...(known.length > 1 ? [tag(kindOf(k.name), k.name)] : []), holdersPill(k.n as number)]);
  if (reads.some((r) => r.set && !r.set.set.roles.length && !r.set.set.matchOther)) values.push({ t: "pill", text: EVERYONE_PILL });
  return values.length ? [{ key: "affects", label: SHORT_AFFECTS, values }] : [];
}

// holdersRow says who holds each role a published draft added or changed,
// from the live role list, and offers to assign a role it added that
// nobody holds.
function holdersRow(reads: Read[], roles: RoleRow[]): ShortRow[] {
  const held = reads
    .filter((r) => r.item.kind === "Role" && (r.what === "new" || r.what === "changed"))
    .map((r) => ({ r, row: roles.find((x) => x.name === r.item.name) }))
    .filter((x) => !!x.row);
  const values: ShortValue[] = held.flatMap(({ r, row }) => {
    const n = row?.holder_count || 0;
    const assign: ShortValue[] = r.what === "new" && n === 0 && row ? [{ t: "assign", role: r.item.name, id: row.id }] : [];
    return [...(held.length > 1 ? [tag(r.kind, r.item.name)] : []), holdersPill(n), ...assign];
  });
  return values.length ? [{ key: "holders", label: SHORT_HOLDERS_NOW, values }] : [];
}

// shortRows builds In short, or no row when the draft has no item, holds an
// item withheld from the reader, or holds one it has no rule for.
export function shortRows(d: DraftDetail, ctx: ShortContext): ShortRow[] {
  const items = d.draft.items;
  if (!items.length) return [];
  const reads: Read[] = [];
  for (const it of items) {
    const r = readItem(d, it);
    if (!r) return [];
    reads.push(r);
  }
  const state = d.draft.state;
  const tense = state === "published" ? 1 : 0;
  const live = state === "open" || (state === "published" && !ctx.replaced);
  const rows: ShortRow[] = [];
  for (const what of WHATS) {
    const these = reads.filter((r) => r.what === what);
    if (these.length) rows.push({ key: "does-" + what, label: SHORT_DOES[what][tense], values: these.flatMap((r) => [tag(r.kind, r.item.name), ...setPills(r)]) });
  }
  rows.push(...alsoRows(d, tense));
  if (live) rows.push(...givesRows(d, reads, ctx.registered));
  rows.push(...loosensRows(reads, tense));
  if (state === "open") rows.push(...affectsRow(d, reads));
  if (state === "published" && !ctx.replaced && ctx.roles) rows.push(...holdersRow(reads, ctx.roles));
  return rows;
}
