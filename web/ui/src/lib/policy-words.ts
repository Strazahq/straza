// The sentences of the Policies area: the list and its By role view, a
// policy's page and its tabs, the rule cards, Test a call, the New policy
// wizard and the publish dialog. Every surface sentence is short; the
// sentence that explains a thing sits behind its help icon (HelpTip) or in
// its dialog, never as prose on a table or a sheet.
import type { Bucket, Lane, Posture, Postures, RuleView, SetView, Who } from "./policy-model";

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

export const SPONSOR_WORDS = "the person behind the agent";

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

export const BUCKET_WORD: Record<Bucket, string> = { deny: "Denied", hum: "Needs approval", allow: "Allowed" };
export const BUCKET_HELP: Record<Bucket, string> = {
  deny: "Rules that deny the call with a reason the agent reads. Hover a count for the lanes.",
  hum: "Rules that stop the call until a person says yes: a hold for seconds, a day-scale ticket, or the requester confirming. Hover a count for the split. An automated check (serverCheck, classify) counts here too and says so in the hover.",
  allow: "Rules that put a local tool on the allowed list, or name MCP tools explicitly. MCP tools run by role access whether or not a rule names them.",
};

export const LANE_WORD: Record<Lane, string> = { mcp: "mcp", shell: "shell", files: "files", net: "net", other: "other", tools: "tools", all: "every lane" };
export const LANE_LABEL: Record<"mcp" | "shell" | "files" | "net", string> = { mcp: "MCP tool", shell: "Shell command", files: "File path", net: "Network" };
export const EVERY_WORD: Record<Lane, string> = { mcp: "every tool", shell: "every command", files: "every path", net: "every fetch", other: "every call", tools: "every tool", all: "every call" };

// humSplit words the needs-approval bucket: 3 holds, 2 day-scale tickets,
// 1 checked before it runs.
export function humSplit(p: Postures): string {
  const parts: string[] = [];
  if (p.hold) parts.push(plural(p.hold, "hold", "holds"));
  if (p.ticket) parts.push(plural(p.ticket, "day-scale ticket", "day-scale tickets"));
  if (p.confirm) parts.push(p.confirm + " the requester confirms");
  if (p.check) parts.push(p.check + " checked before it runs");
  return parts.join(" · ");
}

// laneLine words a count's lanes: 4 mcp · 1 shell.
export function laneLine(lanes: Record<string, Record<string, number>> | undefined, bucket: Bucket): string {
  if (!lanes) return "";
  const parts: string[] = [];
  for (const lane of ["mcp", "shell", "files", "net", "other"]) {
    const p = lanes[lane];
    if (!p) continue;
    const n = bucket === "deny" ? p.deny || 0 : bucket === "allow" ? p.allow || 0 : (p.hold || 0) + (p.ticket || 0) + (p.confirm || 0) + (p.serverCheck || 0) + (p.classify || 0);
    if (n) parts.push(n + " " + lane);
  }
  return parts.join(" · ");
}

// countTitle is the hover of one count in the list.
export function countTitle(bucket: Bucket, p: Postures, lanes: Record<string, Record<string, number>> | undefined): string {
  const n = p[bucket];
  if (!n) {
    if (bucket === "allow") return "Nothing is allowed by this policy's own rules. MCP tools run by role access; local tools follow the profile default, denied under enterprise.";
    return bucket === "deny" ? "No denial in this policy." : "No approval gate in this policy.";
  }
  const head = bucket === "deny" ? plural(n, "rule denies", "rules deny") : bucket === "allow" ? plural(n, "rule allows", "rules allow") : humSplit(p);
  const line = laneLine(lanes, bucket);
  return head + (line ? " · lanes: " + line : "");
}

// ---- the list ----

export const AREA_DESC = "The rules that decide every call: what is denied, what needs approval, and what runs at once.";
export const NEW_POLICY = "New policy";
export const TEST_A_CALL = "Test a call";
export const SEARCH = "Search name or description";
export const VIEW = { sets: "Sets", roles: "By role" };
export const COLUMN = { policy: "Policy", status: "Status", applies: "Applies to", deny: BUCKET_WORD.deny, hum: BUCKET_WORD.hum, allow: BUCKET_WORD.allow, updated: "Updated" };
export const STATUS_HELP = "Live runs now. Off is stored and gates nothing until it is turned on. A draft edits it means a saved edit waits as a draft, and the version that runs stays live until the draft is published.";
export const APPLIES_HELP = "The application roles the policy names in its match. Everyone means no selector: every session. Policies name application roles, the ones that carry tool access; a business role is governed through the application roles it composes.";
export const CATEGORY = {
  everyone: { title: "For everyone", line: "apply to every session, whatever its roles", none: "No policy applies to everyone yet.", door: "New policy for everyone" },
  roles: { title: "For roles", line: "apply to the sessions that hold the role they name", none: "No policy names a role yet.", door: "New policy for a role" },
  outside: { title: "Outside roles", line: "scoped by user or identity, written as text", none: "" },
};
export const EVERYONE = "Everyone";
export const LIVE = "Live";
export const DRAFT = "Off";
export const EDITED = "a draft edits it";
export const LIVE_TITLE = "Running now: every session this policy matches is decided by it.";
export const DRAFT_TITLE = "Stored, gates nothing until it is turned on.";
export const EDITED_TITLE = "A saved edit waits as a draft, and the version that runs stays live until the draft is published. Open the draft under Drafts to publish or discard it.";
export const REC = "REC";
export const REC_VERBATIM = "Records conversations word for word for every session this policy matches.";
export const REC_MASKED = "Records conversations with secrets masked for every session this policy matches.";
export const recWord = (mode: string | null | undefined) => (mode === "redact" ? "masked" : "word for word");
export const NOT_PARSED = "not readable";
export const NOT_PARSED_TITLE = "The stored text no longer parses, so its shape is unknown. Open the policy to read the server's sentence.";
export const SUBJECT_POLICIES = "The policy list";
export const READING = "Reading the policies.";
export const countWords = (shown: number, total: number) => (total > shown ? shown + " of " + plural(total, "policy", "policies") : plural(shown, "policy", "policies"));
export const FILTER = { status: "Status", applies: "Applies to", lane: "Call type", any: "Any", anyRole: "Any role", outside: "Outside roles" };
export const LANE_FILTER: Record<string, string> = { mcp: "MCP", shell: "Shell", files: "Files", net: "Network", other: "Other" };

