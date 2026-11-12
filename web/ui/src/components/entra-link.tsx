import * as React from "react";
import { Button } from "@/components/ui/button";
import { ListRow, Section } from "@/components/sheet-parts";
import type { UserRow } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { entraBlueprints } from "@/lib/keys-api";
import {
  COPY, ENTRA_SECTION, ID_COPIED, LINKED_BY_ADMIN, LINKED_BY_IDM, LINK_BY_IDM, NOT_LINKED, NO_TYPE, OWNER_IDM, OWNER_STRAZA, linkByAdmin,
} from "@/lib/keys-words";
import { adminAreas } from "@/lib/session";

// EntraLink is the Entra agent identity section of an agent's sheet: the
// client id it is linked to, or that it is not linked, who owns the link,
// and how the link is written. The console never writes it. The identity
// manager does over SCIM, or an administrator with strazactl or the admin
// API, so the sheet only shows the field it already read. An agent
// without a link shows no section when no provider uses Entra. The
// providers are read only by a session that may read them, so a delegated
// identity administrator causes no refusals and sees the section as before.
export function EntraLink({ user }: { user: UserRow }) {
  const scim = user.origin === "scim";
  const id = user.entra_agent_id || "";
  const [blueprints, setBlueprints] = React.useState<string[] | null | undefined>(undefined);
  React.useEffect(() => {
    const areas = adminAreas();
    if (areas !== null && !areas.apps) { setBlueprints(null); return; }
    let alive = true;
    void entraBlueprints().then((b) => { if (alive) setBlueprints(b); });
    return () => { alive = false; };
  }, []);
  if (!id && (blueprints === undefined || (blueprints && blueprints.length === 0))) return null;
  const owner = <span className="text-[13px] text-muted-foreground" data-entra-owner>{scim ? OWNER_IDM : OWNER_STRAZA}</span>;
  if (user.user_type !== "agent") {
    return (
      <Section title={ENTRA_SECTION} action={owner}>
        <ListRow><span className="text-muted-foreground" data-entra-link="no-type">{NO_TYPE}</span></ListRow>
      </Section>
    );
  }
  const how = scim ? (id ? LINKED_BY_IDM : LINK_BY_IDM) : (id ? LINKED_BY_ADMIN : linkByAdmin(user.username));
  return (
    <Section title={ENTRA_SECTION} action={owner}>
      {id ? (
        <ListRow actions={<Button variant="outline" size="sm" onClick={() => copyText(id, ID_COPIED)}>{COPY}</Button>}>
          <span className="min-w-0 break-all font-mono text-[13px]" data-entra-link={id}>{id}</span>
        </ListRow>
      ) : (
        <ListRow><span className="text-muted-foreground" data-entra-link="none">{NOT_LINKED}</span></ListRow>
      )}
      <p className="text-[13px] text-muted-foreground">{how}</p>
    </Section>
  );
}
