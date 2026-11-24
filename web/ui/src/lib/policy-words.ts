// The sentences of the Policies area: the list and its By role view, a
// policy's page and its tabs, the rule cards, Test a call, the New policy
// wizard and the publish dialog. Every surface sentence is short; the
// sentence that explains a thing sits behind its help icon (HelpTip) or in
// its dialog, never as prose on a table or a sheet.
import type { Bucket, Lane, NewLane, Posture, Postures, RuleView, Who } from "./policy-model";
import { CHANGE_WORD, OUTCOME_LABEL, OUTCOME_WORD, type Outcome } from "./chip-words";

// ---- shared ----

export const list = (xs: string[]): string => (xs.length < 2 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);
export const orList = (xs: string[]): string => (xs.length < 2 ? xs.join("") : xs.slice(0, -1).join(", ") + " or " + xs[xs.length - 1]);
export const plural = (n: number, one: string, many: string) => n + " " + (n === 1 ? one : many);

// durationWords says a window in natural units: a day, 2 hours, 90 seconds.
export function durationWords(seconds: number): string {
  const unit = (n: number, one: string, an: string) => (n === 1 ? an : n + " " + one + "s");
  if (seconds > 0 && seconds % 86400 === 0) return unit(seconds / 86400, "day", "a day");
  if (seconds > 0 && seconds % 3600 === 0) return unit(seconds / 3600, "hour", "an hour");
  if (seconds > 0 && seconds % 60 === 0) return unit(seconds / 60, "minute", "a minute");
  return seconds + " seconds";
}

export const SPONSOR_WORDS = "the agent's sponsor";
// The pills of a gate after its outcome chip, where howLine says them in a
// line: the requester confirming, a check before the call runs, a ticket,
// the requester as a decider, an approval for any arguments, and the hover
// of the window to use or retry an approval.
export const REQUESTER_CONFIRMS = "the requester confirms first";
export const CHECKED_FIRST = "checked before it runs";
export const TICKET_PILL = "ticket";
export const REQUESTER_PILL = "the requester";
export const ANY_ARGUMENTS = "for any arguments";
export const withinWords = (ticket: boolean, seconds: number) => (ticket ? "used within " : "retried within ") + durationWords(seconds);

// rolesWords says the approver roles a rule names, so a role never reads
// as a person: the approver role a, the approver roles a or b.
export const rolesWords = (roles: string[]): string => (roles.length === 1 ? "the approver role " : "the approver roles ") + orList(roles);

// whoWords names the deciders.
export function whoWords(who: Who): string {
  if (who.sponsor && who.roles.length) return SPONSOR_WORDS + " or " + rolesWords(who.roles);
  if (who.roles.length) return rolesWords(who.roles);
  return SPONSOR_WORDS;
}

export const NOTIFY_WORD: Record<string, string> = { push: "phone", console: "console", slack: "Slack" };

// ---- the three words ----

// BUCKET_OUTCOME is the outcome each bucket of rules gives a call.
export const BUCKET_OUTCOME: Record<Bucket, Outcome> = { deny: "denied", hum: "approval", allow: "allowed" };
export const BUCKET_WORD: Record<Bucket, string> = { deny: OUTCOME_LABEL.denied, hum: OUTCOME_LABEL.approval, allow: OUTCOME_LABEL.allowed };
export const BUCKET_HELP: Record<Bucket, string> = {
  deny: "Policy rules that deny the call with a reason the agent reads.",
  hum: "Policy rules that stop the call until a person says yes: a hold for seconds, a ticket, or the requester confirming. A check by Straza or by a classifier counts here too.",
  allow: "Policy rules that allow a local tool, or name MCP tools. MCP tools are allowed by role access whether or not a policy rule names them.",
};

export const LANE_WORD: Record<Lane, string> = { mcp: "mcp", shell: "shell", files: "files", net: "net", harness: "harness tool", other: "other", tools: "tools", all: "every lane" };
export const LANE_LABEL: Record<NewLane, string> = { mcp: "MCP tool", shell: "Shell command", files: "File path", net: "Network", harness: "Harness tool" };
export const EVERY_WORD: Record<Lane, string> = { mcp: "every tool", shell: "every command", files: "every path", net: "every fetch", harness: "every harness tool", other: "every call", tools: "every tool", all: "every call" };

// humSplit words the needs-approval bucket: 3 holds, 2 tickets, 1 checked
// before it runs.
export function humSplit(p: Postures): string {
  const parts: string[] = [];
  if (p.hold) parts.push(plural(p.hold, "hold", "holds"));
  if (p.ticket) parts.push(plural(p.ticket, "ticket", "tickets"));
  if (p.confirm) parts.push(p.confirm + " confirmed by the requester");
  if (p.check) parts.push(p.check + " checked before it runs");
  return parts.join(" · ");
}

// CALL_TYPE_WORD is a call type as a count of policy rules reads it: 2 MCP
// tool, 1 shell command.
const CALL_TYPE_WORD: Record<string, string> = { mcp: "MCP tool", shell: "shell command", files: "file", net: "network", other: "other" };

// laneLine words a count's call types: 4 MCP tool, 1 shell command.
export function laneLine(lanes: Record<string, Record<string, number>> | undefined, bucket: Bucket): string {
  if (!lanes) return "";
  const parts: string[] = [];
  for (const lane of ["mcp", "shell", "files", "net", "other"]) {
    const p = lanes[lane];
    if (!p) continue;
    const n = bucket === "deny" ? p.deny || 0 : bucket === "allow" ? p.allow || 0 : (p.hold || 0) + (p.ticket || 0) + (p.confirm || 0) + (p.serverCheck || 0) + (p.classify || 0);
    if (n) parts.push(n + " " + CALL_TYPE_WORD[lane]);
  }
  return parts.join(", ");
}

