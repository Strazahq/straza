import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import App from "./App";
import { BASE } from "@/lib/router";
import { ROUTES } from "@/lib/routes";

// The session, the grants and the sign-in card are modules of their own,
// and the shell suite stubs them so it tests the frame alone.
const stub = vi.hoisted(() => ({
  state: { kind: "signed-in", user: "alice", grants: "full", expiresAt: Date.now() + 300000 } as Record<string, unknown>,
  areas: null as Record<string, true> | null,
  servers: 0,
  waiting: "",
  working: [] as { id: string; items: unknown[] }[],
  signOut: vi.fn(),
}));

vi.mock("@/lib/use-session", () => ({
  useSession: () => ({ state: stub.state, signedIn: vi.fn(), signOut: stub.signOut }),
}));
vi.mock("@/lib/session", () => ({
  adminAreas: () => stub.areas,
  adminServers: () => stub.servers,
  onAuthLost: () => () => {},
}));
vi.mock("@/components/sign-in", () => ({
  SignIn: ({ returnTo }: { returnTo: string }) => <div data-sign-in>{"Sign in to go back to " + returnTo}</div>,
}));
vi.mock("@/lib/api", () => ({
  listApps: vi.fn(async () => [{ id: "a1", name: "midpoint", runtime: "remote", status: "running", tools: [], reached_by: ["dev"] }]),
  listTools: vi.fn(async () => []),
  listBindings: vi.fn(async () => []),
  recheckApp: vi.fn(),
  listUsers: vi.fn(async () => ({ items: [], next_cursor: "" })),
  listRoles: vi.fn(async () => []),
  listPolicies: vi.fn(async () => ({ items: [] })),
  query: () => "",
}));

// The wizard and a server's page load their own data; the shell suite
// stands them in with their route arguments so it tests the routing alone.
vi.mock("@/screens/sessions", () => ({
  Sessions: () => <h1 tabIndex={-1}>Sessions</h1>,
}));
vi.mock("@/screens/overview", () => ({
  Overview: () => <h1 tabIndex={-1}>Overview</h1>,
}));
vi.mock("@/screens/settings", () => ({
  Settings: ({ tab }: { tab?: string }) => <h1 tabIndex={-1}>{"Settings" + (tab ? " " + tab : "")}</h1>,
}));
vi.mock("@/screens/drafts", () => ({
  Drafts: ({ tab }: { tab?: string }) => <h1 tabIndex={-1}>{"Drafts" + (tab ? " " + tab : "")}</h1>,
}));
vi.mock("@/screens/draft-page", () => ({
  DraftPage: ({ id }: { id: string }) => <h1 tabIndex={-1}>{"Draft " + id}</h1>,
}));
// The sidebar's waiting count comes from the drafts wrappers, which the
// shell loads on first use.
vi.mock("@/lib/drafts-api", () => ({ waitingLabel: vi.fn(async () => stub.waiting), listDrafts: vi.fn(async () => ({ items: stub.working, next_cursor: "" })) }));
vi.mock("@/screens/approvals", () => ({
  Approvals: ({ tab, openID }: { tab?: string; openID?: string }) => <h1 tabIndex={-1}>{"Approvals" + (tab ? " " + tab : "") + (openID ? " " + openID : "")}</h1>,
}));
vi.mock("@/screens/roles", () => ({
  Roles: () => <h1 tabIndex={-1}>Roles</h1>,
}));
vi.mock("@/screens/new-role", () => ({
  NewRole: () => <h1 tabIndex={-1}>New role</h1>,
}));
vi.mock("@/screens/role-page", () => ({
  RolePage: ({ id, tab }: { id: string; tab?: string }) => <h1 tabIndex={-1}>{"Role " + id + (tab ? " " + tab : "")}</h1>,
}));
vi.mock("@/screens/audit", () => ({
  Audit: ({ preset }: { preset?: { session?: string } }) => <h1 tabIndex={-1}>{"Audit" + (preset?.session ? " of " + preset.session : "")}</h1>,
}));
vi.mock("@/screens/transcripts", () => ({
  Transcripts: ({ session }: { session?: string }) => <h1 tabIndex={-1}>{"Transcripts" + (session ? " of " + session : "")}</h1>,
}));
vi.mock("@/screens/users", () => ({
  Users: ({ openID }: { openID?: string }) => <h1 tabIndex={-1}>{"Users" + (openID ? " opening " + openID : "")}</h1>,
}));
vi.mock("@/screens/server-page", () => ({
  ServerPage: ({ id, tab }: { id: string; tab?: string }) => <h1 tabIndex={-1}>{"page of " + id + " on " + (tab || "overview")}</h1>,
}));
vi.mock("@/screens/add-server", () => ({
  AddServer: () => <h1 tabIndex={-1}>Add MCP server</h1>,
}));

