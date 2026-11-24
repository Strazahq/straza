import { cn } from "@/lib/utils";

type Props = {
  // name is the tile's key, on data-glance for the tests.
  name: string;
  n: number;
  words: string;
  // tone colours the number of a tile that counts waiting or refused calls.
  tone?: "warn" | "danger";
  onOpen: () => void;
};

// GlanceTile is one tile of a page's glance row: a button with a number
// over its words, a door to the part that answers it.
export function GlanceTile({ name, n, words, tone, onOpen }: Props) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex min-w-0 flex-col items-start gap-0.5 rounded-md border border-border bg-card px-3.5 py-2.5 text-left outline-none hover:border-link focus-visible:ring-2 focus-visible:ring-ring/50 sm:min-w-[140px] sm:basis-[180px]"
      data-glance={name}
    >
      <span className={cn("text-stat leading-tight font-semibold tabular-nums", tone === "warn" ? "text-warn" : tone === "danger" ? "text-danger" : "text-foreground")}>{n}</span>{" "}
      <span className="text-caption text-text-2">{words}</span>
    </button>
  );
}
