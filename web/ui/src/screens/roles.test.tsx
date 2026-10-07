import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Roles } from "./roles";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type BindingRow, type PoliciesAnswer, type RoleRow, type ToolRow, exportRole, listApps, listBindings, listPolicies, listRoles, listTools } from "@/lib/api";
import { navigate } from "@/lib/router";
import { downloadText } from "@/lib/utils";
import { CATEGORY, COLUMN, NOT_READ, NOT_READ_WHY, SEARCH_ROLES_BY, SUBJECT_POLICY_COUNTS, SUBJECT_REACH, SUMMARY, holdersTitle, mintedFold, notReadRefused, policiesTitle, roleFileName, sideRefused } from "@/lib/role-words";
import { adminAreas } from "@/lib/session";

// Seven roles in the four categories, in the shapes the roles list answers:
// an application role with two servers, one with four so the chips fold, a
// business role that composes, an approver with a pool, and three straza
// rows covering every console reading.
const roles: RoleRow[] = [
  { id: "r-dev-tools", name: "dev-tools", kind: "application", description: "Tool reach for the developer seat.", holder_count: 2, assigned_count: 0 },
  { id: "r-wide", name: "wide-tools", kind: "application", holder_count: 0, assigned_count: 0 },
  { id: "r-dev", name: "dev", kind: "business", description: "Developer seat.", holder_count: 2, assigned_count: 2, implies: ["dev-tools"] },
  { id: "r-sec", name: "sec-approvers", kind: "approver", description: "Approval deciders.", holder_count: 2, assigned_count: 2, decider_in: ["dev-guardrails", "prod-guardrails"] },
  { id: "r-auditor", name: "auditor", kind: "straza", description: "Read-only oversight.", holder_count: 0, assigned_count: 0, areas: ["audit:read", "sessions:read", "transcripts:read"] },
  { id: "r-admin", name: "straza-admin", kind: "straza", description: "Straza administration", holder_count: 2, assigned_count: 2, areas: ["full"] },
  { id: "r-enroll", name: "straza-enroll-mobile", kind: "straza", description: "May enroll their own phone.", holder_count: 2, assigned_count: 2, areas: [] },
];

// The rows delegated MCP administration adds: the
// product role that reaches every server, the role minted with a server that
// somebody holds, and two minted roles nobody holds yet.
const mcpAdmin: RoleRow = { id: "r-mcp-admin", name: "straza-global-mcp-admin", kind: "straza", description: "Administers every MCP server.", holder_count: 1, assigned_count: 1, areas: ["apps:read", "apps:write"] };
const held: RoleRow = { id: "r-m-jira", name: "mcp-admin-finance-jira", kind: "straza", description: "Administers the MCP server finance/jira.", holder_count: 1, assigned_count: 1, areas: [] };
const unheld = (server: string): RoleRow => ({ id: "r-m-" + server, name: "mcp-admin-" + server, kind: "straza", description: "Administers the MCP server " + server + ".", holder_count: 0, assigned_count: 0, areas: [] });
const minted = [mcpAdmin, held, unheld("demo-tools"), unheld("sales-crm")];

const bindings: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b2", app: "midpoint", role: "dev-tools", tools: ["get-user", "set-user"] },
  { id: "b3", app: "alpha", role: "wide-tools", tools: ["*"] },
  { id: "b4", app: "beta", role: "wide-tools", tools: ["*"] },
  { id: "b5", app: "gamma", role: "wide-tools", tools: ["*"] },
  { id: "b6", app: "delta", role: "wide-tools", tools: ["*"] },
];

const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "a1", name: "echo" },
  { id: "t2", app: "demo-tools", app_id: "a1", name: "get-sum" },
  { id: "t3", app: "midpoint", app_id: "a2", name: "get-user" },
];

// The servers, each naming its admin role: which is how the list tells a
// minted role from a hand-made one, never by the name.
const apps: AppRow[] = [
  { id: "a1", name: "demo-tools", runtime: "remote", status: "running", reached_by: [], admin_role: "mcp-admin-demo-tools", admin_role_id: "r-m-demo-tools" },
  { id: "a2", name: "midpoint", runtime: "remote", status: "running", reached_by: [] },
  { id: "a3", name: "finance/jira", runtime: "remote", status: "running", reached_by: [], admin_role: "mcp-admin-finance-jira", admin_role_id: "r-m-jira" },
];

