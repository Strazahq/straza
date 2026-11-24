import type * as React from "react";
import { cn } from "@/lib/utils";

// The parts every detail sheet of the console is built from: a state row
// with its one action, a facts grid, a
// line of numbers that link somewhere, and a titled section holding a list.
// No part carries explanatory prose; the sentence that explains a verb
// lives in that verb's dialog or tooltip.

// Section is a titled block: a small uppercase label, then its children.
export function Section({ title, action, children, className }: { title: string; action?: React.ReactNode; children: React.ReactNode; className?: string }) {
  return (
    <section className={cn("flex flex-col gap-2", className)} data-section={title}>
      <div className="flex items-center gap-2">
        <h3 className="text-caption font-semibold uppercase tracking-[.06em] text-muted-foreground">{title}</h3>
        {action && <div className="ml-auto">{action}</div>}
      </div>
      {children}
    </section>
  );
}

// Facts is the label and value grid. A value that is absent reads as a
// word ("none", "never", "not set"), never a dash.
export function Facts({ rows }: { rows: [string, React.ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[130px_1fr] gap-x-3 gap-y-1.5 text-dense" data-facts>
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words text-foreground">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

// ListRow is one row of a section list: its content left, its actions on
// the right end.
export function ListRow({ children, actions, className }: { children: React.ReactNode; actions?: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-wrap items-center gap-2.5 rounded-md border border-border px-3 py-2 text-dense", className)} data-list-row>
      {children}
      {actions && <span className="ml-auto flex shrink-0 items-center gap-1.5">{actions}</span>}
    </div>
  );
}
