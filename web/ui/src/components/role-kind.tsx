import { BriefcaseIcon, IdCardIcon, type LucideIcon, ServerCogIcon, ServerIcon, ShieldCogIcon, UserCheckIcon } from "lucide-react";
import { WordBadge } from "@/components/users-table";
import type { RoleRow } from "@/lib/api";
import { KIND_WORD, type Kind, MCP_ADMIN, kindOf } from "@/lib/role-words";
import { cn } from "@/lib/utils";

// The kind marks of the Roles area: one glyph and one tone per kind,
// defined here and nowhere else, so
// the list, the role page badge, the wizard cards and the palette agree and
// a later change lands everywhere at once.

// Mark is a role's kind, or one of the two MCP admin readings of the straza
// kind: the role a server names as its admin role, and the product's
// global one.
export type Mark = Kind | "server-admin" | "global-admin";

// MarkTone is the badge tone a mark paints in: the two kinds that carry
// tools are told apart by hue, the rest stay muted.
export type MarkTone = "accent" | "teal" | "plain";

export const MARKS: Record<Mark, { icon: LucideIcon; tone: MarkTone }> = {
  application: { icon: IdCardIcon, tone: "accent" },
  business: { icon: BriefcaseIcon, tone: "teal" },
  approver: { icon: UserCheckIcon, tone: "plain" },
  straza: { icon: ShieldCogIcon, tone: "plain" },
  "server-admin": { icon: ServerIcon, tone: "plain" },
  "global-admin": { icon: ServerCogIcon, tone: "plain" },
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

const GLYPH_TONE: Record<MarkTone, string> = { accent: "text-link", teal: "text-teal", plain: "text-muted-foreground" };

// KindGlyph draws a mark's glyph, hidden from assistive technology: the
// kind stays a word beside it wherever the glyph appears.
export function KindGlyph({ mark, className }: { mark: Mark; className?: string }) {
  const Icon = MARKS[mark].icon;
  return <Icon aria-hidden="true" data-kind-glyph={mark} className={cn("size-4 shrink-0", GLYPH_TONE[MARKS[mark].tone], className)} />;
}

// RoleKindBadge names a role's kind, with its glyph inside. The list has no
// Kind column, since the category is the kind; the role's page, the
// Composes rows and the wizard's review carry it.
export function RoleKindBadge({ role, administers }: { role: Marked; administers?: string[] | null }) {
  const mark = markOf(role, administers);
  return <WordBadge word={KIND_WORD[kindOf(role)]} tone={MARKS[mark].tone} attr="data-kind" lead={<KindGlyph mark={mark} />} />;
}
