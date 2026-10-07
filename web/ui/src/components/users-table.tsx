import type * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { HEAD, SortHead, plain, sortable } from "@/components/data-table";
import { NoRole, RoleChip } from "@/components/status-badge";
import type { LockRow, UserRow } from "@/lib/api";
import { NEVER_SEEN, NO_ROLE, kindWord, lockedLine, originWord, sponsorShow, sponsoredBy } from "@/lib/user-words";
import { absTime, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// The columns and the badges of the Users directory. The column ids of the
// sortable columns are the server's
// own sort keys, since the screen hands the sort to the paged lane and the
// table never sorts what it holds.

const CHIP = "rounded-md px-2 text-[13px] font-normal";
const TONE = {
  ok: "bg-ok-bg text-ok border-ok/40",
  warn: "bg-warn-bg text-warn border-warn/40",
  danger: "bg-danger-bg text-danger border-danger/40",
  accent: "bg-accent-bg text-link border-link/40",
  teal: "bg-teal-bg text-teal border-teal/40",
  plain: "",
} as const;

type Tone = keyof typeof TONE;

// WordBadge is one word of this screen's vocabulary in its trust hue. The
// hues are the reserved ones, read from the tokens.
// lead is a glyph drawn before the word, so a kind badge or an owned chip
// carries its mark without a second chip kit.
export function WordBadge({ word, tone, title, mono, attr, lead }: { word: string; tone: Tone; title?: string; mono?: boolean; attr?: string; lead?: React.ReactNode }) {
  const data = attr ? { [attr]: word } : {};
  return (
    <Badge variant="outline" title={title} className={cn(CHIP, mono && "font-mono", TONE[tone])} {...data}>{lead}{word}</Badge>
  );
}

// KindBadge says what the identity is: a person is plain muted text, since
// most rows are people and a badge on every row is noise.
export function KindBadge({ user }: { user: Pick<UserRow, "kind" | "user_type"> }) {
  const word = kindWord(user);
  if (word === "person") return <span className="text-muted-foreground" data-kind="person">person</span>;
  return <WordBadge word={word} tone={word === "service" ? "teal" : "accent"} attr="data-kind" />;
}

// lockTitle is the hover text of the locked badge: why each lock exists.
function lockTitle(locks: LockRow[] | undefined): string {
  return (locks || []).map((l) => l.reason || lockedLine(l.origin)).join(" ");
}

// UserStatus is the Status cell: the status word, and the locked badge
// beside it when a revocation lane holds the user.
export function UserStatus({ user }: { user: Pick<UserRow, "status" | "locks"> }) {
  const locks = user.locks || [];
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap">
      <WordBadge word={user.status} tone={user.status === "active" ? "ok" : "danger"} mono attr="data-user-status" />
      {locks.length > 0 && <WordBadge word="locked" tone="danger" title={lockTitle(locks)} mono attr="data-locked" />}
    </span>
  );
}

// OriginBadge names where the identity came from, in the wire's own word.
export function OriginBadge({ origin }: { origin: string | undefined }) {
  return <WordBadge word={originWord(origin)} tone="plain" mono attr="data-origin" />;
}

// Seen is a timestamp the operator way: the relative reading, the absolute
// stamp on hover, and the word "never" for a user who never checked in.
function Seen({ iso }: { iso: string | undefined }) {
  if (!iso) return <span className="text-muted-foreground" title={NEVER_SEEN}>never</span>;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default text-text-2">{relTimeText(iso)}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

// orderedRoles puts the control-plane role first, since a person who holds
// it is the first thing to read off a row.
function orderedRoles(roles: string[] | undefined): string[] {
  return [...(roles || [])].sort((a, b) => (a === "straza-admin" ? -1 : b === "straza-admin" ? 1 : 0));
}

export const USER_LABELS: Record<string, string> = {
  name: "User",
  type: "Type",
  agents: "Agents",
  roles: "Roles",
  origin: "Origin",
  last_seen: "Last seen",
  status: "Status",
  created: "Created",
};

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
// reading.
export function userColumns(onSponsor: (username: string) => void): ColumnDef<UserRow>[] {
  return [
    {
      id: "name",
      header: sortable("User"),
      // One line per row: the display name, or an agent's
      // sponsor, sits beside the username in the muted colour.
      cell: ({ row }) => {
        const u = row.original;
        const beside = u.display || (u.sponsor ? sponsoredBy(u.sponsor) : "");
        return (
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="font-mono text-foreground">{u.username}</span>
            {beside && <span className="truncate text-[13px] text-muted-foreground" title={beside}>{beside}</span>}
          </div>
        );
      },
      enableHiding: false,
    },
    {
      id: "type",
      header: plain("Type"),
      cell: ({ row }) => <KindBadge user={row.original} />,
      enableSorting: false,
    },
    {
      id: "agents",
      header: () => <span className={cn(HEAD, "block text-right")}>Agents</span>,
      cell: ({ row }) => {
        const u = row.original;
        // An agent sponsors nobody, so the cell stays empty rather than
        // saying zero about a question that does not apply to it.
        if (kindWord(u) !== "person") return null;
        const n = u.sponsored_count || 0;
        if (n === 0) return <div className="text-right tabular-nums text-muted-foreground">0</div>;
        const says = sponsorShow(n, u.username);
        return (
          <div className="text-right tabular-nums">
            <Button
              variant="link"
              size="sm"
              className="h-auto p-0 text-link"
              aria-label={says}
              title={says}
              data-sponsor-count={u.username}
              onClick={(e) => { e.stopPropagation(); onSponsor(u.username); }}
            >
              {n}
            </Button>
          </div>
        );
      },
      enableSorting: false,
    },
    {
      id: "roles",
      header: plain("Roles"),
      cell: ({ row }) => {
        const roles = orderedRoles(row.original.effective_roles);
        if (roles.length === 0) return <NoRole text={NO_ROLE} />;
        const rest = roles.slice(1);
        // One chip and a fold keep every row one line high and the table
        // inside 1280; the fold names the rest on hover.
        return (
          <span className="inline-flex items-center whitespace-nowrap">
            <RoleChip name={roles[0]} />
            {rest.length > 0 && <span title={rest.join(", ")} data-more-roles={rest.length}><RoleChip name={"+" + rest.length} /></span>}
          </span>
        );
      },
      enableSorting: false,
    },
    {
      id: "origin",
      header: plain("Origin"),
      cell: ({ row }) => <OriginBadge origin={row.original.origin} />,
      enableSorting: false,
    },
    {
      id: "last_seen",
      header: timeHead("Last seen"),
      cell: ({ row }) => <Seen iso={row.original.last_seen} />,
    },
    {
      id: "status",
      header: sortable("Status"),
      cell: ({ row }) => <UserStatus user={row.original} />,
    },
    {
      id: "created",
      header: timeHead("Created"),
      cell: ({ row }) => <span className="whitespace-nowrap text-text-2">{absTime(row.original.created_at)}</span>,
    },
  ];
}
