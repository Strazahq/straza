import { describe, expect, it } from "vitest";
import type { ConfigAnswer, LockRow, PreviewAnswer, UserRow } from "./api";
import { byDisable, entraAccount, entraOwns, orderedRoles, refusal, sourceKind, sourceWord, tileStatus, userKind, userState } from "./user-look";

// The rows every writer of a user revocation leaves, with what each writes.
// A disable in Straza writes the origin admin-disable, and an older server
// wrote admin with the disable's own reason.
const disable: LockRow = { origin: "admin-disable", reason: "user disabled by admin", created_at: "2026-11-01T10:00:00Z" };
const olderDisable: LockRow = { ...disable, origin: "admin" };
const scim: LockRow = { origin: "scim", reason: "deactivated via SCIM", created_at: "2026-11-01T10:00:00Z" };
const lock: LockRow = { origin: "admin", reason: "incident 42", created_at: "2026-11-02T10:00:00Z" };
const external: LockRow = { origin: "external", reason: "the SIEM flagged it", created_at: "2026-11-03T10:00:00Z" };
const entra: LockRow = { origin: "entra", reason: "the account is disabled in Entra", created_at: "2026-11-04T10:00:00Z" };

type State = Pick<UserRow, "status" | "locks">;

describe("userKind", () => {
  it.each<[string, Pick<UserRow, "kind" | "user_type">, string]>([
    ["a human with no type", { kind: "human" }, "person"],
    ["an agent by type", { kind: "nhi", user_type: "agent" }, "agent"],
    ["a non-human with no type", { kind: "nhi" }, "agent"],
    ["a service by type", { kind: "nhi", user_type: "service" }, "service"],
    ["a service even when born human", { kind: "human", user_type: "service" }, "service"],
  ])("reads %s", (_, u, want) => {
    expect(userKind(u)).toBe(want);
  });
});

describe("byDisable", () => {
  it.each<[string, LockRow, boolean]>([
    ["a disable in Straza", disable, true],
    ["a disable in Straza on an older server", olderDisable, true],
    ["a deactivation by the identity manager", scim, true],
    ["a disable read in Entra", entra, true],
    ["an admin's lock", lock, false],
    ["a lock from automation", external, false],
    ["automation that types the disable's reason", { ...external, reason: disable.reason }, false],
  ])("tells %s", (_, l, want) => {
    expect(byDisable(l)).toBe(want);
  });
});

describe("userState and tileStatus", () => {
  it.each<[string, State, { disabled: boolean; locks: LockRow[] }, "locked" | "disabled" | undefined]>([
    ["an active user", { status: "active", locks: [] }, { disabled: false, locks: [] }, undefined],
    ["an active user with no locks field", { status: "active" } as State, { disabled: false, locks: [] }, undefined],
    ["a locked user", { status: "active", locks: [lock] }, { disabled: false, locks: [lock] }, "locked"],
    ["a disabled user with its own row", { status: "disabled", locks: [disable] }, { disabled: true, locks: [] }, "disabled"],
    ["a user the identity manager deactivated", { status: "disabled", locks: [scim] }, { disabled: true, locks: [] }, "disabled"],
    ["a user disabled in Entra", { status: "disabled", locks: [entra] }, { disabled: true, locks: [] }, "disabled"],
    ["a user disabled in Entra and locked", { status: "disabled", locks: [entra, lock] }, { disabled: true, locks: [lock] }, "locked"],
    ["a disabled user who is also locked", { status: "disabled", locks: [disable, lock] }, { disabled: true, locks: [lock] }, "locked"],
    ["an active user whose disable's row outlived the identity manager's enable", { status: "active", locks: [disable] }, { disabled: true, locks: [] }, "disabled"],
    ["such a user who is also locked", { status: "active", locks: [disable, lock] }, { disabled: true, locks: [lock] }, "locked"],
    ["an active user whose disable's row outlived it on an older server", { status: "active", locks: [olderDisable] }, { disabled: false, locks: [olderDisable] }, "locked"],
    ["a disabled user of an older server", { status: "disabled", locks: [olderDisable] }, { disabled: true, locks: [] }, "disabled"],
    ["a disabled and locked user of an older server", { status: "disabled", locks: [olderDisable, lock] }, { disabled: true, locks: [lock] }, "locked"],
  ])("reads %s", (_, u, state, tile) => {
    expect(userState(u)).toEqual(state);
    expect(tileStatus(u)).toBe(tile);
  });
});

