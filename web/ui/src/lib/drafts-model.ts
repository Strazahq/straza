// The reading of a draft that the Drafts area's screens share: the publish
// body built from the verdict on screen, the acknowledgment rules, the
// verdict's lines by class, who gains what by role, and the facts of each
// item's document. No React here, and no sentence: drafts-words.ts words
// what this file answers.
import type { DraftConflict, DraftDetail, DraftFinding, DraftGain, DraftItem, DraftKind, DraftOp, DraftPublishBody, DraftVerdict, FindingClass, ManifestDoc } from "./api";
import { callerKind, changeRows } from "./manifest-edit";
import { type RuleView, openDoc, rulesOf } from "./policy-model";
import { KIND_NAME, exposeWords, rpsWords, sentWords, timeoutWords, transportWords } from "./server-words";
import { agentsLine } from "./words";

// Acks is what the person did in the publish dialog: the tick of each tick
// line and the text typed for each typed line, both keyed by the finding's
// key.
export type Acks = { ticked: Record<string, boolean>; typed: Record<string, string> };

// typedMatches compares a typed acknowledgment the way the server and
// strazactl do: spaces trimmed, case ignored.
export const typedMatches = (typed: string, entered: string) => entered.trim().toLowerCase() === typed.trim().toLowerCase();

// acknowledged says whether the person acknowledged one risk. A typed risk
// whose text was cut for this reader cannot be typed here, so it does not
// hold the dialog: the server answers its own refusal for it.
export function acknowledged(f: DraftFinding, a: Acks): boolean {
  if (f.ack === "typed") return !f.typed || typedMatches(f.typed, a.typed[f.key] || "");
  return !!a.ticked[f.key];
}

// typedWrongly says a typed risk holds text that is not its typed text, a
// line the person answered and got wrong rather than left empty.
export function typedWrongly(f: DraftFinding, a: Acks): boolean {
  const text = a.typed[f.key] || "";
  return f.ack === "typed" && !!f.typed && text.trim() !== "" && !typedMatches(f.typed, text);
}

// firstMissing is the key of the first risk left unacknowledged, in the
// verdict's order, or null.
export function firstMissing(risks: DraftFinding[], a: Acks): string | null {
  const f = risks.find((r) => !acknowledged(r, a));
  return f ? f.key : null;
}

// publishBody is the body of a publish: the revision and the risk digest
// the person reviewed, and the key of every line they acknowledged with the
// text they typed. Every key is the server's, echoed, never computed.
export function publishBody(revision: number, v: DraftVerdict, a: Acks): DraftPublishBody {
  const ticked: string[] = [];
  const typed: Record<string, string> = {};
  for (const f of v.risks || []) {
    if (f.ack === "typed") {
      const text = a.typed[f.key] || "";
      if (!f.typed || !text.trim()) continue;
      ticked.push(f.key);
      typed[f.key] = text;
    } else if (a.ticked[f.key]) {
      ticked.push(f.key);
    }
  }
  return { revision, risk_digest: v.risk_digest || "", ticked, typed };
}

// pickKey is the key of one pick in a rebase body: the object and the field.
export const pickKey = (c: DraftConflict) => c.object + " " + c.field;

// findingGroups answers the verdict's lines in the page's order, one group
// per class, a missing list read as empty. A line keeps the class of the
// list it came in, whatever its code.
export function findingGroups(v: DraftVerdict): { cls: FindingClass; lines: DraftFinding[] }[] {
  return [
    { cls: "refused", lines: v.refused || [] },
    { cls: "risk", lines: v.risks || [] },
    { cls: "warning", lines: v.warnings || [] },
    { cls: "unchecked", lines: v.unchecked || [] },
    { cls: "passed", lines: v.passed || [] },
    { cls: "info", lines: v.info || [] },
  ];
}

