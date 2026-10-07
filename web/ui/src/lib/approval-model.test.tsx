import { describe, expect, it } from "vitest";
import type { ApprovalRow } from "./api";
import { callOf, dayLeft, decidersOf, durationWords, holdLeft, mine, paramsOf, phaseOf, stuck, timeLeft, windowWords, withinWords } from "./approval-model";

const NOW = Date.parse("2026-09-14T10:00:00Z");
const at = (s: number) => new Date(NOW + s * 1000).toISOString();

const base: ApprovalRow = {
  id: "apr_1", state: "pending", createdAt: at(-30), expiresAt: at(90), decidedAt: null,
  user: "u-joe", username: "joe-java-developer-agent", session: "0199c1a2-7e3f-7000-8000-000000000001",
  rule: "dev-mcp-sum-approval-showcase", set: "dev-guardrails", lane: "gateway",
  summary: "mcp.call demo-tools:get-sum", justification: "", approverRoles: [], approverUsers: ["alice"],
  selfApproval: false, mode: "approve", decidedBy: "", decidedByName: "",
};
const alice = { user: "alice", roles: ["straza-admin"] };
const bob = { user: "bob", roles: ["dev-tools"] };
const sec = { user: "carol", roles: ["sec-approvers"] };

describe("callOf", () => {
  it("reads the gateway and hook summaries and keeps anything else as stored", () => {
    expect(callOf("mcp.call demo-tools:get-sum")).toEqual({ where: "demo-tools", call: "get-sum", whereKind: "server" });
    expect(callOf("shell.exec: printf lf-q4-x")).toEqual({ where: "shell", call: "printf lf-q4-x", whereKind: "shell" });
    expect(callOf("file.write: /etc/hosts, /etc/passwd")).toEqual({ where: "files", call: "/etc/hosts, /etc/passwd", whereKind: "other" });
    expect(callOf("net.fetch")).toEqual({ where: "network", call: "fetch", whereKind: "other" });
    expect(callOf("something odd")).toEqual({ where: "", call: "something odd", whereKind: "other" });
  });
});

describe("whose decision it is", () => {
  it("is the sponsor's alone, never every admin's", () => {
    expect(decidersOf(base)).toEqual({ users: ["alice"], roles: [], kind: "sponsor" });
    expect(mine(base, alice)).toBe(true);
    expect(mine(base, bob)).toBe(false);
    expect(mine(base, { user: "root", roles: ["straza-admin"] })).toBe(false);
  });

  it("is any holder's when the rule names an approver role", () => {
    const r = { ...base, approverUsers: undefined, approverRoles: ["sec-approvers"] };
    expect(decidersOf(r)).toEqual({ users: [], roles: ["sec-approvers"], kind: "role" });
    expect(mine(r, sec)).toBe(true);
    expect(mine(r, alice)).toBe(false);
  });

  it("is either when the rule names the sponsor and a role", () => {
    const r = { ...base, approverRoles: ["sec-approvers"] };
    expect(decidersOf(r).kind).toBe("both");
    expect(mine(r, alice)).toBe(true);
    expect(mine(r, sec)).toBe(true);
    expect(mine(r, bob)).toBe(false);
  });

  it("falls to straza-admin only when the record names nobody", () => {
    const r = { ...base, approverUsers: undefined, approverRoles: [] };
    expect(decidersOf(r).kind).toBe("admins");
    expect(mine(r, alice)).toBe(true);
    expect(mine(r, bob)).toBe(false);
  });

  it("keeps the requester out of their own request unless the rule allows it", () => {
    const r = { ...base, username: "alice", user: "u-alice" };
    expect(mine(r, alice)).toBe(false);
    expect(mine({ ...r, selfApproval: true }, alice)).toBe(true);
  });

  it("gives a confirm record to the requester alone", () => {
    const r = { ...base, mode: "confirm", approverUsers: undefined, username: "bob" };
    expect(decidersOf(r)).toEqual({ users: ["bob"], roles: [], kind: "requester" });
    expect(mine(r, bob)).toBe(true);
    expect(mine(r, alice)).toBe(false);
  });

  it("is stuck when every named role has no holder and no user is named", () => {
    const r = { ...base, approverUsers: undefined, approverRoles: ["sec-approvers"] };
    expect(stuck(r, {})).toBe(true);
    expect(stuck(r, { "sec-approvers": 2 })).toBe(false);
    expect(stuck(base, {})).toBe(false);
    expect(stuck({ ...r, state: "expired" }, {})).toBe(false);
  });
});

