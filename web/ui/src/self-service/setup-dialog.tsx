// SetupDialog sets this browser up to approve. It names the browser, sends
// the person to sign in when the page has no session and carries on with
// the same name when the sign-in returns, and runs the setup: read what the
// account may set up, mint a one-time code, make the key here, register its
// public half, keep the record. The key is made only after the code is in
// hand, so a cooldown or a refused sign-in leaves no orphan keys, and a
// cancelled run never sets anything up: every step re-checks the generation
// the Cancel bumped. A ticked notify box is the consent to notifications,
// which are turned on once the setup has landed.
import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RefusedError } from "@/components/error-state";
import { ApiError, type SelfAnswer, readSelf, selfEnrollToken } from "@/lib/api";
import { CANCEL } from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { beginSignIn, takeResume } from "@/lib/session";
import { type ApproverError, enrollDevice } from "./approver-api";
import { useSelf } from "./context";
import { support } from "./push";
import { couldNotify, turnOn } from "./push-on";
import { exportPublicKeyB64, generateDeviceKey } from "./sign";
import { type Enrollment, type StorageOutlook, requestDurable, saveEnrollment } from "./store";
import * as W from "./words";

const HINT = "text-caption leading-snug text-muted-foreground";

// COOLDOWN_S is the wait this page assumes when the server rate limits the
// mint without saying how long.
const COOLDOWN_S = 10;

type Step = "name" | "signing-in" | "working" | "cooldown";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  // onCloseAutoFocus places the focus once the dialog has closed, by any
  // way. Without it the dialog's own default stands.
  onCloseAutoFocus?: (e: Event) => void;
};

