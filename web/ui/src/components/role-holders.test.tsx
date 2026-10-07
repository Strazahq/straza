import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RoleHolders } from "./role-holders";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type RoleRow, type UserRow, getUser, grantRole, listAssignments, listDevices, listUsers } from "@/lib/api";
import { notify } from "@/lib/notify";
import { DRIFT_SHORT, GRANT_ROLE, USER_MISSING, grantBody, grantedToast, nobodyHolds } from "@/lib/role-words";

const role: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver", holder_count: 2, assigned_count: 2 };
const catalog: RoleRow[] = [role, { id: "r-dev", name: "dev", kind: "business" }];

const ivan: UserRow = {
  id: "u-ivan", username: "ivan", display: "Ivan Petrov", status: "active", origin: "scim", kind: "human",
  created_at: "2026-07-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z",
  effective_roles: ["sec-approvers"], locks: [], last_seen: "2026-09-03T10:00:00Z", sponsored_count: 0,
};
const judy: UserRow = { ...ivan, id: "u-judy", username: "judy", display: "Judy Lam", effective_roles: ["sec-approvers"] };
const carol: UserRow = { ...ivan, id: "u-carol", username: "carol", display: "Carol Reyes", effective_roles: [] };
const local: UserRow = { ...carol, id: "u-glass", username: "break-glass", display: "", origin: "local" };

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listUsers: vi.fn(), getUser: vi.fn(), grantRole: vi.fn(),
  listAssignments: vi.fn(), listDevices: vi.fn(), keyPosture: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const onChanged = vi.fn();
const onGrantOpenChange = vi.fn();
const mount = (grantOpen = false) =>
  render(
    <TooltipProvider>
      <RoleHolders role={role} roles={catalog} grantOpen={grantOpen} onGrantOpenChange={onGrantOpenChange} onChanged={onChanged} />
    </TooltipProvider>,
  );

// pick types a name into the dialog's picker and takes the row it finds.
async function pick(username: string, found: UserRow) {
  vi.mocked(listUsers).mockImplementation(async (q: string) => (q.includes("q=" + username) ? { items: [found], next_cursor: "" } : { items: [ivan, judy], next_cursor: "" }));
  await userEvent.type(screen.getByRole("combobox", { name: "User" }), username);
  await userEvent.click(await screen.findByRole("option", { name: new RegExp(username) }));
}

describe("the Holders tab", () => {
  beforeEach(() => {
    vi.mocked(listUsers).mockResolvedValue({ items: [ivan, judy], next_cursor: "" });
    vi.mocked(getUser).mockResolvedValue({ ...carol, counts: { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 } });
    vi.mocked(listAssignments).mockResolvedValue([]);
    vi.mocked(listDevices).mockResolvedValue([]);
    vi.mocked(grantRole).mockResolvedValue({ id: "as1", subject_kind: "user", subject_id: "u-carol", role_id: "r-sec" });
    onChanged.mockClear();
    onGrantOpenChange.mockClear();
    vi.mocked(notify.ok).mockClear();
  });

  it("asks the server for the holders of this role and renders them", async () => {
    mount();
    await screen.findByRole("button", { name: "Open ivan" });
    expect(listUsers).toHaveBeenCalledWith("role=sec-approvers&sort=name&order=asc&limit=100");
    expect(screen.getByRole("button", { name: "Open judy" })).toBeTruthy();
    expect(within(screen.getByRole("button", { name: "Open ivan" })).getByText("Ivan Petrov")).toBeTruthy();
  });

  it("says who to ask when nobody holds the role", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    mount();
    expect((await screen.findByText(nobodyHolds("sec-approvers"))).textContent).toBe(nobodyHolds("sec-approvers"));
  });

  it("opens the person's own sheet from a row", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open ivan" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("ivan")).toBeTruthy();
  });

  it("grants the role to a user the picker found, and warns where the provider masters it", async () => {
    mount(true);
    await screen.findByRole("dialog");
    await pick("carol", carol);
    expect(await screen.findByText(grantBody("approver", "carol", "sec-approvers"))).toBeTruthy();
    await waitFor(() => expect(document.querySelector("[data-drift]")).toBeTruthy());
    expect((document.querySelector("[data-drift]") as HTMLElement).textContent).toContain(DRIFT_SHORT);

    await userEvent.click(screen.getByRole("button", { name: GRANT_ROLE }));
    await waitFor(() => expect(grantRole).toHaveBeenCalledWith("u-carol", "r-sec"));
    expect(notify.ok).toHaveBeenCalledWith(grantedToast("carol", "sec-approvers"));
    expect(onChanged).toHaveBeenCalled();
  });

  it("leaves the drift line off a user born in Straza itself", async () => {
    vi.mocked(getUser).mockResolvedValue({ ...local, counts: { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 } });
    mount(true);
    await screen.findByRole("dialog");
    await pick("break-glass", local);
    expect(await screen.findByText(grantBody("approver", "break-glass", "sec-approvers"))).toBeTruthy();
    expect(document.querySelector("[data-drift]")).toBeNull();
  });

  it("names the missing field instead of posting when no user is picked", async () => {
    mount(true);
    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("button", { name: GRANT_ROLE }));
    expect(await screen.findByText(USER_MISSING)).toBeTruthy();
    expect(grantRole).not.toHaveBeenCalled();
  });

  it("keeps the dialog open on a refusal and quotes the server", async () => {
    vi.mocked(grantRole).mockRejectedValue(new ApiError("the user is locked", 409));
    mount(true);
    await screen.findByRole("dialog");
    await pick("carol", carol);
    await userEvent.click(screen.getByRole("button", { name: GRANT_ROLE }));
    expect((await screen.findByText(/the user is locked/)).textContent).toContain("The server refused it: the user is locked.");
    expect(onGrantOpenChange).not.toHaveBeenCalledWith(false);
  });
});
