import { WordBadge } from "@/components/users-table";
import type { RoleRow } from "@/lib/api";
import { AREAS, AREAS_LINE, EVERY_AREA, LEVEL_WORD, NO_AREAS } from "@/lib/role-words";

// The Areas tab of a straza role: the console areas
// the role grants, read from strazad's config. Nothing here is editable,
// so the tab carries no action at all.

// levelOf reads a role's grant on one area out of its stored
// "<area>:<level>" list: write outranks read, and an area the list does not
// name is not granted.
function levelOf(areas: string[], area: string): "none" | "read" | "write" {
  if (areas.includes("full")) return "write";
  const mine = areas.filter((a) => a.startsWith(area + ":")).map((a) => a.slice(area.length + 1));
  if (mine.includes("write")) return "write";
  return mine.includes("read") ? "read" : "none";
}

export function RoleAreas({ role }: { role: RoleRow }) {
  const areas = role.areas || [];
  const lead = areas.includes("full") ? EVERY_AREA : areas.length === 0 ? NO_AREAS : "";
  return (
    <div className="flex flex-col gap-3">
      {lead && <p className="text-sm text-text-2">{lead}</p>}
      <div className="grid gap-px overflow-hidden rounded-md border border-border bg-border sm:grid-cols-2" data-areas>
        {AREAS.map(([name, meaning]) => {
          const level = levelOf(areas, name);
          return (
            <div key={name} className="flex items-center gap-2 bg-card px-3 py-2 text-sm" data-area={name}>
              <b className="w-24 shrink-0 font-mono font-semibold text-foreground">{name}</b>
              <span className="min-w-0 flex-1 truncate text-[13px] text-muted-foreground" title={meaning}>{meaning}</span>
              {level === "none"
                ? <span className="shrink-0 text-[13px] text-muted-foreground" data-level="none">{LEVEL_WORD.none}</span>
                : <WordBadge word={LEVEL_WORD[level]} tone={level === "read" ? "plain" : "accent"} attr="data-level" />}
            </div>
          );
        })}
      </div>
      <p className="text-[13px] text-muted-foreground">{AREAS_LINE}</p>
    </div>
  );
}
