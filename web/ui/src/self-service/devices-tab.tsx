// The Devices tab of the self-service page: where the person's approvals
// come from. This browser's row is read from its own record, so it shows
// signed out too; the person's other devices come from the server's list,
// which takes the sign-in session. Turning notifications on asks here
// before the browser's own prompt, because that prompt is answered once
// and remembered. Removing this browser deletes its key; removing another
// device asks the server to forget it.
import * as React from "react";
import { HourglassIcon, Loader2Icon, SmartphoneIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button, DANGER_FILL } from "@/components/ui/button";
import { EmptyState } from "@/components/empty-state";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { type ApiError, type SelfDevice, removeSelfDevice } from "@/lib/api";
import { shortID } from "@/lib/approval-model";
import { CANCEL } from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { selfUnenroll } from "./approver-api";
import { standingOf, useSelf } from "./context";
import { OtherRow, type Push, ThisBrowserRow } from "./device-rows";
import { disable, retireOldWorker, support, sync, teardown } from "./push";
import { turnOn as pushOn } from "./push-on";
import { useDevices } from "./self-devices";
import { SetupCard } from "./setup-card";
import { type Enrollment, wipeEnrollment } from "./store";
import * as W from "./words";

const MUTED = "m-0 text-dense leading-relaxed text-muted-foreground";

// UNENROLL_MS is how long Remove waits for the server to forget this
// browser before it deletes the key anyway. The person's removal must never
// be held up by a slow server, and the sentence stays honest either way.
const UNENROLL_MS = 3000;

const IDLE: Push = { available: false, why: "", on: false };

type Ask = { kind: "push" } | { kind: "remove-here" } | { kind: "remove"; device: SelfDevice };

