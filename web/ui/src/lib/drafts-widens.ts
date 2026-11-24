// The Widens access section of a draft's checks as the review page reads it
// from the verdict and the draft's documents: one row per tool and change
// with the roles it changes for, the objects a role comes to cover, the
// policy rules a changed policy loosens, the warnings folded into a row,
// the draft's objects the lines name, the lines no row can say, and what
// its Details holds. No React here and no sentence: drafts-words.ts words
// what this file answers.
import type { DraftCoverage, DraftDetail, DraftFinding, DraftGain, DraftGate, DraftItem, DraftVerdict, GainOutcome } from "./api";
import { CHANGE_WORD } from "./chip-words";
import { GUARDRAIL_CHANGED_CODE, objectsMove } from "./drafts-access";
import { COVERS_WIDER_CODE, type ChangeWhat, objectOf, whatOf } from "./drafts-model";
import { type Marked, setReading } from "./drafts-rules";
import { NO_VERSION } from "./drafts-words";

// LOCAL_ALLOW_CODE is the risk of a policy rule that allows tools of a
// server Straza has not registered, and NAMES_NOTHING_CODE the warning
// that the same rule names no registered server.
export const LOCAL_ALLOW_CODE = "policy.local-allow";
const NAMES_NOTHING_CODE = "ready.rule-names-nothing";

// Side is one side of a row: its outcome, the gate the server sent for it,
// and the gate's words where the server sent no gate.
export type Side = { outcome: GainOutcome; gate?: DraftGate; words?: string };

// ToolRow is one row of the tools table: tools of one server, one for a
// row of who gains what and every tool a local allow names, local for a
// server on a person's machine, the roles the change is for or everyone,
// the two sides, and the warning folded into the row.
export type ToolRow = { server: string; tools: string[]; local: boolean; roles: string[]; everyone: boolean; now: Side; after: Side; warning?: DraftFinding };

const side = (outcome: GainOutcome, gate?: DraftGate, words?: string): Side => (gate ? { outcome, gate } : words ? { outcome, words } : { outcome });
const roleOfObject = (object?: string) => (object && object.startsWith("Role/") ? object.slice("Role/".length) : "");
const itemOf = (d: DraftDetail, object?: string) => (object ? d.draft.items.find((it) => objectOf(it) === object) : undefined);

// gainRows joins the rows of a risk to the verdict's gains by role, server
// and tool. It is null for a risk that names no role or no rows, and for
// one whose gains the server left out for this reader, which keeps its
// sentence.
function gainRows(f: DraftFinding, gains: DraftGain[]): ToolRow[] | null {
  const role = roleOfObject(f.object);
  if (!role || !f.rows || !f.rows.length) return null;
  const out: ToolRow[] = [];
  for (const r of f.rows) {
    const g = gains.find((x) => x.role === role && x.server === r.server && x.tool === r.tool);
    if (!g) return null;
    out.push({ server: g.server, tools: [g.tool], local: false, roles: [role], everyone: false, now: side(g.before, g.before_gate, g.before_words), after: side(g.after, g.after_gate, g.after_words) });
  }
  return out;
}

// localAllows says a risk is a local allow the page draws as a row: it
// names the server and the tools of its rule.
const localAllows = (f: DraftFinding) => f.code === LOCAL_ALLOW_CODE && !!f.server && !!f.tools && f.tools.length > 0;

// localRow is the row of a local allow: every tool of its rule on the
// server, for the roles its policy matches, from no access to allowed.
function localRow(d: DraftDetail, f: DraftFinding): ToolRow | null {
  if (!localAllows(f)) return null;
  const it = itemOf(d, f.object);
  const set = it ? setReading(d, it)?.set : undefined;
  return {
    server: f.server as string, tools: f.tools as string[], local: true,
    roles: set ? set.roles : [], everyone: !!set && !set.roles.length && !set.matchOther,
    now: { outcome: "not-reachable" }, after: { outcome: "runs" },
  };
}

// foldedWarnings maps each warning that repeats a local allow drawn as a
// row to that risk's key: the rule names no registered server, matched by
// the object, the rule and the server, never by the words.
export function foldedWarnings(v: DraftVerdict): Map<string, string> {
  const out = new Map<string, string>();
  for (const w of v.warnings || []) {
    if (w.code !== NAMES_NOTHING_CODE || !w.object || !w.rule || !w.server) continue;
    const r = (v.risks || []).find((f) => localAllows(f) && f.object === w.object && f.rule === w.rule && f.server === w.server);
    if (r) out.set(w.key, r.key);
  }
  return out;
}

// foldRows folds the rows that say the same change for several roles into
// one row with every role, in the order the rows came.
function foldRows(rows: ToolRow[]): ToolRow[] {
  const out: ToolRow[] = [];
  const at = new Map<string, ToolRow>();
  for (const r of rows) {
    const key = JSON.stringify([r.server, r.tools, r.local, r.now, r.after, r.warning?.key]);
    const have = at.get(key);
    if (!have) {
      const row = { ...r, roles: [...r.roles] };
      at.set(key, row);
      out.push(row);
      continue;
    }
    for (const role of r.roles) if (!have.roles.includes(role)) have.roles.push(role);
    have.everyone = have.everyone || r.everyone;
  }
  return out;
}

// Ref is one part of a line's Now or After: a name with its version, as
// rule@fingerprint, or a bare word with no name.
type Ref = { name: string; version: string };