// STALE_CODE is the refusal of a draft whose objects moved on live state
// since its check; Check again answers it.
export const STALE_CODE = "draft.stale";
// UNLISTED_CODE is the line of a server whose tools nobody read, where
// Contact it now sits.
export const UNLISTED_CODE = "unchecked.tools";
// HIDDEN_CODE is the note that verdictFor left out rows of who gains what
// this reader may not read.
export const HIDDEN_CODE = "info.gains-hidden";

// gainsHidden is the sentence of that note, or null when the reader reads
// every row.
export function gainsHidden(v: DraftVerdict): string | null {
  const f = (v.info || []).find((x) => x.code === HIDDEN_CODE);
  return f ? f.sentence : null;
}

export type Shown = "ready" | "refused" | "stale" | "published" | "discarded" | "expired";

// stateOf is the state the page's badge and bar show.
export function stateOf(d: DraftDetail): Shown {
  if (d.draft.state !== "open") return d.draft.state;
  const refused = d.verdict.refused || [];
  if (refused.some((f) => f.code === STALE_CODE)) return "stale";
  return refused.length ? "refused" : "ready";
}

// ---- who gains what ----

export type GainMark = "gains" | "loses" | "gate" | "same" | "unknown";

// RANK orders the outcomes by how much a holder can do.
const RANK: Record<string, number> = { "not-reachable": 0, denied: 0, "needs-approval": 1, runs: 2 };

// gainMark compares one row's outcome before and after publishing.
export function gainMark(g: DraftGain): GainMark {
  if (g.before === "unknown" || g.after === "unknown") return "unknown";
  const a = RANK[g.before] ?? 0;
  const b = RANK[g.after] ?? 0;
  if (b > a) return "gains";
  if (b < a) return "loses";
  return g.before === g.after && (g.before_words || "") === (g.after_words || "") ? "same" : "gate";
}

export type GainGroup = { role: string; server: string; holders?: string[]; count: number; rows: DraftGain[] };

// gainGroups puts the rows under one group per role and server, in the
// order the server answered them.
export function gainGroups(gains: DraftGain[]): GainGroup[] {
  const out: GainGroup[] = [];
  for (const g of gains || []) {
    let group = out.find((x) => x.role === g.role && x.server === g.server);
    if (!group) {
      group = { role: g.role, server: g.server, holders: g.holders, count: g.holders_count, rows: [] };
      out.push(group);
    }
    group.rows.push(g);
  }
  return out;
}

const uniq = (xs: string[]) => [...new Set(xs)];

// gainStory is what the lead sentence of Who gains what says: the roles
// that gain with nobody holding them, the held roles that gain, the held
// roles that lose, and none when no row changes anything.
export function gainStory(gains: DraftGain[]): { nobodyHolds: string[]; gain: string[]; lose: string[]; none: boolean } {
  const rows = gains || [];
  return {
    nobodyHolds: uniq(rows.filter((g) => gainMark(g) === "gains" && !g.holders_count).map((g) => g.role)),
    gain: uniq(rows.filter((g) => gainMark(g) === "gains" && g.holders_count > 0).map((g) => g.role)),
    lose: uniq(rows.filter((g) => gainMark(g) === "loses" && g.holders_count > 0).map((g) => g.role)),
    none: rows.length === 0,
  };
}

// ---- the items' documents ----

// punyURL answers an address with its host in punycode, the form a
// look-alike host cannot hide behind, and any other text as it is.
export function punyURL(u: string): string {
  try {
    return new URL(u).href;
  } catch {
    return u;
  }
}

// hostOf is an address's host in punycode, or fallback when the text is
// no address.
export function hostOf(u: string, fallback: string): string {
  try {
    return new URL(u).host || fallback;
  } catch {
    return fallback;
  }
}

// plainOf parses one YAML document into plain values, or null when it does
// not parse into a mapping.
function plainOf(text: string | undefined): Record<string, unknown> | null {
  if (!text) return null;
  const doc = openDoc(text);
  if (doc.errors.length) return null;
  const v = doc.toJS() as unknown;
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
}

// manifestOf reads an App document as the manifest the server cards read.
export const manifestOf = (text: string | undefined) => plainOf(text) as ManifestDoc | null;

