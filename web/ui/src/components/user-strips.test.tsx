import type * as React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserStrips } from "./user-strips";
import type { EntraAgent, LockRow, UserDetail } from "@/lib/api";
import { ENTRA_LISTS, NO_SPONSOR_CHIP, SPONSOR_FIX, opensAccount } from "@/lib/entra-words";
import { NONE } from "@/lib/words";
import { DETAIL, DISABLED_STRIP, LEFT_BY_DISABLE, LEFT_BY_ENTRA, LEFT_BY_IDM, LOCK_STRIP, assignedBanner, lockBy } from "@/lib/user-words";

const ago = new Date(Date.now() - 3 * 3600000).toISOString();
const counts = { sessions: 1, active_sessions: 1, devices: 0, approver_devices: 0, approvals: 0 };
const base: UserDetail = {
  id: "u1", username: "mara", status: "active", origin: "scim", kind: "human", created_at: ago, updated_at: ago, effective_roles: [], locks: [], sponsored_count: 0, counts,
};
const lock = (origin: string, reason: string): LockRow => ({ origin, reason, created_at: ago });

function draw(extra: Partial<UserDetail>, more: Partial<React.ComponentProps<typeof UserStrips>> = {}) {
  const props = { onUnlock: vi.fn(), onEnable: vi.fn(), onOpenSessions: vi.fn(), onDismiss: vi.fn() };
  render(<UserStrips user={{ ...base, ...extra }} entraOwned={false} assigned={null} sessions enabling={false} refused={null} {...props} {...more} />);
  return props;
}

const strips = (name: string) => screen.queryAllByRole("group", { name });

