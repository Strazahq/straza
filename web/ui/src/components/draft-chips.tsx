import * as React from "react";
import type { LucideIcon } from "lucide-react";
import { Chip } from "@/components/chip";
import { HoldersPill } from "@/components/draft-parts";
import { KIND_ICON } from "@/components/kind-icon";
import { KindGlyph, markOf } from "@/components/role-kind";
import type { DraftKind } from "@/lib/api";
import type { SummaryChip } from "@/lib/drafts-items";
import type { RoleView } from "@/lib/drafts-model";
import { routeByKey } from "@/lib/routes";
import { EVERY_TOOL, addedMark, unchangedFold } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";

// The small marks of the drafts page that the In this draft table and the
// cards share: the kind icon, the summary chips, the holders pill, and a
// row of name chips that marks what joins and leaves.

const ICON: Record<Exclude<DraftKind, "Role">, LucideIcon> = { App: KIND_ICON.server, PolicySet: KIND_ICON["policy-set"] };

// KindIcon is the icon of an item's kind. A role carries its own kind's
// icon when the item holds the role, and the rail's Roles icon otherwise.
export function KindIcon({ kind, role }: { kind: DraftKind; role?: RoleView | null }) {
  if (kind === "Role" && role) return <KindGlyph mark={markOf(role)} />;
  const Icon = kind === "Role" ? routeByKey("roles").icon : ICON[kind];
  return <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />;
}

// SummaryChips sum up a changed role: green for what joins, red for what
// leaves.
export function SummaryChips({ chips }: { chips: SummaryChip[] }) {
  return <>{chips.map((c) => <Chip key={c.word} tone={c.sign === "+" ? "ok" : "danger"} data-sum={c.word}>{c.word}</Chip>)}</>;
}

// HoldersChip says who holds a role, and nothing where the verdict does not
// say.
export function HoldersChip({ n }: { n: number | null }) {
  return n === null ? null : <HoldersPill n={n} />;
}

const CHIP = "inline-block rounded-md border px-1.5 py-px font-mono text-caption";
const label = (name: string) => (name === "*" ? EVERY_TOOL : name);

// ChipDiff is one row of name chips: a green chip with a plus for each name
// that joins, a red struck chip for each that leaves, and the names that
// stay, folded into a count that opens on click once there are more than
// four.
export function ChipDiff({ added, removed, same }: { added: string[]; removed: string[]; same: string[] }) {
  const [open, setOpen] = React.useState(false);
  const fold = same.length > 4 && !open;
  return (
    <span className="flex flex-wrap gap-1.5" data-names>
      {added.map((n) => <span key={"+" + n} data-name={n} data-added className={cn(CHIP, "border-ok bg-ok-bg text-ok")}>{addedMark(label(n))}</span>)}
      {removed.map((n) => <del key={"-" + n} data-name={n} data-removed className={cn(CHIP, "border-danger bg-danger-bg text-danger line-through")}>{label(n)}</del>)}
      {fold
        ? <button type="button" data-unchanged={same.length} onClick={() => setOpen(true)} className={cn(CHIP, "border-dashed border-border font-sans text-link hover:bg-secondary/60")}>{unchangedFold(same.length)}</button>
        : same.map((n) => <span key={n} data-name={n} className={cn(CHIP, "border-border bg-background text-text-2")}>{label(n)}</span>)}
    </span>
  );
}