// remoteApp says whether an App document names a remote server, the only
// kind Contact reaches.
export function remoteApp(text: string | undefined): boolean {
  const m = manifestOf(text);
  return !!m && (m.straza?.runtime?.kind || "remote") === "remote";
}

const withPuny = (label: string, words: string) => (label === "Address" ? punyURL(words) : words);

// appFacts reads a server's document in the server page's own words, for a
// server the draft adds.
export function appFacts(doc: ManifestDoc): [string, string][] {
  const rt = doc.straza?.runtime || {};
  const kind = rt.kind || "remote";
  const cred = doc.straza?.credential;
  const ck = cred?.kind || "none";
  const lim = doc.straza?.limits || {};
  const rows: [string, string][] = [["Transport", transportWords(kind)]];
  if (kind === "remote") rows.push(["Address", punyURL(rt.remote?.url || "")]);
  if (kind === "command") rows.push(["Executable", rt.command?.exec || ""], ["Arguments", (rt.command?.args || []).join(" ")]);
  if (kind === "oci") rows.push(["Image", rt.oci?.image || ""]);
  rows.push(["Type", KIND_NAME[ck] || ck]);
  if (ck !== "none") rows.push(["Sent as", sentWords(cred)]);
  if (callerKind(ck)) rows.push(["Agents with nothing of their own", agentsLine(cred)]);
  if (ck === "oauth") rows.push(["Provider", cred?.oauth?.provider || ""], ["Scopes", (cred?.oauth?.scopes || []).join(" ")]);
  if (doc.metadata?.description) rows.push(["Description", doc.metadata.description]);
  rows.push(["Tools exposed", exposeWords(doc.straza?.exposure?.tools)], ["Rate limit", rpsWords(lim.rps)]);
  if (lim.timeoutSeconds) rows.push(["Per-call timeout", timeoutWords(lim.timeoutSeconds, null)]);
  return rows.filter((r) => r[1] !== "");
}

// appRows are the Now and After rows of a server the draft changes, the
// rows the Change sheets show.
export function appRows(before: ManifestDoc, after: ManifestDoc): [string, string, string][] {
  return changeRows(before, after).map(([label, was, now]) => [label, withPuny(label, was), withPuny(label, now)]);
}

export type RoleView = { kind: string; description: string; server: string; implies: string[]; bindings: { app: string; tools: string[] }[] };

const strings = (v: unknown) => (Array.isArray(v) ? v.map(String) : []);

// roleOf reads a Role document of spec/objects, or null.
export function roleOf(text: string | undefined): RoleView | null {
  const doc = plainOf(text);
  if (!doc) return null;
  const spec = (doc.spec || {}) as Record<string, unknown>;
  const bindings = Array.isArray(spec.bindings) ? spec.bindings : [];
  return {
    kind: String(spec.kind || ""),
    description: String(spec.description || ""),
    server: String(spec.server || ""),
    implies: strings(spec.implies),
    bindings: bindings.map((b) => ({ app: String((b as Record<string, unknown>).app || ""), tools: strings((b as Record<string, unknown>).tools) })),
  };
}

export type ToolRow = { server: string; tool: string; before: boolean; after: boolean };

// roleToolRows lists every tool a role reaches before or after publishing,
// per server, so a tool that joins or leaves the role shows as a row.
export function roleToolRows(before: RoleView | null, after: RoleView | null): ToolRow[] {
  const reach = (r: RoleView | null) => new Map((r?.bindings || []).map((b) => [b.app, new Set(b.tools)]));
  const was = reach(before);
  const now = reach(after);
  const servers = uniq([...now.keys(), ...was.keys()]);
  const rows: ToolRow[] = [];
  for (const server of servers) {
    const tools = uniq([...(was.get(server) || []), ...(now.get(server) || [])]).sort();
    for (const tool of tools) rows.push({ server, tool, before: !!was.get(server)?.has(tool), after: !!now.get(server)?.has(tool) });
  }
  return rows;
}

