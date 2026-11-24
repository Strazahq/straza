import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SelfServiceApp } from "./app";
import { ApiError, type ServerRow, readSelf, selfDevices, selfEnrollToken, selfRequests, selfServers } from "@/lib/api";
import { CANCEL } from "@/lib/approval-words";
import { enrollDevice } from "./approver-api";
import type { ApproverRow } from "./rows";
import * as W from "./words";

// The shell with a signed-in session and no enrolment, driven to each tab
// through the address. The tabs are the real ones over a mocked API, so a
// shell that hands a tab an unstable door shows up here as the boundary's
// error text instead of the tab.

// sessionStub is the hook's answer, mutable per test so the header can be
// read in each session state and for each standing.
const sessionStub = vi.hoisted(() => ({
  state: { kind: "signed-in", user: "alice", grants: "full", servers: 0, expiresAt: Date.now() + 300000 } as Record<string, unknown>,
}));
vi.mock("@/lib/use-session", () => ({
  useSession: () => ({ state: sessionStub.state, signedIn: vi.fn(), signOut: vi.fn(async () => undefined) }),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  readSelf: vi.fn(),
  selfServers: vi.fn(),
  selfRequests: vi.fn(),
  selfDevices: vi.fn(),
  selfEnrollToken: vi.fn(),
}));
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  storageAvailable: () => true,
  storageOutlook: async () => "unknown",
  loadEnrollment: async () => null,
  requestDurable: async () => true,
  saveEnrollment: async () => undefined,
}));
// A setup that lands registers a key and then reads the device lane, which
// answers nothing here.
vi.mock("./approver-api", async (orig) => ({
  ...(await orig<typeof import("./approver-api")>()),
  enrollDevice: vi.fn(),
  pending: vi.fn(async () => []),
  history: vi.fn(async () => ({ items: [], next_cursor: "" })),
}));
vi.mock("./sign", async (orig) => ({
  ...(await orig<typeof import("./sign")>()),
  generateDeviceKey: vi.fn(async () => ({ privateKey: "private", publicKey: "public" })),
  exportPublicKeyB64: vi.fn(async () => "PUB"),
}));
vi.mock("./push", () => ({ teardown: vi.fn(), retireOldWorker: vi.fn(), support: () => ({ available: false, why: "" }), sync: vi.fn(), enable: vi.fn(), disable: vi.fn() }));

const SIGNED_IN = { kind: "signed-in", user: "alice", grants: "full", servers: 0, expiresAt: Date.now() + 300000 };
const midpoint: ServerRow = { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false };
const none: ServerRow = { app: "demo-tools", runtime: "remote", kind: "none", reached: true };
const github: ServerRow = { app: "github", runtime: "remote", kind: "token", reached: true, connected: false };

const held: ApproverRow = {
  id: "apr_push", created_at: new Date(Date.now() - 30000).toISOString(), expires_at: new Date(Date.now() + 100000).toISOString(),
  requester: { username: "altair", kind: "nhi" }, origin: {}, rule_id: "push-needs-yes", set_name: "agent-guardrails",
  summary: { tool: "shell.exec" }, args_preview: "git push origin main", approver_users: ["alice"],
};

const countOf = (tab: string) => (document.querySelector('[data-count="' + tab + '"]') as HTMLElement | null)?.textContent ?? null;
const notice = () => document.querySelector("[data-notice]") as HTMLElement | null;
const tabHead = () => screen.getByRole("heading", { level: 1 });
// setupHead waits for the setup card, which a lazy tab draws; a loaded
// machine takes more than the default second to load one.
const setupHead = () => screen.findByText(W.SETUP_HEAD, undefined, { timeout: 5000 });

beforeEach(() => {
  vi.clearAllMocks();
  sessionStub.state = { ...SIGNED_IN };
  sessionStorage.clear();
  vi.mocked(readSelf).mockResolvedValue({ username: "alice", user_kind: "human", admin_grants: "full", enroll_channels: ["browser", "mobile"], sponsored: ["joe-java-developer-agent", "sam-sre-agent"] });
  vi.mocked(selfServers).mockImplementation(async (user = "") => (user ? [none, midpoint] : []));
  vi.mocked(selfRequests).mockResolvedValue({ requests: [] });
  vi.mocked(selfDevices).mockResolvedValue({ devices: [] });
  window.history.replaceState(null, "", "/self-service/");
});
afterEach(() => window.history.replaceState(null, "", "/self-service/"));

