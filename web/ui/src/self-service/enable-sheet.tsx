// Enable this browser: the sheet the page head's
// one action opens, in the shape of the console's Add a phone. It names the
// browser, signs the person in when the page has no session, and runs the
// enrolment: read what the account may enrol, mint a one-time token, make
// the key here, register its public half, keep the record. The key is made
// only after the token is in hand, so a cooldown or a refused sign-in leaves
// no orphan keys, and a cancelled run never enrols: every step re-checks the
// generation the Cancel bumped.
import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { SignIn } from "@/components/sign-in";
import { ApiError, type SelfAnswer, readSelf, selfEnrollToken } from "@/lib/api";
import { CANCEL } from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { refused } from "@/lib/say";
import { type ApproverError, enrollDevice } from "./approver-api";
import { useSelf } from "./context";
import { exportPublicKeyB64, generateDeviceKey } from "./sign";
import { type Enrollment, requestDurable, saveEnrollment } from "./store";
import * as W from "./words";

const HINT = "text-[13px] leading-snug text-muted-foreground";

// COOLDOWN_S is the wait this page assumes when the server rate limits the
// mint without saying how long.
const COOLDOWN_S = 10;

type Step = "name" | "signing-in" | "working" | "cooldown";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
};

export function EnableSheet({ open, onOpenChange }: Props) {
  const ctx = useSelf();
  // The run outlives the render that started it, so the shared state it
  // touches is read through a ref and never captured.
  const shared = React.useRef(ctx);
  shared.current = ctx;

  const [step, setStep] = React.useState<Step>("name");
  const [name, setName] = React.useState("");
  const [note, setNote] = React.useState("");
  const [problem, setProblem] = React.useState("");
  const [left, setLeft] = React.useState(0);

  // gen is the cancellation token: Cancel, and every fresh open, bumps it,
  // and each step of the run stops when it no longer holds the live value.
  const gen = React.useRef(0);
  // One fresh sign-in per run: a session that dies between the read and the
  // mint is retried once in the same run, then the refusal stands.
  const authRetried = React.useRef(false);

  // Every open starts from the browser's own name and an empty run, so a
  // sheet opened again never continues the last one.
  React.useEffect(() => {
    gen.current++;
    authRetried.current = false;
    setStep("name");
    setNote("");
    setProblem("");
    setName(defaultDeviceName());
  }, [open]);

  // The cooldown runs off its deadline rather than a counter, so a tick the
  // browser skipped while the tab slept does not slow the clock.
  React.useEffect(() => {
    if (step !== "cooldown") return undefined;
    const t = setInterval(() => setLeft((s) => (s > 0 ? s - 1 : 0)), 1000);
    return () => clearInterval(t);
  }, [step]);

  const { session, self, storage } = ctx;
  // A session is what proves the mint will be authorised. The shell's own
  // reading is one answer; a /v1/self answer in hand is the other, and it
  // survives a sign-in that happened inside this sheet.
  const hasSession = session.kind === "signed-in" || (self !== null && self !== "error");

  // advance runs the enrolment from the eligibility read onwards. It is
  // entered with a session in hand, from the primary or from the sign-in
  // card inside the sheet.
  const advance = async () => {
    const mine = gen.current;
    const stop = () => mine !== gen.current;
    const trimmed = name.trim();
    setProblem("");
    setStep("working");
    setNote(W.STEP_CHANNELS);

    let who: SelfAnswer;
    try {
      who = await readSelf();
    } catch (e) {
      if (stop()) return;
      if (isStatus(e, 401) && !authRetried.current) {
        authRetried.current = true;
        setNote(W.STEP_SIGN_IN);
        setStep("signing-in");
        return;
      }
      setProblem(isStatus(e, 401) ? W.ELIGIBILITY_FAILED : sentence(e));
      setStep("name");
      return;
    }
    if (stop()) return;
    // The gate is read before anything is minted: an account that may not
    // enrol a browser leaves the sheet for the tab's honest resting state,
    // never a refusal.
    if (!(who.enroll_channels || []).includes("browser")) {
      void shared.current.refreshSelf();
      onOpenChange(false);
      return;
    }

    setNote(W.STEP_MINT);
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
        setNote(W.STEP_SIGN_IN);
        setStep("signing-in");
        return;
      }
      setProblem(sentence(e));
      setStep("name");
      return;
    }
    if (stop()) return;

    setNote(W.STEP_KEY);
    await requestDurable();
    const keys = await generateDeviceKey();
    const publicKey = await exportPublicKeyB64(keys.publicKey);
    // The one-time token dies unspent here, and nothing is registered.
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
      setProblem(sentence(e));
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
      // The server now holds a device this browser cannot use, so the
      // sentence names it and an administrator can revoke it.
      setProblem(W.storeRefused(messageOf(e), trimmed));
      setStep("name");
      return;
    }
    if (stop()) return;

    shared.current.setEnrollment(rec);
    onOpenChange(false);
    notify.ok(W.enabledToast((rec.user && rec.user.username) || who.username));
  };

  // begin is the primary's run: the name is checked here, since an empty one
  // is this sheet's own refusal and never reaches the server.
  const begin = () => {
    if (!name.trim()) {
      setProblem(W.DEVICE_NAME_REQUIRED);
      return;
    }
    if (!hasSession) {
      setProblem("");
      setNote(W.STEP_SIGN_IN);
      setStep("signing-in");
      return;
    }
    void advance();
  };

  const cancel = () => {
    gen.current++;
    onOpenChange(false);
  };

  const primary = step === "name"
    ? <Button onClick={begin} data-enable-run>{hasSession ? W.ENABLE : W.SIGN_IN_AND_ENABLE}</Button>
    : step === "cooldown"
      ? <Button disabled={left > 0} onClick={() => void advance()}>{W.TRY_AGAIN}</Button>
      : null;

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) gen.current++; onOpenChange(o); }}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[520px]" data-enable-sheet={step}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="text-lg leading-snug">{W.ENABLE_BROWSER}</SheetTitle>
          <SheetDescription className="flex flex-wrap items-center gap-1">
            {W.ENABLE_SUB}
            <HelpTip label={W.ENABLE_BROWSER} text={W.KEY_MECHANICS} />
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
          {step === "name" && (
            <>
              {storage.outlook === "ephemeral" && <Warning text={W.PRIVATE_WINDOW} />}
              {storage.outlook === "unknown" && <Warning text={W.OUTLOOK_UNKNOWN} />}
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="device-name">{W.DEVICE_NAME_LABEL}</Label>
                <Input id="device-name" value={name} maxLength={60} spellCheck={false} onChange={(e) => setName(e.target.value)} />
                <span className={HINT}>{W.DEVICE_NAME_HINT}</span>
              </div>
            </>
          )}

          {step === "signing-in" && (
            <>
              <p className="text-sm text-text-2" data-enable-step>{W.STEP_SIGN_IN}</p>
              <SignIn
                reason={{ kind: "none", detail: "" }}
                returnTo={W.ENABLE_BROWSER}
                adminOnly={false}
                embedded
                onAuthed={(resp) => {
                  shared.current.signedIn(resp);
                  // The shell reads /v1/self when its own session state
                  // moves, which a sign-in inside this sheet does not do, so
                  // its copy is asked for here and the run reads its own.
                  if (shared.current.self === null) void shared.current.refreshSelf();
                  void advance();
                }}
              />
            </>
          )}

          {step === "working" && (
            <p className="flex items-center gap-2 text-sm text-text-2" role="status" data-enable-step>
              <Loader2Icon className="size-4 animate-spin text-link" aria-hidden="true" />
              {note}
            </p>
          )}

          {step === "cooldown" && <Warning text={W.cooldownLine(left)} />}

          {problem && <RefusedError subject={W.ENABLE_BROWSER} message={problem} />}
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <div className="flex items-center justify-end gap-2">
            <Button variant="outline" onClick={cancel}>{CANCEL}</Button>
            {primary}
          </div>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

// Warning is the amber line of this sheet: the storage outlook before the
// key is made, and the cooldown the server asked for.
function Warning({ text }: { text: string }) {
  return (
    <p className="border-l-[3px] border-warn bg-card px-3 py-2 text-[13px] leading-relaxed text-text-2" role="status" data-enable-warning>{text}</p>
  );
}

// defaultDeviceName guesses a name the person will recognise in their
// administrator's device list: the brand the browser declares, on the
// platform it declares, and a plain word rather than a wrong guess.
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

// The two lanes this sheet calls throw different classes: the login-session
// calls answer ApiError and the enrol call answers ApproverError. Both carry
// the status and the server's own sentence, so the readings are one each.
function isStatus(e: unknown, status: number): boolean {
  return (e as ApiError | ApproverError).status === status;
}

function retryAfter(e: unknown): number {
  return e instanceof ApiError ? e.retryAfter || 0 : 0;
}

function sentence(e: unknown): string {
  if (e instanceof ApiError) return refused(e);
  const err = e as ApproverError;
  return refused(new ApiError(messageOf(e), err.status || 0, !!err.unreachable));
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
