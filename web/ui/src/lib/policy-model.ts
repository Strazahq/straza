// The rules model of the Policies area: a stored PolicySet read
// into the views the screens render, and edited in place so the text keeps
// its comments. The yaml package's document model carries the comments
// across an edit; the server's validate stays the authority on validity,
// and this module only reads shapes and writes keys.
import { Document, Pair, YAMLMap, YAMLSeq, isMap, isScalar, isSeq, parseDocument } from "yaml";

export type Posture = "deny" | "hold" | "ticket" | "confirm" | "check" | "allow";
export type Bucket = "deny" | "hum" | "allow";
// Lane is the governed surface a rule matches: one of the four lanes, other
// for task.spawn and the like, tools when a rule names tools across lanes,
// all when it names no tool and no matcher (it governs every lane).
export type Lane = "mcp" | "shell" | "files" | "net" | "other" | "tools" | "all";
export type Plain = Record<string, unknown>;
export type Doc = Document.Parsed;

// Who is the decider pool of an approval rule: the requester's sponsor
// (the engine default when nothing is named), approver roles, or both.
export type Who = { sponsor: boolean; roles: string[] };

// RuleView is one rule as the cards, the hovers and the review read it.
export type RuleView = {
  id: string;
  index: number;
  lane: Lane;
  posture: Posture;
  bucket: Bucket;
  // app names the MCP servers of an mcp rule, joined; null means every
  // server.
  app: string | null;
  // names are the tools, patterns, paths or tool words the rule matches;
  // null means every one on the lane. namesKey is the YAML key they came
  // from, so a label can carry it.
  names: string[] | null;
  namesKey: string;
  reason: string;
  who: Who;
  timeoutSeconds: number;
  ticketTTLSeconds: number;
  grantTTLSeconds: number;
  notify: string[];
  // events are the explicit events; empty means the engine default, before
  // the tool runs.
  events: string[];
  // extra are the keys the cards cannot show, rendered as written.
  extra: string[];
  raw: Plain;
};

// SetView is the set as its page head and facts read it.
export type SetView = {
  name: string;
  description: string;
  priority: number;
  roles: string[];
  // matchOther says the set is also scoped by users or identity.
  matchOther: boolean;
  capture: "verbatim" | "redact" | null;
  escape: boolean;
  rules: RuleView[];
};

// HOLD_DEFAULT is the in-place window the wizard writes, the gateway cap.
// A hold that stores none holds for the engine's HOLD_UNSET, and any other
// rule reads HOLD_DEFAULT as the window a switch to hold starts from.
export const HOLD_DEFAULT = 120;
const HOLD_UNSET = 90;
export const TICKET_DEFAULT = 86400;
export const GRANT_DEFAULT = 3600;

export const LANE_OF_TOOL: Record<string, Lane> = {
  "mcp.call": "mcp", "shell.exec": "shell", "file.read": "files", "file.write": "files", "file.edit": "files", "net.fetch": "net", "task.spawn": "other", other: "other",
};
export const TOOL_WORD: Record<string, string> = {
  "shell.exec": "shell", "file.read": "file read", "file.write": "file write", "file.edit": "file edit", "net.fetch": "network fetch", "mcp.call": "MCP calls", "task.spawn": "subagents", other: "other tools",
};
// LANE_TOOLS is the tools key a lane's wizard writes.
export const LANE_TOOLS: Record<"mcp" | "shell" | "files" | "net", string[]> = {
  mcp: ["mcp.call"], shell: ["shell.exec"], files: ["file.write", "file.edit"], net: ["net.fetch"],
};

const RULE_KEYS = ["id", "events", "tools", "apps", "toolNames", "command", "paths", "effect", "mode", "approve", "reason"];
const APPROVE_KEYS = ["roles", "class", "timeoutSeconds", "retryTTLSeconds", "ticketTTLSeconds", "grantTTLSeconds", "deciders", "notify", "binding"];

const asPlain = (v: unknown): Plain => (v && typeof v === "object" && !Array.isArray(v) ? (v as Plain) : {});
const strings = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x) => typeof x === "string") as string[] : []);
const num = (v: unknown, d: number): number => (typeof v === "number" && Number.isFinite(v) ? v : d);

