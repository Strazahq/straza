// The plan behind the access editor: which tools of one server a role
// reaches, what happens when a session calls them, and who approves a call
// that needs it. The editor draws a plan, the commit writes it, and the New
// role wizard, the role page and the server page hold the same shape, so the
// model lives apart from all of them.
//
// The model it keeps: the access row stores tool names, every name the
// server has today, or a glob that also reaches tools it gains later. The
// rules live in the role's own policy set. "Require approval for every
// call" is one rule with no tool list, so it holds tools added later too.
// The grammar has no exception to an approve rule, so under it a tool can be
// denied or given its own approval, never allowed without one.
import type { BindingRow, PreviewEntry } from "./api";
import { GRANT_DEFAULT, HOLD_DEFAULT, type Plain, TICKET_DEFAULT, docProblems, docText, newPolicyDoc, openDoc, readSet, ruleView, rulesOf, slug, uniqueId } from "./policy-model";
import {
  EVERY_RULE_FIRST,
  GRANT_MAX,
  HOLD_MAX,
  POLICY_WORD,
  type SentenceParts,
  TICKET_MAX,
  approveGroupReason,
  denyGroupReason,
  everyCallReason,
  heldWord,
  isGlob,
  planSentenceParts,
  shapeProblemWords,
  shapeWords,
  shortShapeWords,
} from "./role-words";
import { matchesTool, runWord } from "./words";

// Reach is what the access row stores: the ticked names, every name the
// server has today, or the glob that also reaches the tools it gains later.
export type Reach = "tick" | "today" | "later";

// OnCall is the server-wide answer to what a call does: allowed, every call
// held for a person, or chosen tool by tool.
export type OnCall = "allow" | "every" | "per";

// Choice is what a rule does on a call to one tool.
export type Choice = "allow" | "approve" | "deny";

// Shape is one approval setting. pool is SPONSOR or an approver role's name.
// A hold keeps the call waiting up to hold seconds; a ticket refuses the call
// now, a person grants it within ticket seconds, and the same call within
// grant seconds runs once. The three windows are all kept so a switch
// between hold and ticket never loses what was typed.
export type Shape = { pool: string; how: "hold" | "ticket"; hold: number; ticket: number; grant: number };

// Plan is the draft of one role's access to one server. choice is read
// under "per", and its deny entries also under "every". own holds the tools
// that carry their own approval setting instead of the server's.
export type Plan = {
  reach: Reach;
  picked: Record<string, boolean>;
  call: OnCall;
  choice: Record<string, Choice>;
  shape: Shape;
  own: Record<string, Shape>;
};

// FixedRule is a rule the editor shows but does not change: one in another
// set, or one in the role's own set the editor cannot say. word is the
// policy word the row shows, for example "needs approval: ticket, a day".
// status is the catalog preview's word for what the rule does: visible for
// a plain allow or a check, which only a rule of the role's own set gives.
export type FixedRule = { set: string; ruleId?: string; word: string; status: "approve_gated" | "hidden_policy" | "visible" };

// OwnRead is what the role's own set says about one server: the parts of a
// plan its rules express, the ids of those rules (the ones a save replaces),
// and the rules the editor leaves alone.
export type OwnRead = {
  plan: Pick<Plan, "call" | "choice" | "shape" | "own">;
  owned: string[];
  fixed: Record<string, FixedRule>;
};

// SPONSOR is the default decider: the person behind the agent, which the
// policy engine spells as a decider rather than a role.
export const SPONSOR = "sponsor";

export const defaultShape = (): Shape => ({ pool: SPONSOR, how: "hold", hold: HOLD_DEFAULT, ticket: TICKET_DEFAULT, grant: GRANT_DEFAULT });

export const emptyPlan = (): Plan => ({ reach: "tick", picked: {}, call: "allow", choice: {}, shape: defaultShape(), own: {} });

// inGrant says whether the access row admits the tool.
export const inGrant = (plan: Plan, tool: string): boolean => plan.reach !== "tick" || !!plan.picked[tool];

