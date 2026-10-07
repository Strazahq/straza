import { describe, expect, it } from "vitest";
import { type Row, actionLabel, ceRow, rowLabel, csvOf, effectTone, effectWord, foldAdjacent, parseCE, shortType, typeMatches, whyOf } from "./audit-words";
import type { AuditRow } from "./api";

// The readers of the Audit screen, read once here so the screen and its
// sheet render the same reading of a record. Every record below is the
// shape strazad writes on the chain.

const SESSION = "0199cf12-4b1e-7a3c-9d21-0b6e4f2a8c10";
const USER_ID = "0198b2c1-77aa-7f00-8e12-3c4d5e6f7a80";

// rec wraps a CloudEvent the way GET /v1/admin/audit answers it.
const rec = (seq: number, ce: unknown, extra: Partial<AuditRow> = {}): AuditRow =>
  ({ seq, ce: JSON.stringify(ce), hash: "h" + seq, prevHash: "h" + (seq - 1), ...extra });

const tool = rec(4210, {
  type: "straza.audit.tool", time: "2026-09-12T10:45:01Z",
  data: { session: SESSION, user: USER_ID, tool: "shell.exec", command: "rm -rf ./build", effect: "deny", ruleId: "no-destructive-delete", setName: "dev-guardrails", reason: "Straza: destructive delete is refused for role dev" },
}, { username: "joe-java-developer-agent" });

const mcp = rec(4209, {
  type: "straza.audit.mcp", time: "2026-09-12T10:44:58Z",
  data: { session: SESSION, user: USER_ID, app: "midpoint", toolName: "assign_role", arguments: "{\"role\":\"sre\"}", effect: "approve", ruleId: "iga-writes", setName: "iga" },
}, { username: "joe-java-developer-agent" });

const approval = rec(4205, {
  type: "straza.audit.approval", time: "2026-09-12T10:31:45Z",
  data: { summary: "mcp.call midpoint:assign_role", state: "approved", decidedBy: "0198b2c1-9999-7f00-8e12-3c4d5e6f7a80", decidedReason: "reviewed the target, fine", rule: "iga-writes", user: USER_ID },
}, { username: "joe-java-developer-agent", decidedByUsername: "judy" });

const prompt = rec(4208, {
  type: "straza.audit.prompt", time: "2026-09-12T10:44:20Z",
  data: { session: SESSION, user: USER_ID, contentBytes: 412, mode: "redact", contentHash: "sha256:8f43c1a09b2e7d5f4411", content: "the registry key is AKIAIOSFODNN7EXAMPLE" },
}, { username: "joe-java-developer-agent" });

const sentinel = rec(4211, {
  type: "straza.audit.sentinel", time: "2026-09-12T10:45:30Z",
  data: { session: SESSION, detector: "burst-deny", severity: "critical", evidence: [1, 2, 3, 4, 5, 6, 7, 8, 9], reason: "9 refusals in 60 s in one session" },
});

const admin = rec(4218, {
  type: "straza.audit.admin", time: "2026-09-12T10:51:07Z",
  data: { action: "user.unlock carol", user: USER_ID },
}, { username: "alice" });