export type RuleMark = "new" | "edited" | "removed" | "same";

// ruleMarks reads the rules of an approval set in the draft's order, each
// marked against the live text, then the live rules the draft drops.
export function ruleMarks(liveText: string | null, itemText: string): { rule: RuleView; mark: RuleMark }[] {
  const read = (text: string | null) => (text ? rulesOf(openDoc(text)) : []);
  const live = read(liveText);
  const next = read(itemText);
  const byID = new Map(live.map((r) => [r.id, JSON.stringify(r.raw)]));
  const out: { rule: RuleView; mark: RuleMark }[] = next.map((rule) => {
    const was = byID.get(rule.id);
    return { rule, mark: was === undefined ? "new" : was === JSON.stringify(rule.raw) ? "same" : "edited" };
  });
  const kept = new Set(next.map((r) => r.id));
  for (const rule of live) if (!kept.has(rule.id)) out.push({ rule, mark: "removed" });
  return out;
}

// objectOf is the Kind/Name an item's lines and live state are keyed by.
export const objectOf = (it: Pick<DraftItem, "kind" | "name">) => it.kind + "/" + it.name;

// impliedObjects are the live objects the draft does not name that its
// removals take along, as live state answers them.
export function impliedObjects(d: DraftDetail): string[] {
  const named = new Set(d.draft.items.map(objectOf));
  return Object.keys(d.live || {}).filter((k) => !named.has(k));
}

export type Side = { op: DraftOp; doc: string };
export type Sides = { before: Side | null; after: Side };

// sidesOf is the before and after of one item. A published draft reads its
// change record and never live state, which the publish itself moved, so an
// item the record does not name changed nothing. An open draft reads live
// state against the item. before is null for an object that did not exist.
export function sidesOf(d: DraftDetail, it: DraftItem): Sides {
  const doc = it.doc || "";
  if (d.draft.state === "published") {
    const row = (d.changes || []).find((c) => !c.implied && c.kind === it.kind && c.name === it.name);
    if (!row) return { before: { op: it.op, doc }, after: { op: it.op, doc } };
    return { before: row.before_op === "remove" ? null : { op: row.before_op, doc: row.before_doc }, after: { op: row.after_op, doc: row.after_doc } };
  }
  // existed is the draft's own reading of the object, which holds even for
  // an item whose live document this reader may not read.
  const live = (d.live || {})[objectOf(it)];
  return { before: it.existed ? { op: live ? live.op : "put", doc: (live && live.doc) || "" } : null, after: { op: it.op, doc } };
}

// impliedOf is what the removals took along: the implied rows of a
// published draft's record, else the live objects the draft does not name.
export function impliedOf(d: DraftDetail): string[] {
  if (d.draft.state === "published") return (d.changes || []).filter((c) => c.implied).map((c) => c.kind + "/" + c.name);
  return impliedObjects(d);
}

export type ChangeWhat = "new" | "changed" | "off" | "removed";
export type ChangeCount = { kind: DraftKind; what: ChangeWhat; n: number };

// whatOf says what an item does to its object.
export function whatOf(it: Pick<DraftItem, "op" | "existed">): ChangeWhat {
  if (it.op === "remove") return "removed";
  if (it.op === "off") return "off";
  return it.existed ? "changed" : "new";
}

const KINDS: DraftKind[] = ["App", "Role", "PolicySet"];
const WHATS: ChangeWhat[] = ["new", "changed", "off", "removed"];

// changeCounts counts the items by kind and by what they do, in a fixed
// order, for the lede of What changes and the publish dialog.
export function changeCounts(items: Pick<DraftItem, "kind" | "op" | "existed">[]): ChangeCount[] {
  const out: ChangeCount[] = [];
  for (const kind of KINDS) {
    for (const what of WHATS) {
      const n = items.filter((it) => it.kind === kind && whatOf(it) === what).length;
      if (n) out.push({ kind, what, n });
    }
  }
  return out;
}
