import * as React from "react";
import { ChevronRightIcon } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { FetchError, RefusedError } from "@/components/error-state";
import { didNotRun } from "@/lib/server-words";
import { cn } from "@/lib/utils";
import type { HealthRead } from "@/lib/words";

// The pieces every wizard step draws with. Wizards after this one reuse
// them, so each takes only what a step needs to say.

export const CAPS = "text-caption font-semibold uppercase tracking-[.06em] text-muted-foreground";
export const HINT = "text-caption leading-snug text-muted-foreground";
export const ERROR = "text-caption leading-snug text-danger";
// LAB is a field label in sentence case, and FILL a grid of fields that
// keeps one field at the width of a column.
export const LAB = "text-dense font-semibold text-foreground";
export const FILL = "grid gap-3 grid-cols-[repeat(auto-fill,minmax(260px,1fr))]";

type FieldProps = { id: string; label: string; error?: string; hint?: React.ReactNode; children: React.ReactNode };

// Field is one labeled input: the caps label above, the input, then the
// error in the danger hue when there is one, the hint otherwise. The
// input's aria-describedby points at id + "-say".
export function Field({ id, label, error, hint, children }: FieldProps) {
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className={CAPS}>{label}</label>
      {children}
      {error ? <p id={id + "-say"} className={ERROR}>{error}</p> : hint ? <p id={id + "-say"} className={HINT}>{hint}</p> : null}
    </div>
  );
}

// Fold is a closed disclosure with a one-line title, for what a step keeps
// out of the way until asked: the manifest, the long form of a choice.
export function Fold({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Collapsible className="rounded-md border border-border bg-background">
      <CollapsibleTrigger className="group flex w-full items-center gap-1.5 rounded-md px-2.5 py-1.5 text-left text-caption text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50">
        <ChevronRightIcon aria-hidden="true" className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        {title}
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t border-border">{children}</CollapsibleContent>
    </Collapsible>
  );
}

// Code is a block of text to copy: a command, a manifest, a snippet.
export function Code({ children, className }: { children: string; className?: string }) {
  return <pre className={cn("m-0 overflow-x-auto rounded-md border border-border bg-background px-3.5 py-2.5 font-mono text-caption leading-relaxed text-text-2", className)}>{children}</pre>;
}

// ReviewFacts is the facts grid of a Review step: the label column beside
// each value from the sm width, and each label above its value below it.
export function ReviewFacts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="m-0 grid grid-cols-1 gap-y-1 text-dense sm:grid-cols-[128px_minmax(0,1fr)] sm:gap-x-4 sm:gap-y-2.5" data-review-facts>
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="m-0 mb-2 min-w-0 break-words text-foreground sm:mb-0" data-fact={k}>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

type GroupProps = { label: string; value: string; onPick: (v: string) => void; className?: string; children: React.ReactNode };

// CardGroup is a radiogroup of option cards with the arrow keys of a radio
// group. A card marked off never becomes the value.
export function CardGroup({ label, value, onPick, className, children }: GroupProps) {
  return (
    <RadioGroupPrimitive.Root aria-label={label} value={value} onValueChange={onPick} loop className={cn("grid gap-2.5", className)}>
      {children}
    </RadioGroupPrimitive.Root>
  );
}

type CardProps = {
  value: string;
  // label is the radio's accessible name, the card's name when absent.
  label?: string;
  // lead is a glyph drawn before the name, for a card whose choice carries
  // a mark of its own.
  lead?: React.ReactNode;
  name: string;
  // tag is the small fact at the card's top right, such as a holder count.
  tag?: string;
  line: React.ReactNode;
  on: boolean;
  off?: boolean;
  dashed?: boolean;
  row?: boolean;
  children?: React.ReactNode;
};

// OptionCard is one choice: a radio dot, the name, a small fact in mono
// when the card has one, and one line under it. A row is the smaller shape
// of the agents picker. The open card's fields sit below the radio, not
// inside it, so they stay inputs of their own for assistive technology.
export function OptionCard({ value, label, lead, name, tag, line, on, off, dashed, row, children }: CardProps) {
  return (
    <div className={cn("flex flex-col rounded-md border", row ? "bg-background" : "bg-card", on ? "border-link" : "border-border", on && !row && "bg-accent-bg", dashed && "border-dashed", off && "opacity-60")}>
      <RadioGroupPrimitive.Item
        value={value}
        aria-label={label || name}
        aria-disabled={off || undefined}
        className={cn("flex flex-col text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 rounded-md", row ? "gap-0.5 px-2.5 py-2" : "gap-1.5 px-3.5 py-3", off ? "cursor-not-allowed" : "cursor-pointer")}
      >
        <span className="flex items-center gap-2">
          <span aria-hidden="true" className={cn("relative size-3.5 shrink-0 rounded-full border-[1.5px]", on ? "border-link after:absolute after:inset-[2.5px] after:rounded-full after:bg-link" : "border-muted-foreground")} />
          {lead}
          <span className="font-semibold text-foreground">{name}</span>
          {tag && <span className={cn("font-mono text-caption leading-4 text-muted-foreground", row ? "ml-1.5" : "ml-auto")}>{tag}</span>}
        </span>
        {line && <span className={cn("leading-normal text-text-2", row ? "pl-5.5 text-caption" : "text-dense")}>{line}</span>}
      </RadioGroupPrimitive.Item>
      {children && <div className="mx-3.5 mb-3 flex flex-col gap-2.5 border-t border-border pt-2.5">{children}</div>}
    </div>
  );
}

