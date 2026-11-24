// The words of the Drafts area: the queue, the review page, the publish
// dialog, Check again, Discard and Undo. Only the lazy drafts screens
// import this file, so none of it lands in the console's entry page. The
// server's own sentences, a finding's words and a refusal's, are shown as
// the server answered them and never live here.
import type { ApiError, DraftDoor, DraftKind, DraftObjectName, DraftPrincipal, DraftState, FindingClass, GainOutcome } from "./api";
import { CHANGE_WORD as DRAFT_CHANGE, OUTCOME_WORD } from "./chip-words";
import type { ObjectsMove } from "./drafts-access";
import { AGENT_LOOSEN_CODE, AGENT_REACH_CODE, agentAccepts, type ChangeCount, type ChangeWhat, type ConfirmBlock, type Shown } from "./drafts-model";
import { type Nouns, cap, counted } from "./object-words";
import type { Bucket } from "./policy-model";
import { list, plural } from "./policy-words";
import { WRITE_UNCONFIRMED } from "./say";

const bare = (s: string) => s.replace(/[.\s]+$/, "");

// answered is the sentence a refused drafts call shows: the server's own,
// as it answered it, or the landed line for a write nobody answered.
export const answered = (err: ApiError) => (err.unreachable ? WRITE_UNCONFIRMED : err.message);

// ---- the queue ----

export type Tab = "waiting" | "published" | "discarded" | "expired";
export const TABS: { key: Tab; label: string; state: DraftState }[] = [
  { key: "waiting", label: "Waiting", state: "open" },
  { key: "published", label: "Published", state: "published" },
  { key: "discarded", label: "Discarded", state: "discarded" },
  { key: "expired", label: "Expired", state: "expired" },
];
export const WHOSE_LABEL = "Whose drafts";
export const WHOSE = { all: "All", mine: "Mine" };
export const FILTER_PLACEHOLDER = "Filter by name, server or who drafted";
export const FILTER_LABEL = "Filter drafts";
export const SUBJECT_LIST = "The draft list";
export const READING_LIST = "Reading the drafts.";
export const COLUMN = { id: "Draft", what: "What changes", who: "Drafted by", door: "How it came in", checks: "Checks", waiting: "Waiting", decided: "Decided" };
export const CHECKS_HELP = "Counts from the last check of this revision.";
export const shownCount = (shown: number, loaded: number) => shown + " of " + loaded + " shown";
export const loadedLine = (n: number) => plural(n, "draft", "drafts") + " loaded";
export const LOAD_MORE = "Load more";
export const EMPTY_SEARCH = "No loaded draft matches the filter.";
export const EMPTY: Record<Tab, { title: string; body: string }> = {
  waiting: { title: "No draft waits for review", body: "Drafts from the console, strazactl, agents and the apps directory land here." },
  published: { title: "No draft was published yet", body: "Every publish lands here with who published it and when." },
  discarded: { title: "No draft was discarded", body: "A discarded draft stays here with its reason." },
  expired: { title: "No draft expired", body: "An agent's open draft expires 14 days after its latest revision." },
};
export const EMPTY_MINE = { title: "None of your drafts is here", body: "Switch to All to see the drafts other people and agents wrote." };

// doorWords names the door a draft came in through.
export function doorWords(door: DraftDoor | string): string {
  const words: Record<string, string> = { console: "the console", strazactl: "strazactl", "straza-app": "the straza app", "apps-directory": "the apps directory", api: "the API" };
  return words[door] || door;
}

// fileOf is the name of the apps directory file a draft came from.
export const fileOf = (source: string | undefined) => (source || "").split("/").pop() || "a file";

// proposerWords names who drafted it and, on the line under the name, what
// kind of author that is, from the reader's point of view.
export function proposerWords(p: DraftPrincipal, me: string, source?: string): { name: string; line: string } {
  if (p.via === "file") return { name: fileOf(source), line: "" };
  if (p.via === "upgrade") return { name: "Straza's upgrade", line: "a saved edit moved into a draft" };
  if (p.agent) return { name: p.username, line: p.sponsor ? "an agent, sponsored by " + (p.sponsor === me ? "you" : p.sponsor) : "an agent" };
  if (p.username === me) return { name: p.username, line: "you" };
  if (p.via === "api-token") return { name: p.username, line: "an admin API token" };
  return { name: p.username, line: "" };
}

// The check counts of a queue row.
export const refusedCount = (n: number) => n + " refused";
export const widenCount = (n: number) => n + (n === 1 ? " widens access" : " widen access");
export const warningCount = (n: number) => plural(n, "warning", "warnings");
export const uncheckedCount = (n: number) => n + " not checked";
export const NOTHING_TO_ACK = "nothing to confirm";
export const NOT_CHECKED_YET = "not checked yet";
export const NOT_CHECKED_HELP = "Straza has not checked this revision yet. Opening the draft checks it.";

// decidedWords is the Decided cell of a published, discarded or expired row.
export function decidedWords(state: DraftState, who: string | undefined): string {
  if (state === "published") return "Published by " + (who || "someone");
  if (state === "discarded") return "Discarded by " + (who || "someone");
  return "Expired";
}

