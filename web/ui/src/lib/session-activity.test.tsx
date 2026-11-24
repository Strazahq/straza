import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type { ApprovalRow, AuditRow } from "./api";
import { GATEWAY_DEFERRED, countOf, inFilter, sessionActivity, sessionQuery, type ActivityRow } from "./session-activity";
import { DENIED_BY_DEFAULT, NOBODY_DECIDED, WAITING_PLAIN, approvedBy, deniedBy, noRoleReaches, ruleLine, throughRole, waitingFor } from "./session-words";

const repo = (rel: string) => fileURLToPath(new URL("../../../../" + rel, import.meta.url));

const S = "01a1da3d-934c-7be8-bd1a-92e51c045044";
const OTHER = "01a1da57-9a2d-7b44-8e10-4c7d1f905b22";
const T0 = Date.parse("2026-11-14T14:27:10Z");
const NOW = T0 + 5 * 60 * 1000;
const at = (s: number) => new Date(T0 + s * 1000).toISOString();

// rec is one chain record of the session, s seconds after T0.
const rec = (seq: number, type: string, s: number, data: Record<string, unknown>, extra: Partial<AuditRow> = {}): AuditRow =>
  ({ seq, ce: JSON.stringify({ type, time: at(s), data: { session: S, user: "u-altair", ...data } }), username: "altair", ...extra });
const mcp = (seq: number, s: number, data: Record<string, unknown>) => rec(seq, "straza.audit.mcp", s, { tool: "mcp.call", granted: true, default: false, ...data });
const tool = (seq: number, s: number, data: Record<string, unknown>) => rec(seq, "straza.audit.tool", s, data);
const approval = (seq: number, s: number, phase: string, id: string, data: Record<string, unknown>, extra: Partial<AuditRow> = {}) =>
  rec(seq, "straza.audit.approval", s, { phase, approvalId: id, ...data }, extra);

const PUSH = { rule: "push-needs-yes", set: "agent-guardrails" };
const GATE = { rule: "delete-needs-yes", set: "agent-guardrails" };
// The request records of the two lanes, the way each stores its summary.
const hookRequest = (seq: number, s: number, id: string) => approval(seq, s, "request", id, { ...PUSH, lane: "hook", state: "pending", summary: "shell.exec: git push origin main" });
const gateRequest = (seq: number, s: number, id: string) => approval(seq, s, "request", id, { ...GATE, lane: "gateway", state: "pending", summary: "mcp.call github:delete_branch" });
const resolution = (seq: number, s: number, id: string, state: string, lane: string) =>
  approval(seq, s, "resolution", id, { ...(lane === "hook" ? PUSH : GATE), lane, state, decidedBy: state === "expired" ? undefined : "u-alice" }, { decidedByUsername: state === "expired" ? undefined : "alice" });
const hookHold = (seq: number, s: number) => tool(seq, s, { tool: "shell.exec", command: "git push origin main", effect: "deny", ruleId: PUSH.rule, setName: PUSH.set, reason: "Straza: approval pending" });
const gateCall = (seq: number, s: number, effect: string, reason = "") => mcp(seq, s, { app: "github", toolName: "delete_branch", effect, ruleId: GATE.rule, setName: GATE.set, reason });

// waiting is the pending answer of the approvals list for one request,
// 100 seconds before its hold ends.
const waiting = (id: string): ApprovalRow => ({ id, state: "pending", expiresAt: new Date(NOW + 100000).toISOString(), approverUsers: ["alice"], approverRoles: [], mode: "approve" } as unknown as ApprovalRow);

// The two records of one call a harness hands to the gateway: the hook's
// hand-over, which decides nothing, and the gateway's decision of it.
const handOver = (seq: number, s: number) =>
  tool(seq, s, { tool: "mcp.call", app: "objects-demo", toolName: "repo_read", effect: "allow", ruleId: "", reason: "Straza: decision deferred to the gateway PEP, which evaluates current policy server-side on this call (single gate)" });
const gatewayAllow = (seq: number, s: number) => mcp(seq, s, { app: "objects-demo", toolName: "repo_read", effect: "allow", role: "objects-demo-tools" });
const gatewayDeny = (seq: number, s: number) =>
  mcp(seq, s, { app: "objects-demo", toolName: "repo_read", effect: "deny", granted: false, ruleId: "deny-repo-read", setName: "agent-guardrails", reason: "Reading this repo is refused." });
