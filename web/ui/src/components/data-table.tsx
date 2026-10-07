import * as React from "react";
import {
  type ColumnDef,
  type SortingState,
  type VisibilityState,
  flexRender,
  getCoreRowModel,
  getFilteredRowModel,
  getSortedRowModel,
  useReactTable,
} from "@tanstack/react-table";
import { ArrowDownIcon, ArrowUpDownIcon, ArrowUpIcon, Columns3Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { HelpTip } from "@/components/help-tip";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

// The list of the Users, Sessions, Audit and Transcripts screens and the
// MCP servers list: a filter row above the table with the count and the
// Columns menu on its right, a sticky header whose sortable columns are the
// sort control, hairline rows a click or Enter opens, and an optional
// checkbox column whose selection lives on the rendered rows only.

export const HEAD = "text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";

// SortHead is a column header that toggles its sort on click and shows the
// direction it is sorted by.
export function SortHead({ label, sorted, onClick }: { label: string; sorted: false | "asc" | "desc"; onClick: () => void }) {
  const Icon = sorted === "asc" ? ArrowUpIcon : sorted === "desc" ? ArrowDownIcon : ArrowUpDownIcon;
  return (
    <Button variant="ghost" size="sm" className={cn("-ml-3 h-8 gap-1", HEAD)} onClick={onClick} aria-label={"Sort by " + label.toLowerCase()} data-sorted={sorted || "none"}>
      {label}
      <Icon className={sorted ? "size-3.5 text-foreground" : "size-3.5 opacity-50"} />
    </Button>
  );
}

// sortable is the header renderer of a sortable column. help is the one
// sentence a help icon beside the header carries.
export function sortable<T>(label: string, help?: string): ColumnDef<T>["header"] {
  const render: ColumnDef<T>["header"] = ({ column }) => (
    <span className="inline-flex items-center">
      <SortHead label={label} sorted={column.getIsSorted()} onClick={() => column.toggleSorting(column.getIsSorted() === "asc")} />
      {help && <HelpTip label={label} text={help} />}
    </span>
  );
  return render;
}

// plain is the header renderer of a column that does not sort, with the
// same optional help icon.
export function plain<T>(label: string, help?: string): ColumnDef<T>["header"] {
  const render: ColumnDef<T>["header"] = () => (
    <span className={cn(HEAD, "inline-flex items-center gap-1")}>
      {label}
      {help && <HelpTip label={label} text={help} />}
    </span>
  );
  return render;
}

export type SortState = { id: string; desc: boolean } | null;

type Selection<T> = {
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
  // label names a row for its checkbox ("Select session 0199…").
  label: (row: T) => string;
  // selectable says which rows can be ticked: the others show no box, and
  // the header box reaches the selectable rows alone.
  selectable?: (row: T) => boolean;
};

// Groups puts consecutive rows under one header each, keyed by a value of
// the row, with a collapsed set the caller owns. A collapsed group's rows
// leave the screen and the selection alike. A header is one cell across
// every column. A caller that gives cell instead lays the group on the
// table's own columns: cell draws each shown column by its id, the
// checkbox column "select" among them.
type Groups<T> = {
  key: (row: T) => string;
  collapsed: Set<string>;
} & (
  | { header: (key: string, rows: T[]) => React.ReactNode }
  | { cell: (column: string, key: string, rows: T[]) => React.ReactNode }
);

type Props<T> = {
  rows: T[];
  columns: ColumnDef<T>[];
  // labels are the Columns menu words, by column id.
  labels: Record<string, string>;
  rowKey: (row: T) => string;
  // rowName is the accessible name of the row's door: "Open <rowName>".
  rowName: (row: T) => string;
  // dataAttr is the attribute the row carries with rowName, for tests and
  // walks: data-server, data-user, data-session, data-seq.
  dataAttr: string;
  onOpen?: (row: T) => void;
  // openKey marks the row whose sheet is open.
  openKey?: string | null;
  // filterBar are the controls of the filter row, left of the count.
  filterBar?: React.ReactNode;
  // count words the shown rows for the filter row's right side.
  count: (shown: number) => string;
  emptyText: string;
  // globalFilter narrows the rows in the browser, for a list the browser
  // holds whole; a paged list filters on the server and leaves it unset.
  globalFilter?: string;
  // serverSort hands the sort to the caller, which asks the server; without
  // it the table sorts the rows it holds.
  serverSort?: { state: SortState; onChange: (s: SortState) => void };
  defaultSort?: SortingState;
  selection?: Selection<T>;
  groups?: Groups<T>;
  // widths fixes the layout: one width per column id, so the header boxes
  // stay put across every state of the table, folded, open, empty or full.
  widths?: Record<string, string>;
  hiddenByDefault?: string[];
  foot?: React.ReactNode;
  rowClassName?: (row: T) => string;
};

// DataTable renders one list. Rows are buttons named "Open <rowName>" so a
// click or Enter opens the row; a checkbox click never does.
export function DataTable<T>({ rows, columns, labels, rowKey, rowName, dataAttr, onOpen, openKey, filterBar, count, emptyText, globalFilter = "", serverSort, defaultSort, selection, groups, widths, hiddenByDefault = [], foot, rowClassName }: Props<T>) {
  const [sorting, setSorting] = React.useState<SortingState>(() => (serverSort ? (serverSort.state ? [serverSort.state] : []) : defaultSort || []));
  const [columnVisibility, setColumnVisibility] = React.useState<VisibilityState>(() => Object.fromEntries(hiddenByDefault.map((id) => [id, false])));

  const onSortingChange = (updater: SortingState | ((old: SortingState) => SortingState)) => {
    const next = typeof updater === "function" ? updater(sorting) : updater;
    setSorting(next);
    if (serverSort) serverSort.onChange(next[0] ? { id: next[0].id, desc: next[0].desc } : null);
  };

  const onScreen = (row: T) => !groups || !groups.collapsed.has(groups.key(row));
  const canPick = (row: T) => onScreen(row) && (!selection?.selectable || selection.selectable(row));
  const cols = React.useMemo<ColumnDef<T>[]>(() => {
    if (!selection) return columns;
    const check: ColumnDef<T> = {
      id: "select",
      header: () => {
        const keys = rows.filter(canPick).map(rowKey);
        const all = keys.length > 0 && keys.every((k) => selection.selected.has(k));
        return (
          <input
            type="checkbox"
            aria-label={all ? "Clear the selection" : "Select every shown row"}
            checked={all}
            onChange={() => selection.onChange(all ? new Set() : new Set(keys))}
            className="size-4 accent-primary"
          />
        );
      },
      cell: ({ row }) => {
        if (!canPick(row.original)) return null;
        const k = rowKey(row.original);
        return (
          <input
            type="checkbox"
            aria-label={selection.label(row.original)}
            checked={selection.selected.has(k)}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => e.stopPropagation()}
            onChange={() => {
              const next = new Set(selection.selected);
              if (next.has(k)) next.delete(k); else next.add(k);
              selection.onChange(next);
            }}
            className="size-4 accent-primary"
          />
        );
      },
      enableHiding: false,
      enableSorting: false,
    };
    return [check, ...columns];
  }, [columns, selection, rows, rowKey, groups?.collapsed]); // eslint-disable-line react-hooks/exhaustive-deps

  // A selection never outlives the rows it was made on: a filter change
  // that hides a row, a poll that ends one, or a group folding away, drops
  // it from the set.
  React.useEffect(() => {
    if (!selection) return;
    const keys = new Set(rows.filter(canPick).map(rowKey));
    const kept = new Set([...selection.selected].filter((k) => keys.has(k)));
    if (kept.size !== selection.selected.size) selection.onChange(kept);
  }, [rows, groups?.collapsed]); // eslint-disable-line react-hooks/exhaustive-deps

  const table = useReactTable({
    data: rows,
    columns: cols,
    state: { sorting, globalFilter, columnVisibility },
    onSortingChange,
    onColumnVisibilityChange: setColumnVisibility,
    manualSorting: !!serverSort,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    globalFilterFn: "includesString",
  });

  const shown = table.getRowModel().rows;

  return (
    <div className="flex flex-col gap-3" data-data-table>
      <div className="flex flex-wrap items-center gap-2">
        {filterBar}
        <span className="text-[13px] text-muted-foreground" data-row-count>{count(shown.length)}</span>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" className="ml-auto h-9">
              <Columns3Icon /> Columns
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuLabel>Shown columns</DropdownMenuLabel>
            <DropdownMenuSeparator />
            {table.getAllLeafColumns().filter((c) => c.getCanHide()).map((c) => (
              <DropdownMenuCheckboxItem key={c.id} checked={c.getIsVisible()} onCheckedChange={(v) => c.toggleVisibility(!!v)}>
                {labels[c.id] || c.id}
              </DropdownMenuCheckboxItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      <div className="overflow-x-auto rounded-md border border-border bg-card">
        <Table className={widths ? "table-fixed" : undefined}>
          {widths && (
            <colgroup>
              {table.getVisibleLeafColumns().map((c) => <col key={c.id} style={{ width: widths[c.id] }} />)}
            </colgroup>
          )}
          <TableHeader>
            {table.getHeaderGroups().map((hg) => (
              <TableRow key={hg.id} className="hover:bg-transparent">
                {hg.headers.map((h) => (
                  <TableHead key={h.id} className={cn("h-10 whitespace-nowrap", h.column.id === "select" && "w-9")}>
                    {h.isPlaceholder ? null : flexRender(h.column.columnDef.header, h.getContext())}
                  </TableHead>
                ))}
              </TableRow>
            ))}
          </TableHeader>
          <TableBody>
            {shown.length === 0 && (
              <TableRow>
                <TableCell colSpan={cols.length} className="h-16 text-center text-muted-foreground" data-empty-text>{emptyText}</TableCell>
              </TableRow>
            )}
            {shown.map((row, i) => {
              const key = rowKey(row.original);
              const name = rowName(row.original);
              const attrs: Record<string, string> = { [dataAttr]: name };
              // A group header opens each run of rows that share a key, and
              // a folded group shows its header alone.
              const g = groups ? groups.key(row.original) : null;
              const opens = groups && (i === 0 || groups.key(shown[i - 1].original) !== g);
              const members = opens ? shown.filter((r) => groups.key(r.original) === g).map((r) => r.original) : [];
              const head = opens && g !== null ? (
                <TableRow key={"group:" + g} data-group={g} className="bg-secondary/40 hover:bg-secondary/40">
                  {"cell" in groups ? table.getVisibleLeafColumns().map((c) => (
                    <TableCell key={c.id} data-column={c.id} className="py-1.5">{groups.cell(c.id, g, members)}</TableCell>
                  )) : (
                    <TableCell colSpan={cols.length} className="py-1.5">
                      {groups.header(g, members)}
                    </TableCell>
                  )}
                </TableRow>
              ) : null;
              if (groups && g !== null && groups.collapsed.has(g)) return head;
              const line = (
                <TableRow
                  key={key}
                  {...attrs}
                  tabIndex={onOpen ? 0 : undefined}
                  role={onOpen ? "button" : undefined}
                  aria-label={onOpen ? "Open " + name : undefined}
                  data-open={openKey === key ? "true" : undefined}
                  className={cn("text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50", onOpen && "cursor-pointer", openKey === key && "bg-accent-bg", rowClassName && rowClassName(row.original))}
                  onClick={() => onOpen && onOpen(row.original)}
                  onKeyDown={(e) => { if (onOpen && (e.key === "Enter" || e.key === " ")) { e.preventDefault(); onOpen(row.original); } }}
                >
                  {row.getVisibleCells().map((cell) => (
                    <TableCell key={cell.id} data-column={cell.column.id} data-label={labels[cell.column.id] || cell.column.id} className="align-top">{flexRender(cell.column.columnDef.cell, cell.getContext())}</TableCell>
                  ))}
                </TableRow>
              );
              return head ? <React.Fragment key={"run:" + key}>{head}{line}</React.Fragment> : line;
            })}
          </TableBody>
        </Table>
      </div>
      {foot}
    </div>
  );
}
