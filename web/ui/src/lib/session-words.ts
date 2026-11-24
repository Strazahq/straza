// The sentences of the Sessions screen and the page of one session.
import type { SessionRow } from "./api";
import { HARNESS_WORD } from "./harness-words";

// Attestation vocabulary, the wire's real value set: managed, advisory,
// none. One sentence each, on the badge's tooltip and in the sheet.
export const ATT_TITLE: Record<string, string> = {
  managed: "Managed install: every artifact the registry lists for this harness and platform reported a hash from its allowed set.",
  advisory: "User-mode install: hashes are recorded but not verified against the registry, or nothing relevant is registered to check them against.",
  none: "Nothing measured, or a managed claim failed: tamper evidence.",
};
export const attTone = (a: string | undefined): "ok" | "warn" | "danger" => (a === "managed" ? "ok" : a === "advisory" ? "warn" : "danger");

// Wiring status, server-classified: which harness-config render a
// session's managed wiring hash matches.
export const WIRING_TITLE: Record<string, string> = {
  current: "The managed hook configuration matches the version Straza publishes.",
  allowed: "An earlier or custom approved configuration. Re-run install --managed.",
  mismatch: "This hash matches no registered row: tampered or never registered. With the attestation floor at managed, it is refused.",
  unmeasured: "No managed measurement: a user-mode install.",
};
export const wiringTone = (w: string | undefined): "ok" | "warn" | "danger" | "plain" => (w === "current" ? "ok" : w === "allowed" ? "warn" : w === "mismatch" ? "danger" : "plain");
export const shortHash = (h: string | undefined) => (h ? h.replace(/^sha256:/, "").slice(0, 12) + "…" : "");

export const statusTone = (s: string): "ok" | "danger" | "plain" => (s === "active" ? "ok" : s === "revoked" ? "danger" : "plain");
export const shortID = (id: string) => id.slice(0, 13) + "…";
// cutID is the first eight characters of an id, the way the list and the
// page show a session or a user.
export const cutID = (id: string) => id.slice(0, 8);
// sessionHarness reads a session's harness, which the server sends as the
// name and the harness's own version joined by a slash, the way a person
// says it: "Claude Code 2.2.0". A name without a word stays as it is.
export function sessionHarness(harness: string): string {
  const cut = harness.indexOf("/");
  const name = cut < 0 ? harness : harness.slice(0, cut);
  const version = cut < 0 ? "" : harness.slice(cut + 1);
  return (HARNESS_WORD[name] || name) + (version ? " " + version : "");
}

// The kill switch.
export const revokeBody = (n: number, name: string) =>
  "Every agent action in " + (n === 1 ? "this session" : "these " + n + " sessions") + (name ? " of " + name : "") +
  " is denied within seconds. The device can start a new session. To keep it out, lock the user or revoke the device.";
export const OWN_SESSION_WARNING = "This includes your own session. You will be signed out.";
export const revokeLabel = (n: number) => (n === 1 ? "Revoke session" : "Revoke " + n + " sessions");
export const selectedWords = (n: number, shown: number, name: string) => n + " of " + shown + " selected" + (name ? " · " + name : "");
export const revokedWords = (n: number) => n + (n === 1 ? " session revoked." : " sessions revoked.");

// The headline above the filter row: the count of active sessions on the
// server at stat size, with these words beside it.
export const activeWords = (n: number) => (n === 1 ? "active session" : "active sessions");

// The group header of one person's sessions: grouped by user, so a fleet
// reads as people.
export const groupWords = (n: number) => n + (n === 1 ? " session" : " sessions");
export const showGroup = (name: string) => "Show the sessions of " + name;
export const hideGroup = (name: string) => "Hide the sessions of " + name;
export const EMPTY_ACTIVE = "No agent is checked in.";
export const EMPTY_FILTERED = "No session matches these filters.";
export const READING = "Reading the session list.";

// ---- the list ----

export const FILTER_LABEL = "Status";
export const FILTER_ALL = "All";

// The push state beside the headline, two words and the rest behind help.
export const LIVE_ON = "Live updates on";
export const LIVE_OFF = "Live updates off";
export const LIVE_HELP_NAME = "About live updates";
export function liveHelp(push: { lane_up?: boolean; connected?: number }, active: number): string {
  if (!push.lane_up) return "Straza cannot push a change to its clients right now. Each client checks for changes every 30 s.";
  const c = push.connected || 0;
  if (c >= active) return "Every active client is connected to Straza and hears of a change, such as a revoke, at once.";
  return c + " of " + active + " active clients are connected to Straza and hear of a change, such as a revoke, at once. The others check every 30 s.";
}