// countTitle is the hover of one count in the list.
export function countTitle(bucket: Bucket, p: Postures, lanes: Record<string, Record<string, number>> | undefined): string {
  const n = p[bucket];
  if (!n) {
    if (bucket === "allow") return NO_ALLOW;
    return bucket === "deny" ? "No denial in this policy." : "No policy rule in this policy needs approval.";
  }
  const head = bucket === "deny" ? plural(n, "policy rule denies", "policy rules deny") : bucket === "allow" ? plural(n, "policy rule allows", "policy rules allow") : humSplit(p);
  const line = laneLine(lanes, bucket);
  return head + (line ? ": " + line : "");
}

// ---- the list ----

export const AREA_DESC = "The policy rules that decide every call: what is denied, what needs approval and what is allowed.";
export const NEW_POLICY = "New policy";
export const TEST_A_CALL = "Test a call";
export const SEARCH = "Search name or description";
export const VIEW = { sets: "By policy", roles: "By role" };
export const COLUMN = { policy: "Policy", status: "Status", applies: "Applies to", deny: BUCKET_WORD.deny, hum: BUCKET_WORD.hum, allow: BUCKET_WORD.allow, updated: "Updated" };
export const STATUS_HELP = "Live decides calls now. Off decides nothing. Draft waits: a saved edit waits under Drafts.";
export const APPLIES_HELP = "The application roles the policy names. Everyone means every session.";
export const CATEGORY = {
  everyone: { title: "For everyone", none: "No policy applies to everyone yet.", door: "New policy for everyone" },
  roles: { title: "For roles", none: "No policy names a role yet.", door: "New policy for a role" },
  outside: { title: "Outside roles", line: "scoped by user or identity", none: "" },
};
export const EVERYONE = "Everyone";
// LIVE, DRAFT and EDITED are a policy's state as a value, in its chip.
export const LIVE = "live";
export const DRAFT = "off";
export const EDITED = "draft waits";
// STATUS_OPTION is the same state as an option of the Status filter.
export const STATUS_OPTION = { live: "Live", off: "Off", edited: "Draft waits" };
export const LIVE_TITLE = "Running now: every session this policy matches is decided by it.";
export const DRAFT_TITLE = "Stored. It decides nothing until it is turned on.";
export const EDITED_TITLE = "A saved edit waits under Drafts. The live version decides until it is published.";
export const REC = "REC";
export const REC_VERBATIM = "Records conversations word for word for every session this policy matches.";
export const REC_MASKED = "Records conversations with secrets masked for every session this policy matches.";
export const recWord = (mode: string | null | undefined) => (mode === "redact" ? "masked" : "word for word");
export const NOT_PARSED = "not readable";
export const NOT_PARSED_TITLE = "The stored text no longer parses, so its shape is unknown. Open the policy to read Straza's sentence.";
export const SUBJECT_POLICIES = "The policy list";
export const READING = "Reading the policies.";
export const countWords = (shown: number, total: number) => (total > shown ? shown + " of " + plural(total, "policy", "policies") : plural(shown, "policy", "policies"));
export const FILTER = { status: "Status", applies: "Applies to", lane: "Call type", any: "Any", outside: "Outside roles" };
export const LANE_FILTER: Record<string, string> = { mcp: "MCP tool", shell: "Shell command", files: "File", net: "Network", other: "Other" };

// ---- By role ----

export const ROLE_COLUMN = { role: "Role", policies: "Policies", deny: BUCKET_WORD.deny, hum: BUCKET_WORD.hum, allow: BUCKET_WORD.allow, recording: "Recording" };
export const ROLE_HELP = "One row per application role, plus Everyone. Counts sum the role's own live policies.";
export const EVERYONE_LINE = "applies under every role";
export const NO_ROLE_POLICY = "No policy names this role.";
export const DOOR_DENY = "Denied…";
export const DOOR_APPROVE = "Needs approval…";
export const DOOR_APPROVE_EVERYONE = "Needs approval…";
export const doorDenyTitle = (role: string) => "Opens New policy with " + role + " picked and Denied chosen.";
export const doorApproveTitle = (role: string) => "Opens New policy with " + role + " picked and Needs approval chosen.";
export const DOOR_APPROVE_EVERYONE_TITLE = "Opens New policy with Everyone picked. Every session would need approval on the calls you name.";
export const FLOOR_LINE = (name: string, n: number) => plural(n, "denial", "denials") + " of " + name + (n === 1 ? " applies" : " apply") + " here too";
// NO_ALLOW is the hover of a 0 under Allowed, in the list and in By role.
export const NO_ALLOW = "No policy rule allows a call. MCP tools run by role access.";
export const setChipTitle = (name: string, status: string, drift: boolean | undefined) => name + ". " + (status !== "active" ? "Off: decides nothing until it is turned on." : drift ? "Live. A saved edit waits as a draft." : "Live.");
export const draftLine = (name: string) => name + " (off)";
export const liveLine = (name: string, words: string) => name + " (live): " + words;

// ---- a policy's page ----

export const TAB = { rules: "Policy rules", yaml: "YAML", decisions: "Decisions" };
export const ADD_RULE = "Add a policy rule";
export const MORE = "More";
export const MENU = { export: "Export YAML", off: "Turn off", on: "Turn on", delete: "Delete" };
export const DELETE_LIVE_TITLE = "Only a policy that is off can be deleted. Turn a live policy off first.";
export const PRIORITY_HELP = "Order never decides the outcome: a denial wins across every policy that matches. Priority only breaks a tie in which policy rule is reported as the reason.";
export const metaRules = (p: Postures, total: number) => plural(total, "policy rule", "policy rules") + ": " + p.deny + " denied, " + p.hum + " needs approval, " + p.allow + " allowed";
export const FACT = { applies: "Applies to", recording: "Recording", priority: "Priority", change: "Change" };
export const APPLIES_FACT_HELP = "The application roles this policy names, however a session comes to hold them.";
export const RECORDING_FACT_HELP = "Whether the sessions this policy matches have their conversations recorded, word for word or with secrets masked.";
export const NO_RECORDING = "no";
export const MISSING_TITLE = "This policy does not exist.";
export const OPEN_POLICIES = "Open Policies";
export const SUBJECT_POLICY = "The policy";
export const SUBJECT_ROLES = "The roles";
export const NOT_PARSED_PAGE = "The stored text does not parse, so the table cannot show it. The YAML tab has Straza's sentence and the text.";
export const RULE_ID_TITLE = "the policy rule's id, the name that records and the CLI use";
export const CHECK_RULE = "An automated check, kept as written. Change it on the YAML tab.";
export const CONFIRM_RULE = "The requester confirms it themselves. Change it on the YAML tab.";
export const AS_WRITTEN = "As written";
export const AS_WRITTEN_HELP = "Anything the table cannot show stays here as YAML, never dropped, and the comment lines of the text stay where they are.";

