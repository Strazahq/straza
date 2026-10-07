import * as React from "react";
import { Door, Panel, PanelNote, ToneBadge } from "@/components/overview-parts";
import { DecisionChart, HOUR_MS, MINUTE_MS } from "@/components/overview-decision-chart";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { DecisionBucket, DecisionsBlock, StoppedRow } from "@/lib/api";
import {
  CATALOG_APART, DECIDED_ALLOWED, DECIDED_APPROVAL, DECIDED_DENIED, DECIDED_HELP, DECIDED_NONE, DECIDED_PANEL, DECIDED_UNREAD, LAST_24, LAST_HOUR, LAST_HOUR_HELP, LAST_HOUR_NONE, OPEN_AUDIT,
  STOPPED_COLUMN, STOPPED_HELP, STOPPED_NONE, STOPPED_TITLE, STOPPED_UNREAD, TOTAL_LABEL, WINDOW_LABEL, nfmt, outcomeWord, seatLine, stoppedRowLabel, toolWords,
} from "@/lib/config-words";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { cn } from "@/lib/utils";

// HourRead is the last hour view while it is on screen: the minutes the
// server counted, or the sentence that stands in for them.
export type HourRead = { minutes: DecisionBucket[] | null; note: string };

// Decisions keeps all three outcomes and the hourly graph together. Catalog
// reads are no outcome: their sum sits apart from the totals and off the
// graph, and is left out when it is zero or the server sent none. denied
// says the session may not read the counts, which travel on the config
// area's overview read. The switch in the header turns the last hour view
// on and off: while hour is set, the totals and the graph count its minutes.
export function Decisions({ block, denied = false, hour, onHour }: { block: DecisionsBlock | null; denied?: boolean; hour: HourRead | null; onHour: (on: boolean) => void }) {
  const buckets = (hour ? hour.minutes : block?.buckets) || [];
  const totals = buckets.reduce((t, b) => ({ allowed: t.allowed + (b.allowed || 0), approval: t.approval + (b.approval || 0), denied: t.denied + (b.denied || 0), catalog: t.catalog + (b.catalog || 0) }), { allowed: 0, approval: 0, denied: 0, catalog: 0 });
  return <Panel name="decisions" title={DECIDED_PANEL} help={hour ? LAST_HOUR_HELP : DECIDED_HELP} right={<span role="group" aria-label={WINDOW_LABEL} className="flex items-center">
    {[false, true].map((on) => <Button key={on ? "hour" : "day"} type="button" variant="outline" size="sm" aria-pressed={!!hour === on} onClick={() => onHour(on)}
      className={cn("-ml-px rounded-none first:ml-0 first:rounded-l-md last:rounded-r-md", !!hour === on && "bg-accent-bg text-foreground")}>{on ? LAST_HOUR : LAST_24}</Button>)}
  </span>}>
    {!buckets.length || totals.allowed + totals.approval + totals.denied === 0 ? <PanelNote>{hour ? (hour.minutes ? LAST_HOUR_NONE : hour.note) : block ? DECIDED_NONE : denied ? seatLine(DECIDED_PANEL, "config:read") : DECIDED_UNREAD}</PanelNote> : <div className="overview-decisions">
      <div className="flex flex-wrap items-start gap-x-6">
        <div className="overview-decision-totals min-w-0" data-decided-totals>
          {([
            [DECIDED_ALLOWED, TOTAL_LABEL.allowed, totals.allowed, "chart-allowed"],
            [DECIDED_DENIED, TOTAL_LABEL.denied, totals.denied, "chart-denied"],
            [DECIDED_APPROVAL, TOTAL_LABEL.approval, totals.approval, "chart-approval"],
          ] as const).map(([word, label, count, tone]) => <div key={word} data-total={word}><b>{nfmt(count)}</b><span><i className={"chart-swatch " + tone} aria-hidden="true" />{label}</span></div>)}
        </div>
        {totals.catalog > 0 && <div data-total="catalog" className="mt-1 mb-5 flex-[1_1_170px] text-[13px] text-muted-foreground sm:text-right"><b className="block text-xl leading-tight font-semibold tabular-nums">{nfmt(totals.catalog)}</b>{CATALOG_APART}</div>}
      </div>
      <DecisionChart key={hour ? "hour" : "day"} buckets={buckets} step={hour ? MINUTE_MS : HOUR_MS} />
      <div className="overview-decision-footer"><Door to="audit" label={OPEN_AUDIT} /></div>
    </div>}
  </Panel>;
}
// Stopped keeps every outcome and its original reason behind a concise evidence action.
export function Stopped({ block, denied = false }: { block: DecisionsBlock | null; denied?: boolean }) {
  const [record, setRecord] = React.useState<StoppedRow | null>(null);
  const trigger = React.useRef<HTMLButtonElement | null>(null);
  const rows = block?.stopped || [];
  React.useEffect(() => { if (!block) setRecord(null); }, [block]);
  const openAudit = (tool: string) => { put("audit", { q: tool }); navigate("audit"); };
  return <>
    <Panel name="stopped" title={STOPPED_TITLE} help={STOPPED_HELP} right={<Door to="audit" />} flush>
      {!rows.length ? <PanelNote>{block ? STOPPED_NONE : denied ? seatLine(STOPPED_TITLE, "config:read") : STOPPED_UNREAD}</PanelNote> : <div className="overflow-x-auto">
        <table className="w-full table-fixed text-sm"><colgroup><col style={{ width: "37%" }} /><col style={{ width: "12%" }} /><col style={{ width: "25%" }} /><col style={{ width: "26%" }} /></colgroup>
          <thead><tr className="border-b border-border"><th className="text-left">{STOPPED_COLUMN.tool}</th><th className="text-right">{STOPPED_COLUMN.times}</th><th className="text-left">{STOPPED_COLUMN.outcome}</th><th className="text-left">Evidence</th></tr></thead>
          <tbody>{rows.map((r) => {
            const tool = toolWords(r.app, r.tool);
            return <tr key={tool + r.outcome} data-stopped={tool} className="border-b border-border last:border-b-0">
              <td className="font-mono text-[13px]">{tool}</td><td className="text-right tabular-nums">{nfmt(r.count)}</td>
              <td><ToneBadge tone={r.outcome === "denied" ? "danger" : "warn"} word={outcomeWord(r.outcome)} /></td>
              <td><Button variant="link" size="sm" className="overview-text-button" aria-label={stoppedRowLabel(tool)} onClick={(e) => { trigger.current = e.currentTarget; setRecord(r); }}>View reason</Button></td>
            </tr>;
          })}</tbody>
        </table>
      </div>}
    </Panel>
    <Sheet open={!!record} onOpenChange={(open) => { if (!open) setRecord(null); }}>
      <SheetContent className="overflow-y-auto" onCloseAutoFocus={(e) => { e.preventDefault(); trigger.current?.focus(); }}>
        <SheetHeader><SheetTitle>Tool decision evidence</SheetTitle><SheetDescription>Most frequent recorded reason for this tool and outcome in the last 24 hours.</SheetDescription></SheetHeader>
        {record && <div className="overview-evidence"><h3 className="font-mono break-words">{toolWords(record.app, record.tool)}</h3><dl><dt>Outcome</dt><dd>{outcomeWord(record.outcome)}</dd><dt>Decisions</dt><dd>{nfmt(record.count)}</dd></dl><pre>{record.reason}</pre><Button variant="outline" onClick={() => openAudit(toolWords(record.app, record.tool))}>Open audit for this tool</Button></div>}
      </SheetContent>
    </Sheet>
  </>;
}
