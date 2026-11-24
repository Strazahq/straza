import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Chip, Pill, Tag } from "@/components/chip";
import { DataTable, plain, sortable } from "@/components/data-table";
import { KindTile } from "@/components/kind-icon";
import { RoleKindTag } from "@/components/role-kind";
import { StatusBadge } from "@/components/status-badge";
import type { AppRow, RoleRow } from "@/lib/api";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { PAUSED, transportWords } from "@/lib/server-words";
import { absTime, relTimeText } from "@/lib/words";

type Props = {
  rows: AppRow[];
  // roles gives each role in Reached by its kind; a role it does not hold
  // keeps a plain tag.
  roles: RoleRow[];
  onOpen: (row: AppRow) => void;
};

const LABEL: Record<string, string> = {
  name: "Server",
  runtime: "Runtime",
  status: "Status",
  tools: "Tools",
  reached_by: "Reached by",
  last_probe_at: "Last checked",
};

// columnsFor lays out the list's columns, with the roles that give the
// Reached by tags their kinds.
const columnsFor = (roles: RoleRow[]): ColumnDef<AppRow>[] => [
  {
    accessorKey: "name",
    header: sortable("Server"),
    // The name is a real link to the server's page, so it opens in a new
    // tab like any link; a plain click navigates once, not once per element.
    // The server's tile leads the row.
    cell: ({ row }) => (
      <div className="flex items-start gap-2">
        <KindTile kind="server" />
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <a
              href={pathFor("servers", [row.original.id])}
              className="font-mono text-foreground hover:underline underline-offset-4"
              onClick={(e) => {
                e.stopPropagation();
                if (!isPlainClick(e)) return;
                e.preventDefault();
                navigate("servers", [row.original.id]);
              }}
            >
              {row.original.name}
            </a>
          </div>
          {row.original.url && <div className="truncate text-caption text-muted-foreground">{row.original.url}</div>}
        </div>
      </div>
    ),
    enableHiding: false,
  },
  {
    accessorKey: "runtime",
    header: sortable("Runtime"),
    // The runtime in words can be a long phrase, so its tag is capped and
    // cut short in the auto-sized column, whole on hover.
    cell: ({ getValue }) => {
      const words = transportWords(String(getValue()));
      return <Tag title={words} className="max-w-[220px]"><span className="min-w-0 truncate">{words}</span></Tag>;
    },
  },
  {
    accessorKey: "status",
    header: sortable("Status"),
    cell: ({ row }) => (
      <span className="inline-flex items-center gap-1">
        <StatusBadge status={row.original.status} />
        {row.original.paused && <Chip tone="warn">{PAUSED}</Chip>}
      </span>
    ),
  },
  {
    id: "tools",
    accessorFn: (r) => (r.tools || []).length,
    header: sortable("Tools"),
    cell: ({ getValue }) => <span className="tabular-nums">{String(getValue())}</span>,
  },
  {
    id: "reached_by",
    accessorFn: (r) => (r.reached_by || []).join(" "),
    header: plain("Reached by"),
    cell: ({ row }) => {
      const names = row.original.reached_by || [];
      // The tags wrap onto further lines, so the column is as wide as its
      // widest tag and the list fits its box.
      return names.length ? <span className="flex flex-wrap gap-y-1" data-reached-by>{names.map((r) => <RoleKindTag key={r} name={r} role={roles.find((x) => x.name === r)} />)}</span> : <Pill>no role yet</Pill>;
    },
    enableSorting: false,
  },
  {
    accessorKey: "last_probe_at",
    header: sortable("Last checked"),
    cell: ({ getValue }) => {
      const iso = getValue() as string | undefined;
      if (!iso) return <span className="text-muted-foreground">never</span>;
      return (
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="cursor-default text-text-2">{relTimeText(iso)}</span>
          </TooltipTrigger>
          <TooltipContent>{absTime(iso)}</TooltipContent>
        </Tooltip>
      );
    },
    sortingFn: (a, b) => Date.parse(a.original.last_probe_at || "") - Date.parse(b.original.last_probe_at || "") || 0,
    sortUndefined: "last",
  },
];

// ServersTable is the MCP servers list: sortable and filterable in the
// browser, since the list is held whole, with a column picker and a live
// row count. A row opens the server's page on click or Enter.
export function ServersTable({ rows, roles, onOpen }: Props) {
  const [globalFilter, setGlobalFilter] = React.useState("");
  const columns = React.useMemo(() => columnsFor(roles), [roles]);
  const total = rows.length;
  return (
    <DataTable
      rows={rows}
      columns={columns}
      labels={LABEL}
      rowKey={(r) => r.id}
      rowName={(r) => r.name}
      dataAttr="data-server"
      onOpen={onOpen}
      globalFilter={globalFilter}
      defaultSort={[{ id: "name", desc: false }]}
      filterBar={
        <Input
          value={globalFilter}
          onChange={(e) => setGlobalFilter(e.target.value)}
          placeholder="Filter by name, status or role"
          aria-label="Filter servers"
          className="h-9 w-full max-w-sm"
        />
      }
      count={(shown) => (shown === total ? total + (total === 1 ? " server" : " servers") : shown + " of " + total + " servers")}
      emptyText={total === 0
        ? "No MCP server yet. strazactl apps install -f app.yaml adds one too."
        : "No server matches the filter. Clear it to see all " + total + "."}
    />
  );
}