// The unpublished bar.
export const unpublished = (n: number) => plural(n, "unpublished change", "unpublished changes");
export const DISCARD = "Discard";
export const PUBLISH = "Publish";
export const LEAVE_TITLE = "Leave without publishing?";
export const LEAVE_BODY = "Your edits are not saved.";
export const LEAVE = "Leave";
export const STAY = "Stay";

// Turn off and delete.
export const offTitle = (name: string) => "Turn off " + name + "?";
// OFF_LANE_WORD names a local call type in the Turn off dialog. A lane with
// no word here reads as local.
const OFF_LANE_WORD: Record<string, string> = { shell: "shell", files: "file", net: "network", harness: "harness tool" };

// offBody says what turning a policy off changes. gatesMcp says its
// approvals or denials cover MCP calls, and allowed are the lanes of the
// local calls it allows, so the dialog names only what the policy holds.
export function offBody(p: Postures, roles: string[], capture: boolean, gatesMcp: boolean, allowed: string[]): string {
  const approvals = p.hum ? plural(p.hum, "policy rule that needs approval", "policy rules that need approval") : "";
  const denials = p.deny ? plural(p.deny, "denial", "denials") : "";
  const what = [approvals, denials].filter(Boolean);
  const head = what.length ? "Its " + what.join(" and its ") + (p.hum + p.deny === 1 ? " stops" : " stop") + " applying." : "It stops applying.";
  const mcp = gatesMcp ? " MCP calls they covered are allowed where a role has access." : "";
  const words = list([...new Set(allowed.map((l) => OFF_LANE_WORD[l] || "local"))]);
  const local = words ? " " + words.charAt(0).toUpperCase() + words.slice(1) + " calls it allowed are denied again under the enterprise profile." : "";
  const rec = capture ? " Recording of " + (roles.length ? list(roles) + " sessions" : "every session") + " stops." : "";
  return head + mcp + local + rec;
}
export const OFF_KEEPS = "It stays stored.";
export const OFF_HELP = "Sessions holding the roles follow within about 30 seconds. A call that waits for approval right now keeps waiting.";
export const TURN_OFF = "Turn off";
export const offToast = (name: string) => name + " is off.";
export const deleteTitle = (name: string) => "Delete " + name + "?";
export const DELETE_BODY = "This cannot be undone.";
export const DELETE = "Delete";
export const deletedToast = (name: string) => name + " is deleted.";
export const CANCEL = "Cancel";

// ---- the rule cards ----

export const WHAT_HAPPENS = "What happens";
export const CONSEQUENCE: Record<Bucket, string> = {
  deny: "The call is denied and the agent reads the reason below.",
  hum: "The call waits until a person says yes.",
  allow: "The call is allowed. For an MCP tool this only restates what role access already gives.",
};
export const WHO_DECIDES = "Who decides";
export const WHO_HELP = "An agent's call goes to the sponsor the identity manager names on it. A person's own agent asks that person, or their sponsor when they have one. With an approver role, the first holder to answer decides. When a policy rule names both, a person's own call goes to the role alone.";
export const WHO_SPONSOR = "The agent's sponsor";
export const WHO_TEAM = "An approver role";
export const WHO_TEAM_LINE = "A role of the approver kind.";
export const HOW = "How";
export const HOW_HELP = "A hold keeps the call waiting for the window, then the agent retries. A ticket denies the call now. Once a person grants it, the same call runs once within the grant window.";
export const HOW_HOLD = "A hold, up to";
export const HOW_HOLD_LINE = "for a call the agent is waiting on right now";
export const HOW_TICKET = "A ticket, granted within";
export const HOW_TICKET_MID = ", then";
export const HOW_TICKET_END = "to run";
export const HOW_TICKET_LINE = "for something planned, like a deploy or a sensitive read";
export const UNITS = { seconds: "seconds", minutes: "minutes", hours: "hours", days: "days" };
export const REASON = "Reason the agent reads";
export const REASON_HINT = "Shown to the agent and to the person deciding. Say what the call does and who decides.";
// REASON_HINT_DENY stands under the reason of a denial, which nobody decides.
export const REASON_HINT_DENY = "Shown to the agent. Say what the call does.";
export const ADVANCED = "Matching";
export const MATCH = { lane: "Call type", server: "Server", tools: "Tools", patterns: "Command patterns", paths: "Paths", events: "Events", require: "Condition" };
export const PATTERNS_HINT = "Patterns in the shell's style over the whole command line: * also matches spaces, ? is one character, the pattern must match the whole line, and case matters. A pattern starting with re: is a regular expression.";
export const ADD_PATTERN = "Add pattern";
export const ADD_PATH = "Add path";
export const ADD_TOOL = "Add tool";
export const EVENTS_DEFAULT = "default: before the tool runs";
export const EVENTS_DEFAULT_HELP = "The policy rule fires when the harness asks before running the tool. This event fires on every harness. Adding another event narrows the policy rule to the harnesses that send it.";
export const EVENTS_MCP = "the gateway checks once, before the call";
export const eventsCoverage = (kind: string, missing: string[]) => (missing.length ? kind + " never fires on " + list(missing) + ", so sessions from " + (missing.length === 1 ? "that harness pass" : "those harnesses pass") + " this policy rule." : kind + " fires on every harness.");
export const CHOOSE_EVENTS = "Choose events";
export const REQUIRE_NONE = "none: every session the policy matches";
export const W1 = (people: number, seconds: number) => plural(people, "holder gets", "holders get") + " " + durationWords(seconds) + " to answer. Most requests will expire. Use a ticket or " + SPONSOR_WORDS + ".";
export const REMOVE_RULE = "Remove policy rule";
export const removeRuleTitle = (id: string) => "Remove policy rule " + id + "?";
export const REMOVE_RULE_BODY = "It leaves the page now and the policy when you publish.";
export const NEW_RULE_ID = "new-rule";