describe("the self-service shell", () => {
  it("opens on Home with its head, the setup card and the three cards", async () => {
    render(<SelfServiceApp />);
    expect(await setupHead()).toBeTruthy();
    expect(tabHead().textContent).toBe(W.TAB_HEAD.home);
    // The head is the title alone, with no sentence under it.
    expect(document.querySelector("[data-tab-head=home] p")).toBeNull();
    expect(document.querySelectorAll("[data-card]").length).toBe(3);
    expect(screen.queryByText(/This screen hit an error/)).toBeNull();
    expect(document.querySelector("[data-page-head]")).toBeNull();
  });

  it("draws the brand and the page's name, and for a signed-in person a menu with Sign out", async () => {
    render(<SelfServiceApp />);
    const header = document.querySelector("[data-self-header]") as HTMLElement;
    expect(within(header).getByText(W.BRAND)).toBeTruthy();
    expect(within(header).getByText(W.PAGE_TITLE).className).toContain("text-muted-foreground");
    const me = header.querySelector("[data-signed-in]") as HTMLElement;
    expect(me.getAttribute("data-signed-in")).toBe("alice");
    expect(me.textContent).toBe("alice");
    await userEvent.click(me);
    expect((await screen.findByRole("menuitem", { name: W.SIGN_OUT }))).toBeTruthy();
    expect(screen.getAllByRole("menuitem").length).toBe(1);
  });

  it("says Signed out after the menu's Sign out", async () => {
    render(<SelfServiceApp />);
    await userEvent.click(document.querySelector("[data-signed-in]") as HTMLElement);
    await userEvent.click(await screen.findByRole("menuitem", { name: W.SIGN_OUT }));
    await waitFor(() => expect(notice()?.textContent).toContain(W.SIGNED_OUT));
  });

  it("offers Sign in to a signed-out visitor and says Resuming while the session resumes", async () => {
    sessionStub.state = { kind: "signed-out", reason: { kind: "none", detail: "" } };
    const { unmount } = render(<SelfServiceApp />);
    const header = () => document.querySelector("[data-self-header]") as HTMLElement;
    expect(within(header()).getByRole("button", { name: W.SIGN_IN }).hasAttribute("data-sign-in")).toBe(true);
    expect(header().querySelector("[data-signed-in]")).toBeNull();
    unmount();

    sessionStub.state = { kind: "booting" };
    render(<SelfServiceApp />);
    expect(within(header()).getByText(W.RESUMING)).toBeTruthy();
    expect(within(header()).queryByRole("button", { name: W.SIGN_IN })).toBeNull();
  });

  it("offers the console door to an area admin, to a server admin, and to nobody else", async () => {
    const { unmount: u1 } = render(<SelfServiceApp />);
    expect(await screen.findByRole("link", { name: /Open the console/ })).toBeTruthy();
    u1();
    sessionStub.state = { ...SIGNED_IN, user: "carol", grants: "", servers: 1 };
    const { unmount: u2 } = render(<SelfServiceApp />);
    expect(await screen.findByRole("link", { name: /Open the console/ })).toBeTruthy();
    u2();
    sessionStub.state = { ...SIGNED_IN, user: "grace", grants: "", servers: 0 };
    render(<SelfServiceApp />);
    expect(document.querySelector('[data-signed-in="grace"]')).not.toBeNull();
    expect(screen.queryByRole("link", { name: /Open the console/ })).toBeNull();
  });

  it("folds the header's right side into one Menu button below 640 px that holds what the row holds", async () => {
    render(<SelfServiceApp />);
    expect((document.querySelector("[data-header-row]") as HTMLElement).className).toMatch(/(^| )hidden( |$).* sm:flex/);
    const menuBox = document.querySelector("[data-header-menu]") as HTMLElement;
    expect(menuBox.className).toContain("sm:hidden");
    await userEvent.click(within(menuBox).getByRole("button", { name: W.MENU }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByText("alice")).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: /Open the console/ })).toBeTruthy();
    expect(within(menu).getByRole("menuitem", { name: W.SIGN_OUT })).toBeTruthy();
  });

  it("holds Sign in in the Menu for a signed-out visitor", async () => {
    sessionStub.state = { kind: "signed-out", reason: { kind: "none", detail: "" } };
    render(<SelfServiceApp />);
    await userEvent.click(screen.getByRole("button", { name: W.MENU }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: W.SIGN_IN })).toBeTruthy();
    expect(within(menu).queryByRole("menuitem", { name: W.SIGN_OUT })).toBeNull();
  });

  it("counts on the tab row what waits for the person, and draws no zero", async () => {
    vi.mocked(selfServers).mockImplementation(async (user = "") => (user ? [none] : [github]));
    vi.mocked(selfRequests).mockImplementation((async (scope: string) => ({ requests: scope === "decidable" ? [held] : [] })) as typeof selfRequests);
    render(<SelfServiceApp />);
    await waitFor(() => expect(countOf("requests")).toBe("1"), { timeout: 5000 });
    await waitFor(() => expect(countOf("credentials")).toBe("1"));
    expect(countOf("home")).toBeNull();
    expect(countOf("devices")).toBeNull();
  });

  it("draws no count when nothing waits", async () => {
    render(<SelfServiceApp />);
    await waitFor(() => expect(vi.mocked(selfServers)).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(vi.mocked(selfRequests).mock.calls.length).toBeGreaterThanOrEqual(2), { timeout: 5000 });
    await new Promise((r) => setTimeout(r, 50));
    expect(countOf("requests")).toBeNull();
    expect(countOf("credentials")).toBeNull();
  });

  it("draws no count, and Home's first card says so, when the requests could not be read", async () => {
    vi.mocked(selfRequests).mockRejectedValue(new ApiError("unreachable", 0, true));
    render(<SelfServiceApp />);
    const line = () => document.querySelector("[data-card=requests] [data-card-line]")?.textContent;
    await waitFor(() => expect(line()).toBe(W.REQUESTS_UNREAD_CARD), { timeout: 5000 });
    expect(countOf("requests")).toBeNull();
  });

  it("reads the credentials once after sign-in, the person's and each sponsored agent's", async () => {
    render(<SelfServiceApp />);
    await waitFor(() => expect(vi.mocked(selfServers)).toHaveBeenCalledTimes(3));
    expect(vi.mocked(selfServers).mock.calls.map((c) => c[0])).toEqual(["", "joe-java-developer-agent", "sam-sre-agent"]);
    await new Promise((r) => setTimeout(r, 50));
    expect(vi.mocked(selfServers)).toHaveBeenCalledTimes(3);
  });

  it("renders the Credentials tab with the agents' rows, which keeps its own reads, without an update loop", async () => {
    window.history.replaceState(null, "", "/self-service/credentials");
    render(<SelfServiceApp />);
    await waitFor(() => expect(screen.getAllByText("midpoint").length).toBeGreaterThanOrEqual(2), { timeout: 5000 });
    expect(screen.queryByText(/This screen hit an error/)).toBeNull();
    // The shell's read and the tab's own, three calls each.
    await waitFor(() => expect(vi.mocked(selfServers)).toHaveBeenCalledTimes(6));
    await new Promise((r) => setTimeout(r, 50));
    expect(vi.mocked(selfServers)).toHaveBeenCalledTimes(6);
  });

  it.each([
    ["requests", "/self-service/requests"],
    ["credentials", "/self-service/credentials"],
    ["devices", "/self-service/devices"],
  ] as const)("opens the %s tab under its own head and moves focus to its title", async (tab, path) => {
    render(<SelfServiceApp />);
    await setupHead();
    const trigger = document.querySelector('[data-tab="' + tab + '"]') as HTMLElement;
    await userEvent.click(trigger);
    await waitFor(() => expect(tabHead().textContent).toBe(W.TAB_HEAD[tab]));
    expect(document.querySelector("[data-tab-head=" + tab + "] p")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(tabHead()));
    expect(window.location.pathname).toBe(path);
  });

  it("does not move focus when the page first opens", async () => {
    render(<SelfServiceApp />);
    await setupHead();
    expect(document.activeElement).not.toBe(tabHead());
  });

  it("offers Add a phone as the Devices tab's action to an account that may add one", async () => {
    window.history.replaceState(null, "", "/self-service/devices");
    render(<SelfServiceApp />);
    const add = await waitFor(() => {
      const el = document.querySelector("[data-add-phone]") as HTMLElement | null;
      if (!el) throw new Error("no Add a phone yet");
      return el;
    });
    expect(add.textContent).toContain(W.ADD_PHONE);
    expect(document.querySelector("[data-tab-head=devices]")!.contains(add)).toBe(true);
    expect(await setupHead()).toBeTruthy();
  });

  it("opens the request the address names on the Requests tab before setup, and closing it takes the name out of the address", async () => {
    vi.mocked(selfRequests).mockImplementation((async (scope: string) => ({ requests: scope === "decidable" ? [held] : [] })) as typeof selfRequests);
    window.history.replaceState(null, "", "/self-service/requests/apr_push");
    render(<SelfServiceApp />);
    const dialog = await screen.findByRole("dialog", undefined, { timeout: 5000 });
    expect(dialog.getAttribute("data-request-dialog")).toBe("apr_push");
    expect(document.querySelector("[data-tab-head=requests]")).not.toBeNull();
    await userEvent.click(within(dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(document.querySelector("[data-request-dialog]")).toBeNull());
    expect(window.location.pathname).toBe("/self-service/requests");
  });

  it("lands the old address on Devices and replaces it in the address bar", async () => {
    window.history.replaceState(null, "", "/self-service/browser");
    render(<SelfServiceApp />);
    await waitFor(() => expect(tabHead().textContent).toBe(W.TAB_HEAD.devices));
    expect(window.location.pathname).toBe("/self-service/devices");
  });

  it("shows a sign-in that failed as the notice, which Dismiss takes away", async () => {
    sessionStorage.setItem("straza.signin.problem", JSON.stringify("Your identity provider did not sign you in. Press Sign in to try again."));
    render(<SelfServiceApp />);
    expect(notice()!.textContent).toBe("Your identity provider did not sign you in. Press Sign in to try again.");
    // The notice sits between the tab row and the tab's head.
    const row = document.querySelector("[data-slot=tabs-list]") as HTMLElement;
    expect(row.compareDocumentPosition(notice()!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(notice()!.compareDocumentPosition(tabHead()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: W.DISMISS }));
    expect(notice()).toBeNull();
  });

  it("says Straza did not answer in place of the session module's sentence for a server it could not reach", async () => {
    sessionStorage.setItem("straza.signin.problem", JSON.stringify("Straza did not answer. Check that it is running, then press Sign in again."));
    render(<SelfServiceApp />);
    expect(notice()!.textContent).toBe(W.NO_ANSWER);
  });
});

