// The sentences of the Approvals area: the queue, the request sheet, the
// decide dialog, the approver devices, the enroll sheet and the channels.
// Every surface sentence is short; the sentence that explains a thing
// sits behind its help icon.
import type { ApprovalRow, ApproverDeviceRow, ChannelRow } from "./api";
import { type Deciders, type DeviceFilter, type Kind, type Phase, type Seat, STALE_DAYS, callOf, decidersOf, kindOf, shortID, timeLeft, windowWords, withinWords } from "./approval-model";
import { absTime, relTimeText } from "./words";

// ---- the page ----

export type ApprovalsTab = "requests" | "devices" | "channels";
export const TABS: { key: ApprovalsTab; label: string }[] = [
  { key: "requests", label: "Requests" },
  { key: "devices", label: "Approver devices" },
  { key: "channels", label: "Channels" },
];
export const ADD_PHONE = "Add a phone";

// ---- the queue ----

export const SUBJECT_REQUESTS = "The request list";
export const READING_REQUESTS = "Reading the requests.";
export type Filter = "waiting" | "decided" | "all";
export const FILTERS: { key: Filter; label: string }[] = [
  { key: "waiting", label: "Waiting" },
  { key: "decided", label: "Decided" },
  { key: "all", label: "All" },
];
export const SEARCH_REQUESTS = "Search who asked, calls, rules";
export const REQUEST_COLUMN = { asked: "Asked", who: "Who asked", call: "Call", decides: "Who decides", state: "State" };
export const DECIDES_HELP = "The rule that held the call names who decides. An agent's call goes to its sponsor, the accountable person the identity manager names on it. A person's own agent asks that person, on their enrolled phone or browser. A person who has a sponsor is decided by that sponsor. An approver role is a role of the approver kind, and whoever holds it and answers first decides. When a rule names both, a person's own call goes to the role alone. Being an admin adds nothing; the console, the CLI and the phone decide the same record under the same rule.";
export const YOURS = "yours";
// The seat's own request on a lane that carries no device signature: the
// row says so and links the page that signs, the dialog adds where to go.
export const OWN_REQUEST = "This is your own request.";
export const OWN_REQUEST_WHERE = "Confirm it on your phone, or on the self-service page under This browser.";
export const OPEN_SELF_SERVICE = "Open the self-service page";
export const SELF_SERVICE_PAGE = "/self-service/";
export const NOBODY_HOLDS = "nobody holds it";
export const NOBODY_CAN = "nobody can decide";
export const decidesWord = (name: string) => name + " decides";
export const requestsCount = (shown: number) => (shown === 1 ? "1 request" : shown + " requests");
export function waitingCount(waiting: number, mine: number): string {
  const head = waiting === 1 ? "1 request waits" : waiting + " requests wait";
  return waiting ? head + ", " + mine + " yours to decide" : head;
}
export const decidedCount = (n: number) => n + " decided";
export const LOAD_MORE = "Load more";
export const loadedLine = (n: number) => "Showing the newest " + n + " requests.";

export const PHASE_WORD: Record<Phase, string> = { waiting: "Waiting", approved: "Approved", granted: "Approved", used: "Used", lapsed: "Expired", denied: "Denied", expired: "Expired" };
export const phaseTone = (p: Phase): "warn" | "ok" | "danger" | "plain" => (p === "waiting" ? "warn" : p === "approved" || p === "granted" || p === "used" ? "ok" : p === "denied" ? "danger" : "plain");
export const KIND_WORD: Record<Kind, string> = { hold: "hold", ticket: "ticket" };
// WAITING_LINE is the small line under a waiting row's kind: what the agent
// does while the request waits.
export const WAITING_LINE: Record<Kind, string> = { hold: "the agent is waiting now", ticket: "runs later, after a yes" };
export const CHANNEL_WORD: Record<string, string> = { console: "the console", slack: "Slack", phone: "the phone", browser: "the browser", api: "the API" };
export const channelWord = (c: string | undefined) => CHANNEL_WORD[c || ""] || (c ? c : "the console");

