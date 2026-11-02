// What a draft does to access as the review page reads it from the verdict:
// the objects each role covers on a server that pins an object type, now
// and after publishing, the groups of Access after publishing, what the
// page's lead says, and the lines of an agent's self-loosening. Every noun
// comes from the coverage entry's own type and plural. No React and no
// sentence here: drafts-words.ts words what this file answers.
import type { DraftCoverage, DraftDetail, DraftFinding, DraftGain, DraftObjectName, DraftVerdict } from "./api";
import { AGENT_LOOSEN_CODE, gainGroups, gainMark, gainStory, roleOf } from "./drafts-model";
import { type Nouns, nounsOf } from "./object-words";

// GUARDRAIL_CHANGED_CODE is the risk of a gate that stops applying, the line
// that says what an agent's self-loosening takes away.
export const GUARDRAIL_CHANGED_CODE = "policy.guardrail-changed";
// NOBODY_HOLDS_CODE is the note of a role nobody holds, which the page's
// lead says instead when it names that role.
export const NOBODY_HOLDS_CODE = "info.nobody-holds";

export const coverageNouns = (c: DraftCoverage): Nouns => nounsOf(c);

// coverageFor is the coverage entry of one role on one server. It is
// undefined for a server that pins no type and for a server too old to send
// the list, where the page keeps the words it had before the list existed.
export const coverageFor = (v: DraftVerdict, role: string, server: string) => (v.objects || []).find((c) => c.role === role && c.server === server);

// holdersOfRole counts the holders of a role from its first coverage entry,
// or null when the verdict carries none for it.
export function holdersOfRole(v: DraftVerdict, role: string): number | null {
  const c = (v.objects || []).find((x) => x.role === role);
  return c ? c.holders_count : null;
}

// An object is the same object by its id, so a value written differently
// is no change. A server that resolved no id is compared by value.
const idOf = (o: DraftObjectName) => o.id || o.value;

// ObjectsMove is what one coverage entry changes: the objects added and
// dropped, and whether the role gains or loses every object.
export type ObjectsMove = { added: DraftObjectName[]; dropped: DraftObjectName[]; every: "gained" | "lost" | "kept" | "none"; changed: boolean };

export function objectsMove(c: DraftCoverage): ObjectsMove {
  if (c.every_before || c.every_after) {
    const every = c.every_before && c.every_after ? "kept" : c.every_after ? "gained" : "lost";
    return { added: [], dropped: [], every, changed: every !== "kept" };
  }
  const before = new Set(c.before.map(idOf));
  const after = new Set(c.after.map(idOf));
  const added = c.after.filter((o) => !before.has(idOf(o)));
  const dropped = c.before.filter((o) => !after.has(idOf(o)));
  return { added, dropped, every: "none", changed: added.length + dropped.length > 0 };
}

// isNewRole reports whether the draft adds the role: a Role item it puts
// that did not exist. A role that exists and covered nothing is not new,
// so what it covers after publishing is a gain.
export const isNewRole =(d: DraftDetail, role: string) => d.draft.items.some((it) => it.kind === "Role" && it.name === role && it.op === "put" && !it.existed);

// roleToolsOf answers the tools a role names on a server after publishing,
// read from the role document the draft holds, or null for a role the
// draft does not hold, whose tools the page does not know.
export function roleToolsOf(d: DraftDetail): (role: string, server: string) => string[] | null {
  return (role, server) => {
    const it = d.draft.items.find((x) => x.kind === "Role" && x.name === role && x.op === "put" && x.doc);
    const row = roleOf(it?.doc)?.bindings.find((b) => b.app === server);
    return row ? row.tools : null;
  };
}

// AccessGroup is one group of Access after publishing: a role on a server
// with its holders, the rows whose outcome changes, the tools that keep
// their outcome, the coverage entry where the server pins a type, and
// isNew when the draft adds the role.
export type AccessGroup = { role: string; server: string; holders?: string[]; count: number; rows: DraftGain[]; same: string[]; coverage?: DraftCoverage; isNew: boolean };

