import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { DataTable, plain } from "@/components/data-table";
import { FetchError, RefusedError } from "@/components/error-state";
import { Section } from "@/components/sheet-parts";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type ChannelRow, type ChannelTestReport, listChannels, testChannel } from "@/lib/api";
import {
  CHANNELS_FOOT,
  CHANNEL_COLUMN,
  DELIVERED,
  DELIVERY_FAILED,
  DELIVERY_OK,
  NEVER,
  NOTHING_TO_DELIVER,
  NOT_CONFIGURED,
  NO_TARGETS,
  NO_TARGETS_PUSH,
  REACHES_CONSOLE,
  REACHES_NOBODY,
  REACHES_SLACK,
  READING_CHANNELS,
  SEE_WHICH,
  SENDING,
  SEND_TEST,
  SUBJECT_CHANNELS,
  TEST_VERB,
  channelStatus,
  channelsCount,
  reachesPush,
  targetsCount,
  testName,
  testTitle,
} from "@/lib/approval-words";
import { readFailed, refused } from "@/lib/say";
import { absTime, relTimeText } from "@/lib/words";

// ChannelsTab is the channels tab: the console,
// Slack and push rows the server answers, each with its status word, the
// server's own detail sentence, what it reaches, the last delivery attempt
// and Send a test where there is something to send. The rows are not doors:
// a channel has no sheet, because the row already carries everything the
// server keeps, and the test result lands under the table.

const SMALL = "text-[13px] leading-snug text-muted-foreground";
const DETAIL = "text-[13px] leading-snug text-text-2";
const POLL_MS = 15000;
const CONSOLE = "console";
const PUSH = "push";
// The widths leave the Detail column the rest of a 1280 px page, which
// would otherwise be a narrow tower of wrapped words.
const WIDTHS: Record<string, string> = { channel: "96px", status: "118px", reaches: "190px", last: "150px", act: "118px" };
const LABELS: Record<string, string> = {
  channel: CHANNEL_COLUMN.channel,
  status: CHANNEL_COLUMN.status,
  detail: CHANNEL_COLUMN.detail,
  reaches: CHANNEL_COLUMN.reaches,
  last: CHANNEL_COLUMN.last,
};

export type ChannelsProps = {
  // onSeeDevices opens the devices tab, from the push row's "See which".
  onSeeDevices: () => void;
};

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: ChannelRow[]; lastRead: Date; problem: string | null };

// Result is what the last test said: the server's per-target report, or its
// refusal in the refused voice. Both belong under the table, since a test is
// read after it ran and a toast would take the sentence away.
type Result =
  | { kind: "report"; name: string; targets: ChannelTestReport["targets"] }
  | { kind: "refused"; name: string; message: string };