const facets = { items: [], roles: [{ role: "dev-tools", sets: 1 }, { role: "dev", sets: 0 }] } as PoliciesAnswer;

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listRoles: vi.fn(),
  listBindings: vi.fn(),
  listTools: vi.fn(),
  listApps: vi.fn(),
  listPolicies: vi.fn(),
  exportRole: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
// The cells key on the server's answer, never on the session's standing,
// which the cases set to a seat without the apps area to prove it.
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), adminAreas: vi.fn(() => ({ identity: true })) }));
vi.mock("@/lib/utils", async (orig) => ({ ...(await orig<typeof import("@/lib/utils")>()), downloadText: vi.fn() }));


const mount = () => render(<TooltipProvider><Roles /></TooltipProvider>);
const ready = () => screen.findByRole("button", { name: "Open dev-tools" });
const pick = (kind: keyof typeof CATEGORY | "all") => userEvent.click(screen.getByRole("button", { name: new RegExp("^" + (kind === "all" ? "All roles" : CATEGORY[kind].title)) }));
const row = (name: string) => screen.getByRole("button", { name: "Open " + name });
const cells = (name: string) => within(row(name)).getAllByRole("cell");
const shownNames = () => [...document.querySelectorAll("[data-role]")].map((r) => r.getAttribute("data-role"));

