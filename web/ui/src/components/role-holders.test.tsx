import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RoleHolders } from "./role-holders";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type RoleRow, type UserRow, getUser, grantRole, listAssignments, listDevices, listUsers } from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { ENTRA_GROUP_URL, opensGroup } from "@/lib/entra-words";
import { DRIFT_SHORT, GRANT_ROLE, USER_MISSING, grantBody, grantedToast, nobodyHolds } from "@/lib/role-words";

const role: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver", holder_count: 2, assigned_count: 2 };

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
      <RoleHolders role={role} roles={[role]} grantOpen={grantOpen} onGrantOpenChange={onGrantOpenChange} onChanged={onChanged} />
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

  it("names a held role it knows as a tag with its kind's icon, and counts the rest in a pill", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [{ ...ivan, effective_roles: ["sec-approvers", "dev", "ops"] }, { ...judy, effective_roles: ["unknown-role"] }], next_cursor: "" });
    mount();
    const cell = (await screen.findByRole("button", { name: "Open ivan" })).querySelector('[data-column="roles"]') as HTMLElement;
    const tag = cell.querySelector('[title="sec-approvers"]') as HTMLElement;
    expect(tag.textContent).toBe("sec-approvers");
    expect(tag.querySelector("svg")?.getAttribute("class")).toContain("lucide-user-check");
    const more = cell.querySelector("[data-more-roles]") as HTMLElement;
    expect(more.textContent).toBe("+2");
    expect(more.className).toContain("rounded-pill");
    expect(more.className).not.toContain("border");
    // A role the page holds no row for keeps a plain tag.
    const other = screen.getByRole("button", { name: "Open judy" }).querySelector('[title="unknown-role"]') as HTMLElement;
    expect(other.querySelector("svg")).toBeNull();
  });

  it("carries each holder's kind chip under the name while people and agents hold the role, and none while one kind does", async () => {
    const agent: UserRow = { ...ivan, id: "u-born", username: "lab-entra-born-agent", display: "", kind: "nhi", user_type: "agent", origin: "entra" };
    vi.mocked(listUsers).mockResolvedValue({ items: [ivan, agent], next_cursor: "" });
    mount();
    await screen.findByRole("button", { name: "Open lab-entra-born-agent" });
    const chip = (user: string) => document.querySelector(`[data-user="${user}"] [data-column="name"] [data-kind-chip]`);
    expect(chip("ivan")?.textContent).toBe("Person");
    expect(chip("lab-entra-born-agent")?.textContent).toBe("AI agent");
    expect(chip("lab-entra-born-agent")?.getAttribute("data-tone")).toBe("teal");
  });

  it("leaves the kind chip out while one kind holds the role", async () => {
    mount();
    await screen.findByRole("button", { name: "Open ivan" });
    expect(document.querySelector("[data-column='name'] [data-kind-chip]")).toBeNull();
  });

  it("says who to ask when nobody holds the role", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    mount();
    expect((await screen.findByText(nobodyHolds("sec-approvers", true))).textContent).toBe("Nobody holds sec-approvers. Assign to a user above.");
  });

  it("opens the user's page from a row, and not a sheet in place", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open ivan" }));
    expect(navigate).toHaveBeenLastCalledWith("users", ["u-ivan"]);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens a sponsor's agents on the user's page", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [{ ...ivan, sponsored_count: 2 }, judy], next_cursor: "" });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Show the 2 agents ivan sponsors" }));
    expect(navigate).toHaveBeenLastCalledWith("users", ["u-ivan", "agents"]);
    expect(navigate).not.toHaveBeenCalledWith("users", ["u-ivan"]);
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
    expect((await screen.findByText(/the user is locked/)).textContent).toContain("Assign role refused: the user is locked.");
    expect(onGrantOpenChange).not.toHaveBeenCalledWith(false);
  });
});

describe("the Holders tab of a role with a group in Entra", () => {
  const fin: RoleRow = { id: "r-fin", name: "finance-readers", kind: "application", holder_count: 4 };
  const desk: RoleRow = { id: "r-desk", name: "finance-desk", kind: "business", implies: ["finance-readers"] };
  const user = (id: string, username: string): UserRow => ({ ...ivan, id, username, display: "", origin: "local", effective_roles: ["finance-readers"] });
  const asg = (id: string, subject: string, roleID: string, origin: string) => ({ id, subject_kind: "user", subject_id: subject, role_id: roleID, origin });
  const assignments = [
    asg("a1", "u-ada", "r-fin", "entra"), asg("a2", "u-ada", "r-fin", "admin"),
    asg("a3", "u-ben", "r-fin", "entra"), asg("a4", "u-cleo", "r-fin", "scim"), asg("a5", "u-dan", "r-desk", "admin"),
  ];
  const source = (name: string) => (screen.getByRole("button", { name: "Open " + name }).querySelector('[data-column="source"]') as HTMLElement);

  beforeEach(() => {
    vi.mocked(listUsers).mockResolvedValue({ items: [user("u-ada", "ada"), user("u-ben", "ben"), user("u-cleo", "cleo"), user("u-dan", "dan")], next_cursor: "" });
  });

  it("says where each holder's role comes from, and counts a holder from Entra and by hand once", async () => {
    render(
      <TooltipProvider>
        <RoleHolders role={fin} roles={[fin, desk]} grantOpen={false} onGrantOpenChange={onGrantOpenChange} onChanged={onChanged} source={{ assignments, groupID: "g-fin", group: "Straza: finance-readers" }} />
      </TooltipProvider>,
    );
    await screen.findByRole("button", { name: "Open ada" });
    expect(screen.getAllByRole("button", { name: "Open ada" })).toHaveLength(1);
    expect(screen.getByText("4 holders")).toBeTruthy();
    expect(source("ada").textContent).toBe("From Entraremove in Entraassigned here");
    expect(source("ben").textContent).toBe("From Entraremove in Entra");
    expect(source("cleo").textContent).toBe("from your identity manager");
    expect(source("dan").textContent).toBe("through finance-desk");
    const link = source("ben").querySelector("[data-remove-in-entra]") as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe(ENTRA_GROUP_URL("g-fin"));
    expect(link.getAttribute("title")).toBe(opensGroup("Straza: finance-readers"));
    await userEvent.click(link);
    expect(navigate).not.toHaveBeenCalledWith("users", ["u-ben"]);
  });

  it("keeps the user's origin in the Source column without the assignments", async () => {
    render(<TooltipProvider><RoleHolders role={fin} roles={[fin]} grantOpen={false} onGrantOpenChange={onGrantOpenChange} onChanged={onChanged} /></TooltipProvider>);
    await screen.findByRole("button", { name: "Open ada" });
    expect(source("ada").textContent).toBe("Straza");
  });
});