export const bucketOf = (p: Posture): Bucket => (p === "deny" ? "deny" : p === "allow" ? "allow" : "hum");

// postureOf folds a rule's effect and mode into one posture the way the
// server's summary does: mode wins over effect, approve splits by class.
export function postureOf(rule: Plain): Posture {
  const mode = rule.mode;
  if (mode === "confirm") return "confirm";
  if (mode === "serverCheck" || mode === "classify") return "check";
  if (mode === "approve") return asPlain(rule.approve).class === "ticket" ? "ticket" : "hold";
  return rule.effect === "deny" ? "deny" : "allow";
}

// laneOf classifies a rule the way the server's lane summary does: explicit
// tools map to their lane, a tool-less rule classifies by matcher shape,
// and a rule with neither governs every lane.
export function laneOf(rule: Plain): Lane {
  const tools = strings(rule.tools);
  if (tools.length) {
    const lanes = new Set(tools.map((t) => LANE_OF_TOOL[t] || "other"));
    return lanes.size === 1 ? [...lanes][0] : "tools";
  }
  if (rule.apps !== undefined || rule.toolNames !== undefined) return "mcp";
  if (rule.command !== undefined || rule.interpreters !== undefined) return "shell";
  if (rule.paths !== undefined) return "files";
  return "all";
}

// namesOf reads what the rule matches on its lane, with the key it read.
export function namesOf(rule: Plain, lane: Lane): { names: string[] | null; key: string } {
  const pick = (map: Plain, prefix: string, first: string, second: string) => {
    if (strings(map[first]).length) return { names: strings(map[first]), key: prefix + "." + first };
    if (strings(map[second]).length) return { names: strings(map[second]), key: prefix + "." + second };
    return { names: null, key: prefix };
  };
  if (lane === "mcp") return pick(asPlain(rule.toolNames), "toolNames", "allow", "deny");
  if (lane === "shell") return pick(asPlain(rule.command), "command", "allowPatterns", "denyPatterns");
  if (lane === "files") return pick(asPlain(rule.paths), "paths", "allow", "deny");
  if (lane === "tools") return { names: strings(rule.tools).map((t) => TOOL_WORD[t] || t), key: "tools" };
  return { names: null, key: "tools" };
}

// whoOf reads the decider pool: nothing named is the sponsor default.
export function whoOf(approve: Plain): Who {
  const roles = strings(approve.roles);
  const deciders = strings(approve.deciders);
  const sponsor = deciders.includes("sponsor") || (roles.length === 0 && deciders.length === 0 && approve.selfApproval !== true);
  return { sponsor, roles };
}

// ruleView reads one plain rule into its view.
export function ruleView(rule: Plain, index: number): RuleView {
  const lane = laneOf(rule);
  const approve = asPlain(rule.approve);
  const { names, key } = namesOf(rule, lane);
  const apps = strings(rule.apps);
  const extra = Object.keys(rule).filter((k) => !RULE_KEYS.includes(k)).concat(Object.keys(approve).filter((k) => !APPROVE_KEYS.includes(k)).map((k) => "approve." + k));
  const posture = postureOf(rule);
  return {
    id: typeof rule.id === "string" ? rule.id : "",
    index,
    lane,
    posture,
    bucket: bucketOf(posture),
    app: lane === "mcp" ? (apps.length ? apps.join(", ") : null) : null,
    names,
    namesKey: key,
    reason: typeof rule.reason === "string" ? rule.reason : "",
    who: whoOf(approve),
    timeoutSeconds: num(approve.timeoutSeconds, posture === "hold" ? HOLD_UNSET : HOLD_DEFAULT),
    ticketTTLSeconds: num(approve.ticketTTLSeconds, TICKET_DEFAULT),
    grantTTLSeconds: num(approve.grantTTLSeconds, GRANT_DEFAULT),
    notify: strings(approve.notify),
    events: strings(rule.events),
    extra,
    raw: rule,
  };
}

// openDoc parses the stored text into the document the edits work on.
export function openDoc(text: string): Doc {
  return parseDocument(text, { prettyErrors: true });
}

