import { Pill, Tag } from "@/components/chip";
import { KIND_ICON } from "@/components/kind-icon";
import type { RoleRow } from "@/lib/api";
import type { Kind as IconKind } from "@/lib/chip-words";
import { KIND_WORD, type Kind, MCP_ADMIN, kindOf } from "@/lib/role-words";
import { cn } from "@/lib/utils";

// The kind marks of the Roles area: one glyph per kind, defined here and
// nowhere else, so the list, the role page badge, the wizard cards and the
// palette agree and a later change lands everywhere at once.

// Mark is a role's kind, or one of the two MCP admin readings of the straza
// kind: the role a server names as its admin role, and the product's
// global one.
export type Mark = Kind | "server-admin" | "global-admin";

// MARKS names the kind whose icon each mark draws. Both MCP admin readings
// draw the server admin role's icon.
export const MARKS: Record<Mark, IconKind> = {
  application: "role-application",
  business: "role-business",
  approver: "role-approver",
  straza: "role-straza",
  "server-admin": "role-server-admin",
  "global-admin": "role-server-admin",
};

// Marked is what markOf needs of a role: the kind, and the name when the
// caller has it, since the product's global MCP admin role is known by its
// reserved name.
export type Marked = Pick<RoleRow, "kind"> & { name?: string };

// markOf reads a role's mark. administers names the servers that name the
// role as their admin role, read off the servers list and never off the
// role's name; null when that list could not be read, which leaves the
// kind's own mark.
export function markOf(role: Marked, administers?: string[] | null): Mark {
  const kind = kindOf(role);
  if (kind !== "straza") return kind;
  if (role.name === MCP_ADMIN) return "global-admin";
  if (administers && administers.length > 0) return "server-admin";
  return kind;
}

// KindGlyph draws a mark's glyph in the muted colour, hidden from assistive
// technology: the kind stays a word beside it wherever the glyph appears.
// Teal marks an AI agent and nothing else, so no role kind has a hue.
export function KindGlyph({ mark, className }: { mark: Mark; className?: string }) {
  const Icon = KIND_ICON[MARKS[mark]];
  return <Icon aria-hidden="true" data-kind-glyph={mark} className={cn("size-4 shrink-0 text-muted-foreground", className)} />;
}

// RoleKindBadge names a role's kind as a fact, a round pill with its glyph
// inside. The role's page and the wizard's review carry it.
export function RoleKindBadge({ role, administers }: { role: Marked; administers?: string[] | null }) {
  const word = KIND_WORD[kindOf(role)];
  return <Pill data-kind={word}><KindGlyph mark={markOf(role, administers)} className="size-3.5" />{word}</Pill>;
}

// RoleKindTag names a role in a cell or a line with its kind's icon. A role
// the page holds no row for keeps a plain tag, since its kind is not known
// there. A name wider than its place is cut short, whole on hover.
export function RoleKindTag({ name, role, administers }: { name: string; role?: Marked | null; administers?: string[] | null }) {
  return (
    <Tag icon={role ? KIND_ICON[MARKS[markOf(role, administers)]] : undefined} title={name} className="mr-1 max-w-full" data-role-kind-tag={name}>
      <span className="min-w-0 truncate">{name}</span>
    </Tag>
  );
}
