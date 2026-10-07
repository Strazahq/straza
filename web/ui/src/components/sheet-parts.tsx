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
        <h3 className="text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground">{title}</h3>
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
    <dl className="grid grid-cols-[130px_1fr] gap-x-3 gap-y-1.5 text-sm" data-facts>
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words text-foreground">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

// Glance is one line of counts, each a link where a screen answers it.
export function Glance({ items }: { items: { n: number; label: string; onOpen?: () => void }[] }) {
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1.5 text-sm text-text-2" data-glance>
      {items.map((it) => {
        const body = <><b className="font-semibold tabular-nums text-foreground">{it.n}</b> {it.label}</>;
        return it.onOpen ? (
          <button key={it.label} type="button" className="hover:text-foreground hover:underline underline-offset-4" onClick={it.onOpen}>{body}</button>
        ) : (
          <span key={it.label}>{body}</span>
        );
      })}
    </div>
  );
}

// StateRow is one row of a state box: a label, the state, and the one
// action that changes it on the right. tone paints the row for a state
// that needs attention.
export function StateRow({ label, children, action, tone, className }: { label: string; children: React.ReactNode; action?: React.ReactNode; tone?: "danger" | "warn"; className?: string }) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center gap-3 border-b border-border px-3 py-2.5 text-sm last:border-b-0",
        tone === "danger" && "bg-danger-bg",
        tone === "warn" && "bg-warn-bg",
        className,
      )}
      data-state-row={label}
    >
      <span className="w-24 shrink-0 text-muted-foreground">{label}</span>
      <span className="min-w-0 flex-1 text-foreground">{children}</span>
      {action && <span className="ml-auto shrink-0">{action}</span>}
    </div>
  );
}

// StateBox frames the state rows of a sheet.
export function StateBox({ children }: { children: React.ReactNode }) {
  return <div className="overflow-hidden rounded-md border border-border" data-state-box>{children}</div>;
}

// ListRow is one row of a section list: its content left, its actions on
// the right end.
export function ListRow({ children, actions, className }: { children: React.ReactNode; actions?: React.ReactNode; className?: string }) {
  return (
    <div className={cn("flex flex-wrap items-center gap-2.5 rounded-md border border-border px-3 py-2 text-sm", className)} data-list-row>
      {children}
      {actions && <span className="ml-auto flex shrink-0 items-center gap-1.5">{actions}</span>}
    </div>
  );
}