const refsOf = (words: string): Ref[] =>
  words
    ? words.split("; ").map((p) => {
        const m = /^(.+)@([^@\s]+)$/.exec(p);
        return m ? { name: m[1], version: m[2] } : { name: "", version: p };
      })
    : [];

// PRINT finds a fingerprint: 12 or 64 hex digits standing alone.
const PRINT = /(?<![0-9a-z])(?:[0-9a-f]{64}|[0-9a-f]{12})(?![0-9a-z])/;

// printed says a line's Now or After carries a fingerprint, which the page
// shows under Details alone.
export const printed = (f: DraftFinding) => PRINT.test(f.before || "") || PRINT.test(f.after || "");

// Version is one row of the table under Details: a name, its version now
// and after publishing, NO_VERSION where a side has none.
export type Version = { name: string; now: string; after: string };

// versionsOf reads the versions a line's Now and After name. A bare word on
// one side, such as off, stands for every name of the other side. A line
// that names no version by name is the version of its own object.
function versionsOf(f: DraftFinding): Version[] {
  const before = refsOf(f.before || "");
  const after = refsOf(f.after || "");
  const names = [...new Set([...before, ...after].map((r) => r.name).filter(Boolean))];
  const pick = (refs: Ref[], name: string) => refs.find((r) => r.name === name)?.version || refs.find((r) => !r.name)?.version || NO_VERSION;
  if (names.length) return names.map((name) => ({ name, now: pick(before, name), after: pick(after, name) }));
  return [{ name: (f.object || "").split("/").slice(1).join("/") || f.code, now: f.before || NO_VERSION, after: f.after || NO_VERSION }];
}

// Loosened is what a policy.guardrail-changed line draws: the edited and
// removed policy rules its Now names, cause where the draft turns the
// policy off or removes it and the cause row says it, or null where the
// page cannot read the rules and the line keeps its sentence.
type Loosened = Marked[] | "cause" | null;

function loosenedRules(d: DraftDetail, f: DraftFinding): Loosened {
  const it = itemOf(d, f.object);
  if (!it || it.kind !== "PolicySet") return null;
  if (it.op === "off" || it.op === "remove") return "cause";
  const s = setReading(d, it);
  const names = refsOf(f.before || "").map((r) => r.name);
  if (!s || !names.length || names.some((n) => !n)) return null;
  const marks = names.map((n) => s.marks.find((m) => m.rule.id === n && (m.mark === "edited" || m.mark === "removed")));
  return marks.every(Boolean) ? (marks as Marked[]) : null;
}

// newRule says the rule a line names is new in the draft's policy.
function newRule(d: DraftDetail, f: DraftFinding): boolean {
  const it = itemOf(d, f.object);
  const s = it && f.rule ? setReading(d, it) : null;
  return !!s && s.marks.some((m) => m.rule.id === f.rule && m.mark === "new");
}

// Cause is an object of the draft a line of the section names: the item,
// what the draft does to it, and whether a changed policy applies to the
// proposer.
export type Cause = { item: DraftItem; what: ChangeWhat; applies: boolean };

// Widens is the whole section: the draft's objects the lines name, the
// rows of the three tables, the lines no row says, the warnings folded
// into a row, and the versions and the lines Details lists.
export type Widens = {
  causes: Cause[];
  tools: ToolRow[];
  covers: DraftCoverage[];
  rules: Marked[];
  lines: DraftFinding[];
  folded: DraftFinding[];
  versions: Version[];
  said: DraftFinding[];
};

// widensOf reads the section from the risks the page shows.
export function widensOf(d: DraftDetail, risks: DraftFinding[]): Widens {
  const v = d.verdict;
  const folds = foldedWarnings(v);
  const folded = (v.warnings || []).filter((w) => folds.has(w.key));
  const tools: ToolRow[] = [];
  const covers: DraftCoverage[] = [];
  const rules: Marked[] = [];
  const lines: DraftFinding[] = [];
  const versions: Version[] = [];
  for (const f of risks) {
    if (printed(f)) versions.push(...versionsOf(f));
    else if (f.rule && newRule(d, f)) versions.push({ name: f.rule, now: NO_VERSION, after: CHANGE_WORD.new });
    const rows = gainRows(f, v.gains || []);
    if (rows) { tools.push(...rows); continue; }
    const local = localRow(d, f);
    if (local) { tools.push({ ...local, warning: folded.find((w) => folds.get(w.key) === f.key) }); continue; }
    if (f.code === COVERS_WIDER_CODE) {
      const role = roleOfObject(f.object);
      const moved = (v.objects || []).filter((c) => c.role === role && objectsMove(c).changed);
      if (role && moved.length) { covers.push(...moved); continue; }
    }
    if (f.code === GUARDRAIL_CHANGED_CODE) {
      const l = loosenedRules(d, f);
      if (l === "cause") continue;
      if (l) { rules.push(...l); continue; }
    }
    lines.push(f);
  }
  const named = new Set(risks.map((f) => f.object).filter(Boolean));
  const causes = d.draft.items.filter((it) => named.has(objectOf(it))).map((item) => ({
    item,
    what: whatOf({ op: item.op, existed: item.existed, was: (d.live || {})[objectOf(item)]?.op }),
    applies: risks.some((f) => f.object === objectOf(item) && !!f.binds_proposer),
  }));
  return { causes, tools: foldRows(tools), covers, rules, lines, folded, versions, said: risks };
}
