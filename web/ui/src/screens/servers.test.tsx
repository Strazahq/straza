import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Servers } from "./servers";
import { TooltipProvider } from "@/components/ui/tooltip";
import { listApps } from "@/lib/api";
import { navigate, pathFor } from "@/lib/router";
import { adminAreas } from "@/lib/session";

const BEARER = { as: "header", name: "Authorization", template: "Bearer {{secret}}" };

// Three servers in three states, the shape /v1/admin/apps answers: one
// added through the API, one a file in the apps directory defines, and a
// command server.
const apps = [
  {
    id: "a1", name: "midpoint", runtime: "remote", status: "running", tools: ["midpoint__search"], reached_by: ["dev"], last_probe_at: "2026-09-10T10:00:00Z", url: "http://mcp-midpoint-http:3001/mcp",
    manifest: { metadata: { name: "midpoint", description: "midPoint IGA operations, per-user identity." }, straza: { runtime: { kind: "remote", remote: { url: "http://mcp-midpoint-http:3001/mcp" } }, credential: { kind: "oauth", agents: "own", oauth: { provider: "keycloak" }, inject: BEARER } } },
  },
  {
    id: "a2", name: "demo-tools", runtime: "remote", status: "degraded", tools: [], reached_by: [], last_probe_at: "2026-09-10T10:00:00Z", url: "http://demo-tools:3001/mcp", file: "/etc/straza/apps/demo-tools.app.yaml",
    manifest: { metadata: { name: "demo-tools" }, straza: { runtime: { kind: "remote", remote: { url: "http://demo-tools:3001/mcp" } }, credential: { kind: "static", inject: { as: "header", name: "X-Demo-Key", template: "{{secret}}" } } } },
  },
  {
    id: "a3", name: "filesystem", runtime: "command", status: "unreachable", tools: [], reached_by: ["dev"],
    manifest: { metadata: { name: "filesystem" }, straza: { runtime: { kind: "command", command: { exec: "npx", args: ["-y", "@modelcontextprotocol/server-filesystem", "/data/My Files"] } } } },
  },
];

vi.mock("@/lib/api", () => ({
  listApps: vi.fn(),
}));
// The session's standing decides the page actions: null is a full grant.
vi.mock("@/lib/session", () => ({ adminAreas: vi.fn() }));

// The router keeps its real addresses; only navigate is watched, so a
// test can say where a door leads without leaving the screen.
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

// rows returns the server rows in table order. Each row is a button named
// "Open <server>", told apart from other buttons by the data-server attribute.
const rows = () => screen.getAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-server")).map((b) => b.getAttribute("data-server"));

const mount = () => render(
  <TooltipProvider>
    <Servers />
  </TooltipProvider>,
);


describe("the MCP servers screen", () => {
  beforeEach(() => {
    vi.mocked(listApps).mockResolvedValue(apps);
    vi.mocked(navigate).mockReset();
    vi.mocked(adminAreas).mockReturnValue(null);
  });

  it("lists every server as a row, sorted by name, and a row opens the server's page", async () => {
    mount();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("MCP servers");
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(rows()).toEqual(["demo-tools", "filesystem", "midpoint"]);
    expect(listApps).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByRole("button", { name: "Open midpoint" }));
    expect(navigate).toHaveBeenCalledWith("servers", ["a1"]);
    expect(navigate).toHaveBeenCalledTimes(1);
    // Enter on a focused row is the same door.
    screen.getByRole("button", { name: "Open filesystem" }).focus();
    await userEvent.keyboard("{Enter}");
    expect(navigate).toHaveBeenLastCalledWith("servers", ["a3"]);
    // No panel opens beside the list: the page is the one door (rule 21).
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("marks no server for the file that names it, since a file owns nothing", async () => {
    mount();
    await screen.findByRole("button", { name: "Open demo-tools" });
    expect(document.querySelector("[data-file-badge]")).toBeNull();
    expect(screen.queryByText("Defined by a file")).toBeNull();
  });

  it("keeps the last good rows on screen when a reload cannot reach strazad", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    vi.mocked(listApps).mockRejectedValueOnce(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("MCP servers: unreachable, state unknown.");
    expect(block.textContent).toContain("last successful read");
    expect(rows()).toEqual(["demo-tools", "filesystem", "midpoint"]);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("filters the rows by what is typed", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    await userEvent.type(screen.getByRole("textbox", { name: "Filter servers" }), "demo");
    expect(rows()).toEqual(["demo-tools"]);
  });



  it("opens the wizard from the page's primary action", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const add = screen.getByRole("button", { name: "Add MCP server" });
    expect(add.getAttribute("data-variant")).toBe("default");
    expect(screen.getByRole("button", { name: "Reload the list" }).getAttribute("data-variant")).toBe("ghost");
    await userEvent.click(add);
    expect(navigate).toHaveBeenCalledWith("servers", ["new"]);
  });

  it("links the server name to its page and a plain click navigates once", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const link = screen.getByRole("link", { name: "midpoint" });
    expect(link.getAttribute("href")).toBe(pathFor("servers", ["a1"]));
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("servers", ["a1"]);
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("for a session that administers servers and holds no apps grant, offers Reload alone and says what it administers", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    vi.mocked(listApps).mockResolvedValue([{ ...apps[0], admin_role: "mcp-admin-midpoint", may_change: true }]);
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(screen.getByRole("button", { name: "Reload the list" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Add MCP server/ })).toBeNull();
    expect(document.querySelector("[data-admin-line]")?.textContent).toBe("You administer 1 server through mcp-admin-midpoint and may change it. Registering a new server or importing one from the registry needs straza-global-mcp-admin.");
  });

  it("keeps Add MCP server and no admin line for a full grant", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(screen.getByRole("button", { name: /Add MCP server/ })).toBeTruthy();
    expect(document.querySelector("[data-admin-line]")).toBeNull();
  });
});
