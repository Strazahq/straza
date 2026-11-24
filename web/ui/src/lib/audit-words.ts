// The Audit screen's readers and sentences.
// Every reader is a pure function over the wire row, so the screen and its
// tests share one reading of a record.
import type { AuditRow } from "./api";
import { OUTCOME_WORD, type Outcome } from "./chip-words";
import { MODE_WORD } from "./config-words";
import { ENTRA } from "./entra-words";
import { cutOf } from "./shell-read";

export const SENTINEL_TYPE = "straza.audit.sentinel";

// LENSES are the type lenses of the picker. A lens may cover several type
// prefixes; its note says what it shows, for the hint under the picker.
export const LENSES: { key: string; label: string; prefixes?: string[]; note?: string }[] = [
  { key: "all", label: "All records" },
  { key: "decisions", label: "Decisions", prefixes: ["straza.audit.tool", "straza.audit.mcp"], note: "What policy decided for each tool call and MCP call." },
  { key: "straza.audit.approval", label: "Approvals", prefixes: ["straza.audit.approval"], note: "What people decided on approval requests, with one record for the request, one for the answer and one when a ticket is used." },
  { key: "capture", label: "Recording", prefixes: ["straza.audit.prompt", "straza.audit.reply"], note: "One record per recorded prompt and reply, with its size and hash and never its text." },
  { key: SENTINEL_TYPE, label: "Sentinel alerts", prefixes: [SENTINEL_TYPE], note: "What the sentinel, Straza's audit watcher, flagged on a session. It only alerts and never revokes." },
  { key: "straza.audit", label: "Calls and admin changes", prefixes: ["straza.audit"], note: "Calls, approvals, recordings, sentinel alerts and admin changes." },
  { key: "straza.revocation", label: "Revocations", prefixes: ["straza.revocation"] },
  { key: "straza.identity", label: "Identity", prefixes: ["straza.identity"] },
  { key: "straza.apps", label: "MCP servers", prefixes: ["straza.apps"], note: "Records of a server being published, changed, started and stopped." },
  { key: "straza.policy", label: "Policy", prefixes: ["straza.policy"] },
];

// The picker's name, the search field's placeholder, and the Outcome
// column with its filter.
export const LENS_LABEL = "Kind of record";
export const SEARCH_PLACEHOLDER = "Search the whole chain: command, path, user, policy rule";
export const OUTCOME_COLUMN = "Outcome";
export const ANY_OUTCOME = "Any outcome";

// sentinelWhat is the Action cell of a sentinel alert after its detector:
// how many records the alert holds as evidence.
export const sentinelWhat = (n: number) => "sentinel alert · " + (n === 1 ? "1 record of evidence" : n + " records of evidence");

// TYPE_WORD says a record's type in words, by the short type the table
// already cuts from the event type. A type with no word shows as it is.
const TYPE_WORD: Record<string, string> = {
  tool: "tool call", mcp: "MCP call", approval: "approval", prompt: "prompt", reply: "reply", sentinel: "sentinel alert",
  admin: "admin change", identity: "identity change", policy: "policy change", authn: "sign-in",
};
const TYPE_FAMILY: Record<string, string> = { revocation: "revocation", apps: "MCP server", session: "session", policy: "policy change" };
export const typeWord = (short: string) => TYPE_WORD[short] || TYPE_FAMILY[short.split(".")[0]] || short;

export function typeMatches(type: string, lensKey: string): boolean {
  const lens = LENSES.find((l) => l.key === lensKey);
  if (!lens || !lens.prefixes) return true;
  return lens.prefixes.some((p) => type.startsWith(p));
}

export const SEV_RANK: Record<string, number> = { info: 1, warning: 2, critical: 3 };
export const sevTone = (s: string): "ok" | "warn" | "danger" | "unknown" => (s === "critical" ? "danger" : s === "warning" ? "warn" : s === "info" ? "ok" : "unknown");

type CE = { type?: string; time?: string; data?: Record<string, unknown> };

export function parseCE(raw: string): CE | null {
  try { return JSON.parse(raw) as CE; } catch { return null; }
}