export function ChannelsTab({ onSeeDevices }: ChannelsProps) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [result, setResult] = React.useState<Result | null>(null);
  const [testing, setTesting] = React.useState<string | null>(null);

  const load = React.useCallback(() => {
    listChannels().then(
      (answer) => setState({ kind: "ready", rows: (answer && answer.channels) || [], lastRead: new Date(), problem: null }),
      (e: ApiError) => {
        if (e.status === 401) return;
        const message = readFailed(SUBJECT_CHANNELS, e);
        // A failed read keeps the last table on screen behind the sentence.
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, []);

  // The list polls while the tab is looked at, because a delivery happens on
  // the agents' time and the last delivery column is the reason to watch it.
  React.useEffect(() => {
    load();
    const t = setInterval(() => { if (document.visibilityState !== "hidden") load(); }, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  const sendTest = React.useCallback(async (name: string) => {
    setTesting(name);
    setResult(null);
    try {
      const report = await testChannel(name);
      setResult({ kind: "report", name, targets: (report && report.targets) || [] });
      // A test send is a delivery, so the list is read again and the last
      // delivery column moves.
      load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setResult({ kind: "refused", name, message: refused(err) });
    } finally {
      setTesting(null);
    }
  }, [load]);

  const columns = React.useMemo<ColumnDef<ChannelRow>[]>(() => [
    {
      id: "channel",
      header: plain<ChannelRow>(CHANNEL_COLUMN.channel),
      cell: ({ row }) => <b className="font-semibold text-foreground">{row.original.name}</b>,
    },
    {
      id: "status",
      header: plain<ChannelRow>(CHANNEL_COLUMN.status),
      cell: ({ row }) => {
        const word = channelStatus(row.original);
        return <WordBadge word={word} tone={word === NOT_CONFIGURED ? "plain" : "ok"} attr="data-channel-status" />;
      },
    },
    {
      id: "detail",
      header: plain<ChannelRow>(CHANNEL_COLUMN.detail),
      // The detail is the server's own sentence about this channel, so it is
      // rendered as it came and given the room to wrap.
      cell: ({ row }) => <span className={"block whitespace-normal break-words " + DETAIL}>{row.original.detail}</span>,
    },
    {
      id: "reaches",
      header: plain<ChannelRow>(CHANNEL_COLUMN.reaches),
      cell: ({ row }) => <Reaches channel={row.original} onSeeDevices={onSeeDevices} />,
    },
    {
      id: "last",
      header: plain<ChannelRow>(CHANNEL_COLUMN.last),
      cell: ({ row }) => <LastDelivery channel={row.original} />,
    },
    {
      id: "act",
      header: plain<ChannelRow>(""),
      enableHiding: false,
      cell: ({ row }) => {
        const channel = row.original;
        // The console is read, not delivered to, and an unconfigured channel
        // has nothing to send through, so neither carries the button.
        if (channel.name === CONSOLE || !channel.configured) return null;
        const busy = testing === channel.name;
        return (
          <span className="flex justify-end">
            <Button
              variant="outline"
              size="sm"
              aria-label={testName(channel.name)}
              aria-busy={busy || undefined}
              disabled={testing !== null}
              onClick={() => void sendTest(channel.name)}
            >
              {busy && <Loader2Icon className="animate-spin" />} {busy ? SENDING : SEND_TEST}
            </Button>
          </span>
        );
      },
    },
  ], [onSeeDevices, sendTest, testing]);

  return (
    <div className="flex flex-col gap-4">
      {state.kind === "loading" && <p className="text-sm text-muted-foreground">{READING_CHANNELS}</p>}
      {state.kind === "error" && <FetchError subject={SUBJECT_CHANNELS} detail={state.message} />}
      {state.kind === "ready" && (
        <>
          {state.problem && <FetchError subject={SUBJECT_CHANNELS} detail={state.problem} lastRead={state.lastRead} />}

          <DataTable
            rows={state.rows}
            columns={columns}
            labels={LABELS}
            rowKey={(c) => c.name}
            rowName={(c) => c.name}
            dataAttr="data-channel"
            count={(shown) => channelsCount(shown)}
            emptyText=""
            widths={WIDTHS}
          />

          {result && <TestResult result={result} />}

          <p className={SMALL + " max-w-[90ch]"} data-channels-foot>{CHANNELS_FOOT}</p>
        </>
      )}
    </div>
  );
}

// Reaches says who a channel gets a request to. Each row has its own
// sentence: the console is read by whoever opens this area, Slack posts to
// one place, and push reaches the devices that registered a route.
function Reaches({ channel, onSeeDevices }: { channel: ChannelRow; onSeeDevices: () => void }) {
  if (channel.name === CONSOLE) return <span className="whitespace-normal text-text-2">{REACHES_CONSOLE}</span>;
  if (channel.name === PUSH) {
    return (
      <span className="flex flex-wrap items-baseline gap-1.5 whitespace-normal text-text-2">
        {reachesPush(channel.devices || 0, channel.registrations || 0)}
        <button type="button" className="text-link underline-offset-4 hover:underline" onClick={onSeeDevices}>{SEE_WHICH}</button>
      </span>
    );
  }
  if (!channel.configured) return <span className="text-muted-foreground">{REACHES_NOBODY}</span>;
  return <span className="whitespace-normal text-text-2">{REACHES_SLACK}</span>;
}

// LastDelivery reads the last attempt on the channel: the word, how long ago
// with the absolute stamp on hover, and the server's short note under it.
// The console delivers nothing, so it says so rather than reading "never",
// which would look like a channel that has not worked yet.
function LastDelivery({ channel }: { channel: ChannelRow }) {
  if (channel.name === CONSOLE) return <span className="text-muted-foreground">{NOTHING_TO_DELIVER}</span>;
  const last = channel.last_delivery;
  if (!last) return <span className="text-muted-foreground">{NEVER}</span>;
  return (
    <span className="flex flex-col items-start gap-0.5">
      <span className="flex items-center gap-1.5">
        <WordBadge word={last.ok ? DELIVERY_OK : DELIVERY_FAILED} tone={last.ok ? "ok" : "danger"} />
        <Tooltip>
          <TooltipTrigger asChild>
            <span className="cursor-default text-text-2">{relTimeText(last.at)}</span>
          </TooltipTrigger>
          <TooltipContent>{absTime(last.at)}</TooltipContent>
        </Tooltip>
      </span>
      {last.note && <span className={SMALL + " whitespace-normal break-words"}>{last.note}</span>}
    </span>
  );
}

// TestResult is what the last test said, under the table: one line per
// target in the words the server answered, or its refusal in the refused
// voice. A channel with no target is the answer that matters most, since it
// means a request announced there reaches nobody.
function TestResult({ result }: { result: Result }) {
  if (result.kind === "refused") {
    return (
      <div data-channel-test={result.name}>
        <RefusedError subject={TEST_VERB} message={result.message} />
      </div>
    );
  }
  const targets = result.targets;
  return (
    <div className="rounded-md border border-border bg-card px-4 py-3" data-channel-test={result.name}>
      <Section title={testTitle(result.name)}>
        {targets.length === 0 ? (
          <p className="max-w-[75ch] text-sm text-text-2">{result.name === PUSH ? NO_TARGETS_PUSH : NO_TARGETS}</p>
        ) : (
          <>
            <p className="text-sm text-text-2">{targetsCount(targets.length)}</p>
            <ul className="flex flex-col gap-1.5">
              {targets.map((t) => (
                <li key={t.target} className="flex flex-wrap items-baseline gap-1.5">
                  <WordBadge word={t.ok ? DELIVERED : DELIVERY_FAILED} tone={t.ok ? "ok" : "danger"} />
                  <span className="font-mono text-[13px] text-text-2">{t.target}</span>
                  {t.error && <span className="text-[13px] text-danger">{t.error}</span>}
                </li>
              ))}
            </ul>
          </>
        )}
      </Section>
    </div>
  );
}
