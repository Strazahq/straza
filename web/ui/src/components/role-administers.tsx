import { Button } from "@/components/ui/button";
import { HelpTip } from "@/components/help-tip";
import { KindTile } from "@/components/kind-icon";
import { ListRow, Section } from "@/components/sheet-parts";
import { StatusBadge } from "@/components/status-badge";
import type { AppRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { ADMINISTERS_HINT, OPEN_SERVER, TAB } from "@/lib/role-words";

// The Administers tab of a straza role: the MCP servers whose
// admin role this is, each a row led by the server's tile and a door to
// its page. Nothing here is editable, since a server names its admin role
// when it is registered and never after, so the tab carries no action but
// the door. What the role lets its holders do sits behind the heading's
// help icon.

export function RoleAdministers({ servers }: { servers: AppRow[] }) {
  return (
    <Section title={TAB.administers} action={<HelpTip label={TAB.administers} text={ADMINISTERS_HINT} />}>
      {servers.map((app) => (
        <ListRow
          key={app.id}
          actions={<Button variant="outline" size="sm" onClick={() => navigate("servers", [app.id])}>{OPEN_SERVER}</Button>}
        >
          <span className="flex min-w-0 items-center gap-2" data-administers={app.name}>
            <KindTile kind="server" />
            <span className="truncate font-mono text-foreground">{app.name}</span>
          </span>
          <StatusBadge status={app.status} />
        </ListRow>
      ))}
    </Section>
  );
}