describe("the focus after the setup dialog", () => {
  // ways are the three ways a person closes the setup dialog without
  // setting anything up.
  const ways: [string, (dialog: HTMLElement) => Promise<void>][] = [
    ["Escape", async () => { await userEvent.keyboard("{Escape}"); }],
    ["Cancel", async (dialog) => { await userEvent.click(within(dialog).getByRole("button", { name: CANCEL })); }],
    ["its close button", async (dialog) => { await userEvent.click(within(dialog).getByRole("button", { name: "Close" })); }],
  ];
  const setupDialog = () => screen.findByRole("dialog", { name: W.SETUP_BROWSER }, { timeout: 5000 });
  const title = () => document.querySelector("[data-tab-head] h1") as HTMLElement;
  const gone = () => waitFor(() => expect(document.querySelector("[data-setup-dialog]")).toBeNull());
  // opener waits for the setup card's button on the open tab.
  const opener = () => waitFor(() => {
    const el = document.querySelector("[data-setup-browser]") as HTMLElement | null;
    if (!el) throw new Error("no Set up this browser yet");
    return el;
  }, { timeout: 5000 });

  it.each(ways)("goes back to Set up this browser on Home after %s", async (_way, close) => {
    render(<SelfServiceApp />);
    const button = await opener();
    await userEvent.click(button);
    await close(await setupDialog());
    await gone();
    await waitFor(() => expect(document.activeElement).toBe(button));
  });

  it.each(ways)("goes back to Set up this browser on Devices after %s", async (_way, close) => {
    window.history.replaceState(null, "", "/self-service/devices");
    render(<SelfServiceApp />);
    const button = await opener();
    expect(document.querySelector("[data-tab-head=devices]")).not.toBeNull();
    await userEvent.click(button);
    await close(await setupDialog());
    await gone();
    await waitFor(() => expect(document.activeElement).toBe(button));
  });

  it.each(ways)("goes to the tab's title after %s when a request opened it, since the request's dialog closed", async (_way, close) => {
    vi.mocked(selfRequests).mockImplementation((async (scope: string) => ({ requests: scope === "decidable" ? [held] : [] })) as typeof selfRequests);
    render(<SelfServiceApp />);
    const row = await waitFor(() => {
      const el = document.querySelector("[data-home-waiting] [data-request]") as HTMLElement | null;
      if (!el) throw new Error("no request yet");
      return el;
    }, { timeout: 5000 });
    await userEvent.click(row);
    const request = await screen.findByRole("dialog");
    expect(request.querySelector("[data-needs-device]")!.textContent).toBe(W.ANSWER_NEEDS_DEVICE);
    await userEvent.click(within(request).getByRole("button", { name: W.SETUP_BROWSER }));
    await close(await setupDialog());
    await gone();
    expect(document.querySelector("[data-request-dialog]")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(title()));
  });

  it("goes to the tab's title after a setup that landed, since the setup card is gone", async () => {
    vi.mocked(selfEnrollToken).mockResolvedValue({ enroll_token: "etk-1", expires_in: 120, project: { id: "prj_01", name: "acme-prod" }, user: { id: "u-alice", username: "alice" } } as never);
    vi.mocked(enrollDevice).mockResolvedValue({ approver_device_id: "apd_01", device_token: "dt-live", expires_in: 2592000, user: { id: "u-alice", username: "alice" } } as never);
    render(<SelfServiceApp />);
    await userEvent.click(await opener());
    const dialog = await setupDialog();
    await userEvent.click(within(dialog).getByRole("button", { name: W.SET_UP }));
    await gone();
    expect(document.querySelector("[data-setup-browser]")).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(title()));
  });
});
