import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { ChevronRightIcon, Loader2Icon, SmartphoneIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { EnrollSheet } from "@/components/approver-enroll-sheet";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type ApproverDeviceRow, listApproverDevices, revokeApproverDevice } from "@/lib/api";
import { DEVICE_FILTERS, type DeviceFilter, deviceMatches, deviceNeedsLook, devicePasses, deviceSeenAt, shortID } from "@/lib/approval-model";
import {
  BY_PUSH,
  CANCEL,
  DEVICES_EMPTY_BODY,
  DEVICES_EMPTY_TITLE,
  DEVICES_LEGEND,
  DEVICES_PAGE,
  DEVICE_COLUMN,
  GROUP_BY_PERSON,
  LOAD_MORE,
  HARDWARE_KEY,
  KEY_HELP,
  MUST_CHECK,
  MUST_CHECK_LINE,
  NOTIFIED_HELP,
  READING_DEVICES,
  REVOKE,
  REVOKE_DEVICE,
  REVOKE_HELP,
  SEARCH_DEVICES,
  SEEN_NEVER,
  STRIP_LABEL,
  SOFTWARE_BROWSER,
  SOFTWARE_KEY,
  SOFTWARE_PHONE,
  SUBJECT_DEVICES,
  attestedWords,
  chipWords,
  deviceName,
  devicesTotal,
  groupDevices,
  hideDevices,
  isHardware,
  isPhone,
  kindBadge,
  matchWords,
  peopleWords,
  revokeBody,
  revokeName,
  revokeTitle,
  revokedToast,
  showDevices,
  shownWords,
} from "@/lib/approval-words";
import { cn } from "@/lib/utils";
import { notify } from "@/lib/notify";
import { readFailed, refused } from "@/lib/say";
import { agoWord } from "@/lib/settings-words";
import { absTime } from "@/lib/words";

// DevicesTab is the approver devices tab: every enrolled phone and
// browser with its key posture and whether Straza can notify it, Revoke at
// the row end, and the Add a phone sheet the page head opens. A strip of
// counts above the table doubles as the filter, a search finds a person or
// a device, the table reads newest contact first fifty at a time, and
// Group by person folds it into one row per person. The rows are not
// doors: a device has no sheet, because the row already carries everything
// the server keeps.

const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
const SMALL = "text-[13px] leading-snug text-muted-foreground";
const POLL_MS = 15000;
// The widths leave the Notified column the rest of a 1280 px page, which
// would be nothing at all with wider siblings.
const WIDTHS: Record<string, string> = { device: "210px", person: "96px", key: "150px", enrolled: "96px", seen: "96px", revoke: "88px" };
const LABELS: Record<string, string> = {
  device: DEVICE_COLUMN.device,
  person: DEVICE_COLUMN.person,
  key: DEVICE_COLUMN.key,
  enrolled: DEVICE_COLUMN.enrolled,
  seen: DEVICE_COLUMN.seen,
  notified: DEVICE_COLUMN.notified,
};

export type DevicesProps = {
  // addRequest is the page head's counter: Add a phone moves it, and the
  // tab opens the enrol sheet when it moves; its first value opens nothing.
  addRequest: number;
  // onChanged tells the page a device was revoked, so the count re-reads.
  onChanged: () => void;
};

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: ApproverDeviceRow[]; lastRead: Date; problem: string | null };

// personOf names whose device a row is, falling back to the identifier
// when the server answered no username.
const personOf = (d: ApproverDeviceRow) => d.username || shortID(d.user_id);

