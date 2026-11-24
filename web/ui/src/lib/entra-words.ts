// The words of a role's group in Entra: the block on the role's page, the
// members Straza did not apply, a holder's source, the two confirms and
// the Overview lines. A state, a member's type and a reason are looked up
// here, and a value the tables do not know shows as the server sent it, so
// a new server value reads without a change to this page.
import type { EntraConfig, EntraMember } from "./api";
import { agoWord } from "./words";

// ENTRA_GROUP_URL opens a group's members in the Entra admin centre. The
// address form is not verified against a live tenant.
export const ENTRA_GROUP_URL = (groupID: string) => "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/GroupDetailsMenuBlade/~/Members/groupId/" + encodeURIComponent(groupID);

// ACCOUNT_BLADE is where the Entra admin centre keeps an account, by the
// account's type in Entra's words: a person and an agent user on the user
// profile, an agent identity and an application among the enterprise apps.
// These address forms are not verified against a live tenant.
const ACCOUNT_BLADE: Record<string, string> = {
  user: "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/",
  agentUser: "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/",
  agentIdentity: "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/",
  servicePrincipal: "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/",
};

// entraAccountURL opens the account with the object ID id in the Entra
// admin centre, or is "" for a type the table does not know, which then
// gets no link.
export const entraAccountURL = (type: string, id: string) => (ACCOUNT_BLADE[type] && id ? ACCOUNT_BLADE[type] + encodeURIComponent(id) : "");

// entraObjectURL opens any object Entra lists, a group included, or is ""
// for a type the console cannot place there.
export const entraObjectURL = (type: string, id: string) => (type === "group" && id ? ENTRA_GROUP_URL(id) : entraAccountURL(type, id));

export const GROUP_TITLE = "Group in Entra";
export const FROM_ENTRA = "From Entra";
export const ENTRA = "Entra";
export const OPEN_IN_ENTRA = "Open in Entra";
export const opensAccount = (who: string) => "Opens " + who + " in the Entra admin centre";

// STATE_WORD is the chip of a group's state. stale and not-owner are not
// server states: stale comes from the read of Entra, and not-owner is a
// linked group that carries a sentence.
export const STATE_WORD: Record<string, string> = {
  creating: "creating",
  linked: "linked",
  stale: "stale",
  missing: "group deleted",
  held: "removals held",
  "not-owner": "not owner",
};
export const stateWord = (s: string) => STATE_WORD[s] || s;

// KIND_WORD is the type chip of a member, in Entra's words. servicePrincipal
// is an application that is no agent identity.
export const KIND_WORD: Record<string, string> = { user: "user", agentUser: "agent user", agentIdentity: "agent identity", servicePrincipal: "application", group: "group" };
export const kindWord = (k: string) => KIND_WORD[k] || k;

// REASON_WORD is the chip of why a member holds no role.
export const REASON_WORD: Record<string, string> = {
  "not-read": "kind not read",
  "name-taken": "name taken",
  unread: "type and ID only",
  "not-agent": "not an agent",
};
export const reasonWord = (r: string) => REASON_WORD[r] || r;

// READ_NAME is how identity.entra.read names a member's type, as words and
// as the config value.
const READ_NAME: Record<string, [string, string]> = { user: ["people", "people"], agentUser: ["agent users", "agentUsers"], agentIdentity: ["agent identities", "agentIdentities"] };

// reasonFix is the line of what to do under a reason, or "" for a reason
// with no known fix. kinds are the types of the members with the reason. A
// name-taken member carries its fix as a link on its own row, and so does
// an application, which is removed from the group in Entra.
export function reasonFix(reason: string, kinds: string[]): string {
  if (reason !== "not-read") return "";
  const names = [...new Set(kinds)].map((k) => READ_NAME[k]).filter(Boolean);
  if (!names.length) return "";
  return "identity.entra.read leaves out " + list(names.map((n) => n[0])) + ". Add " + list(names.map((n) => n[1])) + " there, or remove the member in Entra.";
}

const list = (xs: string[]) => (xs.length <= 1 ? xs.join("") : xs.slice(0, -1).join(", ") + " and " + xs[xs.length - 1]);

// byReason groups the members by reason, in the order each reason first
// shows.
export function byReason(members: EntraMember[]): [string, EntraMember[]][] {
  const out = new Map<string, EntraMember[]>();
  for (const m of members) out.set(m.reason, [...(out.get(m.reason) || []), m]);
  return [...out];
}

// The pills of the block.
export const readPill = (iso: string) => "read " + agoWord(iso);
export const lastGoodPill = (iso: string) => "last good read " + agoWord(iso);
export const membersPill = (n: number) => (n === 1 ? "1 member" : n + " members");
export const notAppliedPill = (n: number) => n + " not applied";

