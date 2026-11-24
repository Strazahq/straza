// The Activity of a session's page: one row per call the session made, read
// from the audit records of the session. Each function is pure over the
// wire rows, so the suite pins the reading as a table.
import type { ApprovalRow, AuditRow } from "./api";
import { callOf, decidersOf, secondsLeft, timeLeft } from "./approval-model";
import { deciderWords } from "./approval-words";
import { parseCE } from "./audit-words";
import type { Outcome } from "./chip-words";
import { TOOL_WORD } from "./policy-model";
import { DENIED_BY_DEFAULT, NOBODY_DECIDED, WAITING_PLAIN, approvedBy, cutID, deniedBy, noRoleReaches, ruleLine, throughRole, waitingFor } from "./session-words";

// ACTIVITY_LIMIT is how many records the page reads, the most the audit
// route takes in one answer.
export const ACTIVITY_LIMIT = 1000;

// sessionQuery is the whole-chain search for the records of one session. A
// record names its session as session and the server matches the raw
// record text, so the key and both quotes keep out a longer id.
export const sessionQuery = (id: string) => '"session":"' + id + '"';

// ofSession keeps the records whose data names session, since the search
// matches the text of a record that only quotes the id.
export function ofSession(records: AuditRow[], session: string): AuditRow[] {
  return records.filter((r) => {
    const ce = parseCE(r.ce);
    return !!ce && !!ce.data && ce.data.session === session;
  });
}

// ActivityCall is what a row says was called: an MCP tool on its server, a
// shell command, or a file, network or other tool with its target.
export type ActivityCall =
  | { kind: "mcp"; server: string; tool: string }
  | { kind: "shell"; command: string }
  | { kind: "file" | "net" | "other"; word: string; target: string };

// ActivityRow is one call. line is the sentence under the call and note
// the small line under it, each "" when the row has none. waiting says the
// call needs approval and its request is still pending, and approval is
// that request's id.
export type ActivityRow = {
  seq: number;
  time: string;
  record: AuditRow;
  outcome: Outcome;
  call: ActivityCall;
  line: string;
  note: string;
  waiting: boolean;
  approval: string;
};

export type ActivityFilter = "all" | "waiting" | "denied";

const APPROVAL = "straza.audit.approval";
const GATEWAY = "straza.audit.mcp";
const HOOK = "straza.audit.tool";
const DECISIONS = [HOOK, GATEWAY];

// GATEWAY_DEFERRED opens the reason the straza client writes when it hands a
// call to the gateway. The gateway decides that call and writes its own
// record, so the client's record states no outcome.
export const GATEWAY_DEFERRED = "Straza: decision deferred to the gateway";

// NO_ACCESS ends the gateway's refusal of a tool no access row admits.
const NO_ACCESS = "no access row for this session's roles admits it";

// NOT_POLICY_RULES are the rule ids the gateway writes on its deny of a
// whole request after the token verified: the session was revoked, or its
// attestation is below the floor. Neither names a policy rule.
const NOT_POLICY_RULES = ["revoked", "attestation"];

// MATCH_WINDOW_MS bounds how long after its request record a decision
// record still counts as that held call. The server takes twice the gateway
// hold and at least ten minutes. The console does not know the hold, so it
// takes the ten minutes.
const MATCH_WINDOW_MS = 10 * 60 * 1000;

type Rec = { r: AuditRow; type: string; time: string; at: number; d: Record<string, unknown> };

const text = (v: unknown) => (v === undefined || v === null ? "" : String(v));

// notACall says a decision record decided no call: a served catalog, which
// the server counts apart from the calls, a client's loss record, or the
// hook's hand-over of a call to the gateway, whose own record is the call.
function notACall(p: Rec): boolean {
  const event = text(p.d.event);
  return (p.type === GATEWAY && (event === "tools.list" || event === "resources.list")) || event === "audit.dropped" ||
    (p.type === HOOK && text(p.d.reason).startsWith(GATEWAY_DEFERRED));
}

const hostOf = (target: string) => {
  try { return new URL(target).host || target; } catch { return target; }
};

// callOfData reads the call a decision record names.
export function callOfData(type: string, d: Record<string, unknown>): ActivityCall {
  const tool = text(d.tool);
  const paths = Array.isArray(d.paths) ? d.paths.map(String) : [];
  if (type === GATEWAY || tool === "mcp.call") return { kind: "mcp", server: text(d.app), tool: text(d.toolName || d.uri || d.event) };
  if (tool === "shell.exec") return { kind: "shell", command: text(d.command) };
  if (tool.startsWith("file.")) return { kind: "file", word: TOOL_WORD[tool] || tool, target: paths.join(" ") };
  if (tool === "net.fetch") return { kind: "net", word: TOOL_WORD[tool], target: paths.map(hostOf).join(" ") };
  return { kind: "other", word: text(d.toolName || d.nativeTool || tool), target: "" };
}

// callOfSummary reads the call of a request no decision record answered
// yet, from the summary the request stores.
function callOfSummary(summary: string): ActivityCall {
  const c = callOf(summary);
  if (c.whereKind === "server") return { kind: "mcp", server: c.where, tool: c.call };
  if (c.whereKind === "shell") return { kind: "shell", command: c.call };
  const m = /^([a-z]+\.[a-z_]+)(?::\s+(\S.*))?$/.exec(summary);
  return m ? callOfData("", { tool: m[1], paths: m[2] ? [m[2]] : [] }) : { kind: "other", word: summary, target: "" };
}