// shortType strips the straza. and audit. prefixes: tool, mcp, prompt,
// admin, identity, revocation.session, apps.deployed.
export function shortType(t: string | undefined): string {
  return String(t || "?").replace(/^straza\./, "").replace(/^audit\./, "");
}

// id8 shortens an id to the length an operator reads; a value that is
// already short, such as a username a record carries in the user field,
// stays whole.
const id8 = (v: unknown) => { const s = String(v); return s.length > 8 ? s.slice(0, 8) + "…" : s; };

// ObjectFact is the object a gateway record names (spec/events revision
// 44): the server's type, the value the call named, the list id, empty when
// the list did not hold the value, and the SHA-256 of a value the record
// cut at 512 bytes, empty when it was kept whole.
export type ObjectFact = { type: string; value: string; id: string; sha256: string };

// objectOf reads the object a gateway record names, and answers null for a
// record whose call named no value, such as a call whose tool reaches its
// objects through the credential, or a record written before the field.
export function objectOf(ce: CE | null): ObjectFact | null {
  const o = ce && ce.data && ce.data.object;
  if (!o || typeof o !== "object" || Array.isArray(o)) return null;
  const f = o as Record<string, unknown>;
  if (!f.value) return null;
  return { type: String(f.type || ""), value: String(f.value), id: String(f.id || ""), sha256: f.valueTruncated ? String(f.valueSha256 || "") : "" };
}

// objectLabel names the record sheet's object row by the server's type,
// such as Repo.
export const objectLabel = (o: ObjectFact) => o.type.charAt(0).toUpperCase() + o.type.slice(1);

// objectNote is the muted part of that row: the list id, or that the list
// did not hold the value, and the hash of a value the record cut.
export const objectNote = (o: ObjectFact) =>
  [o.id ? "id " + o.id : "not in the " + o.type + " list", o.sha256 ? "cut at 512 bytes, SHA-256 " + o.sha256.slice(0, 12) + "…" : ""].filter(Boolean).join(", ");

// LIST_READ names a list read by the event its record carries.
const LIST_READ: Record<string, string> = { "tools.list": "tool list read", "resources.list": "resource list read" };

// ceWhat is the exact thing that happened (the harness's own name of a
// classified tool, command, target paths, upstream tool, the object it
// named and its arguments), never displaced by the reason. The arguments
// stay in the sheet when a command or a path already says what the call
// did. A capture record reads as a reference row, size, mode and hash,
// never the text, even an old record that still carries data.content on the
// chain. A gateway record that served a tool or resource list has no action
// of its own and reads as that read.
export function ceWhat(ce: CE | null): string {
  if (!ce) return "(unreadable record)";
  const d = ce.data || {};
  const bits: string[] = [];
  if (d.nativeTool) bits.push(String(d.nativeTool));
  if (d.command) bits.push(String(d.command));
  const paths = Array.isArray(d.paths) ? d.paths.map(String).join(" ") : "";
  if (paths) bits.push(paths);
  const obj = objectOf(ce);
  if (d.toolName) bits.push((d.app ? String(d.app) + "." : "") + String(d.toolName) + (obj ? " on " + obj.value : ""));
  if (d.arguments && !d.command && !paths) bits.push(String(d.arguments).slice(0, 160));
  if (d.contentHash || d.content) {
    const bytes = d.contentBytes != null ? Number(d.contentBytes) : d.content != null ? String(d.content).length : null;
    const hash = d.contentHash ? String(d.contentHash).replace(/^sha256:/, "").slice(0, 12) + "…" : "unhashed";
    const mode = String(d.mode || "captured");
    bits.push("content " + (bytes != null ? bytes + " B · " : "") + (MODE_WORD[mode] || mode) + " · " + hash);
  }
  if (d.action) bits.push(String(d.action));
  return bits.filter(Boolean).join(" · ") || LIST_READ[String(d.event || "")] || "";
}