// subjectWords names what a rule matches, for the review and the hover.
export function subjectWords(r: RuleView): string {
  if (r.lane === "mcp") {
    const where = r.app ? " on " + r.app : " on every server";
    return (r.names ? list(r.names) : "every tool") + where;
  }
  if (r.lane === "shell") return r.names ? "shell commands matching " + orList(r.names) : "every shell command";
  if (r.lane === "files") return r.names ? "files under " + orList(r.names) : "every file";
  if (r.lane === "net") return "every network fetch";
  if (r.lane === "harness") return r.names ? (r.names.length === 1 ? "the harness tool " : "the harness tools ") + list(r.names) : "every harness tool";
  if (r.lane === "tools") return list(r.names || []);
  return "every call";
}

// singular says whether the subject is one thing: one named tool, or
// "every command" and its kin, which read as one.
export function singular(r: RuleView): boolean {
  if (r.names) return r.names.length === 1;
  return r.lane !== "tools";
}

// outcomeWords says what happens, in a few words, after the chips.
export function outcomeWords(r: RuleView): string {
  const one = singular(r);
  const s = (a: string, b: string) => (one ? a : b);
  if (r.posture === "deny") return "denied.";
  if (r.posture === "allow") return "allowed.";
  if (r.posture === "confirm") return "the requester confirms first.";
  if (r.posture === "check") return "checked before " + s("it runs", "they run") + ".";
  if (r.posture === "ticket") {
    const who = whoWords(r.who);
    const onePool = (r.who.sponsor && !r.who.roles.length) || (!r.who.sponsor && r.who.roles.length === 1);
    return s("needs", "need") + " a ticket " + who + (onePool ? " grants" : " grant") + " within " + durationWords(r.ticketTTLSeconds) + ", after which the same call is allowed for " + durationWords(r.grantTTLSeconds) + ".";
  }
  const told = r.notify.length ? ", told by " + list(r.notify.map((n) => NOTIFY_WORD[n] || n)) : "";
  return s("needs", "need") + " approval by " + whoWords(r.who) + " within " + durationWords(r.timeoutSeconds) + told + ".";
}

// opening starts a sentence with text about the rule's subject. A sentence
// that opens with a tool name keeps the name as written; one that opens
// with a word is capitalised.
function opening(r: RuleView, text: string): string {
  const opensWithName = r.lane === "mcp" && r.names !== null;
  return opensWithName ? text : text.charAt(0).toUpperCase() + text.slice(1);
}

// subjectOpening is the rule's subject where it opens a sentence.
export const subjectOpening = (r: RuleView) => opening(r, subjectWords(r));

// sentence is the whole rule in one sentence.
export function sentence(r: RuleView): string {
  const verb = r.posture === "deny" || r.posture === "allow" ? (singular(r) ? " is " : " are ") : " ";
  return opening(r, subjectWords(r) + verb + outcomeWords(r));
}

// foldSummary is the collapsed line of Advanced matching. It says the
// events in words only, without their names.
export function foldSummary(r: RuleView): string {
  if (r.lane === "mcp") return "MCP calls to " + (r.names ? r.names.join(", ") : "every tool") + " on " + (r.app || "every server") + " · " + EVENTS_MCP;
  const ev = r.events.length ? r.events.map((e) => EVENT_WORD[e] || e).join(", ") : EVENT_WORD["tool.pre"];
  if (r.lane === "shell") return (r.names ? "shell commands matching " + plural(r.names.length, "pattern", "patterns") : "every shell command") + " · " + ev;
  if (r.lane === "files") return (r.names ? "files under " + plural(r.names.length, "path", "paths") : "every file") + " · " + ev;
  if (r.lane === "tools") return list(r.names || []) + " · " + ev;
  return EVERY_WORD[r.lane] + " · " + ev;
}

// ---- the YAML tab ----

export const YAML_HELP = "This text and the table are one document.";
export const VALIDATE = "Validate";
export const COPY = "Copy";
export const COPIED = "Copied.";
export const COPY_REFUSED = "The browser refused the copy.";
export const SHOW_DIFF = "Show changes against the stored version";
// validPills are Straza's verdict on a text that passed Validate, one fact
// per pill: valid, 3 policy rules, applies to dev, records word for word.
export function validPills(a: { rules?: number; matchRoles?: string[]; capture?: string }): string[] {
  const pills = ["valid", plural(a.rules || 0, "policy rule", "policy rules"), "applies to " + (a.matchRoles && a.matchRoles.length ? list(a.matchRoles) : "everyone")];
  if (a.capture) pills.push("records " + recWord(a.capture));
  return pills;
}
export const advisoryLine = (rule: string | undefined, text: string) => (rule ? rule + ": " + text : text);
export const PARSE_FAILED = "The text does not parse";

// ---- Decisions ----

export const DECISIONS_HELP = "Every record whose reported policy rule belongs to this policy. A record names the policy version it was decided under. An edit since then is said on the record.";
export const OPEN_IN_AUDIT = "Open in Audit";
export const DECISION_COLUMN = { when: "When", who: "Who", call: "Call", outcome: "Outcome", rule: "Policy rule" };
export const NO_DECISIONS = "No decision names this policy yet.";
export const SUBJECT_DECISIONS = "The decisions";

// ---- Test a call ----

export const TEST_TITLE = "Test a call";
export const TEST_LEDE = "Tests against the live policy version. Writes and records nothing.";
export const TEST_LEDE_DRAFT = "Tests against the live policy version and the edits on this page. Writes and records nothing.";
export const TEST_WHO = "Who";
export const TEST_WHO_HINT = "A person or an agent. Their roles decide which policies apply.";
export const TEST_WHAT = "What";
export const TEST_SERVERS_HINT = "Servers the caller's roles reach.";
export const TEST_COMMAND = "The command line";
export const TEST_PATH = "The path";
export const TEST_ANSWER = "Answer";
export const LIVE_NOW = "Live now";
export const WITH_EDITS = "With your unpublished changes";
export const TEST = "Test";
export const TEST_THIS_CALL = "Test this call";
export const TEST_MISSING_WHO = "Name who calls.";
export const TEST_MISSING_WHAT = "Pick the tool, or type the command or the path.";