// RowGroup is a radiogroup of radio rows in one bordered box, with the
// arrow keys of a radio group. A row marked off never becomes the value.
export function RowGroup({ label, value, onPick, children }: Omit<GroupProps, "className">) {
  return (
    <RadioGroupPrimitive.Root aria-label={label} value={value} onValueChange={onPick} loop className="flex flex-col overflow-hidden rounded-md border border-border bg-background">
      {children}
    </RadioGroupPrimitive.Root>
  );
}

type RowProps = {
  value: string;
  label: string;
  // fact is the pill at the row's right.
  fact?: React.ReactNode;
  // line is the one sentence under the label, which the radio is
  // described by: why the row is off, or the hint of a picked small row.
  line?: string;
  on: boolean;
  off?: boolean;
  // small is the lighter row of a picker inside a picked row.
  small?: boolean;
  children?: React.ReactNode;
};

// RadioRow is one row of a RowGroup: a radio dot, the label, the fact at
// the right, the line under it, and the picked row's fields under that.
// The fields sit outside the radio, so they stay inputs of their own for
// assistive technology.
export function RadioRow({ value, label, fact, line, on, off, small, children }: RowProps) {
  const lineId = React.useId();
  return (
    <div className={cn("flex flex-col border-t border-border first:border-t-0", on && "shadow-[inset_3px_0_0_var(--accent)]")} data-row={value}>
      <RadioGroupPrimitive.Item
        value={value}
        aria-label={label}
        aria-disabled={off || undefined}
        aria-describedby={line ? lineId : undefined}
        className={cn(
          "flex w-full items-center gap-2.5 text-left text-dense text-foreground outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:ring-inset",
          small ? "min-h-9 px-3 py-[7px]" : "min-h-11 px-3.5 py-2.5",
          on ? "bg-accent-bg" : !off && "hover:bg-card",
          off ? "cursor-not-allowed" : "cursor-pointer",
        )}
      >
        <span aria-hidden="true" className={cn("relative size-3.5 shrink-0 rounded-full border-[1.5px]", on ? "border-link after:absolute after:inset-[2.5px] after:rounded-full after:bg-link" : "border-muted-foreground", off && "border-dashed")} />
        <span className={cn("min-w-0 flex-1", small && !on ? "font-normal" : "font-semibold", off && "text-muted-foreground")}>{label}</span>
        {fact && <span className="ml-auto inline-flex shrink-0">{fact}</span>}
      </RadioGroupPrimitive.Item>
      {line && <p id={lineId} className={cn("m-0 -mt-1 pr-3.5 pb-2.5 text-caption leading-snug text-muted-foreground", small ? "pl-9" : "pl-[38px]")}>{line}</p>}
      {children && <div className="grid gap-3.5 border-t border-border pt-3 pr-3.5 pb-4 pl-3.5 sm:pl-[38px] [&>*]:min-w-0" data-row-fields>{children}</div>}
    </div>
  );
}

export type RowState = "pending" | "running" | "done" | "failed" | "refused";
export type Row = { key: "install" | "secret" | "listing" | "check"; label: string; state: RowState };

const ROW_HUE: Record<RowState, string> = { pending: "text-muted-foreground", running: "text-muted-foreground", done: "text-ok", failed: "text-danger", refused: "text-danger" };

// Landed lists the install rows: a number before they run, then the state
// word each landed in. The number takes a narrow column, and the state
// word one wide enough for the longest state.
export function Landed({ rows, numbered }: { rows: Row[]; numbered?: boolean }) {
  return (
    <ol className="m-0 flex list-none flex-col gap-1 p-0 text-dense" data-landed>
      {rows.map((r, i) => (
        <li key={r.key} className="flex items-center gap-2.5">
          <span className={cn("shrink-0 font-mono text-caption leading-4", numbered ? "w-4 text-muted-foreground" : cn("w-16", ROW_HUE[r.state]))}>{numbered ? i + 1 : r.state}</span>
          <span className="text-foreground">{r.label}</span>
        </li>
      ))}
    </ol>
  );
}

const TONE: Record<HealthRead["tone"], { box: string; lead: string }> = {
  ok: { box: "border-ok/40 bg-ok-bg", lead: "text-ok" },
  warn: { box: "border-warn/40 bg-warn-bg", lead: "text-warn" },
  danger: { box: "border-danger/40 bg-danger-bg", lead: "text-danger" },
};

// HealthBox is what the check read, in its tone: the lead, then the
// sentence.
export function HealthBox({ read }: { read: HealthRead }) {
  return (
    <div className={cn("rounded-md border px-3 py-2.5 text-dense text-foreground", TONE[read.tone].box)} data-health={read.kind}>
      <b className={cn("font-semibold", TONE[read.tone].lead)}>{read.lead}</b>{" " + read.text}
    </div>
  );
}

// Problem is a write that did not land. heading words a state that is not
// known, where the default "<subject> did not run." would be wrong.
export type Problem = { subject: string; text: string; unreachable: boolean; heading?: string };

// ProblemBlock is a write that did not land: violet when it never reached
// strazad and the state is unknown, red with the server's sentence when
// the server refused it.
export function ProblemBlock({ problem }: { problem: Problem | null }) {
  if (!problem) return null;
  return problem.unreachable
    ? <FetchError subject={problem.subject} heading={problem.heading ?? didNotRun(problem.subject)} detail={problem.text} />
    : <RefusedError subject={problem.subject} message={problem.text} />;
}