// denyOffered says whether deny is a choice: only under the glob, because a
// tool left out of a name list is already out of reach.
export const denyOffered = (plan: Plan): boolean => plan.reach === "later";

// allowOffered says whether a tool can be allowed without approval: not
// under "every", whose one rule has no exception in the grammar.
export const allowOffered = (plan: Plan): boolean => plan.call !== "every";

// choiceOf is a tool's effective choice, null for a tool out of reach. A
// deny under a name list only comes from a rule read back, since leaving
// the glob drops the deny choices, and it still reads as denied: the rule
// denies the call, and a save keeps it until someone picks otherwise.
export function choiceOf(plan: Plan, tool: string): Choice | null {
  if (!inGrant(plan, tool)) return null;
  const c = plan.choice[tool];
  if (plan.call === "allow") return "allow";
  if (plan.call === "every") return c === "deny" ? "deny" : "approve";
  return c || "allow";
}

// shapeOf is the approval setting a held tool uses: its own, or the server's.
export const shapeOf = (plan: Plan, tool: string): Shape => plan.own[tool] || plan.shape;

// shapeKey names what a setting writes: the pool and the windows of its
// class, so two settings that write the same approve block share a key.
const shapeKey = (s: Shape) => JSON.stringify([s.pool, s.how, s.how === "hold" ? s.hold : 0, s.how === "ticket" ? s.ticket : 0, s.how === "ticket" ? s.grant : 0]);

// sharesServer says whether a held tool uses the server's setting: its
// own, when it writes the same approve block, shares the server's rule.
const sharesServer = (plan: Pick<Plan, "shape">, s: Shape) => shapeKey(s) === shapeKey(plan.shape);

// grantMatchers is what the access row stores, in name order.
export function grantMatchers(plan: Plan, tools: string[]): string[] {
  if (plan.reach === "later") return ["*"];
  const names = plan.reach === "today" ? tools.slice() : tools.filter((t) => plan.picked[t]);
  return names.sort();
}

// heldTools lists the tools a call to which needs approval, each with the
// setting it uses.
export const heldTools = (plan: Plan, tools: string[]): { tool: string; shape: Shape }[] =>
  tools.filter((t) => choiceOf(plan, t) === "approve").map((t) => ({ tool: t, shape: shapeOf(plan, t) }));

// deniedTools lists the tools a rule refuses.
export const deniedTools = (plan: Plan, tools: string[]): string[] => tools.filter((t) => choiceOf(plan, t) === "deny");

// newToolsRun says whether a tool the server gains later is reached and runs
// with no approval: the glob with per-tool choices.
export const newToolsRun = (plan: Plan): boolean => plan.reach === "later" && plan.call === "per";

// withReach, withCall and withChoice are the plan's transitions, the same
// on every door. Leaving the glob drops the deny choices it offered; picking
// a held or denied tool under "allow" moves the server to "per"; a tool that
// stops being held loses its own setting.
export function withReach(plan: Plan, reach: Reach): Plan {
  const choice = { ...plan.choice };
  if (reach !== "later") for (const t of Object.keys(choice)) if (choice[t] === "deny") delete choice[t];
  return { ...plan, reach, choice };
}

// narrowed is the tick that leaves the glob: the plan becomes ticks of
// every tool but the one clicked off, so the row names its tools.
export function narrowed(plan: Plan, tools: string[], tool: string): Plan {
  const picked: Record<string, boolean> = {};
  for (const t of tools) if (t !== tool) picked[t] = true;
  return { ...withReach(plan, "tick"), picked };
}

export const withCall = (plan: Plan, call: OnCall): Plan => ({ ...plan, call });

export function withChoice(plan: Plan, tool: string, c: Choice): Plan {
  const own = { ...plan.own };
  if (c !== "approve") delete own[tool];
  const call = plan.call === "allow" && c !== "allow" ? "per" : plan.call;
  return { ...plan, call, choice: { ...plan.choice, [tool]: c }, own };
}

