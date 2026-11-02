// The words of the Drafts area: the queue, the review page, the publish
// dialog, Check again, Discard and Undo. Only the lazy drafts screens
// import this file, so none of it lands in the console's entry page. The
// server's own sentences, a finding's words and a refusal's, are shown as
// the server answered them and never live here.
import type { ApiError, DraftCoverage, DraftDoor, DraftKind, DraftObjectName, DraftPrincipal, DraftState, FindingClass, GainOutcome } from "./api";
import type { CoverLead, Lead, ObjectsMove } from "./drafts-access";
import { AGENT_LOOSEN_CODE, type ChangeCount, type ChangeWhat, type ConfirmBlock, type RuleMark, type Shown } from "./drafts-model";
import { type Nouns, cap, counted } from "./object-words";
import { list, orList, plural } from "./policy-words";
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
  waiting: { title: "No draft waits for review", body: "Changes to servers, roles, access and approval sets land here as drafts, from the console, strazactl, an agent or the apps directory. Nothing is live until a person publishes one." },
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
export const revisionLine = (n: number, who: string, door: string, at: string, mechanical: boolean) =>
  "rev " + n + " by " + who + " in " + doorWords(door) + " at " + at + (mechanical ? ", checked again against live state" : "");
export const checkedLine = (at: string, version: string) => at + (version ? ", against live policy " + version : "");
export const expiresLine = (at: string) => at + ", unless someone revises it first";
export const undoesLine = (id: string) => "draft " + id;
export const MAY_PUBLISH_ALL = "You may publish all of it.";
export const WAITS_WHOLE = "A draft publishes whole, so it waits under Drafts for someone who may publish all of it.";
export const NO_NEEDS = "No object in it needs a grant to publish.";

const KIND_WORD: Record<DraftKind, string> = { App: "the server", Role: "the role", PolicySet: "the approval set" };

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

// What changes.
export const CHANGES_TITLE = "What changes";
const KIND_COUNTED: Record<DraftKind, [string, string]> = { App: ["MCP server", "MCP servers"], Role: ["role", "roles"], PolicySet: ["approval set", "approval sets"] };
const countPhrase = (c: ChangeCount) => {
  const noun = plural(c.n, ...KIND_COUNTED[c.kind]);
  if (c.what === "new") return noun.replace(/^(\d+) /, "$1 new ");
  if (c.what === "changed") return noun.replace(/^(\d+) /, "$1 changed ");
  return noun + (c.what === "off" ? " turned off" : " removed");
};
export const changesLede = (counts: ChangeCount[]) => (counts.length ? list(counts.map(countPhrase)) + ". They go live together or not at all." : "This draft holds no object.");
export const changesLedePublished = (counts: ChangeCount[]) => (counts.length ? list(counts.map(countPhrase)) + ". They went live together." : "This draft held no object.");
export const ITEM_KIND: Record<DraftKind, string> = { App: "MCP server", Role: "role", PolicySet: "approval set" };
// roleKindWords names a role's kind on its card: the server that owns it,
// or the kind the Roles area names when no server owns it.
export function roleKindWords(kind: string, server: string): string {
  const k: Record<string, string> = { application: "application role", business: "business role", approver: "approver role", straza: "Straza role" };
  return server ? "role · owned by " + server : k[kind] || "role";
}
export const MARK: Record<ChangeWhat, string> = { new: "new", changed: "changes", off: "turned off", removed: "removed" };
export const NOW_AFTER = { field: "Field", now: "Now", after: "After publishing" };
export const NOTHING_STARTS = "Nothing starts or connects before publishing.";
export const TOOLS_ROW = "Tools";
export const TOOLS_NOT_READ = "not read yet";
export const contactedLine = (host: string, at: string, n: number) => "Contacted " + host + " at " + at + ": " + plural(n, "tool", "tools") + " read.";
export const NOT_READABLE_DOC = "The document does not read as YAML, so this card shows it as text under Files.";
// toolsOn labels a role's tools: Tools alone when the role reaches one
// server, which its card names, else the server too.
export const toolsOn = (server: string, one: boolean) => (one ? TOOLS_ROW : "Tools on " + server);
export const IMPLIES = "Implies";
export const DESCRIPTION = "Description";
export const NOTHING = "nothing";
export const EVERY_TOOL = "every tool, and tools added later";
export const toolsWords = (tools: string[]) => (tools.includes("*") ? EVERY_TOOL : tools.length ? tools.join(", ") : NOTHING);
// toolsSame is a role's tools row when no tool joins or leaves it.
export const toolsSame = (tools: string[]) => (tools.includes("*") ? EVERY_TOOL : plural(tools.length, "tool", "tools")) + ", unchanged";
export const TOOL_COLUMN = (server: string) => "Tool on " + server;
// The objects a role covers, in its server's own words.
export const objectsLabel = (w: Nouns) => cap(w.plural);
export const everyType = (w: Nouns) => "every " + w.type;
export const addedMark = (value: string) => "+ " + value;
export const REACHED = "reached";
export const NOT_REACHED = "not reached";
export const JOINS = "joins the role";
export const LEAVES = "leaves the role";
export const RULE_COLUMN = { rule: "Rule", does: "What it does" };
export const RULE_MARK: Record<RuleMark, string> = { new: "new", edited: "edited", removed: "removed", same: "" };
export const setFold = (name: string) => name + ".yaml, as it would be published";
export const OFF_LINE = "Off after publishing: the set stays stored and gates nothing.";
export const GOES_WITH = "What goes with it";
export const GOES_WITH_ALL = "What goes with the removals";
export const NO_FIELD_CHANGE = "No field the server page shows changes. The files below hold the whole text.";
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
export const NOBODY_GAINS = "Nobody gains access when this is published.";
export const holdersGain = (roles: string[]) => "Holders of " + list(roles) + " gain access when this is published.";
export const holdersLose = (roles: string[]) => "Holders of " + list(roles) + " lose tools they reach today, and their calls to them are refused after publishing.";

