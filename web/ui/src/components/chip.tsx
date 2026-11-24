import type * as React from "react";
import { BanIcon, CheckIcon, CircleSlashIcon, HourglassIcon, LockIcon, type LucideIcon, MinusIcon, PencilIcon, PlusIcon, PowerIcon } from "lucide-react";
import { CHANGE_WORD, OUTCOME_WORD, STATUS_WORD, outcomeCount, type Change, type Outcome, type Status } from "@/lib/chip-words";
import { cn } from "@/lib/utils";

// The three shapes of the console's small labels: a Chip says an outcome or
// a state in its tone, a Tag names a thing, and a Pill carries a count or a
// fact. A ToolChip is a tool name, with its outcome when one is known.

export type Tone = "ok" | "warn" | "danger" | "accent" | "teal" | "unknown" | "plain";

// Data is the data-* attributes a caller hands through to the element, the
// hooks that tests read.
type Data = { [key: `data-${string}`]: string | number | boolean | undefined };

// A tone's fill is its tint laid over the page background, so a chip reads
// the same on a tinted, a selected or a hovered row as on a plain one.
const TINT = "bg-background bg-linear-to-b";
const TONE: Record<Tone, string> = {
  ok: cn("border-ok text-ok from-ok-bg to-ok-bg", TINT),
  warn: cn("border-warn text-warn from-warn-bg to-warn-bg", TINT),
  danger: cn("border-danger text-danger from-danger-bg to-danger-bg", TINT),
  accent: cn("border-link text-link from-accent-bg to-accent-bg", TINT),
  teal: cn("border-teal text-teal from-teal-bg to-teal-bg", TINT),
  unknown: cn("border-unknown text-unknown from-unknown-bg to-unknown-bg", TINT),
  plain: "border-input bg-secondary text-foreground",
};

type ChipProps = Data & { tone: Tone; icon?: LucideIcon; title?: string; className?: string; children: React.ReactNode };

// Chip is an outcome or a state in its tone: mono, a border and a fill of
// the tone, and an optional icon. data-tone names the tone for the tests.
export function Chip({ tone, icon: Icon, title, className, children, ...data }: ChipProps) {
  return (
    <span
      title={title}
      data-tone={tone}
      className={cn("inline-flex items-center gap-1 rounded-md border px-[7px] py-0.5 align-middle font-mono text-caption leading-[18px] whitespace-nowrap", TONE[tone], className)}
      {...data}
    >
      {Icon && <Icon className="size-3.5" aria-hidden="true" />}
      {children}
    </span>
  );
}

const OUTCOME: Record<Outcome, { icon: LucideIcon; tone: Tone }> = {
  allowed: { icon: CheckIcon, tone: "ok" },
  approval: { icon: HourglassIcon, tone: "warn" },
  denied: { icon: BanIcon, tone: "danger" },
  none: { icon: MinusIcon, tone: "plain" },
};

// OutcomeChip is what happens to a call, as one of the four outcome words,
// or as a count of calls with that outcome.
export function OutcomeChip({ outcome, count, title, className }: { outcome: Outcome; count?: number; title?: string; className?: string }) {
  const o = OUTCOME[outcome];
  return (
    <Chip tone={o.tone} icon={o.icon} title={title} className={className} data-outcome={outcome}>
      {count === undefined ? OUTCOME_WORD[outcome] : outcomeCount(count, outcome)}
    </Chip>
  );
}

// isStatus says whether a status word from the server is one of the five a
// StatusChip draws.
export const isStatus = (s: string): s is Status => Object.hasOwn(STATUS_WORD, s);

const STATUS: Record<Exclude<Status, "active">, { tone: Tone; icon?: LucideIcon }> = {
  disabled: { tone: "plain", icon: CircleSlashIcon },
  locked: { tone: "danger", icon: LockIcon },
  revoked: { tone: "danger", icon: BanIcon },
  closed: { tone: "plain" },
};