export function SetupDialog({ open, onOpenChange, onCloseAutoFocus }: Props) {
  const ctx = useSelf();
  // The run outlives the render that started it, so the shared state it
  // touches is read through a ref and never captured.
  const shared = React.useRef(ctx);
  shared.current = ctx;

  const [step, setStep] = React.useState<Step>("name");
  const [name, setName] = React.useState("");
  const [nameMissing, setNameMissing] = React.useState(false);
  const [notifyMe, setNotifyMe] = React.useState(true);
  const [problem, setProblem] = React.useState("");
  const [left, setLeft] = React.useState(0);

  // gen is the cancellation token: Cancel, and every fresh open, bumps it,
  // and each step of the run stops when it no longer holds the live value.
  const gen = React.useRef(0);
  // One fresh sign-in per run: a session that dies between the read and the
  // mint is retried once in the same run, then the refusal stands.
  const authRetried = React.useRef(false);
  // The name a returning sign-in kept, held while the dialog is open, so a
  // second run of the open effect carries on with it too.
  const resumed = React.useRef<string | null>(null);

  // Every open starts from the browser's own name and an empty run, so a
  // dialog opened again never continues the last one.
  React.useEffect(() => {
    gen.current++;
    authRetried.current = false;
    setStep("name");
    setProblem("");
    setNameMissing(false);
    setNotifyMe(true);
    resumed.current = open ? resumed.current || takeResume() : null;
    setName(resumed.current || defaultDeviceName());
    if (resumed.current) void advance(resumed.current, null);
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  // The cooldown runs off its deadline rather than a counter, so a tick the
  // browser skipped while the tab slept does not slow the clock.
  React.useEffect(() => {
    if (step !== "cooldown") return undefined;
    const t = setInterval(() => setLeft((s) => (s > 0 ? s - 1 : 0)), 1000);
    return () => clearInterval(t);
  }, [step]);

  const { session, self, storage } = ctx;
  // A session is what proves the mint will be authorised. The shell's own
  // reading is one answer; a /v1/self answer in hand is the other.
  const hasSession = session.kind === "signed-in" || (self !== null && self !== "error");
  const canNotify = couldNotify(storage.outlook);

  // advance runs the setup from the eligibility read onwards. It is entered
  // with a session in hand, from the primary or from the sign-in card inside
  // the dialog. asked is the browser's answer to the notification prompt
  // the press raised, or null when the box was not ticked.
  const advance = async (runName: string, asked: Promise<NotificationPermission> | null) => {
    const mine = gen.current;
    const stop = () => mine !== gen.current;
    const trimmed = runName.trim();
    setProblem("");
    setStep("working");

    let who: SelfAnswer;
    try {
      who = await readSelf();
    } catch (e) {
      if (stop()) return;
      if (isStatus(e, 401) && !authRetried.current) {
        authRetried.current = true;
        setStep("signing-in");
        return;
      }
      setProblem(unreachable(e) ? W.NO_ANSWER : W.ELIGIBILITY_FAILED);
      setStep("name");
      return;
    }
    if (stop()) return;
    // The gate is read before anything is minted: an account that may not
    // set up a browser leaves the dialog for the page's resting state, never
    // a refusal.
    if (!(who.enroll_channels || []).includes("browser")) {
      void shared.current.refreshSelf();
      onOpenChange(false);
      return;
    }

    let mint;
    try {
      mint = await selfEnrollToken();
    } catch (e) {
      if (stop()) return;
      if (isStatus(e, 429)) {
        setLeft(retryAfter(e) || COOLDOWN_S);
        setStep("cooldown");
        return;
      }
      if (isStatus(e, 401) && !authRetried.current) {
        authRetried.current = true;
        setStep("signing-in");
        return;
      }
      setProblem(W.failedWhy(e as ApiError));
      setStep("name");
      return;
    }
    if (stop()) return;

    await requestDurable();
    let keys: CryptoKeyPair;
    let publicKey: string;
    try {
      keys = await generateDeviceKey();
      publicKey = await exportPublicKeyB64(keys.publicKey);
    } catch {
      if (stop()) return;
      setProblem(W.KEY_FAILED);
      setStep("name");
      return;
    }
    // The one-time code dies unspent here, and nothing is registered.
    if (stop()) return;

    let answer;
    try {
      answer = await enrollDevice({
        enroll_token: mint.enroll_token,
        device: {
          name: trimmed,
          platform: "browser",
          key_alg: "ES256",
          public_key: publicKey,
          // The honest posture: a WebCrypto key cannot be exported but it is
          // not hardware backed, and a browser has no attestation to offer.
          key_security_level: "software",
          attestation: { kind: "none", blob: "" },
        },
      });
    } catch (e) {
      if (stop()) return;
      setProblem(W.failedWhy(e as ApproverError));
      setStep("name");
      return;
    }
    if (stop()) return;

    const rec: Enrollment = {
      deviceId: answer.approver_device_id,
      deviceToken: answer.device_token,
      tokenExpiresAt: Date.now() + (answer.expires_in || 0) * 1000,
      project: answer.project || mint.project || null,
      user: answer.user || mint.user || null,
      webpush: answer.webpush || null,
      keys,
      deviceName: trimmed,
      enrolledAt: new Date().toISOString(),
    };
    try {
      await saveEnrollment(rec);
    } catch (e) {
      // Straza now holds a device this browser cannot use, so the sentence
      // names it for the person to remove under Devices.
      setProblem(W.storeRefused(messageOf(e), trimmed));
      setStep("name");
      return;
    }
    if (stop()) return;

    shared.current.setEnrollment(rec);
    onOpenChange(false);
    notify.ok(W.setUpToast((rec.user && rec.user.username) || who.username));
    if (asked) void notifyAfter(rec, asked, shared.current.storage.outlook);
  };

  // notifyAfter turns notifications on once the setup has landed. Neither a
  // no at the browser's prompt nor a failure undoes the setup. A fresh copy
  // of the record goes to the shell, so a tab that shows this browser reads
  // the subscription.
  const notifyAfter = async (rec: Enrollment, asked: Promise<NotificationPermission>, outlook: StorageOutlook) => {
    if ((await asked.catch(() => "default")) !== "granted") {
      notify.warn(W.NOTIFY_DECLINED);
      return;
    }
    const can = support(rec, outlook);
    if (!can.available) {
      notify.warn(can.why);
      return;
    }
    if (await turnOn(rec)) shared.current.setEnrollment({ ...rec });
  };

  // begin is the primary's run: the name is checked here, since an empty one
  // is this dialog's own refusal and never reaches the server. A ticked box
  // asks the browser for permission inside the press, the one moment a
  // browser lets a page ask, and the answer is used once the setup lands.
  const begin = () => {
    if (!name.trim()) {
      setNameMissing(true);
      return;
    }
    setNameMissing(false);
    if (!hasSession) {
      void leave();
      return;
    }
    const asked = canNotify && notifyMe ? Notification.requestPermission() : null;
    void advance(name, asked);
  };

  // leave sends the tab to sign in, carrying the name, and the dialog opens
  // again with it when the sign-in returns.
  const leave = async () => {
    setProblem("");
    try {
      await beginSignIn({ back: window.location.pathname, sheet: name.trim() });
    } catch (e) {
      setProblem(unreachable(e) ? W.NO_ANSWER : messageOf(e));
      setStep("name");
    }
  };

  const cancel = () => {
    gen.current++;
    onOpenChange(false);
  };

  const primary = step === "name"
    ? <Button onClick={begin} data-setup-run>{hasSession ? W.SET_UP : W.SIGN_IN}</Button>
    : step === "cooldown"
      ? <Button disabled={left > 0} onClick={() => void advance(name, null)}>{W.TRY_AGAIN}</Button>
      : null;

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) gen.current++; onOpenChange(o); }}>
      <DialogContent className="flex max-h-[90vh] flex-col gap-0 p-0 sm:max-w-[520px]" data-setup-dialog={step} onCloseAutoFocus={onCloseAutoFocus}>
        <DialogHeader className="border-b border-border px-5 py-4 pr-12 text-left">
          <DialogTitle className="text-section leading-snug">{W.SETUP_BROWSER}</DialogTitle>
          <DialogDescription className="leading-relaxed text-text-2">{W.SETUP_SUB}</DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-5 py-4">
          {step === "name" && (
            <>
              {storage.outlook === "ephemeral" && <Warning text={W.PRIVATE_WINDOW} />}
              {storage.outlook === "unknown" && <Warning text={W.OUTLOOK_UNKNOWN} />}
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="device-name">{W.DEVICE_NAME_LABEL}</Label>
                <Input
                  id="device-name"
                  value={name}
                  maxLength={60}
                  spellCheck={false}
                  aria-invalid={nameMissing || undefined}
                  aria-describedby={nameMissing ? "device-name-missing" : "device-name-hint"}
                  onChange={(e) => { setName(e.target.value); if (e.target.value.trim()) setNameMissing(false); }}
                />
                {nameMissing
                  ? <span id="device-name-missing" className="text-caption leading-snug text-danger" role="alert" data-name-required>{W.DEVICE_NAME_REQUIRED}</span>
                  : <span id="device-name-hint" className={HINT}>{W.DEVICE_NAME_HINT}</span>}
              </div>
              {canNotify && (
                <label className="flex min-h-11 cursor-pointer items-start gap-2.5" data-notify-me>
                  <input type="checkbox" className="mt-1 size-4 shrink-0 accent-link" checked={notifyMe} onChange={(e) => setNotifyMe(e.target.checked)} />
                  <span className="flex flex-col gap-0.5">
                    <span className="text-dense text-foreground">{W.NOTIFY_ME}</span>
                    <span className={HINT}>{W.NOTIFY_HINT}</span>
                  </span>
                </label>
              )}
            </>
          )}

          {step === "signing-in" && (
            <>
              <p className="m-0 text-dense text-text-2" data-setup-step>{W.SHEET_SIGN_IN}</p>
              <div><Button onClick={() => void leave()} data-setup-sign-in>{W.SIGN_IN}</Button></div>
            </>
          )}

          {step === "working" && (
            <p className="m-0 flex items-center gap-2 text-dense text-text-2" role="status" data-setup-step>
              <Loader2Icon className="size-4 animate-spin text-link" aria-hidden="true" />
              {W.SETTING_UP}
            </p>
          )}

          {step === "cooldown" && <Warning text={W.cooldownLine(left)} />}

          {problem && <RefusedError subject="" heading={W.SETUP_FAILED} message={problem} />}
        </div>

        <DialogFooter className="border-t border-border px-5 py-3">
          <Button variant="outline" onClick={cancel}>{CANCEL}</Button>
          {primary}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Warning is the amber line of this dialog: the storage outlook before the
