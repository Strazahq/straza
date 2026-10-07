import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import App from "./App";
import { BASE } from "@/lib/router";

// The whole console as a server admin: the check-in answers one
// administered server and no area grant, and every apps-area read the
// server page makes is refused. The Roles tab of that server still opens,
// with the roles the server owns and no bare refusal anywhere.

const session = vi.hoisted(() => ({ state: {} as Record<string, unknown> }));

vi.mock("@/lib/use-session", () => ({
  useSession: () => ({ state: session.state, signedIn: vi.fn(), signOut: vi.fn() }),
}));
vi.mock("@/lib/session", async (orig) => ({
  ...(await orig<typeof import("@/lib/session")>()),
  adminAreas: () => ({}),
  adminServers: () => 1,
  onAuthLost: () => () => {},
}));

const app = {
  id: "app-9",
  name: "demo-tools",
  runtime: "http",
  status: "running",
  tools: ["echo", "add", "get-sum"],
  reached_by: ["demo-tools-readers"],
  admin_role: "mcp-admin-demo-tools",
};
const role = {
  id: "r-1",
  name: "demo-tools-readers",
  kind: "application",
  description: "Read-only tools of demo-tools for analysts.",
  server: "demo-tools",
  tools: ["echo", "add"],
  holder_count: 0,
  assigned_count: 0,
};

// REFUSED is what strazad answers a session with no area grant on the
// apps-wide reads: its own sentence, never a bare code.
const REFUSED = "this account administers the MCP servers it holds the admin role of, and reads nothing else";

function stubServer() {
  vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
    const path = String(input);
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (path === "/version") return json({ version: "0.52.0", commit: "abc1234", go: "go1.25", profile: "eval" });
    if (path === "/readyz") return json({ status: "ok", components: { db: "ok" } });
    if (path === "/v1/admin/apps") return json([app]);
    if (path === "/v1/admin/roles") return json([role]);
    return json({ error: REFUSED }, 403);
  });
}

describe("a server admin on their server's Roles tab", () => {
  beforeEach(() => {
    vi.unstubAllGlobals();
    stubServer();
    session.state = { kind: "signed-in", user: "carol", grants: "", servers: 1, expiresAt: Date.now() + 300000 };
  });

  it("opens the tab with the roles the server owns and no bare refusal", async () => {
    window.history.replaceState(null, "", BASE + "servers/app-9/roles");
    render(<App />);

    // The page and its tabs load on first use, so the first wait carries a
    // timeout wide enough for the chunk and the four reads behind it.
    expect((await screen.findByRole("heading", { level: 1, name: "demo-tools" }, { timeout: 5000 })).textContent).toBe("demo-tools");
    const tab = await screen.findByRole("tab", { name: /^Server roles/ });
    expect(tab.getAttribute("aria-selected")).toBe("true");
    expect(tab.textContent).toBe("Server roles 1");

    expect(await screen.findByText("1 role of this server")).toBeTruthy();
    const row = document.querySelector('[data-owned-role="demo-tools-readers"]') as HTMLElement;
    expect(within(row).getAllByRole("cell")[1].textContent).toBe("2 of 3toolsecho, add");
    expect(screen.getByText("Nobody holds it yet. Your identity manager assigns it: in midPoint it is AR:demo-tools-readers within a sync cycle.")).toBeTruthy();

    expect(document.querySelector("[data-refused-error]")).toBeNull();
    expect(screen.queryByText(/HTTP \d/)).toBeNull();
    expect(screen.queryByText(new RegExp(REFUSED))).toBeNull();
  });
});
