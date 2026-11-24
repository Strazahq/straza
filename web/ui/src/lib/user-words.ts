// The sentences of the Users list and of a user's page. A fact is a pill
// or a chip, so most of what stands here is short. The sentence that
// explains a verb sits in that verb's dialog, behind its help icon.
import type { UserRow } from "./api";
import { agoLong, dayWord } from "./words";

// kindWord says what the identity is: the identity manager's typology first, then the
// kind Straza recorded at birth.
export function kindWord(u: Pick<UserRow, "kind" | "user_type">): "person" | "AI agent" | "service" {
  if (u.user_type === "service") return "service";
  if (u.user_type === "agent" || u.kind === "nhi") return "AI agent";
  return "person";
}

// sponsoringWords counts the agents a person sponsors.
export function sponsoringWords(n: number): string {
  return n === 1 ? "1 agent" : n + " agents";
}

// A dialog's body is one or two sentences of consequence. The mechanics sit
// behind the dialog's help icon.
export const lockBody = (name: string) => name + "'s sessions are revoked and their check-ins refused until Unlock.";
export const LOCK_HELP = "Your identity manager cannot lift this lock.";
export const REASON_LABEL = "Reason, recorded on the audit chain";
export const REASON_MISSING = "Type the reason. Straza refuses a lock without one.";
export const unlockBody = (name: string) => name + " can check in again from a fresh session.";
export const UNLOCK_HELP = "Every lock on this user is lifted, whoever set it. A session that ended stays ended. A disabled user stays disabled.";
export const disableBody = (name: string) => name + "'s sessions are revoked and no new one can check in until they are enabled again.";
export const DISABLE_HELP = "Enable here or through your identity manager. Roles are kept.";
export const enabledToast = (name: string) => name + " is enabled.";
// stillLockedToast is the toast of an Enable whose answer says a lock still
// refuses the user, whose strip then says who locked and why.
export const stillLockedToast = (name: string) => name + " is enabled and still locked.";

// The roles a user holds. A removed role reaches a session with live updates
// in seconds and any other at its next check-in.
export const revokeRoleBody = (name: string, role: string) => name + " loses " + role + ".";
// deadRowBody is the body of Remove for an assignment outside its window.
// An expired one gives nothing, and removing one not yet valid cancels the
// access it would give.
export const deadRowBody = (name: string, role: string, when: "not yet valid" | "expired") =>
  when === "expired" ? "This assignment of " + role + " is expired, so removing it changes no access." : name + " does not get " + role + " when the assignment starts.";
export const NO_ROLE = "no role yet";
export const ORIGIN_WORDS: Record<string, string> = { scim: "from your identity manager", admin: "assigned here" };

// originLine says where a direct assignment came from and when: the time
// gone by on its first day, the day after that, and no time for a row
// that carries none.
export function originLine(origin: string | undefined, at: string | undefined): string {
  const from = ORIGIN_WORDS[origin || "admin"] || origin || "";
  const t = at ? Date.parse(at) : NaN;
  if (Number.isNaN(t)) return from;
  return Date.now() - t < 86400000 ? from + " " + agoLong(at) : from + " on " + dayWord(at);
}

export const nothingToGrant = (catalogEmpty: boolean, stale: boolean) =>
  stale ? "The roles could not be read. Close this and reload the page."
    : catalogEmpty ? "No role exists yet to assign. Create one under Roles."
      : "Every role is already assigned.";

// The enrolled devices.
export const DEVICES_LINE = "Machines that hold an enroll credential for the CLI and the hook kit. Approver phones are listed under Approvals.";
export const NO_DEVICE = "No device is enrolled.";
export const deviceRevokeBody = (name: string, device: string) => "The enroll credential of " + device + " is deleted, and " + name + "'s sessions from it end within seconds.";
export const DEVICE_HELP = "Getting it back means enrolling again with a fresh sign-in. Other devices are untouched.";

// The assertion key of an AI agent.
export const NO_KEY_LINE = "No session can start until a key is registered.";
export const KEY_LABEL = "Public key, base64, 32 bytes";
export const KEY_HINT = "The Ed25519 public key this agent signs its client_credentials grant with. Registering another rotates it and ends the agent's running sessions.";
export const KEY_MISSING = "Paste the public key. Straza refuses anything that is not exactly a 32-byte Ed25519 key.";
export const keyRevokeBody = (name: string) => "The running sessions of " + name + " end now, and it can no longer mint sessions with the client_credentials grant.";
export const KEY_REVOKE_HELP = "The ID token the agent already holds stops working too, because it names the removed key. Register a new key to restore access.";