const REPO_READ = { kind: "mcp" as const, server: "objects-demo", tool: "repo_read" };

const SHELL_PUSH = { kind: "shell" as const, command: "git push origin main" };
const GITHUB_DELETE = { kind: "mcp" as const, server: "github", tool: "delete_branch" };

type Want = Partial<Omit<ActivityRow, "record">>;

const CASES: [string, AuditRow[], ApprovalRow[] | null, Want[]][] = [
  ["an allowed MCP call through a role", [mcp(1, 10, { app: "github", toolName: "list_issues", effect: "allow", role: "github-readers", bindingId: "b1" })], null,
    [{ seq: 1, outcome: "allowed", call: { kind: "mcp", server: "github", tool: "list_issues" }, line: "", note: throughRole("github-readers"), waiting: false, approval: "" }]],
  ["an allowed MCP call a policy rule decided", [mcp(1, 10, { app: "elevenlabs", toolName: "search_voices", effect: "allow", ruleId: "elevenlabs-audio-allow", setName: "audio-gen-access", role: "audio-gen" })], null,
    [{ outcome: "allowed", line: "", note: ruleLine("elevenlabs-audio-allow", "audio-gen-access") }]],
  ["an allowed shell command", [tool(1, 10, { tool: "shell.exec", command: "ls -la", effect: "allow", ruleId: "", setName: "" })], null,
    [{ outcome: "allowed", call: { kind: "shell", command: "ls -la" }, line: "", note: "" }]],
  ["a call denied by a policy rule", [tool(1, 10, { tool: "shell.exec", command: "rm -rf build/", effect: "deny", ruleId: "deny-rm-rf", setName: "agent-guardrails", reason: "Recursive force delete is refused." })], null,
    [{ outcome: "denied", line: "Recursive force delete is refused.", note: ruleLine("deny-rm-rf", "agent-guardrails") }]],
  ["a deny no policy rule decided, with its reason", [mcp(1, 10, { app: "github", toolName: "list_issues", effect: "deny", granted: false, reason: "Straza: too many calls to github in a minute. Wait, then try again." })], null,
    [{ outcome: "denied", line: "Straza: too many calls to github in a minute. Wait, then try again.", note: "" }]],
  ["a default deny with no reason", [mcp(1, 10, { app: "elevenlabs", toolName: "check_subscription", effect: "deny", granted: false, default: true })], null,
    [{ outcome: "denied", line: "", note: DENIED_BY_DEFAULT }]],
  ["a gateway request deny of a revoked session", [mcp(1, 10, { event: "request", tool: "", toolName: "", setName: "", app: "github", effect: "deny", granted: false, ruleId: "revoked", reason: "Straza: this session was revoked." })], null,
    [{ outcome: "denied", line: "Straza: this session was revoked.", note: "" }]],
  ["a gateway request deny below the attestation floor", [mcp(1, 10, { event: "request", tool: "", toolName: "", setName: "", app: "", effect: "deny", granted: false, ruleId: "attestation", reason: "Straza: this session's attestation is below governance.minAttestation." })], null,
    [{ outcome: "denied", line: "Straza: this session's attestation is below governance.minAttestation.", note: "" }]],
  ["a gateway request deny with a rule id that is not a policy rule and no reason", [mcp(1, 10, { event: "request", tool: "", app: "", effect: "deny", granted: false, ruleId: "revoked" })], null,
    [{ outcome: "denied", line: "", note: DENIED_BY_DEFAULT }]],
  ["a no access", [mcp(1, 10, { app: "", toolName: "github__delete_branch", effect: "deny", granted: false, reason: 'unknown tool "github__delete_branch": no access row for this session\'s roles admits it' })], null,
    [{ outcome: "none", line: noRoleReaches("altair"), note: "", call: { kind: "mcp", server: "", tool: "github__delete_branch" } }]],
  ["a file read and a network fetch", [tool(1, 10, { tool: "file.read", paths: ["/etc/hosts"], effect: "allow" }), tool(2, 20, { tool: "net.fetch", paths: ["https://example.com/a?b=1"], effect: "allow" })], null,
    [{ seq: 2, call: { kind: "net", word: "network fetch", target: "example.com" } }, { seq: 1, call: { kind: "file", word: "file read", target: "/etc/hosts" } }]],

  ["a call the hook handed to the gateway, as the gateway's row alone", [handOver(1, 10), gatewayAllow(2, 10)], null,
    [{ seq: 2, outcome: "allowed", call: REPO_READ, line: "", note: throughRole("objects-demo-tools") }]],
  ["the hook's hand-over without the gateway's record", [handOver(1, 10)], null, []],
  ["a gateway deny of a call the hook handed over", [handOver(1, 10), gatewayDeny(2, 10)], null,
    [{ seq: 2, outcome: "denied", call: REPO_READ, line: "Reading this repo is refused.", note: ruleLine("deny-repo-read", "agent-guardrails") }]],
  ["an MCP call on a server on the person's machine a policy rule allowed",
    [tool(1, 10, { tool: "mcp.call", app: "claude-in-chrome", toolName: "navigate", effect: "allow", ruleId: "local-tools", setName: "agent-guardrails", reason: "Straza: allowed by local-tools" })], null,
    [{ seq: 1, outcome: "allowed", call: { kind: "mcp", server: "claude-in-chrome", tool: "navigate" }, line: "", note: ruleLine("local-tools", "agent-guardrails") }]],

  ["a gateway call waiting for approval", [gateRequest(1, 10, "apr-g")], [waiting("apr-g")],
    [{ seq: 1, outcome: "approval", call: GITHUB_DELETE, line: waitingFor("alice", "1 m 40 s left"), note: ruleLine(GATE.rule, GATE.set), waiting: true, approval: "apr-g" }]],
  ["a gateway call waiting that the approvals list does not hold", [gateRequest(1, 10, "apr-g")], null,
    [{ outcome: "approval", line: WAITING_PLAIN, waiting: true, approval: "apr-g" }]],
  ["a gateway call approved", [gateRequest(1, 10, "apr-g"), resolution(2, 40, "apr-g", "approved", "gateway"), gateCall(3, 41, "allow")], [],
    [{ seq: 3, outcome: "approval", call: GITHUB_DELETE, line: approvedBy("alice"), note: ruleLine(GATE.rule, GATE.set), waiting: false, approval: "" }]],
  ["a gateway call denied", [gateRequest(1, 10, "apr-g"), resolution(2, 40, "apr-g", "denied", "gateway"), gateCall(3, 41, "deny", "Straza: approval denied")], [],
    [{ seq: 3, outcome: "approval", line: deniedBy("alice"), waiting: false }]],
  ["a gateway call nobody decided", [gateRequest(1, 10, "apr-g"), resolution(2, 130, "apr-g", "expired", "gateway"), gateCall(3, 131, "deny", "Straza: approval expired")], [],
    [{ seq: 3, outcome: "approval", line: NOBODY_DECIDED, waiting: false }]],

  ["a hook call waiting for approval", [hookRequest(1, 10, "apr-h"), hookHold(2, 10)], [waiting("apr-h")],
    [{ seq: 2, outcome: "approval", call: SHELL_PUSH, line: waitingFor("alice", "1 m 40 s left"), note: ruleLine(PUSH.rule, PUSH.set), waiting: true, approval: "apr-h" }]],
  ["a hook call approved", [hookRequest(1, 10, "apr-h"), hookHold(2, 10), resolution(3, 50, "apr-h", "approved", "hook")], [],
    [{ seq: 2, outcome: "approval", line: approvedBy("alice"), waiting: false, approval: "" }]],
  ["a hook call denied", [hookRequest(1, 10, "apr-h"), hookHold(2, 10), resolution(3, 50, "apr-h", "denied", "hook")], [],
    [{ seq: 2, outcome: "approval", line: deniedBy("alice") }]],
  ["a hook call nobody decided", [hookRequest(1, 10, "apr-h"), hookHold(2, 10), resolution(3, 100, "apr-h", "expired", "hook")], [],
    [{ seq: 2, outcome: "approval", line: NOBODY_DECIDED }]],

  ["a decision past the window, which stays a call of its own", [hookRequest(1, 10, "apr-h"), hookHold(2, 10 + 11 * 60)], null,
    [{ seq: 2, outcome: "denied", line: "Straza: approval pending" }, { seq: 1, outcome: "approval", call: SHELL_PUSH, waiting: true }]],
  ["a record of another session", [{ ...mcp(1, 10, { app: "github", toolName: "list_issues", effect: "allow" }), ce: JSON.stringify({ type: "straza.audit.mcp", time: at(10), data: { session: OTHER, effect: "allow" } }) }], null, []],
  ["the record types that give no row", [
    rec(1, "straza.audit.prompt", 1, { mode: "verbatim", contentHash: "sha256:aa" }),
    rec(2, "straza.audit.reply", 2, { mode: "verbatim", contentHash: "sha256:bb" }),
    rec(3, "straza.audit.authn", 3, { action: "login", outcome: "success" }),
    rec(4, "straza.audit.authn", 4, { action: "session.end", outcome: "revoked-admin" }),
    rec(5, "straza.audit.sentinel", 5, { detector: "denyBurst", severity: "warn" }),
    rec(6, "straza.audit.admin", 6, { action: "session.start" }),
    approval(7, 7, "consumed", "apr-t", { rule: "x", state: "approved" }),
    mcp(8, 8, { event: "tools.list", effect: "allow", reason: "catalog served", count: 4 }),
    tool(9, 9, { event: "audit.dropped", reason: "spool full" }),
  ], null, []],
];