// docProblems words the parse errors, one per line; empty when the text
// parses.
export function docProblems(doc: Doc): string[] {
  return doc.errors.map((e) => e.message.replace(/\s+/g, " ").trim());
}

// docText renders the document back to text, comments kept and long lists
// left on one line.
export function docText(doc: Doc): string {
  return doc.toString({ lineWidth: 0, flowCollectionPadding: false });
}

function rulesSeq(doc: Doc): YAMLSeq | null {
  const r = doc.getIn(["spec", "rules"]);
  return isSeq(r) ? r : null;
}

const keyOf = (p: Pair): string => (isScalar(p.key) ? String(p.key.value) : String(p.key));

function ruleNode(doc: Doc, id: string): YAMLMap {
  const seq = rulesSeq(doc);
  const node = seq ? seq.items.find((it) => isMap(it) && it.get("id") === id) : undefined;
  if (!node || !isMap(node)) throw new Error("no rule " + id + " in this policy");
  return node;
}

// setKey replaces a key's value in place, or inserts the key before the
// first of the keys named in before, or appends it, so an edited rule keeps
// the order a hand-written one has.
function setKey(doc: Doc, map: YAMLMap, key: string, value: unknown, before: string[] = []) {
  const node = doc.createNode(value);
  flowLists(node);
  const at = map.items.findIndex((p) => keyOf(p) === key);
  if (at >= 0) {
    map.items[at].value = node;
    return;
  }
  const pair = new Pair(doc.createNode(key), node);
  const anchor = map.items.findIndex((p) => before.includes(keyOf(p)));
  if (anchor >= 0) map.items.splice(anchor, 0, pair);
  else map.items.push(pair);
}

// flowLists puts every list of scalars on one line, the way the spec
// examples and the seed write them.
function flowLists(node: unknown) {
  if (isSeq(node)) {
    if (node.items.every((it) => isScalar(it))) node.flow = true;
    node.items.forEach(flowLists);
  } else if (isMap(node)) {
    node.items.forEach((p) => flowLists(p.value));
  }
}

// rulesOf reads every rule of the document, in order.
export function rulesOf(doc: Doc): RuleView[] {
  const seq = rulesSeq(doc);
  if (!seq) return [];
  return seq.items.map((it, i) => ruleView(isMap(it) ? asPlain(it.toJSON()) : {}, i));
}

// readSet reads the set's own facts and its rules.
export function readSet(doc: Doc): SetView {
  const root = asPlain(doc.toJS());
  const metadata = asPlain(root.metadata);
  const spec = asPlain(root.spec);
  const match = asPlain(spec.match);
  const capture = asPlain(spec.capture);
  return {
    name: typeof metadata.name === "string" ? metadata.name : "",
    description: typeof metadata.description === "string" ? metadata.description : "",
    priority: num(spec.priority, 0),
    roles: strings(match.roles),
    matchOther: match.users !== undefined || match.identity !== undefined,
    capture: capture.conversations === true ? (capture.mode === "redact" ? "redact" : "verbatim") : null,
    escape: spec.escape !== undefined,
    rules: rulesOf(doc),
  };
}

export type ApproveEdit = { who?: Who; timeoutSeconds?: number; ticketTTLSeconds?: number; grantTTLSeconds?: number };

function approveMap(doc: Doc, rule: YAMLMap): YAMLMap {
  const existing = rule.get("approve", true);
  if (isMap(existing)) return existing;
  setKey(doc, rule, "approve", {}, ["reason"]);
  const made = rule.get("approve", true);
  if (!isMap(made)) throw new Error("the approve block could not be written");
  return made;
}

function setWho(doc: Doc, approve: YAMLMap, who: Who) {
  if (who.roles.length) setKey(doc, approve, "roles", who.roles, ["class", "timeoutSeconds", "retryTTLSeconds", "ticketTTLSeconds", "grantTTLSeconds", "binding", "notify"]);
  else approve.delete("roles");
  // The sponsor alone is the engine default; it is still written, so the
  // text reads as the rule means.
  if (who.sponsor) setKey(doc, approve, "deciders", ["sponsor"], ["notify"]);
  else approve.delete("deciders");
}

