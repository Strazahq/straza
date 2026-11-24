import type { ColumnDef } from "@tanstack/react-table";
import { HourglassIcon, ShieldAlertIcon, ShieldCheckIcon } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { plain, sortable } from "@/components/data-table";
import { Chip, Pill } from "@/components/chip";
import { KIND_ICON } from "@/components/kind-icon";
import { SessionStatus } from "@/components/session-status";
import type { SessionRow } from "@/lib/api";
import { HOOK_WORD } from "@/lib/config-words";
import {
  ATT_TITLE, type Health, SESSION_LABELS, THIS_CONSOLE, WAITING_COLUMN, WHERE_COLUMN, WIRING_TITLE, YOUR_BROWSER, agents, attTone, cutID, healthOf, people, services, sessionHarness, shortHash, userKindOf, waitingWords, wiringTone,
} from "@/lib/session-words";
import { NONE, absTime, agoWord, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// The columns, the cells and the headline pills of the Sessions list.

// Stamp is a time read by read, with the absolute stamp on hover, the way
// every table in the console reads a time.
export function Stamp({ iso, read }: { iso: string; read: (iso: string) => string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default text-text-2">{read(iso)}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}

const HEALTH_ICON = { ok: ShieldCheckIcon, warn: ShieldAlertIcon, danger: ShieldAlertIcon };
const HEALTH_TONE = { ok: "text-ok", warn: "text-warn", danger: "text-danger" };

// HealthCell is the Health column: the icon in the level's colour and the
// word, both sentences on hover. A long word wraps onto a second line
// beside the icon. An admin session has no icon and a muted word.
export function HealthCell({ health }: { health: Health }) {
  if (health.level === "plain") {
    return <span className="block truncate text-muted-foreground" title={health.title} data-health={health.level}>{health.word}</span>;
  }
  const Icon = HEALTH_ICON[health.level];
  return (
    <span className="flex min-w-0 items-center gap-1.5" title={health.title} data-health={health.level}>
      <Icon className={cn("size-4 shrink-0", HEALTH_TONE[health.level])} aria-hidden="true" />
      <span className="min-w-0 whitespace-normal">{health.word}</span>
    </span>
  );
}

// WhereCell is the Where column: the machine in mono over the folder in the
// caption size, each line cut inside the cell with its whole value on hover.
// The folder is cut at its start, so the end of the path shows: its line
// runs right to left around a left-to-right box, which keeps the text whole
// and in order. A session with neither draws nothing, and the console's own
// session says it runs in your browser.
export function WhereCell({ host, folder, own }: { host?: string; folder?: string; own: boolean }) {
  if (!host && !folder) return own ? <span className="text-muted-foreground" data-where-browser>{YOUR_BROWSER}</span> : null;
  return (
    <span className="flex min-w-0 flex-col" data-where>
      {host && <span className="truncate font-mono text-foreground" title={host} data-where-host>{host}</span>}
      {folder && (
        <span className="truncate text-left text-caption text-muted-foreground [direction:rtl]" title={folder} data-where-folder>
          <span dir="ltr">{folder}</span>
        </span>
      )}
    </span>
  );
}

// WaitingCell is the Waiting column: the session's waiting requests as a
// warn chip with the hourglass and the number, its words on hover and as
// its name. A session with none draws nothing.
export function WaitingCell({ n }: { n: number }) {
  if (n <= 0) return null;
  const words = waitingWords(n);
  return (
    <span role="img" aria-label={words} className="inline-flex" data-waiting={n}>
      <Chip tone="warn" icon={HourglassIcon} title={words}>{n}</Chip>
    </span>
  );
}

// sessionColumns are the list's columns. The user column leads only while
// the list is not grouped by user, and ownID marks the console's own row.
export function sessionColumns(grouped: boolean, ownID: string | null): ColumnDef<SessionRow>[] {
  const userColumn: ColumnDef<SessionRow> = {
    id: "user",
    header: sortable<SessionRow>(SESSION_LABELS.user),
    cell: ({ row }) => (row.original.username
      ? <span className="block truncate font-mono" title={row.original.username}>{row.original.username}</span>
      : <span className="block truncate font-mono text-muted-foreground">{cutID(row.original.user_id)}</span>),
    enableHiding: false,
  };
  return [
    ...(grouped ? [] : [userColumn]),
    {
      id: "session",
      header: plain<SessionRow>(SESSION_LABELS.session),
      cell: ({ row }) => (
        <span className="flex items-center gap-1.5">
          <span className="font-mono" title={row.original.id}>{cutID(row.original.id)}</span>
          {row.original.id === ownID && <Chip tone="plain" data-this-console>{THIS_CONSOLE}</Chip>}
        </span>
      ),
      enableHiding: false,
    },
    {
      id: "harness",
      header: plain<SessionRow>(SESSION_LABELS.harness),
      cell: ({ row }) => {
        const words = sessionHarness(row.original.harness);
        return <span className="block truncate" title={words}>{words}</span>;
      },
    },
    {
      id: "where",
      header: plain<SessionRow>(WHERE_COLUMN),
      cell: ({ row }) => <WhereCell host={row.original.host} folder={row.original.folder} own={row.original.id === ownID} />,
    },
    { id: "waiting", header: plain<SessionRow>(WAITING_COLUMN), cell: ({ row }) => <WaitingCell n={row.original.waiting || 0} /> },
    { id: "health", header: plain<SessionRow>(SESSION_LABELS.health), cell: ({ row }) => <HealthCell health={healthOf(row.original)} /> },
    { id: "last_seen", header: sortable<SessionRow>(SESSION_LABELS.last_seen), cell: ({ row }) => <Stamp iso={row.original.last_seen} read={agoWord} /> },
    { id: "status", header: sortable<SessionRow>(SESSION_LABELS.status), cell: ({ row }) => <SessionStatus status={row.original.status} /> },
    {
      id: "client",
      header: plain<SessionRow>(SESSION_LABELS.client),
      cell: ({ row }) => (row.original.client_version
        ? <span className="block truncate font-mono" title={row.original.client_version}>{row.original.client_version}</span>
        : <span className="text-muted-foreground">{NONE}</span>),
    },
    {
      id: "attestation",
      header: sortable<SessionRow>(SESSION_LABELS.attestation),
      cell: ({ row }) => <Chip tone={attTone(row.original.attestation)} title={ATT_TITLE[row.original.attestation]}>{row.original.attestation}</Chip>,
    },
    {
      id: "wiring",
      header: plain<SessionRow>(SESSION_LABELS.wiring),
      cell: ({ row }) => (row.original.wiring_status
        ? <Chip tone={wiringTone(row.original.wiring_status)} title={WIRING_TITLE[row.original.wiring_status] + (row.original.wiring_hash ? " " + shortHash(row.original.wiring_hash) : "")}>{HOOK_WORD[row.original.wiring_status] || row.original.wiring_status}</Chip>
        : <span className="text-muted-foreground">{NONE}</span>),
    },
    { id: "started", header: sortable<SessionRow>(SESSION_LABELS.started), cell: ({ row }) => <Stamp iso={row.original.started_at} read={relTimeText} /> },
  ];
}

const PILL_WORDS = { person: people, agent: agents, service: services };

// KindPills counts the users of each kind that hold an active session among
// rows, one pill per kind with its icon, and none at zero.
export function KindPills({ rows }: { rows: SessionRow[] }) {
  const users = { person: new Set<string>(), agent: new Set<string>(), service: new Set<string>() };
  for (const r of rows) {
    const kind = userKindOf(r.user_kind);
    if (kind && r.status === "active") users[kind].add(r.user_id);
  }
  const shown = (["person", "agent", "service"] as const).filter((k) => users[k].size > 0);
  if (shown.length === 0) return null;
  return (
    <span className="flex flex-wrap items-center gap-1.5" data-kind-pills>
      {shown.map((k) => <Pill key={k} icon={KIND_ICON[k]} data-kind-pill={k}>{PILL_WORDS[k](users[k].size)}</Pill>)}
    </span>
  );
}
