import { GlanceTile } from "@/components/glance-tile";
import type { ActivityFilter } from "@/lib/session-activity";
import { TILE } from "@/lib/session-words";

type Props = {
  // allowed and denied are the activity's counts, null while the activity
  // is unread, failed, not allowed or cut at its limit. waiting is the
  // session's own count of waiting requests, and turns the transcript's,
  // null while it is unread, failed or not allowed.
  allowed: number | null;
  waiting: number;
  denied: number | null;
  turns: number | null;
  onFilter: (f: ActivityFilter) => void;
  onTurns: () => void;
};

type Tile = { key: string; n: number | null; words: string; tone?: "warn" | "danger"; onOpen: () => void };

// SessionGlance is the row of numbers under the session's head. A tile at
// zero is not drawn, and neither is one whose count is not there.
export function SessionGlance({ allowed, waiting, denied, turns, onFilter, onTurns }: Props) {
  const tiles: Tile[] = [
    { key: "allowed", n: allowed, words: TILE.allowed(allowed || 0), onOpen: () => onFilter("all") },
    { key: "waiting", n: waiting, words: TILE.waiting(), tone: "warn", onOpen: () => onFilter("waiting") },
    { key: "denied", n: denied, words: TILE.denied(), tone: "danger", onOpen: () => onFilter("denied") },
    { key: "turns", n: turns, words: TILE.turns(turns || 0), onOpen: onTurns },
  ];
  const shown = tiles.filter((t): t is Tile & { n: number } => !!t.n && t.n > 0);
  if (shown.length === 0) return null;
  return (
    <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap" data-glance-row>
      {shown.map((t) => <GlanceTile key={t.key} name={t.key} n={t.n} words={t.words} tone={t.tone} onOpen={t.onOpen} />)}
    </div>
  );
}