// ---- By role ----

export const ROLE_COLUMN = { role: "Role", policies: "Policies", deny: BUCKET_WORD.deny, hum: BUCKET_WORD.hum, allow: BUCKET_WORD.allow, recording: "Recording" };
export const ROLE_HELP = "One row per application role, plus Everyone. Counts sum the role's own live policies; the Everyone floor applies to every row and is named in the hover, not added to the number. Outside roles appears only when a policy is scoped by user or identity.";
export const EVERYONE_LINE = "the floor under every role";
export const NO_ROLE_POLICY = "No policy names this role.";
export const DOOR_DENY = "Denied…";
export const DOOR_APPROVE = "Needs approval…";
export const DOOR_APPROVE_EVERYONE = "Needs approval…";
export const doorDenyTitle = (role: string) => "Opens New policy with " + role + " picked and Denied chosen.";
export const doorApproveTitle = (role: string) => "Opens New policy with " + role + " picked and Needs approval chosen.";
export const DOOR_APPROVE_EVERYONE_TITLE = "Opens New policy with Everyone picked. Every session would need approval on the calls you name.";
export const FLOOR_LINE = (name: string, n: number) => name + "'s " + plural(n, "denial applies", "denials apply") + " to this role too";
export const NO_ALLOW_ROLE = "Nothing is allowed by this role's own policies. MCP tools run by role access; under the enterprise profile a local tool nothing allows is denied.";
export const NO_ALLOW_EVERYONE = "No global allow. Under the enterprise profile that is the default, not a gap: a local tool nothing allows is denied. A global allow would open a tool to every session.";
export const ROLE_FOOT = "Select an empty Denied or Needs approval cell to create a policy for that role and the selected call type.";
export const setChipTitle = (status: string, drift: boolean | undefined) => (status !== "active" ? "Off: gates nothing until it is turned on." : drift ? "Live, and a draft edits it." : "Live.");
export const draftLine = (name: string) => name + " (off)";
export const liveLine = (name: string, words: string) => name + " (live): " + words;

// ---- a policy's page ----

export const TAB = { rules: "Rules", yaml: "YAML", decisions: "Decisions" };
export const ADD_RULE = "Add rule";
export const MORE = "More";
export const MENU = { export: "Export YAML", off: "Turn off", on: "Turn on", delete: "Delete" };
export const DELETE_LIVE_TITLE = "Only a policy that is off can be deleted. Turn a live policy off first.";
export const PRIORITY_HELP = "Order never decides the outcome: a denial wins across every policy that matches. Priority only breaks a tie in which rule is reported as the reason.";
export const metaRules = (p: Postures, total: number) => plural(total, "rule", "rules") + ": " + p.deny + " denied, " + p.hum + " need approval, " + p.allow + " allowed";
export const FACT = { applies: "Applies to", recording: "Recording", priority: "Priority", change: "Change" };
export const APPLIES_FACT_HELP = "The application roles this policy names. An application role reaches one server, and a session holds every role its roles compose, so naming the role governs every path to that server's tools.";
export const RECORDING_FACT_HELP = "Whether every session this policy matches has its conversation recorded, word for word or with secrets masked. Recording is a property of the policy, never of one rule.";
export const NO_RECORDING = "no";
export const MISSING_TITLE = "This policy does not exist.";
export const MISSING_BODY = "It may have been deleted, or the address is wrong.";
export const OPEN_POLICIES = "Open Policies";
export const SUBJECT_POLICY = "The policy";
export const SUBJECT_ROLES = "The roles";
export const NOT_PARSED_PAGE = "The stored text does not parse, so the cards cannot show it. The YAML tab has the server's sentence and the text.";
export const RULE_ID_TITLE = "the rule id, the name records and the CLI use";
export const CHECK_RULE = "An automated check, kept as written. Change it on the YAML tab.";
export const CONFIRM_RULE = "The requester confirms it themselves. Change it on the YAML tab.";
export const AS_WRITTEN = "As written";
export const AS_WRITTEN_HELP = "Anything the cards cannot show stays here as YAML, never dropped, and the comment lines of the text stay where they are.";

