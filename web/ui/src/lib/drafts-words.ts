// The words of the Drafts area: the queue, the review page, the publish
// dialog, Check again, Discard and Undo. Only the lazy drafts screens
// import this file, so none of it lands in the console's entry page. The
// server's own sentences, a finding's words and a refusal's, are shown as
// the server answered them and never live here.
import type { ApiError, DraftDoor, DraftKind, DraftPrincipal, DraftState, FindingClass, GainOutcome } from "./api";
import type { ChangeCount, ChangeWhat, GainMark, RuleMark, Shown } from "./drafts-model";
import { list, plural } from "./policy-words";
import { WRITE_UNCONFIRMED } from "./say";
import { holders } from "./words";

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
export const CHECKS_HELP = "The counts of the server's last check. Each line that widens access is acknowledged by whoever publishes the draft.";
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
export const NOTHING_TO_ACK = "nothing to acknowledge";
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
export const FACT = { by: "Drafted by", door: "How it came in", revisions: "Revisions", checked: "Checked", expires: "Expires", undoes: "Undoes", needs: "Publishing needs" };
export const revisionLine = (n: number, who: string, door: string, at: string, mechanical: boolean) =>
  "rev " + n + " by " + who + " in " + doorWords(door) + " at " + at + (mechanical ? ", checked again against live state" : "");
export const checkedLine = (at: string, version: string) => "against the live state at " + at + (version ? ", policy version " + version : "");
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
export const needLine = (object: string, standing: string) => objectWords(object) + " needs " + bare(standing) + ".";

// The note: the proposer's own words, checked by nobody.
export const saysTitle = (agent: boolean, name: string) => (agent ? "What the agent says" : "What " + name + " says");
export const SAYS_AGENT_HELP = "The note the agent wrote with its draft. Written by the model and checked by nobody. Every other line on this page is computed by the server.";
export const saysPersonHelp = (name: string) => "The note " + name + " wrote with the draft, checked by nobody. Every other line on this page is computed by the server.";

// The verdict strip.
export const STRIP_LABEL = "The server's verdict";
export const stripWord: Record<"refused" | "risk" | "warning" | "unchecked", (n: number) => string> = {
  refused: () => "refused",
  risk: (n) => (n === 1 ? "widens access" : "widen access"),
  warning: (n) => (n === 1 ? "warning" : "warnings"),
  unchecked: () => "not checked",
};
export const stripJump = (n: number, word: string) => n + " " + word + ": go to the checks";

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
export const ITEM_KIND: Record<DraftKind, string> = { App: "MCP server", Role: "Role", PolicySet: "Approval set" };
// roleKindWords names a role's kind the way the Roles area does, with the
// server that owns it.
export function roleKindWords(kind: string, server: string): string {
  const k: Record<string, string> = { application: "Application role", business: "Business role", approver: "Approver role", straza: "Straza role" };
  const word = k[kind] || "Role";
  return server ? word + ", " + server + "'s own" : word;
}
export const MARK: Record<ChangeWhat, string> = { new: "new", changed: "changes", off: "turned off", removed: "removed" };
export const NOW_AFTER = { field: "Field", now: "Now", after: "After publishing" };
export const NOTHING_STARTS = "Nothing starts or connects before publishing.";
export const TOOLS_ROW = "Tools";
export const TOOLS_NOT_READ = "not read yet";
export const contactedLine = (host: string, at: string, n: number) => "Contacted " + host + " at " + at + ": " + plural(n, "tool", "tools") + " read.";
export const NOT_READABLE_DOC = "The document does not read as YAML, so this card shows it as text in The documents.";
export const reachesOn = (server: string) => "Reaches on " + server;
export const IMPLIES = "Implies";
export const DESCRIPTION = "Description";
export const NOTHING = "nothing";
export const toolsWords = (tools: string[]) => (tools.includes("*") ? "every tool, and tools added later" : tools.length ? tools.join(", ") : NOTHING);
export const TOOL_COLUMN = (server: string) => "Tool on " + server;
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
export const NO_FIELD_CHANGE = "No field the server page shows changes. The documents below hold the whole text.";
export const REMOVED_LINE = "Publishing removes it with everything live that belongs to it.";
export const REMOVED_LINE_PAST = "This publish removed it, with everything live that belonged to it.";