describe("the strips under a user's head", () => {
  it.each<[string, LockRow, string, string]>([
    ["an administrator", lock("admin", "incident 42"), lockBy("admin"), "incident 42"],
    ["an external system", lock("external", "SIEM rule 7"), lockBy("external"), "SIEM rule 7"],
    ["a disable on an older server that outlived it", lock("admin", "user disabled by admin"), lockBy("admin"), LEFT_BY_DISABLE],
    ["the identity manager on an active user", lock("scim", "deactivated via SCIM"), lockBy("scim"), LEFT_BY_IDM],
    ["a disable in Entra that outlived it", lock("entra", "the account is disabled in Entra"), lockBy("entra"), LEFT_BY_ENTRA],
  ])("shows a lock by %s with who, when and why", (_name, row, who, why) => {
    draw({ locks: [row] });
    const strip = strips(LOCK_STRIP)[0];
    expect(strip.textContent).toContain(who);
    expect(strip.textContent).toContain("3 h ago");
    expect((strip.querySelector("[data-lock-reason]") as HTMLElement).textContent).toBe(why);
  });

  it("offers Unlock on the first lock only", async () => {
    const { onUnlock } = draw({ locks: [lock("admin", "one"), lock("external", "two")] });
    expect(strips(LOCK_STRIP)).toHaveLength(2);
    expect(within(strips(LOCK_STRIP)[1]).queryByRole("button")).toBeNull();
    await userEvent.click(within(strips(LOCK_STRIP)[0]).getByRole("button", { name: "Unlock" }));
    expect(onUnlock).toHaveBeenCalled();
  });

  it.each<[string, LockRow[], string | null]>([
    ["the identity manager", [lock("scim", "deactivated via SCIM")], lockBy("scim")],
    ["an administrator", [lock("admin-disable", "user disabled by admin")], lockBy("admin")],
    ["an administrator on an older server", [lock("admin", "user disabled by admin")], lockBy("admin")],
    ["nobody the answer names", [], null],
  ])("shows a user disabled by %s, with Enable", async (_name, rows, who) => {
    const { onEnable } = draw({ status: "disabled", locks: rows });
    const strip = strips(DISABLED_STRIP)[0];
    expect(strips(LOCK_STRIP)).toHaveLength(0);
    if (who) expect(strip.textContent).toContain(who);
    else expect(strip.textContent).toBe("Enable");
    await userEvent.click(within(strip).getByRole("button", { name: "Enable" }));
    expect(onEnable).toHaveBeenCalled();
  });

  it.each<[string, LockRow[], number]>([
    ["no lock", [lock("admin-disable", "user disabled by admin")], 0],
    ["a lock", [lock("admin-disable", "user disabled by admin"), lock("admin", "incident 42")], 1],
  ])("shows an active user whose disable in Straza outlived the identity manager's enable as disabled by an administrator, with Enable, and %s", async (_name, rows, locks) => {
    const { onEnable } = draw({ status: "active", locks: rows });
    expect(strips(LOCK_STRIP)).toHaveLength(locks);
    const strip = strips(DISABLED_STRIP)[0];
    expect(strip.textContent).toContain(lockBy("admin"));
    await userEvent.click(within(strip).getByRole("button", { name: "Enable" }));
    expect(onEnable).toHaveBeenCalled();
  });

  it.each<[string, string]>([
    ["a new server", "admin-disable"],
    ["an older server", "admin"],
  ])("shows both strips for a user both locked and disabled, on %s", (_name, origin) => {
    draw({ status: "disabled", locks: [lock(origin, "user disabled by admin"), lock("admin", "incident 42")] });
    expect(strips(LOCK_STRIP)).toHaveLength(1);
    expect((strips(LOCK_STRIP)[0].querySelector("[data-lock-reason]") as HTMLElement).textContent).toBe("incident 42");
    expect(strips(DISABLED_STRIP)).toHaveLength(1);
  });

  it("shows the banner of a role just assigned with the door to the session, and dismisses it", async () => {
    const { onOpenSessions, onDismiss } = draw({}, { assigned: "audio-gen" });
    const banner = screen.getByRole("status");
    expect(banner.getAttribute("data-assigned")).toBe("audio-gen");
    expect(banner.textContent).toContain(assignedBanner("audio-gen", "mara"));
    await userEvent.click(within(banner).getByRole("button", { name: "Open mara's session" }));
    expect(onOpenSessions).toHaveBeenCalled();
    await userEvent.click(within(banner).getByRole("button", { name: "Dismiss" }));
    expect(onDismiss).toHaveBeenCalled();
  });

  it("leaves the door out of the banner with no active session", () => {
    draw({ counts: { ...counts, active_sessions: 0 } }, { assigned: "audio-gen" });
    expect(within(screen.getByRole("status")).getAllByRole("button").map((b) => b.getAttribute("aria-label") || b.textContent)).toEqual(["Dismiss"]);
  });

  it("shows a refused Enable with the server's sentence", () => {
    draw({ status: "disabled" }, { refused: { subject: "Enable", text: "Straza refused it: no. Fix what it names, then try again." } });
    expect(screen.getByRole("alert").textContent).toBe("Enable refused: no. Fix what it names, then try again.");
  });

  describe("of a person from Entra", () => {
    const ACCOUNT = "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/1b4e7c90";
    const ada: Partial<UserDetail> = { username: "ada", origin: "entra", external_id: "1b4e7c90", status: "disabled", effective_roles: ["finance-readers", "github-dev", "finance-approvers"] };
    const fromEntra = lock("entra", "the account is disabled in Entra");

    it("says the disable came from Entra, with the account's link, when and the roles kept, and offers no Enable", () => {
      draw({ ...ada, locks: [fromEntra] }, { entraOwned: true });
      expect(strips(LOCK_STRIP)).toHaveLength(0);
      const strip = strips(DISABLED_STRIP)[0];
      expect(strip.getAttribute("data-disabled-strip")).toBe("entra");
      const tag = within(strip).getByRole("link", { name: /Entra/ });
      expect(tag.getAttribute("href")).toBe(ACCOUNT);
      expect(tag.getAttribute("target")).toBe("_blank");
      expect(tag.getAttribute("title")).toBe(opensAccount("ada"));
      expect([...strip.querySelectorAll("a, span.rounded-pill")].map((e) => e.textContent)).toEqual(["Entra", "3 h ago", "3 roles kept"]);
      expect(within(strip).queryByRole("button")).toBeNull();
    });

    it("shows a lock by an administrator above the disable in Entra, each with its own strip", () => {
      draw({ ...ada, locks: [fromEntra, lock("admin", "Laptop reported stolen.")] }, { entraOwned: true });
      const locks = strips(LOCK_STRIP);
      expect(locks).toHaveLength(1);
      expect(locks[0].textContent).toContain(lockBy("admin"));
      expect((locks[0].querySelector("[data-lock-reason]") as HTMLElement).textContent).toBe("Laptop reported stolen.");
      expect(within(locks[0]).getByRole("button", { name: "Unlock" })).toBeTruthy();
      expect(strips(DISABLED_STRIP)[0].getAttribute("data-disabled-strip")).toBe("entra");
      expect(strips(DISABLED_STRIP)[0].textContent).not.toContain("locked");
    });

    it("keeps Enable for a disable read in Entra once Entra no longer owns the person", async () => {
      const { onEnable } = draw({ ...ada, locks: [fromEntra] });
      const strip = strips(DISABLED_STRIP)[0];
      expect(within(strip).getByRole("link", { name: /Entra/ })).toBeTruthy();
      await userEvent.click(within(strip).getByRole("button", { name: "Enable" }));
      expect(onEnable).toHaveBeenCalled();
    });

    it("leaves out the time and the roles it does not know", () => {
      draw({ ...ada, locks: [], effective_roles: [] }, { entraOwned: true });
      expect([...strips(DISABLED_STRIP)[0].querySelectorAll("a, span.rounded-pill")].map((e) => e.textContent)).toEqual(["Entra"]);
    });
  });

  describe("of an agent from Entra", () => {
    const AGENT_ID = "d3d3d3d3-0000-4000-8000-000000000003";
    const IDENTITY = "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/" + AGENT_ID;
    const BEN = "9d3b6e21-4f8a-4c17-a2e5-80c4f1b7d39a";
    const ADA = "1b4e7c90-5a2d-4f38-9e61-c7d0a3f5b812";
    const GROUP = "48e1b5d0-7c29-4f6e-b3a8-d915c06f2e47";
    const NO_USER = "f2a7c94e-1d63-4b08-9e5f-6b0d28a3c751";
    const agent: Partial<UserDetail> = { username: "lab-entra-born-agent", origin: "entra", kind: "nhi", user_type: "agent", entra_agent_id: AGENT_ID, effective_roles: ["lab-echo-callers"] };
    const fromEntra = lock("entra", "the agent identity is disabled in Entra");
    const sponsorStrip = () => strips(DETAIL.sponsor)[0];
    // facts are the strip's chips, tags and pills, and the line of what to do.
    const facts = (strip: HTMLElement) => [...strip.querySelectorAll("[data-tone], [data-entra-sponsor], span.rounded-pill, [data-sponsor-fix]")].map((e) => e.textContent);

    it.each<[string, EntraAgent, Record<string, string>, string[]]>([
      ["none", { identity: "enabled", sponsor_state: "none" }, {}, [NO_SPONSOR_CHIP, NONE, SPONSOR_FIX.none]],
      ["several", { identity: "enabled", sponsor_state: "several", sponsors: [{ id: BEN, kind: "user" }, { id: ADA, kind: "user" }] }, { [BEN]: "ben", [ADA]: "ada" }, [NO_SPONSOR_CHIP, "ben", "ada", SPONSOR_FIX.several]],
      ["not-person", { identity: "enabled", sponsor_state: "not-person", sponsors: [{ id: GROUP, kind: "group" }] }, {}, [NO_SPONSOR_CHIP, GROUP, SPONSOR_FIX["not-person"]]],
      ["no-user", { identity: "enabled", sponsor_state: "no-user", sponsors: [{ id: NO_USER, kind: "user" }] }, {}, [NO_SPONSOR_CHIP, NO_USER, SPONSOR_FIX["no-user"]]],
    ])("says why it has no sponsor when Entra lists %s, with what Entra lists and what to do", (state, entra, names, want) => {
      draw({ ...agent, entra }, { entraOwned: true, sponsorNames: names });
      const strip = sponsorStrip();
      expect(strip.getAttribute("data-sponsor-strip")).toBe(state);
      expect(strip.textContent).toContain(ENTRA_LISTS);
      expect(facts(strip)).toEqual(want);
      expect(strip.querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("warn");
    });

    it("opens each sponsor Entra lists in Entra: a person by name or ID, a group as a group", () => {
      draw({ ...agent, entra: { sponsor_state: "several", sponsors: [{ id: BEN, kind: "user" }, { id: GROUP, kind: "group" }] } }, { entraOwned: true, sponsorNames: { [BEN]: "ben" } });
      const links = within(sponsorStrip()).getAllByRole("link");
      expect(links.map((a) => a.getAttribute("href"))).toEqual([
        "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/" + BEN,
        "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/GroupDetailsMenuBlade/~/Members/groupId/" + GROUP,
      ]);
      expect(links.map((a) => a.querySelector("[title]")?.getAttribute("title"))).toEqual([opensAccount("ben"), opensAccount(GROUP)]);
      expect(links.every((a) => a.getAttribute("target") === "_blank")).toBe(true);
    });

    it("shows a state and a sponsor's type it does not know as the server sent them, with no link", () => {
      draw({ ...agent, entra: { sponsor_state: "orphaned", sponsors: [{ id: NO_USER, kind: "device" }] } }, { entraOwned: true });
      const strip = sponsorStrip();
      expect(facts(strip)).toEqual([NO_SPONSOR_CHIP, "device" + NO_USER, "orphaned"]);
      expect(within(strip).queryByRole("link")).toBeNull();
    });

    it("draws no sponsor strip while the agent has the sponsor Entra set", () => {
      draw({ ...agent, sponsor: "ben", entra: { identity: "enabled", agent_user: "enabled" } }, { entraOwned: true });
      expect(strips(DETAIL.sponsor)).toHaveLength(0);
    });

    it.each<[string, EntraAgent, string, string]>([
      ["its agent identity", { identity: "disabled", agent_user: "enabled" }, "agentIdentity", IDENTITY],
      ["its agent user", { identity: "enabled", agent_user: "disabled" }, "agentUser", ""],
    ])("says Entra disabled %s, with when and the role kept, and offers no Enable", (_name, entra, object, url) => {
      draw({ ...agent, status: "disabled", locks: [fromEntra], entra }, { entraOwned: true });
      const strip = strips(DISABLED_STRIP)[0];
      expect(strip.getAttribute("data-disabled-strip")).toBe("entra");
      expect([...strip.querySelectorAll("[data-entra-tag], [data-entra-object], span.rounded-pill")].map((e) => e.textContent)).toEqual(["Entra", object === "agentIdentity" ? "agent identity" : "agent user", "3 h ago", "1 role kept"]);
      expect(strip.querySelector("[data-entra-object]")?.getAttribute("data-entra-object")).toBe(object);
      // The answer carries the object ID of the agent identity only, so the
      // tag opens it when that is what Entra disabled and is plain otherwise.
      const tag = within(strip).queryByRole("link", { name: /Entra/ });
      expect(tag?.getAttribute("href") ?? "").toBe(url);
      if (tag) expect(tag.getAttribute("title")).toBe(opensAccount("lab-entra-born-agent"));
      expect(within(strip).queryByRole("button")).toBeNull();
    });
  });
});