// The unpublished bar.
export const unpublished = (n: number) => plural(n, "unpublished change", "unpublished changes");
export const UNPUBLISHED_HELP = "Edits live on this page until you save a draft or publish. Save draft stores the text without publishing it, and Save and publish makes it the version that runs. Leaving the page asks first.";
export const DISCARD = "Discard";
export const PUBLISH = "Publish";
export const LEAVE_TITLE = "Leave without publishing?";
export const LEAVE_BODY = "The edits on this page are not stored anywhere. Save draft or Save and publish keeps them, and leaving drops them.";
export const LEAVE = "Leave and drop the edits";
export const STAY = "Stay";

// Turn off and delete.
export const offTitle = (name: string) => "Turn off " + name + "?";
export function offBody(p: Postures, roles: string[], capture: boolean): string {
  const gates = p.hum ? plural(p.hum, "approval gate", "approval gates") : "";
  const denials = p.deny ? plural(p.deny, "denial", "denials") : "";
  const what = [gates, denials].filter(Boolean);
  const head = what.length ? "Its " + list(what) + (p.hum + p.deny === 1 ? " stops" : " stop") + " governing the moment you confirm. " : "It stops governing the moment you confirm. ";
  const lanes = "MCP calls it gated run at once where a role has access; shell, file and network calls it allowed are denied again under the enterprise profile.";
  const rec = capture ? " Recording of " + (roles.length ? list(roles) + " sessions" : "every session") + " stops." : "";
  return head + lanes + rec;
}
export const OFF_KEEPS = "The policy stays stored and reads Off. Turn it on again from the More menu.";
export const OFF_HELP = "Sessions holding the roles re-evaluate within about 30 seconds. A call held for approval right now keeps its hold.";
export const TURN_OFF = "Turn off";
export const offToast = (name: string) => name + " is off.";
export const deleteTitle = (name: string) => "Delete " + name + "?";
export const DELETE_BODY = "It is off and gates nothing. This cannot be undone.";
export const DELETE = "Delete";
export const deletedToast = (name: string) => name + " is deleted.";
export const CANCEL = "Cancel";

// ---- the rule cards ----

export const WHAT_HAPPENS = "What happens";
export const CONSEQUENCE: Record<Bucket, string> = {
  deny: "The call is denied and the agent reads the reason below.",
  hum: "The call waits until a person says yes. Nothing runs before that.",
  allow: "The call runs at once. For an MCP tool this only restates what role access already gives.",
};
export const WHO_DECIDES = "Who decides";
export const WHO_HELP = "An agent's call goes to its sponsor, the accountable person the identity manager names on it. A person's own agent asks that person, on their enrolled phone or browser. A person who has a sponsor is decided by that sponsor. An approver role is a role of the approver kind, and whoever holds it and answers first decides. When a rule names both, a person's own call goes to the role alone.";
export const WHO_SPONSOR = "The person behind the agent";
export const WHO_TEAM = "An approver role";
export const WHO_TEAM_LINE = "a role of the approver kind; the identity manager masters who holds it";
export const HOW = "How";
export const HOW_HELP = "A hold: the agent's call keeps its socket open for the window, then it is told to wait for the decision and retry. A ticket: the call is denied now and raises a request; after a person grants it, the same call, retried within the grant window, runs once. The text stores them as class hold and class ticket.";
export const HOW_HOLD = "A hold, up to";
export const HOW_HOLD_LINE = "for a call the agent is waiting on right now";
export const HOW_TICKET = "A ticket a person grants within";
export const HOW_TICKET_MID = ", the same call within";
export const HOW_TICKET_END = "runs";
export const HOW_TICKET_LINE = "for something planned, like a deploy or a sensitive read";
export const UNITS = { seconds: "seconds", minutes: "minutes", hours: "hours", days: "days" };
export const REASON = "Reason the agent reads";
export const REASON_HINT = "Shown to the agent and to the person deciding. Say what the call does and who decides.";
export const ADVANCED = "Matching";
export const MATCH = { lane: "Call type", server: "Server", tools: "Tools", patterns: "Command patterns", paths: "Paths", events: "Events", require: "Condition" };
export const PATTERNS_HINT = "Shell-style globs over the whole command line: * crosses spaces, ? is one character, the match is anchored at both ends, and case matters. A pattern starting with re: is a regular expression.";
export const ADD_PATTERN = "Add pattern";
export const ADD_PATH = "Add path";
export const ADD_TOOL = "Add tool";
export const EVENTS_DEFAULT = "default: before the tool runs";
export const EVENTS_DEFAULT_HELP = "The rule fires when the harness asks before running the tool. This event fires on every harness. Adding another event narrows the rule to the harnesses that send it.";
export const EVENTS_MCP = "the gateway checks once, before the call";
export const eventsCoverage = (kind: string, missing: string[]) => (missing.length ? kind + " never fires on " + list(missing) + ", so sessions from " + (missing.length === 1 ? "that harness pass" : "those harnesses pass") + " this rule." : kind + " fires on every harness.");
export const CHOOSE_EVENTS = "Choose events";
export const REQUIRE_NONE = "none: every session the policy matches";
export const W1 = (people: number, seconds: number) => people + (people === 1 ? " person" : " people") + " paged for a " + seconds + " s window. Most of these will expire to deny. Use a ticket, or route to " + SPONSOR_WORDS + ".";
export const REMOVE_RULE = "Remove rule";
export const removeRuleTitle = (id: string) => "Remove rule " + id + "?";
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
    return s("needs", "need") + " a ticket " + who + (onePool ? " grants" : " grant") + " within " + durationWords(r.ticketTTLSeconds) + "; the same call within " + durationWords(r.grantTTLSeconds) + " runs.";
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

