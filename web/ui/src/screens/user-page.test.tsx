import { beforeEach, describe, expect, it, vi } from "vitest";
import { keepsToast, removeBothBody } from "@/lib/entra-words";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserPage } from "./user-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  type AssignmentRow, type BindingRow, type RoleRow, type UserDetail, type UserRow,
  catalogPreview, getConfig, getUser, grantRole, listApprovals, listAssignments, listBindings, listDevices, listRoles, listSessions, listTools, listUsers, keyPosture, lockUser, revokeAssignment, revokeUserSessions, setUserStatus, userPreview,
} from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { adminAreas, snapshot } from "@/lib/session";
import { OWN_SESSION_WARNING, revokedWords } from "@/lib/session-words";
import {
  ACCESS_HELP, BACK_TO_USERS, NO_SUCH_USER, READING_USER, SUBJECT_ACCESS, SUBJECT_USER, USER_TAB, assignedBanner, enabledToast, lockBody, lockedToast, revokeAllBody, revokeAllTitle, stillLockedToast,
} from "@/lib/user-words";
import { absTime, dayWord } from "@/lib/words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getUser: vi.fn(), listAssignments: vi.fn(), listRoles: vi.fn(), userPreview: vi.fn(), listBindings: vi.fn(), listTools: vi.fn(), listApprovals: vi.fn(),
  listUsers: vi.fn(), listSessions: vi.fn(), listDevices: vi.fn(), keyPosture: vi.fn(), lockUser: vi.fn(), unlockUser: vi.fn(), setUserStatus: vi.fn(),
  grantRole: vi.fn(), revokeAssignment: vi.fn(), revokeUserSessions: vi.fn(), revokeDevice: vi.fn(), removeKey: vi.fn(), setKey: vi.fn(), catalogPreview: vi.fn(),
  getConfig: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), adminAreas: vi.fn(), snapshot: vi.fn() }));
// The stand-in for the Test a call sheet closes the way the sheet does: it
// is gone, then it runs onClosed.
vi.mock("@/components/policy-test", () => ({
  PolicyTest: ({ open, prefill, onOpenChange, onClosed }: { open: boolean; prefill?: unknown; onOpenChange: (open: boolean) => void; onClosed?: () => void }) =>
    open ? (
      <>
        <div data-policy-test>{JSON.stringify(prefill)}</div>
        <button type="button" onClick={() => { onOpenChange(false); setTimeout(() => onClosed?.(), 0); }}>Close the test</button>
      </>
    ) : null,
}));
vi.mock("@/components/entra-link", () => ({ EntraLink: () => null }));

const minutesAgo = (n: number) => new Date(Date.now() - n * 60000).toISOString();
const counts = { sessions: 3, active_sessions: 1, devices: 0, approver_devices: 0, approvals: 0 };
const altair: UserDetail = {
  id: "u-altair", username: "altair", display: "Altair (coding agent)", status: "active", origin: "local", kind: "nhi", user_type: "agent",
  agency_mode: "autonomous", sponsor: "alice", created_at: "2026-11-02T10:00:00Z", updated_at: "2026-11-02T10:00:00Z",
  effective_roles: ["github-readers"], locks: [], last_seen: minutesAgo(4), sponsored_count: 0, counts,
};
const alice: UserRow = {
  id: "u-alice", username: "alice", display: "Alice Admin", status: "active", origin: "scim", kind: "human",
  created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z", effective_roles: ["straza-admin"], locks: [], last_seen: minutesAgo(1), sponsored_count: 2,
};
const roles: RoleRow[] = [
  { id: "r-gh", name: "github-readers", kind: "application" },
  { id: "r-audio", name: "audio-gen", kind: "application", description: "Speech." },
  { id: "r-admin", name: "straza-admin", kind: "straza", areas: ["full"] },
];
const bindings: BindingRow[] = [{ id: "b1", app: "github", role: "github-readers", tools: ["list_issues", "create_pull_request"] }];
const grants: AssignmentRow[] = [{ id: "as1", subject_kind: "user", subject_id: "u-altair", role_id: "r-gh", origin: "admin" }];
const preview = { entries: [
  { app: "github", tool: "list_issues", status: "visible", reason: "" },
  { app: "github", tool: "create_pull_request", status: "approve_gated", reason: "" },
  { app: "github", tool: "delete_repo", status: "matcher_miss", reason: "" },
] };
const onMachine = (tool: string, status: string) => ({ app: "elevenlabs", tool, status, reason: "", setName: "audio-access" });
const unreachable = (message: string) => Object.assign(new Error(message), { status: 0, unreachable: true });
const refusal = (message: string, status: number) => Object.assign(new Error(message), { status, unreachable: false });

