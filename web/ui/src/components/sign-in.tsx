// The sign-in card: the real RFC 8628
// device flow as OAuth client console. Request a code, the operator
// authorizes it in a new tab (the password page in standalone, the external
// IdP in enterprise), poll the token endpoint, then exchange the id token
// for a session token through checkin. The session module mirrors the token
// per tab, so a refresh resumes instead of landing back here. The emergency
// sign-in runs the same flow at the server's own page, the door for the
// break-glass admin when the identity provider cannot sign anyone in.
import * as React from "react";
import { CheckIcon, CopyIcon, ExternalLinkIcon, LoaderCircleIcon } from "lucide-react";
import { StrazaMark } from "@/components/straza-mark";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api";
import { type CheckinResponse, type DeviceGrant, IdpUnreachableError, checkin, devicePoll, deviceStart } from "@/lib/session";
import type { SignedOutReason } from "@/lib/use-session";

type Phase = "idle" | "starting" | "pending" | "exchanging" | "stopped";

const EXPIRED = "the code expired before it was authorized. Start again.";
const UNCONFIRMED = "Signed out in this browser, but the server did not confirm the revoke: the session expires on its own within minutes, or sign back in and revoke it from Sessions.";

// noticeFor words why the last session ended, or null on a fresh tab.
function noticeFor(reason: SignedOutReason, returnTo: string): string | null {
  switch (reason.kind) {
    case "lost":
      return "Your session ended: " + reason.detail + ". Sign in again and you return to " + returnTo + ".";
    case "signed-out":
      return "Signed out. The session was revoked on the server.";
    case "unconfirmed":
      return UNCONFIRMED;
    case "unreachable":
      return "strazad is unreachable, state unknown. Sign in once it is back.";
    default:
      return null;
  }
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function copy(code: string) {
  try {
    void navigator.clipboard.writeText(code).catch(() => undefined);
  } catch {
    // No clipboard here; the code stays selectable.
  }
}

function Waiting({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2 text-[13px] text-muted-foreground" role="status">
      <LoaderCircleIcon className="size-[13px] animate-spin text-link" aria-hidden="true" />
      {children}
    </div>
  );
}

// SignIn is the card the app shows while the tab has no session. returnTo is
// the label of the page the person was on; onAuthed receives the checkin
// response once an admin signed in.
// adminOnly keeps the console's gate: a person without admin grants goes
// onward to the self-service page, still signed in. embedded renders the
// card's inside alone, for a dialog on that page.
export function SignIn({ reason, returnTo, onAuthed, adminOnly = true, embedded = false }: { reason: SignedOutReason; returnTo: string; onAuthed: (resp: CheckinResponse) => void; adminOnly?: boolean; embedded?: boolean }) {
  const [phase, setPhase] = React.useState<Phase>("idle");
  const [grant, setGrant] = React.useState<DeviceGrant | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [emergency, setEmergency] = React.useState(false);
  const authWin = React.useRef<Window | null>(null);
  const notice = noticeFor(reason, returnTo);

  // closeAuthWin closes the verification tab at every terminal outcome;
  // closing a window this script opened is always permitted, cross-origin
  // included.
  const closeAuthWin = () => {
    try {
      if (authWin.current && !authWin.current.closed) authWin.current.close();
    } catch {
      // The handle is already gone, nothing to close.
    }
    authWin.current = null;
  };

  const start = async (viaEmergency = false) => {
    setEmergency(viaEmergency);
    setPhase("starting");
    setProblem(null);
    try {
      setGrant(await deviceStart(viaEmergency));
      setPhase("pending");
    } catch (e) {
      const api = e instanceof ApiError ? e : null;
      setProblem(e instanceof IdpUnreachableError ? e.message
        : api && api.unreachable
        ? "strazad is unreachable, state unknown. Check the server and retry."
        : "device flow unavailable: " + messageOf(e) + (api && api.status === 404 ? " (an enterprise deployment authenticates at the external identity provider)" : ""));
      setPhase("stopped");
    }
  };

  // exchange runs outside the polling effect: the setPhase below tears that
  // effect down, so the continuation must not be gated on it, or the checkin
  // response is discarded. A checkin failure surfaces as a stopped state,
  // never a silent spinner.
  const exchange = async (idToken: string) => {
    closeAuthWin();
    setPhase("exchanging");
    try {
      const ses = await checkin(idToken);
      if (adminOnly && !ses.admin_grants && !ses.admin_servers) {
        // The console is the admin plane; an end user who signed in here goes
        // onward signed in, never dead-ended. checkin already wrote the shared
        // per-tab key, so no logout: the self-service page resumes this same
        // session on arrival.
        window.location.replace("/self-service/");
        return;
      }
      onAuthed(ses);
    } catch (e) {
      // An outage (503 from strazad, a dead proxy, no route) is not the
      // person's sign-in failing: say so, and say nothing to fix on their side.
      const api = e instanceof ApiError ? e : null;
      setProblem(api && (api.unreachable || api.status >= 500)
        ? "Straza is temporarily unavailable, try again in a moment (the sign-in itself succeeded, nothing on your side needs fixing)."
        : "signed in, but the session exchange failed: " + messageOf(e) + ". Start again.");
      setPhase("stopped");
    }
  };

  // Poll while pending. The interval comes from the grant, in seconds. OAuth
  // protocol states (authorization_pending, slow_down, expired_token) arrive
  // as the answer's error: RFC 6749 sends them with HTTP 400, and the REST
  // layer returns rather than throws them for this endpoint. The catch is
  // transport blips only: a blip is not a refusal, so polling goes on until
  // the grant's own expiry.
  React.useEffect(() => {
    if (phase !== "pending" || !grant) return undefined;
    let alive = true;
    let wait = Math.max(1, grant.interval || 2) * 1000;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const deadline = Date.now() + Math.max(60, grant.expires_in || 600) * 1000;
    const stop = (why: string) => {
      closeAuthWin();
      setProblem(why);
      setPhase("stopped");
    };

    const poll = async () => {
      if (!alive) return;
      if (Date.now() > deadline) {
        stop(EXPIRED);
        return;
      }
      try {
        const tok = await devicePoll(grant.device_code, emergency);
        if (!alive) return;
        if (tok && tok.id_token) {
          void exchange(tok.id_token); // detached: ends this effect through setPhase
          return;
        }
        const oauthErr = tok && tok.error;
        if (oauthErr === "slow_down") wait += 5000;
        if (oauthErr && oauthErr !== "authorization_pending" && oauthErr !== "slow_down") {
          stop(oauthErr === "expired_token" ? EXPIRED : oauthErr === "access_denied" ? "sign-in was denied at the verification page." : "sign-in refused: " + oauthErr);
          return;
        }
      } catch {
        if (!alive) return; // a transport blip or server hiccup: retry next tick
      }
      timer = setTimeout(poll, wait);
    };
    timer = setTimeout(poll, wait);
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [phase, grant]); // eslint-disable-line react-hooks/exhaustive-deps

  // The verification tab is opened with a handle (no noopener) so the card
  // can close it once the flow ends: the target is this deployment's own IdP,
  // inside the trust boundary that mints our tokens, and a cross-origin
  // opener reference only permits navigation, never DOM access.
  const openVerification = () => {
    if (!grant) return;
    authWin.current = window.open(grant.verification_uri_complete || grant.verification_uri, "_blank");
  };

  const idle = phase === "idle" || phase === "stopped";
  const line = "self-stretch border-l-[3px] py-2 pl-3 text-[13px] leading-relaxed text-muted-foreground";
  return (
    <div className={embedded ? "" : "flex min-h-screen items-center justify-center bg-background"}>
      <div className={embedded ? "flex w-full flex-col items-center gap-4" : "flex w-[380px] max-w-full flex-col items-center gap-4 rounded-md border border-border bg-card p-8"} data-sign-in-card>
        {!embedded && (
          <>
            <div className="flex items-center gap-2 text-xl font-semibold text-foreground">
              <StrazaMark className="size-6 shrink-0" />
              Straza
            </div>
            <div className="text-[13px] uppercase tracking-[.06em] text-muted-foreground">admin console</div>
          </>
        )}

        {notice && phase === "idle" && (reason.kind === "lost" || reason.kind === "unreachable"
          ? <p className="text-center text-[13px] leading-relaxed text-warn">{notice}</p>
          : <p className={line + " border-border"}>{notice}</p>)}

        {idle && (
          <>
            {problem && <p className={line + " border-unknown"}>{problem}</p>}
            <p className="text-center text-sm leading-relaxed text-text-2">
              You get a short code, enter it at your identity provider, and this page signs you in.
            </p>
            <Button className="w-full" onClick={() => void start()} title="One code sign-in for the CLI and this console, nothing to register at the identity provider.">
              {phase === "stopped" ? "Start again" : "Sign in with a code"}
            </Button>
            {adminOnly && (
              <>
                <p className="text-center text-[13px] leading-relaxed text-muted-foreground">
                  Admin roles only. Anyone else lands on the self-service page, still signed in.
                </p>
                <Button variant="link" size="sm" className="text-[13px] text-muted-foreground" onClick={() => void start(true)} title="For the break-glass admin when the identity provider cannot sign you in. The code goes to the server's own sign-in page, which accepts that one account.">
                  Emergency sign-in
                </Button>
              </>
            )}
          </>
        )}

        {phase === "starting" && <Waiting>Requesting a device code</Waiting>}

        {phase === "pending" && grant && (
          <>
            <div className="text-[13px] text-muted-foreground">
              {emergency ? "Enter this code on the server's emergency sign-in page, as break-glass:" : "Enter this code at your identity provider:"}
            </div>
            <div className="flex items-center gap-2 rounded-md border border-border bg-background px-4 py-3 font-mono text-[26px] font-semibold tracking-[.14em] text-link">
              <span className="select-all">{grant.user_code}</span>
              <Button variant="ghost" size="icon-xs" aria-label="Copy the code" title="Copy the code" onClick={() => copy(grant.user_code)}>
                <CopyIcon />
              </Button>
            </div>
            <Button variant="outline" className="w-full" onClick={openVerification}>
              Open the sign-in page <ExternalLinkIcon />
            </Button>
            <Waiting>Waiting for authorization</Waiting>
          </>
        )}

        {phase === "exchanging" && (
          <div className="flex items-center gap-2 text-[15px] font-semibold text-ok">
            <CheckIcon className="size-[18px]" aria-hidden="true" /> Authorized, loading the console
          </div>
        )}
      </div>
    </div>
  );
}
