import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Servers } from "./servers";
import { TooltipProvider } from "@/components/ui/tooltip";
import { listApps, listRoles } from "@/lib/api";
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
  listRoles: vi.fn(),
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
    vi.mocked(listRoles).mockResolvedValue([{ id: "r-dev", name: "dev", kind: "business" }]);
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
    expect(block.textContent).toContain("The server list could not be read because Straza did not answer.");
    expect(block.textContent).toContain("Last good read:");
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
    expect(document.querySelector("[data-admin-line]")?.textContent).toBe("You administer 1 server through mcp-admin-midpoint. Adding a server needs straza-global-mcp-admin.");
  });

  it("leads each row with the server's tile and says the runtime and a pause in words", async () => {
    vi.mocked(listApps).mockResolvedValue([{ ...apps[0], paused: true }, apps[2]]);
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const cells = (name: string) => within(screen.getByRole("button", { name: "Open " + name })).getAllByRole("cell");
    expect(cells("midpoint")[0].querySelector('[data-kind-icon="server"]')).toBeTruthy();
    expect(cells("midpoint")[1].textContent).toBe("Streamable HTTP");
    expect(cells("filesystem")[1].textContent).toBe("stdio, a process Straza starts on the strazad host");
    expect(cells("filesystem")[1].querySelector("[title]")?.getAttribute("title")).toBe("stdio, a process Straza starts on the strazad host");
    expect((cells("filesystem")[1].querySelector("[title]") as HTMLElement).className).toContain("max-w-[220px]");
    expect(cells("midpoint")[2].textContent).toContain("paused");
    expect(cells("midpoint")[2].textContent).not.toContain("admin-paused");
  });

  it("names each role that reaches a server with its kind's icon, and a server no role reaches in a pill", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const reached = (name: string) => within(screen.getByRole("button", { name: "Open " + name })).getAllByRole("cell")[4];
    expect(reached("midpoint").querySelector('[data-role-kind-tag="dev"] svg')?.getAttribute("class")).toContain("lucide-briefcase-business");
    expect(reached("demo-tools").textContent).toBe("no role yet");
    expect(reached("demo-tools").querySelector(".rounded-pill")).toBeTruthy();
    // The tags wrap onto further lines, so the column is as wide as its
    // widest tag and the list fits its box at 1280 px.
    expect((reached("midpoint").querySelector("[data-reached-by]") as HTMLElement).className).toContain("flex-wrap");
  });

  it("keeps the list and plain role tags when the roles cannot be read", async () => {
    vi.mocked(listRoles).mockRejectedValue(Object.assign(new Error("forbidden"), { status: 403 }));
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const reached = within(screen.getByRole("button", { name: "Open midpoint" })).getAllByRole("cell")[4];
    expect(reached.textContent).toBe("dev");
    expect(reached.querySelector("svg")).toBeNull();
  });

  it("keeps Add MCP server and no admin line for a full grant", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(screen.getByRole("button", { name: /Add MCP server/ })).toBeTruthy();
    expect(document.querySelector("[data-admin-line]")).toBeNull();
  });
});
