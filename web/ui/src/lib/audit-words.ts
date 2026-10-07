// The Audit screen's readers and sentences.
// Every reader is a pure function over the wire row, so the screen and its
// tests share one reading of a record.
import type { AuditRow } from "./api";

export const SENTINEL_TYPE = "straza.audit.sentinel";

// LENSES are the type lenses of the picker. A lens may cover several type
// prefixes; its note says which, for the hint under the picker.
export const LENSES: { key: string; label: string; prefixes?: string[]; note?: string }[] = [
  { key: "all", label: "all records" },
  { key: "decisions", label: "decisions", prefixes: ["straza.audit.tool", "straza.audit.mcp"], note: "decisions = type tool and mcp: the policy engine's verdicts on tool calls" },
  { key: "straza.audit.approval", label: "approvals", prefixes: ["straza.audit.approval"], note: "approvals = type approval: a person's decisions on approval requests, one record per phase (request, resolution, consumed)" },
  { key: "capture", label: "recording", prefixes: ["straza.audit.prompt", "straza.audit.reply"], note: "recording = type prompt and reply: conversation witnesses, size and hash only, never the text" },
  { key: SENTINEL_TYPE, label: "sentinel", prefixes: [SENTINEL_TYPE], note: "sentinel = type sentinel: the audit watcher's verdicts on sessions. Alert-only: it flags, it never revokes" },
  { key: "straza.audit", label: "audit", prefixes: ["straza.audit"], note: "audit = every straza.audit.* record: tool, mcp, approval, prompt, reply, sentinel, admin" },
  { key: "straza.revocation", label: "revocations", prefixes: ["straza.revocation"] },
  { key: "straza.identity", label: "identity", prefixes: ["straza.identity"] },
  { key: "straza.apps", label: "MCP servers", prefixes: ["straza.apps"], note: "MCP servers = type apps.*: server lifecycle records (the wire keeps the apps name)" },
  { key: "straza.policy", label: "policy", prefixes: ["straza.policy"] },
];

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

// ceWhat is the exact thing that happened (command, target paths, upstream
// tool and arguments), never displaced by the reason. A capture record
// reads as a reference row, size, mode and hash, never the text, even an
// old record that still carries data.content on the chain.
export function ceWhat(ce: CE | null): string {
  if (!ce) return "(unreadable record)";
  const d = ce.data || {};
  const bits: string[] = [];
  if (d.command) bits.push(String(d.command));
  if (Array.isArray(d.paths) && d.paths.length) bits.push(d.paths.map(String).join(" "));
  if (d.toolName) bits.push((d.app ? String(d.app) + "." : "") + String(d.toolName));
  if (d.arguments) bits.push(String(d.arguments).slice(0, 160));
  if (d.contentHash || d.content) {
    const bytes = d.contentBytes != null ? Number(d.contentBytes) : d.content != null ? String(d.content).length : null;
    const hash = d.contentHash ? String(d.contentHash).replace(/^sha256:/, "").slice(0, 12) + "…" : "unhashed";
    bits.push("content " + (bytes != null ? bytes + " B · " : "") + String(d.mode || "captured") + " · " + hash);
  }
  if (d.action) bits.push(String(d.action));
  return bits.filter(Boolean).join(" · ");
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
  else if (d.phase === "consumed") s += " · ticket consumed by session " + id8(d.consumedBy || "");
  else if (d.state === "approved") s += " approved by " + decider;
  else if (d.state === "denied") s += " denied by " + decider;
  else if (d.state === "expired") s += " expired, nobody decided in time";
  if (d.rule) s += " (rule " + String(d.rule) + ")";
  return s;
}

// Row is one table row: the wire record read once.
export type Row = {
  seq: number;
  hash: string;
  raw: string;
  username: string;
  user: string;
  type: string;
  time: string;
  effect: string;
  tool: string;
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
    type: ce && ce.type ? ce.type : "?",
    time: (ce && ce.time) || "",
    effect: String(approval ? d.state || "" : d.effect || ""),
    tool: String(d.tool || ""),
    what: approval ? approvalWhat(d, r) : ceWhat(ce),
    reason: String(approval ? d.decidedReason || "" : d.reason || (d.effect && d.ruleId ? "rule " + String(d.ruleId) : "")),
    session: String(d.session || ""),
    sentinel,
    approval,
    severity: sentinel ? (SEV_RANK[String(d.severity)] ? String(d.severity) : "info") : "",
    detector: sentinel ? String(d.detector || "?") : "",
    evidence: sentinel && Array.isArray(d.evidence) ? d.evidence.length : 0,
  };
}