// foldSummary is the collapsed line of Advanced matching.
export function foldSummary(r: RuleView): string {
  if (r.lane === "mcp") return "MCP calls to " + (r.names ? r.names.join(", ") : "every tool") + " on " + (r.app || "every server") + " · " + eventsWords(r.events, "mcp");
  const ev = r.events.length ? "events: " + r.events.join(", ") : "events: default";
  if (r.lane === "shell") return (r.names ? "shell commands matching " + plural(r.names.length, "pattern", "patterns") : "every shell command") + " · " + ev;
  if (r.lane === "files") return (r.names ? "files under " + plural(r.names.length, "path", "paths") : "every file") + " · " + ev;
  if (r.lane === "tools") return list(r.names || []) + " · " + ev;
  return EVERY_WORD[r.lane] + " · " + ev;
}

// ---- the YAML tab ----

export const YAML_LINE = "The policy as stored, the full contract, comments included.";
export const YAML_HELP = "The cards on Rules and this text are one document. What you change here shows on Rules; what the cards cannot show stays here as written, and a card edit keeps every comment line.";
export const VALIDATE = "Validate";
export const COPY = "Copy";
export const COPIED = "Copied.";
export const COPY_REFUSED = "The browser refused the copy.";
export const SHOW_DIFF = "Show changes against the live version";
export function validWords(a: { rules?: number; matchRoles?: string[]; capture?: string }, when: string): string {
  const who = a.matchRoles && a.matchRoles.length ? "matches " + list(a.matchRoles) : "applies to everyone";
  const rec = a.capture ? ", records " + recWord(a.capture) : "";
  return "Valid, checked by the server " + when + ": " + plural(a.rules || 0, "rule", "rules") + ", " + who + rec + ".";
}
export const VALID_NOW = "just now";
export const advisoryLine = (rule: string | undefined, text: string) => (rule ? rule + ": " + text : text);
export const PARSE_FAILED = "The text does not parse";

// ---- Decisions ----

export const DECISIONS_LINE = "Decisions this policy made, newest first, from the audit chain.";
export const DECISIONS_HELP = "Every record whose reported rule belongs to this policy. A record names the policy version it was decided under; an edit since then is said on the record.";
export const OPEN_IN_AUDIT = "Open in Audit";
export const DECISION_COLUMN = { when: "When", who: "Who", call: "Call", outcome: "Outcome", rule: "Rule" };
export const NO_DECISIONS = "No decision names this policy yet.";
export const SUBJECT_DECISIONS = "The decisions";
export const DECISIONS_FOOT = "A row opens the record with its why view, the same sheet Audit opens.";

// ---- Test a call ----

export const TEST_TITLE = "Test a call";
export const TEST_LEDE = "Answers for now, against the policy version live right now. It writes nothing and records nothing.";
export const TEST_LEDE_DRAFT = "Answers for now, against the policy version live right now and the edits on this page. It writes nothing and records nothing.";
export const TEST_WHO = "Who";
export const TEST_WHO_HINT = "A person or an agent. Their roles decide which policies apply.";
export const TEST_WHAT = "What";
export const TEST_SERVERS_HINT = "Servers the subject's roles reach, and the tools each exposes.";
export const TEST_COMMAND = "The command line";
export const TEST_PATH = "The path";
export const TEST_ANSWER = "Answer";
export const LIVE_NOW = "Live now";
export const WITH_EDITS = "With your unpublished changes";
export const WITH_EDITS_LINE = "Publish the page for this to become the answer.";
export const TEST = "Test";
export const TEST_FROM_AUDIT = "From Audit, Test this call opens here with the record's call filled in.";
export const TEST_THIS_CALL = "Test this call";
export const TEST_MISSING_WHO = "Name who calls.";
export const TEST_MISSING_WHAT = "Pick the tool, or type the command or the path.";

// ---- New policy ----