// accessGroups merges the gain rows and the coverage list into one group
// per role and server, sorted the way the server sorts its rows. A coverage
// entry whose objects do not change adds no group of its own, so a group
// with no row is a role that only reaches other objects, the case no gain
// row shows. same counts the rows that keep their outcome and the role's
// other named tools.
export function accessGroups(gains: DraftGain[], objects: DraftCoverage[] | undefined, toolsOf: (role: string, server: string) => string[] | null, isNew: (role: string) => boolean): AccessGroup[] {
  const out: AccessGroup[] = gainGroups(gains).map((g) => ({
    role: g.role, server: g.server, holders: g.holders, count: g.count,
    rows: g.rows.filter((r) => gainMark(r) !== "same"),
    same: g.rows.filter((r) => gainMark(r) === "same").map((r) => r.tool),
    isNew: isNew(g.role),
  }));
  for (const c of objects || []) {
    const g = out.find((x) => x.role === c.role && x.server === c.server);
    if (g) g.coverage = c;
    else if (objectsMove(c).changed) out.push({ role: c.role, server: c.server, holders: c.holders, count: c.holders_count, rows: [], same: [], coverage: c, isNew: isNew(c.role) });
  }
  for (const g of out) {
    const shown = new Set([...g.rows.map((r) => r.tool), ...g.same]);
    for (const t of toolsOf(g.role, g.server) || []) if (t !== "*" && !shown.has(t)) g.same.push(t);
  }
  const key = (g: AccessGroup) => [g.role, g.server];
  return out.sort((x, y) => {
    const [a, b] = [key(x), key(y)];
    return a[0] !== b[0] ? (a[0] < b[0] ? -1 : 1) : a[1] < b[1] ? -1 : a[1] > b[1] ? 1 : 0;
  });
}

// CoverLead is one sentence of the lead about a role's objects: the entry,
// what it changes, and whether the draft adds the role.
export type CoverLead = { coverage: DraftCoverage; move: ObjectsMove; isNew: boolean };

// Lead is what the page's lead says, by kind: the roles whose objects
// change, the held roles that gain tools, the roles nobody holds, whether it
// says nobody gains, and the held roles that lose tools.
export type Lead = { covers: CoverLead[]; gain: string[]; nobody: string[]; nobodyGains: boolean; lose: string[] };

// leadOf reads the lead from the verdict. A role a coverage sentence names
// is not named again by the gain sentences. Nobody gains is said only when
// no held role gains a tool or an object, no sentence already says why
// nobody gains, and the reader reads every row. A role the draft removes
// gets no coverage sentence: its card says so.
export function leadOf(d: DraftDetail, hidden: boolean): Lead {
  const v = d.verdict;
  const removed = (role: string) => d.draft.items.some((it) => it.kind === "Role" && it.name === role && it.op === "remove");
  const covers = (v.objects || []).map((c) => ({ coverage: c, move: objectsMove(c), isNew: isNewRole(d, c.role) })).filter((l) => l.move.changed && !removed(l.coverage.role));
  const said = new Set(covers.map((l) => l.coverage.role));
  const story = gainStory(v.gains || []);
  const objectGain = covers.some((l) => l.coverage.holders_count > 0 && (l.move.added.length > 0 || l.move.every === "gained"));
  const anyoneGains = story.gain.length > 0 || objectGain;
  const gain = story.gain.filter((r) => !said.has(r));
  const nobody = anyoneGains ? [] : story.nobodyHolds.filter((r) => !said.has(r));
  const unheldCover = covers.some((l) => !l.coverage.holders_count);
  const nobodyGains = !anyoneGains && !nobody.length && !unheldCover && !hidden && (!story.none || covers.length > 0);
  return { covers, gain, nobody, nobodyGains, lose: story.lose };
}

// mergedNotes are the keys of the notes the lead already says: a role
// nobody holds, named by the lead's coverage or nobody sentence.
export function mergedNotes(v: DraftVerdict, lead: Lead): string[] {
  const nobody = new Set([...lead.nobody, ...lead.covers.filter((l) => !l.coverage.holders_count).map((l) => l.coverage.role)]);
  return (v.info || []).filter((f) => f.code === NOBODY_HOLDS_CODE && nobody.has((f.object || "").replace(/^Role\//, ""))).map((f) => f.key);
}

// Loosening is one set an agent's draft loosens while the set binds that
// agent: the agent.guardrail line, and the lines of the same set that say
// which gates stop applying.
export type Loosening = { line: DraftFinding; loses: DraftFinding[] };

// loosenings answers the agent's self-loosenings among the risks, in the
// verdict's order. A refusal of the same code is not one: it stays refused.
// A line cut for this reader names no object, so it matches no other line.
export function loosenings(v: DraftVerdict): Loosening[] {
  const risks = v.risks || [];
  return risks.filter((f) => f.code === AGENT_LOOSEN_CODE).map((line) => ({ line, loses: line.object ? risks.filter((f) => f.code === GUARDRAIL_CHANGED_CODE && f.object === line.object) : [] }));
}
