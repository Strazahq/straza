import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserGlance } from "./user-glance";
import type { ApprovalRow, UserDetail } from "@/lib/api";
import type { AccessRow } from "@/lib/user-access";

const counts = { sessions: 2, active_sessions: 1, devices: 0, approver_devices: 0, approvals: 0 };
const alice: UserDetail = {
  id: "u1", username: "alice", status: "active", origin: "scim", kind: "human", created_at: "", updated_at: "",
  effective_roles: ["a", "b", "c"], locks: [], sponsored_count: 2, counts,
};
const row = (server: string, allowed: string[], approval: string[] = []): AccessRow => ({ server, running: true, total: 9, allowed, approval, denied: [], through: [] });
const access = [row("github", ["x", "y", "z"], ["w"]), row("midpoint", ["a"])];
const requests = [{ id: "apr-1" }, { id: "apr-2" }] as ApprovalRow[];

function draw(user: UserDetail, a: AccessRow[] | null = access, r: ApprovalRow[] | null = requests) {
  const props = { onRoles: vi.fn(), onTab: vi.fn(), onRequests: vi.fn() };
  render(<UserGlance user={user} access={a} requests={r} {...props} />);
  return props;
}

const tiles = () => [...document.querySelectorAll("[data-glance]")].map((b) => b.getAttribute("data-glance") + ": " + b.textContent);

describe("the glance tiles", () => {
  it("draws each tile as a number over its words", () => {
    draw(alice);
    expect(tiles()).toEqual(["roles: 3 roles", "servers: 2 servers, 5 tools", "sessions: 1 active session", "requests: 2 requests waiting", "agents: 2 agents sponsored"]);
    expect(screen.getByRole("button", { name: "2 servers, 5 tools" })).toBeTruthy();
  });

  it("leaves out a tile whose number is zero and one whose read is not there", () => {
    draw({ ...alice, effective_roles: ["a"], sponsored_count: 0, counts: { ...counts, active_sessions: 0 } }, null, []);
    expect(tiles()).toEqual(["roles: 1 role"]);
  });

  it("draws no agents tile for an agent", () => {
    draw({ ...alice, kind: "nhi", user_type: "agent" }, null, null);
    expect(tiles()).toEqual(["roles: 3 roles", "sessions: 1 active session"]);
  });

  it("opens the part each tile counts", async () => {
    const { onRoles, onTab, onRequests } = draw(alice);
    await userEvent.click(screen.getByRole("button", { name: "3 roles" }));
    expect(onRoles).toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "2 servers, 5 tools" }));
    expect(onTab).toHaveBeenLastCalledWith("access");
    await userEvent.click(screen.getByRole("button", { name: "1 active session" }));
    expect(onTab).toHaveBeenLastCalledWith("sessions");
    await userEvent.click(screen.getByRole("button", { name: "2 agents sponsored" }));
    expect(onTab).toHaveBeenLastCalledWith("agents");
    await userEvent.click(screen.getByRole("button", { name: "2 requests waiting" }));
    expect(onRequests).toHaveBeenCalledWith(requests);
  });
});