// ---- New policy ----

export const WIZ_TITLE = "New policy";
export const STEPS = { what: "What should happen", who: "Applies to", calls: "Which calls", how: "Who decides", review: "Review" };
// STEP_REASON names the fourth step of a denial, which asks for its reason.
export const STEP_REASON = "Reason";
export const WHAT_Q = "What should happen?";
export const INTENT = {
  approve: { title: BUCKET_WORD.hum, line: "A person says yes before the call runs." },
  deny: { title: BUCKET_WORD.deny, line: "The call is denied with a reason the agent reads." },
  allow: { title: BUCKET_WORD.allow, line: "Allows a call the deployment denies by default, such as a shell command or an MCP server on a person's machine." },
};
export const ALLOW_HELP = "Under the enterprise profile a local tool that no policy rule allows is denied, so shell, file and network access for a role starts with an allow. So does a harness tool Straza does not classify, and on every profile the tools of an MCP server on a person's own machine. A registered server's tool is different: it runs when a role has access to it, and a policy only denies it or makes it need approval.";
export const WRITE_YAML = "Write YAML instead";
export const WHO_Q = "Who does this policy apply to?";
export const WHO_EVERYONE = "Everyone";
export const WHO_EVERYONE_LINE = "Every person and agent, whatever roles they hold.";
export const WHO_ROLE = "A role";
export const WHO_ROLE_LINE = "Only the people and agents who hold one application role.";
export const WHO_ROLE_HELP = "Policies name application roles. A business role is governed through the application roles it composes.";
export const searchRoles = (n: number) => "Search " + plural(n, "application role", "application roles");
export const roleCount = (shown: number, total: number) => shown + " of " + total;
export const ROLE_CAP = 12;
export const ROLE_SEARCH_FROM = 9;
export const SELECTED_ROLE = "Selected role:";
export const reachLine = (servers: string[]) => (servers.length ? "Reaches " + list(servers) + "." : "Reaches no server yet.");
export const WHO_MISSING = "Pick Everyone or a role.";
export const CALLS_Q = "Which calls?";
export const SERVER = "Server";
export const serversNarrowed = (role: string, reached: number, total: number) => role + " reaches " + reached + " of " + plural(total, "server", "servers") + ".";
export const SERVERS_NARROWED_HELP = "A policy rule for a server the role cannot reach never applies, because the gateway needs both the access row and the policy. A role reaches a server when it is made on that server, and Edit access on its page changes which tools it reaches there.";
export const EVERYONE_SERVERS = "Every server is offered.";
export const WHOLE_SERVER = "Whole server";
export const WHOLE_SERVER_LINE = "All tools, including tools added later.";
export const OR_PICK = "or pick tools:";
export const pickedCount = (n: number, total: number) => n + " of " + total + " picked";
export const PICK_ALL_TITLE = "Pick every tool shown";
export const TOOL_COLUMN = { tool: "Tool", today: "Today for" };
export const RUNS_TODAY = "allowed by role access";
export const NOT_REACHED_TODAY = "no access for this role";
export const alreadyToday = (words: string, set: string) => "already " + words + " by " + set;
export const PATTERNS_Q = "Command patterns";
export const PATTERNS_EXAMPLE = "for example ./deploy* or terraform apply*";
export const PATHS_Q = "Paths";
export const PATHS_EXAMPLE = "for example **/.env* or **/id_rsa*";
export const NET_LINE = "Every network fetch. The policy rule matches the call type, not a host.";
export const CALLS_MISSING = "Pick at least one tool, or name a pattern or a path.";
export const HOW_Q = "Who decides, and for how long?";
export const DENY_Q = "What reason does the agent read?";
export const howLede = (subject: string) => subject + " will need approval.";
export const DENY_LEDE = (subject: string) => subject + " will be denied.";
export const REVIEW_NAME = "Name";
export const NAME_FREE = "No policy has this name.";
export const NAME_TAKEN = "A policy has this name already.";
export const NAME_HINT = "Lower case, digits and dashes.";
export const NAME_BAD = "Lower case letters, digits and dashes only, starting and ending with a letter or a digit.";
export const existingSet = (set: string, role: string) => set + " already applies to " + role + " alone.";
export const addToExisting = (set: string) => "Add to " + set;
export const REVIEW_FACT = { applies: "Applies to", calls: "Calls", outcome: "Outcome", who: "Who decides", how: "How long", reason: "The agent is told", order: "Order" };
export const STORED_AS = "The policy as it will be stored";
export const VALID_REVIEW_HELP = "The same check that publish runs.";
export const BACK = "Back";
export const suggestedName = (role: string | null, intent: string) => (role ? role : "everyone") + "-" + (intent === "approve" ? "approvals" : intent === "deny" ? "denials" : "allows");

// ---- Publish ----