// key is made, and the cooldown the server asked for.
function Warning({ text }: { text: string }) {
  return (
    <p className="m-0 border-l-[3px] border-warn bg-card px-3 py-2 text-caption leading-relaxed text-text-2" role="status" data-setup-warning>{text}</p>
  );
}

// defaultDeviceName guesses a name the person will recognise on Devices:
// the brand the browser declares, on the platform it declares, and a plain
// word rather than a wrong guess.
export function defaultDeviceName(): string {
  const nav = typeof navigator !== "undefined" ? navigator : ({} as Navigator);
  const ua = nav.userAgent || "";
  const uad = (nav as { userAgentData?: { brands?: { brand: string }[]; platform?: string } }).userAgentData;
  let brand = "";
  if (uad && Array.isArray(uad.brands)) {
    const real = uad.brands.map((b) => b.brand).filter((b) => b && !/not.?a.?brand/i.test(b));
    brand = real[real.length - 1] || "";
  }
  if (!brand) {
    brand = /Edg\//.test(ua) ? "Edge"
      : /OPR\//.test(ua) ? "Opera"
        : /Firefox\//.test(ua) ? "Firefox"
          : /Chrome\//.test(ua) ? "Chrome"
            : /Safari\//.test(ua) ? "Safari" : "Browser";
  }
  const platform = (uad && uad.platform) || (
    /Windows/.test(ua) ? "Windows"
      : /Macintosh|Mac OS X/.test(ua) ? "macOS"
        : /Android/.test(ua) ? "Android"
          : /iPhone|iPad/.test(ua) ? "iOS"
            : /Linux/.test(ua) ? "Linux" : "");
  return platform ? brand + " on " + platform : brand;
}

// The two lanes this dialog calls throw different classes: the login-session
// calls answer ApiError and the setup call answers ApproverError. Both carry
// the status, the server's own sentence and whether it answered at all.
function isStatus(e: unknown, status: number): boolean {
  return (e as ApiError | ApproverError).status === status;
}

function unreachable(e: unknown): boolean {
  return !!(e as ApiError | ApproverError).unreachable;
}

function retryAfter(e: unknown): number {
  return e instanceof ApiError ? e.retryAfter || 0 : 0;
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
