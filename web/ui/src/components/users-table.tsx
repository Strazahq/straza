import type { ColumnDef } from "@tanstack/react-table";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Chip, Pill, StatusChip, Tag } from "@/components/chip";
import { SortHead, plain, sortable } from "@/components/data-table";
import { KIND_ICON, KindIcon, KindTile } from "@/components/kind-icon";
import { MARKS, markOf } from "@/components/role-kind";
import { NoRole } from "@/components/status-badge";
import type { RoleRow, UserRow } from "@/lib/api";
import { NO_SPONSOR_CHIP } from "@/lib/entra-words";
import { ALSO_DISABLED, USER_LABELS } from "@/lib/user-list-words";
import { NOT_YET, orderedRoles, sourceKind, sourceWord, tileStatus, userKind, userState } from "@/lib/user-look";
import { NEVER_SEEN, NO_ROLE, sponsorShow, sponsoringWords } from "@/lib/user-words";
import { absTime, agoWord } from "@/lib/words";

// The columns and the badges of the Users directory. The column ids of the
// sortable columns are the server's
// own sort keys, since the screen hands the sort to the paged lane and the
// table never sorts what it holds.

// UserStatus is the Status cell, one chip so the row stays one line high. A
// lock outranks a disable, as on the kind tile, and its chip is titled with
// why each lock exists and with the disable it hides. A status word the
// console does not know keeps its danger chip.
export function UserStatus({ user }: { user: Pick<UserRow, "status" | "locks"> }) {
  const { disabled, locks } = userState(user);
  if (locks.length > 0) {
    const why = [disabled ? ALSO_DISABLED : "", ...locks.map((l) => l.reason)].filter(Boolean).join(" ");
    return <StatusChip status="locked" title={why || undefined} data-locked="locked" data-user-status={user.status} />;
  }
  if (disabled) return <StatusChip status="disabled" data-user-status="disabled" />;
  if (user.status !== "active") return <Chip tone="danger" data-user-status={user.status}>{user.status}</Chip>;
  return <StatusChip status="active" data-user-status="active" />;
}

// UserName is the User cell: the display name, then the sign-in name, which
// gives way first when the cell is too narrow for both.
export function UserName({ user }: { user: Pick<UserRow, "display" | "username"> }) {
  if (!user.display) return <div className="truncate font-mono font-semibold text-foreground" title={user.username}>{user.username}</div>;
  return (
    <div className="flex min-w-0 items-baseline gap-2">
      <span className="max-w-full shrink-0 truncate font-semibold text-foreground" title={user.display}>{user.display}</span>
      <span className="min-w-0 truncate font-mono text-caption text-muted-foreground" title={user.username}>{user.username}</span>
    </div>
  );
}