// countWords is the muted count beside the filters.
export const countWords = (loaded: number, more: boolean) => loaded + (loaded === 1 ? " session" : " sessions") + (more ? " loaded, more on Straza" : "");

// SESSION_LABELS names the columns, in the headers and the Columns menu.
export const SESSION_LABELS: Record<string, string> = {
  user: "User",
  session: "Session",
  harness: "Harness",
  health: "Health",
  last_seen: "Last seen",
  status: "Status",
  client: "Client build",
  attestation: "Attestation",
  wiring: "Hook configuration",
  started: "Started",
};
export const THIS_CONSOLE = "this console";
export const selectName = (id: string) => "Select session " + id;

// groupLine is the muted words after a group's name: the active sessions
// under the Active filter, and every session with the active ones after it
// under the others.
export function groupLine(total: number, active: number, onlyActive: boolean): string {
  if (onlyActive) return active + (active === 1 ? " active session" : " active sessions");
  return groupWords(total) + (active ? ", " + active + " active" : "");
}
export const GROUPS_OPEN_MAX = 30;

// ---- the Health cell ----

// Health is what the Health cell says of one session: one icon, a few
// words, and the two full sentences on hover.
export type Health = { level: "ok" | "warn" | "danger" | "plain"; word: string; title: string };
export const HEALTH_WORD = {
  verified: "Verified",
  clientNot: "Client not verified",
  clientFailed: "Client unmeasured or failed",
  hookOld: "Hook configuration not current",
  hookUnknown: "Hook configuration not registered",
  hookUnmeasured: "Hook configuration not measured",
  admin: "Not measured",
};
export const ADMIN_HEALTH_TITLE = "A session of the console, of the self-service page or of strazactl runs no agent, so nothing is measured.";

// ADMIN_HARNESSES are the harness names of a session that runs no agent.
const ADMIN_HARNESSES = ["console", "self-service", "strazactl"];

// sentence is the sentence a map holds for a wire value, and nothing for a
// value it does not hold, inherited names included.
const sentence = (map: Record<string, string>, key: string) => (Object.hasOwn(map, key) ? map[key] : "");

// healthOf reads a session's attestation and hook configuration as one
// Health. The first case that fits wins: an admin harness, then the client,
// then the hook configuration, the failures before the doubts. Only a
// managed client with the current hook configuration is verified, so a
// status the console does not know never reads as verified.
export function healthOf(row: Pick<SessionRow, "harness" | "attestation" | "wiring_status">): Health {
  const name = row.harness.split("/")[0];
  if (ADMIN_HARNESSES.includes(name)) return { level: "plain", word: HEALTH_WORD.admin, title: ADMIN_HEALTH_TITLE };
  const att = row.attestation;
  const wiring = row.wiring_status || "";
  const title = [sentence(ATT_TITLE, att), sentence(WIRING_TITLE, wiring)].filter(Boolean).join(" ");
  const said = (level: Health["level"], word: string): Health => ({ level, word, title });
  if (att !== "managed" && att !== "advisory") return said("danger", HEALTH_WORD.clientFailed);
  if (wiring === "mismatch") return said("danger", HEALTH_WORD.hookUnknown);
  if (att === "advisory") return said("warn", HEALTH_WORD.clientNot);
  if (wiring === "allowed") return said("warn", HEALTH_WORD.hookOld);
  if (wiring === "current") return said("ok", HEALTH_WORD.verified);
  return said("warn", HEALTH_WORD.hookUnmeasured);
}

// userKindOf reads a session's user_kind as the kind its tile wears, and
// answers null for a session that sends none or one the console does not
// know.
export const userKindOf = (kind: string | undefined): "person" | "agent" | "service" | null =>
  kind === "person" || kind === "agent" || kind === "service" ? kind : null;

// ---- the list's parts that the server's sessions answer feeds ----

export const SEARCH_LABEL = "Search sessions";
export const SEARCH_PLACEHOLDER = "Search a user or a session id";
export const userFilter = (name: string) => "Sessions of " + name;
export const CLEAR_USER = "Clear the user filter";
export const sponsoredBy = (name: string) => "sponsored by " + name;
export const WAITING_COLUMN = "Waiting";
// waitingWords names the Waiting chip: "2 waiting".
export const waitingWords = (n: number) => n.toLocaleString("en-US") + " waiting";
// The pills beside the headline count the users that hold an active session.
export const people = (n: number) => (n === 1 ? "1 person" : n + " people");
export const agents = (n: number) => (n === 1 ? "1 agent" : n + " agents");
export const services = (n: number) => (n === 1 ? "1 service" : n + " services");

// ---- where a session runs ----