// The list.
export const EMPTY_DIRECTORY = "No user exists yet. Users arrive over SCIM from your identity manager, or through strazactl users create.";
export const EMPTY_FILTERED = "No user matches these filters.";
export const READING_LIST = "Reading the user list.";
export const NEVER_SEEN = "no session has ever checked in";

// A person's Sponsor cell carries its count as the link's text, so the
// name of the link says what a click does.
export const sponsorShow = (n: number, name: string) => "Show the " + sponsoringWords(n) + " " + name + " sponsors";
export const sponsorFilterWords = (name: string) => "Sponsored by " + name;

// The page's own reads: the word while one is out, and the subject its
// failure sentence names. A failed read never blanks the rest.
export const READING_USER = "Reading the user.";
export const READING_ROLES = "Reading the roles this user holds.";
export const READING_DEVICES = "Reading the enrolled devices.";
export const READING_KEY = "Reading the assertion key.";
export const READING_AGENTS = "Reading the sponsored agents.";
export const SUBJECT_USER = "This user";
export const SUBJECT_ROLES = "The role assignments";
export const SUBJECT_DEVICES = "The devices";
export const SUBJECT_KEY = "The assertion key";
export const SUBJECT_AGENTS = "The sponsored agents";

// windowWord says whether a grant sits outside its validity window, in the
// words its chip and its dialog use. An empty answer is a grant in force.
export function windowWord(row: { valid_from?: string; valid_to?: string }, now: number = Date.now()): "" | "not yet valid" | "expired" {
  if (row.valid_to && Date.parse(row.valid_to) <= now) return "expired";
  if (row.valid_from && Date.parse(row.valid_from) > now) return "not yet valid";
  return "";
}

// GRANT_MISSING answers a press of Assign with no role picked.
export const GRANT_MISSING = "Pick the role to assign.";

// The toast of a write that landed. The sentence that explains a verb
// stayed in its dialog, so these only say what is true now.
export const lockedToast = (name: string) => name + " is locked.";
export const unlockedToast = (name: string) => name + " is unlocked.";
export const disabledToast = (name: string) => name + " is disabled.";
export const revokedRoleToast = (name: string, role: string) => name + " no longer holds " + role + ".";
export const deviceRevokedToast = (device: string) => device + " is revoked.";
export const keySetToast = (name: string) => "The assertion key of " + name + " is registered.";
export const keyRevokedToast = (name: string) => "The assertion key of " + name + " is revoked.";

// ---- a user's page: the head ----

export const SUBJECT_ACCESS = "The servers and tools";
export const SUBJECT_SESSIONS = "The sessions";
export const SUBJECT_REQUESTS = "The waiting requests";
export const READING_ACCESS = "Reading the servers and tools.";
export const READING_SESSIONS = "Reading the sessions.";
export const NO_SUCH_USER = "No user has this id. The user may have been deleted.";
export const BACK_TO_USERS = "Back to Users";


// AGENCY_WORD is an agent's agency as a value.
export const AGENCY_WORD: Record<string, string> = { autonomous: "Autonomous", interactive: "Interactive", supervised: "Supervised" };
const FROM: Record<string, string> = { scim: "from your identity manager", local: "made in Straza" };
const from = (origin: string) => FROM[origin] || FROM.local;
const capital = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// The sentence under the title is these parts in this order, each a
// sentence of its own: where the user comes from, the sponsorship, the last
// check-in. The kind chip beside the title says what the user is, so only
// an agent's agency is said here. whereFrom is the part of a person, a
// service, and an agent with no agency. A user from Entra shows where it
// comes from as a tag instead, and agencyLine is then an agent's agency on
// its own, or the plain kind for an agent with none, so the line never
// opens on the sponsor.
export const whereFrom = (origin: string) => capital(from(origin)) + ".";
export const whatAgent = (agency: string | undefined, origin: string) => (AGENCY_WORD[agency || ""] ? AGENCY_WORD[agency || ""] + " agent, " + from(origin) + "." : whereFrom(origin));
export const agencyLine = (agency: string | undefined) => (AGENCY_WORD[agency || ""] ? AGENCY_WORD[agency || ""] + " agent." : "AI agent.");
// SPONSORED_BY is followed by the sponsor's name as a link and a full stop.
export const SPONSORED_BY = "Sponsored by";
export const NO_SPONSOR = "No sponsor.";
export const sponsorsLine = (n: number) => "Sponsors " + sponsoringWords(n) + ".";
export const lastSeenLine = (ago: string) => "Last seen " + ago + ".";
export const NOT_SEEN_YET = "Not seen yet.";

