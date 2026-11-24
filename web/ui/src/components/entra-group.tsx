import * as React from "react";
import { CheckIcon, CirclePauseIcon, CloudIcon, ExternalLinkIcon, Loader2Icon, type LucideIcon, RefreshCwIcon, Trash2Icon, TriangleAlertIcon, UnlinkIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Chip, Pill, Tag, type Tone } from "@/components/chip";
import { EntraConfirm } from "@/components/entra-confirm";
import type { AssignmentRow, EntraConfig, EntraStatus, RoleEntraGroup, RoleRow } from "@/lib/api";
import {
  APPLY_REMOVALS, ENTRA_GROUP_URL, GROUP_TITLE, KEPT, RECREATE, deletedLine, groupName, lastGoodPill, membersPill, notAppliedPill, openGroupTitle, ownerLine, readPill, stateWord,
} from "@/lib/entra-words";
import { DELETE_ROLE } from "@/lib/role-words";
import { absTime } from "@/lib/words";
import { cn } from "@/lib/utils";

// STATE_LOOK is the tone and icon of each state's chip. A state the table
// does not know is a plain chip with the server's word.
const STATE_LOOK: Record<string, { tone: Tone; icon: LucideIcon }> = {
  creating: { tone: "unknown", icon: Loader2Icon },
  linked: { tone: "ok", icon: CheckIcon },
  stale: { tone: "warn", icon: TriangleAlertIcon },
  missing: { tone: "danger", icon: Trash2Icon },
  held: { tone: "warn", icon: CirclePauseIcon },
  "not-owner": { tone: "warn", icon: UnlinkIcon },
};

// READ_STATES are the states in which Straza reads the group's members, so
// a failing read of Entra leaves them stale.
const READ_STATES = ["linked", "held", "released"];

// look reads what the block shows: the chip's key, whether the read of
// Entra is stale for this group, and whether Straza is no longer an owner
// of a group it still reads, which the server says with a sentence on a
// linked group.
export function look(group: RoleEntraGroup, status: EntraStatus | null): { chip: string; stale: boolean; notOwner: boolean } {
  const stale = !!status?.stale_since && READ_STATES.includes(group.state);
  const notOwner = group.state === "linked" && !!group.error;
  return { chip: stale ? "stale" : notOwner ? "not-owner" : group.state, stale, notOwner };
}

type Props = {
  role: RoleRow;
  group: RoleEntraGroup;
  roles: RoleRow[];
  // status, config and assignments are null when their read failed or
  // this seat may not make it. Without the config the group is named by its
  // ID, and without the assignments the member count is left out.
  status: EntraStatus | null;
  config: EntraConfig | null;
  assignments: AssignmentRow[] | null;
  // product marks a role the product made, which cannot be deleted, and
  // canDelete says the page offers Delete role at all.
  product: boolean;
  canDelete: boolean;
  onDelete: () => void;
  onNotApplied: () => void;
  onChanged: () => void;
};