// approvalWhat is the sentence an auditor needs of an approval record:
// requester, action, verdict, decider and rule, from the data the record
// carries; the decider's name is the server's read-time enrichment.
export function approvalWhat(d: Record<string, unknown>, r: AuditRow): string {
  const who = r.username || (d.user ? id8(d.user) : "");
  const decider = r.decidedByUsername || (d.decidedBy ? id8(d.decidedBy) : "");
  let subject = String(d.summary || "");
  const m = /^mcp\.call ([^:\s]+):(\S+)$/.exec(subject);
  if (m) subject = m[2].replace(/_/g, " ") + " in " + m[1];
  let s = [who ? who + "'s" : "", subject].filter(Boolean).join(" ");
  if (d.phase === "request") s += " needs approval";
  else if (d.phase === "consumed") s += " · ticket used by session " + id8(d.consumedBy || "");
  else if (d.state === "approved") s += " approved by " + decider;
  else if (d.state === "denied") s += " denied by " + decider;
  else if (d.state === "expired") s += " expired, nobody decided in time";
  if (d.rule) s += " (policy rule " + String(d.rule) + ")";
  return s;
}

// Row is one table row: the wire record read once.
export type Row = {
  seq: number;
  hash: string;
  raw: string;
  username: string;
  user: string;
  // about is the server's name for the user an admin record was about, ""
  // when the server sent none.
  about: string;
  type: string;
  time: string;
  effect: string;
  tool: string;
  // command is a shell record's command as stored, so the list can lay it
  // out on one line; "" for any other record.
  command: string;
  // commandCut says the record keeps only part of the command.
  commandCut: boolean;
  what: string;
  reason: string;
  session: string;
  sentinel: boolean;
  approval: boolean;
  severity: string;
  detector: string;
  evidence: number;
  foldN?: number;
  foldOpen?: boolean;
  foldEnd?: number;
};

// ceRow maps one wire record to the row shape, shared by the live tail
// and the server search so both render identically.
export function ceRow(r: AuditRow): Row {
  const ce = parseCE(r.ce);
  const d = (ce && ce.data) || {};
  const sentinel = !!ce && ce.type === SENTINEL_TYPE;
  const approval = !!ce && ce.type === "straza.audit.approval";
  return {
    seq: r.seq,
    hash: r.hash || "",
    raw: r.ce,
    username: r.username || "",
    user: r.username || (d.user ? id8(d.user) : ""),
    about: r.about || "",
    type: ce && ce.type ? ce.type : "?",
    time: (ce && ce.time) || "",
    effect: String(approval ? d.state || "" : d.effect || ""),
    tool: String(d.tool || ""),
    command: !approval && typeof d.command === "string" ? d.command : "",
    commandCut: d.commandTruncated === true || (typeof d.command === "string" && cutOf(d.command) !== null),
    what: approval ? approvalWhat(d, r) : ceWhat(ce),
    reason: String(approval ? d.decidedReason || "" : d.reason || (d.effect && d.ruleId ? "policy rule " + String(d.ruleId) : "")),
    session: String(d.session || ""),
    sentinel,
    approval,
    severity: sentinel ? (SEV_RANK[String(d.severity)] ? String(d.severity) : "info") : "",
    detector: sentinel ? String(d.detector || "?") : "",
    evidence: sentinel && Array.isArray(d.evidence) ? d.evidence.length : 0,
  };
}

// foldKey is what two adjacent rows must share to fold: type, what, user,
// effect and reason, and for a record that is not a call its data too, so
// two changes to different fields of one user stay two rows. A call keeps
// the shorter key, so repeated reads still fold.
const foldKey = (r: Row) =>
  JSON.stringify([r.type, r.what, r.user, r.effect, r.reason, CALL_TYPES.includes(r.type) ? null : parseCE(r.raw)?.data ?? null]);

// foldAdjacent collapses runs of rows with the same fold key into their
// newest row carrying a count and an expand toggle. Display only: exports
// and the chain check read the unfolded records, and sentinel verdicts
// never fold. open is keyed by the run's newest seq.
export function foldAdjacent(rows: Row[], open: Record<number, boolean>): Row[] {
  const out: Row[] = [];
  const keys = rows.map(foldKey);
  for (let i = 0; i < rows.length; ) {
    const r = rows[i];
    let j = i + 1;
    while (j < rows.length && !r.sentinel && !rows[j].sentinel && keys[j] === keys[i]) j++;
    if (j - i > 1) {
      const isOpen = !!open[r.seq];
      out.push({ ...r, foldN: j - i, foldOpen: isOpen, foldEnd: rows[j - 1].seq });
      if (isOpen) for (let k = i + 1; k < j; k++) out.push(rows[k]);
    } else {
      out.push(r);
    }
    i = j;
  }
  return out;
}

