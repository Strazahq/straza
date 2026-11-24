import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { KindChip, RoleTag, SponsorName, StateChips, Waiting } from "./user-parts";
import type { LockRow } from "@/lib/api";
import { navigate } from "@/lib/router";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const lock = (origin: string, reason: string): LockRow => ({ origin, reason, created_at: "2026-11-14T09:00:00Z" });
const states = () => [...document.querySelectorAll("[data-user-status]")].map((e) => e.getAttribute("data-user-status"));

describe("the parts of a user's page", () => {
  it.each<[string, { status: string; locks: LockRow[] }, string[]]>([
    ["an active user", { status: "active", locks: [] }, ["active"]],
    ["a disabled user, not drawn locked for its own disable's row", { status: "disabled", locks: [lock("admin-disable", "user disabled by admin")] }, ["disabled"]],
    ["a disabled user of an older server, not drawn locked for its own disable's row", { status: "disabled", locks: [lock("admin", "user disabled by admin")] }, ["disabled"]],
    ["a disabled user who is also locked by an administrator", { status: "disabled", locks: [lock("admin-disable", "user disabled by admin"), lock("admin", "incident")] }, ["disabled", "locked"]],
    ["an active user whose disable in Straza outlived the identity manager's enable", { status: "active", locks: [lock("admin-disable", "user disabled by admin")] }, ["disabled"]],
    ["a locked user", { status: "active", locks: [lock("external", "SIEM")] }, ["locked"]],
    ["a user both disabled and locked", { status: "disabled", locks: [lock("scim", "deactivated via SCIM"), lock("admin", "incident")] }, ["disabled", "locked"]],
    ["a status Straza does not know", { status: "pending", locks: [] }, ["pending"]],
  ])("reads the state of %s", (_name, user, want) => {
    render(<StateChips user={user} />);
    expect(states()).toEqual(want);
  });

  it.each<[string, { kind: string; user_type?: string }, string, string]>([
    ["an AI agent", { kind: "nhi", user_type: "agent" }, "AI agent", "teal"],
    ["a service", { kind: "nhi", user_type: "service" }, "Service", "plain"],
    ["a person", { kind: "human" }, "Person", "plain"],
  ])("names %s with its kind chip", (_name, user, word, tone) => {
    render(<KindChip user={user} />);
    const chip = document.querySelector("[data-kind-chip]") as HTMLElement;
    expect(chip.textContent).toBe(word);
    expect(chip.getAttribute("data-tone")).toBe(tone);
  });

  it("links a sponsor whose id was found, and keeps one not found as plain text", async () => {
    const { unmount } = render(<SponsorName name="alice" id={null} />);
    expect(screen.queryByRole("link")).toBeNull();
    unmount();
    render(<SponsorName name="alice" id="u-alice" />);
    await userEvent.click(screen.getByRole("link", { name: "alice" }));
    expect(navigate).toHaveBeenCalledWith("users", ["u-alice"]);
  });

  it("links a role the catalog holds, and names one it lacks with the Roles icon", () => {
    render(<><RoleTag name="dev" role={{ id: "r-dev", name: "dev", kind: "business" }} /><RoleTag name="gone" role={null} /></>);
    expect(screen.getByRole("link", { name: "dev" }).getAttribute("href")).toContain("roles/r-dev");
    expect(screen.queryByRole("link", { name: "gone" })).toBeNull();
  });

  it("says a read is out, then that it failed, and nothing once it answered or when it is off", () => {
    const { rerender } = render(<Waiting read={{ kind: "loading" }} word="Reading." subject="The thing" />);
    expect(screen.getByRole("status").textContent).toBe("Reading.");
    rerender(<Waiting read={{ kind: "failed", message: "It failed." }} word="Reading." subject="The thing" />);
    expect(screen.getByRole("status").textContent).toBe("The thing could not be read. It failed.");
    rerender(<Waiting read={{ kind: "ready", data: 1 }} word="Reading." subject="The thing" />);
    expect(screen.queryByRole("status")).toBeNull();
    rerender(<Waiting read={{ kind: "off" }} word="Reading." subject="The thing" />);
    expect(screen.queryByRole("status")).toBeNull();
  });
});