// ---- the review page ----

export const draftTitle = (id: string) => "Draft " + id;
export const idChip = (id: string) => "draft " + id;
export const subjectDraft = (id: string) => "Draft " + id;
export const readingDraft = (id: string) => "Reading draft " + id + ".";
export const MISSING_TITLE = "This draft is not here";
export const OPEN_DRAFTS = "Open Drafts";
export const STATE_WORD: Record<Shown, string> = { ready: "Ready", refused: "Refused", stale: "Out of date", published: "Published", discarded: "Discarded", expired: "Expired" };
// cameInLine says how a draft came in: the door, the file it came from,
// and for the straza app the harness the agent ran in.
export const cameInLine = (door: DraftDoor, source: string | undefined, client: string | undefined) =>
  doorWords(door) + (source ? ", " + source : "") + (door === "straza-app" && client ? ", from " + client : "");
export const DETAILS = "Details";
export const revisionLine = (n: number, who: string, door: string, at: string, mechanical: boolean) =>
  "revision " + n + " by " + who + " in " + doorWords(door) + " at " + at + (mechanical ? ", checked again against live state" : "");
export const checkedLine = (at: string, version: string) => at + (version ? ", against live policy " + version : "");
export const expiresLine = (at: string) => at + ", unless someone revises it first";
export const WAITS_WHOLE = "Someone who may publish every object in it must publish it.";

const KIND_WORD: Record<DraftKind, string> = { App: "the server", Role: "the role", PolicySet: "the policy" };

// objectWords names a Kind/Name object in words.
export function objectWords(object: string): string {
  const i = object.indexOf("/");
  const kind = object.slice(0, i) as DraftKind;
  return i > 0 && KIND_WORD[kind] ? KIND_WORD[kind] + " " + object.slice(i + 1) : object;
}
// needLine is one sentence of the Who may publish help: the grant one
// object needs.
export const needLine = (object: string, standing: string) => cap(objectWords(object)) + " needs " + bare(standing) + ".";

// The note: the proposer's own words, checked by nobody.
export const saysTitle = (agent: boolean, name: string) => (agent ? "What the agent says" : "What " + name + " says");
export const SAYS_AGENT_HELP = "Written by the agent. Nothing checks it.";
export const saysPersonHelp = (name: string) => "The note " + name + " wrote with the draft, checked by nobody. Every other line on this page is computed by Straza.";

// In this draft: one row per item, above the cards.
export const IN_DRAFT = "In this draft";
export const IN_DRAFT_COLUMN = { kind: "Kind", name: "Name", change: "Change", does: "What publishing does" };

// What changes.
export const CHANGES_TITLE = "What changes";
const KIND_COUNTED: Record<DraftKind, [string, string]> = { App: ["MCP server", "MCP servers"], Role: ["role", "roles"], PolicySet: ["policy", "policies"] };
export const ITEM_KIND: Record<DraftKind, string> = { App: "MCP server", Role: "Role", PolicySet: "Policy" };
const ROLE_KIND: Record<string, string> = { application: "Application role", business: "Business role", approver: "Approver role", straza: "Straza role" };
// roleKind names a role's kind as the Roles area does.
export const roleKind = (kind: string) => ROLE_KIND[kind] || ITEM_KIND.Role;
export const ownedBy = (server: string) => "owned by " + server;
export const MARK: Record<ChangeWhat, string> = DRAFT_CHANGE;
// The summary chips of a changed role: the tools and the objects that join
// it (+) and leave it (-), in the server's own nouns.
export type Sign = "+" | "-";
export const toolsChip = (sign: Sign, n: number) => sign + n + " " + (n === 1 ? "tool" : "tools");
export const objectsChip = (sign: Sign, n: number, w: Nouns) => sign + counted(n, w);
export const everyChip = (sign: Sign, w: Nouns) => sign + everyType(w);
// unchangedFold folds the chips a role keeps once there are more than four.
export const unchangedFold = (n: number) => n + " unchanged";
// OUTCOME_OF names a policy set's outcomes in the policy pages' words.
const OUTCOME_OF: Record<Bucket, GainOutcome> = { hum: "needs-approval", deny: "denied", allow: "runs" };
export const outcomesWords = (bs: Bucket[]) => bs.map((b) => OUTCOME[OUTCOME_OF[b]]).join(" · ");
export const FILE_MASKED = "A value Straza keeps secret reads [REDACTED] here.";
export const openLive = (name: string) => "Open " + name;
// LIVE_LIST is what the live link reads to find a server's or a role's
// page, and notLive says the object it names is not live now.
export const LIVE_LIST: Record<"App" | "Role", string> = { App: "The server list", Role: "The role list" };
export const notLive = (name: string) => name + " is not live now, so it has no page to open. Reload the draft to read it against live state again.";