// The lead of the page: what publishing does to the objects of each role
// on a server that pins a type, then who holds the roles it touches.
export const NOBODY_YET = "Nobody holds it yet, so nobody gains access until the identity manager assigns it.";
export function nobodyYet(roles: string[], hidden: boolean): string {
  if (hidden) return "Nobody holds " + orList(roles) + " yet.";
  return "Nobody holds " + orList(roles) + " yet, so nobody gains access until the identity manager assigns " + (roles.length === 1 ? "it." : "them.");
}
const more = (n: number, w: Nouns) => counted(n, w).replace(/^([\d,]+) /, "$1 more ");
const everyLater = (w: Nouns) => everyType(w) + ", including " + w.plural + " added later";
// coverLead says what publishing does to one role's objects.
export function coverLead(l: CoverLead, w: Nouns): string {
  const c: DraftCoverage = l.coverage;
  const m = l.move;
  if (l.isNew) return "Adds the role " + c.role + " on " + c.server + ", for " + (c.every_after ? everyLater(w) : coveredWords(w, false, c.after)) + ".";
  const head = "Changes the role " + c.role + ": it will ";
  if (m.every === "gained") return head + "reach " + everyType(w) + " on " + c.server + ", including " + w.plural + " added later.";
  if (m.every === "lost") return head + "reach only " + counted(c.after.length, w) + " on " + c.server + " instead of " + everyType(w) + (c.after.length ? ": " + objectNames(c.after) : "") + ".";
  const add = m.added.length ? "reach " + more(m.added.length, w) + " on " + c.server + ", " + objectNames(m.added) : "";
  const drop = m.dropped.length ? "stop reaching " + counted(m.dropped.length, w) + (add ? "" : " on " + c.server) + ", " + objectNames(m.dropped) : "";
  return head + (add && drop ? add + ", and " + drop : add || drop) + ".";
}
// leadLines are the lead's sentences in order, each marked when it says
// that holders lose what they reach today.
export function leadLines(lead: Lead, nouns: (c: DraftCoverage) => Nouns, hidden: boolean): { text: string; loses?: boolean }[] {
  const out: { text: string; loses?: boolean }[] = [];
  for (const l of lead.covers) {
    out.push({ text: coverLead(l, nouns(l.coverage)) });
    out.push({ text: l.coverage.holders_count ? holdsWords(l.coverage.holders_count) : NOBODY_YET });
  }
  if (lead.gain.length) out.push({ text: holdersGain(lead.gain) });
  if (lead.nobody.length) out.push({ text: nobodyYet(lead.nobody, hidden) });
  if (lead.nobodyGains) out.push({ text: NOBODY_GAINS });
  if (lead.lose.length) out.push({ text: holdersLose(lead.lose), loses: true });
  return out;
}

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
  if (b.blocked) return "Only someone who can read what this line names can confirm it, so ask an admin who can read it to publish this draft.";
  if (b.kind === "tick") return n === 1 ? "Tick it when you publish: republishing the old state undoes it." : "One tick confirms the " + n + " lines of this kind when you publish: republishing the old state undoes them.";
  if (b.kind === "count") return "Type " + b.expect + " when you publish, once for the " + n + " lines of this kind: republishing the old state cannot undo them.";
  if (b.lines.some((f) => f.code === AGENT_LOOSEN_CODE)) return "Type " + b.expect + " when you publish to accept it.";
  return "Type " + b.expect + " when you publish: republishing the old state cannot undo it.";
}
export const CONTACT = "Contact it now";
export const CONTACT_HELP = "Opens one MCP connection to the address with no credential and asks for its tools. Nothing starts, the tool names are kept with the draft, and the audit chain records who asked.";
export const CONTACT_FAILED = "The contact did not finish.";

// Files.
export const DOCS_TITLE = "Files";
// docsFold counts the files: as they will be published while the draft is
// open, else as they were published or drafted.
export function docsFold(n: number, state: DraftState): string {
  const [it, was] = n === 1 ? ["it", "was"] : ["they", "were"];
  return plural(n, "file", "files") + ", as " + it + (state === "open" ? " will be published" : " " + was + (state === "published" ? " published" : " drafted"));
}
export const DOCS_MASKED = "A value the server keeps secret reads [REDACTED] here, so these files are for reading and cannot be sent back as a draft.";
export const removalDoc = (object: string) => "Removes " + objectWords(object) + ".";

// ackBar is the bar at the foot of the page: what to do before Publish.
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
    "policy.guardrail-changed": ["loosens", "approval set", "approval sets"],
    "agent.guardrail": ["loosens", "approval set", "approval sets"],
    "role.covers-wider": ["widens", "role", "roles"],
  },
  kind: {
    App: ["widens access through", "MCP server", "MCP servers"],
    Role: ["widens", "role", "roles"],
    PolicySet: ["widens access through", "approval set", "approval sets"],
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