// stubPublic answers the two reads the shell makes with no bearer.
function stubPublic() {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input);
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (path === "/version") return json({ version: "0.52.0", commit: "abc1234", go: "go1.25", profile: "eval" });
    if (path === "/readyz") return json({ status: "ok", components: { db: "ok" } });
    return json({ error: "no route " + path }, 404);
  });
}

const at = (path: string) => window.history.replaceState(null, "", path);
const navLinks = () => within(screen.getByRole("navigation", { name: "Areas" })).getAllByRole("link").map((a) => a.textContent);

describe("the console shell", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    stubPublic();
    stub.state = { kind: "signed-in", user: "alice", grants: "full", expiresAt: Date.now() + 300000 };
    stub.areas = null;
    stub.servers = 0;
    stub.waiting = "";
    stub.working = [];
  });

  it("lands a server admin on MCP servers from the base, beside the Drafts their standing may write", async () => {
    stub.state = { kind: "signed-in", user: "ivan", grants: "", servers: 1, expiresAt: Date.now() + 300000 };
    stub.areas = {};
    stub.servers = 1;
    at(BASE);
    render(<App />);
    expect(navLinks()).toEqual(["MCP servers1", "Drafts"]);
    await screen.findByRole("heading", { level: 1, name: "MCP servers" });
    expect(screen.queryByText(/is not available to this account/)).toBeNull();
  });

  it("renders a sidebar item per area and lands on Overview from the base", async () => {
    at(BASE);
    render(<App />);
    expect(navLinks()).toEqual(ROUTES.map((r) => r.label));
    expect(window.location.pathname).toBe(BASE + "overview");
    expect((await screen.findByRole("heading", { level: 1 })).textContent).toBe("Overview");
    expect(document.title).toBe("Straza · Overview");
    expect(screen.getByRole("link", { name: "Overview" }).getAttribute("aria-current")).toBe("page");
    expect((await screen.findByText("strazad 0.52.0")).textContent).toBe("strazad 0.52.0");
    expect(screen.getByText("eval").textContent).toBe("eval");
    expect((await screen.findByText("Healthy")).textContent).toBe("Healthy");
    expect(screen.getByRole("button", { name: "alice" }).textContent).toBe("alice");
  });

  it("opens the Roles list from the sidebar and a role's page from its address", async () => {
    at(BASE + "servers");
    render(<App />);
    // The servers list loads on first use like every screen, so the click
    // waits for it: the focus move to the next title holds only when the
    // screen it leaves is up.
    await screen.findByRole("heading", { level: 1, name: "MCP servers" });
    await userEvent.click(screen.getByRole("link", { name: "Roles" }));
    expect(window.location.pathname).toBe(BASE + "roles");
    const h1 = await screen.findByRole("heading", { level: 1, name: "Roles" });
    await waitFor(() => expect(document.activeElement).toBe(h1));
    expect(document.title).toBe("Straza · Roles");
    expect(screen.getByRole("link", { name: "Roles" }).getAttribute("aria-current")).toBe("page");
    at(BASE + "roles/r-dev/holders");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "Role r-dev holders" })).textContent).toBe("Role r-dev holders");
    at(BASE + "roles/new");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "New role" })).textContent).toBe("New role");
  });

  it("opens the Approvals area on its tab and on a request named in the address", async () => {
    at(BASE + "approvals/devices");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "Approvals devices" })).textContent).toBe("Approvals devices");
    at(BASE + "approvals/requests/apr-1");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "Approvals requests apr-1" })).textContent).toBe("Approvals requests apr-1");
  });

  it("opens the Drafts queue on its tab and a draft's review page from its address", async () => {
    at(BASE + "drafts");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "Drafts" })).textContent).toBe("Drafts");
    expect(screen.getByRole("link", { name: "Drafts" }).getAttribute("aria-current")).toBe("page");
    at(BASE + "drafts/published");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "Drafts published" })).textContent).toBe("Drafts published");
    at(BASE + "drafts/41");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1, name: "Draft 41" })).textContent).toBe("Draft 41");
  });

  it("counts the drafts that wait beside Drafts, and says so on hover", async () => {
    stub.waiting = "2";
    at(BASE + "overview");
    render(<App />);
    const badge = await waitFor(() => {
      const b = document.querySelector("[data-drafts-badge]") as HTMLElement | null;
      if (!b) throw new Error("no badge yet");
      return b;
    });
    expect(badge.textContent).toBe("2");
    expect(badge.getAttribute("title")).toBe("2 drafts wait for review.");
  });

  it("shows the person's working draft in the header, read beside the waiting count", async () => {
    stub.working = [{ id: "39", items: [{ kind: "App", name: "demo-tools", op: "put" }, { kind: "Role", name: "r", op: "put" }] }];
    at(BASE + "overview");
    render(<App />);
    expect((await screen.findByRole("button", { name: "Your draft · 2 changes" })).getAttribute("data-your-draft")).toBe("39");
  });

  it("renders the not-found page inside the shell for an address off the table", () => {
    at(BASE + "nowhere");
    render(<App />);
    expect(navLinks()).toHaveLength(ROUTES.length);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Page not found");
    expect(screen.getByText("There is no page at this address.")).toBeTruthy();
    expect(screen.getByText("Nothing in the console answers to " + BASE + "nowhere. Check the address, or start from Overview.")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open Overview" }).getAttribute("href")).toBe(BASE + "overview");
    expect(document.title).toBe("Straza · Page not found");
  });

  it("shows Audit alone to a delegated admin and says Users is not available", async () => {
    stub.state = { kind: "signed-in", user: "carol", grants: "audit:read", expiresAt: Date.now() + 300000 };
    stub.areas = { audit: true };
    at(BASE + "users");
    render(<App />);
    expect(navLinks()).toEqual(["Audit"]);
    expect(screen.getByText("Users is not available to this account.")).toBeTruthy();
    expect(screen.getByText("This account's admin scopes cover audit:read, which does not reach Users. Ask for a wider role, or use strazactl.")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Open Audit" }));
    expect(window.location.pathname).toBe(BASE + "audit");
    expect((await screen.findByRole("heading", { level: 1, name: "Audit" })).textContent).toBe("Audit");
  });

  it("renders the sign-in card with the page label and leaves the address alone when signed out", () => {
    stub.state = { kind: "signed-out", reason: { kind: "none", detail: "" } };
    at(BASE + "users");
    render(<App />);
    expect(screen.getByText("Sign in to go back to Users")).toBeTruthy();
    expect(window.location.pathname).toBe(BASE + "users");
    expect(screen.queryByRole("navigation", { name: "Areas" })).toBeNull();
  });

  it("routes servers/new to the wizard and keeps the sidebar on MCP servers", async () => {
    at(BASE + "servers/new");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1 })).textContent).toBe("Add MCP server");
    expect(screen.getByRole("link", { name: "MCP servers" }).getAttribute("aria-current")).toBe("page");
    expect(document.title).toBe("Straza · MCP servers");
  });

  it("routes a server's typed address with its tab to the server's page", async () => {
    at(BASE + "servers/a1/activity");
    render(<App />);
    expect((await screen.findByRole("heading", { level: 1 })).textContent).toBe("page of a1 on activity");
    expect(screen.getByRole("link", { name: "MCP servers" }).getAttribute("aria-current")).toBe("page");
  });

  it("opens a server's page from the palette", async () => {
    at(BASE + "servers");
    render(<App />);
    await userEvent.keyboard("{Control>}k{/Control}");
    await userEvent.type(await screen.findByPlaceholderText("Find a page, server, user, role or policy"), "mid");
    await userEvent.click(await screen.findByRole("option", { name: /midpoint/ }));
    expect(window.location.pathname).toBe(BASE + "servers/a1");
    expect((await screen.findByRole("heading", { level: 1 })).textContent).toBe("page of a1 on overview");
  });

  it("renders one muted line while the session resumes", () => {
    stub.state = { kind: "booting" };
    at(BASE + "servers");
    render(<App />);
    expect(screen.getByText("Resuming session")).toBeTruthy();
  });

  it("offers Sign out in the user menu", async () => {
    at(BASE + "servers");
    render(<App />);
    await userEvent.click(screen.getByRole("button", { name: "alice" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Sign out" }));
    expect(stub.signOut).toHaveBeenCalledTimes(1);
  });
});