describe("phases and clocks", () => {
  it("reads a hold through waiting, approved, denied and expired", () => {
    expect(phaseOf(base, NOW)).toBe("waiting");
    expect(phaseOf({ ...base, state: "approved" }, NOW)).toBe("approved");
    expect(phaseOf({ ...base, state: "denied" }, NOW)).toBe("denied");
    expect(phaseOf({ ...base, state: "expired" }, NOW)).toBe("expired");
  });

  it("reads an approved standing approval as granted, used or lapsed", () => {
    const t = { ...base, class: "ticket", state: "approved", grantExpiresAt: at(3480) };
    expect(phaseOf(t, NOW)).toBe("granted");
    expect(phaseOf({ ...t, consumedAt: at(-10), consumedBy: "ses" }, NOW)).toBe("used");
    expect(phaseOf({ ...t, grantExpiresAt: at(-1) }, NOW)).toBe("lapsed");
  });

  it("shows seconds and minutes for a hold and hours and days for a standing approval", () => {
    expect(holdLeft(at(45), NOW)).toBe("45 s left");
    expect(holdLeft(at(92), NOW)).toBe("1 m 32 s left");
    expect(holdLeft(at(-1), NOW)).toBe("expired");
    expect(dayLeft(at(82800), NOW)).toBe("23 h 00 min left");
    expect(dayLeft(at(90000), NOW)).toBe("1 d 1 h left");
    expect(dayLeft(at(600), NOW)).toBe("10 min left");
    expect(timeLeft({ ...base, class: "ticket", expiresAt: at(82800) }, NOW)).toBe("23 h 00 min left");
    expect(timeLeft(base, NOW)).toBe("1 m 30 s left");
  });

  it("words the open grant window and the record's own window", () => {
    expect(withinWords(at(3480), NOW)).toBe("the same call within 58 min runs");
    expect(withinWords(at(7260), NOW)).toBe("the same call within 2 h 01 min runs");
    expect(withinWords(at(-5), NOW)).toBe("the window closed");
    expect(windowWords({ createdAt: at(0), expiresAt: at(120) })).toBe("2 minutes");
    expect(windowWords({ createdAt: at(0), expiresAt: at(90) })).toBe("90 seconds");
    expect(windowWords({ createdAt: at(0), expiresAt: at(86400) })).toBe("1 day");
    expect(durationWords(3600)).toBe("1 hour");
    expect(durationWords(7200)).toBe("2 hours");
  });
});

describe("paramsOf", () => {
  it("flattens a JSON object into dotted names with lists and booleans as words", () => {
    const rows = paramsOf('{"user":"jdoe","options":{"recompute":true,"ticket":"CHG-1"},"notify":["a@x","b@x"],"n":3,"empty":null}');
    expect(rows).toEqual([
      { name: "user", value: "jdoe" },
      { name: "options.recompute", value: "yes" },
      { name: "options.ticket", value: "CHG-1" },
      { name: "notify", value: "a@x, b@x" },
      { name: "n", value: "3" },
      { name: "empty", value: "none" },
    ]);
  });

  it("answers null for a command line, a list and nothing", () => {
    expect(paramsOf("printf lf-q4-x")).toBe(null);
    expect(paramsOf("[1,2]")).toBe(null);
    expect(paramsOf("")).toBe(null);
    expect(paramsOf(undefined)).toBe(null);
    expect(paramsOf("{}")).toEqual([]);
  });
});
