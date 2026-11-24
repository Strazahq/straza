import { ChangeChip, Chip } from "@/components/chip";
import { HoldersChip, KindIcon, SummaryChips } from "@/components/draft-chips";
import { type ItemFacts, cardId } from "@/lib/drafts-items";
import { IN_DRAFT, IN_DRAFT_COLUMN, MARK, objectCount, ownedBy } from "@/lib/drafts-words";

// In this draft: one row per item, right under the band, with the kind,
// the name and the server that owns a role, the change, and one line of
// what publishing does. A row moves the reader to its card, and so does its
// name, which is a button for the keyboard.

const TH = "px-3 py-1.5 text-left text-caption font-semibold text-muted-foreground";
const TD = "px-3 py-2 align-top";

// jumpTo scrolls an item's card into view and moves focus to it.
function jumpTo(object: string) {
  const card = document.getElementById(cardId(object));
  if (!card) return;
  card.scrollIntoView({ block: "start" });
  card.focus({ preventScroll: true });
}

export function DraftItems({ facts }: { facts: ItemFacts[] }) {
  return (
    <section className="flex flex-col gap-2" aria-label={IN_DRAFT} data-in-draft>
      <h2 className="m-0 flex flex-wrap items-baseline gap-2 text-body leading-6 font-semibold text-foreground">
        {IN_DRAFT}<span className="text-dense font-normal text-muted-foreground">{objectCount(facts.length)}</span>
      </h2>
      <table className="w-full table-fixed border-collapse overflow-hidden rounded-md border border-border bg-card text-dense">
        <colgroup><col style={{ width: "190px" }} /><col style={{ width: "28%" }} /><col style={{ width: "112px" }} /><col /></colgroup>
        <thead className="border-b border-border bg-secondary/40">
          <tr><th className={TH}>{IN_DRAFT_COLUMN.kind}</th><th className={TH}>{IN_DRAFT_COLUMN.name}</th><th className={TH}>{IN_DRAFT_COLUMN.change}</th><th className={TH}>{IN_DRAFT_COLUMN.does}</th></tr>
        </thead>
        <tbody>
          {facts.map((f) => (
            <tr key={f.object} className="cursor-pointer border-b border-border last:border-b-0 hover:bg-secondary/40" data-row-item={f.object} onClick={() => jumpTo(f.object)}>
              <td className={TD}><span className="flex items-center gap-2 text-foreground"><KindIcon kind={f.item.kind} role={f.role} />{f.kind}</span></td>
              <td className={TD}>
                <button type="button" className="break-all text-left font-mono text-caption text-foreground outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50" onClick={(e) => { e.stopPropagation(); jumpTo(f.object); }}>
                  {f.item.name}
                </button>
                {f.owner && <span className="block text-caption text-muted-foreground">{ownedBy(f.owner)}</span>}
              </td>
              <td className={TD}><ChangeChip change={f.what} data-mark={MARK[f.what]} /></td>
              <td className={TD}>
                <span className="flex flex-wrap items-center gap-1.5 text-text-2">
                  {f.line && <span data-does>{f.line}</span>}
                  <SummaryChips chips={f.summary} />
                  {f.outcomes && <Chip tone="plain" data-outcomes={f.outcomes}>{f.outcomes}</Chip>}
                  <HoldersChip n={f.holders} />
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
