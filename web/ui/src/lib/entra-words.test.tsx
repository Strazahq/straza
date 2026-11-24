import { describe, expect, it } from "vitest";
import { ENTRA_GROUP_URL, byReason, entraAccountURL, entraObjectURL, forLong, groupName, kindWord, reasonFix, reasonWord, staleLine, stateWord } from "./entra-words";

const NOW = Date.parse("2026-11-17T12:00:00Z");
const ago = (s: number) => new Date(NOW - s * 1000).toISOString();

describe("the words of a role's group in Entra", () => {
  it("words a known state, type and reason, and shows an unknown one as the server sent it", () => {
    expect([stateWord("missing"), stateWord("held"), stateWord("not-owner"), stateWord("released")]).toEqual(["group deleted", "removals held", "not owner", "released"]);
    expect([kindWord("agentUser"), kindWord("agentIdentity"), kindWord("servicePrincipal"), kindWord("device")]).toEqual(["agent user", "agent identity", "application", "device"]);
    expect([reasonWord("not-read"), reasonWord("name-taken"), reasonWord("not-agent"), reasonWord("unread")]).toEqual(["kind not read", "name taken", "not an agent", "type and ID only"]);
    // The server sends none of these any more, so each reads as sent.
    expect([reasonWord("no-user"), reasonWord("disabled"), reasonWord("agent")]).toEqual(["no-user", "disabled", "agent"]);
  });

  it("says what to do for a kind the deployment does not read, by the kinds under it", () => {
    expect(reasonFix("not-read", ["user", "user"])).toBe("identity.entra.read leaves out people. Add people there, or remove the member in Entra.");
    expect(reasonFix("not-read", ["agentUser", "agentIdentity"])).toBe("identity.entra.read leaves out agent users and agent identities. Add agentUsers and agentIdentities there, or remove the member in Entra.");
    expect(reasonFix("not-read", ["other"])).toBe("");
    expect(reasonFix("disabled", ["user"])).toBe("");
    expect(reasonFix("name-taken", ["user"])).toBe("");
    expect(reasonFix("not-agent", ["servicePrincipal"])).toBe("");
  });

  it("groups members by reason in the order each first shows", () => {
    const m = (id: string, reason: string) => ({ id, kind: "user", reason });
    expect(byReason([m("a", "not-agent"), m("b", "name-taken"), m("c", "not-agent")]).map(([r, ms]) => [r, ms.map((x) => x.id)])).toEqual([["not-agent", ["a", "c"]], ["name-taken", ["b"]]]);
  });

  it("names the group with the config's prefix, an empty one included, and by its ID without the config", () => {
    expect(groupName("finance-readers", "g1", { group_name_prefix: "Straza: " })).toBe("Straza: finance-readers");
    expect(groupName("finance-readers", "g1", { group_name_prefix: "" })).toBe("finance-readers");
    expect(groupName("finance-readers", "g1", null)).toBe("g1");
    expect(groupName("finance-readers", "", null)).toBe("finance-readers");
  });

  it("opens the group's members in the Entra admin centre", () => {
    expect(ENTRA_GROUP_URL("7d1e4b2a")).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_IAM/GroupDetailsMenuBlade/~/Members/groupId/7d1e4b2a");
  });

  it.each<[string, string, string, string]>([
    ["a person", "user", "1b4e7c90", "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/1b4e7c90"],
    ["an agent user", "agentUser", "0e93b6c4", "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/0e93b6c4"],
    ["an agent identity", "agentIdentity", "3f6a9d27", "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/3f6a9d27"],
    ["an application", "servicePrincipal", "2e7c4a19", "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/2e7c4a19"],
    ["a type the console does not know", "device", "a41b7e09", ""],
    ["an account with no object ID", "user", "", ""],
  ])("opens %s in the Entra admin centre", (_name, type, id, want) => {
    expect(entraAccountURL(type, id)).toBe(want);
  });

  it("opens a group Entra lists as an object, and any other object as its account", () => {
    expect(entraObjectURL("group", "48e1b5d0")).toBe(ENTRA_GROUP_URL("48e1b5d0"));
    expect(entraObjectURL("user", "9d3b6e21")).toBe(entraAccountURL("user", "9d3b6e21"));
    expect(entraObjectURL("other", "9d3b6e21")).toBe("");
    expect(entraObjectURL("group", "")).toBe("");
  });

  it("says how long a read failed in whole units, at least a minute", () => {
    expect([forLong(ago(20), NOW), forLong(ago(150), NOW), forLong(ago(3 * 3600 + 60), NOW), forLong(ago(26 * 3600), NOW), forLong(ago(3 * 86400), NOW)]).toEqual(["1 minute", "2 minutes", "3 hours", "1 day", "3 days"]);
  });

  it("ends the server's sentence with a stop before what holders keep", () => {
    expect(staleLine(new Date(Date.now() - 7200_000).toISOString(), "Microsoft Graph answered a request with HTTP 503")).toBe("Straza has not read Entra for 2 hours: Microsoft Graph answered a request with HTTP 503. Holders stay as last read.");
    expect(staleLine(new Date(Date.now() - 7200_000).toISOString(), undefined)).toBe("Straza has not read Entra for 2 hours. Holders stay as last read.");
  });
});
