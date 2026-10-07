import { describe, expect, it, vi } from "vitest";
import { render, within } from "@testing-library/react";
import { type Chip, RolesTable, serverChips } from "./roles-table";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { BindingRow, RoleRow } from "@/lib/api";
import { NOT_READ, REACH, TOOLS_NONE, toolsCell } from "@/lib/role-words";
import { ownedChip, ownedChipTitle } from "@/lib/server-roles-words";

// The Server and Tools cells of the Application table: every server the
// role reaches, the owned one marked with the
// ownership sentence on hover, and what the role gets on each. The owned
// server comes off the role row itself, so a server admin whose bindings
// read is refused still sees it.

const global: RoleRow = { id: "r-1", name: "dev-tools", kind: "application", description: "Tools of the developer sandbox.", holder_count: 3 };
const owned: RoleRow = { id: "r-2", name: "demo-tools-readers", kind: "application", description: "Read-only tools of demo-tools.", server: "demo-tools", tools: ["echo", "get-sum"], holder_count: 1 };
const bare: RoleRow = { id: "r-3", name: "new-tools", kind: "application", holder_count: 0 };

const bindings: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b2", app: "midpoint", role: "dev-tools", tools: ["get-user"] },
  { id: "b3", app: "demo-tools", role: "demo-tools-readers", tools: ["echo", "get-sum"] },
];
const totals = { "demo-tools": 4, midpoint: 27 };

const mount = (rows: RoleRow[], chips: Record<string, Chip[] | null>) => render(
  <TooltipProvider>
    <RolesTable kind="application" rows={rows} chips={chips} administers={{}} facets={{}} hidden={{}} onOpen={vi.fn()} onExport={vi.fn()} />
  </TooltipProvider>,
);

const cellsOf = (name: string) => within(document.querySelector('[data-role="' + name + '"]') as HTMLElement).getAllByRole("cell");

describe("the Server and Tools cells", () => {
  it.each<[string, RoleRow, BindingRow[] | null, string[], string | null, string]>([
    ["an owned role with the access rows read", owned, bindings, ["demo-tools"], "demo-tools", "echo and get-sum"],
    ["an owned role when the access rows could not be read", owned, null, ["demo-tools"], "demo-tools", "echo and get-sum"],
    ["a global role reaching two servers", global, bindings, ["demo-tools", "midpoint"], null, toolsCell([{ server: "demo-tools", words: "every tool (4), and tools added later" }, { server: "midpoint", words: "get-user" }])],
    ["a global role when the access rows could not be read", global, null, [], null, NOT_READ],
    ["an application role with no access row yet", bare, bindings, [], null, TOOLS_NONE],
  ])("%s", (_, role, rows, servers, marked, words) => {
    const chips = serverChips(role, rows, rows ? totals : {});
    mount([role], { [role.name]: chips });
    const [, , server, tools] = cellsOf(role.name);
    const names = Array.from(server.querySelectorAll("[data-server]")).map((c) => c.getAttribute("data-server"));
    expect(names).toEqual(servers);
    if (chips === null) expect(server.textContent).toBe(NOT_READ);
    if (chips !== null && servers.length === 0) expect(server.textContent).toBe(REACH.noServer);
    const mark = server.querySelector("[data-owned-by]");
    expect(mark ? mark.getAttribute("data-owned-by") : null).toBe(marked);
    if (marked) {
      expect(mark?.getAttribute("title")).toBe(ownedChipTitle(marked));
      // The pill says the ownership in words, so the check needs no legend.
      expect(mark?.textContent).toBe(ownedChip(marked));
      expect(mark?.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    }
    expect(tools.textContent).toBe(words);
  });

  it("marks the owned server once and nothing on a global role", () => {
    mount([global, owned], { [global.name]: serverChips(global, bindings, totals), [owned.name]: serverChips(owned, bindings, totals) });
    expect(document.querySelectorAll("[data-owned-by]")).toHaveLength(1);
    expect(cellsOf("dev-tools")[2].querySelector("[data-owned-by]")).toBeNull();
  });
});