// withOwn gives a tool its own approval setting, seeded from the server's,
// or takes it away when shape is null.
export function withOwn(plan: Plan, tool: string, shape: Shape | null): Plan {
  const own = { ...plan.own };
  if (shape) own[tool] = shape;
  else delete own[tool];
  return { ...plan, own };
}

// shapeProblems names what the server would refuse in a setting, one
// sentence each, empty when it is legal. The bounds are the policyset's.
export function shapeProblems(s: Shape): string[] {
  const out: string[] = [];
  if (s.how === "hold" && (s.hold < 1 || s.hold > HOLD_MAX)) out.push(shapeProblemWords.hold);
  if (s.how === "ticket" && (s.ticket < 1 || s.ticket > TICKET_MAX)) out.push(shapeProblemWords.ticket);
  if (s.how === "ticket" && (s.grant < 1 || s.grant > GRANT_MAX)) out.push(shapeProblemWords.grant);
  return out;
}

// planProblems is every setting's problems for the tools the plan holds.
export function planProblems(plan: Plan, tools: string[]): string[] {
  const out = plan.call === "every" || heldTools(plan, tools).some((h) => h.shape === plan.shape) ? shapeProblems(plan.shape) : [];
  for (const h of heldTools(plan, tools)) if (h.shape !== plan.shape) for (const p of shapeProblems(h.shape)) if (!out.includes(p)) out.push(p);
  return out;
}

// planFor seeds the editor from a stored access row and what the role's
// own set says about this server. A glob reads as "later", a row naming
// every tool the server has today reads as "today", and a prefix pattern
// ticks the names it matches today, which is what saving replaces it with.
export function planFor(binding: BindingRow | null, tools: string[], own?: OwnRead | null): Plan {
  const plan = emptyPlan();
  if (binding) {
    if (isGlob(binding.tools)) plan.reach = "later";
    else {
      for (const t of tools) if (matchesTool(binding.tools, t)) plan.picked[t] = true;
      if (tools.length && tools.every((t) => plan.picked[t])) plan.reach = "today";
    }
  }
  if (own) Object.assign(plan, own.plan);
  return plan;
}

// OwnRule is one rule of the role's own set in a shape this editor writes
// for one server: every (approval with no tool list), group (approval for
// the tools it names) or deny (the tools it names are denied). tools are
// the names as written, a tool the server no longer offers included; shape
// is null for a deny.
export type OwnRule = { id: string; kind: "every" | "group" | "deny"; tools: string[]; shape: Shape | null };

// The keys an owned rule and its approve block may carry. The save keeps
// the ones the editor does not show, such as notify, binding or a reason.
const OWN_RULE_KEYS = ["id", "events", "tools", "apps", "toolNames", "effect", "mode", "approve", "reason"];
const OWN_APPROVE_KEYS = ["deciders", "roles", "class", "timeoutSeconds", "retryTTLSeconds", "ticketTTLSeconds", "grantTTLSeconds", "bind", "binding", "notify", "selfApproval"];

const asMap = (v: unknown): Plain => (v && typeof v === "object" && !Array.isArray(v) ? (v as Plain) : {});
const texts = (v: unknown): string[] | null => (Array.isArray(v) && v.every((x) => typeof x === "string") ? (v as string[]) : null);
const sameList = (v: unknown, want: string[]) => {
  const got = texts(v);
  return !!got && got.length === want.length && got.every((x, i) => x === want[i]);
};
// A name list holds tool names, not the patterns of spec/policyset section 4.
const names = (v: unknown): string[] | null => {
  const got = texts(v);
  return got && got.length && got.every((n) => !/[*?]/.test(n) && !n.startsWith("re:")) ? got : null;
};