// SIDES are the matcher maps with an allow side and a deny side. A denial
// names what it refuses on the deny side; an allow or an approval names
// what it admits on the allow side, and the server refuses a rule whose
// only side is the wrong one for its effect.
const SIDES: [string, string, string][] = [["toolNames", "allow", "deny"], ["command", "allowPatterns", "denyPatterns"], ["paths", "allow", "deny"]];

// renameKey moves a value under a new key in place, so the comments around
// it and its position stay.
function renameKey(doc: Doc, map: YAMLMap, from: string, to: string) {
  const pair = map.items.find((p) => keyOf(p) === from);
  if (pair) pair.key = doc.createNode(to);
}

// swapSides moves a rule's matchers to the side its new effect reads: the
// deny side when it becomes a denial, the allow side otherwise. A map that
// already carries both sides is left as written, since an allow list with
// deny exceptions is a legal shape.
function swapSides(doc: Doc, rule: YAMLMap, toDeny: boolean) {
  for (const [outer, allow, deny] of SIDES) {
    const map = rule.get(outer, true);
    if (!isMap(map)) continue;
    const [from, to] = toDeny ? [allow, deny] : [deny, allow];
    if (map.has(from) && !map.has(to)) renameKey(doc, map, from, to);
  }
}

// switchClass makes an approval block a hold or a ticket. The other
// class's knobs go, because validation refuses timeoutSeconds and
// retryTTLSeconds under a ticket and a hold ignores the ticket's.
function switchClass(doc: Doc, approve: YAMLMap, how: "hold" | "ticket") {
  if (how === "hold") for (const k of ["class", "ticketTTLSeconds", "grantTTLSeconds", "bind"]) approve.delete(k);
  else {
    setKey(doc, approve, "class", "ticket", ["timeoutSeconds", "retryTTLSeconds", "ticketTTLSeconds", "grantTTLSeconds", "binding", "notify"]);
    for (const k of ["timeoutSeconds", "retryTTLSeconds"]) approve.delete(k);
  }
}

// setPosture changes what a rule does. deny and allow drop the approval
// block; hold and ticket write mode approve with the pool and the window
// given, and keep the other approval keys as they were. The matchers move
// to the side the new effect reads.
export function setPosture(doc: Doc, id: string, posture: "deny" | "allow" | "hold" | "ticket", edit: ApproveEdit = {}) {
  const rule = ruleNode(doc, id);
  swapSides(doc, rule, posture === "deny");
  if (posture === "deny" || posture === "allow") {
    setKey(doc, rule, "effect", posture, ["mode", "approve", "reason"]);
    rule.delete("mode");
    rule.delete("approve");
    return;
  }
  setKey(doc, rule, "effect", "allow", ["mode", "approve", "reason"]);
  setKey(doc, rule, "mode", "approve", ["approve", "reason"]);
  const approve = approveMap(doc, rule);
  switchClass(doc, approve, posture);
  if (posture === "hold") {
    if (edit.timeoutSeconds !== undefined) setKey(doc, approve, "timeoutSeconds", edit.timeoutSeconds, ["retryTTLSeconds", "binding", "notify"]);
  } else {
    if (edit.ticketTTLSeconds !== undefined) setKey(doc, approve, "ticketTTLSeconds", edit.ticketTTLSeconds, ["grantTTLSeconds", "binding", "notify"]);
    if (edit.grantTTLSeconds !== undefined) setKey(doc, approve, "grantTTLSeconds", edit.grantTTLSeconds, ["binding", "notify"]);
  }
  if (edit.who) setWho(doc, approve, edit.who);
}

// setClass turns a rule that approves into a hold or a ticket in place, so
// its pool, binding, notify, reason and comments stay. The caller writes
// the new class's windows with setApprove.
export function setClass(doc: Doc, id: string, how: "hold" | "ticket") {
  switchClass(doc, approveMap(doc, ruleNode(doc, id)), how);
}