// Seen is a timestamp the operator way: the relative reading, the absolute
// stamp on hover, and NOT_YET for a user who never checked in.
function Seen({ iso }: { iso: string | undefined }) {
  if (!iso) return <span className="text-muted-foreground" title={NEVER_SEEN}>{NOT_YET}</span>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default text-text-2">{agoWord(iso)}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

// USER_WIDTHS fixes the columns of the Users list and of a role's Holders
// tab, so a row stays one line high and every cell of it fits a 1280 px
// window with the rail open. The User column has no width and takes the rest.
export const USER_WIDTHS: Record<string, string> = { kind: "44px", sponsor: "112px", roles: "232px", source: "152px", last_seen: "108px", status: "120px", created: "184px" };

// userMinWidth is the table's floor for the columns that are shown: their
// fixed widths plus 220 px for the User column. Below it the table scrolls
// inside its box.
export const userMinWidth = (shown: string[]) => shown.reduce((sum, id) => sum + parseInt(USER_WIDTHS[id] || "0", 10), 220) + "px";

// timeHead is the header of a time column: the newest first on the first
// click, since that is the reading a dormant directory is walked for.
function timeHead(label: string): ColumnDef<UserRow>["header"] {
  const render: ColumnDef<UserRow>["header"] = ({ column }) => (
    <SortHead label={label} sorted={column.getIsSorted()} onClick={() => column.toggleSorting(column.getIsSorted() !== "desc")} />
  );
  return render;
}

// userColumns builds the directory's columns. onSponsor takes the screen
// to the agents one person sponsors, the other half of the sponsorship
// reading. catalog is the roles a role's page holds, so the Roles cell
// draws a role it knows with its kind's icon.
export function userColumns(onSponsor: (user: UserRow) => void, catalog?: RoleRow[]): ColumnDef<UserRow>[] {
  return [
    {
      id: "kind",
      header: () => null,
      cell: ({ row }) => <KindTile kind={userKind(row.original)} size="md" named status={tileStatus(row.original)} />,
      enableSorting: false,
    },
    {
      id: "name",
      header: sortable(USER_LABELS.name),
      cell: ({ row }) => <UserName user={row.original} />,
      enableHiding: false,
    },
    {
      id: "sponsor",
      header: plain(USER_LABELS.sponsor),
      cell: ({ row }) => {
        const u = row.original;
        const n = u.sponsored_count || 0;
        if (userKind(u) === "person") {
          if (n === 0) return null;
          const says = sponsorShow(n, u.username);
          // The link is its own door: neither a click nor a key on it
          // reaches the row.
          return (
            <Button
              variant="link"
              size="sm"
              className="h-auto p-0 text-link"
              aria-label={says}
              title={says}
              data-sponsor-count={u.username}
              onClick={(e) => { e.stopPropagation(); onSponsor(u); }}
              onKeyDown={(e) => e.stopPropagation()}
            >
              {sponsoringWords(n)}
            </Button>
          );
        }
        // An agent from Entra with no sponsor is one Entra lists no single
        // person Straza has for. The row does not carry what Entra lists,
        // so its page says why.
        if (!u.sponsor) return u.origin === "entra" && userKind(u) === "agent" ? <Chip tone="warn" data-no-sponsor="">{NO_SPONSOR_CHIP}</Chip> : null;
        return (
          <div className="flex min-w-0 items-center gap-1.5">
            <KindTile kind="person" size="sm" />
            <span className="min-w-0 truncate font-mono text-caption text-foreground" title={u.sponsor}>{u.sponsor}</span>
          </div>
        );
      },
      enableSorting: false,
    },
    {
      id: "roles",
      header: plain(USER_LABELS.roles),
      cell: ({ row }) => {
        const roles = orderedRoles(row.original.effective_roles);
        if (roles.length === 0) return <NoRole text={NO_ROLE} />;
        const rest = roles.slice(1);
        const known = catalog?.find((r) => r.name === roles[0]);
        // One tag and a fold keep every row one line high. A long role
        // name is cut inside the column, and the fold names the rest on
        // hover.
        return (
          <span className="flex min-w-0 items-center gap-1">
            <Tag icon={known ? KIND_ICON[MARKS[markOf(known)]] : undefined} className="min-w-0" title={roles[0]}><span className="min-w-0 truncate">{roles[0]}</span></Tag>
            {rest.length > 0 && <Pill className="shrink-0" title={rest.join(", ")} data-more-roles={rest.length}>{"+" + rest.length}</Pill>}
          </span>
        );
      },
      enableSorting: false,
    },
    {
      id: "source",
      header: plain(USER_LABELS.source),
      cell: ({ row }) => {
        const word = sourceWord(row.original.origin);
        const kind = sourceKind(row.original.origin);
        return (
          <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-foreground" data-source={word}>
            {kind && <KindIcon kind={kind} className="text-muted-foreground" />}
            {word}
          </span>
        );
      },
      enableSorting: false,
    },
    {
      id: "last_seen",
      header: timeHead(USER_LABELS.last_seen),
      cell: ({ row }) => <Seen iso={row.original.last_seen} />,
    },
    {
      id: "status",
      header: sortable(USER_LABELS.status),
      cell: ({ row }) => <UserStatus user={row.original} />,
    },
    {
      id: "created",
      header: timeHead(USER_LABELS.created),
      cell: ({ row }) => <span className="whitespace-nowrap text-text-2">{absTime(row.original.created_at)}</span>,
    },
  ];
}
