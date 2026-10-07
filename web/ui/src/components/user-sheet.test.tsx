import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserSheet } from "./user-sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  type AssignmentRow, type DeviceRow, type RoleRow, type UserDetail, type UserRow,
  getUser, grantRole, keyPosture, listAssignments, listDevices, listUsers, lockUser, removeKey, revokeAssignment, revokeDevice, setKey, setUserStatus, unlockUser,
} from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import {
  DRIFT_LINE, GRANT_MISSING, IMPLIED_LINE, KEY_HINT, KEY_LABEL, KEY_MISSING, NO_DEVICE, NO_KEY_LINE, REASON_LABEL, REASON_MISSING, SUBJECT_DEVICES,
  deadRowBody, deviceRevokeBody, disableBody, driftBody, grantBody, keyRevokeBody, lockBody, lockedToast, originLine, revokeRoleBody, unlockBody,
} from "@/lib/user-words";
import { absTime } from "@/lib/words";

const LOCK = { origin: "admin", reason: "incident 42: token pasted in a public channel", created_at: "2026-09-11T14:02:00Z" };

const carol: UserRow = {
  id: "u-carol", username: "carol", display: "Carol <b>Reyes</b>", email: "carol@corp.example",
  status: "active", origin: "scim", kind: "human", external_id: "3f1a",
  created_at: "2026-07-02T10:00:00Z", updated_at: "2026-09-09T10:00:00Z",
  effective_roles: ["dev"], locks: [], last_seen: "2026-09-09T11:20:00Z", sponsored_count: 1,
};
const locked: UserRow = { ...carol, locks: [LOCK] };
const dave: UserRow = {
  id: "u-dave", username: "dave", display: "Dave Okafor", status: "disabled", origin: "local", kind: "human",
  created_at: "2026-06-01T10:00:00Z", updated_at: "2026-08-02T10:00:00Z",
  effective_roles: [], locks: [], sponsored_count: 0,
};
const nina: UserRow = {
  id: "u-nina", username: "nina-research-agent", status: "active", origin: "scim", kind: "nhi",
  user_type: "agent", sponsor: "alice", agency_mode: "supervised",
  created_at: "2026-09-12T08:12:00Z", updated_at: "2026-09-12T08:12:00Z",
  effective_roles: [], locks: [], sponsored_count: 0,
};
const sam: UserRow = {
  id: "u-sam", username: "sam-sre-agent", status: "active", origin: "scim", kind: "nhi", user_type: "agent",
  sponsor: "carol", created_at: "2026-08-01T10:00:00Z", updated_at: "2026-09-12T08:51:00Z",
  effective_roles: ["sre"], locks: [], last_seen: "2026-09-12T08:51:00Z", sponsored_count: 0,
};

const counts = { sessions: 4, active_sessions: 0, devices: 1, approver_devices: 1, approvals: 2 };
const detailOf = (u: UserRow, extra: Partial<UserDetail> = {}): UserDetail => ({ ...u, counts, ...extra });

const roles: RoleRow[] = [
  { id: "r-dev", name: "dev", kind: "application" },
  { id: "r-sre", name: "sre", kind: "application" },
];
// carol holds dev from the identity provider and an sre grant whose window
// has closed, the row the resolver already ignores.
const grants: AssignmentRow[] = [
  { id: "as1", subject_kind: "user", subject_id: "u-carol", role_id: "r-dev", origin: "scim" },
  { id: "as2", subject_kind: "user", subject_id: "u-carol", role_id: "r-sre", origin: "admin", valid_to: "2026-01-01T00:00:00Z" },
];
const laptop: DeviceRow = { id: "d1", user_id: "u-carol", name: "carol-laptop", fingerprint: "SHA256:ab12", platform: "macos", status: "active", enrolled_at: "2026-08-14T09:00:00Z" };

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getUser: vi.fn(), listAssignments: vi.fn(), listDevices: vi.fn(), keyPosture: vi.fn(), listUsers: vi.fn(),
  lockUser: vi.fn(), unlockUser: vi.fn(), setUserStatus: vi.fn(), grantRole: vi.fn(), revokeAssignment: vi.fn(),
  revokeDevice: vi.fn(), setKey: vi.fn(), removeKey: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const refusal = (message: string, status: number) => Object.assign(new Error(message), { status, unreachable: false });