// poolOf reads a decider pool the editor can say: the sponsor, named or by
// the engine's default, or one approver role. null for any other pool.
function poolOf(approve: Plain): string | null {
  if (approve.selfApproval !== undefined && approve.selfApproval !== false) return null;
  const roles = approve.roles === undefined ? [] : texts(approve.roles);
  const deciders = approve.deciders === undefined ? [] : texts(approve.deciders);
  if (!roles || !deciders) return null;
  if (!roles.length && (!deciders.length || sameList(deciders, [SPONSOR]))) return SPONSOR;
  return roles.length === 1 && !deciders.length ? roles[0] : null;
}

// ownRuleOf reads one rule as this editor's own rule for the server app,
// or null when it is a rule the editor cannot say.
export function ownRuleOf(rule: Plain, app: string): OwnRule | null {
  if (typeof rule.id !== "string" || Object.keys(rule).some((k) => !OWN_RULE_KEYS.includes(k))) return null;
  if (rule.events !== undefined && !sameList(rule.events, ["tool.pre"])) return null;
  if (!sameList(rule.tools, ["mcp.call"]) || !sameList(rule.apps, [app])) return null;
  const matched = asMap(rule.toolNames);
  const sides = Object.keys(matched);
  if (rule.effect === "deny") {
    const denied = names(matched.deny);
    if (rule.mode !== undefined || rule.approve !== undefined || !denied || sides.length !== 1) return null;
    return { id: rule.id, kind: "deny", tools: denied, shape: null };
  }
  const approve = asMap(rule.approve);
  const pool = poolOf(approve);
  const cls = approve.class;
  if (rule.approve !== undefined && rule.approve !== approve) return null;
  if (rule.effect !== "allow" || rule.mode !== "approve" || pool === null || (cls !== undefined && cls !== "hold" && cls !== "ticket")) return null;
  if (Object.keys(approve).some((k) => !OWN_APPROVE_KEYS.includes(k))) return null;
  const view = ruleView(rule, 0);
  const shape: Shape = { pool, how: cls === "ticket" ? "ticket" : "hold", hold: view.timeoutSeconds, ticket: view.ticketTTLSeconds, grant: view.grantTTLSeconds };
  if (rule.toolNames === undefined) return { id: rule.id, kind: "every", tools: [], shape };
  const allowed = names(matched.allow);
  return allowed && sides.length === 1 ? { id: rule.id, kind: "group", tools: allowed, shape } : null;
}

// matches is the pattern test of spec/policyset section 4 on a tool name:
// a glob where * is any run and ? one character, or a regular expression
// after re:, both anchored.
function matches(patterns: unknown, name: string): boolean {
  return (texts(patterns) || []).some((p) => {
    const source = p.startsWith("re:") ? p.slice(3) : p.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".");
    try {
      return new RegExp("^(?:" + source + ")$").test(name);
    } catch {
      return false;
    }
  });
}

// ruleTools lists the server's tools a rule applies to on a call, by
// spec/policyset section 2: its events, tools and apps admit an MCP call to
// app, and its toolNames name the tool. A rule whose only matchers are for
// commands or paths never fires on an MCP call.
function ruleTools(rule: Plain, app: string, tools: string[]): string[] {
  const events = texts(rule.events);
  const kinds = texts(rule.tools);
  const apps = texts(rule.apps);
  if (events && events.length && !events.includes("tool.pre")) return [];
  if (kinds && kinds.length && !kinds.includes("mcp.call")) return [];
  if (apps && apps.length && !apps.includes(app)) return [];
  if (rule.toolNames === undefined) return rule.command !== undefined || rule.paths !== undefined || rule.interpreters !== undefined ? [] : tools.slice();
  const matched = asMap(rule.toolNames);
  return tools.filter((t) => matches(matched.allow, t) || matches(matched.deny, t));
}

// Verdict is what a fixed rule does to one tool, ranked the way the
// engine combines them: a deny wins, an approval holds a winning allow.
type Verdict = { rank: number; status: FixedRule["status"]; word: string };
const RANK: Record<FixedRule["status"], number> = { visible: 0, approve_gated: 1, hidden_policy: 2 };