// What publishing does, one line per row of In this draft. A published
// draft says what its publish did.
export function startsLine(remote: boolean, tools: number | null, past: boolean): string {
  const starts = past ? "Started it" : "Starts it";
  if (tools !== null) return starts + (past ? " and offered its " : " and offers its ") + plural(tools, "tool", "tools") + " to the roles that reach them.";
  return starts + "." + (remote && !past ? " Its tools are not read yet." : "");
}
const lowerFirst = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);
// changesLine names what publishing changes on a live object: the first pin
// of a server's object type by the server's plural, then the fields that
// change, or none when nothing the object's page shows changes.
export function changesLine(pins: string, fields: string[], past: boolean, none: string): string {
  const parts = [pins ? (past ? "pinned " : "pins ") + pins : "", fields.length ? (past ? "changed " : "changes ") + list(fields.map(lowerFirst)) : ""].filter(Boolean);
  return parts.length ? cap(list(parts)) + "." : none;
}
// reachesLine says what a new role reaches: its tools on each server, or
// the roles it implies when it reaches none of its own.
export function reachesLine(bindings: { app: string; tools: string[] }[], implies: string[]): string {
  if (!bindings.length) return implies.length ? "Implies " + list(implies) + "." : "Reaches no tool.";
  return "Reaches " + list(bindings.map((b) => (b.tools.includes("*") ? "every tool of " + b.app : plural(b.tools.length, "tool", "tools") + " of " + b.app))) + ".";
}
// rulesLine counts a policy set's rules that are new, edited and removed,
// the noun said once, after the first count.
export function rulesLine(c: { new: number; edited: number; removed: number }): string {
  const parts = (["new", "edited", "removed"] as const).filter((k) => c[k]).map((k, i) => c[k] + " " + k + (i ? "" : " " + (c[k] === 1 ? "policy rule" : "policy rules")));
  return parts.length ? list(parts) + "." : "Changes no policy rule.";
}

export const TOOLS_ROW = "Tools";
export const TOOLS_NOT_READ = "not read yet";
export const contactedLine = (host: string, at: string, n: number) => "Contacted " + host + " at " + at + ": " + plural(n, "tool", "tools") + " read.";
export const NOT_READABLE_DOC = "The document does not read as YAML. View as YAML shows it as text.";
// toolsOn labels a role's tools: Tools alone when the role reaches one
// server, which its card names, else the server too.
export const toolsOn = (server: string, one: boolean) => (one ? TOOLS_ROW : "Tools on " + server);
export const IMPLIES = "Implies";
export const DESCRIPTION = "Description";
export const NOTHING = "nothing";
export const EVERY_TOOL = "every tool, and tools added later";
// The objects a role covers, in its server's own words.
export const objectsLabel = (w: Nouns) => cap(w.plural);
export const everyType = (w: Nouns) => "every " + w.type;
export const addedMark = (value: string) => "+ " + value;
export const OFF_LINE = "Off after publishing: the policy stays stored and decides nothing.";
// ON_LINE is the line of a policy that is off and that the draft turns on.
export const ON_LINE = "On after publishing: its policy rules decide again.";
export const NO_FIELD_CHANGE = "No field the server page shows changes.";
export const NO_ROLE_CHANGE = "No field the role page shows changes.";
export const REMOVED_LINE = "Publishing removes it with everything live that belongs to it.";
export const REMOVED_LINE_PAST = "This publish removed it, with everything live that belonged to it.";

// Access after publishing.
export const GAINS_TITLE = "Access after publishing";
export const GAINS_HELP = "What a holder of each role the draft touches gets, now and after publishing.";
export const GAIN_COLUMN = { tool: "Tool", now: "Now", after: "After publishing" };
// OUTCOME is the chip of one cell, in the words the policy pages use.
export const OUTCOME: Record<GainOutcome, string> = { runs: OUTCOME_WORD.allowed, "needs-approval": OUTCOME_WORD.approval, denied: OUTCOME_WORD.denied, "not-reachable": OUTCOME_WORD.none, unknown: "not known" };
// outcomeFoot says a gate's words once under the table, after the chip
// word it explains.
export const outcomeFoot = (o: GainOutcome, words: string) => (OUTCOME[o] || o) + ": " + bare(words) + ".";
// people counts the holders of a role in the console's words for people.
const people = (n: number) => (n === 1 ? "1 person" : n.toLocaleString("en-US") + " people");
const objectNames = (os: DraftObjectName[]) => list(os.map((o) => o.value));
// coveredWords names the objects a role covers: every object, none, or the
// count and the names.
export const coveredWords = (w: Nouns, every: boolean, os: DraftObjectName[]) =>
  every ? everyType(w) : os.length ? counted(os.length, w) + ": " + objectNames(os) : "no " + w.plural;

