import { describe, expect, it, vi } from "vitest";
import { accessRead, mayWritePolicy, retires } from "./access-read";
import { withChoice } from "./access-plan";
import type { BindingRow, PreviewEntry } from "./api";

const sess = vi.hoisted(() => ({ grants: "full" }));
vi.mock("./session", () => ({ snapshot: () => ({ user: "alice", roles: [], grants: sess.grants, expiresIn: 300, sessionID: "s1" }) }));

const NAMES = ["echo", "get-env", "get-sum"];
const row: BindingRow = { id: "b-1", app: "demo-tools", role: "readers", tools: ["*"] };

// OWN denies get-env for the role, the one rule the editor owns.
const OWN = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: readers-access",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [readers]",
  "  rules:",
  "    - id: demo-tools-deny",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        deny: [get-env]",
  "      effect: deny",
  "",
].join("\n");

// GUARD is another set that holds get-sum on a day-scale ticket.
const GUARD = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: guardrails",
  "spec:",
  "  rules:",
  "    - id: sum-ticket",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [get-sum]",
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        class: ticket",
  "        ticketTTLSeconds: 86400",
  "        grantTTLSeconds: 3600",
  "",
].join("\n");

const PREVIEW: Record<string, PreviewEntry> = {
  echo: { app: "demo-tools", tool: "echo", status: "visible", reason: "" },
  "get-env": { app: "demo-tools", tool: "get-env", status: "hidden_policy", reason: "", setName: "readers-access", ruleId: "demo-tools-deny" },
  "get-sum": { app: "demo-tools", tool: "get-sum", status: "approve_gated", reason: "", setName: "guardrails", ruleId: "sum-ticket" },
};

describe("what a door reads before the editor opens", () => {
  it("seeds its own rules, and names another set's hold or ticket with its window", () => {
    const read = accessRead("readers", "demo-tools", NAMES, row, OWN, PREVIEW, { guardrails: GUARD });
    expect(read.initial).toMatchObject({ reach: "later", call: "per", choice: { "get-env": "deny" } });
    expect(Object.keys(read.fixed)).toEqual(["get-sum"]);
    expect(read.fixed["get-sum"]).toMatchObject({ set: "guardrails", word: "needs approval: ticket within a day, then an hour to run" });
    expect(read.taken).toEqual([]);
  });

  it("shows every rule as fixed and seeds from the row alone when the own set could not be read", () => {
    const read = accessRead("readers", "demo-tools", NAMES, row, undefined, PREVIEW);
    expect(read.initial).toMatchObject({ reach: "later", call: "allow", choice: {} });
    expect(Object.keys(read.fixed).sort()).toEqual(["get-env", "get-sum"]);
    expect(read.fixed["get-env"]).toMatchObject({ set: "readers-access", word: "denied" });
  });

  // Each case is what the own set and the preview answered, and the reads
  // the editor is told failed.
  const reads: [string, string | null | undefined, Record<string, PreviewEntry> | null | undefined, { own: boolean; others: boolean }][] = [
    ["both read", OWN, PREVIEW, { own: false, others: false }],
    ["no own set yet", null, PREVIEW, { own: false, others: false }],
    ["the own set refused", undefined, PREVIEW, { own: true, others: false }],
    ["the preview refused", OWN, null, { own: false, others: true }],
    ["no preview asked", OWN, undefined, { own: false, others: false }],
    ["both refused", undefined, null, { own: true, others: true }],
  ];
  it.each(reads)("tells the editor which reads failed: %s", (_name, text, preview, unread) => {
    const read = accessRead("readers", "demo-tools", NAMES, row, text, preview);
    expect(read.unread).toEqual(unread);
    expect(read.text).toBe(text);
  });

  it("says a save retires the set only when no rule would be left in it", () => {
    const read = accessRead("readers", "demo-tools", NAMES, row, OWN, PREVIEW);
    expect(retires(read, withChoice(read.initial, "get-env", "allow"), "demo-tools", NAMES, "readers")).toBe(true);
    expect(retires(read, read.initial, "demo-tools", NAMES, "readers")).toBe(false);
    expect(retires({ ...read, taken: ["kept-by-hand"] }, withChoice(read.initial, "get-env", "allow"), "demo-tools", NAMES, "readers")).toBe(false);
  });

  // Each case is a session's admin grants and whether it may change what
  // a call does.
  const grants: [string, boolean][] = [
    ["full", true],
    ["policy:read,policy:write", true],
    ["apps:read,apps:write", false],
    ["policy:read", false],
    ["", false],
  ];
  it.each(grants)("reads the grants %j as may write policy: %s", (g, want) => {
    sess.grants = g;
    expect(mayWritePolicy()).toBe(want);
  });
});
