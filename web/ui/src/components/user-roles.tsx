import { ExternalLinkIcon, PlusIcon, ScrollTextIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Chip } from "@/components/chip";
import { KIND_ICON, KindTile } from "@/components/kind-icon";
import { MARKS, markOf } from "@/components/role-kind";
import { NoRole } from "@/components/status-badge";
import { Heading, MUTED, PageLink } from "@/components/user-parts";
import type { AssignmentRow } from "@/lib/api";
import { CHANGE_IN_ENTRA, ENTRA_GROUP_URL, FROM_ENTRA, opensGroup } from "@/lib/entra-words";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { KIND_WORD as ROLE_KIND_WORD, REMOVE, kindOf } from "@/lib/role-words";
import type { RoleLine } from "@/lib/user-roles";
import { ASSIGN_A_ROLE, IDM_OWNS, NO_ROLE, ROLES_HEAD, auditTrailOf, originLine, removeName, roleOn, throughRoles } from "@/lib/user-words";
import { absTime } from "@/lib/words";

const IdmIcon = KIND_ICON["from-idm"];

type Props = {
  name: string;
  scim: boolean;
  // lines is null while the reads behind the list are out or failed, which
  // the page says beside it.
  lines: RoleLine[] | null;
  // servers says the reader's role includes the MCP servers area, without
  // which a role's servers are left out. audit says the same of the audit
  // area, which the foot line opens.
  servers: boolean;
  audit: boolean;
  onAssign: () => void;
  // onRemove asks to remove the direct assignment of a row. fromEntra says
  // the user keeps the role from Entra.
  onRemove: (row: AssignmentRow, role: string, fromEntra: boolean) => void;
};

// UserRoles is the Roles part of the Access tab: every role the user
// holds, a composed one marked with the roles it comes through, every
// direct assignment outside its window, and a quiet Remove on each direct
// assignment. A role from Entra carries the From Entra chip, and where it
// has no assignment made another way, Change in Entra opens its group.
export function UserRoles({ name, scim, lines, servers, audit, onAssign, onRemove }: Props) {
  return (
    <section className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Heading data-roles-head>{ROLES_HEAD}</Heading>
        <Button variant="outline" size="sm" className="ml-auto" onClick={onAssign}><PlusIcon />{ASSIGN_A_ROLE}</Button>
      </div>
      {scim && (
        <p className="m-0 flex items-start gap-1.5 text-dense text-text-2" data-idm-owns>
          <IdmIcon className="mt-1 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          {IDM_OWNS}
        </p>
      )}
      {lines && lines.length === 0 && <div><NoRole text={NO_ROLE} /></div>}
      {lines && lines.map((l) => (
        <div key={l.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border bg-card px-3 py-2" data-role-row={l.name}>
          {l.role && <KindTile kind={MARKS[markOf(l.role)]} size="sm" />}
          {l.role ? <PageLink to="roles" rest={[l.role.id]} className="font-mono text-dense text-link underline-offset-4 hover:underline">{l.name}</PageLink> : <span className="font-mono text-dense">{l.name}</span>}
          {l.role && <span className={MUTED}>{roleOn(ROLE_KIND_WORD[kindOf(l.role)], servers ? l.servers : [])}</span>}
          {l.entra && <Chip tone="plain" icon={KIND_ICON["from-entra"]} data-from-entra>{FROM_ENTRA}</Chip>}
          {l.direct && <span className={MUTED} title={l.direct.created_at ? absTime(l.direct.created_at) : undefined}>{originLine(l.direct.origin, l.direct.created_at)}</span>}
          {!l.direct && l.through.length > 0 && <span className={MUTED}>{throughRoles(l.through)}</span>}
          {l.window && <Chip tone="warn" data-dead-row={l.window}>{l.window}</Chip>}
          {l.direct && (
            <Button variant="ghost" size="sm" className="ml-auto" aria-label={removeName(l.name)} onClick={() => onRemove(l.direct as AssignmentRow, l.name, !!l.entra)}>{REMOVE}</Button>
          )}
          {l.entra && !l.direct && l.role?.entra?.group_id && (
            <a
              href={ENTRA_GROUP_URL(l.role.entra.group_id)}
              target="_blank"
              rel="noreferrer"
              title={opensGroup(l.role.entra.group_id)}
              className="ml-auto inline-flex items-center gap-1 px-3 text-caption whitespace-nowrap text-link hover:underline"
              data-change-in-entra
            >
              {CHANGE_IN_ENTRA}<ExternalLinkIcon className="size-3" aria-hidden="true" />
            </a>
          )}
        </div>
      ))}
      {audit && (
        <div>
          <Button variant="link" size="sm" className="h-auto max-w-full items-start p-0 text-left whitespace-normal text-link [overflow-wrap:anywhere]" onClick={() => navigate("audit", [], false, () => put("audit", { user: name }))} data-audit-link>
            <ScrollTextIcon className="mt-0.5 shrink-0" />{auditTrailOf(name)}
          </Button>
        </div>
      )}
    </section>
  );
}