// deciderWords names who may decide, for the cell: "alice", "sec-approvers",
// "alice or sec-approvers", the requester, or any straza-admin.
export function deciderWords(d: Deciders): string {
  if (d.kind === "admins") return "any straza-admin";
  return [...d.users, ...d.roles].join(" or ");
}
// deciderWhy is the small line under the name.
export function deciderWhy(d: Deciders, requester: string): string {
  switch (d.kind) {
    case "sponsor": return requester + "'s sponsor";
    case "role": return d.roles.length === 1 ? "an approver role" : "approver roles";
    case "both": return "the sponsor, or the role";
    case "requester": return "only the requester confirms";
    default: return "no decider named, so any admin";
  }
}
// stateLine is the small line under the head of the State cell: what the
// agent does under a waiting row's kind, and who decided under a decided
// row's phase word.
export function stateLine(r: ApprovalRow, phase: Phase, now = Date.now()): string {
  const by = (r.decidedByName || r.decidedBy) ? "by " + (r.decidedByName || r.decidedBy) + " from " + channelWord(r.channel) + ", " + relTimeText(r.decidedAt ?? undefined) : "";
  const reason = r.decidedReason ? " · “" + r.decidedReason + "”" : "";
  switch (phase) {
    case "waiting": return WAITING_LINE[kindOf(r)];
    case "granted": return withinWords(r.grantExpiresAt, now) + (by ? " · " + by : "");
    case "used": return "by session " + shortID(r.consumedBy) + (r.consumedAt ? " · " + relTimeText(r.consumedAt) : "");
    case "lapsed": return "approved by " + (r.decidedByName || r.decidedBy) + ", never used";
    case "expired": return "nobody decided within " + windowWords(r);
    default: return by + reason;
  }
}
// srStateLine is the request dialog's description for a screen reader:
// the state word and who decided as one sentence, "Approved by alice from
// the console, 2 m ago.", where stateLine's small line would read after the
// word as a second sentence that opens in lower case.
export function srStateLine(r: ApprovalRow, phase: Phase, now = Date.now()): string {
  const who = r.decidedByName || r.decidedBy;
  const by = who ? " by " + who + " from " + channelWord(r.channel) + (r.decidedAt ? ", " + relTimeText(r.decidedAt) : "") : "";
  if (phase === "used") return "Used by session " + shortID(r.consumedBy) + (r.consumedAt ? ", " + relTimeText(r.consumedAt) : "") + ".";
  if (phase === "lapsed") return "Expired: approved" + (who ? " by " + who : "") + ", never used.";
  if (phase === "granted") {
    const within = withinWords(r.grantExpiresAt, now);
    return "Approved" + by + ". " + within.charAt(0).toUpperCase() + within.slice(1) + ".";
  }
  const reason = r.decidedReason ? " Their reason: “" + r.decidedReason + "”" + (/[.!?]$/.test(r.decidedReason) ? "" : ".") : "";
  return PHASE_WORD[phase] + by + "." + reason;
}
export const EMPTY_WAITING_TITLE = "No pending approval requests.";
export const EMPTY_WAITING_BODY = "Requests appear here when a policy requires approval. Configured channels notify approvers; roles and signing requirements determine who can decide.";
export const EMPTY_DECIDED_TITLE = "No request has been decided yet.";
export const EMPTY_DECIDED_BODY = "Decided requests stay here for as long as the server keeps them.";
export const EMPTY_SEARCH = "No request matches. Clear the search to see them all.";
export const ROW_HINT = "A row opens the request.";

// ---- the request dialog ----