// setApprove changes the pool or a window of a rule that already holds or
// tickets, without touching its class.
export function setApprove(doc: Doc, id: string, edit: ApproveEdit) {
  const rule = ruleNode(doc, id);
  const approve = approveMap(doc, rule);
  if (edit.who) setWho(doc, approve, edit.who);
  if (edit.timeoutSeconds !== undefined) setKey(doc, approve, "timeoutSeconds", edit.timeoutSeconds, ["retryTTLSeconds", "binding", "notify"]);
  if (edit.ticketTTLSeconds !== undefined) setKey(doc, approve, "ticketTTLSeconds", edit.ticketTTLSeconds, ["grantTTLSeconds", "binding", "notify"]);
  if (edit.grantTTLSeconds !== undefined) setKey(doc, approve, "grantTTLSeconds", edit.grantTTLSeconds, ["binding", "notify"]);
}

// setReason writes the reason the agent reads; an empty one drops the key.
export function setReason(doc: Doc, id: string, reason: string) {
  const rule = ruleNode(doc, id);
  if (reason.trim()) setKey(doc, rule, "reason", reason);
  else rule.delete("reason");
}

// setNames writes what the rule matches on its lane, under the key it
// already uses; null means every one, which drops the key.
export function setNames(doc: Doc, id: string, names: string[] | null) {
  const rule = ruleNode(doc, id);
  const view = ruleView(asPlain(rule.toJSON()), 0);
  const [outer, inner] = view.namesKey.split(".");
  if (view.lane === "tools" || view.lane === "all" || view.lane === "net" || view.lane === "other") throw new Error("this rule matches by tool, not by name");
  const defaults: Record<string, string> = { toolNames: "allow", command: view.posture === "deny" ? "denyPatterns" : "allowPatterns", paths: view.posture === "deny" ? "deny" : "allow" };
  const key = inner || defaults[outer];
  if (!names || names.length === 0) {
    const map = rule.get(outer, true);
    if (isMap(map)) map.delete(key);
    if (isMap(map) && map.items.length === 0) rule.delete(outer);
    return;
  }
  let map = rule.get(outer, true);
  if (!isMap(map)) {
    setKey(doc, rule, outer, {}, ["effect", "mode", "approve", "reason"]);
    map = rule.get(outer, true);
  }
  if (!isMap(map)) throw new Error("the " + outer + " block could not be written");
  setKey(doc, map, key, names);
}

// setEvents writes the explicit events; an empty list means the engine
// default and drops the key.
export function setEvents(doc: Doc, id: string, events: string[]) {
  const rule = ruleNode(doc, id);
  if (events.length) setKey(doc, rule, "events", events, ["tools", "apps", "toolNames", "command", "paths", "effect", "mode", "approve", "reason"]);
  else rule.delete("events");
}

// removeRule drops a rule with its comments.
export function removeRule(doc: Doc, id: string) {
  const seq = rulesSeq(doc);
  if (!seq) return;
  const at = seq.items.findIndex((it) => isMap(it) && it.get("id") === id);
  if (at >= 0) seq.items.splice(at, 1);
}

// moveRule moves a rule with its comments to position at, counted once
// the rule is taken out of the list.
export function moveRule(doc: Doc, id: string, at: number) {
  const seq = rulesSeq(doc);
  const from = seq ? seq.items.findIndex((it) => isMap(it) && it.get("id") === id) : -1;
  if (!seq || from < 0) throw new Error("no rule " + id + " in this policy");
  const [node] = seq.items.splice(from, 1);
  seq.items.splice(Math.max(0, Math.min(at, seq.items.length)), 0, node);
}

// addRule appends a rule; the id must not already exist.
export function addRule(doc: Doc, rule: Plain) {
  const seq = rulesSeq(doc);
  const id = typeof rule.id === "string" ? rule.id : "";
  if (seq && seq.items.some((it) => isMap(it) && it.get("id") === id)) throw new Error("a rule named " + id + " exists already");
  const node = doc.createNode(rule);
  flowLists(node);
  if (seq) seq.items.push(node);
  else doc.setIn(["spec", "rules"], doc.createNode([node]));
}

