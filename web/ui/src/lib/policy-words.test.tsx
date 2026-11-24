import { describe, expect, it } from "vitest";
import { ALLOW_HELP, INTENT, SERVERS_NARROWED_HELP, WHERE_WORD, decisionCall, offBody, serversNarrowed } from "./policy-words";

// The words of a policy's call types, of the New policy flow and of the
// Decisions tab's Call column, against the terms the console uses.

describe("the policy words", () => {
  it("names the call types of a policy rule in words", () => {
    expect(WHERE_WORD.shell).toBe("shell command");
    expect(WHERE_WORD.files).toBe("file");
    expect(WHERE_WORD.net).toBe("network");
  });

  it("words the New policy flow with policy rule and allowed", () => {
    expect(INTENT.allow.line).toBe("Allows a call the deployment denies by default, such as a shell command or an MCP server on a person's machine.");
    expect(ALLOW_HELP).toBe("Under the enterprise profile a local tool that no policy rule allows is denied, so shell, file and network access for a role starts with an allow. So does a harness tool Straza does not classify, and on every profile the tools of an MCP server on a person's own machine. A registered server's tool is different: it runs when a role has access to it, and a policy only denies it or makes it need approval.");
    expect(serversNarrowed("dev", 2, 5)).toBe("dev reaches 2 of 5 servers.");
    expect(SERVERS_NARROWED_HELP).toBe("A policy rule for a server the role cannot reach never applies, because the gateway needs both the access row and the policy. A role reaches a server when it is made on that server, and Edit access on its page changes which tools it reaches there.");
  });

  it("names a shell command a decision ran as a shell command", () => {
    expect(decisionCall({ tool: "shell.exec", command: "date" })).toBe("shell command: date");
  });

  it.each([
    ["names neither lane of a policy that holds neither", false, [], "Its 1 denial stops applying."],
    ["names MCP calls only where the policy gates them", true, [], "Its 1 denial stops applying. MCP calls they covered are allowed where a role has access."],
    ["names the local lanes the policy allows, once each", false, ["shell", "files", "shell"], "Its 1 denial stops applying. Shell and file calls it allowed are denied again under the enterprise profile."],
    ["reads a mixed lane as local", false, ["tools"], "Its 1 denial stops applying. Local calls it allowed are denied again under the enterprise profile."],
  ])("says what Turn off changes: %s", (_case, gatesMcp, allowed, want) => {
    const p = { deny: 1, hum: 0, allow: 0, hold: 0, ticket: 0, confirm: 0, check: 0 };
    expect(offBody(p, [], false, gatesMcp, allowed)).toBe(want);
  });
});