export const REQUEST_TITLE = "Approval request";
export const LANE_WORD: Record<string, string> = { gateway: "through the MCP gateway", hook: "from the hook on the agent's machine" };
export const laneWord = (lane: string) => LANE_WORD[lane] || lane;
// The strip under the title: the kind in plain words, and what happens when
// the time runs out.
export const KIND_SAYS: Record<Kind, string> = {
  hold: "The agent is waiting right now for your answer.",
  ticket: "The agent moved on. It runs this only after a yes.",
};
export const atZero = (r: ApprovalRow) => "then the " + (kindOf(r) === "hold" ? "call" : "request") + " is denied";
// askVerb ends the line over the call: a command runs and a tool is called,
// and a decided request reads in the past.
export function askVerb(r: ApprovalRow): string {
  return (r.state === "pending" ? "wants to " : "asked to ") + (callOf(r.summary).whereKind === "shell" ? "run" : "call");
}
export const ON_SERVER = "on";
export const RUNS_WHERE: Record<string, string> = { gateway: "through the MCP gateway", hook: "on its own machine" };
// metaLine is the one line under the call: where it runs, when it was asked
// and who sponsors the agent. sponsor is empty when the record names none,
// and the part is then left out.
export function metaLine(r: ApprovalRow, sponsor: string, seat: Seat): string {
  return [RUNS_WHERE[r.lane] || r.lane, "asked " + relTimeText(r.createdAt), sponsor ? sponsoredWords(sponsor === seat.user ? "you" : sponsor) : ""].filter(Boolean).join(" · ");
}
// The parameters, folded under the call. The hint beside the word says how
// many values the fold holds, or why it holds no table.
export const CARD_PARAMS = "Parameters";
export const paramsHint = (count: number) => (count === 1 ? "1 value" : count + " values");
export const PARAMS_HINT_COMMAND = "the command line is the whole call";
export const PARAMS_HINT_STORED = "as the server stored them";
export const PARAMS_HINT_NONE = "none";
export const PARAMS_HELP = "What the agent passed to the tool, as the server stored it, with secrets redacted. The approval covers this exact call: the same tool with these same parameters, which the server hashed.";
export const PARAMS_HELP_TOOL = "What the agent passed to the tool, as the server stored it, with secrets redacted. This rule binds the tool alone, so the approval covers the tool whatever the parameters.";
export const PARAM_COLUMN = { name: "Parameter", value: "Value" };
export const PARAMS_NONE = "The server kept no parameters for this call.";
export const PARAMS_COMMAND = "The command line above is the whole call.";
export const SHOW_JSON = "Show as JSON and the hash";
export const hashLine = (prefix: string | undefined) => (prefix ? "sha256 " + prefix + "…" : "no hash");
export const paramsCut = (bytes: number) => "Cut at 2 KiB of " + bytes + " bytes.";
export const UNVERIFIED = "unverified";
export const SAYS_OWN = "The agent's own words. Nobody checked them.";
export const SAYS_NONE = "The agent gave no reason.";
export const SAYS_NONE_HOOK = "The agent gave no reason; a call from the hook carries none.";
export const GRANT_UNKNOWN = "the grant window the rule sets";
// The two boxes under the reason: what a yes does and what a no does.
export const IF_APPROVE = "If you approve";
export const IF_DENY = "If you deny";
// approveDoes is the sentence of the approve box. A hold releases the one
// call on the line. A ticket covers what the approval's fingerprint binds:
// the exact call, or the tool whatever its parameters. A record that carries
// no binding scope claims neither. grant words the ticket's grant window, or
// GRANT_UNKNOWN.
export function approveDoes(r: ApprovalRow, grant: string | null): string {
  if (kindOf(r) === "hold") return "The call runs now, once.";
  const within = "within " + (grant || GRANT_UNKNOWN);
  if (r.bindingScope === "tool_identity") return "Its next call of this tool runs, once, " + within + ", whatever the parameters.";
  if (r.bindingScope === "call") return "Its next try of this exact " + (callOf(r.summary).whereKind === "shell" ? "command" : "call") + " runs, once, " + within + ".";
  return "Its next matching call runs, once, " + within + ".";
}
export const denyDoes = (r: ApprovalRow) => (kindOf(r) === "hold" ? "The call does not run. The agent is told who said no." : "Nothing runs. The agent is told who said no.");
// doesLine says both outcomes as one line, for a reader who does not see the
// two boxes.
export const doesLine = (r: ApprovalRow, grant: string | null) => IF_APPROVE + ": " + approveDoes(r, grant) + " " + IF_DENY + ": " + denyDoes(r);
// The fold over the record rows.
export const DETAILS = "Details";
export const DETAILS_HINT = "session, when and how it came in, policy rule";
// The facts table.
export const FACT_REQUESTED = "Requested by";
export const sponsoredWords = (name: string) => "sponsored by " + name;
export const FACT_SESSION = "Session";
export const OPEN_SESSION = "Open session";
export const OPEN_TRANSCRIPT = "Open transcript";
export const FACT_WHEN = "When";
export const FACT_LANE = "How it came in";
export const FACT_WHO = "Who can decide";
export const WHO_HELP = DECIDES_HELP;
export const FACT_DECIDE_BY = "Decide by";
export const DECIDE_BY_HELP_HOLD = "The agent's call is waiting on the line for this long. When the time runs out the call is denied and the agent is told to try again later.";
export const DECIDE_BY_HELP_TICKET = "The agent was told to wait and will call again. When the time runs out the request is denied.";
// decideByRest is the plain part after the clock: the deadline and what
// happens at zero.
export const decideByRest = (r: ApprovalRow) => "until " + absTime(r.expiresAt) + " · " + atZero(r);
export const decideByLine = (r: ApprovalRow, now = Date.now()) => timeLeft(r, now) + " · " + decideByRest(r);
export const FACT_RULE = "Policy rule";
export const IN_POLICY = "in policy";
// whoLine speaks to the reader: You when the request is theirs, a name and
// Not you otherwise, Nobody when the named role has no holder. holders maps
// a role to its holder count; a missing count leaves the number out.
export function whoLine(r: ApprovalRow, seat: Seat, holders: Record<string, number>, isMine: boolean, isStuck: boolean, needsDevice = false): string {
  const d = decidersOf(r);
  const requester = r.username || r.user;
  const roles = d.roles.join(" or ");
  const n = d.roles.reduce((sum, x) => sum + (holders[x] || 0), 0);
  const known = d.roles.some((x) => x in holders);
  const held = known ? "held by " + (n === 1 ? "1 person" : n + " people") : "an approver role";
  if (r.state !== "pending") {
    switch (d.kind) {
      case "requester": return "Only the requester, " + requester + ".";
      case "sponsor": return d.users.join(" or ") + ", as " + requester + "'s sponsor.";
      case "role": return roles + ", " + held + ".";
      case "both": return d.users.join(" or ") + ", as " + requester + "'s sponsor, or anyone holding " + roles + ".";
      default: return "Any straza-admin, since the rule named nobody.";
    }
  }
  if (isStuck) return "Nobody. The rule names the role " + roles + " and nobody holds it.";
  switch (d.kind) {
    case "requester": return isMine ? "You, as the requester. Only you can confirm it" + (needsDevice ? ", on a device that signs." : ".") : "Only the requester, " + requester + ". Not you.";
    case "sponsor": return isMine ? "You (" + seat.user + "), as " + requester + "'s sponsor. Nobody else." : d.users.join(" or ") + ", as " + requester + "'s sponsor. Not you.";
    case "role": return isMine ? "You, as " + (known ? "one of " + (n === 1 ? "1 person" : n + " people") + " holding " : "a holder of ") + roles + ". The first to answer decides." : roles + ", " + held + ". Not you.";
    case "both":
      if (!isMine) return d.users.join(" or ") + ", as " + requester + "'s sponsor, or anyone holding " + roles + ". Not you.";
      // The record's users say whether the reader is the sponsor. The
      // seat's roles cannot: the self-service page passes a seat with none.
      if (d.users.includes(seat.user)) return "You, as the sponsor, or anyone holding " + roles + ". The first to answer decides.";
      return "You, as a holder of " + roles + ", or " + d.users.join(" or ") + ", the sponsor. The first to answer decides.";
    default: return isMine ? "You, as a straza-admin: the rule named nobody." : "Any straza-admin, since the rule named nobody. Not you.";
  }
}
export const openRole = (name: string) => "Open the role " + name;
export const OPEN_RULE = "Open the rule";
// The decision rows of a decided request.
export const FACT_DECIDED_BY = "Decided by";
export const decidedWhere = (r: ApprovalRow) => (r.decidedByName || r.decidedBy) + " from " + channelWord(r.channel);
export const THEIR_REASON = "Their reason";
export const NONE_GIVEN = "none given";
export const FACT_SIGNED = "Signed with";
export const signedWords = (r: ApprovalRow) => (r.decidedDeviceId ? "the device " + shortID(r.decidedDeviceId) : "the " + channelWord(r.channel).replace(/^the /, "") + " session");
export const FACT_GRANT = "Grant window";
export const grantWords = (r: ApprovalRow, phase: Phase, now = Date.now()) => (phase === "used" ? "used by session " + shortID(r.consumedBy) : phase === "granted" ? withinWords(r.grantExpiresAt, now) : "the window closed unused");
export function expiredLine(r: ApprovalRow, isStuck: boolean): string {
  const d = decidersOf(r);
  return "Nobody decided within " + windowWords(r) + ", so the call was denied." + (isStuck ? " The request was routed to " + deciderWords(d) + ", which nobody holds." : "");
}
export const DENY = "Deny";
export const APPROVE = "Approve";
export const CLOSE = "Close";