// groupLine heads one group of the table: the role, on its server when the
// table's groups span several servers (server empty otherwise), who holds
// it, the objects it covers when the table does not show them as a row,
// and what a holder gains and loses.
export function groupLine(role: string, server: string, names: string[] | undefined, count: number, on: string, gains: string[], loses: string[]): string {
  const held = names && names.length ? "held by " + list(names) : count ? people(count) : "nobody holds it";
  return [role + (server ? " on " + server : ""), held, on ? "on " + on : "", gains.length ? "gains " + list(gains) : "", loses.length ? "loses " + list(loses) : ""].filter(Boolean).join(" · ");
}
export const toolCount = (n: number) => plural(n, "tool", "tools");
// objectsGained and objectsLost are a group line's words for the objects
// a role adds and drops.
export function objectsGained(w: Nouns, m: ObjectsMove): string {
  if (m.every === "gained") return everyType(w);
  return m.added.length ? coveredWords(w, false, m.added) : "";
}
export function objectsLost(w: Nouns, m: ObjectsMove): string {
  if (m.every === "lost") return "every other " + w.type;
  return m.dropped.length ? coveredWords(w, false, m.dropped) : "";
}
// sameFold folds the tools whose outcome does not change.
export const sameFold = (n: number) => toolCount(n) + ", same outcome as now.";
export const NO_GAINS = "No role gains or loses a tool when this is published.";
export const GAINS_HIDDEN = "Part of the access after publishing is hidden from you.";
// holdersLose warns, under the band, that holders of a held role lose tools.
export const holdersLose = (roles: string[]) => "Holders of " + list(roles) + " lose tools.";

// Checks.
export const CHECKS_TITLE = "Checks";
// publishedUnread stands for the stored check when a publish landed and the
// page's read of the draft after it failed.
export const publishedUnread = (id: string) => "Draft " + id + " is published. This page could not read it again, so its stored check is not shown. Reload to try again.";
export const GROUP: Record<FindingClass, string> = { refused: "Refused", risk: "Widens access", warning: "Warnings", unchecked: "Not checked", passed: "Passed", info: "Notes" };
// RISK_TAIL follows the heading of the lines that widen access.
export const RISK_TAIL = "you confirm this when you publish";
// nowAfter says a line's Now and After, with no Now where it has none.
export const nowAfter = (before: string, after: string) =>
  [before ? "Now: " + bare(before) + "." : "", after ? "After publishing: " + bare(after) + "." : ""].filter(Boolean).join(" ");
export const TICK_LINE = "Tick to confirm.";
// ackWords says how the publish dialog asks to confirm one block, split
// around the text to type, which the page sets as code: a tick, a typed
// name, a typed count of the objects of its lines, the agent's change a
// person accepts, or a block this reader cannot confirm.
export function ackWords(b: ConfirmBlock): [pre: string, code: string, post: string] {
  if (b.blocked) return ["Only someone who may read what this line names can confirm it, so ask an admin who may read it to publish this draft.", "", ""];
  if (b.kind === "tick") return [TICK_LINE, "", ""];
  if (b.kind === "count") return ["Type", b.expect, "when you publish, " + countOf(doesOf(b.lines[0].code, b.lines[0].object)) + "."];
  if (b.lines.some(agentAccepts)) return ["Type", b.expect, "when you publish to accept it."];
  return ["Type", b.expect, "when you publish."];
}
// undoLine says once whether publishing the old version undoes what the
// confirmed lines do: a typed line cannot be undone that way.
export const undoLine = (typed: boolean) => (typed ? "Publishing the old version again does not undo it." : "Publishing the old version again undoes it.");
// The table of Widens access, its pills, and the note of a warning folded
// into a row.
export const WIDENS_COLUMN = { tool: "Tool", rule: "Policy rule", objects: "Objects", for: "For", calls: "Calls", now: "Now", after: "After publishing" };
export const SAME_AS_NOW = "same as now";
export const NOT_REGISTERED = "not registered";
export const machineNote = (server: string) => "It matches any MCP server a person's own harness runs under the name " + server + ".";
// appliesTo says a changed policy applies to the proposer: you when the
// reader is the proposer, else the proposer's name.
export const appliesTo = (name: string | null) => "applies to " + (name === null ? "you" : name);
// Details of Widens access: the versions the lines name, and the lines by
// check code, with the warnings folded into them.
export const VERSION_COLUMN = { name: "Policy rule", now: "Version now", after: "After publishing" };
export const NO_VERSION = "none";
export function checkLines(codes: { code: string; objects: string[] }[], folded: string[]): string {
  const parts = codes.map((c) => c.code + (c.objects.length ? " on " + c.objects.join(" and on ") : ""));
  const said = parts.length < 2 ? parts.join("") : parts.slice(0, -1).join(", ") + ", and " + parts[parts.length - 1];
  const tail = folded.length ? ", with the " + (folded.length === 1 ? "warning " : "warnings ") + list(folded) + " folded into " + (folded.length === 1 ? "it" : "them") : "";
  return "Check lines: " + said + tail + ".";
}
export const CONTACT = "Contact it now";
export const CONTACT_HELP = "Opens one MCP connection with no credential to read its tools. The audit chain records who asked.";
export const CONTACT_FAILED = "The contact did not finish.";

// The band's Publish button holds a stale or refused draft with one of
// these as its title.
export const STALE_BAR = "Check the draft again before publishing.";
export const PUBLISH_OPEN = "Publish…";
export const PUBLISH_NOT_YET = "Fix the refused lines under Checks first.";
export const DISCARD = "Discard";

