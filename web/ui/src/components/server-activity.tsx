import { EVENTS_NOT_READABLE } from "@/lib/server-words";
import * as React from "react";
import { ScrollTextIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { FetchError } from "@/components/error-state";
import { type ApiError, type AppRow, type LogsAnswer, appLogs, listAudit, listChanges } from "@/lib/api";
import { type EventRow, type LogRow, absTime, driftRow, eventRow, foldRuns, probeWords, relTimeText, removedAt } from "@/lib/words";
import { cn } from "@/lib/utils";

const LOG_POLL_MS = 5000;
const EVENTS_POLL_MS = 15000;
const EVENTS_MAX = 30;

const CAPS = "text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";
const HINT = "mt-1.5 text-[13px] leading-snug text-muted-foreground";
const BOX = "rounded-md border border-border bg-card";

// clockTime is the wall clock part of a stamp in the active zone.
const clockTime = (iso: string) => absTime(iso).slice(11, 19);

// usePoll runs fn now and every ms after that while not paused, and drops
// answers that land after the caller unmounted or changed key. A pause
// keeps the last answer; the first read still happens while paused.
function usePoll<T>(fn: () => Promise<T>, ms: number, key: string, paused = false) {
  const [state, setState] = React.useState<{ data: T | null; error: ApiError | null }>({ data: null, error: null });
  const fnRef = React.useRef(fn);
  fnRef.current = fn;
  const has = React.useRef(false);
  React.useEffect(() => { has.current = false; }, [key]);
  React.useEffect(() => {
    let alive = true;
    const run = () => fnRef.current().then(
      (data) => { if (alive) { has.current = true; setState({ data, error: null }); } },
      (e) => { if (alive) setState((s) => ({ data: s.data, error: e as ApiError })); },
    );
    if (!paused || !has.current) void run();
    if (paused) return () => { alive = false; };
    const t = setInterval(run, ms);
    return () => { alive = false; clearInterval(t); };
  }, [key, ms, paused]);
  return state;
}

// readDetail words a failed read for the FetchError block.
function readDetail(what: string, err: ApiError): string {
  return err.unreachable
    ? "The " + what + " could not be read because strazad did not answer. Check that it is running, then try again."
    : "The " + what + " could not be read: " + err.message + ".";
}

const DOT: Record<EventRow["tone"], string> = { accent: "bg-link", warn: "bg-warn", danger: "bg-danger" };

// Events is Straza's own record of the server: admin actions, refused
// calls and holds from the audit chain, tool list drift from the change
// feed, newest first.
function Events({ app }: { app: AppRow }) {
  const { data, error } = usePoll(async () => {
    const [rows, feed] = await Promise.all([
      listAudit("q=" + encodeURIComponent(app.name) + "&order=desc&limit=100"),
      listChanges("tool", 1000),
    ]);
    // A server reinstalled under a removed server's name starts after the
    // removal, so the removed server's records stay off this page.
    const cut = removedAt(rows || [], app.name);
    const out = (rows || []).map((r) => eventRow(r, app.name)).filter((e): e is EventRow => e !== null)
      .concat(((feed && feed.changes) || []).map((c) => driftRow(c, app.id)).filter((e): e is EventRow => e !== null))
      .filter((e) => !cut || Date.parse(e.t) > cut);
    out.sort((a, b) => Date.parse(b.t) - Date.parse(a.t));
    return out.slice(0, EVENTS_MAX);
  }, EVENTS_POLL_MS, app.id + "/" + app.name);
  return (
    <section>
      <div className={cn(CAPS, "mb-1.5")}>events</div>
      <div className={BOX} data-server-events>
        {error && error.status === 403 && <p className="px-3 py-3 text-[13px] text-muted-foreground" data-events-closed>{EVENTS_NOT_READABLE}</p>}
        {error && error.status !== 403 && <FetchError subject="Events" detail={readDetail("events", error)} />}
        {data && data.length === 0 && (
          <p className="px-3 py-3 text-[13px] text-muted-foreground">Nothing recorded about this server yet. Admin actions, tool list changes, denied calls and holds land here.</p>
        )}
        {data && data.length > 0 && (
          <div className="grid grid-cols-[auto_12px_1fr] gap-x-3 text-sm">
            {data.map((e, i) => {
              const cell = cn("py-1.5", i < data.length - 1 && "border-b border-border");
              // The words module hands the actor and the rest apart so the
              // actor can be bold; an admin sentence needs the space between.
              const gap = /^['\s]/.test(e.text) ? "" : " ";
              return (
                <React.Fragment key={e.seq}>
                  <div className={cn(cell, "pl-3 font-mono text-[13px] whitespace-nowrap text-muted-foreground")}>{e.t ? clockTime(e.t) : ""}</div>
                  <div className={cn(cell, "relative")}><span className={cn("absolute top-3 left-px size-[9px] rounded-full", DOT[e.tone])} aria-hidden="true" /></div>
                  <div className={cn(cell, "pr-3 text-text-2")}>
                    <span className="font-semibold text-foreground">{e.who}</span>{gap}{e.text}{" "}
                    <span className="ml-1.5 text-[13px] text-muted-foreground">{e.src}</span>
                  </div>
                </React.Fragment>
              );
            })}
          </div>
        )}
      </div>
      <p className={HINT}>Newest first, from the audit chain and the change feed. Denied calls and holds name the session; allowed calls are on the Audit page.</p>
    </section>
  );
}

type Sev = "all" | "err" | "wrn";
type Span = "since" | "all";

const KDOT: Record<string, string> = { err: "bg-danger", wrn: "bg-warn", dbg: "bg-border", inf: "bg-transparent" };
const cnt = (n: number, word: string) => n + " " + word + (n === 1 ? "" : "s");

function Pill({ on, onClick, children, label }: { on: boolean; onClick: () => void; children: React.ReactNode; label?: string }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      aria-label={label}
      onClick={onClick}
      className={cn("h-6 rounded-full border px-2.5 text-[13px] transition-colors", on ? "border-link bg-accent-bg text-link" : "border-border bg-card text-text-2 hover:text-foreground")}
    >
      {children}
    </button>
  );
}

// logRows folds the entries, inserts the health check marker at the first
// line received after it (or at the end), and cuts to the span.
function logRows(entries: { t: string; line: string }[], app: AppRow, span: Span): LogRow[] {
  const checkAt = app.last_probe_at ? Date.parse(app.last_probe_at) : NaN;
  let rows = foldRuns(entries);
  if (!Number.isNaN(checkAt)) {
    const words = app.detail && app.status !== "running" ? ", " + probeWords(app.detail, app.url) : "";
    const mark: LogRow = { marker: true, check: true, t: app.last_probe_at as string, text: "health check · " + (app.status || "") + words };
    const at = rows.findIndex((r) => r.t && Date.parse(r.t) >= checkAt);
    if (at < 0) rows.push(mark); else rows.splice(at, 0, mark);
    if (span === "since") rows = rows.slice(rows.findIndex((r) => r.marker && r.check));
  }
  return rows;
}

// Log is the runtime's stderr ring made readable: a receive time per line,
// repeated lines folded with a count, a marker where the last health check
// ran, a tally with a severity and a span filter, Follow and Copy.
function Log({ app }: { app: AppRow }) {
  const [sev, setSev] = React.useState<Sev>("all");
  const [span, setSpan] = React.useState<Span>("since");
  const [follow, setFollow] = React.useState(true);
  const [copied, setCopied] = React.useState(false);
  const copiedTimer = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  React.useEffect(() => () => { if (copiedTimer.current) clearTimeout(copiedTimer.current); }, []);
  const remote = app.runtime === "remote";
  const { data, error } = usePoll<LogsAnswer | null>(() => (remote ? Promise.resolve(null) : appLogs(app.id)), LOG_POLL_MS, app.id + "/" + app.runtime, !follow);

  const head = <div className={cn(CAPS, "mb-1.5")}>log</div>;
  if (remote) {
    return (
      <section>{head}
        <div className={cn(BOX, "px-4 py-3 text-sm text-text-2")} data-server-log="remote">
          {"A remote server keeps its own log. Straza shows its health checks here" + (app.last_probe_at ? ": the last one answered " + relTimeText(app.last_probe_at) : "") + "."}
        </div>
      </section>
    );
  }
  if (error && error.status === 409) {
    return (
      <section>{head}
        <div className={cn(BOX, "px-4 py-3 text-sm text-text-2")} data-server-log="stopped">
          The runtime is stopped, so there is no output. <b className="font-semibold text-foreground">Enable</b> it to see the log.
        </div>
      </section>
    );
  }
  const problem = error ? <FetchError subject="Log" detail={readDetail("log", error)} /> : null;
  if (!data) return <section>{head}{problem || <p className="px-3 py-3 text-[13px] text-muted-foreground">Reading the log.</p>}</section>;

  const entries = data.entries || (data.lines || []).map((line) => ({ t: "", line }));
  if (entries.length === 0) {
    return <section>{head}{problem}<div className={BOX}><EmptyState icon={ScrollTextIcon} title="No log output">The runtime has not written anything yet.</EmptyState></div></section>;
  }
  const rows = logRows(entries, app, span);
  const tally = { err: 0, wrn: 0, lines: 0, lastErr: "" };
  for (const r of rows) {
    if (r.marker) continue;
    tally.lines += r.n;
    if (r.k === "err") { tally.err += r.n; tally.lastErr = r.last; }
    if (r.k === "wrn") tally.wrn += r.n;
  }
  const shown = rows.filter((r) => r.marker || sev === "all" || r.k === sev);
  const hidden = rows.length - shown.length;
  const lines = shown.filter((r) => !r.marker);
  const copy = () => {
    const text = lines.map((r) => (r.marker ? "" : (r.t ? clockTime(r.t) + "  " : "") + r.text + (r.n > 1 ? "  ×" + r.n : ""))).join("\n");
    try { void navigator.clipboard?.writeText(text); } catch { /* denied: the text is on screen either way */ }
    setCopied(true);
    if (copiedTimer.current) clearTimeout(copiedTimer.current);
    copiedTimer.current = setTimeout(() => setCopied(false), 1100);
  };
  const TALLY = "flex flex-wrap items-center gap-2 px-3 py-2 text-[13px] text-text-2";
  return (
    <section>
      {head}
      {problem && <div className="mb-2">{problem}</div>}
      <div className={BOX} data-server-log="ring">
        <div className={cn(TALLY, "border-b border-border")} data-log-tally>
          <span><span className="mr-1 inline-block size-2 rounded-full bg-danger" aria-hidden="true" />{cnt(tally.err, "error")}</span>
          <span><span className="mr-1 inline-block size-2 rounded-full bg-warn" aria-hidden="true" />{cnt(tally.wrn, "warning")}</span>
          <span>{cnt(tally.lines, "line") + (span === "since" ? " since the last check" : " in the ring")}</span>
          {tally.lastErr && <span>{"· last error " + relTimeText(tally.lastErr)}</span>}
          <span className="ml-auto inline-flex flex-wrap items-center gap-1">
            <Pill on={sev === "err"} onClick={() => setSev("err")}>Errors</Pill>
            <Pill on={sev === "wrn"} onClick={() => setSev("wrn")}>Warnings</Pill>
            <Pill on={sev === "all"} onClick={() => setSev("all")}>All</Pill>
            <span className="mx-1 text-border" aria-hidden="true">·</span>
            <Pill on={span === "since"} onClick={() => setSpan("since")}>Since last check</Pill>
            <Pill on={span === "all"} onClick={() => setSpan("all")}>Everything</Pill>
          </span>
        </div>
        <div className="max-h-[340px] overflow-auto font-mono text-[13px] leading-relaxed" data-log-lines>
          {shown.map((r, i) => r.marker ? (
            <div key={"m" + i} className={cn("flex items-center gap-2 px-3 py-1 font-sans text-[13px]", r.check ? "text-link" : "text-warn")} data-log-marker>
              <span className="flex-1 border-t border-dashed border-border" aria-hidden="true" />
              {(r.t ? clockTime(r.t) + " · " : "") + r.text}
              <span className="flex-1 border-t border-dashed border-border" aria-hidden="true" />
            </div>
          ) : (
            <div key={i} className="grid grid-cols-[62px_14px_1fr_auto] items-start gap-x-2 py-px pr-3 pl-2" data-log-line={r.k}>
              <span className="text-muted-foreground tabular-nums">{r.t ? clockTime(r.t) : ""}</span>
              <span className="relative h-[1.6em]"><span className={cn("absolute top-[calc(50%-4px)] left-0.5 size-[9px] rounded-full", KDOT[r.k])} aria-hidden="true" /></span>
              <span className={cn("whitespace-pre-wrap [overflow-wrap:anywhere]", r.k === "dbg" ? "text-muted-foreground" : "text-foreground")}>{r.text}</span>
              {r.n > 1 ? <span className="rounded-full border border-border px-1.5 font-sans text-xs whitespace-nowrap text-text-2" title={"repeated until " + absTime(r.last)}>{"×" + r.n}</span> : <span />}
            </div>
          ))}
        </div>
        {hidden > 0 && (
          <div className="border-t border-dashed border-border px-3 py-1 text-[13px] text-muted-foreground" data-log-hidden>
            {cnt(hidden, "other line") + " hidden by the filter. "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => setSev("all")}>Show all</Button>
          </div>
        )}
        <div className={cn(TALLY, "border-t border-border")}>
          <span className="inline-flex items-center gap-1.5">Follow <Pill on={follow} onClick={() => setFollow(!follow)} label="follow the log">{follow ? "on, every 5 s while open" : "off"}</Pill></span>
          <Button variant="ghost" size="sm" className={cn("ml-auto", copied && "text-ok")} onClick={copy}>{copied ? "Copied" : "Copy " + cnt(lines.length, "line")}</Button>
        </div>
      </div>
      <p className={HINT}>The last lines the runtime wrote to stderr, each stamped when Straza received it. A repeated line shows once with its count. The dashed rows mark the last health check and every status change.</p>
    </section>
  );
}

// ServerActivity is the Activity tab of a server's page: Events, then Log.
// Both poll only while mounted, so a tab change or leaving the page stops
// the reads.
export function ServerActivity({ app }: { app: AppRow }) {
  return (
    <div className="flex flex-col gap-5" data-server-activity>
      <Events app={app} />
      <Log app={app} />
    </div>
  );
}