export function DevicesTab({ addRequest, onChanged }: DevicesProps) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [ask, setAsk] = React.useState<ApproverDeviceRow | null>(null);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [adding, setAdding] = React.useState(false);
  const [filter, setFilter] = React.useState<DeviceFilter>("all");
  const [search, setSearch] = React.useState("");
  const [shown, setShown] = React.useState(DEVICES_PAGE);
  const [grouped, setGrouped] = React.useState(false);
  // closed is the set of folded people once the person touched one; before
  // that every person is folded, so a fleet opens as a list of people.
  const [closed, setClosed] = React.useState<Set<string> | null>(null);

  const load = React.useCallback(() => {
    listApproverDevices().then(
      (rows) => setState({ kind: "ready", rows: rows || [], lastRead: new Date(), problem: null }),
      (e: ApiError) => {
        if (e.status === 401) return;
        const message = readFailed(SUBJECT_DEVICES, e);
        // A failed read keeps the last table on screen behind the sentence.
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, []);

  // The list polls while the tab is looked at, since a phone enrols on its
  // own time and the operator watches this page for it to land.
  React.useEffect(() => {
    load();
    const t = setInterval(() => { if (document.visibilityState !== "hidden") load(); }, POLL_MS);
    return () => clearInterval(t);
  }, [load]);

  // The head owns the action and the tab owns the sheet, so the counter
  // moving is the request; its first value opens nothing.
  const seen = React.useRef(addRequest);
  React.useEffect(() => {
    if (seen.current === addRequest) return;
    seen.current = addRequest;
    setAdding(true);
  }, [addRequest]);

  const revoke = async (row: ApproverDeviceRow) => {
    setBusy(true);
    setRefusal(null);
    try {
      await revokeApproverDevice(row.id);
      notify.ok(revokedToast(personOf(row), deviceName(row)));
      setAsk(null);
      load();
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(refused(err));
    } finally {
      setBusy(false);
    }
  };

  const columns = React.useMemo<ColumnDef<ApproverDeviceRow>[]>(() => [
    {
      id: "device",
      header: plain<ApproverDeviceRow>(DEVICE_COLUMN.device),
      cell: ({ row }) => (
        <span className="flex flex-col gap-0.5">
          <span className="flex flex-wrap items-center gap-1.5">
            <b className="font-semibold text-foreground">{deviceName(row.original)}</b>
            <WordBadge word={kindBadge(row.original)} tone="plain" attr="data-device-kind" />
          </span>
          <span className="font-mono text-[13px] text-muted-foreground">{shortID(row.original.id)}</span>
        </span>
      ),
    },
    {
      id: "person",
      header: plain<ApproverDeviceRow>(DEVICE_COLUMN.person),
      cell: ({ row }) => <span className="text-text-2">{personOf(row.original)}</span>,
    },
    {
      id: "key",
      header: plain<ApproverDeviceRow>(DEVICE_COLUMN.key, KEY_HELP),
      cell: ({ row }) => <KeyCell device={row.original} />,
    },
    {
      id: "enrolled",
      header: plain<ApproverDeviceRow>(DEVICE_COLUMN.enrolled),
      cell: ({ row }) => <Stamp iso={row.original.enrolled_at} />,
    },
    {
      id: "seen",
      header: plain<ApproverDeviceRow>(DEVICE_COLUMN.seen),
      cell: ({ row }) => (row.original.last_seen
        ? <Stamp iso={row.original.last_seen} />
        : <span className="text-muted-foreground" data-last-seen={SEEN_NEVER}>{SEEN_NEVER}</span>),
    },
    {
      id: "notified",
      header: plain<ApproverDeviceRow>(DEVICE_COLUMN.notified, NOTIFIED_HELP),
      cell: ({ row }) => (row.original.push_routes > 0
        ? <span className="text-text-2" data-notified={BY_PUSH}>{BY_PUSH}</span>
        : (
          <span className="flex flex-col items-start gap-0.5 whitespace-normal">
            <WordBadge word={MUST_CHECK} tone="warn" attr="data-notified" />
            <span className={SMALL}>{MUST_CHECK_LINE}</span>
          </span>
        )),
    },
    {
      id: "revoke",
      header: plain<ApproverDeviceRow>(""),
      enableHiding: false,
      cell: ({ row }) => (
        <span className="flex justify-end">
          <Button
            variant="outline"
            size="sm"
            className={DANGER_BUTTON}
            aria-label={revokeName(deviceName(row.original))}
            onClick={(e) => { e.stopPropagation(); setRefusal(null); setAsk(row.original); }}
          >
            {REVOKE}
          </Button>
        </span>
      ),
    },
  ], []);

  // The sheet keeps one place in the tree through every state of the read,
  // so a code minted while a read was failing survives the recovery.
  const sheet = <EnrollSheet open={adding} onOpenChange={setAdding} onMinted={load} />;
  const all = state.kind === "ready" ? state.rows : [];
  const person = ask ? personOf(ask) : "";
  const now = Date.now();
  const counts = Object.fromEntries(DEVICE_FILTERS.map((f) => [f, all.filter((d) => devicePasses(d, f, now)).length])) as Record<DeviceFilter, number>;
  // The list is held whole, so the chips and the search narrow it here and
  // the newest contact reads first.
  const matching = all.filter((d) => devicePasses(d, filter, now) && deviceMatches(d, search)).sort((a, b) => deviceSeenAt(b) - deviceSeenAt(a));
  // The table groups consecutive rows, so the grouped view sorts by person
  // first and by contact within a person.
  const rows = grouped ? [...matching].sort((a, b) => personOf(a).localeCompare(personOf(b)) || deviceSeenAt(b) - deviceSeenAt(a)) : matching.slice(0, shown);
  const people = [...new Set(matching.map(personOf))];
  const collapsed = closed ?? new Set(people.length > 1 ? people : []);
  const toggleGroup = (key: string) => setClosed((c) => { const next = new Set(c ?? collapsed); if (next.has(key)) next.delete(key); else next.add(key); return next; });
  const groupHeader = (key: string, members: ApproverDeviceRow[]) => {
    const open = !collapsed.has(key);
    return (
      <button
        type="button"
        className="flex w-full items-center gap-2 px-2 py-1 text-left text-sm hover:bg-secondary/60"
        aria-expanded={open}
        aria-label={open ? hideDevices(key) : showDevices(key)}
        onClick={() => toggleGroup(key)}
      >
        <ChevronRightIcon className={cn("size-4 text-muted-foreground transition-transform", open && "rotate-90")} aria-hidden="true" />
        <b className="font-semibold text-foreground">{key}</b>
        <span className="text-muted-foreground">{groupDevices(members.length, members.filter((d) => deviceNeedsLook(d, now)).length)}</span>
      </button>
    );
  };

  const strip = (
    <div role="group" aria-label={STRIP_LABEL} className="flex flex-wrap items-center gap-2" data-devices-strip>
      <b className="mr-1 font-semibold text-foreground" data-devices-total>{devicesTotal(all.length)}</b>
      {DEVICE_FILTERS.map((f) => {
        const warn = f === "mustcheck" || f === "softphone" || f === "stale";
        const on = filter === f;
        return (
          <button
            key={f}
            type="button"
            aria-pressed={on}
            data-chip={f}
            onClick={() => { setFilter(f); setShown(DEVICES_PAGE); }}
            className={cn(
              "rounded-full border px-2.5 py-1 text-[12.5px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              on ? (warn ? "border-warn bg-warn-bg text-warn" : "border-link bg-accent-bg text-link") : (warn ? "border-border bg-card text-warn" : "border-border bg-card text-text-2"),
            )}
          >
            {chipWords(f, counts[f])}
          </button>
        );
      })}
    </div>
  );
  const filters = (
    <>
      <Input id="devices-search" value={search} onChange={(e) => { setSearch(e.target.value); setShown(DEVICES_PAGE); }} placeholder={SEARCH_DEVICES} aria-label={SEARCH_DEVICES} className="h-9 w-full max-w-sm" />
      <label className="flex items-center gap-2 text-[13px] text-text-2">
        <Switch checked={grouped} onCheckedChange={(v) => setGrouped(!!v)} aria-label={GROUP_BY_PERSON} />
        {GROUP_BY_PERSON}
      </label>
    </>
  );

  return (
    <div className="flex flex-col gap-4">
      {state.kind === "loading" && <p className="text-sm text-muted-foreground">{READING_DEVICES}</p>}
      {state.kind === "error" && <FetchError subject={SUBJECT_DEVICES} detail={state.message} />}
      {state.kind === "ready" && (
        <>
          {state.problem && <FetchError subject={SUBJECT_DEVICES} detail={state.problem} lastRead={state.lastRead} />}

          {all.length === 0 ? (
            <EmptyState icon={SmartphoneIcon} title={DEVICES_EMPTY_TITLE}>{DEVICES_EMPTY_BODY}</EmptyState>
          ) : (
            <>
              {strip}
              <DataTable
                rows={rows}
                columns={columns}
                labels={LABELS}
                rowKey={(d) => d.id}
                rowName={(d) => deviceName(d)}
                dataAttr="data-device"
                filterBar={filters}
                count={() => (grouped ? peopleWords(people.length, matching.length) : matchWords(matching.length, all.length))}
                emptyText={DEVICES_EMPTY_BODY}
                widths={WIDTHS}
                groups={grouped ? { key: personOf, header: groupHeader, collapsed } : undefined}
                foot={!grouped && matching.length > DEVICES_PAGE ? (
                  <div className="flex flex-wrap items-center justify-center gap-3">
                    <span className="text-[13px] text-muted-foreground" data-shown>{shownWords(Math.min(shown, matching.length), matching.length)}</span>
                    {shown < matching.length && <Button variant="outline" size="sm" onClick={() => setShown((n) => n + DEVICES_PAGE)}>{LOAD_MORE}</Button>}
                  </div>
                ) : undefined}
              />
            </>
          )}

          <p className={SMALL + " max-w-[90ch]"} data-devices-legend>{DEVICES_LEGEND}</p>
        </>
      )}

      {sheet}

      <AlertDialog open={ask !== null} onOpenChange={(o) => { if (!o && !busy) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-revoke-device={ask ? ask.id : undefined}>
          <AlertDialogHeader>
            <AlertDialogTitle>{revokeTitle(ask ? deviceName(ask) : "")}</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <span className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
                {revokeBody(person)}
                <HelpTip label={REVOKE_DEVICE} text={REVOKE_HELP} />
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          {refusal && <RefusedError subject={REVOKE_DEVICE} message={refusal} />}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-white hover:bg-danger/90"
              aria-busy={busy || undefined}
              onClick={(e) => { e.preventDefault(); if (ask && !busy) void revoke(ask); }}
            >
              {busy && <Loader2Icon className="animate-spin" />} {REVOKE_DEVICE}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// KeyFacts is the part of a device the Key cell reads. The self-service
// page's This browser tab renders the same cell from its own enrolment
// record, which is not a row of this list.
export type KeyFacts = Pick<ApproverDeviceRow, "platform" | "key_security_level" | "attestation">;

// KeyCell says where the signing key lives, in the words of the column's
// help icon: a hardware key names what attested it, a software key on a
// phone is the amber case, and a software key in a browser is normal.
export function KeyCell({ device }: { device: KeyFacts }) {
  if (isHardware(device)) {
    return (
      <span className="flex flex-col items-start gap-0.5">
        <WordBadge word={HARDWARE_KEY} tone="ok" attr="data-key" />
        <span className={SMALL}>{attestedWords(device.attestation)}</span>
      </span>
    );
  }
  const phone = isPhone(device);
  return (
    <span className="flex flex-col items-start gap-0.5">
      <WordBadge word={SOFTWARE_KEY} tone={phone ? "warn" : "plain"} attr="data-key" />
      <span className={SMALL}>{phone ? SOFTWARE_PHONE : SOFTWARE_BROWSER}</span>
    </span>
  );
}

// Stamp is a time the operator way: the relative reading, the absolute
// stamp with its zone on hover.
export function Stamp({ iso }: { iso: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-default tabular-nums text-text-2">{agoWord(iso)}</span>
      </TooltipTrigger>
      <TooltipContent>{absTime(iso)}</TooltipContent>
    </Tooltip>
  );
}
