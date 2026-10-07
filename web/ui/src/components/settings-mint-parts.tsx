import { XIcon } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HEAD } from "@/components/data-table";
import {
  AREAS,
  AREA_COLUMN,
  JOBS,
  JOB_TITLE,
  LEVEL_NONE,
  LEVEL_READ,
  LEVEL_RW,
  LEVEL_WRITE,
  SENSITIVE,
  areaHint,
  areaLevelLabel,
  grantsAtLevel,
  levelHint,
  levelOf,
  removeGrant,
} from "@/lib/settings-words";
import { cn } from "@/lib/utils";

// The three panels of the mint sheet: the job cards, the grants a job
// brings with the reason each one
// is there, and the area table Custom opens.

// JobCards is the radio group of the common mints. Full root wears the
// warn hue in its title, since it is a root credential.
export function JobCards({ picked, onPick }: { picked: string; onPick: (key: string) => void }) {
  return (
    <RadioGroupPrimitive.Root
      aria-label={JOB_TITLE}
      value={picked}
      onValueChange={onPick}
      loop
      className="grid grid-cols-[repeat(auto-fit,minmax(260px,1fr))] gap-2.5"
    >
      {JOBS.map((j) => {
        const on = j.key === picked;
        return (
          <div key={j.key} className={cn("rounded-md border", on ? "border-link bg-accent-bg" : "border-border bg-card")}>
            <RadioGroupPrimitive.Item
              value={j.key}
              aria-label={j.title}
              data-job={j.key}
              className="flex w-full cursor-pointer flex-col gap-1.5 rounded-md px-3.5 py-3 text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              <span className="flex items-center gap-2">
                <span
                  aria-hidden="true"
                  className={cn("relative size-3.5 shrink-0 rounded-full border-[1.5px]", on ? "border-link after:absolute after:inset-[2.5px] after:rounded-full after:bg-link" : "border-muted-foreground")}
                />
                <span className={cn("font-semibold", j.full ? "text-warn" : "text-foreground")}>{j.title}</span>
              </span>
              <span className="text-[13px] leading-normal text-text-2">{j.blurb}</span>
            </RadioGroupPrimitive.Item>
          </div>
        );
      })}
    </RadioGroupPrimitive.Root>
  );
}

// GrantPills lists the held grants with the reason the job brings each
// one, and the cross that drops exactly that grant.
export function GrantPills({ grants, why, onRemove }: { grants: string[]; why: Record<string, string>; onRemove: (grant: string) => void }) {
  return (
    <div className="flex flex-wrap gap-2" data-grant-pills>
      {grants.map((g) => (
        <span
          key={g}
          data-pill={g}
          className={cn("inline-flex items-center gap-2 rounded-md border px-2.5 py-1", SENSITIVE[g] ? "border-warn/40 bg-warn-bg" : "border-border bg-card")}
        >
          <b className={cn("font-mono text-[13px] font-semibold", SENSITIVE[g] ? "text-warn" : "text-foreground")}>{g}</b>
          <span className="text-[13px] text-muted-foreground">{why[g] || areaHint(g.split(":")[0])}</span>
          <Button variant="ghost" size="icon-xs" aria-label={removeGrant(g)} onClick={() => onRemove(g)}><XIcon /></Button>
        </span>
      ))}
    </div>
  );
}

const OFFERED: [string, string][] = [["none", LEVEL_NONE], ["read", LEVEL_READ], ["rw", LEVEL_RW]];

// Levels is the segmented control of one area. A held write without its
// read is a real server state, so it renders as itself and is never one of
// the levels offered.
function Levels({ area, level, onPick }: { area: string; level: string; onPick: (level: string) => void }) {
  const shown: [string, string][] = level === "write" ? [...OFFERED, ["write", LEVEL_WRITE]] : OFFERED;
  return (
    <RadioGroupPrimitive.Root
      aria-label={areaLevelLabel(area)}
      value={level}
      onValueChange={onPick}
      loop
      className="inline-flex overflow-hidden rounded-md border border-border"
    >
      {shown.map(([key, label]) => (
        <RadioGroupPrimitive.Item
          key={key}
          value={key}
          className={cn(
            "cursor-pointer border-r border-border px-3 py-1.5 text-[13px] outline-none last:border-r-0 focus-visible:ring-2 focus-visible:ring-ring/50",
            level === key ? "bg-accent-bg font-semibold text-foreground" : "text-text-2",
          )}
        >
          {label}
        </RadioGroupPrimitive.Item>
      ))}
    </RadioGroupPrimitive.Root>
  );
}

// AreaTable is the Custom path: one row per area, the level as a
// segmented control, and the meaning of the picked level printed under it
// rather than hidden in a hover.
export function AreaTable({ grants, onChange }: { grants: string[]; onChange: (next: string[]) => void }) {
  return (
    <div className="overflow-hidden rounded-md border border-border" data-area-table>
      <Table className="table-fixed">
        <colgroup>
          <col style={{ width: "120px" }} />
          <col />
          <col style={{ width: "300px" }} />
        </colgroup>
        <TableHeader>
          <TableRow>
            <TableHead className={HEAD}>{AREA_COLUMN.area}</TableHead>
            <TableHead className={HEAD}>{AREA_COLUMN.covers}</TableHead>
            <TableHead className={HEAD}>{AREA_COLUMN.access}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {AREAS.map(([area, hint]) => {
            const level = levelOf(grants, area);
            return (
              <TableRow key={area} data-area={area}>
                <TableCell className="align-top">
                  <b className={cn("font-mono text-[13px] font-semibold", area === "tokens" ? "text-warn" : "text-foreground")}>{area}</b>
                </TableCell>
                <TableCell className="align-top text-[13px] leading-snug text-text-2">{hint}</TableCell>
                <TableCell className="align-top">
                  <Levels area={area} level={level} onPick={(next) => onChange(grantsAtLevel(grants, area, next))} />
                  {level !== "none" && (
                    <span className="mt-1 block text-[13px] leading-snug text-muted-foreground" data-level-hint={area}>{levelHint(area, level)}</span>
                  )}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