// ---- the decide dialog ----

export type Verdict = "approve" | "deny";
export const decideTitle = (v: Verdict, r: ApprovalRow) => (v === "approve" ? "Approve " : "Deny ") + callOf(r.summary).call + " for " + (r.username || r.user) + "?";
export function decideBody(v: Verdict, r: ApprovalRow): string {
  if (v === "deny") return "The call is denied and the agent is told who denied it.";
  if (kindOf(r) === "ticket") return "The next " + callOf(r.summary).call + " call from this session within the grant window runs, once.";
  return "The call runs now, once, and the agent's session gets the answer.";
}
export function decideHelp(v: Verdict, r: ApprovalRow): string {
  if (v === "deny") return "A denied request stays denied for this call in this session until its window closes. The audit chain records who denied it and the reason.";
  if (kindOf(r) === "ticket") return "A ticket is used by the first matching call inside the grant window and then it is spent. The audit chain records the approval and the use as two records.";
  return "Approving a hold releases this one call. The same call later raises a new request.";
}
export const REASON_LABEL = "Reason, optional";
export const REASON_PLACEHOLDER = "What the agent and the audit record will read";
export const REASON_HINT = "Plain text, up to 500 characters.";
export const REASON_MAX = 500;
export const CANCEL = "Cancel";
export const APPROVE_REQUEST = "Approve request";
export const DENY_REQUEST = "Deny request";
export const verdictVerb = (v: Verdict) => (v === "approve" ? APPROVE_REQUEST : DENY_REQUEST);
// The final answers: the question is gone and Close is the only button.
export const finalTitle = (v: Verdict) => (v === "approve" ? "Not approved" : "Not denied");
export function alreadyDecided(r: ApprovalRow): string {
  const what = callOf(r.summary).call + " for " + (r.username || r.user);
  const verb = r.state === "approved" ? "approved" : r.state === "denied" ? "denied" : "closed";
  const by = (r.decidedByName || r.decidedBy) ? " by " + (r.decidedByName || r.decidedBy) + " from " + channelWord(r.channel) : "";
  return what + " was already " + verb + by + " " + relTimeText(r.decidedAt ?? undefined) + ", so there is nothing left to decide here.";
}
export const WINDOW_CLOSED = "The window closed while this was open, so the call was denied.";
export const notYours = (server: string) => "This request is no longer yours to decide: " + server + ".";
export function approvedToast(r: ApprovalRow): string {
  const c = callOf(r.summary).call;
  const who = r.username || r.user;
  return kindOf(r) === "ticket" ? "Approved. " + c + " may run once within the grant window for " + who + "." : "Approved. " + c + " runs now for " + who + ".";
}
export const deniedToast = (r: ApprovalRow) => "Denied. " + (r.username || r.user) + " is told " + callOf(r.summary).call + " was denied.";

