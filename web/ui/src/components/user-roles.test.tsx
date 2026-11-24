import type * as React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserRoles } from "./user-roles";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import type { RoleLine } from "@/lib/user-roles";
import { ENTRA_GROUP_URL } from "@/lib/entra-words";
import { IDM_OWNS, NO_ROLE } from "@/lib/user-words";
import { absTime, dayWord } from "@/lib/words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const direct = { id: "a1", subject_kind: "user", subject_id: "u", role_id: "r-gh", origin: "admin" };
const lines: RoleLine[] = [
  { key: "role:github-readers", name: "github-readers", role: { id: "r-gh", name: "github-readers", kind: "application" }, servers: ["github", "midpoint"], direct, window: "", through: [] },
  { key: "role:gone", name: "gone", role: null, servers: [], direct: null, window: "", through: [] },
];

function draw(extra: Partial<React.ComponentProps<typeof UserRoles>> = {}) {
  const props = { onAssign: vi.fn(), onRemove: vi.fn() };
  render(<UserRoles name="altair" scim={false} lines={lines} servers audit {...props} {...extra} />);
  return props;
}

describe("the Roles list", () => {
  it("names each role with its kind and servers, and removes a direct assignment behind the confirm", async () => {
    const { onRemove, onAssign } = draw();
    const row = document.querySelector("[data-role-row=github-readers]") as HTMLElement;
    expect(row.textContent).toBe("github-readersapplication role on github and midpointassigned hereRemove");
    expect((document.querySelector("[data-role-row=gone]") as HTMLElement).textContent).toBe("gone");
    await userEvent.click(screen.getByRole("button", { name: "Remove github-readers" }));
    expect(onRemove).toHaveBeenCalledWith(direct, "github-readers", false);
    await userEvent.click(screen.getByRole("button", { name: "Assign a role" }));
    expect(onAssign).toHaveBeenCalled();
    await userEvent.click(screen.getByRole("link", { name: "github-readers" }));
    expect(navigate).toHaveBeenCalledWith("roles", ["r-gh"]);
  });

  it("marks a role from Entra with its chip, and offers Change in Entra where no assignment was made here", () => {
    const fin = { id: "r-fin", name: "finance-readers", kind: "application", entra: { group_id: "g-fin", state: "linked" } };
    const entra = { ...direct, id: "e1", role_id: "r-fin", origin: "entra" };
    const { onRemove } = draw({ lines: [{ key: "role:finance-readers", name: "finance-readers", role: fin, servers: [], direct: null, entra, window: "", through: [] }] });
    const row = document.querySelector("[data-role-row=finance-readers]") as HTMLElement;
    expect(row.textContent).toBe("finance-readersapplication roleFrom EntraChange in Entra");
    const link = row.querySelector("[data-change-in-entra]") as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe(ENTRA_GROUP_URL("g-fin"));
    expect(link.getAttribute("target")).toBe("_blank");
    expect(screen.queryByRole("button", { name: "Remove finance-readers" })).toBeNull();
    expect(onRemove).not.toHaveBeenCalled();
  });

  it("removes only the assignment made here of a role also held from Entra", async () => {
    const entra = { ...direct, id: "e1", origin: "entra" };
    const { onRemove } = draw({ lines: [{ ...lines[0], entra }] });
    const row = document.querySelector("[data-role-row=github-readers]") as HTMLElement;
    expect(row.textContent).toBe("github-readersapplication role on github and midpointFrom Entraassigned hereRemove");
    expect(row.querySelector("[data-change-in-entra]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Remove github-readers" }));
    expect(onRemove).toHaveBeenCalledWith(direct, "github-readers", true);
  });

  it("leaves a role's servers out without the MCP servers area", () => {
    draw({ servers: false });
    expect((document.querySelector("[data-role-row=github-readers]") as HTMLElement).textContent).toBe("github-readersapplication roleassigned hereRemove");
  });

  it("says the day a direct assignment was made, with the whole stamp as its title", () => {
    const at = "2026-11-02T12:00:00Z";
    draw({ lines: [{ ...lines[0], direct: { ...direct, origin: "scim", created_at: at } }] });
    const line = screen.getByText("from your identity manager on " + dayWord(at));
    expect(line.getAttribute("title")).toBe(absTime(at));
    expect(dayWord(at)).toBe("2 Nov 2026");
  });

  it("says the time gone by on an assignment's first day", () => {
    draw({ lines: [{ ...lines[0], direct: { ...direct, created_at: new Date(Date.now() - 5 * 3600000).toISOString() } }] });
    expect(screen.getByText("assigned here 5 hours ago")).toBeTruthy();
  });

  it("says where an assignment with no stamp came from, with no time and no title", () => {
    draw();
    const line = screen.getByText("assigned here");
    expect(line.hasAttribute("title")).toBe(false);
  });

  it("says the identity manager owns a SCIM user's assignments", () => {
    draw({ scim: true });
    expect(screen.getByText(IDM_OWNS)).toBeTruthy();
  });

  it("says a user holds no role yet", () => {
    draw({ lines: [] });
    expect(screen.getByText(NO_ROLE)).toBeTruthy();
  });

  it("opens the user's audit trail through the hand-off box", async () => {
    draw();
    await userEvent.click(screen.getByRole("button", { name: "Audit chain of altair" }));
    const [key, rest, , before] = vi.mocked(navigate).mock.calls.at(-1) as [string, string[], boolean, () => void];
    expect([key, rest]).toEqual(["audit", []]);
    before();
    expect(take("audit")).toEqual({ user: "altair" });
  });

  it("draws no audit link for a role without the audit area", () => {
    draw({ audit: false });
    expect(screen.queryByRole("button", { name: "Audit chain of altair" })).toBeNull();
  });
});