async function open(tab?: string, id = "u-altair") {
  const view = render(<TooltipProvider><UserPage id={id} tab={tab} /></TooltipProvider>);
  await screen.findByRole("heading", { level: 1, name: /Altair|Alice|Ada|altair|alice/ });
  return view;
}

const head = () => document.querySelector("[data-page-head]") as HTMLElement;
const ask = () => screen.getByRole("alertdialog");

// drawn waits until the page has drawn the element and answers it. A wait
// on a bare querySelector would end at once with null.
const drawn = (selector: string) => waitFor(() => {
  const el = document.querySelector<HTMLElement>(selector);
  if (!el) throw new Error(selector + " is not drawn yet");
  return el;
});

describe("the page of one user", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(adminAreas).mockReturnValue(null);
    vi.mocked(snapshot).mockReturnValue({ user: "admin", roles: [], grants: "full", expiresIn: 300, sessionID: "s-own" });
    vi.mocked(getUser).mockResolvedValue(altair);
    vi.mocked(listAssignments).mockResolvedValue(grants);
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(userPreview).mockResolvedValue(preview);
    vi.mocked(listBindings).mockResolvedValue(bindings);
    vi.mocked(listTools).mockResolvedValue([]);
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [{ id: "apr-1", user: "u-altair" }] as never });
    vi.mocked(listUsers).mockImplementation(async (q: string) => ({ items: q.startsWith("q=alice") ? [alice] : [], next_cursor: "" }));
    vi.mocked(listSessions).mockResolvedValue({ items: [{ id: "01a1da3d-0000-0000-0000-000000000001", user_id: "u-altair", harness: "claude-code", client_version: "2.2.0", attestation: "none", status: "active", started_at: minutesAgo(30), last_seen: minutesAgo(1) }], next_cursor: "" });
    vi.mocked(listDevices).mockResolvedValue([]);
    vi.mocked(keyPosture).mockResolvedValue({ user_id: "u-altair", registered: false });
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(getConfig).mockResolvedValue({ entra: { read: ["people"] } });
  });

  it("says it is reading the user under a head titled Users", async () => {
    vi.mocked(getUser).mockReturnValue(new Promise(() => {}));
    render(<TooltipProvider><UserPage id="u-altair" /></TooltipProvider>);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Users");
    expect(screen.getByRole("status").textContent).toBe(READING_USER);
    expect(listAssignments).not.toHaveBeenCalled();
  });

  it("says no user has the id on a 404, with the way back to the list, and no error box", async () => {
    vi.mocked(getUser).mockRejectedValue(refusal("no such user", 404));
    render(<TooltipProvider><UserPage id="u-gone" /></TooltipProvider>);
    expect((await screen.findByText(NO_SUCH_USER)).textContent).toBe(NO_SUCH_USER);
    expect(document.querySelector("[data-fetch-error]")).toBeNull();
    expect(screen.getByRole("link", { name: "Back to Users" }).hasAttribute("data-back-link")).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: BACK_TO_USERS }));
    expect(navigate).toHaveBeenCalledWith("users");
  });

  it("shows a failed read of the user as a read error with Reload now", async () => {
    vi.mocked(getUser).mockRejectedValueOnce(unreachable("down"));
    render(<TooltipProvider><UserPage id="u-altair" /></TooltipProvider>);
    expect((await screen.findByText(SUBJECT_USER + " could not be read")).textContent).toContain(SUBJECT_USER);
    await userEvent.click(screen.getByRole("button", { name: "Reload now" }));
    expect(await screen.findByRole("heading", { level: 1, name: "Altair (coding agent)" })).toBeTruthy();
  });

  it("keeps the rest of the page when one further read fails", async () => {
    vi.mocked(userPreview).mockRejectedValue(unreachable("down"));
    vi.mocked(listApprovals).mockRejectedValue(unreachable("down"));
    vi.mocked(listUsers).mockRejectedValue(unreachable("down"));
    await open();
    expect((await screen.findByText(SUBJECT_ACCESS + " could not be read")).textContent).toBeTruthy();
    await waitFor(() => expect(document.querySelector("[data-role-row=github-readers]")).not.toBeNull());
    expect(document.querySelector("[data-glance=requests]")).toBeNull();
    expect(document.querySelector("[data-glance=servers]")).toBeNull();
    expect(document.querySelector("[data-glance=roles]")).not.toBeNull();
    // The sponsor's lookup failed, so the name stays plain text.
    expect(head().querySelector("[data-sponsor=alice] a")).toBeNull();
  });

  it("reads the head of an AI agent with its sponsor as a link once the sponsor is found", async () => {
    await open();
    expect(head().textContent).toContain("Autonomous agent, made in Straza. Sponsored by alice. Last seen 4 minutes ago.");
    await waitFor(() => expect(head().querySelector("[data-sponsor=alice] a")).not.toBeNull());
    expect(listUsers).toHaveBeenCalledWith("q=alice&limit=20");
    await userEvent.click(within(head()).getByRole("link", { name: "alice" }));
    expect(navigate).toHaveBeenCalledWith("users", ["u-alice"]);
  });

  it.each<[string, string | undefined, string, string]>([
    ["users/<id>", undefined, USER_TAB.access, "data-user-access"],
    ["users/<id>/sessions", "sessions", USER_TAB.sessions, "data-user-sessions"],
    ["users/<id>/devices", "devices", USER_TAB.devices, "data-user-devices"],
    ["users/<id>/details", "details", USER_TAB.details, "data-user-details"],
    ["an unknown tab", "nowhere", USER_TAB.access, "data-user-access"],
    ["the Agents tab of a user who sponsors nobody", "agents", USER_TAB.access, "data-user-access"],
  ])("opens %s on its tab", async (_name, tab, label, attr) => {
    await open(tab);
    expect(screen.getByRole("tab", { selected: true }).textContent).toContain(label);
    await waitFor(() => expect(document.querySelector("[" + attr + "]")).not.toBeNull());
  });

  it("puts the tab in the address in place, and keeps the page mounted when it changes", async () => {
    const view = await open();
    await userEvent.click(screen.getByRole("tab", { name: /Details/ }));
    expect(navigate).toHaveBeenCalledWith("users", ["u-altair", "details"], true);
    await userEvent.click(screen.getByRole("tab", { name: /Sessions/ }));
    expect(navigate).toHaveBeenCalledWith("users", ["u-altair", "sessions"], true);
    view.rerender(<TooltipProvider><UserPage id="u-altair" tab="details" /></TooltipProvider>);
    expect(await screen.findByRole("tab", { selected: true, name: /Details/ })).toBeTruthy();
    expect(getUser).toHaveBeenCalledTimes(1);
  });

  it("shows the Agents tab with its count for a person who sponsors agents", async () => {
    vi.mocked(getUser).mockResolvedValue({ ...alice, counts });
    await open("agents", "u-alice");
    expect(screen.getByRole("tab", { selected: true }).textContent).toBe(USER_TAB.agents + "2");
    expect(listUsers).toHaveBeenCalledWith("sponsor=alice&limit=50");
  });

  it("locks from the Stop access menu only with a reason, then reads the page again", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Lock/ }));
    expect(ask().textContent).toContain(lockBody("altair"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Lock" }));
    expect(lockUser).not.toHaveBeenCalled();
    await userEvent.type(screen.getByLabelText("Reason, recorded on the audit chain"), "incident 42");
    await userEvent.click(within(ask()).getByRole("button", { name: "Lock" }));
    await waitFor(() => expect(lockUser).toHaveBeenCalledWith("u-altair", "incident 42"));
    expect(notify.ok).toHaveBeenCalledWith(lockedToast("altair"));
    await waitFor(() => expect(getUser).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("removes the assignment made here of a role also held from Entra, and says the user keeps it", async () => {
    vi.mocked(listAssignments).mockResolvedValue([...grants, { id: "e1", subject_kind: "user", subject_id: "u-altair", role_id: "r-gh", origin: "entra" }]);
    vi.mocked(revokeAssignment).mockResolvedValue({});
    await open();
    await userEvent.click(await screen.findByRole("button", { name: "Remove github-readers" }));
    expect(ask().textContent).toContain(removeBothBody("altair", "github-readers", "admin"));
    await userEvent.click(within(ask()).getByRole("button", { name: "Remove role" }));
    await waitFor(() => expect(revokeAssignment).toHaveBeenCalledWith("as1"));
    expect(notify.ok).toHaveBeenCalledWith(keepsToast("altair", "github-readers"));
  });

  it("shows the server's sentence when it refuses to remove an assignment from Entra", async () => {
    const sentence = "the assignment comes from the role's group in Microsoft Entra ID. Remove the user from that group";
    vi.mocked(revokeAssignment).mockRejectedValue(refusal(sentence, 409));
    await open();
    await userEvent.click(await screen.findByRole("button", { name: "Remove github-readers" }));
    await userEvent.click(within(ask()).getByRole("button", { name: "Remove role" }));
    expect((await within(ask()).findByRole("alert")).textContent).toContain(sentence);
  });

  it("puts the focus back on Stop access when a confirm from its menu closes", async () => {
    await open();
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /^Lock/ }));
    await screen.findByRole("alertdialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: /Stop access/ })));
  });

  it("ends every active session of the user in one call that names the user", async () => {
    vi.mocked(revokeUserSessions).mockResolvedValue({ revoked: 1 });
    await open();
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Revoke all sessions/ }));
    expect(ask().textContent).toContain(revokeAllTitle("altair"));
    expect(ask().textContent).toContain(revokeAllBody(1, "altair"));
    expect(ask().textContent).not.toContain(OWN_SESSION_WARNING);
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke session" }));
    await waitFor(() => expect(revokeUserSessions).toHaveBeenCalledWith("u-altair"));
    expect(notify.ok).toHaveBeenCalledWith(revokedWords(1));
  });

  it("warns that the console's own session ends on the reader's own page", async () => {
    vi.mocked(snapshot).mockReturnValue({ user: "altair", roles: [], grants: "full", expiresIn: 300, sessionID: "s-own" });
    await open();
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: /Revoke all sessions/ }));
    expect(ask().textContent).toContain(revokeAllBody(1, "altair") + " " + OWN_SESSION_WARNING);
  });

  describe("of a person Entra owns", () => {
    const ada: UserDetail = {
      ...altair, id: "u-ada", username: "ada", display: "Ada Lind", kind: "human", user_type: "human", origin: "entra", external_id: "1b4e7c90",
      agency_mode: undefined, sponsor: undefined,
    };
    const ACCOUNT = "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/1b4e7c90";
    const choices = () => [...document.querySelectorAll("[data-stop]")].map((e) => e.getAttribute("data-stop"));
    const openMenu = async () => {
      await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
      await screen.findByRole("menu");
    };

    it("names Entra in the head and sends Disable to Entra while the configuration lists people", async () => {
      vi.mocked(getUser).mockResolvedValue(ada);
      await open(undefined, "u-ada");
      expect(head().querySelector("[data-entra-tag]")?.textContent).toBe("Entra");
      expect(within(head()).getByRole("link", { name: /Open in Entra/ }).getAttribute("href")).toBe(ACCOUNT);
      await waitFor(() => expect(getConfig).toHaveBeenCalledTimes(1));
      await openMenu();
      expect(choices()).toEqual(["sessions", "lock", "entra"]);
      expect(screen.getByRole("menuitem", { name: "Disable in Entra" }).getAttribute("href")).toBe(ACCOUNT);
    });

    it("offers Disable once the configuration no longer lists people, and shows the server's sentence when it refuses", async () => {
      const sentence = "ada was not changed here, because the account comes from Microsoft Entra ID and Straza would write Entra's state back at its next read. Change the account in Entra. To stop ada at once, lock the account";
      vi.mocked(getUser).mockResolvedValue(ada);
      vi.mocked(getConfig).mockResolvedValue({ entra: { read: ["agentIdentities"] } });
      vi.mocked(setUserStatus).mockRejectedValue(refusal(sentence, 409));
      await open(undefined, "u-ada");
      await waitFor(() => expect(getConfig).toHaveBeenCalledTimes(1));
      await openMenu();
      await waitFor(() => expect(choices()).toEqual(["sessions", "lock", "disable"]));
      await userEvent.click(screen.getByRole("menuitem", { name: /^Disable/ }));
      await userEvent.click(within(ask()).getByRole("button", { name: "Disable" }));
      expect((await within(ask()).findByRole("alert")).textContent).toContain(sentence);
    });

    it("counts the person as Entra's for a reader who may not read the configuration", async () => {
      vi.mocked(adminAreas).mockReturnValue({ users: true, sessions: true });
      vi.mocked(getUser).mockResolvedValue(ada);
      await open(undefined, "u-ada");
      await openMenu();
      expect(choices()).toEqual(["sessions", "lock", "entra"]);
      expect(getConfig).not.toHaveBeenCalled();
    });

    it("shows a person disabled in Entra with no Enable", async () => {
      vi.mocked(getUser).mockResolvedValue({ ...ada, status: "disabled", locks: [{ origin: "entra", reason: "the account is disabled in Entra", created_at: minutesAgo(180) }] });
      await open(undefined, "u-ada");
      const strip = await drawn("[data-disabled-strip=entra]");
      expect(within(strip).queryByRole("button", { name: "Enable" })).toBeNull();
      expect(document.querySelector("[data-lock-strip]")).toBeNull();
    });

    it("reads the configuration only for a user from Entra", async () => {
      await open();
      expect(getConfig).not.toHaveBeenCalled();
    });
  });

  describe("of an agent from Entra", () => {
    const BEN = "9d3b6e21-4f8a-4c17-a2e5-80c4f1b7d39a";
    const ADA = "1b4e7c90-5a2d-4f38-9e61-c7d0a3f5b812";
    const born: UserDetail = {
      ...altair, id: "u-born", username: "lab-entra-born-agent", display: undefined, origin: "entra", agency_mode: undefined, sponsor: undefined,
      entra_agent_id: "d3d3d3d3-0000-4000-8000-000000000003",
      entra: { identity: "enabled", sponsor_state: "several", sponsors: [{ id: BEN, kind: "user" }, { id: ADA, kind: "user" }] },
    };
    const ben: UserRow = { ...alice, id: "u-ben", username: "ben", external_id: BEN, origin: "entra" };

    it("names the people Entra lists as sponsors by their Straza names, and one it does not find by its ID", async () => {
      vi.mocked(getUser).mockResolvedValue(born);
      vi.mocked(getConfig).mockResolvedValue({ entra: { read: ["people", "agentIdentities"] } });
      vi.mocked(listUsers).mockImplementation(async (q: string) => ({ items: q.startsWith("q=" + BEN) ? [ben] : [], next_cursor: "" }));
      render(<TooltipProvider><UserPage id="u-born" /></TooltipProvider>);
      const strip = await drawn("[data-sponsor-strip=several]");
      await waitFor(() => expect([...strip.querySelectorAll("[data-entra-sponsor]")].map((e) => e.textContent)).toEqual(["ben", ADA]));
      expect(listUsers).toHaveBeenCalledWith("q=" + BEN + "&limit=5");
      expect(listUsers).toHaveBeenCalledWith("q=" + ADA + "&limit=5");
      await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
      await screen.findByRole("menu");
      expect([...document.querySelectorAll("[data-stop]")].map((e) => e.getAttribute("data-stop"))).toEqual(["sessions", "lock", "entra"]);
    });

    it("looks nobody up for the other states, whose sponsors are no person Straza has", async () => {
      vi.mocked(getUser).mockResolvedValue({ ...born, entra: { identity: "enabled", sponsor_state: "no-user", sponsors: [{ id: ADA, kind: "user" }] } });
      render(<TooltipProvider><UserPage id="u-born" /></TooltipProvider>);
      const strip = await drawn("[data-sponsor-strip=no-user]");
      expect(strip.querySelector("[data-entra-sponsor]")?.textContent).toBe(ADA);
      expect(vi.mocked(listUsers).mock.calls.some(([q]) => q.includes(ADA))).toBe(false);
    });
  });

  it("shows no servers tile for a user whose every call is refused", async () => {
    vi.mocked(getUser).mockResolvedValue({ ...altair, status: "disabled" });
    await open();
    await waitFor(() => { if (!document.querySelector("[data-refused=disabled]")) throw new Error("the refused line is not drawn yet"); });
    expect(document.querySelector("[data-glance=servers]")).toBeNull();
    expect(document.querySelector("[data-glance=roles]")).not.toBeNull();
  });

  it("enables a disabled user without a confirm, and shows a refusal on the page", async () => {
    vi.mocked(getUser).mockResolvedValue({ ...altair, status: "disabled" });
    await open();
    vi.mocked(setUserStatus).mockRejectedValueOnce(refusal("break-glass accounts cannot change status", 403));
    await userEvent.click(screen.getByRole("button", { name: "Enable" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Enable refused: break-glass accounts cannot change status.");
    vi.mocked(setUserStatus).mockResolvedValueOnce(altair);
    await userEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith(enabledToast("altair")));
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("says a user stays locked when the enable's answer carries a notice", async () => {
    vi.mocked(getUser).mockResolvedValue({ ...altair, status: "disabled" });
    await open();
    vi.mocked(setUserStatus).mockResolvedValueOnce({ ...altair, notice: "altair is enabled and the disable is lifted, but Straza still refuses every check-in of altair, because altair is locked." });
    await userEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(notify.warn).toHaveBeenCalledWith(stillLockedToast("altair")));
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("assigns a role from the dialog, shows the banner with the session's door, and dismisses it", async () => {
    vi.mocked(grantRole).mockResolvedValue(grants[0]);
    await open();
    await userEvent.click(await screen.findByRole("button", { name: "Assign a role" }));
    await userEvent.click(await screen.findByRole("radio", { name: /audio-gen/ }));
    expect(catalogPreview).toHaveBeenCalledWith("audio-gen");
    await userEvent.click(screen.getByRole("button", { name: "Assign audio-gen" }));
    await waitFor(() => expect(grantRole).toHaveBeenCalledWith("u-altair", "r-audio"));
    const banner = await drawn("[data-assigned=audio-gen]");
    expect(banner.textContent).toContain(assignedBanner("audio-gen", "altair"));
    expect(notify.ok).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
    await userEvent.click(within(banner).getByRole("button", { name: "Open altair's session" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("sessions", ["01a1da3d-0000-0000-0000-000000000001"]));
    await userEvent.click(within(banner).getByRole("button", { name: "Dismiss" }));
    expect(document.querySelector("[data-assigned]")).toBeNull();
  });

  it("opens the waiting request from its tile and Test a call with the user filled in", async () => {
    await open();
    await userEvent.click(await screen.findByRole("button", { name: "1 request waiting" }));
    expect(listApprovals).toHaveBeenCalledWith("pending", "u-altair");
    expect(navigate).toHaveBeenCalledWith("approvals", ["requests", "apr-1"]);
    await userEvent.click(screen.getByRole("button", { name: "Test a call" }));
    expect(JSON.parse((document.querySelector("[data-policy-test]") as HTMLElement).textContent || "{}")).toEqual({ user: "altair" });
  });

  it("carries the day of an assignment from the user's assignments to its role's row", async () => {
    const at = "2026-11-02T12:00:00Z";
    vi.mocked(listAssignments).mockResolvedValue([{ ...grants[0], created_at: at }]);
    await open();
    const line = await screen.findByText("assigned here on " + dayWord(at));
    expect(line.closest("[data-role-row]")?.getAttribute("data-role-row")).toBe("github-readers");
    expect(line.getAttribute("title")).toBe(absTime(at));
  });

  it("counts the requests the server answers for this user alone", async () => {
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [{ id: "apr-1", user: "u-altair" }, { id: "apr-3", user: "u-altair" }] as never });
    await open();
    await userEvent.click(await screen.findByRole("button", { name: "2 requests waiting" }));
    expect(listApprovals).toHaveBeenCalledTimes(1);
    expect(listApprovals).toHaveBeenCalledWith("pending", "u-altair");
    expect(navigate).toHaveBeenCalledWith("approvals");
  });

  it.each<[string, string | undefined]>([
    ["Access", undefined],
    ["Sessions", "sessions"],
    ["Devices and keys", "devices"],
    ["Details", "details"],
  ])("brings the Roles heading into view from the Roles tile on the %s tab", async (_name, tab) => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    try {
      const view = await open(tab);
      await userEvent.click(await screen.findByRole("button", { name: "1 role" }));
      if (tab) {
        expect(navigate).toHaveBeenLastCalledWith("users", ["u-altair"], true);
        expect(scroll).not.toHaveBeenCalled();
        view.rerender(<TooltipProvider><UserPage id="u-altair" /></TooltipProvider>);
      }
      await waitFor(() => expect(scroll).toHaveBeenCalledTimes(1));
      expect(scroll.mock.contexts[0]).toBe(document.querySelector("[data-roles-head]"));
    } finally {
      scroll.mockRestore();
    }
  });

  it("brings the Roles heading into view while the access table above it is still loading", async () => {
    vi.mocked(userPreview).mockReturnValue(new Promise(() => {}));
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    try {
      const view = await open("details");
      await userEvent.click(await screen.findByRole("button", { name: "1 role" }));
      view.rerender(<TooltipProvider><UserPage id="u-altair" /></TooltipProvider>);
      await waitFor(() => expect(scroll).toHaveBeenCalledTimes(1));
      expect(scroll.mock.contexts[0]).toBe(document.querySelector("[data-roles-head]"));
      expect(document.querySelector("[data-access-table]")).toBeNull();
    } finally {
      scroll.mockRestore();
    }
  });

  it.each<[string, () => Promise<unknown>]>([
    ["Escape", () => userEvent.keyboard("{Escape}")],
    ["Cancel", () => userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }))],
    ["an assignment that landed", async () => {
      await userEvent.click(await screen.findByRole("radio", { name: /audio-gen/ }));
      await userEvent.click(screen.getByRole("button", { name: "Assign audio-gen" }));
    }],
  ])("puts the focus back on Assign a role when its dialog closes by %s", async (_name, close) => {
    vi.mocked(grantRole).mockResolvedValue(grants[0]);
    await open();
    const opener = await screen.findByRole("button", { name: "Assign a role" });
    await userEvent.click(opener);
    await screen.findByRole("dialog");
    await close();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(opener));
  });

  it("puts the focus back on Test a call when its sheet closes", async () => {
    await open();
    const opener = screen.getByRole("button", { name: "Test a call" });
    await userEvent.click(opener);
    await userEvent.click(screen.getByRole("button", { name: "Close the test" }));
    expect(document.querySelector("[data-policy-test]")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(opener));
  });

  it("shows the table for a user whose policies name only servers on the person's machine, counts them on the tile, and says so in the help", async () => {
    vi.mocked(userPreview).mockResolvedValue({
      entries: [{ app: "github", tool: "", status: "no_binding", reason: "" }],
      machine: [
        { app: "elevenlabs", open: true, tools: [onMachine("text_to_speech", "visible"), onMachine("clone_voice", "approve_gated"), onMachine("delete_voice", "hidden_policy")] },
        { app: "filesystem", open: true, tools: [] },
      ],
    });
    await open();
    await waitFor(() => expect(document.querySelector("[data-access-table]")).not.toBeNull());
    expect([...document.querySelectorAll("[data-server]")].map((r) => r.getAttribute("data-server"))).toEqual(["elevenlabs", "filesystem"]);
    expect(document.querySelector("[data-reaches-nothing]")).toBeNull();
    expect(screen.getByRole("button", { name: "2 servers, 2 tools" })).toBeTruthy();
    expect(within(document.querySelector("[data-server=elevenlabs]") as HTMLElement).getByRole("link", { name: "audio-access" })).toBeTruthy();
    await userEvent.hover(screen.getByRole("button", { name: "Help: What altair can do" }));
    expect((await screen.findByRole("tooltip")).textContent).toBe(ACCESS_HELP);
  });

  it("counts the servers on the person's machine with the registered ones on the tile", async () => {
    vi.mocked(userPreview).mockResolvedValue({ ...preview, machine: [{ app: "elevenlabs", tools: [onMachine("text_to_speech", "visible"), onMachine("clone_voice", "approve_gated"), onMachine("delete_voice", "hidden_policy")] }] });
    await open();
    expect(await screen.findByRole("button", { name: "2 servers, 4 tools" })).toBeTruthy();
    expect([...document.querySelectorAll("[data-server]")].map((r) => r.getAttribute("data-server"))).toEqual(["github", "elevenlabs"]);
  });

  it("names a policy as plain text for a reader without the policy area", async () => {
    vi.mocked(adminAreas).mockReturnValue({ identity: true, apps: true });
    vi.mocked(userPreview).mockResolvedValue({ entries: [], machine: [{ app: "elevenlabs", tools: [onMachine("text_to_speech", "visible")] }] });
    await open();
    const row = await drawn("[data-server=elevenlabs]");
    expect(row.querySelector("[data-tag=audio-access]")).not.toBeNull();
    expect(within(row).queryAllByRole("link")).toEqual([]);
  });

  it("leaves out Test a call, the servers and the requests for a role without their areas", async () => {
    vi.mocked(adminAreas).mockReturnValue({ identity: true });
    await open();
    expect(screen.queryByRole("button", { name: "Test a call" })).toBeNull();
    expect(userPreview).not.toHaveBeenCalled();
    expect(listApprovals).not.toHaveBeenCalled();
    expect(document.querySelector("[data-needs-area]")).not.toBeNull();
  });
});
