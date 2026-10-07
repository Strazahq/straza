import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Palette, matches } from "./palette";
import { BASE } from "@/lib/router";
import { ROUTES } from "@/lib/routes";
import { paletteLine } from "@/lib/policy-words";

vi.mock("@/lib/api", () => ({
  listApps: vi.fn(async () => [
    { id: "a1", name: "midpoint", runtime: "remote", status: "running", tools: [], reached_by: [] },
    { id: "a2", name: "demo-tools", runtime: "remote", status: "degraded", tools: [], reached_by: [] },
    { id: "a3", name: "filesystem", runtime: "command", status: "unreachable", tools: [], reached_by: [] },
  ]),
  // The directory is searched on the server: carol answers to "car", nobody to "zebra".
  listUsers: vi.fn(async (q: string) => ({ items: q.includes("q=car") ? [{ id: "u-carol", username: "carol", display: "Carol Reyes" }] : [], next_cursor: "" })),
  listPolicies: vi.fn(async () => ({
    items: [
      { name: "org-baseline", status: "active", summary: { matchRoles: [] } },
      { name: "release-window", status: "draft", summary: { matchRoles: ["dev-tools", "sre-tools"] } },
    ],
  })),
  listRoles: vi.fn(async () => [
    { id: "r-dev", name: "dev", kind: "business", holder_count: 2 },
    { id: "r-dev-tools", name: "dev-tools", kind: "application", holder_count: 2 },
    { id: "r-sec", name: "sec-approvers", kind: "approver", holder_count: 1 },
  ]),
  query: (params: Record<string, string | number | undefined>) => Object.entries(params).filter(([, v]) => v !== undefined && v !== "").map(([k, v]) => k + "=" + String(v)).join("&"),
}));

const options = () => screen.queryAllByRole("option").map((o) => o.textContent || "");

describe("the palette match rule", () => {
  it.each([
    ["demo", "demo-tools", true],
    ["demo", "midpoint", false],
    ["mid", "midpoint", true],
    ["run", "midpoint", false],
    ["tools", "demo-tools", true],
    ["mcp s", "MCP servers", true],
    ["serv", "MCP servers", true],
    ["ers", "MCP servers", false],
    ["", "anything", true],
  ])("%s against %s reads %s", (query, name, want) => {
    expect(matches(query, name)).toBe(want);
  });
});

describe("the palette", () => {
  const onOpenChange = vi.fn();
  const onOpenServer = vi.fn();
  const onOpenUser = vi.fn();
  const onOpenRole = vi.fn();
  const props = { onOpenChange, routes: ROUTES, onOpenServer, onOpenUser, onOpenRole };

  beforeEach(() => {
    onOpenChange.mockReset();
    onOpenServer.mockReset();
    onOpenUser.mockReset();
    onOpenRole.mockReset();
    window.history.replaceState(null, "", BASE + "servers");
  });

  const filter: [string, string[], string[]][] = [
    ["demo", ["demo-tools"], []],
    ["mid", ["midpoint"], []],
    ["run", [], []],
    ["zebra", [], []],
    ["se", [], ["Sessions", "MCP servers", "Settings"]],
    ["file", ["filesystem"], []],
  ];
  it.each(filter)("typing %s lists the servers %j and the pages %j", async (query, servers, pages) => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), query);
    const rows = options();
    for (const s of servers) expect(rows.some((r) => r.startsWith(s))).toBe(true);
    for (const a of ["midpoint", "demo-tools", "filesystem"]) if (!servers.includes(a)) expect(rows.some((r) => r.startsWith(a))).toBe(false);
    for (const p of pages) expect(rows.some((r) => r.startsWith(p))).toBe(true);
    for (const r of ROUTES) if (!pages.includes(r.label)) expect(rows.some((row) => row.startsWith(r.label))).toBe(false);
    if (servers.length + pages.length === 0) expect(screen.getByText("Nothing matches what you typed.")).toBeTruthy();
    expect(screen.queryByText("Open this palette")).toBeNull();
    expect(screen.getByText("Matches the start of a word in a page, server, role or policy name; users are searched on the server")).toBeTruthy();
  });

  it("lists the roles that match with their kind and holders, and hands one to the caller on Enter", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "dev");
    const rows = options().filter((r) => r.startsWith("dev"));
    expect(rows).toEqual(["devbusiness2 holders", "dev-toolsapplication2 holders"]);
    expect(options().some((r) => r.startsWith("sec-approvers"))).toBe(false);
    expect(screen.getByText("Roles")).toBeTruthy();
    await userEvent.keyboard("{Enter}");
    expect(onOpenRole).toHaveBeenCalledWith(expect.objectContaining({ id: "r-dev", name: "dev" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("marks a role option with its kind's glyph, hidden from assistive technology", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "dev-tools");
    const option = await screen.findByRole("option", { name: /dev-tools/ });
    const glyph = option.querySelector("[data-kind-glyph]") as SVGElement;
    expect(glyph.getAttribute("data-kind-glyph")).toBe("application");
    expect(glyph.getAttribute("aria-hidden")).toBe("true");
  });

  it("finds a user through the server search and opens it on Enter", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "car");
    const carol = await screen.findByRole("option", { name: /carol/ });
    expect(carol.textContent).toBe("carolCarol Reyes");
    expect(screen.getByText("Users")).toBeTruthy();
    await userEvent.keyboard("{Enter}");
    expect(onOpenUser).toHaveBeenCalledWith(expect.objectContaining({ id: "u-carol", username: "carol" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("lists the shortcuts and every page while nothing is typed, and no page carries the not-built suffix now that every area is built", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    expect(screen.getByText("Open this palette")).toBeTruthy();
    expect(screen.getByText("Close the innermost overlay")).toBeTruthy();
    expect(screen.getByText("Move and open")).toBeTruthy();
    expect(screen.getByRole("option", { name: /^Approvals/ }).textContent).toBe("Approvals");
    expect(screen.getByRole("option", { name: /^MCP servers/ }).textContent).toBe("MCP servers");
    expect(screen.queryByText("not built yet")).toBe(null);
  });

  it("navigates to a page on Enter", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "users{Enter}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(window.location.pathname).toBe(BASE + "users");
  });

  it("hands a server to the caller on Enter", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "demo{Enter}");
    expect(onOpenServer).toHaveBeenCalledTimes(1);
    expect(onOpenServer.mock.calls[0][0].name).toBe("demo-tools");
  });

  it("finds a policy by name and opens its page on Enter", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "release");
    expect(options()).toEqual(["release-window" + paletteLine("draft", ["dev-tools", "sre-tools"])]);
    expect(screen.getByText("Policies")).toBeTruthy();
    await userEvent.keyboard("{Enter}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(window.location.pathname).toBe(BASE + "policies/release-window");
  });

  it("lists New policy as a page of its own and navigates to it", async () => {
    render(<Palette open {...props} />);
    await screen.findByRole("option", { name: /midpoint/ });
    await userEvent.type(screen.getByRole("combobox"), "new policy");
    expect(options()).toEqual(["Policies \u203a New policypolicies/new"]);
    await userEvent.keyboard("{Enter}");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(window.location.pathname).toBe(BASE + "policies/new");
  });

  it("opens on Ctrl K", async () => {
    render(<Palette open={false} {...props} />);
    await userEvent.keyboard("{Control>}k{/Control}");
    expect(onOpenChange).toHaveBeenCalledWith(true);
  });
});
