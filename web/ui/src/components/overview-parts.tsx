import * as React from "react";
import { ArrowRightIcon, ChevronDownIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { HelpTip } from "@/components/help-tip";
import type { ApprovalRow } from "@/lib/api";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { type RouteKey, routeByKey } from "@/lib/routes";
import { cn } from "@/lib/utils";

// The chrome the Overview panels share: the panel box with its header and
// help icon, the
// door that ends a line, and the reading of the chain check that both the
// tile and the attention list render.

// ChainRead is what this browser can say about the chain after a read:
// intact and broken are proven here by re-hashing, seat and unverified say
// why nothing was proven, unknown is the state before the first answer.
export type ChainRead = { word: "intact" | "broken" | "seat" | "unverified" | "unknown"; seq: number };

// oldestPending is the request that has waited longest, by the stamp the
// server set when it was held. The tile and the attention line name the
// same one, so both read it here.
export function oldestPending(rows: ApprovalRow[]): ApprovalRow | null {
  return rows.reduce<ApprovalRow | null>((old, r) => (!old || r.createdAt < old.createdAt ? r : old), null);
}

type DoorProps = {
  to: RouteKey;
  rest?: string[];
  // label names the door when the area's own name is not the right word,
  // such as a tab inside an area.
  label?: string;
  // hash opens a row inside the target screen; it rides the address so a
  // copied link lands in the same place.
  hash?: string;
  // arrow is off for a door that sits inside a sentence, where the mark
  // would read as punctuation.
  arrow?: boolean;
  className?: string;
};

// Door is the link at the end of a line or a panel header: where to go and
// an arrow. A plain click navigates inside the app; every other click is
// the browser's, so a new tab still works.
export function Door({ to, rest, label, hash, arrow = true, className }: DoorProps) {
  const path = pathFor(to, rest) + (hash ? "#" + hash : "");
  return (
    <a
      href={path}
      data-door={to}
      className={cn("inline-flex items-center gap-1 text-[13px] whitespace-nowrap text-link hover:underline", className)}
      onClick={(e) => {
        if (!isPlainClick(e)) return;
        e.preventDefault();
        navigate(to, rest || []);
        if (hash) window.history.replaceState(null, "", path);
      }}
    >
      {label || routeByKey(to).label}
      {arrow && <ArrowRightIcon className="size-3 shrink-0" aria-hidden="true" />}
    </a>
  );
}

type PanelProps = {
  // name is the panel's handle for a test and for the eye pass.
  name: string;
  title: string;
  help: string;
  // lead sits beside the title: a count, a posture badge.
  lead?: React.ReactNode;
  // right fills the end of the header: a line with a door, or the switch.
  right?: React.ReactNode;
  // flush drops the body padding, for a table that reaches the panel edge.
  flush?: boolean;
  reveal?: boolean;
  children: React.ReactNode;
};

// Panel is one Overview widget: the header with its title, the help icon
// that carries the sentence, and the body.
export function Panel({ name, title, help, lead, right, flush, reveal, children }: PanelProps) {
  const id = React.useId();
  const collapsible = ["posture", "signed-in", "live"].includes(name);
  const [open, setOpen] = React.useState(!collapsible || !!reveal);
  React.useEffect(() => { if (reveal) setOpen(true); }, [reveal]);
  return (
    <section aria-labelledby={id} data-panel={name} className="min-w-0 rounded-md border border-border bg-card">
      <header className="flex flex-wrap items-center gap-2.5 border-b border-border px-3.5 py-2.5">
        <h2 id={id} className="flex items-center gap-2 text-[15px] font-semibold text-foreground">
          {title}
          {lead}
          <HelpTip label={title} text={help} />
        </h2>
        {right && <span className="ml-auto flex items-center gap-2.5 text-[13px] text-muted-foreground">{right}</span>}
        {collapsible && <Button type="button" variant="outline" size="sm" className="panel-disclosure" aria-expanded={open} aria-controls={id + "-body"} aria-label={(open ? "Hide " : "Show ") + title.toLowerCase()} onClick={() => setOpen(!open)}><ChevronDownIcon className={cn("size-3.5", open && "rotate-180")} aria-hidden="true" />{open ? "Hide details" : "Show details"}</Button>}
      </header>
      <div id={id + "-body"} hidden={!open} className={cn(flush ? "" : "flex flex-col gap-2.5 px-3.5 py-3", !open && "hidden")}>{children}</div>
    </section>
  );
}

// PanelNote is the one muted line a panel shows in place of its data: what
// would be here and why it is not.
export function PanelNote({ children }: { children: React.ReactNode }) {
  return <p className="max-w-[80ch] text-sm leading-relaxed text-text-2" data-panel-note>{children}</p>;
}

// TONE paints one word of the decision, wiring and server vocabulary in
// the reserved trust hues. The map is the one SessionBadge uses; it is
// repeated here so the entry page does not pull a sheet in to badge a word.
const TONE: Record<string, string> = {
  ok: "bg-ok-bg text-ok border-ok/40",
  warn: "bg-warn-bg text-warn border-warn/40",
  danger: "bg-danger-bg text-danger border-danger/40",
  unknown: "bg-unknown-bg text-unknown border-unknown/40",
  plain: "border-border bg-background text-text-2",
};

// ToneBadge renders one state word in its hue, with the sentence that
// explains the word on its tooltip.
export function ToneBadge({ tone, word, title }: { tone: string; word: string; title?: string }) {
  return (
    <Badge variant="outline" title={title} data-tone={tone} className={cn("rounded-md px-2 font-mono text-[13px] font-normal", TONE[tone] || TONE.plain)}>
      {word}
    </Badge>
  );
}