export const publishTitle = (name: string) => "Publish " + name + "?";
export function whatChanges(added: number, changed: string[], removed: string[], roles: string[]): string {
  const who = roles.length === 0 ? "every session" : (roles.length === 1 ? "role " : "roles ") + list(roles);
  const bits: string[] = [];
  if (added) bits.push("adds " + plural(added, "policy rule", "policy rules"));
  if (changed.length) bits.push("changes " + plural(changed.length, "policy rule", "policy rules") + " (" + changed.join(", ") + ")");
  if (removed.length) bits.push("removes " + plural(removed.length, "policy rule", "policy rules") + " (" + removed.join(", ") + ")");
  const text = bits.length ? list(bits) + " for " + who + "." : "No policy rule is added, changed or removed for " + who + ".";
  return text.charAt(0).toUpperCase() + text.slice(1);
}
export const STRIP = { call: "This call", now: "Now", after: "After publishing", rules: "Policy rules for", sessions: "Active sessions" };
export const VERDICT = { deny: OUTCOME_WORD.denied, hum: OUTCOME_WORD.approval, allow: OUTCOME_WORD.allowed };
export const verdictBy = (word: string, set: string | null) => (set ? word + ", by " + set : word);
export const rulesStrip = (p: Postures) => p.deny + " denied · " + p.hum + " needs approval · " + p.allow + " allowed";
export function blastSentence(blast: { total: number; covered: number; names: string[]; extra: number; capped: boolean; unknown: boolean; holdersUnknown: boolean; roles: string[] }): string {
  const tail = " re-evaluate against the new version within about 30 s.";
  if (blast.unknown) return "An unknown number of active sessions" + tail;
  const who = list(blast.roles);
  if (!blast.roles.length) {
    if (blast.total === 0) return "No session is active right now.";
    return (blast.total === 1 ? "The 1 active session re-evaluates" : "All " + blast.total + " active sessions re-evaluate") + " against the new version within about 30 s.";
  }
  if (blast.holdersUnknown) return plural(blast.total, "active session", "active sessions") + " re-evaluate within about 30 s (holders of " + who + " could not be read).";
  const of = (blast.capped ? "at least " : "") + blast.covered + " of " + plural(blast.total, "active session", "active sessions");
  if (blast.covered === 0) return of + " belong to people who hold " + who + ".";
  const one = blast.covered === 1;
  const names = blast.names.length ? " (" + blast.names.join(", ") + (blast.extra ? " and " + (blast.extra === 1 ? "one more" : blast.extra + " more") : "") + ")" : "";
  const rest = blast.total - blast.covered;
  return of + (one ? " belongs to a person who holds " : " belong to people who hold ") + who + names + (one ? ". It follows" : ". They follow") + " the new version within about 30 s." + (rest > 0 ? " The other " + rest + (rest === 1 ? " is" : " are") + " not covered by this policy." : "");
}

// The proof banner after a publish.
export const liveSince = (when: string, version: string) => "Live since " + when + " as version " + version + ", signed.";
export const REEVALUATE = "Sessions holding the roles re-evaluate within about 30 s.";
export const WATCHING = "Waiting for the first decision.";
export const WATCH_STOPPED = "No call in two minutes.";
export const firstDecision = (when: string, who: string, tool: string, outcome: string) => "First decision at " + when + ": " + who + "'s " + tool + " " + outcome + ".";
export const AUDIT_THIS_POLICY = "Audit: this policy's decisions";

// ---- the palette and the record sheet ----

export const PALETTE_GROUP = "Policies";
export const paletteLine = (status: string, roles: string[]) => (status === "active" ? LIVE : DRAFT) + " · " + (roles.length ? roles.join(", ") : EVERYONE);

// ---- a policy's page, continued ----
// A policy's page as it reads: the head's meta line, the three facts and
// their Change sheets, the YAML tab and the Decisions tab.

export const READING_POLICY = "Reading the policy.";
export const META = { priority: "priority", updated: "updated" };
export const openRule = (id: string) => "Open policy rule " + id;
export const MCP_SERVER_TITLE = "the MCP server";
export const policyFileName = (name: string) => name + ".yaml";

// TOUCHED are the words the unpublished bar lists for an edit that is not
// one rule: the facts, and the text when the YAML tab was typed in.
export const TOUCHED = { applies: "applies to", recording: "recording", priority: "priority", text: "text" };

export const CHANGE_APPLIES = "Change who this policy applies to";
export const CHANGE_RECORDING = "Change recording";
export const RECORDING_CHOICE = { off: "No recording", verbatim: "Word for word", masked: "Secrets masked" };
export const CHANGE_PRIORITY = "Change the priority";
export const APPLY_TO_PAGE = "Apply";

export const EDITOR_LABEL = "The policy text";
export const LINE_NUMBERS = "Line numbers";
export const DIFF_ADDED = "added";
export const DIFF_REMOVED = "removed";

export const decisionCount = (n: number) => plural(n, "decision", "decisions");

// decisionCall names the call one audit record decided, from the record's
// own data: an MCP tool on its server, a shell command, or a file verb
// with the paths it names. An empty answer leaves the reading to the audit
// area's own reader.
export function decisionCall(d: Record<string, unknown>): string {
  const tool = String(d.tool || "");
  if (d.toolName) return (d.app ? String(d.app) + " / " : "") + String(d.toolName);
  if (tool === "shell.exec") return "shell command: " + String(d.command || "");
  if (tool.startsWith("file.")) {
    const paths = Array.isArray(d.paths) ? d.paths.map(String).join(" ") : String(d.paths || "");
    return "file " + tool.slice("file.".length) + (paths ? ": " + paths : "");
  }
  return "";
}

export const missingPolicy = (name: string) => "No policy is stored as " + name + ". It may have been deleted, or the address is wrong.";

// ---- the list, continued ----

export const COLUMNS = "Columns";
export const SHOWN_COLUMNS = "Shown columns";
export const RELOAD_NOW = "Reload now";
export const NO_MATCH = "No policy matches here.";
export const ALSO_OUTSIDE = "outside roles";
export const ALSO_OUTSIDE_TITLE = "This policy also names users or identity attributes, so it reaches sessions beyond these roles. Its whole match is in the text.";
export const DOOR_DENY_EVERYONE_TITLE = "Opens New policy with Everyone picked. Every session would be denied the calls you name.";
export const SUBJECT_REACH = "What each role reaches";
export const roleCountWords = (n: number) => plural(n, "role", "roles") + " and " + EVERYONE;
export const denyWords = (n: number) => n + " denied";
export const allowWords = (n: number) => n + " allowed";

// ---- New policy and Publish, continued ----
// The sentences the New policy wizard, the publish dialog and the proof
// banner need beyond the shared ones.

export const DEPENDS_ON_ROLE = "depends on the role";
export const NO_TOOLS = "This server lists no tools yet. Pick another server, or turn on Whole server.";
export const removePick = (name: string) => "Remove " + name;
export const ADD_PICK = "Add";

