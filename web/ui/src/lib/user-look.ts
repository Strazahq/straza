// What a user is and what state it is in, read the same way by the Users
// list, a user's page and a role's Holders tab.
import type { ConfigAnswer, LockRow, PreviewAnswer, UserRow } from "./api";
import type { Kind } from "./chip-words";
import { ENTRA, entraAccountURL } from "./entra-words";
import { kindWord } from "./user-words";

// UserKind is the kind whose icon and tile a user wears.
export type UserKind = Extract<Kind, "person" | "agent" | "service">;

// userKind reads a user's kind from the row, with the rule kindWord has.
export function userKind(u: Pick<UserRow, "kind" | "user_type">): UserKind {
  const word = kindWord(u);
  return word === "AI agent" ? "agent" : word;
}

// DISABLE_REASON is the reason the server writes itself when a user is
// disabled in Straza.
const DISABLE_REASON = "user disabled by admin";

// byDisable says whether a revocation row was written by a disable and not
// by a lock. The origin tells: only a deactivation by the identity manager
// writes scim, only a disable that Straza read in Entra writes entra, and
// only a disable in Straza writes admin-disable. An older server wrote
// admin for that disable, so there its own reason tells.
export function byDisable(l: LockRow): boolean {
  return l.origin === "scim" || l.origin === "entra" || l.origin === "admin-disable" || (l.origin === "admin" && l.reason === DISABLE_REASON);
}

// UserState is what the Status cell, the tile's overlay and the page's
// strips show of one user.
export type UserState = { disabled: boolean; locks: LockRow[] };

// userState reads a user's state. A disabled user is not drawn as locked for
// the row its own disable wrote. A disable in Straza holds against an
// enable from the identity manager, so its row keeps a user who reads
// active disabled until Enable here. On any other user every row counts as
// a lock, because each one refuses check-ins, a disable's row that outlived
// the disable on an older server included.
export function userState(u: Pick<UserRow, "status" | "locks">): UserState {
  const rows = u.locks || [];
  const disabled = u.status === "disabled" || rows.some((l) => l.origin === "admin-disable");
  return { disabled, locks: disabled ? rows.filter((l) => !byDisable(l)) : rows };
}

// refusal says why the gateway refuses every call of a user before any
// policy runs, or undefined when the calls reach policy. served is the
// cause the preview answer carries, if any. Every revocation row of an
// active user puts it on the denylist, and a disable outranks a
// revocation, as on the server.
export function refusal(u: Pick<UserRow, "status" | "locks">, served?: PreviewAnswer["refused"]): PreviewAnswer["refused"] {
  const s = userState(u);
  if (s.disabled || served === "disabled") return "disabled";
  return served || (s.locks.length > 0 ? "revoked" : undefined);
}

// tileStatus is the one overlay a kind tile takes. A lock outranks a
// disable, because the identity manager cannot lift it.
export function tileStatus(u: Pick<UserRow, "status" | "locks">): "locked" | "disabled" | undefined {
  const s = userState(u);
  return s.locks.length > 0 ? "locked" : s.disabled ? "disabled" : undefined;
}

// SOURCE is where a user comes from, by the user's origin: the icon's kind
// and the word. Every identity manager arrives over SCIM, so the console
// cannot say which one it is.
const SOURCE: Record<string, { kind: Kind; word: string }> = {
  local: { kind: "from-straza", word: "Straza" },
  scim: { kind: "from-idm", word: "Identity manager" },
  entra: { kind: "from-entra", word: ENTRA },
};

// sourceWord says where a user comes from, and sourceKind names its icon.
// A user with no origin was made in Straza. An origin the table does not
// know shows as the server sent it, with no icon.
export const sourceWord = (origin: string | undefined) => (SOURCE[origin || "local"] || { word: origin || "" }).word;
export const sourceKind = (origin: string | undefined): Kind | undefined => SOURCE[origin || "local"]?.kind;

// ENTRA_OWNS maps a kind of user onto the values of identity.entra.read
// that give Entra the users of that kind, onto the account's type in
// Entra's words and onto the field that holds its object ID. An agent's
// account is its agent identity, whose object ID is the Entra agent ID.
// Straza makes no service from Entra, so that kind has no row, as on the
// server.
const ENTRA_OWNS: Partial<Record<UserKind, { read: string[]; type: string; id: "external_id" | "entra_agent_id" }>> = {
  person: { read: ["people"], type: "user", id: "external_id" },
  agent: { read: ["agentUsers", "agentIdentities"], type: "agentIdentity", id: "entra_agent_id" },
};

type Owned = Pick<UserRow, "origin" | "kind" | "user_type" | "external_id" | "entra_agent_id">;

// entraOwns says whether Entra owns the user, so that Straza refuses a
// change of its status or names and leaves them to Entra. config is the
// read of the configuration, or undefined while it is out, failed or not
// allowed, in which case a user of origin entra counts as owned, since only
// a deployment that reads its kind from Entra writes that origin.
export function entraOwns(u: Owned, config: ConfigAnswer | undefined): boolean {
  const own = ENTRA_OWNS[userKind(u)];
  if (u.origin !== "entra" || !own) return false;
  return config === undefined || own.read.some((r) => !!config.entra?.read?.includes(r));
}

// entraAccount is the address of a user's account in the Entra admin
// centre, or "" for a user that is not from Entra or whose kind or object
// ID the console cannot place there.
export function entraAccount(u: Owned): string {
  const own = ENTRA_OWNS[userKind(u)];
  return u.origin === "entra" && own ? entraAccountURL(own.type, u[own.id] || "") : "";
}

// NOT_YET stands where a user has never checked in.
export const NOT_YET = "not yet";

// orderedRoles puts the control-plane role first, since a person who holds
// it is the first thing to read off a row.
export function orderedRoles(roles: string[] | undefined): string[] {
  return [...(roles || [])].sort((a, b) => (a === "straza-admin" ? -1 : b === "straza-admin" ? 1 : 0));
}
