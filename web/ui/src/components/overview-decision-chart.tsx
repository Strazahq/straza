import * as React from "react";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Door } from "@/components/overview-parts";
import type { DecisionBucket } from "@/lib/api";
import { DECIDED_ALT, HOUR_TITLE, MINUTES_ALT, MINUTE_TITLE, OPEN_AUDIT, PER_BUCKET, SO_FAR, TOTAL_LABEL, UNKNOWN_HOUR, bucketTitle, hourSpan, nfmt } from "@/lib/config-words";

// The two bucket lengths the chart draws, in milliseconds.
export const HOUR_MS = 3600000;
export const MINUTE_MS = 60000;

// decisionHour prints a bucket boundary in UTC, including a midnight
// rollover. offset counts buckets of step milliseconds.
export function decisionHour(iso: string, offset = 0, step = HOUR_MS): string {
  const time = Date.parse(iso);
  return Number.isFinite(time) ? new Date(time + offset * step).toISOString().slice(11, 16) : UNKNOWN_HOUR;
}

// chartCeiling rounds the largest stack up to a readable integer scale.
export function chartCeiling(max: number): number {
  if (max <= 2) return 2;
  const scale = 10 ** Math.floor(Math.log10(max));
  return ([1, 2, 5, 10].find((step) => step * scale >= max) || 10) * scale;
}

const axisNumber = (n: number) => new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 }).format(n);

