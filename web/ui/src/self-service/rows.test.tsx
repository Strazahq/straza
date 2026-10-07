import { describe, expect, it } from "vitest";
import { callOf, decidersOf, kindOf, phaseOf } from "@/lib/approval-model";
import { type ApproverRow, decidable, summaryString, toSelfRow } from "./rows";

const base: ApproverRow = {
  id: "apr_1", created_at: "2026-09-14T14:29:00Z", expires_at: "2026-09-14T14:31:00Z",
  requester: { username: "joe-java-developer-agent", kind: "nhi" },
  origin: { actor: "agent_session" }, session_id: "ses_4b1e", harness: "claude-code",
  rule_id: "get-sum-showcase", set_name: "dev-guardrails",
  summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get-sum" },
  justification: "Summing the two build durations.", challenge: "nonce",
  approver_users: ["alice"], args_preview: '{"a":2,"b":40}', argv_hash_prefix: "9f3a1c2e", binding_scope: "argv",
};

describe("toSelfRow", () => {
  it("maps a decidable MCP row onto the console's record", () => {
    const r = toSelfRow(base);
    expect(r.state).toBe("pending");
    expect(r.username).toBe("joe-java-developer-agent");
    expect(r.summary).toBe("mcp.call demo-tools:get-sum");
    expect(callOf(r.summary)).toEqual({ where: "demo-tools", call: "get-sum", whereKind: "server" });
    expect(r.lane).toBe("gateway");
    expect(decidersOf(r)).toEqual({ users: ["alice"], roles: [], kind: "sponsor" });
    expect(kindOf(r)).toBe("hold");
    expect(phaseOf(r)).toBe("waiting");
    expect(r.session).toBe("ses_4b1e");
    expect(r.challenge).toBe("nonce");
    expect(decidable(r)).toBe(true);
    expect(r.requesterKind).toBe("nhi");
  });

  it("reads a hook call's command from the preview and a bare kind as itself", () => {
    expect(summaryString({ summary: { tool: "shell.exec" }, args_preview: "terraform apply\n--auto-approve" })).toBe("shell.exec: terraform apply");
    expect(summaryString({ summary: { tool: "file.read" } })).toBe("file.read");
    const r = toSelfRow({ ...base, summary: { tool: "shell.exec" }, args_preview: "printf x", challenge: undefined, approver_users: undefined, approver_roles: ["sec-approvers"] });
    expect(r.lane).toBe("hook");
    expect(callOf(r.summary)).toEqual({ where: "shell", call: "printf x", whereKind: "shell" });
    expect(decidersOf(r).kind).toBe("role");
    expect(decidable(r)).toBe(false);
  });

  it("maps a decided row with its decider, channel, device and reason", () => {
    const r = toSelfRow({ ...base, challenge: undefined, state: "denied", decided_by: "alice", decided_at: "2026-09-13T14:02:00Z", decided_via: { surface: "browser", device_id: "apd_7f3c" }, decided_reason: "walk cleanup", class: "ticket", grant_expires_at: null });
    expect(r.state).toBe("denied");
    expect(r.decidedByName).toBe("alice");
    expect(r.channel).toBe("browser");
    expect(r.decidedDeviceId).toBe("apd_7f3c");
    expect(r.decidedReason).toBe("walk cleanup");
    expect(kindOf(r)).toBe("ticket");
    expect(phaseOf(r)).toBe("denied");
    expect(decidable(r)).toBe(false);
  });
});
