import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import type { ColumnDef } from "@tanstack/react-table";
import { DataTable, plain } from "./data-table";

type Fruit = { id: string; kind: string; colour: string };

const FRUIT: Fruit[] = [
  { id: "apple", kind: "pome", colour: "red" },
  { id: "pear", kind: "pome", colour: "green" },
  { id: "plum", kind: "stone", colour: "violet" },
];

const columns: ColumnDef<Fruit>[] = [
  { id: "name", header: plain<Fruit>("Name"), cell: ({ row }) => row.original.id },
  { id: "colour", header: plain<Fruit>("Colour"), cell: ({ row }) => row.original.colour },
  { id: "kind", header: plain<Fruit>("Kind"), cell: ({ row }) => row.original.kind },
];

// The two ways a caller draws a group: one cell across the table, or one
// cell under each shown column.
const header = (key: string, rows: Fruit[]) => key + ", " + rows.length;
const cell = (column: string, key: string, rows: Fruit[]) => column + " of " + key + ", " + rows.length;

const mount = (groups: Parameters<typeof DataTable<Fruit>>[0]["groups"], ticks: boolean, hidden: string[]) => render(
  <DataTable<Fruit>
    rows={FRUIT}
    columns={columns}
    labels={{ name: "Name", colour: "Colour", kind: "Kind" }}
    rowKey={(f) => f.id}
    rowName={(f) => f.id}
    dataAttr="data-fruit"
    count={(n) => n + " shown"}
    emptyText="No fruit."
    groups={groups}
    selection={ticks ? { selected: new Set(), onChange: () => {}, label: (f) => "Select " + f.id } : undefined}
    hiddenByDefault={hidden}
  />,
);

// groupRows reads each group row as its cells: the column a cell sits
// under, how many columns it spans and its words.
const groupRows = () => [...document.querySelectorAll("tr[data-group]")].map((tr) => [...tr.children].map((td) => [td.getAttribute("data-column"), (td as HTMLTableCellElement).colSpan, td.textContent]));

describe("the group rows of the data table", () => {
  it.each([
    ["with no checkbox column", false, 3],
    ["with the checkbox column", true, 4],
  ])("spans the header across every column %s", (_case, ticks, span) => {
    mount({ key: (f) => f.kind, header, collapsed: new Set() }, ticks, []);
    expect(groupRows()).toEqual([[[null, span, "pome, 2"]], [[null, span, "stone, 1"]]]);
  });

  it.each([
    ["every column shown", false, [], ["name", "colour", "kind"]],
    ["the checkbox column leading", true, [], ["select", "name", "colour", "kind"]],
    ["a hidden column left out", true, ["colour"], ["select", "name", "kind"]],
  ])("lays a group on the columns with %s", (_case, ticks, hidden, shown) => {
    mount({ key: (f) => f.kind, cell, collapsed: new Set(["stone"]) }, ticks, hidden);
    expect(groupRows()).toEqual([
      shown.map((id) => [id, 1, id + " of pome, 2"]),
      shown.map((id) => [id, 1, id + " of stone, 1"]),
    ]);
    // The cells sit under the headers one for one, and a folded group keeps
    // its header row while its rows leave the table.
    expect(document.querySelectorAll("thead th").length).toBe(shown.length);
    expect([...document.querySelectorAll("tr[data-fruit]")].map((tr) => tr.getAttribute("data-fruit"))).toEqual(["apple", "pear"]);
  });
});

// drawn counts how often each fruit's name cell is drawn.
const drawn = new Map<string, number>();
const counted: ColumnDef<Fruit>[] = [
  { id: "name", header: plain<Fruit>("Name"), cell: ({ row }) => { drawn.set(row.original.id, (drawn.get(row.original.id) || 0) + 1); return row.original.id; } },
  columns[1],
];
const KIWI: Fruit = { id: "kiwi", kind: "berry", colour: "brown" };
const LABELS = { name: "Name", colour: "Colour" };

const list = (rows: Fruit[], stable: boolean, openKey: string | null = null, onOpen: (f: Fruit) => void = () => {}, cols = counted) => (
  <DataTable<Fruit>
    rows={rows}
    columns={cols}
    labels={LABELS}
    rowKey={(f) => f.id}
    rowName={(f) => f.id}
    dataAttr="data-fruit"
    count={(n) => n + " shown"}
    emptyText="No fruit."
    onOpen={onOpen}
    openKey={openKey}
    stableRows={stable}
  />
);

describe("the stable rows of the data table", () => {
  it("draws only the row that joins when stableRows is set", () => {
    const { rerender } = render(list(FRUIT, true));
    drawn.clear();
    rerender(list([...FRUIT, KIWI], true));
    expect([...drawn]).toEqual([["kiwi", 1]]);
  });

  it("draws every row again when stableRows is unset, so a list whose cells read the clock stays fresh", () => {
    const { rerender } = render(list(FRUIT, false));
    drawn.clear();
    rerender(list(FRUIT, false));
    expect([...drawn.keys()]).toEqual(["apple", "pear", "plum"]);
  });

  it("draws the row that opens and the row that closes, and every row when the columns change", () => {
    const { rerender } = render(list(FRUIT, true));
    drawn.clear();
    rerender(list(FRUIT, true, "apple"));
    expect([...drawn.keys()]).toEqual(["apple"]);
    drawn.clear();
    rerender(list(FRUIT, true, "pear"));
    expect([...drawn.keys()]).toEqual(["apple", "pear"]);
    drawn.clear();
    rerender(list(FRUIT, true, "pear", () => {}, [...counted]));
    expect([...drawn.keys()]).toEqual(["apple", "pear", "plum"]);
  });

  it("opens a row it skipped through the onOpen the caller passed last", () => {
    const opened: string[] = [];
    const { rerender } = render(list(FRUIT, true, null, () => opened.push("old")));
    rerender(list(FRUIT, true, null, (f) => opened.push("new " + f.id)));
    fireEvent.click(screen.getByRole("button", { name: "Open plum" }));
    expect(opened).toEqual(["new plum"]);
  });
});
