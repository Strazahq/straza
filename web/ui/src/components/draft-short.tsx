import { ArrowRightIcon, PencilIcon, UserRoundIcon, WrenchIcon } from "lucide-react";
import { OutcomeChip, Pill } from "@/components/chip";
import { Arrow, Facts, Muted, ObjectTag } from "@/components/draft-parts";
import { Button } from "@/components/ui/button";
import type { ShortRow, ShortValue } from "@/lib/drafts-short";
import { IN_SHORT, assignRole } from "@/lib/drafts-words";
import { navigate } from "@/lib/router";

// In short: the card at the top of a draft's page that says what publishing
// does, as rows of a label and pills. shortRows in lib builds the rows.

const ICON = { pencil: PencilIcon, person: UserRoundIcon, wrench: WrenchIcon };

function Value({ v }: { v: ShortValue }) {
  switch (v.t) {
    case "tag":
      return <ObjectTag kind={v.kind} name={v.name} />;
    case "pill":
      return <Pill icon={v.icon ? ICON[v.icon] : undefined}>{v.text}</Pill>;
    case "outcome":
      return <OutcomeChip outcome={v.outcome} />;
    case "arrow":
      return <Arrow />;
    case "muted":
      return <Muted>{v.text}</Muted>;
    case "rule":
      return <span className="font-mono text-caption font-semibold break-all text-foreground">{v.name}</span>;
    case "assign":
      // The role's page opens on its Holders tab, where a role is assigned.
      return <Button size="sm" onClick={() => navigate("roles", [v.id, "holders"])} data-assign={v.role}><ArrowRightIcon />{assignRole(v.role)}</Button>;
  }
}

// DraftShort draws the rows. A row that repeats the label of the row above
// it leaves its label out, so a label reads once over its rows.
export function DraftShort({ rows }: { rows: ShortRow[] }) {
  return (
    <section className="flex flex-col gap-3 rounded-md border border-border bg-card px-4 py-3.5" aria-label={IN_SHORT} data-short>
      <h2 className="m-0 text-section leading-7 font-semibold text-foreground">{IN_SHORT}</h2>
      <Facts
        rows={rows.map((r, i) => ({
          key: r.key,
          label: i > 0 && rows[i - 1].label === r.label ? "" : r.label,
          value: <span className="contents" data-short-row={r.key}>{r.values.map((v, j) => <Value key={j} v={v} />)}</span>,
        }))}
      />
    </section>
  );
}