// Out of date, and Check again.
export const STALE_TITLE = "Out of date";
export const CHECK_AGAIN = "Check again";
export const checkedAgain = (id: string) => "Checked draft " + id + " again against the live state.";
export const CHECK_AGAIN_SUBJECT = "Check again";
export const PICKS_TITLE = "Pick the values to keep";
export const PICKS_BODY = "The draft and live state both changed these fields.";
// valueWords is a picked value in words: a field the side left empty reads
// unset.
const valueWords = (v: string) => (v === "" ? "unset" : v);
export const keepDraft = (v: string) => "Draft: " + valueWords(v);
export const keepLive = (v: string) => "Live: " + valueWords(v);
export const pickWas = (base: string) => "It was " + valueWords(base) + " when the draft was checked.";
// A field whose values run over several lines, such as a set's text, is
// picked by its side, with each text in a fold under its choice.
export const KEEP_DRAFT_TEXT = "Keep the draft's text";
export const KEEP_LIVE_TEXT = "Keep live's text";
export const BASE_TEXT = "The text when the draft was checked";
export const SHOW_TEXT = "Show the text";
export const PICKS_MISSING = "Pick a value for every field.";

// The publish dialog.
export const publishTitle = (id: string) => "Publish draft " + id + "?";
// publishOne titles the dialog of a draft that adds one object and changes
// nothing else.
export const publishOne = (name: string) => "Publish " + name + "?";
const VERB: Record<ChangeWhat, string> = { new: "adds", changed: "changes", on: "turns on", off: "turns off", removed: "removes" };
// goLive is the lead of the publish dialog: what publishing does to each
// kind, by what it does, so a removal never reads as going live.
export function goLive(counts: ChangeCount[]): string {
  const phrases = (Object.keys(VERB) as ChangeWhat[])
    .map((what) => counts.filter((c) => c.what === what))
    .filter((cs) => cs.length)
    .map((cs) => VERB[cs[0].what] + " " + list(cs.map((c) => plural(c.n, ...KIND_COUNTED[c.kind]))));
  return phrases.length ? "Publishing " + list(phrases) + "." : "Publishing changes nothing.";
}

// Does is what publishing does to the objects a risk line names: a verb,
// then the noun it counts in the singular and the plural.
export type Does = [verb: string, one: string, many: string];

// PUBLISH_DOES words what publishing does, for the heading of each confirm
// block of the publish dialog and the count a typed block asks for. A line
// is worded by its risk code, else as unread when the server left out its
// object for this reader, else by the kind of the object it names, else by
// other. Lines of one code name distinct objects, but unread lines of
// several codes may name one object, so those count as lines. Every risk
// widens access, so the fallbacks say so. strazactl's publish prompt copies
// this table, so a change here goes there too.
export const PUBLISH_DOES: { code: Record<string, Does>; kind: Record<string, Does>; unread: Does; other: Does } = {
  code: {
    "server.removal": ["removes", "MCP server", "MCP servers"],
    "policy.guardrail-changed": ["loosens", "policy", "policies"],
    "agent.guardrail": ["loosens", "policy", "policies"],
    "agent.own-reach": ["widens the agent's own reach on", "MCP server", "MCP servers"],
    "role.covers-wider": ["widens", "role", "roles"],
  },
  kind: {
    App: ["widens access through", "MCP server", "MCP servers"],
    Role: ["widens", "role", "roles"],
    PolicySet: ["widens access through", "policy", "policies"],
  },
  unread: ["widens access through", "line", "lines"],
  other: ["widens access through", "object", "objects"],
};

// doesOf is the words of one risk line, by its code and its Kind/Name
// object.
export function doesOf(code: string, object: string | undefined): Does {
  const kind = (object || "").split("/")[0];
  if (Object.hasOwn(PUBLISH_DOES.code, code)) return PUBLISH_DOES.code[code];
  if (!object) return PUBLISH_DOES.unread;
  return Object.hasOwn(PUBLISH_DOES.kind, kind) ? PUBLISH_DOES.kind[kind] : PUBLISH_DOES.other;
}

// countOf says what a typed count stands for: the roles that gain access,
// else the objects of the verb, such as the policies it loosens.
const countOf = (does: Does) => "the number of " + does[2] + (does[0] === "widens" ? " that gain access" : " it " + does[0]);

