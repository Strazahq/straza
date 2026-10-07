import { HelpTip } from "@/components/help-tip";
import { type ChainRead, oldestPending } from "@/components/overview-parts";
import type { ApprovalRow, OverviewAnswer } from "@/lib/api";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import type { RouteKey } from "@/lib/routes";
import {
  CHAIN_INTACT,
  CHAIN_SEAT,
  CHAIN_UNREAD,
  CHAIN_UNVERIFIED,
  CHAIN_VALUE,
  LOADING,
  NOT_READ,
  NO_ANSWER,
  PERMISSION_REQUIRED,
  TILE,
  TILE_UNREAD,
  TILE_WAITING,
  approvalsDetail,
  chainBrokenDetail,
  lastCheckedDot,
  lastReadDot,
  latestRecord,
  offDetail,
  lacksGrant,
  nfmt,
  pushDetail,
  serversDetail,
  tileOf,
  usersDetail,
} from "@/lib/config-words";
import { relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// The six stat tiles: the number, its total in small type, one line of
// detail in words, and a door to the area that holds it. Revoked sessions
// show in the Sessions detail.

type Tone = "ok" | "warn" | "danger" | "";

const TONE: Record<string, string> = { ok: "text-ok", warn: "text-warn", danger: "text-danger" };

type TileProps = {
  to: RouteKey;
  label: string;
  help: string;
  value: string;
  // of is the small word after the number: its total, or what it counts.
  of?: string;
  // dead marks a value that is a word rather than a number, so it is set
  // small and muted.
  dead?: boolean;
  detail: string;
  metadata?: string;
  tone?: Tone;
};

// Tile is one number with its door. The whole card is the link, so the
// number and its words are one target.
function Tile({ to, label, help, value, of, dead, detail, metadata, tone }: TileProps) {
  return (
    <a
      href={pathFor(to)}
      data-tile={to}
      className="flex min-w-0 flex-col gap-1 rounded-md border border-border bg-card px-3.5 py-3 text-foreground no-underline hover:border-link focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
      onClick={(e) => { if (!isPlainClick(e)) return; e.preventDefault(); navigate(to); }}
    >
      <span className="flex items-center gap-1.5 text-[13px] whitespace-nowrap text-muted-foreground">
        {label}
        <HelpTip label={label} text={help} />
      </span>
      <span className={cn("flex flex-wrap items-baseline gap-1.5 leading-tight font-semibold tabular-nums", dead ? "text-base font-normal text-muted-foreground" : "text-[28px]")} data-tile-value>
        {value}
        {of && !dead && <small className="text-sm font-normal text-muted-foreground">{of}</small>}
      </span>
      <span className={cn("text-[13px] leading-snug text-text-2", tone && TONE[tone])} data-tile-detail>{detail}</span>
      {metadata && <span className="text-[13px] text-muted-foreground" data-tile-metadata>{metadata}</span>}
    </a>
  );
}

type Props = {
  // answer is the overview read, or null when it has not answered; every
  // field is optional, so a partial answer blanks no tile.
  answer: OverviewAnswer | null;
  // approvals is the pending list, null when the seat may not read it or
  // the read failed.
  approvals: ApprovalRow[] | null;
  approvalsDenied: boolean;
  chain: ChainRead;
  denied?: boolean;
  loading?: boolean;
  stale?: { summary?: string; approvals?: string; chain?: string };
};

// chainDetail is the Audit tile's line: what this browser proved about the
// newest records, or why it proved nothing.
function chainDetail(chain: ChainRead): { detail: string; tone: Tone } {
  if (chain.word === "intact") return { detail: CHAIN_INTACT, tone: "ok" };
  if (chain.word === "broken") return { detail: chainBrokenDetail(chain.seq), tone: "danger" };
  if (chain.word === "seat") return { detail: CHAIN_SEAT, tone: "" };
  if (chain.word === "unverified") return { detail: CHAIN_UNVERIFIED, tone: "" };
  return { detail: "", tone: "" };
}

// Tiles renders the row of six. A count with no answer behind it reads as
// the word, never as zero.
export function Tiles({ answer, approvals, approvalsDenied, chain, denied = false, loading = false, stale = {} }: Props) {
  const a = answer || {};
  const apps = a.apps || {};
  const users = a.users || {};
  const sessions = a.sessions || {};
  const policies = a.policies || {};
  const push = a.push || {};
  const waiting = approvals ? approvals.length : 0;
  const oldest = approvals ? oldestPending(approvals) : null;
  const chainLine = chainDetail(chain);
  const missing = denied ? NOT_READ : loading ? LOADING : NO_ANSWER;
  const count = (n: number | undefined) => n === undefined ? missing : nfmt(n);
  const total = (n: number | undefined) => n === undefined ? undefined : tileOf(n);
  const summaryDetail = (line: string) => stale.summary ? lastReadDot(stale.summary) + line : denied ? PERMISSION_REQUIRED : line;
  const appsTone: Tone = apps.failed ? "danger" : apps.degraded || apps.pending ? "warn" : "";

  return (
    <div className="overview-tiles" data-tiles>
      <Tile
        to="sessions"
        label={TILE.sessions.label}
        help={TILE.sessions.help}
        value={count(sessions.active)}
        of={total(sessions.total)}
        dead={sessions.active === undefined}
        detail={summaryDetail(push.lane_up === undefined || push.connected === undefined ? TILE_UNREAD : pushDetail(push.lane_up, push.connected, a.denylist?.entries || 0))}
        tone={push.lane_up === false ? "warn" : ""}
      />
      <Tile
        to="users"
        label={TILE.users.label}
        help={TILE.users.help}
        value={count(users.active)}
        of={total(users.total)}
        dead={users.active === undefined}
        detail={summaryDetail(users.total === undefined || users.active === undefined ? TILE_UNREAD : usersDetail(users.total, users.active))}
      />
      <Tile
        to="servers"
        label={TILE.servers.label}
        help={TILE.servers.help}
        value={count(apps.running)}
        of={total(apps.total)}
        dead={apps.running === undefined}
        detail={summaryDetail(serversDetail(a.apps))}
        tone={appsTone}
      />
      <Tile
        to="policies"
        label={TILE.policies.label}
        help={TILE.policies.help}
        value={count(policies.active)}
        of={total(policies.total)}
        dead={policies.active === undefined}
        detail={summaryDetail(policies.total === undefined || policies.active === undefined ? TILE_UNREAD : offDetail(policies.total, policies.active))}
      />
      {approvalsDenied || !approvals ? (
        <Tile
          to="approvals"
          label={TILE.approvals.label}
          help={TILE.approvals.help}
          value={approvalsDenied ? NOT_READ : NO_ANSWER}
          dead
          detail={approvalsDenied ? lacksGrant("approvals:read") : ""}
        />
      ) : (
        <Tile
          to="approvals"
          label={TILE.approvals.label}
          help={TILE.approvals.help}
          value={nfmt(waiting)}
          of={TILE_WAITING}
          detail={(stale.approvals ? lastReadDot(stale.approvals) : "") + approvalsDetail(waiting, oldest ? relTimeText(oldest.createdAt) : "")}
          tone={waiting ? "warn" : ""}
        />
      )}
      <Tile
        to="audit"
        label={TILE.audit.label}
        help={TILE.audit.help}
        value={chain.word === "intact" ? CHAIN_VALUE.intact : chain.word === "broken" ? CHAIN_VALUE.broken : chain.word === "seat" ? CHAIN_VALUE.seat : CHAIN_VALUE.other}
        dead
        detail={(stale.chain ? lastCheckedDot(stale.chain) : "") + (chainLine.detail || CHAIN_UNREAD)}
        metadata={a.audit?.head_seq === undefined ? undefined : latestRecord(a.audit.head_seq)}
        tone={chainLine.tone}
      />
    </div>
  );
}