describe("the Roles list", () => {
  beforeEach(() => {
    vi.mocked(listRoles).mockReset().mockResolvedValue(roles);
    vi.mocked(listBindings).mockReset().mockResolvedValue(bindings);
    vi.mocked(listTools).mockReset().mockResolvedValue(tools);
    vi.mocked(listApps).mockReset().mockResolvedValue(apps);
    vi.mocked(listPolicies).mockReset().mockResolvedValue(facets);
    vi.mocked(exportRole).mockReset().mockResolvedValue("apiVersion: straza.dev/v1\n");
    vi.mocked(navigate).mockClear(); vi.mocked(downloadText).mockClear();
  });

  it("starts with every role, including unheld server administrators, without descriptions or a fold", async () => {
    vi.mocked(listRoles).mockResolvedValue([...roles, ...minted]);
    mount(); await ready();
    expect(shownNames()).toEqual([...roles, ...minted].map((r) => r.name));
    expect(screen.getByRole("button", { name: /^All roles/ }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.queryByRole("columnheader", { name: COLUMN.description })).toBeNull();
    expect(document.querySelector("[data-minted-fold]")).toBeNull();
    expect(cells("mcp-admin-demo-tools")[2].textContent).toBe("Administers demo-tools");
    expect(cells("mcp-admin-demo-tools")[3].textContent).toBe("0");
    expect(screen.queryByText(roles[0].description!)).toBeNull();
  });

  it.each(["application", "business", "approver", "straza"] as const)("selects %s with its explanation and descriptions", async (kind) => {
    mount(); await ready(); await pick(kind);
    expect(shownNames()).toEqual(roles.filter((r) => r.kind === kind).map((r) => r.name));
    expect(screen.getByRole("heading", { name: CATEGORY[kind].title })).toBeTruthy();
    expect(screen.getByText(CATEGORY[kind].line)).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: COLUMN.description })).toBeTruthy();
    const desc = roles.find((r) => r.kind === kind)?.description;
    expect(screen.getByText(desc!).getAttribute("title")).toBe(desc);
    await pick("all"); expect(shownNames()).toHaveLength(roles.length);
  });

  it("keeps empty categories selectable and distinguishes filtered empty states", async () => {
    vi.mocked(listRoles).mockResolvedValue(roles.filter((r) => r.kind !== "business"));
    mount(); await ready(); await pick("business");
    expect(screen.getByText(CATEGORY.business.none)).toBeTruthy();
    await userEvent.type(screen.getByRole("textbox", { name: SEARCH_ROLES_BY }), "no such role");
    expect(screen.getByText(CATEGORY.business.noMatch)).toBeTruthy();
    await pick("all"); expect(screen.getByText("No roles match this view.")).toBeTruthy();
  });

  it("uses consistent text for configured access, composition, decisions and administration", async () => {
    mount(); await ready();
    expect(cells("dev-tools")[2].textContent).toContain("demo-tools: every tool (2), and tools added later");
    expect(cells("dev-tools")[2].textContent).toContain("midpoint: get-user and set-user");
    expect(cells("dev")[2].textContent).toBe("Includes dev-tools");
    expect(cells("sec-approvers")[2].textContent).toBe("Decides approval requests");
    expect(cells("straza-admin")[2].textContent).toBe("All console areas");
    expect(cells("auditor")[2].textContent).toBe("Console: audit, sessions, transcripts");
    expect(cells("straza-enroll-mobile")[2].textContent).toBe("No console area grants");
  });

  it("retains server and tool detail in Application roles", async () => {
    mount(); await ready(); await pick("application");
    expect(within(cells("dev-tools")[2]).getByText("demo-tools")).toBeTruthy();
    expect(within(cells("dev-tools")[2]).getByText("midpoint")).toBeTruthy();
    expect(cells("dev-tools")[3].textContent).toContain("get-user and set-user");
    expect(cells("wide-tools")[2].querySelector("[data-more-reach]")?.getAttribute("title")).toBe("gamma, delta");
  });

  it("keeps actual holder and policy counts with their source details", async () => {
    mount(); await ready();
    expect(cells("dev-tools")[3].getAttribute("title")).toBe(holdersTitle(2, 0));
    expect(cells("dev")[3].getAttribute("title")).toBe(holdersTitle(2, 2));
    expect(cells("dev-tools")[4].getAttribute("title")).toBe(policiesTitle(1));
    expect(cells("sec-approvers")[4].getAttribute("title")).toBe(policiesTitle(2, ["dev-guardrails", "prod-guardrails"]));
  });

  it("searches descriptions even when All roles hides them, and retains the query between categories", async () => {
    mount(); await ready(); await userEvent.type(screen.getByRole("textbox", { name: SEARCH_ROLES_BY }), "oversight");
    expect(shownNames()).toEqual(["auditor"]);
    await pick("straza"); expect(shownNames()).toEqual(["auditor"]);
    await pick("application"); expect(screen.getByText(CATEGORY.application.noMatch)).toBeTruthy();
  });

  it.each(["description", "server", "tools", "holders", "policies", "reach"] as const)("preserves the %s column choice across views", async (id) => {
    mount(); await ready(); await pick(id === "reach" ? "business" : "application");
    await userEvent.click(screen.getByRole("button", { name: /Columns/ }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: COLUMN[id] }));
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("columnheader", { name: COLUMN[id] })).toBeNull();
    await pick("all");
    if (["holders", "policies", "reach"].includes(id)) expect(screen.queryByRole("columnheader", { name: COLUMN[id] })).toBeNull();
  });

  it("opens rows by keyboard and exports through the real API without opening the row", async () => {
    mount(); await ready(); row("dev").focus(); await userEvent.keyboard("{Enter}");
    expect(navigate).toHaveBeenCalledWith("roles", ["r-dev"]);
    vi.mocked(navigate).mockClear();
    await userEvent.click(within(row("dev-tools")).getByRole("button", { name: /Export/ }));
    await waitFor(() => expect(exportRole).toHaveBeenCalledWith("r-dev-tools"));
    expect(downloadText).toHaveBeenCalledWith(roleFileName("dev-tools"), "apiVersion: straza.dev/v1\n", "application/yaml");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("marks unavailable side reads without inventing zero access or policy counts", async () => {
    vi.mocked(listBindings).mockRejectedValue(new ApiError("offline", 503));
    vi.mocked(listApps).mockRejectedValue(new ApiError("offline", 503));
    vi.mocked(listPolicies).mockRejectedValue(new ApiError("offline", 503));
    mount(); await ready();
    expect(cells("dev-tools")[2].textContent).toBe(SUMMARY.toolsNotRead);
    expect(cells("straza-enroll-mobile")[2].textContent).toBe(SUMMARY.adminNotRead);
    expect(cells("dev-tools")[4].textContent).toBe(NOT_READ);
    expect(cells("dev-tools")[4].querySelector("[title]")?.getAttribute("title")).toBe(NOT_READ_WHY);
    expect(document.body.textContent).not.toContain("lacks");
    expect(document.querySelectorAll("[data-fetch-error]").length).toBeGreaterThan(0);
  });

  it("keeps the reload words for a server admin without the apps area whose side reads fail with a 503", async () => {
    expect(adminAreas()).toEqual({ identity: true });
    vi.mocked(listTools).mockRejectedValue(new ApiError("offline", 503));
    vi.mocked(listBindings).mockRejectedValue(new ApiError("offline", 503));
    vi.mocked(listPolicies).mockRejectedValue(new ApiError("offline", 503));
    mount(); await ready();
    expect(cells("dev-tools")[2].textContent).toBe(SUMMARY.toolsNotRead);
    expect(cells("dev-tools")[4].querySelector("[title]")?.getAttribute("title")).toBe(NOT_READ_WHY);
    await pick("application");
    const app = within(row("dev-tools")).getAllByRole("cell");
    expect(app.some((c) => c.querySelector("[title]")?.getAttribute("title") === NOT_READ_WHY)).toBe(true);
    expect(document.body.textContent).not.toContain("lacks");
    expect(document.querySelectorAll("[data-side-refused]").length).toBe(0);
    expect(document.querySelectorAll("[data-fetch-error]").length).toBe(2);
  });

  it("names the grant, and offers no reload, where the server refuses the side reads", async () => {
    const refusedRead = new ApiError("session lacks grant apps:read", 403);
    vi.mocked(listBindings).mockRejectedValue(refusedRead);
    vi.mocked(listApps).mockRejectedValue(refusedRead);
    vi.mocked(listPolicies).mockRejectedValue(new ApiError("session lacks grant policy:read", 403));
    mount(); await ready();
    expect(cells("dev-tools")[2].textContent).toBe(SUMMARY.toolsRefused);
    expect(cells("straza-enroll-mobile")[2].textContent).toBe(SUMMARY.adminRefused);
    expect(cells("dev-tools")[4].querySelector("[title]")?.getAttribute("title")).toBe(notReadRefused("policy:read"));
    await pick("application");
    const app = within(row("dev-tools")).getAllByRole("cell");
    expect(app.some((c) => c.querySelector("[title]")?.getAttribute("title") === notReadRefused("apps:read"))).toBe(true);
    // The notices over the table name the grant too, and none of them
    // calls the server unreachable or offers a reload.
    const notices = [...document.querySelectorAll("[data-side-refused]")].map((n) => n.textContent);
    expect(notices).toEqual([sideRefused(SUBJECT_REACH) + " " + notReadRefused("apps:read"), sideRefused(SUBJECT_POLICY_COUNTS) + " " + notReadRefused("policy:read")]);
    expect(document.querySelectorAll("[data-fetch-error]").length).toBe(0);
    expect(document.body.textContent).not.toMatch(/Reload to try again|unreachable/);
  });

  it("retains rows after a failed reload and reports a failed first read", async () => {
    const view = mount(); await ready();
    vi.mocked(listRoles).mockRejectedValue(new ApiError("offline", 503));
    await userEvent.click(screen.getByRole("button", { name: /Reload the list/ }));
    await screen.findByRole("status"); expect(row("dev-tools")).toBeTruthy();
    view.unmount(); mount(); await screen.findByRole("alert"); expect(screen.queryByRole("button", { name: "Open dev-tools" })).toBeNull();
  });

  it("folds unheld server administrators only inside the Straza category", async () => {
    vi.mocked(listRoles).mockResolvedValue([...roles, ...minted]);
    mount(); await ready(); await pick("straza");
    expect(screen.queryByRole("button", { name: "Open mcp-admin-demo-tools" })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: mintedFold(2) }));
    expect(row("mcp-admin-demo-tools")).toBeTruthy();
    await userEvent.type(screen.getByRole("textbox", { name: SEARCH_ROLES_BY }), "mcp-admin-demo-tools");
    expect(shownNames()).toEqual(["mcp-admin-demo-tools"]); expect(document.querySelector("[data-minted-fold]")).toBeNull();
  });

  it("renders names and descriptions as text, including in the category view", async () => {
    vi.mocked(listRoles).mockResolvedValue([{ ...roles[0], name: "<b>role</b>", description: "Tools for <b>everyone</b>" }]);
    mount(); await screen.findByRole("button", { name: "Open <b>role</b>" }); await pick("application");
    expect(screen.getByText("Tools for <b>everyone</b>")).toBeTruthy(); expect(row("<b>role</b>").querySelector("b")).toBeNull();
  });
});
