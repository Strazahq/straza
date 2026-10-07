import { describe, expect, it } from "vitest";
import type { DraftPrincipal } from "./api";
import { changeCounts } from "./drafts-model";
import {
  ackBar,
  ackWords,
  changesLede,
  decidedWords,
  doorWords,
  goLive,
  groupLine,
  keepDraft,
  keepLive,
  needLine,
  outcomeWords,
  pickWas,
  proposerWords,
  revisionLine,
  signLine,
  typedWrong,
  widenCount,
} from "./drafts-words";
import { DETAIL } from "@/test/drafts-fixture";

// The sentences of the Drafts area that are built from values: each one a
// full sentence in the console's own words.

const p = (over: Partial<DraftPrincipal>): DraftPrincipal => ({ user_id: "u", username: "carol", agent: false, via: "session", client: "console", ...over });

describe("who drafted it", () => {
  const cases: [string, DraftPrincipal, string | undefined, { name: string; line: string }][] = [
    ["an agent the reader sponsors", p({ username: "joe", agent: true, sponsor: "alice" }), undefined, { name: "joe", line: "an agent, sponsored by you" }],
    ["an agent someone else sponsors", p({ username: "sam", agent: true, sponsor: "bob" }), undefined, { name: "sam", line: "an agent, sponsored by bob" }],
    ["an agent with no sponsor on record", p({ username: "sam", agent: true }), undefined, { name: "sam", line: "an agent" }],
    ["the reader", p({ username: "alice" }), undefined, { name: "alice", line: "you" }],
    ["an admin API token", p({ username: "ci-bundles", via: "api-token" }), undefined, { name: "ci-bundles", line: "an admin API token" }],
    ["another person", p({}), undefined, { name: "carol", line: "" }],
    ["a file of the apps directory", p({ username: "strazad", via: "file" }), "/etc/straza/apps/demo-tools.yaml", { name: "the file demo-tools.yaml", line: "no person" }],
    ["the upgrade's conversion", p({ username: "strazad", via: "upgrade" }), undefined, { name: "Straza's upgrade", line: "a saved edit moved into a draft" }],
  ];
  it.each(cases)("%s", (_name, who, source, want) => {
    expect(proposerWords(who, "alice", source)).toEqual(want);
  });

  it("names every door in words", () => {
    expect(["console", "strazactl", "straza-app", "apps-directory", "api"].map(doorWords)).toEqual(["the console", "strazactl", "the straza app", "the apps directory", "the API"]);
  });
});

describe("the sentences of the page", () => {
  it("counts what changes and what goes live", () => {
    const counts = changeCounts(DETAIL.draft.items);
    expect(changesLede(counts)).toBe("1 new MCP server, 1 changed MCP server, 1 MCP server removed, 1 new role, 2 changed roles, 1 new approval set and 1 changed approval set. They go live together or not at all.");
    expect(goLive(counts)).toBe("Publishing adds 1 MCP server, 1 role and 1 approval set, changes 1 MCP server, 2 roles and 1 approval set and removes 1 MCP server in one step, or nothing changes.");
    expect(goLive([{ kind: "App", what: "removed", n: 1 }])).toBe("Publishing removes 1 MCP server in one step, or nothing changes.");
    expect(goLive([{ kind: "PolicySet", what: "off", n: 1 }])).toBe("Publishing turns off 1 approval set in one step, or nothing changes.");
    expect(goLive([{ kind: "Role", what: "changed", n: 1 }])).toBe("Publishing changes 1 role in one step, or nothing changes.");
  });

  const acks: [string, string | undefined, string | undefined, string][] = [
    ["a tick", "tick", undefined, "Tick it when you publish: republishing the old state undoes it."],
    ["a typed line", "typed", "api.githubcopilot.com", "Type api.githubcopilot.com when you publish: republishing the old state cannot undo it."],
    ["a typed line cut for the reader", "typed", undefined, "Only someone who can read what this line names can acknowledge it."],
  ];
  it.each(acks)("words how %s is acknowledged", (_name, ack, typed, want) => {
    expect(ackWords(ack, typed)).toBe(want);
  });

  it("words the bar and the counts in the singular and the plural", () => {
    expect(ackBar(1, 0)).toBe("1 line needs your acknowledgment when you publish.");
    expect(ackBar(3, 2)).toBe("3 lines need your acknowledgment when you publish.");
    expect(ackBar(0, 2)).toBe("Nothing to acknowledge. Read the warnings first.");
    expect(ackBar(0, 0)).toBe("Nothing to acknowledge.");
    expect(widenCount(1)).toBe("1 widens access");
    expect(widenCount(3)).toBe("3 widen access");
  });

  it("words a pick's values, an unset one included", () => {
    expect(pickWas("5")).toBe("It was 5 when the draft was checked.");
    expect(pickWas("")).toBe("It was unset when the draft was checked.");
    expect(keepDraft("")).toBe("Keep the draft's value, unset");
    expect(keepLive("10")).toBe("Keep live's value, 10");
  });

  it("names the text a wrong typed line needs", () => {
    expect(typedWrong("App/github", "api.githubcopilot.com")).toBe("The text typed for the server github is not api.githubcopilot.com. Type api.githubcopilot.com exactly to acknowledge it.");
  });

  it("words the other lines built from values", () => {
    expect(needLine("App/github", "the scope apps:write or the role straza-global-mcp-admin.")).toBe("the server github needs the scope apps:write or the role straza-global-mcp-admin.");
    expect(revisionLine(3, "alice", "console", "10:49", true)).toBe("rev 3 by alice in the console at 10:49, checked again against live state");
    expect(outcomeWords("needs-approval", "a hold, up to 10 minutes, decided by sec-approvers")).toBe("needs approval: a hold, up to 10 minutes, decided by sec-approvers");
    expect(outcomeWords("not-reachable")).toBe("not reachable");
    expect(groupLine("demo-tools-readers", "demo-tools", ["sam-sre-agent"], 1)).toBe("demo-tools-readers on demo-tools · held by sam-sre-agent");
    expect(groupLine("demo-tools-readers", "demo-tools", undefined, 3)).toBe("demo-tools-readers on demo-tools · 3 holders");
    expect(groupLine("github-readers", "github", [], 0)).toBe("github-readers on github · held by nobody");
    expect(signLine("joe")).toBe("You sponsor joe, which proposed this draft. The publish records you, the console and your login, so a borrowed login would show.");
    expect(decidedWords("published", "alice")).toBe("Published by alice");
    expect(decidedWords("expired", undefined)).toBe("Expired");
  });
});
