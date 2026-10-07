// The This browser tab of the self-service page: the Approvals area's
// device row, for the one device this page can
// speak for. It is the console's table with the console's columns, so a
// person and their administrator read the same facts about the same browser.
// The Notified cell is the one control the row carries, and turning
// notifications on asks here before the browser's own prompt, because that
// prompt is answered once and remembered. Revoke destroys the deciding key
// and is the page's only destructive action. Before this browser is
// enrolled the tab says what enrolling means and the one honest next step
// for the account looking at it.
import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Loader2Icon, MonitorIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { KeyCell, Stamp } from "@/components/approver-devices";
import { DataTable, plain } from "@/components/data-table";
import { EmptyState } from "@/components/empty-state";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { WordBadge } from "@/components/users-table";
import { ApiError } from "@/lib/api";
import { shortID } from "@/lib/approval-model";
import {
  BY_PUSH,
  CANCEL,
  DEVICES_LEGEND,
  DEVICE_COLUMN,
  KEY_HELP,
  MUST_CHECK,
  MUST_CHECK_LINE,
  NOTIFIED_HELP,
  REVOKE,
  REVOKE_DEVICE,
  devicesCount,
  kindBadge,
  revokeTitle,
} from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { readFailed } from "@/lib/say";
import { type ApproverError, pending, selfUnenroll } from "./approver-api";
import { useSelf } from "./context";
import { EnrollSheet } from "@/components/approver-enroll-sheet";
import { selfEnrollToken } from "@/lib/api";
import { EnableSheet } from "./enable-sheet";
import { disable, enable, retireOldWorker, support, sync, teardown } from "./push";
import { type Enrollment, wipeEnrollment } from "./store";
import * as W from "./words";

const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
const SMALL = "text-[13px] leading-snug text-muted-foreground";
const LEGEND = SMALL + " max-w-[90ch]";
// The widths are the console's, less the columns a one-browser table never
// needs to leave room for.
const WIDTHS: Record<string, string> = { device: "220px", person: "110px", key: "150px", enrolled: "110px", seen: "100px", revoke: "96px" };
const LABELS: Record<string, string> = {
  device: DEVICE_COLUMN.device,
  person: DEVICE_COLUMN.person,
  key: DEVICE_COLUMN.key,
  enrolled: DEVICE_COLUMN.enrolled,
  seen: DEVICE_COLUMN.seen,
  notified: DEVICE_COLUMN.notified,
};

// THIS_BROWSER is the key posture of every browser enrolment: a WebCrypto
// key that cannot be exported, with no attestation to offer.
const THIS_BROWSER = { platform: "browser", key_security_level: "software", attestation: "none" };

// UNENROLL_MS is how long Revoke waits for the server to retire the row
// before it destroys the key anyway. The person's revoke must never be held
// up by a slow server, and the sentence stays honest either way.
const UNENROLL_MS = 3000;

type Push = { available: boolean; why: string; on: boolean };
const IDLE: Push = { available: false, why: "", on: false };