function verdictOf(rule: Plain, tool: string): Verdict {
  const view = ruleView(rule, 0);
  if (view.posture === "deny" || matches(asMap(rule.toolNames).deny, tool)) return { rank: RANK.hidden_policy, status: "hidden_policy", word: POLICY_WORD.deny };
  if (view.posture === "hold" || view.posture === "ticket" || view.posture === "confirm") {
    const how = asMap(rule.approve).class === "ticket" ? "ticket" : "hold";
    const short = shortShapeWords({ pool: SPONSOR, how, hold: view.timeoutSeconds, ticket: view.ticketTTLSeconds, grant: view.grantTTLSeconds });
    return { rank: RANK.approve_gated, status: "approve_gated", word: heldWord(short) };
  }
  return { rank: RANK.visible, status: "visible", word: POLICY_WORD.allow };
}

const noRead = (): OwnRead => ({ plan: { call: "allow", choice: {}, shape: defaultShape(), own: {} }, owned: [], fixed: {} });

// readOwnRules reads what the role's own set says about one server; yaml
// is null when the set does not exist. The rules it owns are the ones
// ownRuleOf reads. The first with no tool list makes the call "every", a
// group before it gives its tools their own setting (the engine picks the
// earlier approval of a set), and a group after it never decides, so it
// is fixed. Without one, any owned rule makes the call "per": the setting
// most tools hold is the server's (the earliest rule on a tie), the other
// held tools keep their own. A tool named twice takes the earlier rule and
// a deny wins. A tool the server no longer offers leaves the plan while
// its rule stays owned, so a save removes it. Every other rule touching
// the server is fixed, the strongest per tool. A set that does not parse
// reads as saying nothing, and the save refuses it.
export function readOwnRules(yaml: string | null, app: string, tools: string[]): OwnRead {
  const out = noRead();
  if (yaml === null) return out;
  const doc = openDoc(yaml);
  if (docProblems(doc).length) return out;
  const set = readSet(doc).name;
  const held: Record<string, { shape: Shape; at: number }> = {};
  const denied: string[] = [];
  const touched = new Set<string>();
  const found: Record<string, Verdict & { ruleId: string }> = {};
  const fix = (tool: string, v: Verdict, ruleId: string) => {
    if (!found[tool] || v.rank > found[tool].rank) found[tool] = { ...v, ruleId };
  };
  let every: Shape | null = null;
  for (const view of rulesOf(doc)) {
    const own = ownRuleOf(view.raw, app);
    const offered = own ? own.tools.filter((t) => tools.includes(t)) : [];
    if (own && own.kind === "group" && every) {
      for (const t of offered) fix(t, { rank: RANK.approve_gated, status: "approve_gated", word: EVERY_RULE_FIRST }, own.id);
    } else if (own) {
      out.owned.push(own.id);
      if (own.kind === "every" && !every) {
        every = own.shape;
        tools.forEach((t) => touched.add(t));
      }
      for (const t of offered) {
        touched.add(t);
        if (own.kind === "deny" && !denied.includes(t)) denied.push(t);
        if (own.kind === "group" && !held[t]) held[t] = { shape: own.shape as Shape, at: view.index };
      }
    } else {
      for (const t of ruleTools(view.raw, app, tools)) fix(t, verdictOf(view.raw, t), view.id);
    }
  }
  for (const t of Object.keys(found)) {
    const v = found[t];
    if (v.rank === RANK.visible && touched.has(t)) continue;
    out.fixed[t] = { set, ruleId: v.ruleId, word: v.word, status: v.status };
  }
  const heldNow = tools.filter((t) => held[t] && !denied.includes(t));
  const plan = out.plan;
  if (every) {
    plan.call = "every";
    plan.shape = every;
    for (const t of heldNow) {
      plan.choice[t] = "approve";
      if (!sharesServer(plan, held[t].shape)) plan.own[t] = { ...held[t].shape };
    }
  } else if (out.owned.length) {
    plan.call = "per";
    const counts: { key: string; shape: Shape; n: number; at: number }[] = [];
    for (const t of heldNow) {
      const key = shapeKey(held[t].shape);
      const c = counts.find((x) => x.key === key);
      if (c) {
        c.n++;
        c.at = Math.min(c.at, held[t].at);
      } else counts.push({ key, shape: held[t].shape, n: 1, at: held[t].at });
    }
    const top = counts.sort((a, b) => b.n - a.n || a.at - b.at)[0];
    if (top) plan.shape = { ...top.shape };
    for (const t of heldNow) {
      plan.choice[t] = "approve";
      if (!sharesServer(plan, held[t].shape)) plan.own[t] = { ...held[t].shape };
    }
  }
  for (const t of denied) plan.choice[t] = "deny";
  return out;
}