export const WIZ_TITLE = "New policy";
export const WIZ_LEDE = "One question per step. Nothing is stored until you publish or save a draft.";
export const STEPS = { what: "What should happen", who: "Who", calls: "Which calls", how: "How", review: "Review" };
export const WHAT_Q = "What should happen?";
export const INTENT = {
  approve: { title: BUCKET_WORD.hum, line: "A person says yes before the call runs. The most common choice." },
  deny: { title: BUCKET_WORD.deny, line: "The call is denied, with a reason the agent and the person both read." },
  allow: { title: BUCKET_WORD.allow, line: "A local tool the profile denies by default runs: shell, files, network. MCP tools run by role access, so they need no allow." },
};
export const ALLOW_HELP = "Under the enterprise profile a local tool no rule allows is denied, so shell, file and network access for a role starts with an allow. An MCP tool is different: it runs when a role has access to it, and a policy only denies or gates it.";
export const RECORDING_NOTE = "Recording is not a policy kind: it is a property of a policy, set on its page.";
export const WRITE_YAML = "Write YAML instead";
export const WHO_Q = "Who?";
export const WHO_EVERYONE = "Everyone";
export const WHO_EVERYONE_LINE = "every session, the home of organisation-wide rules";
export const WHO_ROLE = "A role";
export const WHO_ROLE_LINE = "the policy names an application role, the kind that carries tool access";
export const WHO_ROLE_HELP = "An application role reaches one server, and a session holds every role its roles compose, so naming the role governs everyone who reaches that server through it, whichever business role gave it to them. Business, approver and Straza roles are not offered: a business role would govern one assignment path only, an approver role decides and never acts, and a Straza role governs Straza itself and never matches sessions.";
export const searchRoles = (n: number) => "Search " + plural(n, "application role", "application roles");
export const roleCount = (shown: number, total: number) => shown + " of " + total;
export const ROLE_CAP = 12;
export const ROLE_SEARCH_FROM = 9;
export const SELECTED_ROLE = "Selected role:";
export const reachLine = (servers: string[]) => (servers.length ? "Reaches " + list(servers) + "." : "Reaches no server yet.");
export const WHO_MISSING = "Pick Everyone or a role.";
export const CALLS_Q = "Which calls?";
export const SERVER = "Server";
export const serversNarrowed = (role: string, reached: number, total: number) => role + " reaches " + reached + " of " + plural(total, "server", "servers") + "; the others are not offered because a rule for them could never fire.";
export const SERVERS_NARROWED_HELP = "A rule for a server the role cannot reach is dead: the gateway needs both the access row and the policy. A role reaches a server when it is made on that server, and Edit access on its page changes which tools it reaches there.";
export const EVERYONE_SERVERS = "Everyone: every server is offered, the home of organisation-wide rules, denials on unreachable servers included.";
export const WHOLE_SERVER = "Whole server";
export const WHOLE_SERVER_LINE = "All tools, including tools added later.";
export const OR_PICK = "or pick tools:";
export const pickedCount = (n: number, total: number) => n + " of " + total + " picked";
export const PICK_ALL_TITLE = "Pick every tool shown";
export const TOOL_COLUMN = { tool: "Tool", today: "Today for" };
export const RUNS_TODAY = "runs at once by role access";
export const NOT_REACHED_TODAY = "not reached by this role";
export const alreadyToday = (words: string, set: string) => "already " + words + " by " + set;
export const PATTERNS_Q = "Command patterns";
export const PATTERNS_EXAMPLE = "for example ./deploy* or terraform apply*";
export const PATHS_Q = "Paths";
export const PATHS_EXAMPLE = "for example **/.env* or **/id_rsa*";
export const NET_LINE = "Every network fetch: the rule matches the lane, not a host.";
export const CALLS_MISSING = "Pick at least one tool, or name a pattern or a path.";
export const HOW_Q = "How?";
export const howLede = (subject: string) => subject + " will need approval. Say who decides, and for how long.";
export const DENY_LEDE = (subject: string) => subject + " will be denied. Say why, in the words the agent reads.";
export const REVIEW_NAME = "Name";
export const NAME_FREE = "No policy has this name.";
export const NAME_TAKEN = "A policy has this name already.";
export const NAME_HINT = "Suggested from the role and what happens. Lower case, digits and dashes.";
export const NAME_BAD = "Lower case letters, digits and dashes only, starting and ending with a letter or a digit.";
export const existingSet = (set: string, role: string) => set + " already matches exactly " + role + (set.endsWith("-access") ? " and was written in the console." : ".");
export const addToExisting = (set: string) => "Add these rules to " + set + " instead";
export const REVIEW_FACT = { applies: "Applies to", waits: "Needs approval when", refused: "Denied when", allowed: "Allowed when", who: "Who decides", how: "How long", reason: "Reason shown" };
export const STORED_AS = "The policy as it will be stored";
export const VALID_REVIEW_HELP = "The same check publishing runs. It knows the role kinds, so a business role in the match is refused here, not at publish.";
export const NEXT = "Next";
export const BACK = "Back";
export const holdersToday = (holders: string[]) => (holders.length ? list(holders) + " today" : "nobody today");
export const suggestedName = (role: string | null, intent: string) => (role ? role : "everyone") + "-" + (intent === "approve" ? "approvals" : intent === "deny" ? "denials" : "allows");

// ---- Publish ----

