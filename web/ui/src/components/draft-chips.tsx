import * as React from "react";
import { FileCheckIcon, ServerIcon, ShieldIcon } from "lucide-react";
import { type ChipTone, DraftChip } from "@/components/draft-checks";
import type { DraftKind } from "@/lib/api";
import type { SummaryChip } from "@/lib/drafts-items";
import type { ChangeWhat } from "@/lib/drafts-model";
import { EVERY_TOOL, addedMark, holdersChip, unchangedFold } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";

// The small marks of the drafts page that the In this draft table and the
// cards share: the change hue, the kind icon, the summary and holders
// chips, and a row of name chips that marks what joins and leaves.

// MARK_TONE is the hue of an item's change chip.
export const MARK_TONE: Record<ChangeWhat, ChipTone> = { new: "accent", changed: "warn", off: "warn", removed: "danger" };

const ICON: Record<DraftKind, typeof ServerIcon> = { App: ServerIcon, Role: ShieldIcon, PolicySet: FileCheckIcon };

// KindIcon is the icon of a kind, the one its area carries in the rail.
export function KindIcon({ kind }: { kind: DraftKind }) {
  const Icon = ICON[kind];
  return <Icon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />;
}

// SummaryChips sum up a changed role: green for what joins, red for what
// leaves.
export function SummaryChips({ chips }: { chips: SummaryChip[] }) {
  return <>{chips.map((c) => <DraftChip key={c.word} word={c.word} tone={c.sign === "+" ? "ok" : "danger"} attr="data-sum" />)}</>;
}

// HoldersChip says who holds a role, and nothing where the verdict does not
// say.
export function HoldersChip({ n }: { n: number | null }) {
  return n === null ? null : <DraftChip word={holdersChip(n)} tone="plain" attr="data-holders" />;
}

const CHIP = "inline-block rounded-md border px-1.5 py-px font-mono text-[13px]";
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