// ---- approver devices ----

export const SUBJECT_DEVICES = "The device list";
export const READING_DEVICES = "Reading the devices.";
export const devicesCount = (n: number) => (n === 1 ? "1 device" : n + " devices");
export const DEVICE_COLUMN = { device: "Device", person: "Person", key: "Key", enrolled: "Enrolled", seen: "Last seen", notified: "Notified" };
export const KEY_HELP = "Hardware means the signing key lives in the phone's secure hardware and the enrollment was attested by the platform. Software means the key lives in the operating system's keystore and the posture is the device's own claim; that is normal for a browser.";
export const NOTIFIED_HELP = "With a push route, Straza sends each request to the device as a push notification. Without one the device sees requests only while its page is open. The device registers its route itself.";
export const kindBadge = (d: Pick<ApproverDeviceRow, "platform">) => (d.platform === "browser" ? "browser" : "phone · " + d.platform);
export const isPhone = (d: Pick<ApproverDeviceRow, "platform">) => d.platform !== "browser";
export const HARDWARE_KEY = "hardware key";
export const SOFTWARE_KEY = "software key";
export const ATTESTED_BY: Record<string, string> = { "play-integrity": "attested by Google Play Integrity", "app-attest": "attested by Apple App Attest" };
export const attestedWords = (att: string) => ATTESTED_BY[att] || "attested";
export const SOFTWARE_PHONE = "the phone's own claim, not attested";
export const SOFTWARE_BROWSER = "normal for a browser";
export const isHardware = (d: Pick<ApproverDeviceRow, "key_security_level" | "attestation">) => d.key_security_level !== "software" && d.attestation !== "none" && !!d.attestation;
export const BY_PUSH = "by push notification";
export const MUST_CHECK = "must check";
export const MUST_CHECK_LINE = "no push route: sees requests only while its page is open";
export const SEEN_NEVER = "never";
export const REVOKE = "Revoke";
export const REVOKE_DEVICE = "Revoke device";
export const revokeName = (name: string) => "Revoke " + name;
export const revokeTitle = (name: string) => "Revoke " + name + "?";
export const revokeBody = (person: string) => person + " can no longer approve from it. The device deletes its key the next time it contacts Straza.";
export const REVOKE_HELP = "A phone enrolls again from a new QR code on this page. A browser enrolls again from the self-service page. Other devices of the same person are untouched.";
export const revokedToast = (person: string, name: string) => "Revoked. " + person + " can no longer approve from " + name + ".";
export const DEVICES_EMPTY_TITLE = "No approver device is enrolled.";
export const DEVICES_EMPTY_BODY = "Add a phone from the button above. A person can also enable approvals in their own browser on the self-service page.";
export const DEVICES_LEGEND = "A person approves from a phone with the Straza approver app or from their own browser. Only the person's enrolled devices sign their decisions; a decision from the console is signed by the session.";
export const deviceName = (d: Pick<ApproverDeviceRow, "name" | "id">) => d.name || shortID(d.id);
// The strip above the table: every chip is a count and a filter.
export const DEVICE_CHIP: Record<DeviceFilter, string> = { all: "all", phones: "phones", browsers: "browsers", mustcheck: "must check", softphone: "software key on a phone", stale: "not seen in " + STALE_DAYS + " days" };
export const STRIP_LABEL = "Which devices to show";
export const devicesTotal = (n: number) => n.toLocaleString("en") + (n === 1 ? " device" : " devices");
// chipWords is a chip's label: the count and the word, singular for one
// phone or one browser.
export const chipWords = (f: DeviceFilter, n: number) => (f === "all" ? DEVICE_CHIP.all : n.toLocaleString("en") + " " + (n === 1 && (f === "phones" || f === "browsers") ? DEVICE_CHIP[f].slice(0, -1) : DEVICE_CHIP[f]));
export const SEARCH_DEVICES = "Find a person or a device";
export const GROUP_BY_PERSON = "Group by person";
export const matchWords = (n: number, total: number) => (n === total ? devicesTotal(n) : n.toLocaleString("en") + " of " + total.toLocaleString("en") + " devices match");
export const peopleWords = (people: number, devices: number) => people + (people === 1 ? " person, " : " people, ") + devicesTotal(devices);
export const shownWords = (shown: number, total: number) => "Showing " + shown + " of " + total.toLocaleString("en");
export const groupDevices = (n: number, look: number) => devicesCount(n) + (look ? " · " + look + " to look at" : "");
export const showDevices = (name: string) => "Show the devices of " + name;
export const hideDevices = (name: string) => "Hide the devices of " + name;
export const DEVICES_PAGE = 50;