export const publishTitle = (name: string) => "Publish " + name + "?";
export function whatChanges(added: number, changed: string[], removed: string[], roles: string[], isNew: boolean, replaces: boolean): string {
  const who = roles.length === 0 ? "every session" : (roles.length === 1 ? "role " : "roles ") + list(roles);
  const bits: string[] = [];
  if (added) bits.push("adds " + plural(added, "rule", "rules"));
  if (changed.length) bits.push("changes " + plural(changed.length, "rule", "rules") + " (" + changed.join(", ") + ")");
  if (removed.length) bits.push("removes " + plural(removed.length, "rule", "rules") + " (" + removed.join(", ") + ")");
  let text = bits.length ? list(bits) + " for " + who + "." : "No rule is added, changed or removed for " + who + ".";
  text = text.charAt(0).toUpperCase() + text.slice(1);
  if (replaces) text = "Replaces the running version: " + text.charAt(0).toLowerCase() + text.slice(1);
  if (isNew) text += " A new policy: nothing removed.";
  return text;
}
export const STRIP = { call: "This call", now: "Now", after: "After publishing", rules: "Rules for", sessions: "Sessions that notice" };
export const VERDICT = { deny: "is denied", hum: "needs approval", allow: "runs at once" };
export const verdictBy = (word: string, set: string | null) => (set ? word + ", by " + set : word);
export const rulesStrip = (p: Postures) => p.deny + " denied · " + p.hum + " need approval · " + p.allow + " allowed";
export function blastSentence(blast: { total: number; covered: number; names: string[]; extra: number; capped: boolean; unknown: boolean; holdersUnknown: boolean; roles: string[] }): string {
  const tail = " re-evaluate against the new version within about 30 s.";
  if (blast.unknown) return "An unknown number of active sessions" + tail;
  const who = list(blast.roles);
  if (!blast.roles.length) {
    if (blast.total === 0) return "No session is active right now; new sessions start under the new version.";
    return (blast.total === 1 ? "The 1 active session re-evaluates" : "All " + blast.total + " active sessions re-evaluate") + " against the new version within about 30 s.";
  }
  if (blast.holdersUnknown) return plural(blast.total, "active session", "active sessions") + " re-evaluate within about 30 s (holders of " + who + " could not be read).";
  const of = (blast.capped ? "at least " : "") + blast.covered + " of " + plural(blast.total, "active session", "active sessions");
  if (blast.covered === 0) return of + " belong to people who hold " + who + "; nobody re-evaluates until someone is granted it.";
  const one = blast.covered === 1;
  const names = blast.names.length ? " (" + blast.names.join(", ") + (blast.extra ? " and " + (blast.extra === 1 ? "one more" : blast.extra + " more") : "") + ")" : "";
  const rest = blast.total - blast.covered;
  return of + (one ? " belongs to a person who holds " : " belong to people who hold ") + who + names + (one ? "; it re-evaluates" : "; they re-evaluate") + " against the new version within about 30 s." + (rest > 0 ? " The other " + rest + (rest === 1 ? " is" : " are") + " not covered by this policy." : "");
}
export const TEST_FIRST = "Test a call first";

// The proof banner after a publish.
export const liveSince = (when: string, version: string) => "Live since " + when + " as version " + version + ", signed.";
export const REEVALUATE = "Sessions holding the roles re-evaluate within about 30 s.";
export const WATCHING = "No call has matched yet; this line watches the audit for two minutes.";
export const WATCH_STOPPED = "No call matched in two minutes; stopped watching.";
export const firstDecision = (when: string, who: string, tool: string, outcome: string) => "First decision at " + when + ": " + who + "'s " + tool + " " + outcome + ".";
export const AUDIT_THIS_POLICY = "Audit: this policy's decisions";

// ---- the palette and the record sheet ----

export const PALETTE_GROUP = "Policies";
export const paletteLine = (status: string, roles: string[]) => (status === "active" ? LIVE : DRAFT) + " · " + (roles.length ? roles.join(", ") : EVERYONE);

// setLine is the one-line reading of a set for the role page and chips.
export function setLine(v: SetView): string {
  const p = { deny: 0, hum: 0, allow: 0 };
  for (const r of v.rules) p[r.bucket]++;
  return p.deny + " denied · " + p.hum + " need approval · " + p.allow + " allowed";
}

// ---- a policy's page, continued ----
// A policy's page as it reads: the head's meta line, the three facts and
// their Change sheets, the YAML tab and the Decisions tab.

export const READING_POLICY = "Reading the policy.";
export const META = { priority: "priority", updated: "updated" };
export const openRule = (id: string) => "Open rule " + id;
export const MCP_SERVER_TITLE = "the MCP server";
export const policyFileName = (name: string) => name + ".yaml";

// TOUCHED are the words the unpublished bar lists for an edit that is not
// one rule: the facts, and the text when the YAML tab was typed in.
export const TOUCHED = { applies: "applies to", recording: "recording", priority: "priority", text: "text" };

export const CHANGE_APPLIES = "Change who this policy applies to";
export const EVERYONE_MEANS = "Everyone means every session, whatever its roles.";
export const CHANGE_RECORDING = "Change recording";
export const RECORDING_CHOICE = { off: "No recording", verbatim: "Word for word", masked: "Secrets masked" };
export const CHANGE_PRIORITY = "Change the priority";
export const APPLY_TO_PAGE = "Add to unpublished changes";

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
  if (tool === "shell.exec") return "shell: " + String(d.command || "");
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
export const VIEW_LABEL = "View";
export const RELOAD_NOW = "Reload now";
export const NO_MATCH = "No policy matches here.";
export const LIST_FOOT = "Sorted by name inside each table. A row opens the policy.";
export const OUTSIDE_FOOT = "A third table, Outside roles, appears when a policy is scoped by user or identity.";
export const ALSO_OUTSIDE = "outside roles";
export const ALSO_OUTSIDE_TITLE = "This policy also names users or identity attributes, so it reaches sessions beyond these roles. Its whole match is in the text.";
export const DOOR_DENY_EVERYONE_TITLE = "Opens New policy with Everyone picked. Every session would be denied the calls you name.";
export const SUBJECT_REACH = "What each role reaches";
export const roleCountWords = (n: number) => plural(n, "role", "roles") + " and " + EVERYONE;
export const denyWords = (n: number) => n + " deny";
export const allowWords = (n: number) => n + " allow";

// ---- New policy and Publish, continued ----
// The sentences the New policy wizard, the publish dialog and the proof
// banner need beyond the shared ones.