describe("refusal", () => {
  it.each<[string, State, PreviewAnswer["refused"], PreviewAnswer["refused"]]>([
    ["an active user", { status: "active", locks: [] }, undefined, undefined],
    ["a disabled user", { status: "disabled", locks: [disable] }, undefined, "disabled"],
    ["a locked user", { status: "active", locks: [lock] }, undefined, "revoked"],
    ["an active user whose disable's row outlived the identity manager's enable", { status: "active", locks: [disable] }, "revoked", "disabled"],
    ["an active user whose disable's row outlived it on an older server", { status: "active", locks: [olderDisable] }, undefined, "revoked"],
    ["a disabled user who is also locked", { status: "disabled", locks: [disable, lock] }, undefined, "disabled"],
    ["an active user the preview calls revoked", { status: "active", locks: [] }, "revoked", "revoked"],
    ["a locked user the preview calls disabled", { status: "active", locks: [lock] }, "disabled", "disabled"],
    ["a disabled user the preview calls revoked", { status: "disabled", locks: [] }, "revoked", "disabled"],
  ])("reads %s", (_, u, served, want) => {
    expect(refusal(u, served)).toBe(want);
  });
});

describe("sourceWord and sourceKind", () => {
  it.each<[string | undefined, string, string | undefined]>([
    ["scim", "Identity manager", "from-idm"],
    ["local", "Straza", "from-straza"],
    ["entra", "Entra", "from-entra"],
    [undefined, "Straza", "from-straza"],
    ["okta", "okta", undefined],
  ])("words the origin %s", (origin, word, kind) => {
    expect(sourceWord(origin)).toBe(word);
    expect(sourceKind(origin)).toBe(kind);
  });
});

describe("entraOwns and entraAccount", () => {
  const ada = { origin: "entra", kind: "human", external_id: "1b4e7c90" };
  const people: ConfigAnswer = { entra: { read: ["people", "agentIdentities"] } };
  const agent = { origin: "entra", kind: "nhi", user_type: "agent", entra_agent_id: "d3d3d3d3-0000-4000-8000-000000000003" };
  it.each<[string, Pick<UserRow, "origin" | "kind" | "user_type" | "external_id" | "entra_agent_id">, ConfigAnswer | undefined, boolean]>([
    ["a person from Entra while Entra owns people", ada, people, true],
    ["a person from Entra before the configuration is read", ada, undefined, true],
    ["a person from Entra once Entra no longer owns people", ada, { entra: { read: ["agentIdentities"] } }, false],
    ["a person from Entra with the Entra section off", ada, {}, false],
    ["a person from the identity manager", { ...ada, origin: "scim" }, people, false],
    ["a person made in Straza", { ...ada, origin: "local" }, people, false],
    ["an agent from Entra while Entra owns agent identities", agent, people, true],
    ["an agent from Entra while Entra owns agent users only", agent, { entra: { read: ["people", "agentUsers"] } }, true],
    ["an agent from Entra before the configuration is read", agent, undefined, true],
    ["an agent from Entra once Entra owns people only", agent, { entra: { read: ["people"] } }, false],
    ["an agent from the identity manager", { ...agent, origin: "scim" }, people, false],
    ["a service of origin entra, a kind Entra does not own", { ...agent, user_type: "service" }, people, false],
  ])("reads %s", (_name, u, config, want) => {
    expect(entraOwns(u, config)).toBe(want);
  });

  it("links a person from Entra to the account and an agent from Entra to its agent identity, and nobody else", () => {
    expect(entraAccount(ada)).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/1b4e7c90");
    expect(entraAccount({ ...ada, external_id: undefined })).toBe("");
    expect(entraAccount({ ...ada, origin: "scim" })).toBe("");
    expect(entraAccount(agent)).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/d3d3d3d3-0000-4000-8000-000000000003");
    expect(entraAccount({ ...agent, entra_agent_id: undefined, external_id: "1b4e7c90" })).toBe("");
    expect(entraAccount({ ...agent, origin: "local" })).toBe("");
    expect(entraAccount({ ...agent, user_type: "service" })).toBe("");
  });
});

describe("orderedRoles", () => {
  it.each<[string, string[] | undefined, string[]]>([
    ["no roles", undefined, []],
    ["an empty list", [], []],
    ["the server's order without the admin role", ["dev", "sre"], ["dev", "sre"]],
    ["the admin role first", ["dev", "straza-admin", "sec-approvers"], ["straza-admin", "dev", "sec-approvers"]],
  ])("orders %s", (_, roles, want) => {
    expect(orderedRoles(roles)).toEqual(want);
  });

  it("leaves the server's list as it was", () => {
    const roles = ["dev", "straza-admin"];
    orderedRoles(roles);
    expect(roles).toEqual(["dev", "straza-admin"]);
  });
});