// foldAdjacent collapses runs of rows with the same type, what, user and
// effect into their newest row carrying a count and an expand toggle.
// Display only: exports and the chain check read the unfolded records, and
// sentinel verdicts never fold. open is keyed by the run's newest seq.
export function foldAdjacent(rows: Row[], open: Record<number, boolean>): Row[] {
  const out: Row[] = [];
  for (let i = 0; i < rows.length; ) {
    const r = rows[i];
    let j = i + 1;
    while (j < rows.length && !r.sentinel && !rows[j].sentinel && rows[j].type === r.type && rows[j].what === r.what && rows[j].user === r.user && rows[j].effect === r.effect) j++;
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

// effectWords is the Effect cell: the decision's word, an approval's
// state, or a sentinel's severity.
export function effectTone(effect: string): "ok" | "warn" | "danger" | "plain" {
  if (effect === "deny" || effect === "denied") return "danger";
  if (effect === "allow" || effect === "approved") return "ok";
  if (effect === "expired" || effect === "approve" || effect === "confirm" || effect === "pending") return "warn";
  return "plain";
}
export const effectWord = (effect: string) => (effect === "approve" || effect === "confirm" ? "needs approval" : effect || "none");

// Why is the three-line explanation of a decision record (spec/events rev
// 20): the outcome, the decided-by sentence, and the context, all from what the
// record stored at decision time. hasSetName tells an old record (no
// setName key) from a real no-match (setName "" with ruleId "").
export type Why = { outcome: string; tone: "ok" | "warn" | "danger"; basis: string; context: string; wire: string };

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
  const outcome = effect === "deny" ? "Denied." : held ? "Needs approval." : effect === "allow" ? "Allowed." : effect + ".";
  const tone: Why["tone"] = effect === "deny" ? "danger" : held ? "warn" : "ok";
  let basis: string;
  // An absent rule cannot establish a denial or a profile default.
  if (ruleId === "") basis = "No policy rule was recorded for this call." + (reason ? " " + reason : "");
  else if (!hasSetName) basis = "Decided by rule " + ruleId + ". The policy name was not recorded on this record." + (reason ? " " + reason : "");
  else basis = "Decided by rule " + ruleId + " in policy " + setName + (reason ? ": " + reason : ".");
  const context = "Recorded at decision time." + (ce.type === "straza.audit.mcp" && held ? " The gateway records the policy verdict before a person's decision resolves; see Approvals for the outcome." : "");
  const wire = ["effect=" + effect, "ruleId=" + ruleId, hasSetName ? "setName=" + setName : "", d.snapshot ? "snapshot=" + String(d.snapshot) : ""].filter(Boolean).join(" · ");
  return { outcome, tone, basis, context, wire };
}

// The chain status: one word in its tone, a short count beside it, and
// the sentence that explains it on hover, because the full sentence on the
// page is noise.
export const TAIL_WINDOW = 200;
export const TAIL_KEEP = 2000;
export const CHAIN_WORD = "Chain verified";
export const CHAIN_TIP = "Every loaded record was re-hashed in this browser and linked to its predecessor. Live tail, polled every second.";
export const chainVerified = (n: number, head: number) => n.toLocaleString("en-US") + (n === 1 ? " record" : " records") + " loaded, head at seq " + head;
export const chainBroken = (seq: number) => "Audit chain broken at seq " + seq + ": a record fails re-hashing. Records may have been altered or removed. Verify with strazactl audit verify.";
export const UNVERIFIED_WORD = "Chain not verified";
// The status before the first window lands, and after it failed to.
export const CHECKING_WORD = "Checking audit chain";
export const CHECKING_TEXT = "Loading and verifying the latest records.";
export const CHECKING_TIP = "Verification is reported only after the records have loaded and their hashes have been checked.";
export const UNREAD_WORD = "Audit unavailable";
export const UNREAD_TEXT = "The newest records did not load, so nothing is verified yet. The notice above says why, and Reload the chain reads them again.";
export const EMPTY_UNREAD = "No audit records available.";
export const EMPTY_LOADING = "Loading audit records…";
export const CHAIN_UNVERIFIED = "re-hashing needs a secure context (TLS or localhost)";
export const SEARCH_WORD = "Whole-chain search";
export const SEARCH_TIP = "Search results are not contiguous, so they are not re-hashed here; the tail underneath stays verified.";
export const searchLine = (q: string, effect: string, user: string, capped: boolean) => {
  const parts = [q ? "for “" + q + "”" : "", effect ? "effect " + effect : "", user ? "by " + user : ""].filter(Boolean);
  return (parts.length ? parts.join(", ") : "every record") + (capped ? ", newest " + TAIL_WINDOW + " shown" : "");
};
export const loadedWords = (n: number, total: number) => n.toLocaleString("en-US") + " of " + total.toLocaleString("en-US") + " records loaded";
// shownWords and matchWords count the rows the filter row has on screen,
// which the lens and the fold both move. loadedWords counts the window.
export const shownWords = (n: number) => n.toLocaleString("en-US") + (n === 1 ? " row shown" : " rows shown");
export const matchWords = (n: number) => n.toLocaleString("en-US") + (n === 1 ? " match" : " matches");
export const EMPTY_CHAIN = "Nothing has been decided or changed yet. The first check-in or admin action writes the first record.";
export const EMPTY_LENS = "Nothing in the loaded tail under this lens. The search box, the effect and the user filter search the whole chain.";
export const EMPTY_SEARCH = "No record in the chain matches this search.";
export const revokeBody = (session: string, detector: string) =>
  "The sentinel is alert-only: it never revokes on its own, and revoking is your call. Confirming denies every agent action in session " + session.slice(0, 13) + "…" +
  (detector ? " (flagged by " + detector + ")" : "") + " within seconds. A session revoke is a stand-down, not a ban: the device can check in again as a new session, and a hard cut-off is a device revoke or a user lock.";

// The record sheet's sentences. SENTINEL says on the sheet
// what revokeBody says in the confirm, because the strip is read without
// opening the confirm.
export const SENTINEL = "The sentinel is alert-only: it never revokes on its own, and revoking is your call.";
export const REHASHED = "Hash matches the loaded chain.";
export const NOT_REHASHED = "Not verified in this browser. See the audit chain status for details.";
export const COPIED = "Copied.";
export const COPY_REFUSED = "The record was not copied because the browser refused the clipboard. Select the record text above and copy it by hand.";
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
  "roles.implication.create": "Added a role to a composition", "roles.implication.delete": "Removed a role from a composition",
  "policy.create": "Created a policy set", "policy.update": "Changed a policy set", "policy.delete": "Deleted a policy set",
  "policy.activate": "Turned on a policy set", "policy.deactivate": "Turned off a policy set",
  "policy.updated": "Made a new policy snapshot live",
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
  "sink.replay": "Replayed a sink's parked events",
  "oauth.connect": "Connected a user to an MCP server", "oauth.connect.refused": "Refused a connection to an MCP server",
  "oauth.disconnect": "Disconnected a user from an MCP server", "token.connect": "Stored a user's token for an MCP server",
  "connection.agents": "Changed which agents use a connection",
  "attestation-hash.registered": "Registered a hook configuration hash", "attestation-hash.removed": "Removed a hook configuration hash",
  enroll: "Enrolled a machine", renew: "Renewed a machine's credential", "breakglass.login": "Signed in with emergency access",
  "approver-enroll": "Enrolled an approver phone or browser", "approver-self-enroll-token": "Started enrolling an approver phone or browser",
  "approver-device-revoked": "Revoked an approver phone or browser",
  "enroll-token.create": "Started enrolling an approver phone or browser", "approver.enroll": "Enrolled an approver phone or browser",
  "approver.revoke": "Revoked an approver phone or browser with its user",
  "revocation.session": "Revoked a session", "revocation.sessions": "Revoked sessions", "revocation.user": "Revoked a user's access",
  "revocation.device": "Revoked a machine", "revocation.lift": "Lifted a user's revocation",
};

// ACTION_NOUN names the object of each action namespace, for an action
// this console does not know yet.
const ACTION_NOUN: Record<string, string> = {
  apps: "MCP server", "api-token": "Admin API token", roles: "Role", policy: "Policy set", "signing-keys": "Signing key",
  draft: "Draft", sessions: "Sessions", session: "Session", user: "User", identity: "User", "nhi-key": "AI agent key", sink: "Sink",
  oauth: "Connection", token: "Token", connection: "Connection", "attestation-hash": "Hook configuration hash", breakglass: "Emergency access",
  revocation: "Revocation",
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