describe("sessionActivity", () => {
  it.each(CASES)("reads %s", (_case, records, pending, want) => {
    const rows = sessionActivity(records, pending, S, NOW);
    expect(rows.length).toBe(want.length);
    want.forEach((w, i) => expect(rows[i]).toMatchObject(w));
  });

  it("keeps the record each row opens and lists the rows newest first", () => {
    const first = tool(1, 30, { tool: "shell.exec", command: "a", effect: "allow" });
    const late = tool(2, 10, { tool: "shell.exec", command: "b", effect: "allow" });
    const rows = sessionActivity([late, first], null, S, NOW);
    expect(rows.map((r) => r.seq)).toEqual([1, 2]);
    expect(rows[0].record).toBe(first);
    expect(rows[0].time).toBe(at(30));
  });

  it("pairs a request with the decision that follows it in the chain, not one before it", () => {
    const rows = sessionActivity([gateCall(1, 5, "allow"), gateRequest(2, 10, "apr-g")], null, S, NOW);
    expect(rows.map((r) => [r.seq, r.outcome])).toEqual([[2, "approval"], [1, "allowed"]]);
  });

  it("knows the hand-over by the sentence the straza client writes", () => {
    const decide = readFileSync(repo("internal/agentguard/service/api_decide.go"), "utf8");
    expect(decide).toContain('Reason: "' + GATEWAY_DEFERRED);
  });
});

describe("the activity filter", () => {
  const rows = sessionActivity([
    mcp(1, 10, { app: "github", toolName: "list_issues", effect: "allow", role: "github-readers" }),
    tool(2, 20, { tool: "shell.exec", command: "rm -rf /", effect: "deny", ruleId: "deny-rm-rf", reason: "no" }),
    mcp(3, 30, { app: "", toolName: "x", effect: "deny", reason: 'unknown tool "x": no access row for this session\'s roles admits it' }),
    gateRequest(4, 40, "apr-g"),
  ], null, S, NOW);

  it.each([
    ["all", [4, 3, 2, 1]],
    ["waiting", [4]],
    ["denied", [3, 2]],
  ] as const)("shows under %s the rows it holds", (f, seqs) => {
    expect(rows.filter((r) => inFilter(r, f)).map((r) => r.seq)).toEqual(seqs);
    expect(countOf(rows, f)).toBe(seqs.length);
  });

  it("counts the allowed calls", () => {
    expect(countOf(rows, "allowed")).toBe(1);
  });
});

describe("sessionQuery", () => {
  it("names the session with its key and quotes", () => {
    expect(sessionQuery(S)).toBe('"session":"' + S + '"');
  });
});
