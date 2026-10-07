import type * as React from "react";
import { ChevronRightIcon } from "lucide-react";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { FetchError, RefusedError } from "@/components/error-state";
import { cn } from "@/lib/utils";

// The pieces every wizard step draws with. Wizards after this one reuse
// them, so each takes only what a step needs to say.

export const CAPS = "text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
export const HINT = "text-[13px] leading-snug text-muted-foreground";
export const ERROR = "text-[13px] leading-snug text-danger";
export const TWO = "grid gap-3 grid-cols-[repeat(auto-fit,minmax(260px,1fr))]";

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
      <CollapsibleTrigger className="group flex w-full items-center gap-1.5 rounded-md px-2.5 py-1.5 text-left text-[13px] text-muted-foreground outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50">
        <ChevronRightIcon aria-hidden="true" className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        {title}
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t border-border">{children}</CollapsibleContent>
    </Collapsible>
  );
}

// ManifestFold is the live canonical preview under the Server and the
// Credential steps: the app.yaml the answers so far would install.
export function ManifestFold({ yaml }: { yaml: string }) {
  return (
    <Fold title="The manifest this writes (app.yaml)">
      <pre className="m-0 overflow-x-auto px-3.5 py-2.5 font-mono text-[13px] leading-relaxed text-text-2" data-manifest>{yaml}</pre>
    </Fold>
  );
}

// Code is a block of text to copy: a command, a manifest, a snippet.
export function Code({ children, className }: { children: string; className?: string }) {
  return <pre className={cn("m-0 overflow-x-auto rounded-md border border-border bg-background px-3.5 py-2.5 font-mono text-[13px] leading-relaxed text-text-2", className)}>{children}</pre>;
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
  label: string;
  // lead is a glyph drawn before the name, for a card whose choice carries
  // a mark of its own.
  lead?: React.ReactNode;
  name: string;
  tag: string;
  line: React.ReactNode;
  on: boolean;
  off?: boolean;
  dashed?: boolean;
  row?: boolean;
  children?: React.ReactNode;
};

// OptionCard is one choice: a radio dot, the name, the value it writes in
// mono, and one line under it. A row is the smaller shape of the agents
// picker. The open card's fields sit below the radio, not inside it, so
// they stay inputs of their own for assistive technology.
export function OptionCard({ value, label, lead, name, tag, line, on, off, dashed, row, children }: CardProps) {
  return (
    <div className={cn("flex flex-col rounded-md border", row ? "bg-background" : "bg-card", on ? "border-link" : "border-border", on && !row && "bg-accent-bg", dashed && "border-dashed", off && "opacity-60")}>
      <RadioGroupPrimitive.Item
        value={value}
        aria-label={label}
        aria-disabled={off || undefined}
        className={cn("flex flex-col text-left outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 rounded-md", row ? "gap-0.5 px-2.5 py-2" : "gap-1.5 px-3.5 py-3", off ? "cursor-not-allowed" : "cursor-pointer")}
      >
        <span className="flex items-center gap-2">
          <span aria-hidden="true" className={cn("relative size-3.5 shrink-0 rounded-full border-[1.5px]", on ? "border-link after:absolute after:inset-[2.5px] after:rounded-full after:bg-link" : "border-muted-foreground")} />
          {lead}
          <span className="font-semibold text-foreground">{name}</span>
          <span className={cn("font-mono text-xs text-muted-foreground", row ? "ml-1.5" : "ml-auto")}>{tag}</span>
        </span>
        <span className={cn("leading-normal text-text-2", row ? "pl-5.5 text-[13px]" : "text-sm")}>{line}</span>
      </RadioGroupPrimitive.Item>
      {children && <div className="mx-3.5 mb-3 flex flex-col gap-2.5 border-t border-border pt-2.5">{children}</div>}
    </div>
  );
}

export type RowState = "pending" | "running" | "done" | "failed" | "refused";
export type Row = { key: "install" | "secret" | "check"; label: string; state: RowState };

const ROW_HUE: Record<RowState, string> = { pending: "text-muted-foreground", running: "text-muted-foreground", done: "text-ok", failed: "text-danger", refused: "text-danger" };

// Landed lists the install rows: a number before they run, then the state
// word each landed in. The number takes a narrow column, and the state
// word one wide enough for the longest state.
export function Landed({ rows, numbered }: { rows: Row[]; numbered?: boolean }) {
  return (
    <ol className="m-0 flex list-none flex-col gap-1 p-0 text-sm" data-landed>
      {rows.map((r, i) => (
        <li key={r.key} className="flex items-center gap-2.5">
          <span className={cn("shrink-0 font-mono text-xs", numbered ? "w-4 text-muted-foreground" : cn("w-16", ROW_HUE[r.state]))}>{numbered ? i + 1 : r.state}</span>
          <span className="text-foreground">{r.label}</span>
        </li>
      ))}
    </ol>
  );
}

export type Problem = { subject: string; text: string; unreachable: boolean };

// ProblemBlock is a write that did not land: violet when it never reached
// strazad and the state is unknown, red with the server's sentence when
// the server refused it.
export function ProblemBlock({ problem }: { problem: Problem | null }) {
  if (!problem) return null;
  return problem.unreachable ? <FetchError subject={problem.subject} detail={problem.text} /> : <RefusedError subject={problem.subject} message={problem.text} />;
}