// groupName is the group's name in Entra: the config's prefix and the
// role's name. Without the config it is the group's object ID, and the
// role's name while the group has none yet.
export const groupName = (role: string, groupID: string, config: EntraConfig | null) =>
  config && config.group_name_prefix !== undefined ? config.group_name_prefix + role : groupID || role;
export const openGroupTitle = (group: string) => "Open " + group + " in the Entra admin centre";

// The block's sentences while something is wrong.
export const KEPT = "Holders stay as last read.";
export const deletedLine = (group: string, product: boolean) =>
  "Entra has no group " + group + " any more. Straza stopped reading it. " + KEPT + (product ? " Create the group again." : " Create the group again, or delete the role.");
export const ownerLine = (group: string, app: string) =>
  "Straza is no longer an owner of " + group + ", so it cannot rename or delete it. Reads still work. Add " + app + " as an owner of the group in Entra.";

// Apply the removals, for a group whose removals are held.
export const APPLY_REMOVALS = "Apply the removals";
export const applyTitle = (role: string) => "Apply the removals to " + role + "?";
export const appliedToast = (role: string) => "Straza applies the removals of " + role + " at its next read.";

// Create the group again, for a group deleted in Entra.
export const RECREATE = "Create the group again";
export const RECREATE_TITLE = "Create the group again?";
export const recreateBody = (group: string) => "Straza creates an empty group " + group + " in Entra. Add the holders to it there.";
export const loseLabel = (role: string) => "Lose " + role;
export const KEEP_LABEL = "Keep it, assigned another way";
export const recreatedToast = (group: string) => "Straza is creating " + group + " in Entra.";

// The members not applied, on the Holders tab.
export const NOT_APPLIED_TITLE = "Members not applied";
export const EVERY_MEMBER_APPLIED = "every member applied";
export const COPY_ID = "Copy the Entra object ID";
export const copiedID = (id: string) => "Copied " + id;

// A holder's source on the Holders tab, and a role from Entra on a user's
// page.
export const REMOVE_IN_ENTRA = "remove in Entra";
export const CHANGE_IN_ENTRA = "Change in Entra";
export const opensGroup = (group: string) => "Opens " + group + " in the Entra admin centre";

// The Remove of a role a user holds from Entra and by hand.
export const removeBothBody = (name: string, role: string, origin: string | undefined) =>
  (origin === "scim" ? "Only the assignment from your identity manager goes. " : "Only the assignment made here goes. ") + name + " keeps " + role + " from Entra.";
export const keepsToast = (name: string, role: string) => name + " keeps " + role + " from Entra.";

// ---- an agent from Entra ----

// IN_ENTRA_AS is the Details row of the objects Entra has for an agent,
// each a chip in Entra's words with what it is on hover.
export const IN_ENTRA_AS = "In Entra as";
export const OBJECT_HINT: Record<string, string> = {
  agentIdentity: "The agent's identity in Entra. It gets the tokens.",
  agentUser: "An optional user account of the agent in Entra, for things that need a user, such as a mailbox.",
};

// NO_SPONSOR_CHIP marks an agent from Entra that has no sponsor in Straza,
// because Entra lists no single person Straza has.
export const NO_SPONSOR_CHIP = "no sponsor";
export const ENTRA_LISTS = "Entra lists";

// SPONSOR_FIX is what to do, by why the agent has no sponsor. A state the
// table does not know shows as the server sent it.
export const SPONSOR_FIX: Record<string, string> = {
  none: "Add one person as its sponsor in Entra.",
  several: "Keep one person as its sponsor in Entra.",
  "not-person": "Name one person as its sponsor in Entra.",
  "no-user": "Give this person a role in Straza, or name another sponsor in Entra.",
};

// SUBJECT_ENTRA names the read of GET /v1/admin/entra in the sentence of
// its failure, on the role's page and the Overview.
export const SUBJECT_ENTRA = "The state of the Entra sync";

// forLong is the time since a stamp the long way a sentence holds it,
// with no "ago": 1 minute, 3 hours, 2 days.
export function forLong(iso: string, now: number = Date.now()): string {
  const s = Math.max(0, Math.floor((now - Date.parse(iso)) / 1000));
  const [n, unit] = s < 3600 ? [Math.max(1, Math.floor(s / 60)), "minute"] : s < 86400 ? [Math.floor(s / 3600), "hour"] : [Math.round(s / 86400), "day"];
  return n + " " + unit + (n === 1 ? "" : "s");
}

// staleLine is the Overview line while reads of Entra fail: how long, the
// server's sentence of the cause and the fix, and what holders keep.
export const staleLine = (since: string, error: string | undefined) =>
  "Straza has not read Entra for " + forLong(since) + (error ? ": " + error.trim().replace(/([^.])$/, "$1.") : ".") + " " + KEPT;

// CERT_CODE is Entra's code for a service app that holds no certificate of
// the assertion key, whose fix is on the Agent signing keys tab.
export const CERT_CODE = "AADSTS700027";