export function BrowserTab() {
  const { session, self, enrollment, storage, setEnrollment, refreshSelf, sheets, setSheet } = useSelf();

  const [ask, setAsk] = React.useState<"push" | "revoke" | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [push, setPush] = React.useState<Push>(IDLE);
  const [pushLine, setPushLine] = React.useState("");
  const [seen, setSeen] = React.useState(false);
  const [problem, setProblem] = React.useState("");

  // The worker of the replaced approvals page outlives that page, and its
  // notifications open an address that is no longer served.
  React.useEffect(() => { void retireOldWorker(); }, []);

  // The head opens the two sheets and the shell keeps whether they are
  // open, so a press before this tab has loaded still opens one here.
  // Add a phone is the console's sheet over the person's own mint; the
  // phone then approves the requests routed to them, this page aside.
  const me = (session.kind === "signed-in" ? session.user : "") || (self && self !== "error" ? self.username : "");
  const phone = React.useMemo(() => ({
    username: me,
    mint: async () => {
      const a = await selfEnrollToken("mobile");
      return { ...a, servers: a.servers || [], user: { id: (a.user && a.user.id) || "", username: (a.user && a.user.username) || me } };
    },
  }), [me]);

  // One read of the queue proves the credential still works and is what
  // Last seen can honestly say. A failure keeps the row on screen under the
  // console's failed-read line; a revoked device is the shell's business and
  // is not said twice.
  React.useEffect(() => {
    if (!enrollment) return undefined;
    let alive = true;
    setSeen(false);
    pending<unknown>("decidable").then(
      () => { if (alive) { setSeen(true); setProblem(""); } },
      (e) => { if (alive) setProblem(readProblem(e)); },
    );
    return () => { alive = false; };
  }, [enrollment]);

  // What this browser can be told, re-read whenever the enrolment changes:
  // the VAPID advert arrives with the enrolment, and the browser's own
  // permission state moves under the page. A stored registration is only a
  // claim, so sync proves or disproves it against what the browser holds.
  const probe = React.useCallback(() => {
    const can = support(enrollment, storage.outlook);
    setPush({ available: can.available, why: can.why, on: can.available && !!(enrollment && enrollment.push) });
  }, [enrollment, storage.outlook]);

  React.useEffect(() => {
    probe();
    setPushLine("");
    if (!enrollment || !enrollment.push) return undefined;
    let alive = true;
    void sync(enrollment, storage.outlook).then((r) => {
      if (!alive) return;
      setPush((p) => ({ ...p, on: r.on }));
      setPushLine(r.on ? "" : r.why);
    });
    return () => { alive = false; };
  }, [enrollment, storage.outlook, probe]);

  const turnOn = async () => {
    if (!enrollment) return;
    setAsk(null);
    setBusy(true);
    try {
      const body = await enable(enrollment);
      if (body) {
        setPush((p) => ({ ...p, on: true }));
        setPushLine("");
        notify.ok(W.NOTIFY_ON_TOAST);
      } else {
        // Saying no to the browser is an outcome, not a failure, and the
        // prompt has now flipped the permission the cell reads.
        notify.warn(W.NOTIFY_DECLINED);
        probe();
      }
    } catch (e) {
      notify.failed(W.notifyFailed(messageOf(e)));
      probe();
    }
    setBusy(false);
  };

  const turnOff = async () => {
    if (!enrollment) return;
    setBusy(true);
    const refusal = await disable(enrollment);
    setPush((p) => ({ ...p, on: false }));
    setPushLine("");
    setBusy(false);
    if (refusal) notify.warn(W.notifyRemovedLocally(refusal));
    else notify.ok(W.NOTIFY_OFF_TOAST);
  };

  // Revoke retires the server row before the key dies, since the best-effort
  // DELETE may need the key-signed token refresh to get through.
  const revoke = async () => {
    setBusy(true);
    const gone = await Promise.race([
      selfUnenroll(),
      new Promise<boolean>((res) => setTimeout(() => res(false), UNENROLL_MS)),
    ]);
    await teardown();
    await wipeEnrollment();
    setEnrollment(null);
    setAsk(null);
    setBusy(false);
    if (gone) notify.ok(W.REVOKED_TOAST);
    else notify.warn(W.REVOKE_UNCONFIRMED);
  };

  const columns = React.useMemo<ColumnDef<Enrollment>[]>(() => [
    {
      id: "device",
      header: plain<Enrollment>(DEVICE_COLUMN.device),
      cell: ({ row }) => (
        <span className="flex flex-col gap-0.5">
          <span className="flex flex-wrap items-center gap-1.5">
            <b className="min-w-0 whitespace-normal break-all font-semibold text-foreground">{nameOf(row.original)}</b>
            <WordBadge word={kindBadge(THIS_BROWSER)} tone="plain" attr="data-device-kind" />
            <WordBadge word={W.THIS_BROWSER_BADGE} tone="accent" attr="data-this-browser" />
          </span>
          <span className="font-mono text-[13px] text-muted-foreground">{shortID(row.original.deviceId)}</span>
        </span>
      ),
    },
    {
      id: "person",
      header: plain<Enrollment>(DEVICE_COLUMN.person),
      cell: ({ row }) => <span className="text-text-2">{personOf(row.original)}</span>,
    },
    {
      id: "key",
      header: plain<Enrollment>(DEVICE_COLUMN.key, KEY_HELP),
      cell: () => <KeyCell device={THIS_BROWSER} />,
    },
    {
      id: "enrolled",
      header: plain<Enrollment>(DEVICE_COLUMN.enrolled),
      cell: ({ row }) => <Stamp iso={row.original.enrolledAt} />,
    },
    {
      id: "seen",
      header: plain<Enrollment>(DEVICE_COLUMN.seen),
      // The stored token's expiry is not a contact, so the honest readings
      // are "now" for a page that just read its queue and "unknown" for one
      // that could not.
      cell: () => <span className="text-text-2" data-last-seen={seen ? W.SEEN_NOW : W.SEEN_UNKNOWN}>{seen ? W.SEEN_NOW : W.SEEN_UNKNOWN}</span>,
    },
    {
      id: "notified",
      header: plain<Enrollment>(DEVICE_COLUMN.notified, NOTIFIED_HELP),
      cell: () => (
        <span className="flex flex-col items-start gap-0.5 whitespace-normal">
          {!push.available ? (
            <>
              <WordBadge word={W.PUSH_UNAVAILABLE} tone="plain" attr="data-notified" />
              <span className={SMALL}>{push.why}</span>
            </>
          ) : push.on ? (
            <>
              <span className="text-text-2" data-notified={BY_PUSH}>{BY_PUSH}</span>
              <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" disabled={busy} onClick={() => void turnOff()} data-turn-off>{W.TURN_OFF}</Button>
            </>
          ) : (
            <>
              <WordBadge word={MUST_CHECK} tone="warn" attr="data-notified" />
              <span className={SMALL}>{pushLine || MUST_CHECK_LINE}</span>
              <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" disabled={busy} onClick={() => setAsk("push")} data-turn-on>{W.TURN_ON}</Button>
            </>
          )}
        </span>
      ),
    },
    {
      id: "revoke",
      header: plain<Enrollment>(""),
      enableHiding: false,
      cell: ({ row }) => (
        <span className="flex justify-end">
          <Button variant="outline" size="sm" className={DANGER_BUTTON} aria-label={REVOKE + " " + nameOf(row.original)} onClick={() => setAsk("revoke")}>{REVOKE}</Button>
        </span>
      ),
    },
  ], [push, pushLine, busy, seen]); // eslint-disable-line react-hooks/exhaustive-deps

  const sheetNode = (
    <>
      <EnableSheet open={sheets.enable} onOpenChange={(o) => setSheet("enable", o)} />
      <EnrollSheet open={sheets.phone} onOpenChange={(o) => setSheet("phone", o)} self={phone} />
    </>
  );

  if (!enrollment) {
    return (
      <div className="flex flex-col gap-4">
        <NotEnrolled
          usable={storage.usable}
          signedIn={session.kind === "signed-in" || (self !== null && self !== "error")}
          channels={self && self !== "error" ? self.enroll_channels || [] : null}
          failed={self === "error"}
          onRetry={() => void refreshSelf()}
        />
        <p className={LEGEND} data-devices-legend>{DEVICES_LEGEND}</p>
        {sheetNode}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      {problem && <FetchError subject={W.SUBJECT_THIS_BROWSER} detail={problem} />}
      <DataTable
        rows={[enrollment]}
        columns={columns}
        labels={LABELS}
        rowKey={(d) => d.deviceId}
        rowName={(d) => nameOf(d)}
        dataAttr="data-device"
        count={(n) => devicesCount(n)}
        emptyText={W.DECIDING_MEANS}
        widths={WIDTHS}
      />
      <p className={LEGEND} data-browser-legend>
        {W.NO_EXPIRY_LINE} <HelpTip label={W.SIGN_OUT} text={W.SIGN_OUT_VS_REVOKE} />
      </p>
      <p className={LEGEND} data-devices-legend>{DEVICES_LEGEND}</p>

      <AlertDialog open={ask === "push"} onOpenChange={(o) => { if (!o) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-consent>
          <AlertDialogHeader>
            <AlertDialogTitle>{W.CONSENT_TITLE}</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <span className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
                {W.CONSENT_BODY}
                <HelpTip label={W.TURN_ON} text={W.CONSENT_HELP} />
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction onClick={(e) => { e.preventDefault(); void turnOn(); }}>{W.CONTINUE}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={ask === "revoke"} onOpenChange={(o) => { if (!o && !busy) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-revoke-browser={enrollment.deviceId}>
          <AlertDialogHeader>
            <AlertDialogTitle>{revokeTitle(nameOf(enrollment))}</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <span className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
                {W.REVOKE_BODY}
                <HelpTip label={REVOKE_DEVICE} text={W.REVOKE_BROWSER_HELP} />
              </span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-danger text-white hover:bg-danger/90"
              aria-busy={busy || undefined}
              onClick={(e) => { e.preventDefault(); if (!busy) void revoke(); }}
            >
              {busy && <Loader2Icon className="animate-spin" />} {REVOKE_DEVICE}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {sheetNode}
    </div>
  );
}

// NotEnrolled says what enrolling means, then the one next step that is
// true for this account: signing in, the action in the page head, an
// administrator's QR code, the identity provider, or a browser that will
// not keep a key at all.
function NotEnrolled({ usable, signedIn, channels, failed, onRetry }: {
  usable: boolean;
  signedIn: boolean;
  channels: string[] | null;
  failed: boolean;
  onRetry: () => void;
}) {
  if (!usable) {
    return (
      <p className="border-l-[3px] border-danger bg-card px-4 py-3 text-sm leading-relaxed text-text-2" role="alert" data-no-storage>{W.NO_STORAGE}</p>
    );
  }
  const next = failed ? W.ELIGIBILITY_UNREAD
    : !signedIn ? W.ENABLE_SIGN_IN_FIRST
      : !channels ? W.ENABLE_SIGN_IN_FIRST
        : channels.includes("browser") ? W.ENABLE_HERE
          : channels.includes("mobile") ? W.ENABLE_PHONE_ONLY : W.NOT_ENROLLED_NO_ROLE;
  return (
    <EmptyState
      icon={MonitorIcon}
      title={W.NOT_ENROLLED_TITLE}
      action={failed ? <Button variant="outline" onClick={onRetry}>{W.TRY_AGAIN}</Button> : undefined}
    >
      <span className="inline-flex flex-wrap items-center justify-center gap-1">
        {W.DECIDING_MEANS} {next}
        <HelpTip label={W.ENABLE_BROWSER} text={W.KEY_MECHANICS} />
      </span>
    </EmptyState>
  );
}

const nameOf = (rec: Enrollment) => rec.deviceName || shortID(rec.deviceId);

// personOf names who this browser decides as: the enrolment answer's user,
// or the identifier it carries when the server named no username.
const personOf = (rec: Enrollment) => (rec.user && (rec.user.username || shortID(rec.user.id))) || "";

// readProblem words a failed proof of the credential. A revoked or missing
// device is the shell's own answer, said once and not here.
function readProblem(e: unknown): string {
  const err = e as ApproverError;
  if (err.revoked || err.notEnrolled) return "";
  return readFailed(W.SUBJECT_THIS_BROWSER, new ApiError(err.message || String(e), err.status || 0, !!err.unreachable));
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