// ---- the head's actions and the strips under it ----

export const STOP_ACCESS = "Stop access";
export const REVOKE_ALL_SESSIONS = "Revoke all sessions";
export const LOCK = "Lock";
export const UNLOCK = "Unlock";
export const DISABLE = "Disable";
export const ENABLE = "Enable";
// The line under each choice of the Stop access menu.
export const stopSessionsLine = (name: string) => "Every active session ends now. " + name + " can start a new one.";
export const NO_ACTIVE_SESSION = "No session is active.";
export const STOP_LOCK_LINE = "Sessions end and no new one starts until you unlock.";
export const STOP_DISABLE_LINE = "The account is off until it is enabled again, here or by your identity manager.";
// DISABLE_IN_ENTRA stands for Disable on a user Entra owns, as a link to
// the account in Entra, where the disable is done.
export const DISABLE_IN_ENTRA = "Disable in Entra";

// A lock's strip says who set it, when and why. The reason is the words of
// whoever locked.
const LOCK_BY: Record<string, string> = { admin: "by an administrator", external: "by an external system", scim: "by your identity manager", entra: "by Entra" };
export const lockBy = (origin: string) => LOCK_BY[origin] || LOCK_BY.admin;
export const LOCK_STRIP = "Lock";
// LEFT_BY_DISABLE stands for the reason of a lock that a disable in Straza
// wrote and that outlived the disable.
export const LEFT_BY_DISABLE = "Left by a disable in Straza. Check-ins stay refused until you unlock.";
// LEFT_BY_IDM is the same for a row the identity manager's deactivation
// wrote, on a user who is active again.
export const LEFT_BY_IDM = "Left by a deactivation in your identity manager. Check-ins stay refused until you unlock.";
// LEFT_BY_ENTRA is the same for a row a disable read in Entra wrote.
export const LEFT_BY_ENTRA = "Left by a disable in Entra. Check-ins stay refused until you unlock.";
export const DISABLED_STRIP = "Disabled";
// rolesKept is the pill of a user disabled in Entra, whose roles work
// again once Entra enables the account.
export const rolesKept = (n: number) => (n === 1 ? "1 role kept" : n + " roles kept");

// ---- the glance tiles ----

export const GLANCE = {
  roles: (n: number) => (n === 1 ? "role" : "roles"),
  servers: (n: number, tools: number) => (n === 1 ? "server, " : "servers, ") + (tools === 1 ? "1 tool" : tools + " tools"),
  sessions: (n: number) => (n === 1 ? "active session" : "active sessions"),
  requests: (n: number) => (n === 1 ? "request waiting" : "requests waiting"),
  agents: (n: number) => (n === 1 ? "agent sponsored" : "agents sponsored"),
};

// ---- the tabs ----

export const USER_TAB = { access: "Access", sessions: "Sessions", agents: "Agents", devices: "Devices and keys", details: "Details" };

// ---- the Access tab ----

export const canDo = (name: string) => "What " + name + " can do";
export const ACCESS_HELP = "The tools on registered MCP servers, and on a server on the person's own machine that a policy names, as today's live policies decide them for this user.";
export const ON_MACHINE = "on the user's machine";
export const toolsNamed = (n: number) => (n === 1 ? "1 tool named" : n + " tools named");
export const MORE_BY_PATTERN = "more by pattern";
export const BY_PATTERN_ONLY = "Policy rules name this server's tools by a pattern, so there is no tool to list.";
// machineGives is one line of the Assign a role dialog: what the picked
// role's policies give on a server on a person's machine.
export function machineGives(app: string, named: number, open: boolean): string {
  if (named === 0) return "Tools of " + app + " on the person's machine, named by a pattern.";
  return (named === 1 ? "1 tool" : named + " tools") + " of " + app + " on the person's machine" + (open ? ", and more by a pattern." : ".");
}
export const ACCESS_COLUMN = { server: "Server", tools: "Tools", through: "Through" };
export const ofTools = (n: number, all: number) => n + " of " + all + (all === 1 ? " tool" : " tools");
export const toolTitle = (tool: string, outcome: string) => tool + ": " + outcome;
export const EVERY_TOOL = "every tool";
// EVERY_CALL stands beside the denied chip of a user whose every call the
// gateway refuses before any policy runs.
export const EVERY_CALL = "every call";
export const NOT_RUNNING = "not running";
export const reachesNothing = (name: string) => name + " reaches no MCP server.";
export const CONSOLE_FACT = "Console";
export const APPROVALS_FACT = "Approvals";
export const THROUGH = "through";
export const DECIDES_FOR = "decides for";
export const DECIDES_NOTHING = "decides nothing";
export const SPONSORS = "sponsors";

