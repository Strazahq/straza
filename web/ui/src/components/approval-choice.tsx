import * as React from "react";
import { HelpTip } from "@/components/help-tip";
import { Input } from "@/components/ui/input";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { CAPS, HINT } from "@/components/wizard/parts";
import { SPONSOR, type Shape } from "@/lib/access-plan";
import type { RoleRow } from "@/lib/api";
import {
  HOW, HOW_HELP, HOW_HOLD, HOW_HOLD_LINE, HOW_TICKET, HOW_TICKET_END, HOW_TICKET_LINE, HOW_TICKET_MID, NO_APPROVER_ROLE, UNITS, W1,
  WHO_DECIDES, WHO_HELP, WHO_SPONSOR, WHO_TEAM, unitWord,
} from "@/lib/policy-words";
import { cn } from "@/lib/utils";
import { holders } from "@/lib/words";

// ApprovalChoice is the Who decides and How pair of an approval: the New
// policy wizard's How step and the access editor draw the same one, so a
// hold or a ticket reads and works alike in both.

type Unit = keyof typeof UNITS;

export const SECONDS: Record<Unit, number> = { seconds: 1, minutes: 60, hours: 3600, days: 86400 };
const UNIT_ORDER: Unit[] = ["seconds", "minutes", "hours", "days"];
const LARGEST_FIRST = [...UNIT_ORDER].reverse();

// FEW_HOLDERS and SHORT_HOLD are when a hold routed to an approver role is
// worth the warning under How: one or two people, or a window under two
// minutes, rarely see a hold answered before it expires.
const FEW_HOLDERS = 2;
const SHORT_HOLD = 120;

// spanOf reads a window in the largest unit that divides it evenly, so 120
// seconds is 2 minutes and 86400 is 1 day.
export function spanOf(seconds: number): { n: number; unit: Unit } {
  for (const unit of LARGEST_FIRST) if (seconds > 0 && seconds % SECONDS[unit] === 0) return { n: seconds / SECONDS[unit], unit };
  return { n: seconds, unit: "seconds" };
}

// Choice is one radio of a plain list: the dot, the label, what the choice
// needs answered beside it, and the line under it that says what it means.
export function Choice({ id, value, label, line, children }: { id: string; value: string; label: string; line?: React.ReactNode; children?: React.ReactNode }) {
  return (
    <div className="flex items-start gap-2.5">
      <RadioGroupItem value={value} id={id} aria-label={label} className="mt-1" />
      <div className="flex min-w-0 flex-col gap-1">
        <div className="flex flex-wrap items-center gap-2 text-sm text-foreground">
          <label htmlFor={id}>{label}</label>
          {children}
        </div>
        {line && <span className={HINT}>{line}</span>}
      </div>
    </div>
  );
}

// Head is a caps label with its help icon, the shape both questions open
// with.
function Head({ label, help }: { label: string; help: string }) {
  return (
    <span className={cn(CAPS, "flex items-center gap-1.5")}>
      {label}
      <HelpTip label={label} text={help} />
    </span>
  );
}

// Duration is a window as a number and a unit over the seconds the rule
// stores. The box keeps what was typed, so it can be emptied and retyped;
// only a whole number above zero changes the window.
function Duration({ label, seconds, onChange }: { label: string; seconds: number; onChange: (seconds: number) => void }) {
  const [typed, setTyped] = React.useState<{ text: string; unit: Unit; seconds: number } | null>(null);
  const read = spanOf(seconds);
  const span = typed && typed.seconds === seconds ? typed : { text: String(read.n), unit: read.unit };
  const write = (text: string, unit: Unit) => {
    const n = Number.parseInt(text, 10);
    const next = Number.isFinite(n) && n > 0 ? n * SECONDS[unit] : seconds;
    setTyped({ text, unit, seconds: next });
    if (next !== seconds) onChange(next);
  };
  return (
    <span className="inline-flex items-center gap-1.5">
      <Input type="number" min={1} aria-label={label} value={span.text} onChange={(e) => write(e.target.value, span.unit)} className="h-8 w-16" />
      <Select value={span.unit} onValueChange={(u) => write(span.text, u as Unit)}>
        <SelectTrigger aria-label={label + " unit"} className="h-8 w-28"><SelectValue /></SelectTrigger>
        <SelectContent>
          {UNIT_ORDER.map((u) => <SelectItem key={u} value={u}>{unitWord(Number.parseInt(span.text, 10) || 0, u)}</SelectItem>)}
        </SelectContent>
      </Select>
    </span>
  );
}