export const WHERE_COLUMN = "Where";
// YOUR_BROWSER stands in the Where cell of the console's own session.
export const YOUR_BROWSER = "your browser";
// The words between the parts of the head's first sentence: "Started 5
// minutes ago on dev-laptop in ~/projects/game-audio."
export const started = (ago: string) => "Started " + ago;
export const ON_MACHINE = "on";
export const IN_FOLDER = "in";

// ---- the page of one session ----

export const SUBJECT_SESSION = "This session";
export const READING_SESSION = "Reading the session.";
export const NO_SUCH_SESSION = "No session has this id.";
export const BACK_TO_SESSIONS = "Back to Sessions";
export const sessionTitle = (who: string, harness: string) => who + " on " + harness;
export const seenLine = (ago: string) => "Last seen " + ago + ".";
const KIND_LINE: Record<string, string> = { person: "Person", agent: "AI agent", service: "Service" };
export const kindLine = (kind: string) => KIND_LINE[kind] || "";
export const SPONSORED_BY = "sponsored by";
export const REVOKE_SESSION = "Revoke session";
export const REVOKE_TITLE = "Revoke this session?";
export const revokePageBody = (name: string) =>
  "Every agent action in this session is denied within seconds. " + name + " can start a new session. To keep " + name + " out, lock the user or revoke the device.";
export const revokedToast = "Session revoked.";

// The glance tiles.
export const TILE = {
  allowed: (n: number) => (n === 1 ? "call allowed" : "calls allowed"),
  waiting: () => "waiting for approval",
  denied: () => "denied",
  turns: (n: number) => (n === 1 ? "turn recorded" : "turns recorded"),
};

// The activity.
export const ACTIVITY = "Activity";
export const ACTIVITY_FILTER_LABEL = "Which calls to show";
export const ACTIVITY_FILTER = { all: "All", waiting: "Waiting", denied: "Denied" };
export const SUBJECT_ACTIVITY = "The activity";
export const READING_ACTIVITY = "Reading what the session did.";
export const NO_ACTIVITY = "Nothing is recorded for this session yet.";
export const NO_ACTIVITY_HERE = "No call of this kind.";
export const RELOAD_ACTIVITY = "Reload the activity";
export const OPEN_REQUEST = "Open the request";
export const waitingFor = (who: string, left: string) => "Waiting for " + who + ". " + left + ", then the call is denied.";
export const WAITING_PLAIN = "Waiting for a person's answer.";
export const approvedBy = (who: string) => "Approved by " + who + ".";
export const deniedBy = (who: string) => "Denied by " + who + ".";
export const NOBODY_DECIDED = "Nobody decided in time. The call was denied.";
export const noRoleReaches = (who: string) => "No role of " + who + " reaches this tool.";
export const ruleLine = (rule: string, policy: string) => "Policy rule " + rule + (policy ? " in " + policy : "");
export const throughRole = (role: string) => "Allowed through " + role + ".";
export const DENIED_BY_DEFAULT = "Denied by default. No policy rule allows it.";
export const allRecords = (n: number) => (n === 1 ? "The 1 record in Audit" : "All " + n.toLocaleString("en-US") + " records in Audit");
export const NEWEST_RECORDS = "Open all records in Audit";

// The Health card.
export const HEALTH = "Health";
export const CLIENT_ROW: Record<string, [string, string]> = {
  managed: ["Client verified", "The installed client matches the registry."],
  advisory: ["Client not verified", "Its hashes were reported and could not be checked against the registry."],
  none: ["Client unmeasured or failed", "Nothing was measured, or a managed claim failed its check."],
};
export const HOOK_ROW: Record<string, [string, string]> = {
  current: ["Hook configuration current", "It matches what Straza publishes."],
  allowed: ["Hook configuration not current", "An approved configuration of an earlier version or a custom installation."],
  mismatch: ["Hook configuration not registered", "Its hash matches no registered configuration."],
  unmeasured: ["Hook configuration not measured", "A user-mode install is not measured."],
};
export const NOT_MEASURED_ROW: [string, string] = ["Not measured", "A session of the console, of the self-service page or of strazactl runs no agent."];
export const IDS_AND_HASHES = "Ids and hashes";
export const ID_FACT = { id: "Session id", client: "Client build", hook: "Hook configuration", started: "Started", seen: "Last seen" };

// The Recording card.
export const RECORDING = "Recording";
export const RECORDED: Record<string, string> = { verbatim: "Recorded, word for word", redact: "Recorded, secrets masked", mixed: "Recorded, partly masked" };
export const turnsSoFar = (n: number) => (n === 1 ? "1 turn so far." : n + " turns so far.");
export const NOT_RECORDED = "Not recorded";
export const NOT_RECORDED_LINE = "No policy records this session.";
export const OPEN_TRANSCRIPT = "Open transcript";
export const SUBJECT_RECORDING = "The recording";
export const READING_RECORDING = "Reading the recording.";