// effectWords is the Outcome cell: the decision's word, an approval's
// state, or a sentinel's severity.
export function effectTone(effect: string): "ok" | "warn" | "danger" | "plain" {
  if (effect === "deny" || effect === "denied") return "danger";
  if (effect === "allow" || effect === "approved") return "ok";
  if (effect === "expired" || effect === "approve" || effect === "confirm" || effect === "pending") return "warn";
  return "plain";
}
// effectOutcome reads a decision's effect as an outcome, and is null for
// an approval's state or a record that decided nothing.
export const effectOutcome = (effect: string): Outcome | null =>
  effect === "allow" ? "allowed" : effect === "deny" ? "denied" : effect === "approve" || effect === "confirm" ? "approval" : null;
// APPROVAL_STATE_WORD says an approval record's state where the Outcome
// column shows it.
const APPROVAL_STATE_WORD: Record<string, string> = { pending: "waiting", approved: "approved", denied: "denied", expired: "expired" };
export const effectWord = (effect: string) => {
  const o = effectOutcome(effect);
  return o ? OUTCOME_WORD[o] : APPROVAL_STATE_WORD[effect] || effect || "none";
};

// Why is the explanation of a decision record (spec/events rev 20): the
// outcome, the decided-by sentence, and for a call the gateway held the
// context, all from what the record stored at decision time. hasSetName
// tells an old record (no setName key) from a real no-match
// (setName "" with ruleId "").
export type Why = { outcome: string; tone: "ok" | "warn" | "danger"; basis: string; context: string; wire: string };

// The parts of the sentence that names the policy rule and the policy that
// decided a call, shared with the card of the two what-if sheets.
export const DECIDED_BY = "Decided by policy rule ";
export const IN_POLICY = " in policy ";
// The sentences of a call no policy rule decided, on that card.
export const DENIED_DEFAULT_ENTERPRISE = "Denied by default. On the enterprise profile a call that nothing allows is denied.";
export const DENIED_DEFAULT = "Denied by default. No policy allowed this call.";
export const ALLOWED_BY_ACCESS = "Allowed by role access. No policy rule decides this tool.";

export function whyOf(ce: CE | null): Why | null {
  if (!ce || !/^straza\.audit\.(tool|mcp)$/.test(ce.type || "")) return null;
  const d = ce.data || {};
  const effect = String(d.effect || "");
  if (!effect) return null;
  const ruleId = String(d.ruleId || "");
  const setName = String(d.setName || "");
  const hasSetName = Object.prototype.hasOwnProperty.call(d, "setName");
  const reason = String(d.reason || "");
  const held = effect === "approve" || effect === "confirm";
  const outcome = effect === "deny" ? "Denied" : held ? "Needs approval" : effect === "allow" ? "Allowed" : effect;
  const tone: Why["tone"] = effect === "deny" ? "danger" : held ? "warn" : "ok";
  let basis: string;
  // An absent rule cannot establish a denial or a profile default.
  if (ruleId === "") basis = "No policy rule was recorded for this call." + (reason ? " " + reason : "");
  else if (!hasSetName) basis = DECIDED_BY + ruleId + ". The policy's name was not recorded on this record." + (reason ? " " + reason : "");
  else basis = DECIDED_BY + ruleId + IN_POLICY + setName + (reason ? ": " + reason : ".");
  const context = ce.type === "straza.audit.mcp" && held ? "The gateway records the policy's outcome before a person answers. See Approvals for what the person decided." : "";
  const wire = ["effect=" + effect, "ruleId=" + ruleId, hasSetName ? "setName=" + setName : "", d.snapshot ? "snapshot=" + String(d.snapshot) : ""].filter(Boolean).join(" · ");
  return { outcome, tone, basis, context, wire };
}