// DecisionChart shows the actual buckets of length step, the hours of the
// day or the minutes of the last hour, and keeps their exact counts keyboard
// accessible. The last minute is still running, and its counts say so. The
// focused bar and the open sheet are held by their start, so they stay with
// their bucket when a refresh moves the window on.
export function DecisionChart({ buckets, step = HOUR_MS }: { buckets: DecisionBucket[]; step?: number }) {
  const byMinute = step === MINUTE_MS;
  const [hovered, setHovered] = React.useState<number | null>(null);
  const [focused, setFocused] = React.useState<string | null>(null);
  const [tabStop, setTabStop] = React.useState(0);
  const [selected, setSelected] = React.useState<DecisionBucket | null>(null);
  const trigger = React.useRef<HTMLButtonElement | null>(null);
  const tipID = React.useId();
  const focusedAt = focused === null ? -1 : buckets.findIndex((b) => b.start === focused);
  const active = hovered ?? (focusedAt < 0 ? null : focusedAt);
  const point = active === null ? null : buckets[active];
  const shown = selected && (buckets.find((b) => b.start === selected.start) || selected);
  const end = (b: DecisionBucket) => decisionHour(b.start, 1, step);
  const running = (b: DecisionBucket) => byMinute && b.start === buckets[buckets.length - 1]?.start;
  const ceiling = chartCeiling(Math.max(0, ...buckets.map((b) => b.allowed + b.approval + b.denied)));
  const middle = Math.floor(ceiling / 2);
  const middleTop = (1 - middle / ceiling) * 100 + "%";
  const ticks = [...new Set([0, Math.floor(buckets.length / 4), Math.floor(buckets.length / 2), Math.floor(buckets.length * 3 / 4), buckets.length])];

  React.useEffect(() => { if (tabStop >= buckets.length) setTabStop(0); }, [tabStop, buckets.length]);

  const move = (event: React.KeyboardEvent<HTMLButtonElement>, hour: number) => {
    let next: number;
    if (event.key === "ArrowRight") next = (hour + 1) % buckets.length;
    else if (event.key === "ArrowLeft") next = (hour + buckets.length - 1) % buckets.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = buckets.length - 1;
    else if (event.key === "Escape") { setHovered(null); setFocused(null); return; }
    else return;
    event.preventDefault();
    setTabStop(next);
    setHovered(null);
    event.currentTarget.parentElement?.querySelector<HTMLButtonElement>(`[data-bucket="${next}"]`)?.focus();
  };

  return <>
    <div className="overview-chart" data-minutes={byMinute || undefined} role="group" aria-label={byMinute ? MINUTES_ALT : DECIDED_ALT}>
      <div className="chart-caption"><span>{PER_BUCKET[byMinute ? "minute" : "hour"]}</span><span>UTC</span></div>
      <div className="chart-plot">
        <div className="chart-y" aria-hidden="true"><span>{axisNumber(ceiling)}</span><span style={{ top: middleTop }}>{axisNumber(middle)}</span><span>0</span></div>
        <div className="chart-field" onPointerLeave={() => setHovered(null)}>
          <div className="chart-grid" aria-hidden="true"><i /><i style={{ top: middleTop }} /><i /></div>
          <div className="chart-columns" style={{ gridTemplateColumns: `repeat(${buckets.length}, minmax(0, 1fr))` }} onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) setFocused(null); }}>
            {buckets.map((b, i) => <button key={b.start || i} type="button" data-bucket={i} className={active === i ? "is-active" : ""} tabIndex={tabStop === i ? 0 : -1}
              aria-label={bucketTitle(decisionHour(b.start), end(b), b.allowed, b.approval, b.denied, running(b))} aria-describedby={active === i ? tipID : undefined}
              onPointerEnter={(e) => { if (e.pointerType !== "touch") setHovered(i); }} onFocus={() => { setFocused(b.start); setTabStop(i); }} onKeyDown={(e) => move(e, i)}
              onClick={(e) => { trigger.current = e.currentTarget; setHovered(null); setFocused(null); setSelected(b); }}>
              <span className="chart-stack" style={{ height: `${(b.allowed + b.approval + b.denied) / ceiling * 100}%` }} aria-hidden="true">
                {b.denied > 0 && <span className="chart-denied" style={{ flex: b.denied }} />}
                {b.approval > 0 && <span className="chart-approval" style={{ flex: b.approval }} />}
                {b.allowed > 0 && <span className="chart-allowed" style={{ flex: b.allowed }} />}
              </span>
            </button>)}
          </div>
          {point && active !== null && <div id={tipID} role="tooltip" className="chart-tooltip" style={{ left: `clamp(0px, calc(${(active + .5) / buckets.length * 100}% - 98px), calc(100% - 196px))` }}>
            <strong>{decisionHour(point.start)} to {end(point)} <span>UTC{running(point) ? SO_FAR : ""}</span></strong>
            <dl><dt><i className="chart-swatch chart-allowed" />{TOTAL_LABEL.allowed}</dt><dd>{nfmt(point.allowed)}</dd><dt><i className="chart-swatch chart-denied" />{TOTAL_LABEL.denied}</dt><dd>{nfmt(point.denied)}</dd><dt><i className="chart-swatch chart-approval" />{TOTAL_LABEL.approval}</dt><dd>{nfmt(point.approval)}</dd></dl>
          </div>}
        </div>
      </div>
      <div className="chart-x" aria-hidden="true">{ticks.map((i) => <span key={i}>{i === buckets.length ? end(buckets[i - 1]) : decisionHour(buckets[i].start)}</span>)}</div>
    </div>
    <Sheet open={!!selected} onOpenChange={(open) => { if (!open) setSelected(null); }}>
      <SheetContent onCloseAutoFocus={(e) => { e.preventDefault(); trigger.current?.focus(); }}>
        <SheetHeader><SheetTitle>{byMinute ? MINUTE_TITLE : HOUR_TITLE}</SheetTitle><SheetDescription>{shown ? shown.start.slice(0, 10) + " · " + hourSpan(decisionHour(shown.start), end(shown)) + (running(shown) ? SO_FAR : "") : ""}</SheetDescription></SheetHeader>
        {shown && <div className="overview-evidence"><dl><dt>{TOTAL_LABEL.allowed}</dt><dd>{nfmt(shown.allowed)}</dd><dt>{TOTAL_LABEL.denied}</dt><dd>{nfmt(shown.denied)}</dd><dt>{TOTAL_LABEL.approval}</dt><dd>{nfmt(shown.approval)}</dd></dl><Door to="audit" label={OPEN_AUDIT} /></div>}
      </SheetContent>
    </Sheet>
  </>;
}