// fixedFrom merges the rules the editor leaves alone: the server's preview
// of other sets that decide a tool for this role, and the rules of the
// role's own set it cannot say. A preview entry from the role's own set is
// the editor's to change unless its rule is not among the owned ids. Per
// tool the stronger rule shows, a deny over an approval over an allow; on
// a tie the own set's reading shows for its own rules, the preview for
// another set's, since that set is the one that decides.
export function fixedFrom(preview: Record<string, PreviewEntry> | null | undefined, ownSet: string, own: OwnRead | null): Record<string, FixedRule> {
  const out: Record<string, FixedRule> = own ? { ...own.fixed } : {};
  for (const tool of Object.keys(preview || {})) {
    const e = (preview as Record<string, PreviewEntry>)[tool];
    if (e.status !== "approve_gated" && e.status !== "hidden_policy") continue;
    if (e.setName === ownSet && (!own || (e.ruleId && own.owned.includes(e.ruleId)))) continue;
    const cur = out[tool];
    if (cur && (RANK[cur.status] > RANK[e.status] || (RANK[cur.status] === RANK[e.status] && e.setName === ownSet))) continue;
    out[tool] = { set: e.setName || "", ruleId: e.ruleId, word: runWord({ status: e.status }), status: e.status };
  }
  return out;
}

// approveBlock renders a setting as the approve block of a rule. A ticket
// never carries timeoutSeconds, which validation refuses.
export function approveBlock(s: Shape): Plain {
  const out: Plain = s.pool === SPONSOR ? { deciders: [SPONSOR] } : { roles: [s.pool] };
  if (s.how === "hold") out.timeoutSeconds = s.hold;
  else {
    out.class = "ticket";
    out.ticketTTLSeconds = s.ticket;
    out.grantTTLSeconds = s.grant;
  }
  return out;
}

// rulesFor renders the plan as the rules of the role's own set for this
// server, in the order the engine needs: tools with their own setting
// first, one rule per distinct setting, because among firing approve rules
// of one set the earlier wins; then the server's rule (no tool list under
// "every", the held tools under "per"); then one deny rule. Ids are kept
// clear of taken, the ids the set keeps for other rules.
export function rulesFor(plan: Plan, app: string, tools: string[], role: string, taken: string[] = []): Plain[] {
  const ids = taken.slice();
  const id = (base: string) => {
    const v = uniqueId(slug(base), ids);
    ids.push(v);
    return v;
  };
  const approve = (ruleId: string, names: string[] | null, s: Shape, reason: string): Plain => {
    const r: Plain = { id: ruleId, tools: ["mcp.call"], apps: [app] };
    if (names) r.toolNames = { allow: names };
    r.effect = "allow";
    r.mode = "approve";
    r.approve = approveBlock(s);
    r.reason = reason;
    return r;
  };
  const rules: Plain[] = [];
  const held = heldTools(plan, tools);
  const groups: { shape: Shape; tools: string[] }[] = [];
  for (const h of held) {
    if (sharesServer(plan, h.shape)) continue;
    const g = groups.find((x) => shapeKey(x.shape) === shapeKey(h.shape));
    if (g) g.tools.push(h.tool);
    else groups.push({ shape: h.shape, tools: [h.tool] });
  }
  for (const g of groups) rules.push(approve(id(app + "-" + g.tools[0] + "-approve"), g.tools, g.shape, approveGroupReason(app, g.tools)));
  if (plan.call === "every") rules.push(approve(id(app + "-every-call-approve"), null, plan.shape, everyCallReason(app)));
  else {
    const shared = held.filter((h) => sharesServer(plan, h.shape)).map((h) => h.tool);
    if (shared.length) rules.push(approve(id(app + "-approve"), shared, plan.shape, approveGroupReason(app, shared)));
  }
  const denied = deniedTools(plan, tools);
  if (denied.length) rules.push({ id: id(app + "-deny"), tools: ["mcp.call"], apps: [app], toolNames: { deny: denied }, effect: "deny", reason: denyGroupReason(app, role) });
  return rules;
}

