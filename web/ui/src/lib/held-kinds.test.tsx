import { describe, expect, it } from "vitest";
import type { PreviewEntry } from "./api";
import { heldKinds, heldSets } from "./held-kinds";
import { type PolicyCounts, SPONSOR_POOL, heldTitle, policySummary } from "./role-words";

// The seeded role's own set: get-sum held for 2 minutes by the person
// behind the agent, get-env on a day-scale ticket an approver role grants.
const OWN = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: demo-tools-readers-access",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [demo-tools-readers]",
  "  rules:",
  "    # Held by hand, under Policies.",
  "    - id: demo-tools-get-sum-approve",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [get-sum]",
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        deciders: [sponsor]",
  "        timeoutSeconds: 120",
  "    - id: demo-tools-get-env-ticket",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [get-env]",
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        roles: [sec-approvers]",
  "        class: ticket",
  "        ticketTTLSeconds: 86400",
  "        grantTTLSeconds: 3600",
  "",
].join("\n");

const SET = "demo-tools-readers-access";
const entry = (tool: string, status: string, ruleId?: string, setName = SET): PreviewEntry => ({ app: "demo-tools", tool, status, reason: "", ruleId, setName: ruleId ? setName : undefined });

const PREVIEW: Record<string, PreviewEntry> = {
  echo: entry("echo", "visible"),
  "get-annotated-message": entry("get-annotated-message", "visible"),
  "get-sum": entry("get-sum", "approve_gated", "demo-tools-get-sum-approve"),
  "get-env": entry("get-env", "approve_gated", "demo-tools-get-env-ticket"),
};

describe("hold or ticket on a reading surface", () => {
  it("lists each set that holds a tool once", () => {
    expect(heldSets(PREVIEW)).toEqual([SET]);
    expect(heldSets(null)).toEqual([]);
  });

  // Each case is a preview, the set texts read, and the Policy cell's
  // counts with the hovers of its needs approval and denied chips.
  const cases: [string, Record<string, PreviewEntry>, Record<string, string | null>, PolicyCounts][] = [
    ["says hold and ticket once the set is read", PREVIEW, { [SET]: OWN }, { approval: 2, denied: 0, allowed: 2, approvalTitle: "1 hold and 1 ticket, by " + SET, deniedTitle: "" }],
    ["counts needs approval while the set is not read", PREVIEW, {}, { approval: 2, denied: 0, allowed: 2, approvalTitle: "by " + SET, deniedTitle: "" }],
    ["counts needs approval when the set could not be read", PREVIEW, { [SET]: null }, { approval: 2, denied: 0, allowed: 2, approvalTitle: "by " + SET, deniedTitle: "" }],
    [
      "counts a rule it cannot find as needs approval, beside the one it can",
      { ...PREVIEW, "get-env": entry("get-env", "approve_gated", "written-elsewhere") },
      { [SET]: OWN },
      { approval: 2, denied: 0, allowed: 2, approvalTitle: "1 hold and 1 needs approval, by " + SET, deniedTitle: "" },
    ],
    [
      "keeps a denied tool apart with its own policy",
      { ...PREVIEW, echo: entry("echo", "hidden_policy", "no-echo", "agent-guardrails") },
      { [SET]: OWN },
      { approval: 2, denied: 1, allowed: 1, approvalTitle: "1 hold and 1 ticket, by " + SET, deniedTitle: "by agent-guardrails" },
    ],
    ["classifies nothing from a set that does not parse", PREVIEW, { [SET]: "spec: [unclosed" }, { approval: 2, denied: 0, allowed: 2, approvalTitle: "by " + SET, deniedTitle: "" }],
  ];
  it.each(cases)("%s", (_case, preview, texts, counts) => {
    expect(policySummary(preview, heldKinds(preview, texts))).toEqual(counts);
  });

  // Each case is a preview that gives the cell no counts, and its word.
  const words: [string, Record<string, PreviewEntry> | null | undefined, string][] = [
    ["says reading while the preview is out", undefined, "reading"],
    ["says the preview could not be read", null, "could not be read"],
    ["says the server is not running", { echo: entry("echo", "not_running") }, "the server is not running"],
    ["says no tool is known when the preview names none it counts", { echo: entry("echo", "matcher_miss") }, "no tool is known yet"],
  ];
  it.each(words)("%s", (_case, preview, word) => {
    expect(policySummary(preview)).toBe(word);
  });

  it("puts each held tool's window and who decides on the needs approval chip's hover", () => {
    const kinds = heldKinds(PREVIEW, { [SET]: OWN });
    expect(kinds["get-sum"]).toEqual({ pool: "sponsor", how: "hold", hold: 120, ticket: 86400, grant: 3600 });
    const counts = policySummary(PREVIEW, kinds) as PolicyCounts;
    expect(heldTitle(counts.approvalTitle, kinds)).toBe(
      [
        "1 hold and 1 ticket, by " + SET,
        "get-env: ticket within a day, then an hour to run, decided by the approver role sec-approvers",
        "get-sum: hold, up to 2 minutes, decided by " + SPONSOR_POOL,
      ].join("\n"),
    );
  });
});