// blockTitle heads a confirm block: what publishing does to the n objects
// its lines name. A line carries no access of its own, so lines this
// reader cannot read are counted as such, and an object of a kind this
// console does not know is not counted.
export const blockTitle = (does: Does, n: number) => {
  if (does === PUBLISH_DOES.unread) return plural(n, "line", "lines") + " you cannot read " + (n === 1 ? "widens" : "widen") + " access";
  if (does === PUBLISH_DOES.other) return "Widens access";
  return cap(does[0] + " " + plural(n, does[1], does[2]));
};
export const AGENT_LOOSENS = "The agent loosens its own rules";
export const AGENT_WIDENS = "The agent widens its own reach";
// agentTitle heads a block of lines a person accepts for the agent.
export const agentTitle = (lines: { code: string }[]) => (lines.some((f) => f.code === AGENT_REACH_CODE) ? AGENT_WIDENS : AGENT_LOOSENS);
// agentWrote opens the warning of an agent's draft that changes that
// agent's own access, by what its lines do: loosen its rules, widen its
// reach, or both.
export function agentWrote(agent: string | null, lines: { code: string }[] = [{ code: AGENT_LOOSEN_CODE }]): string {
  const does = [lines.some((f) => f.code === AGENT_LOOSEN_CODE) && "loosen its own rules", lines.some((f) => f.code === AGENT_REACH_CODE) && "widen its own reach"].filter(Boolean);
  return (agent || "An agent") + " wrote this draft to " + does.join(" and ") + ".";
}
// A typed block is confirmed, and an agent's self-loosening accepted.
export const CONFIRM = "confirm";
export const ACCEPT = "accept it";
// typePrompt is the label of a typed block's field, split around the text
// to type, which the dialog sets as code: Type <text> to confirm.
export const typePrompt = (verb: string): [string, string] => ["Type", "to " + verb];
export const MATCHED = "Typed correctly.";
export const typedWrong = (expect: string, verb: string) => "That is not " + expect + ". Type " + expect + " to " + verb + ".";
export const BLOCKED = "You cannot confirm this block, because a line in it names something you may not read. Ask an admin who may read it to publish this draft.";
export const ACK_MISSING = "Confirm each block to publish.";
// sponsorLine tells the sponsor of the agent that proposed the draft that a
// draft with a risk needs another admin while second-person publishing is on.
export const sponsorLine = (agent: string, risky: boolean) =>
  "You sponsor " + agent + (risky ? ". When second-person publishing is on, another admin must publish this." : ", which proposed this draft.");
export const PUBLISH = "Publish";
export const CANCEL = "Cancel";
export const NOT_PUBLISHED = "Nothing was published.";
export const publishedToast = (id: string) => "Draft " + id + " is live.";

// The secret of a server with one shared secret and none stored: its row on
// the server's card, its line under Checks, and the section of the publish
// dialog that asks for it and stores it once the publish lands.
export const SECRET_ROW = "Secret";
export const SECRET_LINE = "None is stored yet. Whoever publishes this draft enters it in the publish dialog.";
export const SECRET_CHECKS_LINE = "Whoever publishes this draft enters it in the publish dialog.";
export const secretsHead = (n: number) => (n === 1 ? "Secret" : "Secrets");
export const secretsLead = (names: string[]) =>
  names.length === 1
    ? names[0] + " has no secret yet. Every call to it fails until one is stored."
    : list(names) + " have no secret yet. Every call to them fails until one is stored.";
export const secretLabel = (name: string) => "Secret of " + name;
// secretHint names the agent that wrote the draft, which never sees the
// secret, and leaves it out of a draft only people wrote.
export const secretHint = (agent: string | null) =>
  "Stored encrypted and never shown again." + (agent ? " " + agent + ", the agent that wrote this draft, never sees it." : "");
export const ADD_LATER = "Add it later";
export const laterLine = (name: string) => name + " goes live with no secret. Every call fails until one is stored.";
export const enterOr = (name: string) => "Enter the secret of " + name + ", or tick Add it later.";
// notSent says a refused publish sent none of the n values typed, which the
// dialog dropped.
export const notSent = (n: number) =>
  n === 1
    ? "The secret you entered was not sent anywhere, because nothing was published. The field is empty again."
    : "The secrets you entered were not sent anywhere, because nothing was published. The fields are empty again.";
export const publishingTitle = (id: string) => "Publishing draft " + id;
export const storeRow = (name: string) => "Store the secret of " + name;
export const notStored = (name: string) => "The secret of " + name + " was not stored.";
export const notStoredText = (id: string, name: string, reason: string) =>
  "Draft " + id + " is published, but " + name + " has no secret, so every call fails. Straza said: \"" + bare(reason) + "\". Enter the secret again below.";
export const maybeStored = (name: string) => "The secret of " + name + " may not be stored.";
export const maybeText = (id: string, name: string) =>
  "Draft " + id + " is published and stays published. Straza is unreachable, so the secret may or may not be stored. Check the secret on the page of " + name + " before you enter it again.";
export const retryHint = (name: string) => name + " is live. Storing the secret runs the check under it.";
export const againHint = (name: string) => name + " is live. Storing the secret again replaces the encrypted value, then the check runs under it.";
export const storedNotPassed = (name: string) => "The secret of " + name + " is stored, and the check that ran under it did not pass.";
export const CLOSE = "Close";

// The listing credential of a server that pins an object type, which only
// Straza's own listing call carries. pin is the type, its plural and the
// tool that lists them.
type Pin = { type: string; plural: string; tool: string };
export const SECRET_LINE_BOTH = "Neither its secret nor its listing credential is stored yet. Whoever publishes this draft enters both in the publish dialog.";
export const leadBoth = (name: string, pin: Pin) =>
  name + " has no secret and no listing credential yet, so every call to it fails and Straza cannot list its " + pin.plural + ".";
