import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import { ServerPage } from "./server-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import { catalogPreview, getConfig, listApps, listAudit, listBindings, listChanges, listRoles, listSecrets, listTools, listUsers } from "@/lib/api";
import { adminAreas } from "@/lib/session";

// The Tools tab of a server's page, beside the page's own suite: a tool
// several roles reach keeps its column as wide as its widest role tag.

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApps: vi.fn(), listTools: vi.fn(), listBindings: vi.fn(), listAudit: vi.fn(), listChanges: vi.fn(), listSecrets: vi.fn(), listRoles: vi.fn(), listUsers: vi.fn(),
  catalogPreview: vi.fn(), getConfig: vi.fn(),
}));
vi.mock("@/lib/session", () => ({ adminAreas: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/components/origin-line", () => ({ OriginLine: () => null }));

const app = {
  id: "app-7", name: "midpoint", runtime: "remote", version: "0", status: "running", detail: "", tools: ["get_user"], reached_by: ["midpoint-self-service", "midpoint-admin-tools"],
  last_probe_at: new Date().toISOString(), url: "http://midpoint:3001/mcp",
};

describe("the Tools tab of a server's page", () => {
  beforeEach(() => {
    vi.mocked(listApps).mockResolvedValue([app] as never);
    vi.mocked(listTools).mockResolvedValue([{ id: "t1", app: "midpoint", app_id: "app-7", name: "get_user", description: "Reads one user" }]);
    vi.mocked(listBindings).mockResolvedValue([
      { id: "b1", app: "midpoint", role: "midpoint-self-service", tools: ["get_user"] },
      { id: "b2", app: "midpoint", role: "midpoint-admin-tools", tools: ["*"] },
    ]);
    vi.mocked(listAudit).mockResolvedValue([]);
    vi.mocked(listChanges).mockResolvedValue({ changes: [] });
    vi.mocked(listSecrets).mockResolvedValue([]);
    vi.mocked(listRoles).mockResolvedValue([
      { id: "r1", name: "midpoint-self-service", kind: "application" }, { id: "r2", name: "midpoint-admin-tools", kind: "application" },
    ]);
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(getConfig).mockResolvedValue({});
    vi.mocked(adminAreas).mockReturnValue(null);
  });

  it("wraps the role tags of Reached by onto further lines, so the table fits its box", async () => {
    render(<TooltipProvider><ServerPage id="app-7" tab="tools" /></TooltipProvider>);
    const row = await waitFor(() => {
      const found = within(document.querySelector("[data-server-tools]") as HTMLElement).getAllByRole("row").find((r) => r.querySelector("td")?.textContent === "get_user");
      expect(found).toBeTruthy();
      return found as HTMLElement;
    });
    const reached = row.querySelectorAll("td")[2].querySelector("[data-reached-by]") as HTMLElement;
    expect(reached.className).toContain("flex-wrap");
    expect([...reached.querySelectorAll("[data-role-kind-tag]")].map((t) => t.textContent)).toEqual(["midpoint-admin-tools", "midpoint-self-service"]);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("midpoint");
  });
});
