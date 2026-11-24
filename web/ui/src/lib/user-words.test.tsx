import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  GLANCE, agencyLine, givesTools, includesRoles, lastSeenLine, lockBy, ofTools, openSessions, originLine, phones, revokeAllBody, roleOn, rolesKept, sponsorsLine, throughRoles, toolsOn, whatAgent, whereFrom,
} from "./user-words";
import { needsArea } from "./say";

// originLine reads a stamp against a clock held at noon, so the day of
// every stamp is the same in any zone the suite runs in.
describe("originLine", () => {
  const NOW = Date.parse("2026-11-15T12:00:00Z");
  const before = (ms: number) => new Date(NOW - ms).toISOString();
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  it.each<[string, string | undefined, string | undefined, string]>([
    ["no stamp", "admin", undefined, "assigned here"],
    ["a bad stamp", "admin", "yesterday", "assigned here"],
    ["a stamp under a minute old", "admin", before(30000), "assigned here just now"],
    ["a stamp hours old", "admin", before(5 * 3600000), "assigned here 5 hours ago"],
    ["a stamp exactly a day old", "admin", before(86400000), "assigned here on 14 Nov 2026"],
    ["an older stamp", "admin", "2026-11-02T12:00:00Z", "assigned here on 2 Nov 2026"],
    ["no origin", undefined, "2026-11-02T12:00:00Z", "assigned here on 2 Nov 2026"],
    ["the identity manager's row hours old", "scim", before(5 * 3600000), "from your identity manager 5 hours ago"],
    ["the identity manager's older row", "scim", "2026-11-02T12:00:00Z", "from your identity manager on 2 Nov 2026"],
    ["the identity manager's row with no stamp", "scim", undefined, "from your identity manager"],
  ])("words %s", (_name, origin, at, want) => {
    expect(originLine(origin, at)).toBe(want);
  });
});

// The words of a user's page that are built from parts, read back whole.
describe("the words of a user's page", () => {
  it.each<[string, string, string]>([
    ["an autonomous agent made in Straza", whatAgent("autonomous", "local"), "Autonomous agent, made in Straza."],
    ["an agent with no agency from the identity manager", whatAgent(undefined, "scim"), "From your identity manager."],
    ["an agent with an agency Straza does not know", whatAgent("roaming", "local"), "Made in Straza."],
    ["a service", whereFrom("scim"), "From your identity manager."],
    ["a person from the identity manager", whereFrom("scim"), "From your identity manager."],
    ["a person made in Straza", whereFrom("local"), "Made in Straza."],
    ["a sponsor of one agent", sponsorsLine(1), "Sponsors 1 agent."],
    ["a sponsor of two agents", sponsorsLine(2), "Sponsors 2 agents."],
    ["the last check-in", lastSeenLine("4 minutes ago"), "Last seen 4 minutes ago."],
    ["a part the role may not read", needsArea("The servers and tools are", "apps"), "The servers and tools are not shown, because your role does not include MCP servers."],
    ["a lock by automation", lockBy("external"), "by an external system"],
    ["a row a disable in Entra left", lockBy("entra"), "by Entra"],
    ["the roles of a user disabled in Entra", rolesKept(3), "3 roles kept"],
    ["the one role of a user disabled in Entra", rolesKept(1), "1 role kept"],
    ["the agency of an agent from Entra", agencyLine("supervised") || "", "Supervised agent."],
    ["an agency Straza does not know, from Entra", agencyLine("roaming"), "AI agent."],
    ["no agency, from Entra", agencyLine(undefined), "AI agent."],
    ["a lock with an origin Straza does not know", lockBy("other"), "by an administrator"],
    ["one server tile", GLANCE.servers(1, 4), "server, 4 tools"],
    ["two servers and one tool", GLANCE.servers(2, 1), "servers, 1 tool"],
    ["a count of tools", ofTools(4, 21), "4 of 21 tools"],
    ["a count of one tool", ofTools(1, 1), "1 of 1 tool"],
    ["every tool of a server", toolsOn(9, 9, "demo-tools"), "every tool on demo-tools"],
    ["the gives line of two servers", givesTools([toolsOn(9, 14, "demo-tools"), toolsOn(3, 3, "github")]), "9 of 14 tools on demo-tools and every tool on github."],
    ["a business role that includes two", includesRoles(["a", "b"]), "Includes a and b."],
    ["a role on two servers", roleOn("application role", ["github", "midpoint"]), "application role on github and midpoint"],
    ["a role on no server", roleOn("business role", []), "business role"],
    ["a role through three", throughRoles(["a", "b", "c"]), "through a, b and c"],
    ["one session to open", openSessions("altair", 1), "Open altair's session"],
    ["two sessions to open", openSessions("alice", 2), "Open alice's sessions"],
    ["one session to revoke", revokeAllBody(1, "altair"), "Its one active session ends within seconds. altair can start a new one."],
    ["three sessions to revoke", revokeAllBody(3, "alice"), "All 3 active sessions end within seconds. alice can start a new one."],
    ["two phones", phones(2), "2 phones"],
  ])("reads %s", (_name, got, want) => {
    expect(got).toBe(want);
  });
});