// The chain status: one word in its tone, a short count beside it, and
// the sentence that explains it on hover, because the full sentence on the
// page is noise.
export const TAIL_WINDOW = 200;
export const TAIL_KEEP = 2000;
export const CHAIN_WORD = "Chain verified";
export const CHAIN_TIP = "Every loaded record was re-hashed in this browser and linked to its predecessor.";
export const chainVerified = (head: number) => "Newest record " + head.toLocaleString("en-US");
export const chainBroken = (seq: number) => "The audit chain is broken at record " + seq + ": its hash does not match. Records may have been altered or removed. Verify with strazactl audit verify.";
export const UNVERIFIED_WORD = "Chain not verified";
// The status before the first window lands, and after it failed to.
export const CHECKING_WORD = "Checking audit chain";
export const CHECKING_TEXT = "Loading and verifying the latest records.";
export const UNREAD_WORD = "Audit unavailable";
export const UNREAD_TEXT = "Nothing verified yet.";
export const EMPTY_UNREAD = "No audit records available.";
export const EMPTY_LOADING = "Loading audit records…";
export const CHAIN_UNVERIFIED = "Needs HTTPS or localhost.";
export const SEARCH_WORD = "Whole-chain search";
export const SEARCH_TIP = "The browser does not re-hash search results.";
// searchLine says what a search asks for. effect is the decision the
// server filters on, allow or deny, said as its outcome.
export const searchLine = (q: string, effect: string, user: string, capped: boolean) => {
  const parts = [q ? "for “" + q + "”" : "", effect ? "outcome " + effectWord(effect) : "", user ? "by " + user : ""].filter(Boolean);
  return (parts.length ? parts.join(", ") : "every record") + (capped ? ", newest " + TAIL_WINDOW + " shown" : "");
};
// policyQuery is the whole-chain search for the decisions one policy made.
// A decision record names its policy as setName and the server matches the
// raw record text, so the key and both quotes keep out a policy whose name
// only contains this one.
export const policyQuery = (name: string) => '"setName":"' + name + '"';
export const loadedWords = (n: number, total: number) => n.toLocaleString("en-US") + " of " + total.toLocaleString("en-US") + " records loaded";
// shownWords and matchWords count the rows the filter row has on screen,
// which the lens and the fold both move. loadedWords counts the window.
export const shownWords = (n: number) => n.toLocaleString("en-US") + (n === 1 ? " row shown" : " rows shown");
export const matchWords = (n: number) => n.toLocaleString("en-US") + (n === 1 ? " match" : " matches");
export const EMPTY_CHAIN = "No records yet.";
export const EMPTY_LENS = "No loaded record of this kind. Search reads the whole chain.";
export const EMPTY_SEARCH = "No record in the chain matches this search.";
export const recordMissing = (seq: number) => "Record " + seq + " is not in the audit chain. Search Audit for the call.";
export const revokeBody = (session: string) =>
  "Every agent action in session " + session.slice(0, 13) + "… is denied within seconds. The device can start a new session. To keep it out, lock the user or revoke the device.";

// The record sheet's sentences. SENTINEL says on the sheet that revoking
// is the reader's call, because the strip is read without opening the
// confirm.
export const SENTINEL = "Alert only. The sentinel never revokes.";
export const REHASHED = "Hash matches the loaded chain.";
export const NOT_REHASHED = "Not verified in this browser.";
export const COPIED = "Copied.";
export const COPY_REFUSED = "Nothing was copied: the browser refused the clipboard. Select the text and copy it by hand.";
export const revokedWords = (session: string) => "Session " + session.slice(0, 13) + "… is revoked. Every agent action in it is denied within seconds.";