export const leadListing = (name: string, pin: Pin) =>
  name + " has no listing credential yet, so Straza cannot list its " + pin.plural + ".";
export const listingLabel = (name: string) => "Listing credential of " + name;
export const listingHint = (pin: Pin, agent: string | null) =>
  "Straza uses it only to list " + pin.plural + ". Stored encrypted and never shown again." +
  (agent ? " " + agent + ", the agent that wrote this draft, never sees it." : "");
export const laterListing = (name: string, pin: Pin) => "Until the listing credential is set, only roles on every " + pin.type + " can use the " + pin.type + " tools of " + name + ".";
export const enterListingOr = (name: string) => "Enter the listing credential of " + name + ", or tick Add it later.";
export const storeListingRow = (name: string) => "Store the listing credential of " + name;
export const listsSoon = (name: string, pin: Pin) => "Straza lists the " + pin.plural + " of " + name + " within about 30 seconds.";
export const listingNotStored = (name: string) => "The listing credential of " + name + " was not stored.";
// listingNotStoredText says the server's calls work when its secret is
// stored, which a server whose secret this dialog did not store may not.
export const listingNotStoredText = (id: string, name: string, pin: Pin, secretStored: boolean, reason: string) =>
  "Draft " + id + " is published and stays published" + (secretStored ? ", and the secret of " + name + " is stored, so its calls work" : "") + ". Straza cannot list its " + pin.plural + ", and only roles on every " + pin.type + " can use its " + pin.type +
  " tools until it has one. The reason Straza gave: \"" + bare(reason) + "\". Enter it again below.";
export const listingMaybeStored = (name: string) => "The listing credential of " + name + " may not be stored.";
export const listingMaybeText = (id: string, name: string) =>
  "Draft " + id + " is published and stays published. Straza is unreachable, so the listing credential may or may not be stored. Check Server authentication on the Overview of " + name + " before you enter it again.";
export const listingRetryHint = (name: string) => name + " is live. Storing the listing credential starts a listing at once.";
export const STORE_LISTING = "Store the listing credential";

// After publishing.
const clientWords = (client: string | undefined) => (client === "console" ? "the console" : client || "a client Straza did not name");
const CHANGE_WORD: Record<string, string> = { created: "added", changed: "changed", removed: "removed" };
export const serverLine = (name: string, change: string, status: string, detail?: string) =>
  name + ": " + (CHANGE_WORD[change] || change) + ", " + status + "." + (detail ? " " + detail : "");
export const NEXT_TITLE = "Next";
export const UNDO = "Undo this publish";
export const UNDO_HELP = "Makes a new draft that restores the state before this publish.";
export const UNDO_SUBJECT = "Undo";
export const undoneToast = (newID: string, id: string) => "Draft " + newID + " takes back draft " + id + ".";

// A decided draft.
export const FILE_REFUSED = "The apps directory file could not become a draft.";

// Discard.
export const discardTitle = (id: string) => "Discard draft " + id + "?";
export const DISCARD_BODY = "The draft moves to Discarded with your reason.";
export const discardUnlinks = (server: string) => server + " keeps running, and its link to the file ends.";
export const REASON_LABEL = "Reason";
export const REASON_HINT = "Optional. Whoever drafted it reads it.";
export const DISCARD_GO = "Discard draft";
export const discardedToast = (id: string) => "Draft " + id + " is discarded.";
export const DISCARD_SUBJECT = "Discard";

// ---- the review page: the head ----

// The pill after the author's tag. An agent's pill says who sponsors it,
// anyone else's says the door the draft came in through.
export const SPONSOR_YOU = "you sponsor it";
export const sponsoredBy = (name: string) => "sponsored by " + name;
export const NO_SPONSOR = "no sponsor";
const DOOR_PILL: Record<string, string> = { console: "in the console", strazactl: "with strazactl", "straza-app": "through the straza app", "apps-directory": "from the apps directory", api: "through the API" };
export const doorPill = (door: string) => DOOR_PILL[door] || door;
export const TOKEN_PILL = "an admin API token";
export const UPGRADE_NAME = "Straza's upgrade";
export const UPGRADE_PILL = "a saved edit moved into a draft";
// agoPill is the age pill of a decided draft: published 5 h ago.
export const agoPill = (state: "published" | "discarded" | "expired", ago: string) => state + " " + ago;
export const FACT2 = { door: "Came from", revision: "Revision", checked: "Last checked", may: "May publish", expires: "Expires", undoes: "Undoes" };
export const MAY_YOU = "you";
export const MAY_NOT_YOU = "not you";

// ---- the band ----

export const passedChecks = (n: number) => (n === 1 ? "1 check passed" : n + " checks passed");
export const toConfirm = (n: number) => n + " to confirm";
// BAND_PILL says what stands between the reader and Publish.
export const BAND_PILL = {
  none: "nothing to confirm",
  risks: "confirmed when you publish",
  refused: "fix what is refused first",
  stale: "check it again first",
  waits: "waits for someone who may publish it",
};

// ---- In short ----