// setMatchRoles writes the roles the set applies to; an empty list makes
// the set apply to everyone.
export function setMatchRoles(doc: Doc, roles: string[]) {
  if (roles.length) {
    const node = doc.createNode(roles);
    flowLists(node);
    doc.setIn(["spec", "match", "roles"], node);
    return;
  }
  doc.deleteIn(["spec", "match", "roles"]);
  const match = doc.getIn(["spec", "match"]);
  if (isMap(match) && match.items.length === 0) doc.deleteIn(["spec", "match"]);
}

// setCapture turns recording on with its mode, or off.
export function setCapture(doc: Doc, mode: "verbatim" | "redact" | null) {
  if (!mode) {
    doc.deleteIn(["spec", "capture"]);
    return;
  }
  doc.setIn(["spec", "capture", "conversations"], true);
  doc.setIn(["spec", "capture", "mode"], mode);
}

export function setPriority(doc: Doc, priority: number) {
  doc.setIn(["spec", "priority"], priority);
}

export function setDescription(doc: Doc, description: string) {
  if (description.trim()) doc.setIn(["metadata", "description"], description);
  else doc.deleteIn(["metadata", "description"]);
}

// slug makes a name or a rule id from free text, the grammar the server
// accepts: lower case, digits and dashes, at most 64 characters.
export function slug(text: string): string {
  return text.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64).replace(/-+$/g, "");
}

// uniqueId keeps an id clear of the ids taken.
export function uniqueId(base: string, taken: string[]): string {
  let id = base;
  for (let n = 2; taken.includes(id); n++) id = base + "-" + n;
  return id;
}

export type NewRule = {
  id: string;
  lane: "mcp" | "shell" | "files" | "net";
  posture: "deny" | "allow" | "hold" | "ticket";
  app?: string;
  // names are the tools, patterns or paths; empty means every one.
  names?: string[];
  who?: Who;
  timeoutSeconds?: number;
  ticketTTLSeconds?: number;
  grantTTLSeconds?: number;
  reason?: string;
};

// buildRule renders one wizard answer as a plain rule in the key order a
// hand-written rule has.
export function buildRule(r: NewRule): Plain {
  const out: Plain = { id: r.id, tools: LANE_TOOLS[r.lane] };
  const names = r.names && r.names.length ? r.names : null;
  if (r.lane === "mcp") {
    if (r.app) out.apps = [r.app];
    if (names) out.toolNames = r.posture === "deny" ? { deny: names } : { allow: names };
  } else if (r.lane === "shell" && names) {
    out.command = r.posture === "deny" ? { denyPatterns: names } : { allowPatterns: names };
  } else if (r.lane === "files" && names) {
    out.paths = r.posture === "deny" ? { deny: names } : { allow: names };
  }
  out.effect = r.posture === "deny" ? "deny" : "allow";
  if (r.posture === "hold" || r.posture === "ticket") {
    out.mode = "approve";
    const approve: Plain = {};
    const who = r.who || { sponsor: true, roles: [] };
    if (who.roles.length) approve.roles = who.roles;
    if (who.sponsor) approve.deciders = ["sponsor"];
    if (r.posture === "ticket") {
      approve.class = "ticket";
      if (r.ticketTTLSeconds !== undefined) approve.ticketTTLSeconds = r.ticketTTLSeconds;
      if (r.grantTTLSeconds !== undefined) approve.grantTTLSeconds = r.grantTTLSeconds;
    } else if (r.timeoutSeconds !== undefined) approve.timeoutSeconds = r.timeoutSeconds;
    out.approve = approve;
  }
  if (r.reason) out.reason = r.reason;
  return out;
}

export type NewPolicy = { name: string; description?: string; priority?: number; roles?: string[]; capture?: "verbatim" | "redact" | null; rules: Plain[] };

// newPolicyDoc renders a policy the wizard authored, lists on one line and
// no comments.
export function newPolicyDoc(p: NewPolicy): Doc {
  const spec: Plain = {};
  if (p.priority !== undefined) spec.priority = p.priority;
  if (p.roles && p.roles.length) spec.match = { roles: p.roles };
  if (p.capture) spec.capture = { conversations: true, mode: p.capture };
  spec.rules = p.rules;
  const metadata: Plain = { name: p.name };
  if (p.description) metadata.description = p.description;
  const doc = new Document({ apiVersion: "straza.dev/v1beta1", kind: "PolicySet", metadata, spec }) as Doc;
  flowLists(doc.contents);
  return doc;
}