// CSV and JSONL of the loaded rows.
export function csvCell(v: unknown): string {
  const s = String(v == null ? "" : v);
  return /[",\n]/.test(s) ? "\"" + s.replace(/"/g, "\"\"") + "\"" : s;
}
export function csvOf(rows: Row[]): string {
  const head = "seq,time,user,session,type,effect,what,reason";
  return head + "\n" + rows.map((r) => [r.seq, r.time, r.username, r.session, r.type, r.effect, r.what, r.reason].map(csvCell).join(",")).join("\n") + "\n";
}
export const jsonlOf = (rows: Row[]) => rows.map((r) => r.raw).join("\n") + "\n";

// ACTION_LABEL reads each action an admin, identity or policy record
// carries as a person says it. A record with no action falls back to its
// short type, so the three lifecycle types and the snapshot type read here
// too.
const ACTION_LABEL: Record<string, string> = {
  "apps.install": "Registered an MCP server", "apps.remove": "Removed an MCP server", "apps.recheck": "Rechecked an MCP server",
  "apps.disable": "Paused an MCP server", "apps.enable": "Enabled an MCP server",
  "apps.binding.create": "Gave a role access to an MCP server", "apps.binding.delete": "Removed an access row",
  "apps.secret.set": "Set an MCP server's secret", "apps.secret.remove": "Removed an MCP server's secret",
  "apps.grant.remove": "Removed a user's connection to an MCP server",
  "api-token.create": "Created an admin API token", "api-token.revoke": "Revoked an admin API token",
  "roles.assign": "Assigned a role", "roles.unassign": "Removed a role assignment", "roles.create": "Created a role",
  "roles.delete": "Deleted a role", "roles.update": "Changed a role",
  "roles.implication.create": "Added a role to a business role", "roles.implication.delete": "Removed a role from a business role",
  "policy.create": "Created a policy", "policy.update": "Changed a policy", "policy.delete": "Deleted a policy",
  "policy.activate": "Turned on a policy", "policy.deactivate": "Turned off a policy",
  "policy.updated": "Made a new policy version live",
  "signing-keys.create": "Created a signing key", "signing-keys.rotate": "Rotated a signing key",
  "signing-keys.promote": "Promoted a signing key", "signing-keys.retire": "Retired a signing key",
  "draft.create": "Created a draft", "draft.update": "Changed a draft", "draft.check": "Checked a draft",
  "draft.contact": "Listed the tools of a drafted MCP server", "draft.publish": "Published a draft",
  "draft.discard": "Discarded a draft", "draft.expire": "A draft expired",
  "sessions.bulk-revoke": "Revoked sessions", "session.start": "Started a session",
  "user.create": "Created a user", "user.update": "Changed a user",
  "user.lock": "Locked a user", "user.unlock": "Unlocked a user", "user.killed": "Cut off a user's access",
  "user.reactivated": "Reactivated a user", "user.lift.blocked": "Kept a user locked, since another lock remains",
  "identity.created": "Created a user", "identity.updated": "Changed a user", "identity.deactivated": "Deactivated a user",
  "nhi-key.set": "Registered an AI agent's key", "nhi-key.removed": "Removed an AI agent's key",
  "sink.replay": "Replayed an audit sink's undelivered events",
  "oauth.connect": "Connected a user to an MCP server", "oauth.connect.refused": "Refused a connection to an MCP server",
  "oauth.disconnect": "Disconnected a user from an MCP server", "token.connect": "Stored a user's token for an MCP server",
  "connection.agents": "Changed which agents use a connection",
  "attestation-hash.registered": "Registered a hook configuration hash", "attestation-hash.removed": "Removed a hook configuration hash",
  enroll: "Enrolled a machine", renew: "Renewed a machine's credential", "breakglass.login": "Signed in with emergency access",
  "approver-enroll": "Enrolled an approver phone or browser", "approver-self-enroll-token": "Started enrolling an approver phone or browser",
  "approver-device-revoked": "Revoked an approver phone or browser",
  "enroll-token.create": "Started enrolling an approver phone or browser", "approver.enroll": "Enrolled an approver phone or browser",
  "enroll-token.refused": "Refused enrolling an approver phone or browser for someone",
  "scim.refused": "Refused an identity manager's SCIM write",
  "admin.refused": "Refused an admin change",
  "approver.revoke": "Revoked an approver phone or browser with its user",
  "revocation.session": "Revoked a session", "revocation.sessions": "Revoked sessions", "revocation.user": "Revoked a user's access",
  "revocation.device": "Revoked a machine", "revocation.lift": "Lifted a user's lock",
  "device.revoke": "Revoked a user's machine",
  "entra.group.create": "Created a role's group in Entra", "entra.group.update": "Changed a role's group in Entra",
  "entra.group.delete": "Deleted a role's group in Entra", "entra.group.missing": "Found a role's group deleted in Entra",
  "entra.group.recreate": "Created a role's group in Entra again", "entra.read.stale": "Could not read Entra",
  "entra.read.recovered": "Read Entra again", "entra.pass.held": "Held the removals of a role's group in Entra",
  "entra.pass.released": "Applied the held removals of a role's group in Entra",
  "entra.sponsor.unresolved": "Left an agent from Entra with no sponsor", "entra.sponsor.resolved": "Set the sponsor of an agent from Entra",
};

// ACTOR_BY_ORIGIN names who acted on a record that carries no actor, by
// the record's origin: Straza changed a person because Entra said so.
export const ACTOR_BY_ORIGIN: Record<string, string> = { entra: ENTRA };

// ACTION_NOUN names the object of each action namespace, for an action
// this console does not know yet.
const ACTION_NOUN: Record<string, string> = {
  apps: "MCP server", "api-token": "Admin API token", roles: "Role", policy: "Policy", "signing-keys": "Signing key",
  draft: "Draft", sessions: "Sessions", session: "Session", user: "User", identity: "User", "nhi-key": "AI agent key", sink: "Audit sink",
  oauth: "Connection", token: "Token", connection: "Connection", "attestation-hash": "Hook configuration hash", breakglass: "Emergency access",
  revocation: "Revocation", entra: "Entra",
};

const spaced = (s: string) => s.replace(/[-_]/g, " ");

// actionLabel reads an admin, identity or policy record's action in words:
// the label of a known one, and the object and the verb of an unknown
// dotted one, so no dotted action reaches the page raw. Free text, such as
// a sentence a test writes, stays as recorded.
export function actionLabel(value: string): string {
  if (ACTION_LABEL[value]) return ACTION_LABEL[value];
  const [head, ...rest] = value.split(".");
  if (!rest.length || !/^[a-z0-9._-]+$/.test(value)) return value;
  const noun = ACTION_NOUN[head] || spaced(head).charAt(0).toUpperCase() + spaced(head).slice(1);
  return noun + ": " + spaced(rest.join(" "));
}

// CALL_TYPES are the records of a call, a turn or a verdict, whose what is
// the command or the tool call itself, never an action.
const CALL_TYPES = ["straza.audit.tool", "straza.audit.mcp", "straza.audit.prompt", "straza.audit.reply", "straza.audit.approval", "straza.audit.sentinel"];

// rowLabel is the line a row reads as on Overview: a call exactly as
// recorded, so a server named policy or draft never reads as an admin
// action, and any other record's action in words.
export const rowLabel = (r: Row) => (CALL_TYPES.includes(r.type) ? r.what || shortType(r.type) : actionLabel(r.what || shortType(r.type)));

// RECORD_FACTS are the fields the record sheet shows of a record that
// decided no call, in order, each with its label.
const RECORD_FACTS: [string, string][] = [["changed", "Changed"], ["role", "Role"], ["group", "Group"], ["name", "Name"], ["description", "Description"], ["origin", "Origin"], ["target", "Target"], ["route", "Route"]];

// recordFacts reads those fields as labels with their values: a list as
// its items, a changed field's name spaced, and an origin as who it names.
// An admin record leads with the user it was about, because its Who is the
// actor: the name the server resolved, else the record's username, else its
// user id. A field the record does not carry, or carries empty, is left out.
export function recordFacts(ce: CE | null, resolved = ""): [string, string[]][] {
  const d = (ce && ce.data) || {};
  const about = ce?.type === "straza.audit.admin" ? [resolved, d.username, d.user].find((v) => typeof v === "string" && v) : undefined;
  const facts = RECORD_FACTS.flatMap(([key, label]): [string, string[]][] => {
    const v = d[key];
    const values = (Array.isArray(v) ? v : [v]).filter((x) => typeof x === "string" || typeof x === "number").map(String).filter(Boolean);
    const shown = key === "changed" ? values.map(spaced) : key === "origin" ? values.map((o) => ACTOR_BY_ORIGIN[o] || o) : values;
    return shown.length ? [[label, shown]] : [];
  });
  return about ? [["User", [String(about)]], ...facts] : facts;
}
