// The words that a chip, a tag and a kind icon carry on every screen of the
// console and of the self-service page. One meaning has one word here, and
// a screen's own words file reads it from here and never spells it again.

// Outcome is what happens to a call.
export type Outcome = "allowed" | "approval" | "denied" | "none";

// OUTCOME_WORD is the word of an outcome wherever it stands as a value: in
// a chip, a cell, a line or beside a count. These four are the only words
// for an outcome.
export const OUTCOME_WORD: Record<Outcome, string> = {
  allowed: "allowed",
  approval: "needs approval",
  denied: "denied",
  none: "no access",
};

// OUTCOME_LABEL is the same word where it opens a column header, a button
// or an option of a filter, which start with a capital like every label.
export const OUTCOME_LABEL: Record<Outcome, string> = {
  allowed: "Allowed",
  approval: "Needs approval",
  denied: "Denied",
  none: "No access",
};

// outcomeCount is a count of calls, tools or policy rules with one outcome,
// as "3 allowed". The word does not change with the number.
export const outcomeCount = (n: number, o: Outcome) => n.toLocaleString("en-US") + " " + OUTCOME_WORD[o];

// Status is the state of a user, a session, a device or a key. An active
// thing shows a dot and the word with no box, so the others stand out.
export type Status = "active" | "disabled" | "locked" | "revoked" | "closed";

export const STATUS_WORD: Record<Status, string> = {
  active: "active",
  disabled: "disabled",
  locked: "locked",
  revoked: "revoked",
  closed: "closed",
};

// STATUS_LABEL is the same word where it opens an option of a filter or a
// column header, which start with a capital like every label.
export const STATUS_LABEL: Record<Status, string> = { active: "Active", disabled: "Disabled", locked: "Locked", revoked: "Revoked", closed: "Closed" };

// Change is what a draft does to an object, or an unpublished edit to a
// policy rule. on is a put of a policy that is off live, which turns it on.
export type Change = "new" | "changed" | "removed" | "off" | "on";

export const CHANGE_WORD: Record<Change, string> = {
  new: "new",
  changed: "changed",
  removed: "removed",
  off: "turned off",
  on: "turned on",
};

// Kind is what a thing is. Each kind has one icon, the same on every
// screen.
export type Kind =
  | "person" | "agent" | "service"
  | "role-application" | "role-business" | "role-approver" | "role-straza" | "role-server-admin"
  | "server" | "local-server" | "tool" | "policy-set" | "session"
  | "hold" | "ticket" | "phone" | "browser" | "channel" | "draft"
  | "api-token" | "credential" | "signing-key" | "record" | "transcript"
  | "from-idm" | "from-straza" | "from-entra";

// KIND_WORD names a kind where its icon stands alone, as the icon's
// accessible name and its hover text.
export const KIND_WORD: Record<Kind, string> = {
  person: "Person",
  agent: "AI agent",
  service: "Service",
  "role-application": "Application role",
  "role-business": "Business role",
  "role-approver": "Approver role",
  "role-straza": "Straza role",
  "role-server-admin": "Server admin role",
  server: "MCP server",
  "local-server": "Server on the person's machine",
  tool: "Tool",
  "policy-set": "Policy set",
  session: "Session",
  hold: "Hold",
  ticket: "Ticket",
  phone: "Approver phone",
  browser: "Approver browser",
  channel: "Notification channel",
  draft: "Draft",
  "api-token": "API token",
  credential: "Credential at a server",
  "signing-key": "Agent signing key",
  record: "Audit record",
  transcript: "Transcript",
  "from-idm": "From the identity manager",
  "from-straza": "Made in Straza",
  "from-entra": "From Entra",
};
