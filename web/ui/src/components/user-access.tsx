import { MinusIcon } from "lucide-react";
import { OutcomeChip, Pill, Tag } from "@/components/chip";
import { Facts } from "@/components/draft-parts";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { KIND_ICON } from "@/components/kind-icon";
import { AccessTable } from "@/components/user-access-table";
import { Heading, MUTED, PageLink, RoleTag, Waiting } from "@/components/user-parts";
import { UserRoles } from "@/components/user-roles";
import type { AssignmentRow, BindingRow, Page, PreviewAnswer, RoleRow, UserDetail, UserRow } from "@/lib/api";
import { OUTCOME_WORD, STATUS_WORD } from "@/lib/chip-words";
import { READING_ROLES as READING_CATALOG, SUBJECT_ROLES as SUBJECT_CATALOG } from "@/lib/role-words";
import type { AccessRow } from "@/lib/user-access";
import { refusal } from "@/lib/user-look";
import { needsArea } from "@/lib/say";
import { type Read, dataOf } from "@/lib/user-read";
import { approverRoles, consoleFact, roleLines } from "@/lib/user-roles";
import {
  ACCESS_HELP, APPROVALS_FACT, CONSOLE_FACT, DECIDES_FOR, DECIDES_NOTHING, EVERY_CALL, READING_ACCESS, READING_AGENTS, READING_ROLES, SPONSORS, SUBJECT_ACCESS, SUBJECT_AGENTS, SUBJECT_ROLES, THROUGH,
  canDo, reachesNothing,
} from "@/lib/user-words";

type Props = {
  user: UserDetail;
  // servers, audit and policy say the reader's role includes the MCP
  // servers, the audit and the policy areas.
  servers: boolean;
  audit: boolean;
  policy: boolean;
  preview: Read<PreviewAnswer>;
  bindings: Read<BindingRow[]>;
  roles: Read<RoleRow[]>;
  assignments: Read<AssignmentRow[]>;
  agents: Read<Page<UserRow>>;
  // access is the table the page computed once the preview answered.
  access: AccessRow[] | null;
  onAssign: () => void;
  onRemove: (row: AssignmentRow, role: string, fromEntra: boolean) => void;
};

// UserAccessTab is the Access tab: what the user can do on the registered
// servers and on the servers on the person's machine, the Console and
// Approvals facts, then the Roles list. A user whose every call the gateway
// refuses before any policy runs gets one denied chip with its cause in
// place of the table, since the tools its roles give are not reached.
export function UserAccessTab({ user, servers, audit, policy, preview, bindings, roles, assignments, agents, access, onAssign, onRemove }: Props) {
  const name = user.username;
  const catalog = dataOf(roles);
  const held = dataOf(assignments);
  const effective = user.effective_roles || [];
  const bound = servers ? dataOf(bindings) : null;
  const lines = catalog && held ? roleLines(effective, held, catalog, bound) : null;
  const refused = servers ? refusal(user, dataOf(preview)?.refused) : undefined;
  const table = refused ? null : access;
  return (
    <div className="flex flex-col gap-6" data-user-access>
      <section className="flex flex-col gap-3">
        <Heading>{canDo(name)}{servers && <HelpTip label={canDo(name)} text={ACCESS_HELP} />}</Heading>
        {!servers && <p className="m-0 text-dense text-text-2" data-needs-area>{needsArea("The servers and tools are", "apps")}</p>}
        {servers && <Waiting read={preview} word={READING_ACCESS} subject={SUBJECT_ACCESS} />}
        {servers && bindings.kind === "failed" && <FetchError subject={SUBJECT_ACCESS} detail={bindings.message} />}
        {refused && (
          <div className="flex flex-wrap items-center gap-1.5" data-refused={refused === "revoked" ? "locked" : refused}>
            <OutcomeChip outcome="denied" />
            <Pill>{EVERY_CALL}</Pill>
            <Pill>{STATUS_WORD[refused === "revoked" ? "locked" : refused]}</Pill>
          </div>
        )}
        {table && table.length === 0 && <p className="m-0 text-dense text-text-2" data-reaches-nothing>{reachesNothing(name)}</p>}
        {table && table.length > 0 && <AccessTable rows={table} roles={catalog || []} policy={policy} />}
        {catalog && <Facts rows={[{ label: CONSOLE_FACT, value: <ConsoleValue effective={effective} roles={catalog} /> }, { label: APPROVALS_FACT, value: <ApprovalsValue user={user} roles={catalog} agents={agents} /> }]} />}
      </section>
      <Waiting read={roles} word={READING_CATALOG} subject={SUBJECT_CATALOG} />
      <Waiting read={assignments} word={READING_ROLES} subject={SUBJECT_ROLES} />
      <UserRoles name={name} scim={user.origin === "scim"} lines={lines} servers={servers} audit={audit} onAssign={onAssign} onRemove={onRemove} />
    </div>
  );
}

// ConsoleValue is the Console fact: no access, or the areas the user's
// Straza roles open together and the roles they come through.
function ConsoleValue({ effective, roles }: { effective: string[]; roles: RoleRow[] }) {
  const fact = consoleFact(effective, roles);
  if (fact.roles.length === 0) return <Pill icon={MinusIcon} data-console="none">{OUTCOME_WORD.none}</Pill>;
  return (
    <>
      <Pill data-console={fact.areas}>{fact.areas}</Pill>
      <span className={MUTED}>{THROUGH}</span>
      {fact.roles.map((r) => <RoleTag key={r.id} name={r.name} role={r} icon={KIND_ICON["role-straza"]} />)}
    </>
  );
}

// ApprovalsValue is the Approvals fact: the approver roles the user holds,
// then, for a person, the agents the person sponsors, or decides nothing.
function ApprovalsValue({ user, roles, agents }: { user: UserDetail; roles: RoleRow[]; agents: Read<Page<UserRow>> }) {
  const approvers = approverRoles(user.effective_roles || [], roles);
  const sponsored = dataOf(agents)?.items || [];
  const waiting = agents.kind === "loading" || agents.kind === "failed";
  if (approvers.length === 0 && sponsored.length === 0 && !waiting) return <Pill icon={MinusIcon} data-approvals="none">{DECIDES_NOTHING}</Pill>;
  return (
    <>
      {approvers.length > 0 && <span className={MUTED}>{DECIDES_FOR}</span>}
      {approvers.map((r) => <RoleTag key={r.id} name={r.name} role={r} icon={KIND_ICON["role-approver"]} />)}
      {sponsored.length > 0 && <span className={MUTED}>{SPONSORS}</span>}
      {sponsored.map((a) => (
        <PageLink key={a.id} to="users" rest={[a.id]} className="inline-flex max-w-full rounded-md hover:opacity-80">
          <Tag icon={KIND_ICON.agent} agent title={a.username} className="max-w-full" data-agent-tag={a.username}><span className="min-w-0 truncate">{a.username}</span></Tag>
        </PageLink>
      ))}
      <Waiting read={agents} word={READING_AGENTS} subject={SUBJECT_AGENTS} />
    </>
  );
}