// ---- the enroll sheet ----

export const ADD_PHONE_SUB = "A one-time QR code the Straza approver app scans. The phone then approves the requests routed to its owner.";
export const PERSON_LABEL = "Person";
export const PERSON_HINT = "A person, never an agent. Agents cannot approve.";
export const agentPicked = (name: string) => name + " is an agent and cannot approve. Pick a person.";
export const CREATE_QR = "Create the QR code";
export const CREATE_ANOTHER = "Create another";
export const SCAN_TITLE = "Scan with the Straza approver app";
export const SCAN_LINE = "The phone reads the server and a one-time code from it, makes a key in its secure hardware and enrolls. The code expires in ";
export const CODE_TITLE = "One-time code";
export const COPY = "Copy";
export const COPIED = "Copied";
export const CODE_EXPIRED = "expired";
export const CODE_SPENT = "The code expired before a phone scanned it. Create another.";
export const SERVERS_TITLE = "Servers the phone tries, in order";
export const SERVERS_NONE = "none returned";
export const TRUST_TITLE = "How the phone trusts it";
export const PINNED = "pinned";
export const PINNED_LINE = "strazad serves the phone's connection itself, so the QR carries its key and the phone accepts no other certificate. Behind an ingress there is no pin and the phone trusts the public certificate.";
export const PUBLIC_CA = "public certificate";
export const PUBLIC_CA_LINE = "The approver address presents a publicly trusted certificate and the phone verifies it through the system trust store, the expected shape behind an ingress. A pin rides the QR only when strazad terminates the phone's connection itself.";
export const PLAINTEXT = "plain HTTP";
export const plaintextLine = (hosts: string[]) => hosts.join(", ") + " carries the one-time code and every approval in the clear. Fine for a local eval; give the approver address TLS before a real phone enrolls.";
export const TRANSPORT_UNKNOWN = "transport unknown";
export const TRANSPORT_UNKNOWN_LINE = "No usable server address came back, so the trust posture cannot be stated.";
export const QR_TOO_BIG = "This code is too large to draw. Use strazactl approvals enroll-token instead.";
export const MINT_VERB = "Create the QR code";
export const QR_LABEL = "Enrollment QR code";
// trustPosture classifies the transport the phone will use, from the
// servers list and the pin the mint answered.
export function trustPosture(servers: string[], pin: string | undefined): { word: string; tone: "ok" | "warn" | "plain" | "unknown"; line: string } {
  if (pin) return { word: PINNED, tone: "ok", line: PINNED_LINE };
  const real = (servers || []).map((s) => (s || "").trim()).filter((s) => s !== "");
  const plain = real.filter((s) => /^http:\/\//i.test(s));
  if (plain.length) return { word: PLAINTEXT, tone: "warn", line: plaintextLine(plain) };
  if (real.length === 0) return { word: TRANSPORT_UNKNOWN, tone: "unknown", line: TRANSPORT_UNKNOWN_LINE };
  return { word: PUBLIC_CA, tone: "plain", line: PUBLIC_CA_LINE };
}

// ---- channels ----

export const SUBJECT_CHANNELS = "The channel list";
export const READING_CHANNELS = "Reading the channels.";
export const CHANNEL_COLUMN = { channel: "Channel", status: "Status", detail: "Detail", reaches: "Reaches", last: "Last delivery" };
export const ALWAYS_ON = "always on";
export const ON = "on";
export const NOT_CONFIGURED = "not configured";
export const channelStatus = (c: ChannelRow) => (c.name === "console" ? ALWAYS_ON : c.configured ? ON : NOT_CONFIGURED);
export const REACHES_CONSOLE = "everyone with the approvals area";
export const REACHES_NOBODY = "nobody";
export const REACHES_SLACK = "the channel it posts to";
export function reachesPush(devices: number, routes: number): string {
  return (devices === 1 ? "1 device enrolled" : devices + " devices enrolled") + ", " + routes + " with a push route";
}
export const SEE_WHICH = "See which";
export const NOTHING_TO_DELIVER = "nothing to deliver";
export const NEVER = "never";
export const DELIVERY_OK = "ok";
export const DELIVERY_FAILED = "failed";
export const SEND_TEST = "Send a test";
export const SENDING = "Sending";
export const TEST_VERB = "Send a test";
export const testTitle = (name: string) => "Test on " + name;
export const targetsCount = (n: number) => (n === 1 ? "1 target." : n + " targets.");
export const DELIVERED = "delivered";
export const NO_TARGETS = "No target. Nothing on this channel can receive a request.";
export const NO_TARGETS_PUSH = "No target. No enrolled device has a push route, so Straza cannot send a request to any of them.";
export const CHANNELS_FOOT = "Which channels announce a rule's requests is set on the rule (approve.notify). Deciding always works here, on the CLI and on the phone, whatever announced it.";

// ---- the enroll sheet, continued ----

// codeLeft is the enroll code's countdown, the clock the sentence beside the
// QR ends with. It carries no word of its own, since SCAN_LINE already says
// what is running out, and a code whose life is spent reads as expired.
export function codeLeft(seconds: number): string {
  if (seconds <= 0) return CODE_EXPIRED;
  if (seconds < 60) return seconds + " s";
  return Math.floor(seconds / 60) + " m " + String(seconds % 60).padStart(2, "0") + " s";
}

// ---- the queue, continued ----

// decideAction names a row's own Deny and Approve button, since a button
// in a table says nothing about which request it acts on.
export const decideAction = (v: Verdict, r: ApprovalRow) => (v === "approve" ? APPROVE : DENY) + " " + callOf(r.summary).call + " for " + (r.username || r.user);
export const SEARCH_LABEL = "Search requests";
export const FILTER_LABEL = "Which requests to show";

// ---- channels, continued ----

// channelsCount is the row count above the channels table. The server
// answers the same three rows every time, so the line never reads a
// singular.
export const channelsCount = (n: number) => n + " channels";

// testName names the row a Send a test button acts on, since a button in a
// table says nothing about which channel it sends through.
export const testName = (name: string) => SEND_TEST + " on " + name;

// ---- the self-service page ----

// selfPhoneLine is the person line of Add a phone on the self-service page,
// where the person is the one signed in and nobody is picked.
export const selfPhoneLine = (username: string) => "For you, " + username + ". The phone approves the requests routed to you.";