describe("ceRow", () => {
  it("reads a tool decision as the command, its effect and the rule that refused it", () => {
    const r = ceRow(tool);
    expect(r.seq).toBe(4210);
    expect(r.type).toBe("straza.audit.tool");
    expect(r.username).toBe("joe-java-developer-agent");
    expect(r.tool).toBe("shell.exec");
    expect(r.what).toBe("rm -rf ./build");
    expect(r.effect).toBe("deny");
    expect(r.reason).toBe("Straza: destructive delete is refused for role dev");
    expect(r.session).toBe(SESSION);
    expect(r.sentinel).toBe(false);
    expect(r.time).toBe("2026-09-12T10:45:01Z");
  });

  it("reads an MCP call as the upstream tool with its arguments, and falls back to the rule id for the reason", () => {
    const r = ceRow(mcp);
    expect(r.what).toBe("midpoint.assign_role · {\"role\":\"sre\"}");
    expect(r.effect).toBe("approve");
    expect(effectWord(r.effect)).toBe("needs approval");
    expect(r.reason).toBe("rule iga-writes");
  });

  it("reads an approval as the requester, the action, the verdict and the decider", () => {
    const r = ceRow(approval);
    expect(r.approval).toBe(true);
    expect(r.what).toBe("joe-java-developer-agent's assign role in midpoint approved by judy (rule iga-writes)");
    expect(r.effect).toBe("approved");
    expect(r.reason).toBe("reviewed the target, fine");
  });

  it("reads a capture record as the reference row and never the captured text", () => {
    const r = ceRow(prompt);
    expect(r.what).toBe("content 412 B · redact · 8f43c1a09b2e…");
    expect(r.what).not.toContain("AKIAIOSFODNN7EXAMPLE");
    expect(r.effect).toBe("");
  });

  it("reads a sentinel verdict as its detector, severity and evidence count", () => {
    const r = ceRow(sentinel);
    expect(r.sentinel).toBe(true);
    expect(r.severity).toBe("critical");
    expect(r.detector).toBe("burst-deny");
    expect(r.evidence).toBe(9);
    expect(r.username).toBe("");
    expect(r.reason).toBe("9 refusals in 60 s in one session");
  });

  it("reads an admin record as its action, with no effect", () => {
    const r = ceRow(admin);
    expect(r.what).toBe("user.unlock carol");
    expect(r.effect).toBe("");
    expect(effectWord(r.effect)).toBe("none");
    expect(shortType(r.type)).toBe("admin");
  });

  it("reads an unparseable record as one that cannot be read, without throwing", () => {
    const r = ceRow({ seq: 9, ce: "{not json", hash: "h9" });
    expect(r.what).toBe("(unreadable record)");
    expect(r.type).toBe("?");
  });
});

describe("foldAdjacent", () => {
  const read = (seq: number): Row => ({ ...ceRow(rec(seq, { type: "straza.audit.tool", time: "2026-09-12T10:49:5" + (seq % 10) + "Z", data: { user: USER_ID, tool: "read", paths: ["src/main/java/Billing.java"], effect: "allow", ruleId: "dev-reads" } }, { username: "joe-java-developer-agent" })) });

  it("collapses a run of identical rows into its newest row with the count and the run's end", () => {
    const rows = [read(4217), read(4216), read(4215), ceRow(tool)];
    const out = foldAdjacent(rows, {});
    expect(out.map((r) => r.seq)).toEqual([4217, 4210]);
    expect(out[0].foldN).toBe(3);
    expect(out[0].foldEnd).toBe(4215);
    expect(out[0].foldOpen).toBe(false);
    expect(out[1].foldN).toBeUndefined();
  });

  it("shows every row of the run once its newest seq is open", () => {
    const rows = [read(4217), read(4216), read(4215)];
    const out = foldAdjacent(rows, { 4217: true });
    expect(out.map((r) => r.seq)).toEqual([4217, 4216, 4215]);
    expect(out[0].foldOpen).toBe(true);
  });

  it("never folds a sentinel verdict, even beside an identical one", () => {
    const out = foldAdjacent([ceRow(sentinel), ceRow(rec(4212, JSON.parse(sentinel.ce)))], {});
    expect(out.map((r) => r.foldN)).toEqual([undefined, undefined]);
  });
});