// open renders the sheet the way the list does and waits for the detail
// read, which is what the numbers come from.
async function open(user: UserRow, rolesIn: RoleRow[] = roles) {
  const onChanged = vi.fn();
  const onOpenChange = vi.fn();
  const onFilterSessions = vi.fn();
  render(
    <TooltipProvider>
      <UserSheet user={user} roles={rolesIn} open={true} onOpenChange={onOpenChange} onChanged={onChanged} onFilterSessions={onFilterSessions} />
    </TooltipProvider>,
  );
  await waitFor(() => expect(document.querySelector("[data-glance]")).not.toBeNull());
  return { onChanged, onOpenChange, onFilterSessions };
}

const sheet = () => screen.getByRole("dialog");
const ask = () => screen.getByRole("alertdialog");
const box = (attr: string) => document.querySelector(attr) as HTMLElement;
const row = (label: string) => document.querySelector('[data-state-row="' + label + '"]') as HTMLElement;
const section = (title: string) => document.querySelector('[data-section="' + title + '"]') as HTMLElement;

describe("the user sheet", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getUser).mockResolvedValue(detailOf(carol));
    vi.mocked(listAssignments).mockResolvedValue(grants);
    vi.mocked(listDevices).mockResolvedValue([laptop]);
    vi.mocked(listUsers).mockResolvedValue({ items: [sam], next_cursor: "" });
    vi.mocked(keyPosture).mockResolvedValue({ user_id: "u-nina", registered: false });
  });

  it("reads the user as state rows, facts, numbers and lists", async () => {
    vi.mocked(getUser).mockResolvedValue(detailOf(locked));
    await open(locked);
    expect(within(sheet()).getByRole("heading", { level: 2 }).textContent).toContain("carol");
    // A display name that carries markup is text, never markup.
    expect(sheet().textContent).toContain("Carol <b>Reyes</b>");
    expect(sheet().querySelector("[data-slot=sheet-description] b")).toBeNull();

    expect(row("Straza lock").textContent).toContain("Locked by Straza.");
    expect(row("Straza lock").textContent).toContain(LOCK.reason);
    expect(within(row("Straza lock")).getByRole("button", { name: "Unlock user" })).toBeTruthy();
    expect(within(row("Status")).getByRole("button", { name: "Disable user" })).toBeTruthy();
    expect(row("Status").textContent).toContain("active");

    const facts = box("[data-facts]").textContent || "";
    expect(facts).toContain("carol@corp.example");
    expect(facts).toContain(originLine(carol));
    expect(facts).toContain(absTime(carol.created_at));
    expect(facts).toContain("1 agent");

    expect(box("[data-glance]").textContent).toContain("4 sessions, 0 active");
    expect(box("[data-glance]").textContent).toContain("1 approver phone");
    expect(box("[data-glance]").textContent).toContain("2 approvals raised");

    expect(listUsers).toHaveBeenCalledWith("sponsor=carol&limit=50");
    expect(section("Sponsored agents").textContent).toContain("sam-sre-agent");
    expect(section("Sponsored agents").textContent).toContain("AI agent");
    expect(section("Roles").textContent).toContain("from your identity manager");
    expect(section("Roles").textContent).toContain("expired");
    expect(section("Roles").textContent).toContain(DRIFT_LINE);
    expect(section("Roles").textContent).toContain(IMPLIED_LINE);
    expect(section("Devices").textContent).toContain("carol-laptop");
    expect(section("Devices").textContent).toContain("enrolled " + absTime(laptop.enrolled_at));
    // No Assertion key section on a person.
    expect(section("Assertion key")).toBeNull();
  });

  it("locks the user only with a reason, and shows the lock it wrote", async () => {
    const { onChanged } = await open(carol);
    await userEvent.click(within(row("Straza lock")).getByRole("button", { name: "Lock user" }));
    expect(ask().textContent).toContain("Lock carol?");
    expect(ask().textContent).toContain(lockBody("carol"));

    // The primary stays clickable: an empty reason is answered at the field.
    (document.activeElement as HTMLElement).blur();
    await userEvent.click(within(ask()).getByRole("button", { name: "Lock user" }));
    expect(within(ask()).getByText(REASON_MISSING)).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText(REASON_LABEL));
    expect(lockUser).not.toHaveBeenCalled();

    vi.mocked(getUser).mockResolvedValue(detailOf(locked));
    await userEvent.type(screen.getByLabelText(REASON_LABEL), "incident 42");
    await userEvent.click(within(ask()).getByRole("button", { name: "Lock user" }));
    await waitFor(() => expect(lockUser).toHaveBeenCalledWith("u-carol", "incident 42"));
    await waitFor(() => expect(within(row("Straza lock")).getByRole("button", { name: "Unlock user" })).toBeTruthy());
    expect(notify.ok).toHaveBeenCalledWith(lockedToast("carol"));
    expect(onChanged).toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("unlocks and disables behind a confirm, and enables without one", async () => {
    vi.mocked(getUser).mockResolvedValue(detailOf(locked));
    await open(locked);
    await userEvent.click(within(row("Straza lock")).getByRole("button", { name: "Unlock user" }));
    expect(ask().textContent).toContain("Unlock carol?");
    expect(ask().textContent).toContain(unlockBody("carol"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Unlock user" }));
    await waitFor(() => expect(unlockUser).toHaveBeenCalledWith("u-carol"));

    await userEvent.click(within(row("Status")).getByRole("button", { name: "Disable user" }));
    expect(ask().textContent).toContain("Disable carol?");
    expect(ask().textContent).toContain(disableBody("carol"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Disable user" }));
    await waitFor(() => expect(setUserStatus).toHaveBeenCalledWith("u-carol", "disabled"));
  });

  it("enables a disabled user on the click, with no confirm in the way", async () => {
    vi.mocked(getUser).mockResolvedValue(detailOf(dave));
    vi.mocked(listDevices).mockResolvedValue([]);
    await open(dave);
    await userEvent.click(within(row("Status")).getByRole("button", { name: "Enable user" }));
    await waitFor(() => expect(setUserStatus).toHaveBeenCalledWith("u-dave", "active"));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(section("Devices").textContent).toContain(NO_DEVICE);
  });

  it("keeps the dialog open with the server's own sentence when a write is refused", async () => {
    vi.mocked(getUser).mockResolvedValue(detailOf(locked));
    await open(locked);
    vi.mocked(unlockUser).mockRejectedValue(refusal("user has an external lock that Straza cannot lift", 409));
    await userEvent.click(within(row("Straza lock")).getByRole("button", { name: "Unlock user" }));
    await userEvent.click(within(ask()).getByRole("button", { name: "Unlock user" }));
    const alert = await within(ask()).findByRole("alert");
    expect(alert.textContent).toContain("Unlock user refused.");
    expect(alert.textContent).toContain("The server refused it: user has an external lock that Straza cannot lift. Fix what it names, then try again.");
    expect(ask().textContent).toContain("Unlock carol?");
    expect(row("Straza lock").textContent).toContain("Locked by Straza.");
  });

  it("grants a role the user does not hold, and says the grant is drift on a SCIM user", async () => {
    await open(carol);
    // Nothing picked: the primary says what is missing at the picker.
    await userEvent.click(within(section("Roles")).getByRole("button", { name: "Assign role" }));
    expect(screen.getByText(GRANT_MISSING)).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("combobox", { name: "Assign" }));
    expect(grantRole).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("combobox", { name: "Assign" }));
    await userEvent.click(await screen.findByRole("option", { name: "sre" }));
    await userEvent.click(within(section("Roles")).getByRole("button", { name: "Assign role" }));
    expect(ask().textContent).toContain("Assign sre to carol?");
    expect(ask().textContent).toContain(grantBody("carol", "sre"));
    expect(ask().textContent).toContain(driftBody("sre", "assignment"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Assign role" }));
    await waitFor(() => expect(grantRole).toHaveBeenCalledWith("u-carol", "r-sre"));
  });

  it("revokes a grant, and says a row outside its window changes no access", async () => {
    await open(carol);
    await userEvent.click(within(section("Roles")).getByRole("button", { name: "Revoke dev" }));
    expect(ask().textContent).toContain("Revoke dev from carol?");
    expect(ask().textContent).toContain(revokeRoleBody("carol", "dev"));
    expect(ask().textContent).toContain(driftBody("dev", "revoke"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke role" }));
    await waitFor(() => expect(revokeAssignment).toHaveBeenCalledWith("as1"));

    await userEvent.click(within(section("Roles")).getByRole("button", { name: "Revoke sre" }));
    expect(ask().textContent).toContain(deadRowBody("sre", "expired"));
  });

  it("revokes a device behind its confirm and reads the list again", async () => {
    await open(carol);
    expect(listDevices).toHaveBeenCalledTimes(1);
    await userEvent.click(within(section("Devices")).getByRole("button", { name: "Revoke carol-laptop" }));
    expect(ask().textContent).toContain("Revoke carol-laptop?");
    expect(ask().textContent).toContain(deviceRevokeBody("carol", "carol-laptop"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke device" }));
    await waitFor(() => expect(revokeDevice).toHaveBeenCalledWith("u-carol", "d1"));
    await waitFor(() => expect(listDevices).toHaveBeenCalledTimes(2));
  });

  it("registers the assertion key of an agent that has none", async () => {
    vi.mocked(getUser).mockResolvedValue(detailOf(nina, { counts: { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 }, nhi_key_registered: false }));
    vi.mocked(listAssignments).mockResolvedValue([]);
    vi.mocked(listDevices).mockResolvedValue([]);
    await open(nina);
    const key = section("Assertion key");
    expect(key.textContent).toContain("no key");
    expect(key.textContent).toContain(NO_KEY_LINE);
    expect(keyPosture).toHaveBeenCalledWith("u-nina");

    await userEvent.click(within(key).getByRole("button", { name: "Register key" }));
    expect(screen.getByText(KEY_HINT)).toBeTruthy();
    const field = screen.getByLabelText(KEY_LABEL);
    (document.activeElement as HTMLElement).blur();
    await userEvent.click(within(section("Assertion key")).getByRole("button", { name: "Register key" }));
    expect(screen.getByText(KEY_MISSING)).toBeTruthy();
    expect(document.activeElement).toBe(field);
    expect(setKey).not.toHaveBeenCalled();

    await userEvent.type(field, "bo9pMuhh0gS0qLSCTMmpEJMzHbG3uJ6rXR2n5WZP0aM=");
    await userEvent.click(within(section("Assertion key")).getByRole("button", { name: "Register key" }));
    await waitFor(() => expect(setKey).toHaveBeenCalledWith("u-nina", "bo9pMuhh0gS0qLSCTMmpEJMzHbG3uJ6rXR2n5WZP0aM="));
  });

  it("revokes a registered key behind its confirm", async () => {
    vi.mocked(getUser).mockResolvedValue(detailOf(nina, { counts: { sessions: 1, active_sessions: 1, devices: 0, approver_devices: 0, approvals: 0 }, nhi_key_registered: true }));
    vi.mocked(listAssignments).mockResolvedValue([]);
    vi.mocked(listDevices).mockResolvedValue([]);
    vi.mocked(keyPosture).mockResolvedValue({ user_id: "u-nina", registered: true, fingerprint: "SHA256:9f2c", created: "2026-09-12T08:20:00Z" });
    await open(nina);
    const key = section("Assertion key");
    expect(key.textContent).toContain("registered");
    expect(key.textContent).toContain("SHA256:9f2c");
    await userEvent.click(within(key).getByRole("button", { name: "Revoke key" }));
    expect(ask().textContent).toContain("Revoke the assertion key of nina-research-agent?");
    expect(ask().textContent).toContain(keyRevokeBody("nina-research-agent"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke key" }));
    await waitFor(() => expect(removeKey).toHaveBeenCalledWith("u-nina"));
  });

  it("keeps the rest of the sheet when one read fails", async () => {
    vi.mocked(listDevices).mockRejectedValue(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    await open(carol);
    await waitFor(() => expect(section("Devices").textContent).toContain(SUBJECT_DEVICES + ": unreachable, state unknown."));
    expect(section("Devices").textContent).toContain("The devices could not be read because strazad did not answer.");
    expect(section("Roles").textContent).toContain("dev");
    expect(box("[data-facts]").textContent).toContain("carol@corp.example");
  });

  it("opens the audit trail and closes from the footer", async () => {
    const { onOpenChange } = await open(carol);
    await userEvent.click(within(sheet()).getByRole("button", { name: "Open the audit trail" }));
    expect(navigate).toHaveBeenCalledWith("audit");
    // The sheet's own X carries the same word, so the footer button is the
    // one the kit rendered.
    const close = within(sheet()).getAllByRole("button", { name: "Close" }).filter((b) => b.getAttribute("data-slot") === "button");
    await userEvent.click(close[0]);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
