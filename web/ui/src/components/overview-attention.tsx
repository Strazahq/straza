import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { type ChainRead, Door, oldestPending, Panel } from "@/components/overview-parts";
import { type ApiError, type AppRow, type ApprovalRow, type OverviewAnswer, type SinkRow, replaySink } from "@/lib/api";
import {
  ATTENTION_HELP,
  ATTENTION_TITLE,
  COUNT_WORD,
  type ConfigRow,
  LANE_DOWN,
  NOTHING_TO_DO,
  PARTIAL_CHECKS,
  VIEW_CHECKS,
  chainBrokenLine,
  degradedLine,
  draftsLine,
  failedLine,
  relaxedLine,
  replayWord,
  replayedWords,
  sinkLine,
  waitingLine,
} from "@/lib/config-words";
import { notify } from "@/lib/notify";
import type { RouteKey } from "@/lib/routes";
import { refused } from "@/lib/say";
import { absTime, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// Needs attention: only what a person should
// act on, each line a sentence with the door to the area where the action
// is. A read this seat may not make drops its line rather than failing the
// page, and a sink holding parked events carries Replay at the row end.

const DOT: Record<string, string> = { danger: "bg-danger", warn: "bg-warn", ok: "bg-ok" };

// clockOf is the wall clock of a stamp in the active zone, the reading a
// sink's "since" clause takes.
const clockOf = (iso: string | undefined) => (iso ? absTime(iso).slice(11, 16) : "");

type Item = { key: string; tone: "warn" | "danger"; text: string; to?: RouteKey; sink?: string };

// Waiting is the drafts that wait for review, from one page of the list:
// how many, whether the page was full, and the oldest one's proposer and
// when it was drafted.
export type Waiting = { count: number; more: boolean; who: string; at: string };

type Props = {
  chain: ChainRead;
  // apps is the server list, null when it did not answer; the count alone
  // cannot name which server failed, so a line needs this read.
  apps: AppRow[] | null;
  approvals: ApprovalRow[] | null;
  // drafts is null when the list did not answer or this seat may not read
  // it, and its line is dropped then.
  drafts?: Waiting | null;
  push: OverviewAnswer["push"];
  sinks: SinkRow[] | null;
  relaxed: ConfigRow[];
  incomplete?: boolean;
  compact?: boolean;
  onDetails?: () => void;
};

// attentionItems is the list in the order a person should read it: the
// broken chain first, then servers, waiting calls, waiting drafts, the push
// lane, parked events, and the relaxed settings last.
export function attentionItems({ chain, apps, approvals, drafts, push, sinks, relaxed }: Props): Item[] {
  const items: Item[] = [];
  if (chain.word === "broken") items.push({ key: "chain", tone: "danger", text: chainBrokenLine(chain.seq), to: "audit" });
  const failed = (apps || []).filter((a) => a.status === "failed").map((a) => a.name);
  if (failed.length) items.push({ key: "failed", tone: "danger", text: failedLine(failed.length, failed), to: "servers" });
  const degraded = (apps || []).filter((a) => a.status === "degraded").map((a) => a.name);
  if (degraded.length) items.push({ key: "degraded", tone: "warn", text: degradedLine(degraded.length, degraded), to: "servers" });
  if (approvals && approvals.length) {
    const oldest = oldestPending(approvals);
    items.push({ key: "waiting", tone: "warn", text: waitingLine(approvals.length, oldest || undefined, oldest ? relTimeText(oldest.createdAt) : ""), to: "approvals" });
  }
  if (drafts && drafts.count > 0) items.push({ key: "drafts", tone: "warn", text: draftsLine(drafts.count, drafts.more, drafts.who, relTimeText(drafts.at)), to: "drafts" });
  if (push && push.lane_up === false) items.push({ key: "lane", tone: "warn", text: LANE_DOWN, to: "sessions" });
  for (const s of (sinks || []).filter((s) => s.parked > 0)) {
    const stream = (s.streams || []).find((x) => x.last_error);
    items.push({ key: "sink:" + s.name, tone: "warn", text: sinkLine(s, clockOf(stream && stream.last_error_at)), sink: s.name });
  }
  if (relaxed.length) items.push({ key: "relaxed", tone: "warn", text: relaxedLine(relaxed), to: "settings" });
  return items;
}

// Replay re-feeds one sink's parked events. The server's own sentence
// lands in the row when it refuses, and its answer lands in a toast when
// it takes.
function Replay({ name }: { name: string }) {
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setProblem(null);
    try {
      const answer = (await replaySink(name)) as { replayed?: number; remaining?: number } | null;
      notify.ok(replayedWords(name, (answer && answer.replayed) || 0, (answer && answer.remaining) || 0));
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) {
        setProblem(refused(err));
        notify.failed(refused(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Button variant="outline" size="sm" className="ml-auto" disabled={busy} onClick={() => void run()} data-replay={name}>
        {busy && <Loader2Icon className="animate-spin" />}
        {replayWord(name)}
      </Button>
      {problem && <p className="w-full text-[13px] leading-relaxed text-danger" data-replay-refused={name}>{problem}</p>}
    </>
  );
}

// Attention is the list that opens the page, or the one line that says
// nothing needs doing.
export function Attention(props: Props) {
  const items = attentionItems(props);
  const incomplete = props.incomplete || !props.apps || !props.approvals || props.drafts === null || !props.sinks || props.push?.lane_up === undefined || ["unknown", "seat", "unverified"].includes(props.chain.word);
  if (props.compact && items.length === 0) return <section data-panel="attention" aria-label={ATTENTION_TITLE}>
    <span className="sr-only" data-attention-count>{incomplete ? COUNT_WORD.partial : COUNT_WORD.none}</span>
    <div className="flex items-start gap-2.5 text-sm text-text-2" data-attention-none><span className={cn("mt-1.5 size-2 shrink-0 rounded-full", incomplete ? "bg-unknown" : DOT.ok)} aria-hidden="true" /><span>{incomplete ? PARTIAL_CHECKS : NOTHING_TO_DO}</span></div>
    {props.onDetails && <Button variant="link" size="sm" className="overview-text-button mt-2 ml-4" onClick={props.onDetails}>{VIEW_CHECKS}</Button>}
  </section>;
  return (
    <Panel
      name="attention"
      title={ATTENTION_TITLE}
      help={ATTENTION_HELP}
      lead={<span className="font-mono text-[12px] font-normal text-muted-foreground" data-attention-count>{items.length || (incomplete ? COUNT_WORD.partial : COUNT_WORD.none)}</span>}
      flush
    >
      {items.length === 0 ? (
        <div className="flex items-center gap-2.5 px-3.5 py-2.5 text-sm text-text-2" data-attention-none>
          <span className={cn("size-2 shrink-0 rounded-full", incomplete ? "bg-unknown" : DOT.ok)} aria-hidden="true" />
          <span>{incomplete ? PARTIAL_CHECKS : NOTHING_TO_DO}</span>
        </div>
      ) : (
        items.map((i) => (
          <div key={i.key} data-attention={i.key} className="flex flex-wrap items-center gap-2.5 border-b border-border px-3.5 py-2.5 text-sm text-foreground last:border-b-0">
            <span className={cn("size-2 shrink-0 rounded-full", DOT[i.tone])} aria-hidden="true" />
            <span className="min-w-0 flex-1">{i.text}</span>
            {i.sink ? <Replay name={i.sink} /> : i.to ? <Door to={i.to} className="ml-auto" /> : null}
          </div>
        ))
      )}
    </Panel>
  );
}