export const DEPENDS_ON_ROLE = "depends on the role";
export const NO_TOOLS = "This server lists no tools yet. Pick another server, or name the whole server.";
export const removePick = (name: string) => "Remove " + name;
export const ADD_PICK = "Add";

// The reason the agent reads, offered before it is typed over.
export const reasonApprove = (subject: string, one: boolean) => "Straza: " + subject + (one ? " needs" : " need") + " approval";
export const reasonDeny = (subject: string, one: boolean) => "Straza: " + subject + (one ? " is" : " are") + " denied";

// The window as the review reads it back.
export const holdWords = (seconds: number) => "a hold of up to " + durationWords(seconds);
export const ticketWords = (ticket: number, grant: number) => "a ticket granted within " + durationWords(ticket) + ", then the same call within " + durationWords(grant) + " runs";

// Write YAML instead: the whole contract, typed by hand and stored as a
// draft.
export const WRITE_YAML_TITLE = "Write the policy as YAML";
export const WRITE_YAML_LEDE = "The whole contract, the text the server stores. Save a draft of it, then publish it under Drafts.";
export const WRITE_YAML_START = "apiVersion: straza.dev/v1beta1\nkind: PolicySet\nmetadata:\n  name: my-policy\nspec:\n  priority: 150\n  rules: []\n";

export const WRITE_NEW_INSTEAD = "Write a new policy instead";

// The publish dialog's This call row, and the proof banner without a
// version to name.
export const probeCall = (call: string, user: string) => call + " by " + user;
export const PROBE_ORIGIN = "from Which calls";
export const liveSinceNoVersion = (when: string) => "Live since " + when + ", signed.";

// ---- Test a call, continued ----
// Test a call: the labels and sentences the sheet needs beyond the section
// above, the context lines of the answer, and the record sheet's door.

export const CLOSE = "Close";
export const TEST_TOOL = "Tool";
export const TEST_EVERY_SERVER = "Every server; pick who calls to narrow the list.";
export const TEST_NO_SERVERS = "No server is reached by the roles this person holds. Pick another person, or give one of their roles access on its page.";
export const TEST_PATH_LINE = "Tested as a file write.";
export const SUBJECT_SERVERS = "The servers and their tools";
export const DECIDED_LIVE = "Decided under the policy version live right now.";
export const SPONSOR_DECIDES = "The person behind the agent decides.";
export const CONFIRM_DECIDES = "The requester confirms on their own device.";

// holdsLine names the roles the server resolved for the person tested, so
// an answer that surprises the operator says which roles produced it.
export const holdsLine = (user: string, roles: string[]) => user + " holds " + list(roles) + ".";

// decidesLine names who says yes to a call that waits: the person behind
// the agent, the approver roles the rule names, or both.
export function decidesLine(who: Who): string {
  if (!who.roles.length) return SPONSOR_DECIDES;
  const roles = rolesWords(who.roles);
  if (who.sponsor) return "The person behind the agent or " + roles + " decide.";
  return roles.charAt(0).toUpperCase() + roles.slice(1) + (who.roles.length === 1 ? " decides." : " decide.");
}

// ---- the rule cards, continued ----
// The open rule card: the third set of deciders a rule can already name, the
// server an MCP rule matches when it names none, the harness truth under
// Events, and the confirm of Remove rule.

export const WHO_BOTH = "The person behind the agent, or the approver role";
export const EVERY_SERVER = "every server";
export const REMOVE = "Remove";

// eventsNeverFire is the orange line under Events: every event the rule
// picked is missing on these harnesses, so their sessions pass the rule.
export const eventsNeverFire = (harnesses: string[]) => "This rule's only events never fire on " + list(harnesses) + ": sessions from there pass it.";

// NO_APPROVER_ROLE stands where the approver role picker would: a deployment
// with no approver role cannot route an approval to one yet.
export const NO_APPROVER_ROLE = "No role of the approver kind exists yet. Create one on Roles, then pick it here.";

// ---- rules as rows ----
// The policy page as a table, the rule sheet, the bar with Save, the
// Decisions filters and the Engine fields fold. The wire words (priority,
// class ticket) stay, and only the console words change.

// The facts row states the one rule that matters beside the number.
export const PRIORITY_LINE = "A denial always wins. Priority only picks which rule is reported, higher first.";

// The table.
export const RULE_COLUMN = { where: "Call type", calls: "Calls", what: "What happens", reason: REASON, rule: "Rule" };
export const WHERE_WORD: Record<Lane, string> = { mcp: "MCP server", shell: "shell", files: "files", net: "network", other: "other", tools: "local tools", all: "every call" };
export const RULES_SEARCH = "Search tools, patterns, reasons and ids";
export const rulesMatch = (shown: number, total: number) => shown + " of " + plural(total, "rule", "rules") + " match";
export const NO_RULE_MATCH = "No rule matches here.";
export const RESTATES_ACCESS = "restates role access";
export const NO_REASON_YET = "no reason yet";
export const MARK = { edited: "edited", new: "new" };
export const RULES_FOOT = "Sorted by where the call goes. A row opens the rule; the table stays put.";

