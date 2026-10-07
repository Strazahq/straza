import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { DataTable, plain, sortable } from "@/components/data-table";
import { NoRole, RoleChip, StatusBadge } from "@/components/status-badge";
import type { AppRow } from "@/lib/api";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { absTime, relTimeText } from "@/lib/words";

type Props = {
  rows: AppRow[];
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

const columns: ColumnDef<AppRow>[] = [
  {
    accessorKey: "name",
    header: sortable("Server"),
    // The name is a real link to the server's page, so it opens in a new
    // tab like any link; a plain click navigates once, not once per element.
    cell: ({ row }) => (
      <div>
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
        {row.original.url && <div className="truncate text-[13px] text-muted-foreground">{row.original.url}</div>}
      </div>
    ),
    enableHiding: false,
  },
  {
    accessorKey: "runtime",
    header: sortable("Runtime"),
    cell: ({ getValue }) => <Badge variant="secondary" className="rounded-md font-mono text-[13px] font-normal">{String(getValue())}</Badge>,
  },
  {
    accessorKey: "status",
    header: sortable("Status"),
    cell: ({ row }) => (
      <span className="inline-flex items-center gap-1">
        <StatusBadge status={row.original.status} />
        {row.original.paused && <Badge variant="outline" className="rounded-md border-warn/40 bg-warn-bg font-mono text-[13px] font-normal text-warn">admin-paused</Badge>}
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
      const roles = row.original.reached_by || [];
      return roles.length ? <span>{roles.map((r) => <RoleChip key={r} name={r} />)}</span> : <NoRole text="no role yet" />;
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
export function ServersTable({ rows, onOpen }: Props) {
  const [globalFilter, setGlobalFilter] = React.useState("");
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
          placeholder="Filter by name, runtime, status or role"
          aria-label="Filter servers"
          className="h-9 w-full max-w-sm"
        />
      }
      count={(shown) => (shown === total ? total + (total === 1 ? " server" : " servers") : shown + " of " + total + " servers")}
      emptyText={total === 0
        ? "No MCP server is installed yet. Add one with Add MCP server above, or with strazactl apps install -f app.yaml."
        : "No server matches the filter. Clear it to see all " + total + "."}
    />
  );
}