// ---- the Roles list of the Access tab ----

export const ROLES_HEAD = "Roles";
export const ASSIGN_A_ROLE = "Assign a role";
export const IDM_OWNS = "Your identity manager owns these assignments. A change made here lasts until its next sync.";
const joined = (xs: string[]) => (xs.length <= 1 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);
// roleOn is a role's kind and the servers it is for: application role on github.
export const roleOn = (kind: string, servers: string[]) => kind + (servers.length ? " on " + joined(servers) : "");
export const throughRoles = (roles: string[]) => "through " + joined(roles);
export const removeName = (role: string) => "Remove " + role;
export const removeTitle = (role: string, name: string) => "Remove " + role + " from " + name + "?";
export const REMOVE_ROLE = "Remove role";
export const auditTrailOf = (name: string) => "Audit chain of " + name;

// ---- the Assign a role dialog ----

export const assignTitle = (name: string) => "Assign a role to " + name;
// The line under a role says what it gives. toolsOn is one server of an
// application role, and the lines of several servers are joined.
export const toolsOn = (n: number, all: number, server: string) => (n === all ? "every tool" : ofTools(n, all)) + " on " + server;
export const givesTools = (parts: string[]) => capital(joined(parts)) + ".";
export const NO_SERVER_TOOL = "No tool on a registered server.";
export const includesRoles = (roles: string[]) => (roles.length ? "Includes " + joined(roles) + "." : "Includes no role yet.");
export const DECIDES_REQUESTS = "Decides requests routed to this role.";
export const NOT_FOR_AGENT = "Not for an agent or a service.";
export const NO_ROLE_MATCHES = "No role matches your search.";
export const assignName = (role: string) => "Assign " + role;
export const assignedBanner = (role: string, name: string) => role + " assigned to " + name + ". Sessions with live updates get it in seconds, others within 5 minutes.";
export const openSessions = (name: string, n: number) => "Open " + name + (n === 1 ? "'s session" : "'s sessions");
export const DISMISS = "Dismiss";

// ---- the Sessions tab ----

export const SESSION_COLUMN = { session: "Session", harness: "Harness", seen: "Last seen", status: "Status" };
export const openSession = (short: string) => "Open session " + short;
export const NO_SESSION = "No session yet.";
export const allSessionsOf = (name: string) => "All sessions of " + name;
export const revokeAllTitle = (name: string) => "Revoke every active session of " + name + "?";
export const revokeAllBody = (n: number, name: string) => (n === 1 ? "Its one active session ends" : "All " + n + " active sessions end") + " within seconds. " + name + " can start a new one.";

// ---- the Devices and keys tab ----

export const KEY_FACT = "Assertion key";
export const DEVICES_FACT = "Devices";
export const PHONES_FACT = "Approver phones";
export const REGISTERED = "registered";
export const NO_KEY = "no key";
export const since = (day: string) => "since " + day;
export const REGISTER_KEY = "Register key";
export const REVOKE_KEY = "Revoke key";
export const phones = (n: number) => (n === 1 ? "1 phone" : n + " phones");
export const enrolledOn = (day: string) => "enrolled " + day;
export const UNKNOWN_PLATFORM = "unknown";
export const REVOKE = "Revoke";
export const revokeName = (what: string) => "Revoke " + what;
export const REVOKE_DEVICE = "Revoke device";

// ---- the Details tab ----

export const DETAIL = { name: "Sign-in name", email: "Email", external: "External id", title: "Title", source: "Source", created: "Created", seen: "Last seen", agency: "Agency", sponsor: "Sponsor", swarm: "Swarm", ephemeral: "Ephemeral" };
export const NOT_SET = "not set";
export const YES = "yes";

// ---- the Agents tab ----

export const agentsOf = (name: string) => "Agents " + name + " sponsors";
export const allAgentsOf = (name: string) => "All agents " + name + " sponsors";
export const openUser = (name: string) => "Open " + name;

// ---- the titles of the confirms ----

export const lockTitle = (name: string) => "Lock " + name + "?";
export const unlockTitle = (name: string) => "Unlock " + name + "?";
export const disableTitle = (name: string) => "Disable " + name + "?";
export const deviceTitle = (device: string) => "Revoke " + device + "?";
export const keyTitle = (name: string) => "Revoke the assertion key of " + name + "?";
