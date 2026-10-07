// The sentences of the Users screen and the user sheet. State rows, facts
// and lists carry the
// short forms; the sentence that explains a verb sits in that verb's
// dialog or tooltip, never as prose on the sheet.
import type { UserRow } from "./api";

// kindWord says what the identity is: the identity manager's typology first, then the
// kind Straza recorded at birth.
export function kindWord(u: Pick<UserRow, "kind" | "user_type">): "person" | "AI agent" | "service" {
  if (u.user_type === "service") return "service";
  if (u.user_type === "agent" || u.kind === "nhi") return "AI agent";
  return "person";
}

// originWord is the badge word: the wire's own spelling, since the console
// cannot know which product provisions over SCIM.
export function originWord(origin: string | undefined): string {
  return origin === "scim" ? "SCIM" : "local";
}

// originLine is the Origin fact of the sheet.
export function originLine(u: Pick<UserRow, "origin" | "external_id">): string {
  if (u.origin === "scim") return "your identity manager, over SCIM" + (u.external_id ? ", external id " + u.external_id : "");
  return "Straza, through this console or strazactl";
}

// sponsoringWords is the Agents cell and the Sponsoring fact.
export function sponsoringWords(n: number): string {
  return n === 1 ? "1 agent" : n + " agents";
}

export const LOCK_TIP = "Revokes every session and refuses check-ins until Unlock. Status and roles are kept; the identity manager cannot lift it.";
export const DISABLE_TIP = "Sets the status to disabled: every session is revoked and check-in is refused until it is enabled again, here or by the identity manager. Roles are kept.";
export const ENABLE_TIP = "Sets the status back to active, so the user can check in again. A Straza lock, if any, stays.";
export const UNLOCK_TIP = "Lifts every revocation lane for this user, the only way to lift a lock the identity manager cannot touch.";

// lockedLine is the red state row on a locked user.
export function lockedLine(origin: string): string {
  return origin === "external" ? "Locked by an external system." : "Locked by Straza.";
}

// A dialog body is one or two sentences of consequence; the mechanics sit
// behind the dialog's help icon.
export const lockBody = (name: string) => name + "'s sessions are revoked and their check-ins refused until Unlock.";
export const LOCK_HELP = "The status stays as it is: an identity manager re-enable cannot lift this lock, only Unlock can. The reason is recorded on the audit chain.";
export const REASON_LABEL = "Reason, recorded on the audit chain";
export const REASON_MISSING = "Type the reason. The server refuses a lock without one.";
export const unlockBody = (name: string) => name + " can check in again from a fresh session.";
export const UNLOCK_HELP = "Every revocation lane is cleared. Dead sessions stay dead. A user the identity manager disabled stays disabled.";
export const disableBody = (name: string) => name + "'s sessions are revoked and no new one can check in until they are enabled again.";
export const DISABLE_HELP = "Enable here or through the identity manager. Roles are kept.";
export const enableBody = (name: string) => name + " can check in again.";
export const ENABLE_HELP = "Roles were kept. A Straza lock, if any, stays until Unlock.";

// The Roles section.
export const CHECKIN_HELP = "A running session keeps its roles until its next check-in, at most 5 minutes.";
export const grantBody = (name: string, role: string) => name + " holds " + role + " from the next check-in.";
export const GRANT_HELP = CHECKIN_HELP + " The assignment has no end date.";
export const revokeRoleBody = (name: string, role: string) => name + " loses " + role + " at the next check-in.";
export const deadRowBody = (role: string, when: string) => "The row is deleted; this assignment of " + role + " is " + when + ", so access does not change.";
export const DRIFT_LINE = "Membership is mastered by your identity manager; a change here is drift its next reconciliation can undo.";
export const driftBody = (role: string, verb: string) => " Your identity manager masters membership of " + role + "; this " + verb + " is drift it can undo.";
export const DRIFT_HELP = "Your identity manager writes who holds this role over SCIM. A change made here lasts until its next reconciliation. Make it there for it to stay.";
export const IMPLIED_LINE = "Composed roles are not listed; the server applies them on top of these assignments.";
export const NO_ROLE = "no role yet";
export const ORIGIN_WORDS: Record<string, string> = { scim: "from your identity manager", admin: "assigned here" };
export const nothingToGrant = (catalogEmpty: boolean, stale: boolean) =>
  stale ? "The role catalog could not be read, so nothing can be offered until it comes back."
    : catalogEmpty ? "No role exists yet to assign. Create one under Roles."
      : "Every role is already assigned.";