// sameRules says whether two plans write the same rules for one server,
// which is how a door tells that nothing changed.
export const sameRules = (a: Plan, b: Plan, app: string, tools: string[], role: string): boolean =>
  JSON.stringify(rulesFor(a, app, tools, role)) === JSON.stringify(rulesFor(b, app, tools, role));

// rulesText is the rules block on its own, the text the fold under the
// editor shows, in the layout the stored set has.
export function rulesText(rules: Plain[]): string {
  return rules.length ? rulesBlock(docText(newPolicyDoc({ name: "x", rules }))) : "";
}

// rulesBlock cuts the rules out of a set's text as docText lays it out: the
// line "  rules:" and every line under it, up to the next key of the spec.
// Empty when the text has no rules key.
export function rulesBlock(text: string): string {
  const lines = text.replace(/\n+$/, "").split("\n");
  const at = lines.findIndex((l) => /^ {2}rules:/.test(l));
  if (at === -1) return "";
  let end = at + 1;
  while (end < lines.length && !/^ {0,2}\S/.test(lines[end])) end++;
  return lines.slice(at, end).join("\n").replace(/\n+$/, "");
}

// sentenceParts is what the sentence under the editor says, counted. The
// tools the server's setting holds are a count under "per" only: under
// "every" the rule for every call already says them.
function sentenceParts(plan: Plan, app: string, tools: string[], role: string): SentenceParts {
  const held = heldTools(plan, tools);
  const shared = plan.call === "per" ? held.filter((h) => sharesServer(plan, h.shape)).length : 0;
  return {
    role,
    app,
    reach: plan.reach,
    total: tools.length,
    ticked: grantMatchers(plan, tools).length,
    call: plan.call,
    every: plan.call === "every" ? shapeWords(plan.shape) : "",
    allowed: tools.filter((t) => choiceOf(plan, t) === "allow").length,
    shared: shared ? { count: shared, words: shapeWords(plan.shape) } : null,
    own: held.filter((h) => !sharesServer(plan, h.shape)).map((h) => ({ tool: h.tool, words: shapeWords(h.shape) })),
    denied: deniedTools(plan, tools),
    newToolsRun: newToolsRun(plan),
  };
}

// planSentence is the one sentence under the editor: what the access row
// reaches and what a call does, the approval shape named.
export const planSentence = (plan: Plan, app: string, tools: string[], role: string): string => planSentenceParts(sentenceParts(plan, app, tools, role));

// callSentence is planSentence without its first sentence, the one on what
// the row reaches: what a call does, for the publish row of a save.
export function callSentence(plan: Plan, app: string, tools: string[], role: string): string {
  const p = sentenceParts(plan, app, tools, role);
  const full = planSentenceParts(p);
  const reach = planSentenceParts({ ...p, call: "per", allowed: 0, shared: null, own: [], denied: [], newToolsRun: false });
  return full.startsWith(reach) ? full.slice(reach.length).trimStart() : full;
}

// policyWord is a tool's word in the Review and the read-only column:
// allowed, denied, or needs approval with its shape.
export function policyWord(plan: Plan, tool: string): string {
  const c = choiceOf(plan, tool);
  if (c === null) return "";
  if (c === "approve") return heldWord(shortShapeWords(shapeOf(plan, tool)));
  return POLICY_WORD[c];
}