// Who gains what.
export const GAINS_TITLE = "Who gains what";
export const GAINS_HELP = "For each role the draft touches and each tool on its server: what a holder gets today and after publishing, in the words the policy pages use, with the holders named where you may read them. The server computes it with the policy engine over the published policy plus this draft.";
export const GAIN_COLUMN = { tool: "Tool", today: "Today", after: "After publishing" };
const OUTCOME: Record<GainOutcome, string> = { runs: "runs at once", "needs-approval": "needs approval", denied: "denied", "not-reachable": "not reachable", unknown: "not known" };
// outcomeWords is one cell of the table: the policy word, and the gate in
// the server's words when it named one.
export const outcomeWords = (o: GainOutcome, words?: string) => (OUTCOME[o] || o) + (words ? ": " + words : "");
export const GAIN_MARK: Record<GainMark, string> = { gains: "gains", loses: "loses", gate: "gate changes", same: "same", unknown: "not known" };
export function groupLine(role: string, server: string, names: string[] | undefined, count: number): string {
  const held = names && names.length ? "held by " + list(names) : count ? holders(count) : "held by nobody";
  return role + " on " + server + " · " + held;
}
export const NO_GAINS = "No role gains or loses a tool when this is published.";
export const GAINS_HIDDEN = "Part of who gains what is hidden from you.";
export const NOBODY_GAINS = "Nobody gains access when this is published.";
export const nobodyHolds = (roles: string[]) => "Nobody holds " + list(roles) + ". The table says what a holder gets once the identity manager or an admin assigns the role.";
export const holdersGain = (roles: string[]) => "Holders of " + list(roles) + " gain access when this is published.";
export const holdersLose = (roles: string[]) => "Holders of " + list(roles) + " lose tools they reach today, and their calls to them are refused after publishing.";
export const GAINS_FOOT = "Roles this draft does not touch lose and gain nothing.";

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
export const GROUP: Record<FindingClass, string> = { refused: "Refused", risk: "Widens access, acknowledged when you publish", warning: "Warnings", unchecked: "Not checked", passed: "Passed", info: "Notes" };
export const NOTHING_REFUSED = "Nothing refused.";
export const nowAfter = (before: string, after: string) => "Now: " + bare(before) + ". After publishing: " + bare(after) + ".";
// ackWords says how a risk is acknowledged at publish.
export function ackWords(ack: string | undefined, typed: string | undefined): string {
  if (ack !== "typed") return "Tick it when you publish: republishing the old state undoes it.";
  if (!typed) return "Only someone who can read what this line names can acknowledge it.";
  return "Type " + typed + " when you publish: republishing the old state cannot undo it.";
}
export const CONTACT = "Contact it now";
export const CONTACT_HELP = "Opens one MCP connection to the address with no credential and asks for its tools. Nothing starts, the tool names are kept with the draft, and the audit chain records who asked.";
export const CONTACT_FAILED = "The contact did not finish.";

// The documents.
export const DOCS_TITLE = "The documents";
export const docsFold = (n: number, revision: number) => plural(n, "document", "documents") + ", revision " + revision + ", as they would be published";
export const DOCS_MASKED = "A value the server keeps secret reads [REDACTED] here, so these documents are for reading and cannot be sent back as a draft.";
export const removalDoc = (object: string) => "Removes " + objectWords(object) + ".";

// The bar at the foot of the page.
export function ackBar(risks: number, warnings: number): string {
  if (risks === 1) return "1 line needs your acknowledgment when you publish.";
  if (risks > 1) return risks + " lines need your acknowledgment when you publish.";
  return warnings ? "Nothing to acknowledge. Read the warnings first." : "Nothing to acknowledge.";
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
export const typePrompt = (typed: string) => "Type " + typed + " to acknowledge it:";
export const typeLabel = (typed: string) => "Type " + typed;
export const CUT_TYPED = "You cannot type the text for this line, because it names something you cannot read.";
export const ACK_MISSING = "Acknowledge each line to publish. The first one left is outlined.";
export const typedWrong = (object: string | undefined, typed: string) =>
  "The text typed for " + objectWords(object || "this line") + " is not " + typed + ". Type " + typed + " exactly to acknowledge it.";
export const signLine = (agent: string | null) =>
  (agent ? "You sponsor " + agent + ", which proposed this draft. " : "") + "The publish records you, the console and your login, so a borrowed login would show.";
export const SIGN_LABEL = "Who the publish records";
export const SIGN_HELP = "Straza cannot tell your agent from you when it uses your sign-in. strazactl refuses config writes with your login inside a coding agent, and every publish names the client and the credential it came from.";
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