// howLine is the small line under the outcome word in the table: who
// decides and for how long, without the verb the row's word carries.
export function howLine(r: RuleView): string {
  if (r.posture === "confirm") return "the requester confirms first";
  if (r.posture === "check") return "checked before it runs";
  if (r.posture === "ticket") return whoWords(r.who) + ", a ticket within " + durationWords(r.ticketTTLSeconds) + ", then " + durationWords(r.grantTTLSeconds) + " to run";
  if (r.posture === "hold") {
    const told = r.notify.length ? ", told by " + list(r.notify.map((n) => NOTIFY_WORD[n] || n)) : "";
    return whoWords(r.who) + ", a hold of up to " + durationWords(r.timeoutSeconds) + told;
  }
  return "";
}

// The rule sheet.
export const RULE_SHEET_LEDE = "Nothing reaches the server until you save or publish.";
export const NEW_RULE_TITLE = "New rule";
export const NEW_RULE_LEDE = "Nothing is chosen yet. The rule joins the page once where the call goes, at least one call, what happens and a reason are filled in.";
export const RULE_ID = "Rule id";
export const RULE_ID_HINT = "The name every audit record and the CLI use. Proposed from the calls and the outcome; change it before the first publish.";
export const NEED = { where: "where the call goes", calls: "at least one call", what: "what happens", reason: "a reason" } as const;
export const stillNeeded = (parts: string[]) => "Still needed: " + list(parts) + ".";
export const appliesToAll = (n: number) => "This outcome applies to all " + n + " calls.";
export const MOVE_OUT = "Move to its own rule";
export const movedOut = (name: string, id: string) => name + " is now its own rule, " + id + ".";
export const REASON_STALE = "The reason still describes a denial. Rewrite it for the new outcome.";
export const REASON_UNUSED = "Not read while the rule allows. It stays in the text.";
export const allowRemoves = (n: number) => "This removes the only denial for " + (n === 1 ? "this call" : "these " + n + " calls") + ".";
export const CLOSE_SHEET = "Close";
export const WHERE_Q = "Where does the call go?";
export const WHERE_CHOICE: Record<"mcp" | "shell" | "files" | "net", string> = LANE_LABEL;
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

// eventsWords prints the stored events with their plain reading, in the
// same shape on every lane. An empty list is the engine default.
export function eventsWords(events: string[], lane: Lane): string {
  const stored = events.length ? events.join(", ") : "default";
  if (lane === "mcp") return stored + ": " + EVENTS_MCP;
  if (events.length === 0 || (events.length === 1 && events[0] === "tool.pre")) return stored + ": before the tool runs";
  return stored;
}

// The bar with Save.
export const SAVE = "Save draft";
export const SAVE_HELP = "Save draft stores the text without publishing it. A live policy keeps running its published version, and the page says a draft edits it, until you publish.";
// savedToast is a Save draft into a set that exists: its saved edit when it
// runs, its stored text when it is off.
export const savedToast = (name: string, live: boolean) =>
  live ? name + " is saved as a draft. The version that runs stays live until you publish it." : name + " is saved. It is off, so it gates nothing until it is turned on.";
export const SHOW_CHANGE = "Show the change";
export const onPage = (p: Postures) => "on this page: " + rulesStrip(p);
export const liveNow = (p: Postures) => "live: " + rulesStrip(p);

// Turn on.
export const onTitle = (name: string) => "Turn on " + name + "?";
export const ON_BODY = "The stored text starts governing the moment you confirm. Sessions holding the roles re-evaluate within about 30 seconds.";
export const TURN_ON = "Turn on";
export const onToast = (name: string) => name + " is live.";
export const PUBLISH_DRAFT_NOTE = "Publish stores the text and turns the policy on.";

// Decisions.
export const DEC_FILTER = { who: "Who", outcome: "Outcome", since: "Since" };
export const ANY_WHO = "Anyone";
export const ANY_OUTCOME = "Any outcome";
export const SINCE = { day: "24 hours", week: "7 days", month: "30 days", all: "everything loaded" };
export const DECIDED_EARLIER = "Decided under an earlier version of this policy. The version live now differs.";
export const DECIDED_CURRENT = "Decided under the version live now.";

// The Engine fields fold under a decision.
export const ENGINE_FIELDS = "Engine fields";
export const ENGINE_FIELDS_HELP = "The decision in the engine's own field names: the effect, the gate it carries, the rule, the policy and the snapshot it was decided against. Needs approval is stored as effect allow with mode approve.";

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
export const CONDITION_HELP = "A condition narrows the rule to sessions that meet it: attestation rank, a device certificate, a harness version. It is written in the text, and the cards only read it.";

// moveOut names the split link of one chip, so a screen reader hears which
// call leaves the rule.
export const moveOut = (name: string) => "Move " + name + " to its own rule";

// UNSET_SPAN stands in the number box and the unit of the approval branch
// the rule does not use, because that branch stores no window yet.
export const UNSET_SPAN = "-";

// ---- the Rules tab, continued ----

// ALL_RULES is the first button of the rules filter, the one that narrows
// nothing.
export const ALL_RULES = "All";

// RULE_ID_TAKEN stands under the id box of the new rule when the id typed
// is one a rule on the page already has, which the document refuses.
export const RULE_ID_TAKEN = "A rule on this page has this id already. Type another one.";

// ---- the unpublished bar ----
// The bar's second count once the stored text differs from what runs: the
// policy answer carries no counts of the running version, so the bar says
// what it counts.
export const storedNow = (p: Postures) => "stored, not live: " + rulesStrip(p);