// The reason the agent reads, offered before it is typed over.
export const reasonApprove = (subject: string, one: boolean) => "Straza: " + subject + (one ? " needs" : " need") + " approval";
export const reasonDeny = (subject: string, one: boolean) => "Straza: " + subject + (one ? " is" : " are") + " denied";

// The window as the review reads it back.
export const waitWords = (seconds: number) => "Waits up to " + durationWords(seconds) + ", then denied.";
export const ticketFact = (ticket: number, grant: number) => "Granted within " + durationWords(ticket) + ", then " + durationWords(grant) + " to run.";

// Write YAML instead: the whole contract, typed by hand and stored as a
// draft.
export const WRITE_YAML_TITLE = "Write the policy as YAML";
export const WRITE_YAML_LEDE = "Save it as a draft, then publish it under Drafts.";
export const WRITE_YAML_START = "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: my-policy\nspec:\n  priority: 150\n  rules: []\n";

export const WRITE_NEW_INSTEAD = "Write a new policy instead";

// The publish dialog's This call row, and the proof banner without a
// version to name.
export const probeCall = (call: string, user: string) => call + " by " + user;
export const liveSinceNoVersion = (when: string) => "Live since " + when + ", signed.";

// ---- Test a call, continued ----
// Test a call: the labels and sentences the sheet needs beyond the section
// above, the context lines of the answer, and the record sheet's door.

export const CLOSE = "Close";
export const TEST_TOOL = "Tool";
export const TEST_EVERY_SERVER = "Every server. Pick who calls to narrow it.";
export const TEST_NO_SERVERS = "No server is reached by the roles this person holds. Pick another person, or give one of their roles access on its page.";
export const TEST_PATH_LINE = "Tested as a file write.";
export const SUBJECT_SERVERS = "The servers and their tools";
export const SPONSOR_DECIDES = "The agent's sponsor decides.";
export const CONFIRM_DECIDES = "The requester confirms on their own device.";

// holdsLine names the roles the server resolved for the person tested, so
// an answer that surprises the operator says which roles produced it.
export const holdsLine = (user: string, roles: string[]) => user + " holds " + list(roles) + ".";

// decidesLine names who says yes to a call that waits: the person behind
// the agent, the approver roles the rule names, or both.
export function decidesLine(who: Who): string {
  if (!who.roles.length) return SPONSOR_DECIDES;
  const roles = rolesWords(who.roles);
  if (who.sponsor) return "The agent's sponsor or " + roles + " decide.";
  return roles.charAt(0).toUpperCase() + roles.slice(1) + (who.roles.length === 1 ? " decides." : " decide.");
}

// ---- the rule cards, continued ----
// The open rule card: the third set of deciders a rule can already name, the
// server an MCP rule matches when it names none, the harness truth under
// Events, and the confirm of Remove rule.

export const WHO_BOTH = "The agent's sponsor, or the approver role";
export const EVERY_SERVER = "every server";
export const REMOVE = "Remove";

// eventsNeverFire is the orange line under Events: every event the rule
// picked is missing on these harnesses, so their sessions pass the rule.
export const eventsNeverFire = (harnesses: string[]) => "This policy rule's only events never fire on " + list(harnesses) + ", so sessions from there pass it.";

// NO_APPROVER_ROLE stands where the approver role picker would: a deployment
// with no approver role cannot route an approval to one yet.
export const NO_APPROVER_ROLE = "No role of the approver kind exists yet. Create one on Roles, then pick it here.";

// ---- rules as rows ----
// The policy page as a table, the rule sheet, the bar with Save, the
// Decisions filters and the Engine fields fold. The wire words (priority,
// class ticket) stay, and only the console words change.

// The table.
export const RULE_COLUMN = { where: "Call type", calls: "Calls", what: "What happens", reason: REASON, rule: "Policy rule" };
export const WHERE_WORD: Record<Lane, string> = { mcp: "MCP tool", shell: "shell command", files: "file", net: "network", harness: "harness tool", other: "other", tools: "local tools", all: "every call" };
export const RULES_SEARCH = "Search tools, patterns, reasons and ids";
export const rulesMatch = (shown: number, total: number) => shown + " of " + plural(total, "policy rule", "policy rules") + " match";
export const NO_RULE_MATCH = "No policy rule matches here.";
export const RESTATES_ACCESS = "restates role access";
export const NO_REASON_YET = "no reason yet";
export const MARK = { edited: CHANGE_WORD.changed, new: CHANGE_WORD.new };

// howLine is the small line under the outcome word in the table: who
// decides and for how long, without the verb the row's word carries.
export function howLine(r: RuleView): string {
  if (r.posture === "confirm") return REQUESTER_CONFIRMS;
  if (r.posture === "check") return CHECKED_FIRST;
  if (r.posture === "ticket") return whoWords(r.who) + ", a ticket within " + durationWords(r.ticketTTLSeconds) + ", then " + durationWords(r.grantTTLSeconds) + " to run";
  if (r.posture === "hold") {
    const told = r.notify.length ? ", told by " + list(r.notify.map((n) => NOTIFY_WORD[n] || n)) : "";
    return whoWords(r.who) + ", a hold of up to " + durationWords(r.timeoutSeconds) + told;
  }
  return "";
}

// The rule sheet.
export const NEW_RULE_TITLE = "New policy rule";
export const RULE_ID = "Policy rule id";
export const RULE_ID_HINT = "Audit records and the CLI use this name. Change it before the first publish.";
export const NEED = { where: "where the call goes", calls: "at least one call", what: "what happens", reason: "a reason" } as const;
export const stillNeeded = (parts: string[]) => "Still needed: " + list(parts) + ".";
export const appliesToAll = (n: number) => "This outcome applies to all " + n + " calls.";
export const MOVE_OUT = "Split out";
export const movedOut = (name: string, id: string) => name + " is now its own policy rule, " + id + ".";
export const REASON_STALE = "The reason still describes a denial. Rewrite it for the new outcome.";
export const REASON_UNUSED = "Not read while the policy rule allows. It stays in the text.";
export const allowRemoves = (n: number) => "This removes the denial for " + (n === 1 ? "this call" : "these " + n + " calls") + ".";
export const CLOSE_SHEET = "Close";
export const WHERE_Q = "Where does the call go?";
export const WHERE_CHOICE: Record<NewLane, string> = LANE_LABEL;
export const PICK_SERVER = "Pick the server";
export const CONSEQUENCE_HOLD = CONSEQUENCE.hum;
export const CONSEQUENCE_TICKET = "The call is denied now and raises a request. After a person grants it, the same call, retried within the grant window, runs once.";