// Postures counts the rules of a set by bucket, from the server's summary
// or from the views, with the human bucket split for the hover.
export type Postures = { deny: number; hum: number; allow: number; hold: number; ticket: number; confirm: number; check: number };

export function posturesOf(summary: Record<string, number> | undefined): Postures {
  const p = summary || {};
  const n = (k: string) => p[k] || 0;
  return {
    deny: n("deny"),
    hum: n("hold") + n("ticket") + n("confirm") + n("serverCheck") + n("classify"),
    allow: n("allow"),
    hold: n("hold"),
    ticket: n("ticket"),
    confirm: n("confirm"),
    check: n("serverCheck") + n("classify"),
  };
}

export function posturesOfRules(rules: RuleView[]): Postures {
  const out: Postures = { deny: 0, hum: 0, allow: 0, hold: 0, ticket: 0, confirm: 0, check: 0 };
  for (const r of rules) {
    out[r.bucket]++;
    if (r.posture === "hold" || r.posture === "ticket" || r.posture === "confirm" || r.posture === "check") out[r.posture]++;
  }
  return out;
}

// addPostures sums two counts, for a role's row over its live sets.
export function addPostures(a: Postures, b: Postures): Postures {
  return { deny: a.deny + b.deny, hum: a.hum + b.hum, allow: a.allow + b.allow, hold: a.hold + b.hold, ticket: a.ticket + b.ticket, confirm: a.confirm + b.confirm, check: a.check + b.check };
}

export const NO_POSTURES: Postures = { deny: 0, hum: 0, allow: 0, hold: 0, ticket: 0, confirm: 0, check: 0 };

// splitRule moves one name out of a rule into a new rule of its own: the
// original keeps its other names and its comments, the new rule copies
// every other key and lands at the end of the list under newId.
export function splitRule(doc: Doc, id: string, name: string, newId: string) {
  const rule = ruleNode(doc, id);
  const view = ruleView(asPlain(rule.toJSON()), 0);
  if (!view.names || !view.names.includes(name)) throw new Error("rule " + id + " does not name " + name);
  if (view.names.length < 2) throw new Error("rule " + id + " names only " + name);
  const [outer, inner] = view.namesKey.split(".");
  const copy = asPlain(JSON.parse(JSON.stringify(rule.toJSON())));
  copy.id = newId;
  const map = asPlain(copy[outer]);
  map[inner] = [name];
  copy[outer] = map;
  setNames(doc, id, view.names.filter((n) => n !== name));
  addRule(doc, copy);
}

// proposeRuleId names a new rule from what it does and matches, clear of
// the ids taken: approve-demo-tools-get-env, deny-shell-rm-rf.
export function proposeRuleId(posture: "deny" | "allow" | "hold" | "ticket", lane: Lane, app: string | null, names: string[], taken: string[]): string {
  const verb = posture === "deny" ? "deny" : posture === "allow" ? "allow" : "approve";
  const where = lane === "mcp" ? (app || "mcp") : lane;
  const first = names.length ? names[0] : "";
  return uniqueId(slug([verb, where, first].filter(Boolean).join("-")) || verb, taken);
}

export type Gap = "where" | "calls" | "what" | "reason";

// ruleGaps says what a rule still lacks before it can join the page: where
// the call goes, at least one call, what happens, and a reason for anything
// but an allow.
export function ruleGaps(r: { lane: Lane | null; names: string[] | null; posture: Posture | null; reason: string }): Gap[] {
  const gaps: Gap[] = [];
  if (!r.lane) gaps.push("where");
  if (r.lane && r.lane !== "net" && (!r.names || r.names.length === 0)) gaps.push("calls");
  if (!r.posture) gaps.push("what");
  if (r.posture && r.posture !== "allow" && !r.reason.trim()) gaps.push("reason");
  return gaps;
}