export const IN_SHORT = "In short";
// SHORT_DOES labels the row of each change, before and after the publish.
export const SHORT_DOES: Record<ChangeWhat, [open: string, published: string]> = {
  new: ["Adds", "Added"],
  changed: ["Changes", "Changed"],
  on: ["Turns on", "Turned on"],
  off: ["Turns off", "Turned off"],
  removed: ["Removes", "Removed"],
};
// SHORT_ALSO labels what a removal takes along without naming it as an item.
export const SHORT_ALSO: Record<"removed" | "changed", [open: string, published: string]> = {
  removed: ["Also removes", "Also removed"],
  changed: ["Also changes", "Also changed"],
};
export const SHORT_GIVES = "Gives";
export const SHORT_LOOSENS: [open: string, published: string] = ["Loosens", "Loosened"];
export const SHORT_AFFECTS = "Affects";
export const SHORT_HOLDERS_NOW = "Holders now";
export const ON = "on";
export const ruleCount = (n: number) => plural(n, "policy rule", "policy rules");
export const rulesChanged = (changed: number, total: number) => changed + " of " + ruleCount(total);
export const NOBODY_YET = "nobody yet";
export const holderCount = (n: number) => (n === 1 ? "1 holder" : n.toLocaleString("en-US") + " holders");
export const EVERYONE_PILL = "everyone";
export const EVERY_TOOL_PILL = "every tool";
export const assignRole = (role: string) => "Assign " + role;

// ---- the note ----

export const OWN_WORDS = { agent: "in its own words", person: "in their own words" };
export const SHOW_ALL = "Show all";
export const SHOW_LESS = "Show less";

// ---- What changes ----

export const CHANGED_TITLE = "What changed";
export const objectCount = (n: number) => (n === 1 ? "1 object" : n + " objects");
export const ALL_OR_NOTHING = "all or nothing";
export const KIND_PILL: Record<DraftKind, string> = { App: "MCP server", Role: "role", PolicySet: "policy" };
const ROLE_KIND_PILL: Record<string, string> = { application: "application role", business: "business role", approver: "approver role", straza: "Straza role" };
export const roleKindPill = (kind: string) => ROLE_KIND_PILL[kind] || KIND_PILL.Role;
export { VIEW_YAML } from "./save-words";
export const CARD = {
  holders: "Holders",
  behind: "Behind Straza",
  machine: "Person's machine",
  rule: "Policy rule",
  rules: "Policy rules",
  applies: "Applies to",
  recording: "Recording",
  after: "After publishing",
  afterPast: "After this publish",
};
export const NO_SERVER = "no server";
export const IN = "in";
export const moreCount = (n: number) => "+" + n + " more";
export const SHOW_FEWER = "Show fewer";
export const OFF_PILL = "off";
export const STAYS_STORED = "stays stored";
export const REMOVED_PILL = "removed";
// The table of a policy set: every policy rule of a new set, and only the
// changed ones of a changed set.
export const SET_COLUMN = { rule: "Policy rule", calls: "Calls", outcome: "Outcome" };
export const SET_CHANGE_COLUMN: Record<"open" | "published", { rule: string; what: string; now: string; after: string }> = {
  open: { rule: "Policy rule", what: "What changes", now: "Now", after: "After publishing" },
  published: { rule: "Policy rule", what: "What changed", now: "Before", after: "After this publish" },
};
export const ASPECT_OUTCOME = "Outcome";
export const ASPECT_SERVER = "Server";
export const ASPECT_OTHER = "Other fields";
export const SEE_YAML = "see the YAML";
export const showPattern = (n: number, open: boolean) => (open ? "Hide the " : "Show the ") + (n === 1 ? "pattern" : "patterns");
export const rulesUnchanged = (n: number) => ruleCount(n) + " unchanged";

// ---- the YAML sheet ----

export const yamlName = (name: string) => name + ".yaml";
export const YAML_SUB: Record<DraftState, string> = { open: "As it will be published.", published: "As it was published.", discarded: "As it was drafted.", expired: "As it was drafted." };
export const COPY = "Copy";
export const YAML_COPIED = "The YAML is copied.";

// ---- Checks ----

export const passedCount = (n: number) => n + " passed";
export const notesCount = (n: number) => (n === 1 ? "1 note" : n + " notes");
export const SHOW = "Show";
export const HIDE = "Hide";
export const STORED_WHEN: Record<"published" | "discarded" | "expired", string> = { published: "before publishing", discarded: "before it was discarded", expired: "before it expired" };
export const NOT_CHECKED_AGAIN: Record<"published" | "discarded" | "expired", string> = {
  published: "A published draft is not checked again.",
  discarded: "A discarded draft is not checked again.",
  expired: "An expired draft is not checked again.",
};
export const NO_STORED_CHECK = "no check stored";
export const revisionPill = (n: number) => "revision " + n;

// ---- after a decision ----

export const BY = "by";
export const VERSION = "version";
export const SIGNED = "signed";
export const fromClient = (client: string | undefined) => "from " + clientWords(client);
export const REPLACED = "Replaced";
export const THEN = "then";