export type ApprovalChoiceProps = {
  shape: Shape;
  // approvers are the roles that may decide, with their holder counts.
  approvers: RoleRow[];
  onShape: (next: Shape) => void;
  // compact stacks the two questions, for a block inside a table row.
  compact?: boolean;
};

export function ApprovalChoice({ shape, approvers, onShape, compact }: ApprovalChoiceProps) {
  const id = React.useId();
  const set = (patch: Partial<Shape>) => onShape({ ...shape, ...patch });
  // The picker always shows a role: the pool when it names one, else the
  // role that picking An approver role would take, so it is never empty.
  const teams = approvers.map((r) => r.name);
  if (shape.pool !== SPONSOR && !teams.includes(shape.pool)) teams.push(shape.pool);
  const team = shape.pool !== SPONSOR ? shape.pool : teams[0] || "";
  const row = approvers.find((r) => r.name === team);
  const count = row ? row.holder_count || 0 : 0;
  const few = shape.pool !== SPONSOR && shape.how === "hold" && !!row && (count <= FEW_HOLDERS || shape.hold < SHORT_HOLD);

  return (
    <div className={cn("grid gap-4", !compact && "md:grid-cols-[1fr_1.35fr]")} data-approval-choice={shape.pool === SPONSOR ? "sponsor" : "team"}>
      <div className="flex min-w-0 flex-col gap-2">
        <Head label={WHO_DECIDES} help={WHO_HELP} />
        <RadioGroup
          aria-label={WHO_DECIDES}
          value={shape.pool === SPONSOR ? "sponsor" : "team"}
          onValueChange={(v) => (v === "sponsor" ? set({ pool: SPONSOR }) : team && set({ pool: team }))}
          className="gap-2.5"
        >
          <Choice id={id + "-sponsor"} value="sponsor" label={WHO_SPONSOR} />
          <Choice id={id + "-team"} value="team" label={WHO_TEAM} line={team ? (row ? holders(count) : undefined) : NO_APPROVER_ROLE}>
            {team && (
              <Select value={team} onValueChange={(v) => set({ pool: v })}>
                <SelectTrigger aria-label={WHO_TEAM} className="h-8 w-56"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {teams.map((name) => <SelectItem key={name} value={name}>{name}</SelectItem>)}
                </SelectContent>
              </Select>
            )}
          </Choice>
        </RadioGroup>
      </div>

      <div className="flex min-w-0 flex-col gap-2">
        <Head label={HOW} help={HOW_HELP} />
        {/* A window typed into one branch picks that branch: nobody sets a ticket's windows to keep a hold. */}
        <RadioGroup aria-label={HOW} value={shape.how} onValueChange={(v) => set({ how: v as Shape["how"] })} className="gap-2.5">
          <Choice id={id + "-hold"} value="hold" label={HOW_HOLD} line={HOW_HOLD_LINE}>
            <Duration label={HOW_HOLD} seconds={shape.hold} onChange={(hold) => set({ hold, how: "hold" })} />
          </Choice>
          <Choice id={id + "-ticket"} value="ticket" label={HOW_TICKET} line={HOW_TICKET_LINE}>
            <Duration label={HOW_TICKET} seconds={shape.ticket} onChange={(ticket) => set({ ticket, how: "ticket" })} />
            <span className="text-sm text-foreground">{HOW_TICKET_MID}</span>
            <Duration label={HOW_TICKET_END} seconds={shape.grant} onChange={(grant) => set({ grant, how: "ticket" })} />
            <span className="text-sm text-foreground">{HOW_TICKET_END}</span>
          </Choice>
        </RadioGroup>
        {few && <p className="m-0 max-w-[80ch] text-[13px] leading-relaxed text-warn" data-w1>{W1(count, shape.hold)}</p>}
      </div>
    </div>
  );
}
