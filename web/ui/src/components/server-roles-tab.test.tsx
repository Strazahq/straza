import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServerRolesTab } from "./server-roles-tab";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type RoleRow, type ToolRow, catalogPreview, deleteRole, getPolicy, listBindings, listRoles } from "@/lib/api";
import { notify } from "@/lib/notify";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listBindings: vi.fn(),
  deleteRole: vi.fn(),
  createBinding: vi.fn(),
  removeBinding: vi.fn(),
  getPolicy: vi.fn(),
  catalogPreview: vi.fn(),
  listRoles: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
// The session may publish policy, so the sheet's editor asks every question
// it has for whoever opens it.
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: () => ({ user: "dana", roles: [], grants: "policy:write", expiresIn: 300, sessionID: "s1" }) }));

const app: AppRow = { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: [], tools: ["echo", "add", "get-sum"] };

const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "app-9", name: "echo", description: "Echoes the input back." },
  { id: "t2", app: "demo-tools", app_id: "app-9", name: "add", description: "Adds two numbers." },
  { id: "t3", app: "demo-tools", app_id: "app-9", name: "get-sum", description: "Sums a list of numbers." },
  { id: "t4", app: "midpoint", app_id: "app-2", name: "read-user" },
];

const readers = (holders: number): RoleRow => ({
  id: "r-1",
  name: "demo-tools-readers",
  kind: "application",
  description: "Read-only tools of demo-tools for analysts.",
  server: "demo-tools",
  tools: ["echo", "add"],
  holder_count: holders,
});

const changed = vi.fn();

function mount(roles: RoleRow[], globalAdmin = false, of: AppRow = app) {
  return render(
    <TooltipProvider>
      <ServerRolesTab app={of} roles={roles} tools={tools} globalAdmin={globalAdmin} onChanged={changed} />
    </TooltipProvider>,
  );
}

const row = () => document.querySelector('[data-owned-role="demo-tools-readers"]') as HTMLElement;
const line = () => document.querySelector('[data-role-line="demo-tools-readers"]') as HTMLElement;
const refusal = () => document.querySelector("[data-refused-error]") as HTMLElement;
// reach reads the Tools cell of the readers row: the headline, the state of
// each drawn cell in the server's own tool order, and the names line.
const reach = () => {
  const cell = within(row()).getAllByRole("cell")[1];
  const drawn = cell.querySelector("[data-reach]");
  return {
    head: cell.querySelector("b")?.parentElement?.textContent,
    cells: [...(drawn?.children || [])].map((c) => c.getAttribute("data-cell")),
    names: drawn?.nextElementSibling?.textContent,
  };
};
const heldRules = () => [...document.querySelectorAll("[data-held-rule]")].map((p) => p.textContent);