// consequenceWords says what the rule does, following its shape.
export function consequenceWords(posture: Posture | null): string {
  if (posture === "ticket") return CONSEQUENCE_TICKET;
  if (posture === "hold" || posture === "confirm" || posture === "check") return CONSEQUENCE_HOLD;
  if (posture === "deny") return CONSEQUENCE.deny;
  if (posture === "allow") return CONSEQUENCE.allow;
  return "";
}

// unitWord is a duration unit by count: 1 day, 2 days.
export const UNIT_ONE: Record<keyof typeof UNITS, string> = { seconds: "second", minutes: "minute", hours: "hour", days: "day" };
export const unitWord = (n: number, unit: keyof typeof UNITS) => (n === 1 ? UNIT_ONE[unit] : UNITS[unit]);

// EVENT_WORD says an event a harness sends in words. An event with no
// words here shows by its name alone.
const EVENT_WORD: Record<string, string> = { "tool.pre": "before the tool runs", "tool.post": "after the tool ran" };

// eventsWords prints the stored events in words, each with its name after
// it, in the same shape on every lane. An empty list is the engine
// default, which fires before the tool runs.
export function eventsWords(events: string[], lane: Lane): string {
  const named = events.map((e) => (EVENT_WORD[e] ? EVENT_WORD[e] + " (" + e + ")" : e)).join(", ");
  if (lane === "mcp") return EVENTS_MCP + (events.length ? " (" + events.join(", ") + ")" : "");
  return events.length ? named : EVENT_WORD["tool.pre"];
}

// The bar with Save.
export const SAVE = "Save draft";
export const SAVE_HELP = "Save draft stores the text. The live version decides until you publish.";
// savedToast is a Save draft into a set that exists: its saved edit when it
// runs, its stored text when it is off.
export const savedToast = (name: string, live: boolean) =>
  live ? name + " is saved as a draft. The version that runs stays live until you publish it." : name + " is saved. It is off, so it decides nothing until it is turned on.";
export const SHOW_CHANGE = "Show the change";
export const onPage = (p: Postures) => "on this page: " + rulesStrip(p);
export const liveNow = (p: Postures) => "live: " + rulesStrip(p);

// Turn on.
export const onTitle = (name: string) => "Turn on " + name + "?";
export const ON_BODY = "Sessions holding the roles follow it within about 30 seconds.";
export const TURN_ON = "Turn on";
export const onToast = (name: string) => name + " is live.";
export const PUBLISH_DRAFT_NOTE = "Publish stores the text and turns the policy on.";

// Decisions.
export const DEC_FILTER = { who: "Who", outcome: "Outcome", since: "Since" };
export const ANY_WHO = "Anyone";
export const ANY_OUTCOME = "Any outcome";
export const SINCE = { day: "24 hours", week: "7 days", month: "30 days", all: "Everything loaded" };

// The Engine fields fold under a decision.
export const ENGINE_FIELDS = "Engine fields";
export const ENGINE_FIELDS_HELP = "The decision in the engine's own field names: the effect, the approval it carries, the policy rule, the policy and the version it was decided against. Needs approval is stored as effect allow with mode approve.";

// The Applies to sheet with Everyone as its own row.
export const NOTHING_TICKED = "Pick Everyone or at least one role.";

// ---- Decisions, continued ----

// NO_DECISIONS_HERE stands where the table is empty because the three
// filters excluded every loaded row, which NO_DECISIONS would misread as a
// policy that has decided nothing.
export const NO_DECISIONS_HERE = "No decision matches these filters. Widen Since, or pick Anyone and Any outcome.";

// ---- the rule cards, continued ----

// CONDITION_HELP says what the Condition row of the matching fold holds,
// for an operator who has never written a require block.
export const CONDITION_HELP = "A condition narrows the policy rule to sessions that meet it: the level of client verification, a device certificate, a harness version. It is written in the text, and the page only shows it.";

// moveOut names the split link of one chip, so a screen reader hears which
// call leaves the rule.
export const moveOut = (name: string) => "Split out " + name;

// UNSET_SPAN stands in the number box and the unit of the approval branch
// the rule does not use, because that branch stores no window yet.
export const UNSET_SPAN = "-";

// ---- the Rules tab, continued ----

// The rules filter: RULES_FILTER names it, ALL_RULES is its button that narrows nothing.
export const RULES_FILTER = "Filter by outcome";
export const ALL_RULES = "All";

// RULE_ID_TAKEN stands under the id box of the new rule when the id typed
// is one a rule on the page already has, which the document refuses.
export const RULE_ID_TAKEN = "A policy rule on this page has this id already. Type another one.";

// ---- the unpublished bar ----
// The bar's second count once the stored text differs from what runs: the
// policy answer carries no counts of the running version, so the bar says
// what it counts.
export const storedNow = (p: Postures) => "stored, not live: " + rulesStrip(p);

// ---- Test a call on a server's page ----
// The sentences of the server page's Test a call sheet that name the
// policies it tests against.

// testSubjectLine opens the last line of the answer, before the version's
// id.
export const testSubjectLine = (who: string) => "subject: " + who + " · policy version ";
export const testEventLine = (server: string) => "before the tool runs · MCP call on " + server;
export const TEST_USER_HINT = "Uses the user's roles as Straza holds them.";
export const TEST_VERIFICATION = "client verification";
// The options of the client verification picker: the plain word, then the
// value a policy rule's condition spells.
export const TEST_VERIFICATION_OPTION: Record<string, string> = { unset: "Not set", none: "Unmeasured or failed (none)", advisory: "Not verified (advisory)", managed: "Verified (managed)" };
export const TEST_NO_TOOL = "The server listed no tool yet. Type the name to test.";
