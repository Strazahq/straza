// The words of the Drafts area: the queue, the review page, the publish
// dialog, Check again, Discard and Undo. Only the lazy drafts screens
// import this file, so none of it lands in the console's entry page. The
// server's own sentences, a finding's words and a refusal's, are shown as
// the server answered them and never live here.
import type { ApiError, DraftDoor, DraftKind, DraftObjectName, DraftPrincipal, DraftState, FindingClass, GainOutcome } from "./api";
import type { ObjectsMove } from "./drafts-access";
import { AGENT_LOOSEN_CODE, type ChangeCount, type ChangeWhat, type ConfirmBlock, type RuleMark, type Shown } from "./drafts-model";
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
export const CHECKS_HELP = "The counts of the server's last check. Each line that widens access is confirmed by whoever publishes the draft.";
export const shownCount = (shown: number, loaded: number) => shown + " of " + loaded + " shown";
export const loadedLine = (n: number) => plural(n, "draft", "drafts") + " loaded";
export const LOAD_MORE = "Load more";
export const LEGEND = "A draft changes nothing until a person publishes it. Discarding one keeps your reason with it, where whoever drafted it reads it.";
export const EMPTY_SEARCH = "No loaded draft matches the filter.";
export const EMPTY: Record<Tab, { title: string; body: string }> = {
  waiting: { title: "No draft waits for review", body: "Changes to servers, roles, access and policy sets land here as drafts, from the console, strazactl, an agent or the apps directory. Nothing is live until a person publishes one." },
  published: { title: "No draft was published yet", body: "Every publish lands here with who published it and when, so this tab is the change record." },
  discarded: { title: "No draft was discarded", body: "A discarded draft stays here with its reason, and nothing live changed." },
  expired: { title: "No draft expired", body: "An agent's open draft expires 14 days after its latest revision, and nothing live changes." },
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
  if (p.via === "file") return { name: "the file " + fileOf(source), line: "no person" };
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
export const NOT_CHECKED_HELP = "The server has not checked this revision yet. Opening the draft checks it.";

// decidedWords is the Decided cell of a published, discarded or expired row.
export function decidedWords(state: DraftState, who: string | undefined): string {
  if (state === "published") return "Published by " + (who || "someone");
  if (state === "discarded") return "Discarded by " + (who || "someone");
  return "Expired";
}

// ---- the review page ----

export const draftTitle = (id: string) => "Draft " + id;
// crumbLabel is the breadcrumb of a draft's page, after the area's name.
export const crumbLabel = (area: string, id: string) => area + " / " + draftTitle(id);
export const idChip = (id: string) => "draft " + id;
export const subjectDraft = (id: string) => "Draft " + id;
export const readingDraft = (id: string) => "Reading draft " + id + ".";
export const MISSING_TITLE = "This draft is not here";
export const OPEN_DRAFTS = "Open Drafts";
export const STATE_WORD: Record<Shown, string> = { ready: "Ready to publish", refused: "Refused", stale: "Out of date", published: "Published", discarded: "Discarded", expired: "Expired" };
// cameInLine says how a draft came in: the door, the file it came from,
// and for the straza app the harness the agent ran in.
export const cameInLine = (door: DraftDoor, source: string | undefined, client: string | undefined) =>
  doorWords(door) + (source ? ", " + source : "") + (door === "straza-app" && client ? ", from " + client : "");
export const FACT = { by: "Drafted by", door: "Came from", revisions: "Revisions", checked: "Last checked", expires: "Expires", undoes: "Undoes", needs: "Who may publish" };
// The meta line under a draft's title: who drafted it, what kind of author
// that is in brackets, the door, the revision, when, and the expiry, then
// the Details toggle that opens the facts.
export const META_DOT = "·";
export const kindOfAuthor = (line: string) => "(" + line + ")";
export const revisionWord = (n: number) => "revision " + n;
export const expiresWord = (day: string) => "expires " + day;
export const DETAILS = "Details";
export const revisionLine = (n: number, who: string, door: string, at: string, mechanical: boolean) =>
  "revision " + n + " by " + who + " in " + doorWords(door) + " at " + at + (mechanical ? ", checked again against live state" : "");
export const checkedLine = (at: string, version: string) => at + (version ? ", against live policy " + version : "");
export const expiresLine = (at: string) => at + ", unless someone revises it first";
export const undoesLine = (id: string) => "draft " + id;
export const MAY_PUBLISH_ALL = "You may publish all of it.";
export const WAITS_WHOLE = "A draft publishes whole, so it waits under Drafts for someone who may publish all of it.";
export const NO_NEEDS = "No object in it needs a grant to publish.";

const KIND_WORD: Record<DraftKind, string> = { App: "the server", Role: "the role", PolicySet: "the policy set" };

// objectWords names a Kind/Name object in words.
export function objectWords(object: string): string {
  const i = object.indexOf("/");
  const kind = object.slice(0, i) as DraftKind;
  return i > 0 && KIND_WORD[kind] ? KIND_WORD[kind] + " " + object.slice(i + 1) : object;
}
// needLine is one sentence of the Who may publish help: the grant one
// object needs.
export const needLine = (object: string, standing: string) => cap(objectWords(object)) + " needs " + bare(standing) + ".";

// LOOSEN_TAIL ends the red warning of an agent's draft that loosens the
// rules binding that agent, which agentWrote opens.
export const LOOSEN_TAIL = "A person may accept it when publishing.";

// The note: the proposer's own words, checked by nobody.
export const saysTitle = (agent: boolean, name: string) => (agent ? "What the agent says" : "What " + name + " says");
export const SAYS_AGENT_HELP = "The note the agent wrote with its draft. Written by the model and checked by nobody. Every other line on this page is computed by the server.";
export const saysPersonHelp = (name: string) => "The note " + name + " wrote with the draft, checked by nobody. Every other line on this page is computed by the server.";

// The verdict strip. It counts only the classes that hold a line, and says
// NOTHING_TO_CONFIRM when none does.
export const STRIP_LABEL = "The server's verdict";
export const stripWord: Record<"refused" | "risk" | "warning" | "unchecked", (n: number) => string> = {
  refused: () => "refused",
  risk: (n) => (n === 1 ? "line to confirm" : "lines to confirm"),
  warning: (n) => (n === 1 ? "warning" : "warnings"),
  unchecked: () => "not checked",
};
export const stripJump = (n: number, word: string) => n + " " + word + ": go to the checks";
export const NOTHING_TO_CONFIRM = "Nothing to confirm";

// Before you publish: the band under the head of an open draft.
export const BAND_LABEL = "Before you publish";
export const GO_TO_CHECKS = "Go to the checks";

// In this draft: one row per item, above the cards.
export const IN_DRAFT = "In this draft";
export const inDraftCount = (n: number) => (n === 1 ? "1 object" : n + " objects, published together or not at all");
export const IN_DRAFT_COLUMN = { kind: "Kind", name: "Name", change: "Change", does: "What publishing does" };

// What changes.
export const CHANGES_TITLE = "What changes";
const KIND_COUNTED: Record<DraftKind, [string, string]> = { App: ["MCP server", "MCP servers"], Role: ["role", "roles"], PolicySet: ["policy set", "policy sets"] };
export const ITEM_KIND: Record<DraftKind, string> = { App: "MCP server", Role: "Role", PolicySet: "Policy set" };
const ROLE_KIND: Record<string, string> = { application: "Application role", business: "Business role", approver: "Approver role", straza: "Straza role" };
// roleKind names a role's kind as the Roles area does.
export const roleKind = (kind: string) => ROLE_KIND[kind] || ITEM_KIND.Role;
export const ownedBy = (server: string) => "owned by " + server;
// roleKindWords names a role's kind on its card, with the server that owns
// it when one does.
export const roleKindWords = (kind: string, server: string) => roleKind(kind) + (server ? " · " + ownedBy(server) : "");
export const MARK: Record<ChangeWhat, string> = { new: "new", changed: "changes", off: "turned off", removed: "removed" };
// holdersChip says who holds a role the draft touches.
export const holdersChip = (n: number) => (n === 0 ? "nobody holds it yet" : n === 1 ? "1 person holds it" : n.toLocaleString("en-US") + " people hold it");
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
// fileFold heads the fold of a card's file: as it will be published while
// the draft is open, else as it was published or drafted.
export const fileFold = (name: string, state: DraftState) =>
  name + ".yaml, as it " + (state === "open" ? "will be published" : state === "published" ? "was published" : "was drafted");
export const FILE_MASKED = "A value the server keeps secret reads [REDACTED] here.";
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
// rulesLine counts a policy set's rules that are new, edited and removed.
export function rulesLine(c: { new: number; edited: number; removed: number }): string {
  const parts = (["new", "edited", "removed"] as const).filter((k) => c[k]).map((k) => plural(c[k], "rule", "rules") + " " + k);
  return parts.length ? cap(list(parts)) + "." : "Changes no rule.";
}

export const NOW_AFTER = { field: "Field", now: "Now", after: "After publishing" };
export const NOTHING_STARTS = "Nothing starts or connects before publishing.";
export const TOOLS_ROW = "Tools";
export const TOOLS_NOT_READ = "not read yet";
export const contactedLine = (host: string, at: string, n: number) => "Contacted " + host + " at " + at + ": " + plural(n, "tool", "tools") + " read.";
export const NOT_READABLE_DOC = "The document does not read as YAML, so the file below shows it as text.";
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
export const RULE_COLUMN = { rule: "Rule", does: "What it does" };
export const RULE_MARK: Record<RuleMark, string> = { new: "new", edited: "edited", removed: "removed", same: "" };
export const OFF_LINE = "Off after publishing: the set stays stored and gates nothing.";
export const GOES_WITH = "What goes with it";
export const GOES_WITH_ALL = "What goes with the removals";
export const NO_FIELD_CHANGE = "No field the server page shows changes.";
export const NO_ROLE_CHANGE = "No field the role page shows changes.";
export const NO_OTHER_FIELD = "No other field changes.";
export const REMOVED_LINE = "Publishing removes it with everything live that belongs to it.";
export const REMOVED_LINE_PAST = "This publish removed it, with everything live that belonged to it.";

// Access after publishing.
export const GAINS_TITLE = "Access after publishing";
export const GAINS_HELP = "What a holder of each role the draft touches gets, now and after publishing. The server computes it with the policy engine.";
export const GAIN_COLUMN = { tool: "Tool", now: "Now", after: "After publishing" };
// OUTCOME is the chip of one cell, in the words the policy pages use.
export const OUTCOME: Record<GainOutcome, string> = { runs: "allowed", "needs-approval": "needs approval", denied: "denied", "not-reachable": "no access", unknown: "not known" };
// outcomeFoot says a gate's words once under the table, after the chip
// word it explains.
export const outcomeFoot = (o: GainOutcome, words: string) => (OUTCOME[o] || o) + ": " + bare(words) + ".";
// people counts the holders of a role in the console's words for people,
// and holdsWords says it as a sentence.
const people = (n: number) => (n === 1 ? "1 person" : n.toLocaleString("en-US") + " people");
export const holdsWords = (n: number) => people(n) + (n === 1 ? " holds it." : " hold it.");
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
export const holdersLose = (roles: string[]) => "Holders of " + list(roles) + " lose tools they reach today, and their calls to them are refused after publishing.";

// Checks.
export const CHECKS_TITLE = "Checks";
export const checksLede = (at: string) => "Checked against the live state at " + at + ". The server checks again when you publish.";
// A draft that is not open is never checked again, so its page says what
// the server's last check of its revision counted, in the queue's words.
export const checkCounts = (c: { refused: number; risks: number; warnings: number; unchecked: number }) =>
  [c.refused ? refusedCount(c.refused) : "", c.risks ? widenCount(c.risks) : "", !c.refused && !c.risks ? NOTHING_TO_ACK : "", c.warnings ? warningCount(c.warnings) : "", c.unchecked ? uncheckedCount(c.unchecked) : ""].filter(Boolean).join(", ");
export const storedCheckLine = (revision: number, at: string, counts: string) => "Revision " + revision + " was checked against the live state at " + at + ": " + counts + ".";
export const storedNoCheck = (revision: number) => "Straza stored no check of revision " + revision + ".";
export const notCheckedAgain = (state: string) => "Straza does not check a draft again once it is " + state + ".";
// publishedUnread stands for the stored check when a publish landed and the
// page's read of the draft after it failed.
export const publishedUnread = (id: string) => "Draft " + id + " is published. This page could not read it again, so its stored check is not shown. Reload to try again.";
export const GROUP: Record<FindingClass, string> = { refused: "Refused", risk: "Widens access", warning: "Warnings", unchecked: "Not checked", passed: "Passed", info: "Notes" };
// riskTail follows the count in the heading of the lines that widen access.
export const riskTail = (n: number) => (n === 1 ? "you confirm it when you publish" : "you confirm them when you publish");
export const nowAfter = (before: string, after: string) => "Now: " + bare(before) + ". After publishing: " + bare(after) + ".";
// ackWords says how the publish dialog asks to confirm a risk, by the
// confirm block that holds it: a tick, the text to type, or the count of
// the lines of its kind typed once.
export function ackWords(b: ConfirmBlock): string {
  const n = b.lines.length;
  if (b.blocked) return "Only someone who may read what this line names can confirm it, so ask an admin who may read it to publish this draft.";
  if (b.kind === "tick") return n === 1 ? "Tick it when you publish: republishing the old state undoes it." : "One tick confirms the " + n + " lines of this kind when you publish: republishing the old state undoes them.";
  if (b.kind === "count") return "Type " + b.expect + " when you publish, once for the " + n + " lines of this kind: republishing the old state cannot undo them.";
  if (b.lines.some((f) => f.code === AGENT_LOOSEN_CODE)) return "Type " + b.expect + " when you publish to accept it.";
  return "Type " + b.expect + " when you publish: republishing the old state cannot undo it.";
}
export const CONTACT = "Contact it now";
export const CONTACT_HELP = "Opens one MCP connection to the address with no credential and asks for its tools. Nothing starts, the tool names are kept with the draft, and the audit chain records who asked.";
export const CONTACT_FAILED = "The contact did not finish.";

// ackBar is the band's line: what to do before Publish.
// It counts the confirmations the way the publish dialog asks for them,
// one per confirm block, and names them lines while every block holds one
// line.
export function ackBar(blocks: number, lines: number, warnings: number): string {
  const read = warnings ? "Read " + warningCount(warnings) : "";
  if (!blocks) return read ? read + " before you publish." : NOTHING_TO_CONFIRM + ".";
  const asks = blocks === lines
    ? (blocks === 1 ? "1 line needs" : blocks + " lines need") + " your confirmation when you publish."
    : "Publishing asks you for " + plural(blocks, "confirmation", "confirmations") + ".";
  return read ? read + " first. " + asks : asks;
}
export const REFUSED_BAR = "Fix what is refused before publishing.";
export const STALE_BAR = "Check the draft again before publishing.";
export const PUBLISH_OPEN = "Publish…";
export const PUBLISH_NOT_YET = "The server refuses to publish a draft with a refusal. Its lines under Checks say what to fix.";
export const DISCARD = "Discard";
export const DISCARD_KEEP_LIVE = "Discard: keep live as it is";
export const discardKeep = (server: string) => "Discard: keep " + server;

// Out of date, and Check again.
export const STALE_TITLE = "Out of date.";
export const STALE_TAIL = "Publish is refused until the draft is checked again.";
export const CHECK_AGAIN = "Check again";
export const checkedAgain = (id: string) => "Checked draft " + id + " again against the live state.";
export const CHECK_AGAIN_SUBJECT = "Check again";
export const PICKS_TITLE = "Pick the values to keep";
export const PICKS_BODY = "The draft and live state both changed these fields since the draft was checked. Pick one value for each, then check again.";
// valueWords is a picked value in words: a field the side left empty reads
// unset.
const valueWords = (v: string) => (v === "" ? "unset" : v);
export const keepDraft = (v: string) => "Keep the draft's value, " + valueWords(v);
export const keepLive = (v: string) => "Keep live's value, " + valueWords(v);
export const pickWas = (base: string) => "It was " + valueWords(base) + " when the draft was checked.";
// A field whose values run over several lines, such as a set's text, is
// picked by its side, with each text in a fold under its choice.
export const KEEP_DRAFT_TEXT = "Keep the draft's text";
export const KEEP_LIVE_TEXT = "Keep live's text";
export const BASE_TEXT = "The text when the draft was checked";
export const SHOW_TEXT = "Show the text";
export const PICKS_MISSING = "Pick a value for every field.";
export const PICKS_GO = "Check again with these values";

// The publish dialog.
export const publishTitle = (id: string) => "Publish draft " + id + "?";
const VERB: Record<ChangeWhat, string> = { new: "adds", changed: "changes", off: "turns off", removed: "removes" };
// goLive is the lead of the publish dialog: what publishing does to each
// kind, by what it does, so a removal never reads as going live.
export function goLive(counts: ChangeCount[]): string {
  const phrases = (Object.keys(VERB) as ChangeWhat[])
    .map((what) => counts.filter((c) => c.what === what))
    .filter((cs) => cs.length)
    .map((cs) => VERB[cs[0].what] + " " + list(cs.map((c) => plural(c.n, ...KIND_COUNTED[c.kind]))));
  return phrases.length ? "Publishing " + list(phrases) + " in one step, or nothing changes." : "Publishing changes nothing.";
}
export const NOBODY_UNTIL = "Nobody gains access until the roles are assigned.";

// Does is what publishing does to the objects a risk line names: a verb,
// then the noun it counts in the singular and the plural.
export type Does = [verb: string, one: string, many: string];

// PUBLISH_DOES words what publishing does, for the lead of the publish
// dialog and the heading of each confirm block. A line is worded by its
// risk code, else as unread when the server left out its object for this
// reader, else by the kind of the object it names, else by other. Lines of
// one code name distinct objects, but unread lines of several codes may
// name one object, so those count as lines. Every risk widens access, so
// the fallbacks say so. strazactl's publish prompt copies this table, so
// a change here goes there too.
export const PUBLISH_DOES: { code: Record<string, Does>; kind: Record<string, Does>; unread: Does; other: Does } = {
  code: {
    "server.removal": ["removes", "MCP server", "MCP servers"],
    "policy.guardrail-changed": ["loosens", "policy set", "policy sets"],
    "agent.guardrail": ["loosens", "policy set", "policy sets"],
    "role.covers-wider": ["widens", "role", "roles"],
  },
  kind: {
    App: ["widens access through", "MCP server", "MCP servers"],
    Role: ["widens", "role", "roles"],
    PolicySet: ["widens access through", "policy set", "policy sets"],
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

const doesCount = (does: Does, n: number) => does[0] + " " + plural(n, does[1], does[2]);

// publishLead is the lead of the publish dialog over the risk lines of a
// verdict, in the order of their blocks: one phrase per verb and noun,
// each counting the objects its lines name, a line with no object counted
// alone.
export function publishLead(lines: { code: string; object?: string; key: string }[]): string {
  const said = new Map<string, { does: Does; objects: Set<string> }>();
  for (const f of lines) {
    const does = doesOf(f.code, f.object);
    const p = said.get(does.join("\n")) || { does, objects: new Set<string>() };
    p.objects.add(f.object || f.key);
    said.set(does.join("\n"), p);
  }
  const phrases = [...said.values()].map((p) => doesCount(p.does, p.objects.size));
  return "Publishing " + list(phrases) + (phrases.length > 1 ? ", in one step" : "") + ", or nothing changes.";
}

// blockTitle heads a confirm block: what publishing does to the n objects
// its lines name.
export const blockTitle = (does: Does, n: number) => {
  const s = doesCount(does, n);
  return s.charAt(0).toUpperCase() + s.slice(1);
};
// showLines opens the fold of a block's lines.
export const showLines = (does: Does, n: number) => "Show the " + plural(n, does[1], does[2]);
// readLines is the one tick of a block of several tick lines.
export const readLines = (n: number) => "I read the " + n + " lines.";
export const AGENT_LOOSENS = "The agent loosens its own rules";
export const agentWrote = (agent: string | null) => (agent || "An agent") + " wrote this draft to loosen its own rules.";
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
// signLine says who the publish record names: the publisher, as the person
// who accepted an agent's self-loosening when the draft holds one. To the
// sponsor of the agent that proposed the draft, it also says that a draft
// with a risk needs another admin while second-person publishing is on.
export function signLine(sponsored: string | null, accepts: boolean, risky: boolean): string {
  const names = accepts ? "The publish record names you as the person who accepted it." : "The publish record names you.";
  if (!sponsored) return names;
  return "You sponsor " + sponsored + (risky ? ". When second-person publishing is on, another admin must publish this. " : ", which proposed this draft. ") + names;
}
export const SIGN_LABEL = "Who the publish records";
export const SIGN_HELP = "It also records the console and your login, so a borrowed login would show. Straza cannot tell your agent from you when it uses your sign-in. strazactl refuses config writes with your login inside a coding agent, and every publish names the client and the credential it came from.";
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
    ? names[0] + " has no secret yet, and every call to it fails until one is stored. Enter it here, and Straza stores it right after the publish."
    : list(names) + " have no secret yet, and every call to them fails until one is stored. Enter each here, and Straza stores them right after the publish.";
export const secretLabel = (name: string) => "Secret of " + name;
// secretHint names the agent that wrote the draft, which never sees the
// secret, and leaves it out of a draft only people wrote.
export const secretHint = (agent: string | null) =>
  "Straza stores it encrypted. It is never shown again and never returned by any read." + (agent ? " " + agent + ", the agent that wrote this draft, never sees it." : "");
export const ADD_LATER = "Add it later";
export const laterLine = (name: string) => name + " goes live with no secret, and every call to it fails until one is stored. The page after the publish says how to store it.";
export const enterOr = (name: string) => "Enter the secret of " + name + ", or tick Add it later.";
// publishGo is the dialog's button: Publish alone when no secret is stored.
export const publishGo = (n: number) => (n === 0 ? PUBLISH : n === 1 ? "Publish and store the secret" : "Publish and store " + n + " secrets");
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
  "Draft " + id + " is published and stays published, so " + name + " is live with no secret, and every call to it fails until one is stored. The reason Straza gave: \"" + bare(reason) + "\". Enter the secret again below. Nothing is published again.";
export const maybeStored = (name: string) => "The secret of " + name + " may not be stored.";
export const maybeText = (id: string, name: string) =>
  "Draft " + id + " is published and stays published. strazad is unreachable, so the secret may or may not be stored. Check the secret on the page of " + name + " before you enter it again.";
export const retryHint = (name: string) => name + " is live. Storing the secret runs the check under it, and nothing is published again.";
export const againHint = (name: string) => name + " is live. Storing the secret again replaces the sealed value, then the check runs under it.";
export const storedNotPassed = (name: string) => "The secret of " + name + " is stored, and the check that ran under it did not pass.";
export const CLOSE = "Close";

// The listing credential of a server that pins an object type, which only
// Straza's own listing call carries. pin is the type, its plural and the
// tool that lists them.
type Pin = { type: string; plural: string; tool: string };
export const SECRET_LINE_BOTH = "Neither its secret nor its listing credential is stored yet. Whoever publishes this draft enters both in the publish dialog.";
export const leadBoth = (name: string, pin: Pin) =>
  name + " has no secret and no listing credential yet, so every call to it fails and Straza cannot list its " + pin.plural + ". Enter both here, and Straza stores them right after the publish.";
export const leadListing = (name: string, pin: Pin) =>
  name + " has no listing credential yet, so Straza cannot list its " + pin.plural + ". Enter it here, and Straza stores it right after the publish.";
export const listingLabel = (name: string) => "Listing credential of " + name;
export const listingHint = (name: string, pin: Pin, agent: string | null) =>
  "Only Straza's own listing of the " + pin.plural + " of " + name + " carries it, when Straza calls " + pin.tool + ". Calls by people and agents never carry it. Straza stores it encrypted and never shows it again." +
  (agent ? " " + agent + ", the agent that wrote this draft, never sees it." : "");
export const laterListing = (name: string, pin: Pin) =>
  name + " goes live with no listing credential, so Straza cannot list its " + pin.plural + ", and only roles on every " + pin.type + " can use its " + pin.type + " tools until it has one. Set one later under Server authentication on the Overview of " + name + ".";
export const enterListingOr = (name: string) => "Enter the listing credential of " + name + ", or tick Add it later.";
export const GO_LISTING = "Publish and store the listing credential";
export const storeListingRow = (name: string) => "Store the listing credential of " + name;
export const listsSoon = (name: string, pin: Pin) => "Straza lists the " + pin.plural + " of " + name + " within about 30 seconds.";
export const listingNotStored = (name: string) => "The listing credential of " + name + " was not stored.";
// listingNotStoredText says the server's calls work when its secret is
// stored, which a server whose secret this dialog did not store may not.
export const listingNotStoredText = (id: string, name: string, pin: Pin, secretStored: boolean, reason: string) =>
  "Draft " + id + " is published and stays published" + (secretStored ? ", and the secret of " + name + " is stored, so its calls work" : "") + ". Straza cannot list its " + pin.plural + ", and only roles on every " + pin.type + " can use its " + pin.type +
  " tools until it has one. The reason Straza gave: \"" + bare(reason) + "\". Enter it again below. Nothing is published again.";
export const listingMaybeStored = (name: string) => "The listing credential of " + name + " may not be stored.";
export const listingMaybeText = (id: string, name: string) =>
  "Draft " + id + " is published and stays published. strazad is unreachable, so the listing credential may or may not be stored. Check Server authentication on the Overview of " + name + " before you enter it again.";
export const listingRetryHint = (name: string) => name + " is live. Storing the listing credential starts a listing at once, and nothing is published again.";
export const STORE_LISTING = "Store the listing credential";

// After publishing.
const clientWords = (client: string | undefined) => (client === "console" ? "the console" : client || "a client Straza did not name");
export const publishedLine = (who: string, at: string, client: string | undefined, id: string) =>
  "Published by " + who + " at " + at + " from " + clientWords(client) + ". Draft " + id + " is live.";
const CHANGE_WORD: Record<string, string> = { created: "added", changed: "changed", removed: "removed" };
export const serverLine = (name: string, change: string, status: string, detail?: string) =>
  name + ": " + (CHANGE_WORD[change] || change) + ", " + status + "." + (detail ? " " + detail : "");
export const NEXT_TITLE = "Next";
export const UNDO = "Undo this publish";
export const UNDO_HELP = "Makes a new draft with the state before this publish. It is checked like any other draft, and it lists what cannot come back, such as each person's connection to a server.";
export const UNDO_SUBJECT = "Undo";
export const undoneToast = (newID: string, id: string) => "Draft " + newID + " takes back draft " + id + ". Review it, then publish it.";

// A decided draft.
export const discardedLine = (who: string, at: string) => "Discarded by " + who + " at " + at + ". Nothing live changed.";
export const reasonLine = (reason: string) => "Reason: " + reason;
export const expiredLine = (at: string) => "Expired at " + at + ". Nothing live changed.";
export const FILE_REFUSED = "The apps directory file could not become a draft.";

// Discard.
export const discardTitle = (id: string) => "Discard draft " + id + "?";
export const DISCARD_BODY = "Nothing live changes. The draft moves to Discarded with your reason.";
export const discardUnlinks = (server: string) => server + " keeps running, and its link to the file ends.";
export const REASON_LABEL = "Reason";
export const REASON_HINT = "Optional. Whoever drafted it reads it, and the draft and its audit record keep it.";
export const DISCARD_GO = "Discard draft";
export const discardedToast = (id: string) => "Draft " + id + " is discarded. Nothing live changed.";
export const DISCARD_SUBJECT = "Discard";