describe("the Roles tab of a server", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listBindings).mockResolvedValue([{ id: "b-1", app: "demo-tools", role: "demo-tools-readers", tools: ["echo", "add"] }]);
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(listRoles).mockResolvedValue([]);
  });

  it("says what a role made here is, with Add role as the one door", () => {
    mount([]);
    expect(screen.getByText("No role of demo-tools yet.")).toBeTruthy();
    expect(screen.getByText("A role made here reaches this server only, is named demo-tools- and a word of yours, and is assigned by your identity manager, not here. Give it the tools it should reach.")).toBeTruthy();
    expect(screen.getAllByRole("button", { name: /Add role/ })).toHaveLength(1);
  });

  it("renders the role, its tools, its holders and the line naming the identity manager", () => {
    mount([readers(0)]);
    expect(screen.getByText("1 role of this server")).toBeTruthy();
    // The identifier column reads Name, the word the Roles list uses.
    expect(screen.getAllByRole("columnheader")[0].textContent).toBe("Name");
    const cells = within(row()).getAllByRole("cell");
    expect(cells[0].textContent).toBe("demo-tools-readersRead-only tools of demo-tools for analysts.");
    // The description wraps under the name, so it is not cut and needs no tooltip to be read.
    expect(cells[0].querySelector(".truncate, [title]")).toBeNull();
    expect(reach()).toEqual({ head: "2 of 3tools", cells: ["on", "on", "off"], names: "echo, add" });
    expect(cells[1].getAttribute("title")).toBe("echo, add");
    expect(cells[2].textContent).toBe("0");
    expect(line().textContent).toBe("Nobody holds it yet. Your identity manager assigns it: in midPoint it is AR:demo-tools-readers within a sync cycle.");
    expect(within(row()).getByRole("button", { name: "Edit tools" }).getAttribute("aria-disabled")).toBeNull();
    expect(within(row()).getByRole("button", { name: "Delete" }).getAttribute("aria-disabled")).toBeNull();
  });

  // Each case is the matchers a role stores against the three tools the
  // server lists, echo, add and get-sum in that order, and what the Tools
  // cell draws for them.
  const reaches: [string, string[], string, string[], string | undefined][] = [
    ["some tools by name", ["echo", "get-sum"], "2 of 3tools", ["on", "off", "on"], "echo, get-sum"],
    ["every tool by name, which takes no tool added later", ["echo", "add", "get-sum"], "3 of 3tools", ["on", "on", "on"], "echo, add, get-sum"],
    ["every tool and tools added later, with one dashed cell at the end", ["*"], "every tool(3), and tools added later", ["on", "on", "on", "later"], "every tool the server lists, and tools added later"],
    ["a tool the server no longer lists, which is not counted", ["echo", "gone"], "1 of 3tools", ["on", "off", "off"], "echo"],
    ["no tool the server lists, with no names line", ["gone"], "0 of 3tools", ["off", "off", "off"], undefined],
  ];
  it.each(reaches)("draws the reach of a role with %s", (_case, matchers, head, cells, names) => {
    mount([{ ...readers(0), tools: matchers }]);
    expect(reach()).toEqual({ head, cells, names });
  });

  it("names each cell's tool on hover and keeps the cells out of the tab order and the accessibility tree", () => {
    mount([readers(0)]);
    const drawn = row().querySelector("[data-reach]") as HTMLElement;
    expect(drawn.getAttribute("aria-hidden")).toBe("true");
    expect([...drawn.children].map((c) => c.getAttribute("title"))).toEqual(["echo", "add", "get-sum"]);
    expect(drawn.querySelector("button, a, [tabindex]")).toBeNull();
    expect(within(row()).getAllByRole("button").map((b) => b.textContent)).toEqual(["Edit tools", "Delete"]);
  });

  it("counts one tool in the singular", () => {
    mount([{ ...readers(0), tools: ["ping"] }], false, { ...app, name: "solo", tools: ["ping"] });
    expect(reach().head).toBe("1 of 1tool");
  });

  it("says the role's tools in words when the server lists no tool to draw", () => {
    mount([readers(0)], false, { ...app, name: "sleeper", status: "stopped", tools: undefined });
    const cell = within(row()).getAllByRole("cell")[1];
    expect(cell.textContent).toBe("echo and add");
    expect(cell.getAttribute("title")).toBe("echo, add");
    expect(cell.querySelector("[data-reach]")).toBeNull();
  });

  // Each case is who reads the tab, and the rule as it holds for them: a
  // global admin may still change a held role's tools, its server admin may not.
  const rules: [string, boolean, string][] = [
    ["a server admin", false, "A held role keeps its tools and cannot be deleted until the identity manager removes the holders."],
    ["a global admin", true, "A held role keeps its tools for its server admin and cannot be deleted until the identity manager removes the holders."],
  ];
  it.each(rules)("states the held-role rule once under the table for %s, not under each held role", (_case, globalAdmin, rule) => {
    mount([readers(83), { ...readers(19), id: "r-2", name: "demo-tools-sandbox", tools: ["*"] }, { ...readers(0), id: "r-3", name: "demo-tools-ops" }], globalAdmin);
    expect(heldRules()).toEqual([rule]);
    expect([...document.querySelectorAll("[data-role-line]")].map((l) => l.getAttribute("data-role-line"))).toEqual(["demo-tools-ops"]);
    expect(within(row()).getAllByRole("cell")[2].textContent).toBe("83");
  });

  it("greys both acts on a held role for a server admin and says the server's sentence on a click", async () => {
    mount([readers(1)]);
    const edit = within(row()).getByRole("button", { name: "Edit tools" });
    const del = within(row()).getByRole("button", { name: "Delete" });
    expect(edit.getAttribute("aria-disabled")).toBe("true");
    expect(edit.getAttribute("title")).toBe("The role demo-tools-readers has 1 holder. Its tools change only by the global admin or by a new role.");
    expect(del.getAttribute("aria-disabled")).toBe("true");
    expect(del.getAttribute("title")).toBe("The role demo-tools-readers has 1 holder. The identity manager removes them first, then delete it.");
    expect(line()).toBeNull();
    expect(heldRules()).toEqual(["A held role keeps its tools and cannot be deleted until the identity manager removes the holders."]);

    await userEvent.click(edit);
    expect(refusal().textContent).toBe("Edit tools refused. The role demo-tools-readers has 1 holder. Its tools change only by the global admin or by a new role.");
    expect(listBindings).not.toHaveBeenCalled();

    await userEvent.click(del);
    expect(refusal().textContent).toBe("Delete refused. The role demo-tools-readers has 1 holder. The identity manager removes them first, then delete it.");
    expect(deleteRole).not.toHaveBeenCalled();
  });

  it("leaves Edit tools open to a global admin on a held role and keeps Delete refused", async () => {
    mount([readers(2)], true);
    const edit = within(row()).getByRole("button", { name: "Edit tools" });
    expect(edit.getAttribute("aria-disabled")).toBeNull();
    expect(edit.getAttribute("title")).toBe("2 holders were certified on the current list. Widening it changes what they were certified for.");
    expect(within(row()).getByRole("button", { name: "Delete" }).getAttribute("aria-disabled")).toBe("true");
    expect(line()).toBeNull();
    expect(heldRules()).toEqual(["A held role keeps its tools for its server admin and cannot be deleted until the identity manager removes the holders."]);

    await userEvent.click(edit);
    await screen.findByRole("dialog");
    expect(listBindings).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Edit the tools of demo-tools-readers")).toBeTruthy();
  });

  // Each case is who opens Edit tools on a role a global admin gave every
  // tool, and tools added later, and whether that switch is greyed for them.
  const cases: [string, boolean, string | null][] = [
    ["greys the switch for tools added later for the server's admin", false, "true"],
    ["leaves the switch for tools added later live for a global admin", true, null],
  ];
  it.each(cases)("%s", async (_case, globalAdmin, disabled) => {
    vi.mocked(listBindings).mockResolvedValue([{ id: "b-2", app: "demo-tools", role: "demo-tools-all", tools: ["*"] }]);
    mount([{ ...readers(0), id: "r-2", name: "demo-tools-all", tools: ["*"] }], globalAdmin);
    const all = document.querySelector('[data-owned-role="demo-tools-all"]') as HTMLElement;
    expect(within(all).getAllByRole("cell")[1].querySelector("b")?.parentElement?.textContent).toBe("every tool(3), and tools added later");
    await userEvent.click(within(all).getByRole("button", { name: "Edit tools" }));
    const later = await screen.findByRole("radio", { name: "Every tool, and tools added later" });
    expect(later.getAttribute("aria-checked")).toBe("true");
    expect(later.getAttribute("aria-disabled")).toBe(disabled);
  });

  it("deletes an unheld role through the confirm dialog and says what went", async () => {
    vi.mocked(deleteRole).mockResolvedValue({ status: "deleted", sets_off: ["demo-tools-readers-access"] });
    mount([readers(0)]);
    await userEvent.click(within(row()).getByRole("button", { name: "Delete" }));
    const ask = await screen.findByRole("alertdialog");
    expect(within(ask).getByText("Delete demo-tools-readers?")).toBeTruthy();
    expect(within(ask).getByText("It leaves Straza with its access row to demo-tools. Nobody holds it, so no session loses a tool.")).toBeTruthy();
    await userEvent.click(within(ask).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(deleteRole).toHaveBeenCalledWith("r-1"));
    expect(notify.ok).toHaveBeenCalledWith("demo-tools-readers is deleted, and its policy set demo-tools-readers-access is turned off.");
    expect(changed).toHaveBeenCalledTimes(1);
  });

  it("says what the server said when the access rows behind Edit tools are refused", async () => {
    vi.mocked(listBindings).mockRejectedValue(new ApiError("a server admin reads the access rows of their own servers", 403));
    mount([readers(0)]);
    await userEvent.click(within(row()).getByRole("button", { name: "Edit tools" }));
    await waitFor(() => expect(refusal()).toBeTruthy());
    expect(refusal().textContent).toBe("Edit tools refused. The server refused it: a server admin reads the access rows of their own servers. Fix what it names, then try again.");
  });
});
