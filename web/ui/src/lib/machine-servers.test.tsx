import { describe, expect, it } from "vitest";
import { machineServers, onMachines, registeredAfter } from "./machine-servers";
import { ruleView } from "./policy-model";

// A server on a person's machine is an MCP server a rule names that is not
// straza and not registered. The registered list after a draft is published
// is the live one with the draft's servers put and removed.

const mcp = (apps?: string[]) => ruleView({ id: "r", tools: ["mcp.call"], ...(apps ? { apps } : {}), effect: "allow" }, 0);

describe("the servers on a person's machine", () => {
  const cases: [string, ReturnType<typeof mcp>, string[] | null, string[], boolean][] = [
    ["a server that is not registered", mcp(["elevenlabs"]), ["github"], ["elevenlabs"], true],
    ["a registered server", mcp(["github"]), ["github"], [], false],
    ["Straza's own tools", mcp(["straza"]), [], [], false],
    ["two servers, one registered", mcp(["github", "elevenlabs"]), ["github"], ["elevenlabs"], true],
    ["every server", mcp(), [], [], false],
    ["a shell rule", ruleView({ id: "s", tools: ["shell.exec"], command: { denyPatterns: ["rm *"] }, effect: "deny" }, 0), [], [], false],
    ["an unread list", mcp(["elevenlabs"]), null, [], false],
  ];
  it.each(cases)("reads %s", (_name, rule, registered, machines, on) => {
    expect(registered ? machineServers(rule, registered) : []).toEqual(machines);
    expect(onMachines(rule, registered)).toBe(on);
  });

  it("counts the draft's servers as registered after publishing, and its removals as gone", () => {
    const items = [
      { kind: "App" as const, name: "tracker", op: "put" as const },
      { kind: "App" as const, name: "old-tools", op: "remove" as const },
      { kind: "Role" as const, name: "tracker-readers", op: "put" as const },
    ];
    expect(registeredAfter(["github", "old-tools"], items).sort()).toEqual(["github", "tracker"]);
  });
});