// The Devices section.
export const DEVICES_LINE = "Machines holding an enroll credential for the CLI and hook kit. Approver phones are a separate fleet, under Approvals.";
export const NO_DEVICE = "No device is enrolled.";
export const deviceRevokeBody = (name: string, device: string) => "The enroll credential of " + device + " is deleted; " + name + "'s sessions from it are revoked within seconds.";
export const DEVICE_HELP = "Getting it back means enrolling again with a fresh sign-in. Other devices are untouched.";

// The Assertion key section.
export const NO_KEY_LINE = "Cannot start a session until a key is registered.";
export const KEY_LABEL = "Public key, base64, 32 bytes";
export const KEY_HINT = "The Ed25519 public key this agent signs its client_credentials grant with. One key per agent; registering again rotates it. The private half never travels.";
export const KEY_MISSING = "Paste the public key. The server refuses anything that is not exactly a 32-byte Ed25519 key.";
export const keyRevokeBody = (name: string) => name + " can no longer mint sessions with the client_credentials grant.";
export const KEY_REVOKE_HELP = "Running sessions keep going until revoked; register a new key to restore access.";

// The list.
export const SEARCH_PLACEHOLDER = "Search by name, email or external id";
export const countWords = (loaded: number, more: boolean) => loaded + (loaded === 1 ? " user" : " users") + " loaded" + (more ? ", more on the server" : "");
export const sortedWords = (key: string, asc: boolean) =>
  key === "last_seen" ? "Sorted by last seen, " + (asc ? "longest ago" : "most recent") + " first, on the server."
    : key === "created" ? "Sorted by creation, " + (asc ? "oldest" : "newest") + " first, on the server."
      : "Sorted by " + (key === "status" ? "status" : "name") + (asc ? "" : ", reversed") + ".";
export const EMPTY_DIRECTORY = "No user exists yet. Users arrive over SCIM from your identity manager, or through strazactl users create.";
export const EMPTY_FILTERED = "No user matches these filters. The server searched the whole directory, not just the loaded rows.";
export const READING_LIST = "Reading the user list.";
export const NEVER_SEEN = "no session has ever checked in";

// The Agents cell carries its count as the button's text, so the name of
// the button says what a click does.
export const sponsorShow = (n: number, name: string) => "Show the " + sponsoringWords(n) + " " + name + " sponsors";
export const sponsoredBy = (sponsor: string) => "sponsored by " + sponsor;
export const sponsorFilterWords = (name: string) => "Sponsored by " + name;

// The sheet's own reads: the word while one is out, and the subject its
// failure sentence names. A failed sub-read never blanks the rest.
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

// provisionedWords is the sheet's one line under the title when the
// identity carries neither a display name nor an email.
export const provisionedWords = (when: string) => "Provisioned " + when + ".";

// windowWord says whether a grant sits outside its validity window, in the
// words its badge and its dialog use. An empty answer is a grant in force.
export function windowWord(row: { valid_from?: string; valid_to?: string }, now: number = Date.now()): "" | "not yet valid" | "expired" {
  if (row.valid_to && Date.parse(row.valid_to) <= now) return "expired";
  if (row.valid_from && Date.parse(row.valid_from) > now) return "not yet valid";
  return "";
}

// The Grant line of the Roles section.
export const GRANT_PICK = "pick a role";
export const GRANT_MISSING = "Pick the role to assign. The list holds every role this user does not hold yet.";

// The toast of a write that landed. The sentence that explains a verb
// stayed in its dialog, so these only say what is true now.
export const lockedToast = (name: string) => name + " is locked.";
export const unlockedToast = (name: string) => name + " is unlocked.";
export const disabledToast = (name: string) => name + " is disabled.";
export const grantedToast = (name: string, role: string) => name + " holds " + role + " now.";
export const revokedRoleToast = (name: string, role: string) => name + " no longer holds " + role + ".";
export const deviceRevokedToast = (device: string) => device + " is revoked.";
export const keySetToast = (name: string) => "The assertion key of " + name + " is registered.";
export const keyRevokedToast = (name: string) => "The assertion key of " + name + " is revoked.";