export function DevicesTab() {
  const { session, self, enrollment, storage, setEnrollment, openSignIn } = useSelf();
  const signedIn = session.kind === "signed-in";
  const devices = useDevices(signedIn);

  const [ask, setAsk] = React.useState<Ask | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [push, setPush] = React.useState<Push>(IDLE);
  const [lost, setLost] = React.useState("");
  const [refusal, setRefusal] = React.useState("");

  // The worker of the replaced approvals page outlives that page, and its
  // notifications open an address that is no longer served.
  React.useEffect(() => { void retireOldWorker(); }, []);

  // What this browser can be told, read again whenever the record changes:
  // the deployment's key arrives with the setup, and the browser's own
  // permission moves under the page. A stored registration is only a claim,
  // so sync proves or disproves it against what the browser holds.
  const probe = React.useCallback(() => {
    const can = support(enrollment, storage.outlook);
    setPush({ available: can.available, why: can.why, on: can.available && !!(enrollment && enrollment.push) });
  }, [enrollment, storage.outlook]);

  React.useEffect(() => {
    probe();
    setLost("");
    if (!enrollment || !enrollment.push) return undefined;
    let alive = true;
    void sync(enrollment, storage.outlook).then((r) => {
      if (!alive) return;
      setPush((p) => ({ ...p, on: r.on }));
      setLost(r.on ? "" : r.why);
    });
    return () => { alive = false; };
  }, [enrollment, storage.outlook, probe]);

  const turnOn = async () => {
    if (!enrollment) return;
    setAsk(null);
    setBusy(true);
    if (await pushOn(enrollment)) {
      setPush((p) => ({ ...p, on: true }));
      setLost("");
    } else {
      // The prompt may have flipped the permission the row reads.
      probe();
    }
    setBusy(false);
  };

  const turnOff = async () => {
    if (!enrollment) return;
    setBusy(true);
    const problem = await disable(enrollment);
    setPush((p) => ({ ...p, on: false }));
    setLost("");
    setBusy(false);
    if (problem) notify.warn(W.notifyRemovedLocally(problem));
    else notify.ok(W.NOTIFY_OFF_TOAST);
  };

  // Removing this browser retires the server's row before the key dies,
  // since the best-effort call may need the key-signed token refresh.
  const removeHere = async () => {
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
    if (gone) notify.ok(W.REMOVED_TOAST);
    else notify.warn(W.REMOVE_UNCONFIRMED);
    devices.reread();
  };

  const removeOther = async (d: SelfDevice) => {
    setBusy(true);
    setRefusal("");
    try {
      await removeSelfDevice(d.id);
      setAsk(null);
      notify.ok(W.removedOtherToast(d.name));
      devices.reread();
    } catch (e) {
      const err = e as ApiError;
      // A 401 has already ended the session, and the page redraws signed out.
      if (err.status !== 401) setRefusal(W.failedWhy(err));
    }
    setBusy(false);
  };

  const others = (devices.list || []).filter((d) => !enrollment || d.id !== enrollment.deviceId);
  const listed = !!enrollment || others.length > 0;
  const name = enrollment ? nameOf(enrollment) : "";
  const standing = standingOf({ session, self });

  return (
    <div className="flex flex-col gap-4">
      <SetupCard />
      {!enrollment && others.length === 0 && storage.usable && standing.kind === "read" && !standing.browser && (
        standing.mobile
          ? <EmptyState icon={SmartphoneIcon} title={W.NO_PHONE_YET}>{W.PHONE_APPROVES}</EmptyState>
          : <EmptyState icon={HourglassIcon} title={W.NO_ROLE_TITLE}>{W.NOT_ENROLLED_NO_ROLE}</EmptyState>
      )}
      {devices.failed && (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-md border border-input bg-secondary px-4 py-2.5" data-devices-unread>
          <p className="m-0 min-w-0 flex-1 text-dense leading-relaxed text-text-2">{W.DEVICES_UNREAD}</p>
          <Button variant="outline" onClick={devices.reread}>{W.TRY_AGAIN}</Button>
        </div>
      )}
      {listed && (
        <ul className="m-0 flex list-none flex-col gap-2 p-0" data-devices>
          {enrollment && (
            <ThisBrowserRow
              name={name}
              user={personOf(enrollment)}
              setUpAt={enrollment.enrolledAt}
              push={push}
              lost={lost}
              busy={busy}
              onTurnOn={() => setAsk({ kind: "push" })}
              onTurnOff={() => void turnOff()}
              onRemove={() => setAsk({ kind: "remove-here" })}
            />
          )}
          {others.map((d) => <OtherRow key={d.id} device={d} onRemove={() => { setRefusal(""); setAsk({ kind: "remove", device: d }); }} />)}
        </ul>
      )}
      {enrollment && !signedIn && (
        <p className={MUTED + " flex flex-wrap items-center gap-2"} data-sign-in-for-devices>
          {W.SIGN_IN_FOR_DEVICES}
          <Button variant="outline" onClick={openSignIn}>{W.SIGN_IN}</Button>
        </p>
      )}
      {listed && <p className={MUTED} data-devices-foot>{W.DEVICES_FOOT}</p>}

      <AlertDialog open={ask?.kind === "push"} onOpenChange={(o) => { if (!o) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-consent>
          <AlertDialogHeader>
            <AlertDialogTitle>{W.CONSENT_TITLE}</AlertDialogTitle>
            <AlertDialogDescription asChild>
              <span className="flex flex-wrap items-center gap-1 text-dense text-muted-foreground">
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

      <Confirm
        open={ask?.kind === "remove-here"}
        title={W.removeTitle(name)}
        body={W.REMOVE_BODY}
        busy={busy}
        onClose={() => setAsk(null)}
        onConfirm={() => void removeHere()}
      />
      <Confirm
        open={ask?.kind === "remove"}
        title={ask?.kind === "remove" ? W.removeTitle(ask.device.name) : ""}
        body={ask?.kind === "remove" ? W.removeOtherBody(ask.device.name) : ""}
        busy={busy}
        refusal={refusal}
        onClose={() => setAsk(null)}
        onConfirm={() => { if (ask?.kind === "remove") void removeOther(ask.device); }}
      />
    </div>
  );
}

// Confirm is the question before a device is removed, with a red Remove
// device and, after a failed removal, what went wrong.
function Confirm({ open, title, body, busy, refusal, onClose, onConfirm }: {
  open: boolean;
  title: string;
  body: string;
  busy: boolean;
  refusal?: string;
  onClose: () => void;
  onConfirm: () => void;
}) {
  return (
    <AlertDialog open={open} onOpenChange={(o) => { if (!o && !busy) onClose(); }}>
      <AlertDialogContent className="sm:max-w-[560px]" data-remove-confirm>
        <AlertDialogHeader>
          <AlertDialogTitle className="[overflow-wrap:anywhere]">{title}</AlertDialogTitle>
          <AlertDialogDescription>{body}</AlertDialogDescription>
        </AlertDialogHeader>
        {refusal && <RefusedError subject="" heading={W.DEVICE_NOT_REMOVED} message={refusal} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
          <AlertDialogAction className={DANGER_FILL} aria-busy={busy || undefined} onClick={(e) => { e.preventDefault(); if (!busy) onConfirm(); }}>
            {busy && <Loader2Icon className="animate-spin" />} {W.REMOVE_DEVICE}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

const nameOf = (rec: Enrollment) => rec.deviceName || shortID(rec.deviceId);

// personOf names who this browser approves as: the setup answer's user, or
// the identifier it carries when the server named no username.
const personOf = (rec: Enrollment) => (rec.user && (rec.user.username || shortID(rec.user.id))) || "";