// StatusChip is the state of a user, a session, a device or a key. An
// active thing is a green dot and the muted word with no box, so the other
// states stand out.
export function StatusChip({ status, title, ...data }: Data & { status: Status; title?: string }) {
  if (status === "active") {
    return (
      <span title={title} data-status-word={status} className="inline-flex items-center gap-1.5 align-middle font-mono text-caption leading-[18px] whitespace-nowrap text-muted-foreground" {...data}>
        <span className="size-[7px] rounded-pill bg-ok" aria-hidden="true" />
        {STATUS_WORD[status]}
      </span>
    );
  }
  const s = STATUS[status];
  return (
    <Chip tone={s.tone} icon={s.icon} title={title} data-status-word={status} {...data}>
      {STATUS_WORD[status]}
    </Chip>
  );
}

const CHANGE: Record<Change, { icon: LucideIcon; tone: Tone }> = {
  new: { icon: PlusIcon, tone: "accent" },
  changed: { icon: PencilIcon, tone: "plain" },
  removed: { icon: MinusIcon, tone: "danger" },
  off: { icon: PowerIcon, tone: "plain" },
  on: { icon: PowerIcon, tone: "accent" },
};

// ChangeChip is what a draft does to an object, or an unpublished edit to a
// policy rule.
export function ChangeChip({ change, ...data }: Data & { change: Change }) {
  const c = CHANGE[change];
  return (
    <Chip tone={c.tone} icon={c.icon} data-change={change} {...data}>
      {CHANGE_WORD[change]}
    </Chip>
  );
}

type TagProps = Data & { icon?: LucideIcon; agent?: boolean; title?: string; className?: string; children: React.ReactNode };

// Tag names a thing: a role, a server, a runtime. An AI agent's tag takes
// the teal fill, the one hue that marks a machine.
export function Tag({ icon: Icon, agent, title, className, children, ...data }: TagProps) {
  return (
    <span
      title={title}
      className={cn("inline-flex items-center gap-[5px] rounded-md border border-border py-0.5 align-middle font-mono text-caption leading-[18px] whitespace-nowrap text-foreground", Icon ? "pr-2 pl-1.5" : "px-1.5", agent ? "bg-teal-bg" : "bg-card", className)}
      {...data}
    >
      {Icon && <Icon className={cn("size-3.5", agent ? "text-teal" : "text-muted-foreground")} aria-hidden="true" />}
      {children}
    </span>
  );
}

type PillProps = Data & { icon?: LucideIcon; title?: string; className?: string; children: React.ReactNode };

// Pill is a count or a fact in a round shape with no border.
export function Pill({ icon: Icon, title, className, children, ...data }: PillProps) {
  return (
    <span
      title={title}
      className={cn("inline-flex items-center gap-[5px] rounded-pill bg-pill px-2.5 py-0.5 align-middle font-sans text-caption leading-5 whitespace-nowrap text-text-2", className)}
      {...data}
    >
      {Icon && <Icon className="size-3.5 text-muted-foreground" aria-hidden="true" />}
      {children}
    </span>
  );
}

const TOOL_FILL: Record<Exclude<Outcome, "none">, string> = { allowed: "bg-ok-bg", approval: "bg-warn-bg", denied: "bg-danger-bg" };
const TOOL_ICON: Record<Exclude<Outcome, "none">, string> = { allowed: "text-ok", approval: "text-warn", denied: "text-danger" };

// ToolChip is a tool name. With an outcome it takes the outcome's fill and
// a small icon of it; the outcome none keeps the plain look.
export function ToolChip({ outcome, title, children }: { outcome?: Outcome; title?: string; children: React.ReactNode }) {
  // A name wider than its row is cut inside the chip, with the whole of it
  // on hover.
  const hover = title || (typeof children === "string" ? children : undefined);
  if (!outcome || outcome === "none") {
    return <span title={hover} className="inline-block max-w-full truncate rounded-md bg-pill px-1 align-bottom font-mono text-caption leading-5 text-text-2">{children}</span>;
  }
  const Icon = OUTCOME[outcome].icon;
  return (
    <span title={hover} data-outcome={outcome} className={cn("inline-flex max-w-full items-center gap-[3px] rounded-md pr-1 pl-[3px] font-mono text-caption leading-5 whitespace-nowrap text-foreground", TOOL_FILL[outcome])}>
      <Icon className={cn("size-3 shrink-0", TOOL_ICON[outcome])} aria-hidden="true" />
      <span title={hover} className="min-w-0 truncate">{children}</span>
    </span>
  );
}