describe("whyOf", () => {
  const why = (data: Record<string, unknown>, type = "straza.audit.tool") => whyOf({ type, time: "2026-09-12T10:45:01Z", data });

  it("says a rule in a policy decided it, with the rule's own reason", () => {
    const w = why({ effect: "deny", ruleId: "no-destructive-delete", setName: "dev-guardrails", reason: "Straza: destructive delete is refused for role dev", snapshot: "sha256:4b1e" });
    expect(w?.outcome).toBe("Denied.");
    expect(w?.tone).toBe("danger");
    expect(w?.basis).toBe("Decided by rule no-destructive-delete in policy dev-guardrails: Straza: destructive delete is refused for role dev");
    expect(w?.wire).toBe("effect=deny · ruleId=no-destructive-delete · setName=dev-guardrails · snapshot=sha256:4b1e");
  });

  it("says a rule in a policy decided it when the record carries no reason", () => {
    const w = why({ effect: "allow", ruleId: "dev-reads", setName: "dev-guardrails" });
    expect(w?.outcome).toBe("Allowed.");
    expect(w?.tone).toBe("ok");
    expect(w?.basis).toBe("Decided by rule dev-reads in policy dev-guardrails.");
  });

  it("says no policy matched when the record carries no rule", () => {
    const w = why({ effect: "deny", ruleId: "", setName: "", reason: "no rule matched" });
    expect(w?.basis).toBe("No policy rule was recorded for this call. no rule matched");
  });

  it("preserves role-access allowance without inventing a denial or pending approval", () => {
    const reason = "allowed by role access. No policy rule gates this tool.";
    const w = why({ effect: "allow", ruleId: "", setName: "", default: true, reason }, "straza.audit.mcp");
    expect(w?.outcome).toBe("Allowed.");
    expect(w?.basis).toBe("No policy rule was recorded for this call. " + reason);
    expect(w?.context).toBe("Recorded at decision time.");
  });

  it("does not invent a cause when an older decision has no rule or reason", () => {
    const w = why({ effect: "deny" });
    expect(w?.outcome).toBe("Denied.");
    expect(w?.basis).toBe("No policy rule was recorded for this call.");
  });

  it("says the policy name was not recorded on a record written before the field existed", () => {
    const w = why({ effect: "allow", ruleId: "dev-reads" });
    expect(w?.basis).toBe("Decided by rule dev-reads. The policy name was not recorded on this record.");
  });

  it("sends a held MCP call to Approvals for the outcome, and reads nothing that is not a decision", () => {
    const w = why({ effect: "approve", ruleId: "iga-writes", setName: "iga" }, "straza.audit.mcp");
    expect(w?.outcome).toBe("Needs approval.");
    expect(w?.tone).toBe("warn");
    expect(w?.context).toContain("see Approvals for the outcome");
    expect(whyOf(parseCE(sentinel.ce))).toBeNull();
    expect(whyOf(parseCE(admin.ce))).toBeNull();
  });
});

describe("effectTone and the lens", () => {
  it("paints a refusal red, an allowance green, a wait amber and everything else plain", () => {
    expect(effectTone("deny")).toBe("danger");
    expect(effectTone("denied")).toBe("danger");
    expect(effectTone("allow")).toBe("ok");
    expect(effectTone("approved")).toBe("ok");
    expect(effectTone("approve")).toBe("warn");
    expect(effectTone("confirm")).toBe("warn");
    expect(effectTone("expired")).toBe("warn");
    expect(effectTone("")).toBe("plain");
  });

  it("narrows to the types of the chosen lens and keeps every record under all records", () => {
    expect(typeMatches("straza.audit.tool", "decisions")).toBe(true);
    expect(typeMatches("straza.audit.mcp", "decisions")).toBe(true);
    expect(typeMatches("straza.audit.prompt", "decisions")).toBe(false);
    expect(typeMatches("straza.audit.prompt", "capture")).toBe(true);
    expect(typeMatches("straza.audit.admin", "all")).toBe(true);
  });
});

describe("csvOf", () => {
  it("writes the header, one line per row, and quotes a cell that carries a comma", () => {
    const lines = csvOf([ceRow(tool), ceRow(approval), ceRow(admin)]).split("\n");
    expect(lines[0]).toBe("seq,time,user,session,type,effect,what,reason");
    expect(lines[1]).toBe("4210,2026-09-12T10:45:01Z,joe-java-developer-agent," + SESSION + ",straza.audit.tool,deny,rm -rf ./build,Straza: destructive delete is refused for role dev");
    expect(lines[2]).toBe("4205,2026-09-12T10:31:45Z,joe-java-developer-agent,,straza.audit.approval,approved,joe-java-developer-agent's assign role in midpoint approved by judy (rule iga-writes),\"reviewed the target, fine\"");
    expect(lines[3]).toBe("4218,2026-09-12T10:51:07Z,alice,,straza.audit.admin,,user.unlock carol,");
  });
});