// takeRequest pops the oldest request of the policy rule that precedes at
// within the window, the way the server pairs a held call.
function takeRequest(open: Map<string, Rec[]>, rule: string, at: number): Rec | null {
  const reqs = open.get(rule) || [];
  const i = reqs.findIndex((q) => q.at <= at && at - q.at <= MATCH_WINDOW_MS);
  if (i < 0) return null;
  const [hit] = reqs.splice(i, 1);
  return hit;
}

// sessionActivity reads the audit records of session into its rows, newest
// first. pending is the answer of the pending approvals, null when it is
// not read. A held call is one row: the approval request record pairs with
// the decision record of the same policy rule that follows it in the chain
// within the window, and a request still unpaired is a row of its own.
export function sessionActivity(records: AuditRow[], pending: ApprovalRow[] | null, session: string, now = Date.now()): ActivityRow[] {
  const mine: Rec[] = [];
  for (const r of records) {
    const ce = parseCE(r.ce);
    const d = (ce && ce.data) || {};
    if (!ce || d.session !== session) continue;
    mine.push({ r, type: text(ce.type), time: text(ce.time), at: Date.parse(text(ce.time)), d });
  }
  mine.sort((a, b) => a.r.seq - b.r.seq);
  const resolutions = new Map<string, Rec>();
  for (const p of mine) if (p.type === APPROVAL && p.d.phase === "resolution") resolutions.set(text(p.d.approvalId), p);
  const waitingOn = new Map((pending || []).map((a) => [a.id, a]));

  const held = (p: Rec, req: Rec): ActivityRow => {
    const id = text(req.d.approvalId);
    const res = resolutions.get(id);
    let line = "";
    if (res) {
      const who = res.r.decidedByUsername || cutID(text(res.d.decidedBy));
      line = res.d.state === "approved" ? approvedBy(who) : res.d.state === "denied" ? deniedBy(who) : res.d.state === "expired" ? NOBODY_DECIDED : "";
    } else {
      const a = waitingOn.get(id);
      line = a && secondsLeft(a.expiresAt, now) > 0 ? waitingFor(deciderWords(decidersOf(a)), timeLeft(a, now)) : WAITING_PLAIN;
    }
    const rule = text(p.d.ruleId || req.d.rule);
    return {
      seq: p.r.seq, time: p.time, record: p.r, outcome: "approval",
      call: p === req ? callOfSummary(text(req.d.summary)) : callOfData(p.type, p.d),
      line, note: rule ? ruleLine(rule, text(p.d.setName || req.d.set)) : "", waiting: !res, approval: res ? "" : id,
    };
  };

  // A denied call says the record's own reason, whether a policy rule
  // decided it or not. Only a deny with no policy rule and no reason at all
  // reads as denied by default.
  const decided = (p: Rec): ActivityRow => {
    const raw = text(p.d.ruleId);
    const ruleId = NOT_POLICY_RULES.includes(raw) ? "" : raw;
    const reason = text(p.d.reason);
    const gateway = p.type === GATEWAY;
    const outcome: Outcome = p.d.effect === "allow" ? "allowed" : gateway && !ruleId && reason.includes(NO_ACCESS) ? "none" : "denied";
    let line = "";
    if (outcome === "denied") line = reason;
    if (outcome === "none") line = noRoleReaches(p.r.username || cutID(text(p.d.user)));
    let note = "";
    if (ruleId) note = ruleLine(ruleId, text(p.d.setName));
    else if (outcome === "allowed" && gateway && p.d.role) note = throughRole(text(p.d.role));
    else if (outcome === "denied" && !reason) note = DENIED_BY_DEFAULT;
    return { seq: p.r.seq, time: p.time, record: p.r, outcome, call: callOfData(p.type, p.d), line, note, waiting: false, approval: "" };
  };

  const open = new Map<string, Rec[]>();
  const rows: ActivityRow[] = [];
  for (const p of mine) {
    if (p.type === APPROVAL) {
      if (p.d.phase === "request") {
        const rule = text(p.d.rule);
        open.set(rule, [...(open.get(rule) || []), p]);
      }
      continue;
    }
    if (!DECISIONS.includes(p.type) || notACall(p)) continue;
    const effect = p.d.effect;
    const req = effect === "allow" || effect === "deny" ? takeRequest(open, text(p.d.ruleId), p.at) : null;
    rows.push(req ? held(p, req) : decided(p));
  }
  for (const reqs of open.values()) for (const req of reqs) rows.push(held(req, req));
  const stamp = (row: ActivityRow) => { const t = Date.parse(row.time); return Number.isNaN(t) ? -Infinity : t; };
  return rows.sort((a, b) => stamp(b) - stamp(a) || b.seq - a.seq);
}

// inFilter says whether a row shows under a filter: Waiting holds the calls
// whose request is still pending, Denied the denied calls and the calls no
// access row admits.
export const inFilter = (row: ActivityRow, f: ActivityFilter) =>
  f === "all" || (f === "waiting" ? row.waiting : row.outcome === "denied" || row.outcome === "none");

// countOf counts the rows that show under a filter, and the allowed calls.
export const countOf = (rows: ActivityRow[], f: ActivityFilter | "allowed") => rows.filter((r) => (f === "allowed" ? r.outcome === "allowed" : inFilter(r, f))).length;