// EntraGroup is the block above a role's tabs: the role's security group
// in Entra, its state, when it was read, how many members it has and how
// many were not applied, and what the admin does when something is wrong.
export function EntraGroup({ role, group, roles, status, config, assignments, product, canDelete, onDelete, onNotApplied, onChanged }: Props) {
  const [ask, setAsk] = React.useState<"apply" | "recreate" | null>(null);
  const { chip, stale, notOwner } = look(group, status);
  const name = groupName(role.name, group.group_id, config);
  const state = group.state;
  const lookOf = STATE_LOOK[chip];
  const tone = state === "missing" ? "danger" : stale || notOwner || state === "held" || (state !== "linked" && group.error) ? "warn" : "";

  const linkable = !!group.group_id && state !== "creating" && state !== "missing";
  const tag = linkable ? (
    <a href={ENTRA_GROUP_URL(group.group_id)} target="_blank" rel="noreferrer" title={openGroupTitle(name)} className="inline-flex max-w-full min-w-0 rounded-md" data-entra-open={group.group_id}>
      <Tag icon={CloudIcon} className="max-w-full min-w-0 text-link hover:border-link"><span className="min-w-0 truncate">{name}</span><ExternalLinkIcon className="size-3 shrink-0 text-link" aria-hidden="true" /></Tag>
    </a>
  ) : name ? <Tag icon={CloudIcon} title={name} className="max-w-full min-w-0"><span className="min-w-0 truncate">{name}</span></Tag> : null;

  const time = group.read_at && state !== "creating" && state !== "missing"
    ? <Pill title={absTime(group.read_at)} data-entra-read>{stale ? lastGoodPill(group.read_at) : readPill(group.read_at)}</Pill>
    : null;

  const notApplied = (group.not_applied || []).length;
  const applied = assignments ? assignments.filter((a) => a.role_id === role.id && a.origin === "entra").length : null;
  const counts = state === "creating" || state === "held" ? null : (
    <>
      {applied !== null && <Pill data-entra-members>{membersPill(applied + notApplied)}</Pill>}
      {notApplied > 0 && (
        <button type="button" onClick={onNotApplied} className="inline-flex items-center rounded-pill bg-pill px-2.5 py-0.5 align-middle text-caption leading-5 whitespace-nowrap text-link hover:underline" data-entra-not-applied>
          {notAppliedPill(notApplied)}
        </button>
      )}
    </>
  );

  // The sentences under the facts: the read of Entra while it fails, then
  // the state's own. A sentence the server wrote stands where the page has
  // none, so no error is hidden.
  const said: React.ReactNode[] = [];
  if (stale) {
    if (status?.error) said.push(<p key="stale" className="text-foreground">{status.error}</p>);
    said.push(<p key="kept">{KEPT}</p>);
  }
  if (state === "missing") {
    said.push(<p key="missing" className="text-foreground">{deletedLine(name, product)}</p>);
    said.push(
      <div key="acts" className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" onClick={() => setAsk("recreate")}><RefreshCwIcon />{RECREATE}</Button>
        {canDelete && <Button variant="outline" size="sm" className="border-danger text-danger hover:bg-danger-bg" onClick={onDelete}>{DELETE_ROLE}</Button>}
      </div>,
    );
  } else if (state === "held") {
    if (group.error) said.push(<p key="held" className="text-foreground">{group.error}</p>);
    said.push(<div key="acts" className="flex flex-wrap gap-2"><Button size="sm" onClick={() => setAsk("apply")}>{APPLY_REMOVALS}</Button></div>);
  } else if (notOwner && !stale) {
    said.push(<p key="owner" className="text-foreground">{config?.client_id ? ownerLine(name, config.client_id) : group.error}</p>);
  } else if (group.error && !notOwner) {
    said.push(<p key="error" className="text-foreground">{group.error}</p>);
  }

  return (
    <section
      aria-label={GROUP_TITLE}
      data-entra-group={chip}
      className={cn("min-w-0 overflow-hidden rounded-md border bg-card", tone === "danger" ? "border-danger/45" : tone === "warn" ? "border-warn/45" : "border-border")}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 px-3.5 pt-2.5 pb-1.5">
        <CloudIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <h2 className="text-body font-semibold text-foreground">{GROUP_TITLE}</h2>
        <Chip tone={lookOf ? lookOf.tone : "plain"} icon={lookOf?.icon} className={chip === "creating" ? "[&_svg]:animate-spin motion-reduce:[&_svg]:animate-none" : undefined} data-entra-state={chip}>
          {stateWord(chip)}
        </Chip>
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 px-3.5 pb-3">
        {tag}
        {time}
        {counts}
      </div>
      {said.length > 0 && (
        <div
          className={cn(
            "grid gap-2 border-t px-3.5 pt-2.5 pb-3 text-dense leading-relaxed text-text-2 [&_p]:max-w-[80ch]",
            tone === "danger" ? "border-danger/30 bg-danger-bg" : tone === "warn" ? "border-warn/30 bg-warn-bg" : "border-border",
          )}
          data-entra-said
        >
          {said}
        </div>
      )}
      <EntraConfirm ask={ask} role={role} group={group} name={name} roles={roles} assignments={assignments} onClose={() => setAsk(null)} onDone={onChanged} />
    </section>
  );
}