// ACTIONS is every action the server writes on an admin, identity or
// policy record, from the events spec and the producers.
const ACTIONS = [
  "apps.install", "apps.remove", "apps.disable", "apps.enable", "apps.recheck", "apps.binding.create", "apps.binding.delete",
  "apps.secret.set", "apps.secret.remove", "apps.grant.remove", "api-token.create", "api-token.revoke",
  "roles.assign", "roles.unassign", "roles.create", "roles.delete", "roles.update", "roles.implication.create", "roles.implication.delete",
  "policy.create", "policy.update", "policy.delete", "policy.activate", "policy.deactivate",
  "signing-keys.create", "signing-keys.rotate", "signing-keys.promote", "signing-keys.retire",
  "draft.create", "draft.update", "draft.check", "draft.contact", "draft.publish", "draft.discard", "draft.expire",
  "sessions.bulk-revoke", "user.create", "user.update", "user.lock", "user.unlock", "nhi-key.removed", "sink.replay",
  "nhi-key.set", "oauth.connect", "oauth.connect.refused", "attestation-hash.registered", "attestation-hash.removed",
  "user.killed", "user.reactivated", "user.lift.blocked",
  "session.start", "enroll", "renew", "breakglass.login", "approver-enroll", "approver-self-enroll-token", "approver-device-revoked",
  "enroll-token.create", "approver.enroll", "approver.revoke",
  "token.connect", "oauth.disconnect", "connection.agents", "identity.created", "identity.updated", "identity.deactivated", "policy.updated",
  "revocation.session", "revocation.sessions", "revocation.user", "revocation.device", "revocation.lift",
];

describe("actionLabel", () => {
  it.each(ACTIONS)("reads %s in words, never as the dotted action", (action) => {
    const label = actionLabel(action);
    expect(label).not.toBe(action);
    expect(label).not.toMatch(/[a-z]\.[a-z]/);
    expect(label.charAt(0)).toBe(label.charAt(0).toUpperCase());
  });

  it.each([
    ["policy.delete", "Deleted a policy set"],
    ["apps.binding.create", "Gave a role access to an MCP server"],
    ["policy.updated", "Made a new policy snapshot live"],
    ["user.create", "Created a user"],
    ["user.update", "Changed a user"],
    ["enroll-token.create", "Started enrolling an approver phone or browser"],
    ["approver.enroll", "Enrolled an approver phone or browser"],
    ["approver.revoke", "Revoked an approver phone or browser with its user"],
    ["nhi-key.set", "Registered an AI agent's key"],
    ["policy.frobnicate", "Policy set: frobnicate"],
    ["roles.implication.re-rank", "Role: implication re rank"],
    ["policy.re_rank", "Policy set: re rank"],
    ["apps.binding.create2", "MCP server: binding create2"],
    ["identity.suspended", "User: suspended"],
    ["frob-nitz.sparkle", "Frob nitz: sparkle"],
    ["published dev-guardrails v3", "published dev-guardrails v3"],
  ])("reads %s as %s, and never prints a dotted action raw", (value, want) => {
    expect(actionLabel(value)).toBe(want);
  });
});

describe("rowLabel", () => {
  const call = (type: string, data: Record<string, unknown>) => ceRow(rec(1, { type, time: "2026-09-12T10:45:01Z", data }));
  it.each([
    ["a hook call to the server policy", call("straza.audit.tool", { tool: "mcp.call", app: "policy", toolName: "delete", effect: "allow" }), "policy.delete"],
    ["a gateway call to the server draft", call("straza.audit.mcp", { app: "draft", toolName: "create", effect: "allow" }), "draft.create"],
    ["a gateway call to the server user", call("straza.audit.mcp", { app: "user", toolName: "get-profile", effect: "allow" }), "user.get-profile"],
    ["a shell command named like an action", call("straza.audit.tool", { tool: "shell.exec", command: "enroll", effect: "deny" }), "enroll"],
    ["an admin record", call("straza.audit.admin", { action: "policy.delete" }), "Deleted a policy set"],
    ["a revocation with no action", call("straza.revocation.session", { session: "s-1" }), "Revoked a session"],
  ])("reads %s", (_, row, want) => {
    expect(rowLabel(row)).toBe(want);
  });
});
