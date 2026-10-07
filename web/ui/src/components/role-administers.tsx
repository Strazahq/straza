import { Button } from "@/components/ui/button";
import { ListRow } from "@/components/sheet-parts";
import { RoleChip, StatusBadge } from "@/components/status-badge";
import type { AppRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { ADMINISTERS_HINT, OPEN_SERVER } from "@/lib/role-words";

// The Administers tab of a straza role: the MCP servers whose
// admin role this is, each a door to the server's page. Nothing here is
// editable, since a server names its admin role when it is registered and
// never after, so the tab carries no action but the door.

export function RoleAdministers({ servers }: { servers: AppRow[] }) {
  return (
    <div className="flex flex-col gap-3">
      {servers.map((app) => (
        <ListRow
          key={app.id}
          actions={<Button variant="outline" size="sm" onClick={() => navigate("servers", [app.id])}>{OPEN_SERVER}</Button>}
        >
          <span data-administers={app.name}><RoleChip name={app.name} /></span>
          <StatusBadge status={app.status} />
        </ListRow>
      ))}
      <p className="text-[13px] text-muted-foreground">{ADMINISTERS_HINT}</p>
    </div>
  );
}
