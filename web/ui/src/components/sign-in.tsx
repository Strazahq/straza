// The sign-in card: a browser sign-in with PKCE
// as OAuth client console. Sign in sends this tab to the identity provider
// (the server's own page in standalone), which sends it back to
// SIGNIN_PATH, where this card finishes the sign-in and hands the person to
// the page they started on. The session module mirrors the token per tab,
// so a refresh resumes instead of landing back here. The emergency sign-in
// goes to the server's own page, the door for the break-glass admin when
// the identity provider cannot sign anyone in.
import * as React from "react";
import { CheckIcon, LoaderCircleIcon } from "lucide-react";
import { StrazaMark } from "@/components/straza-mark";
import { Button } from "@/components/ui/button";
import { ApiError } from "@/lib/api";
import { replaceAddress } from "@/lib/router";
import { type CheckinResponse, SIGNIN_PATH, beginSignIn, finishSignIn, signInHost, stashProblem, takeSignInProblem } from "@/lib/session";
import type { SignedOutReason } from "@/lib/use-session";

type Phase = "idle" | "leaving" | "finishing" | "authorized" | "stopped";

const UNCONFIRMED = "Signed out in this browser, but the server did not confirm the revoke: the session expires on its own within minutes, or sign back in and revoke it from Sessions.";
const PROVIDER_MAY_REMEMBER = " Your identity provider may still have you signed in, so on a shared computer sign out there too.";

// noticeFor words why the last session ended, or null on a fresh tab.
function noticeFor(reason: SignedOutReason, returnTo: string): string | null {
  switch (reason.kind) {
    case "lost":
      return "Your session ended: " + reason.detail + ". Sign in again and you return to " + returnTo + ".";
    case "signed-out":
      return "Signed out. The session was revoked on the server." + PROVIDER_MAY_REMEMBER;
    case "unconfirmed":
      return UNCONFIRMED;
    case "unreachable":
      return "strazad is unreachable, state unknown. Sign in once it is back.";
    default:
      return null;
  }
}

// whereLine says where Sign in sends the person: undefined while discovery
// is out, "" for the server's own page, else the provider's host.
function whereLine(host: string | undefined): string {
  if (host === undefined) return "You sign in at your identity provider and come back to this page.";
  if (host === "") return "You sign in on this server's own sign-in page and come back to this page.";
  return "You sign in at your identity provider, " + host + ", and come back to this page.";
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
// response once an admin signed in. On SIGNIN_PATH it finishes the sign-in
// the identity provider answered: a person without admin standing, or one
// who started on the self-service page, goes there still signed in.
export function SignIn({ reason, returnTo, onAuthed }: { reason: SignedOutReason; returnTo: string; onAuthed: (resp: CheckinResponse) => void }) {
  const answering = window.location.pathname === SIGNIN_PATH;
  const [phase, setPhase] = React.useState<Phase>(answering ? "finishing" : "idle");
  const [problem, setProblem] = React.useState<string | null>(() => (answering ? null : takeSignInProblem()));
  const [host, setHost] = React.useState<string | undefined>(undefined);
  const notice = noticeFor(reason, returnTo);

  React.useEffect(() => {
    let alive = true;
    signInHost().then((h) => { if (alive) setHost(h); }, () => undefined);
    return () => { alive = false; };
  }, []);

  // A Back from the identity provider can restore this page from the
  // back-forward cache with the card still leaving; say why nothing came.
  React.useEffect(() => {
    const onShow = (e: PageTransitionEvent) => {
      if (!e.persisted) return;
      setProblem(takeSignInProblem());
      setPhase("stopped");
    };
    window.addEventListener("pageshow", onShow);
    return () => window.removeEventListener("pageshow", onShow);
  }, []);

  React.useEffect(() => {
    if (!answering) return undefined;
    let alive = true;
    void finishSignIn(window.location.search).then((done) => {
      if (!alive) return;
      const fromSelf = done.back.startsWith("/self-service/");
      if (!("resp" in done)) {
        if (fromSelf) {
          stashProblem(done.problem);
          window.location.replace(done.back);
          return;
        }
        replaceAddress(done.back);
        setProblem(done.problem);
        setPhase("stopped");
        return;
      }
      // The console is the admin plane; an end user who signed in here goes
      // onward signed in, never dead-ended. checkin already wrote the shared
      // per-tab key, so the self-service page resumes this same session.
      if (fromSelf || (!done.resp.admin_grants && !done.resp.admin_servers)) {
        window.location.replace(fromSelf ? done.back : "/self-service/");
        return;
      }
      setPhase("authorized");
      replaceAddress(done.back);
      onAuthed(done.resp);
    });
    return () => { alive = false; };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const start = async (emergency = false) => {
    setPhase("leaving");
    setProblem(null);
    try {
      await beginSignIn({ emergency, back: window.location.pathname });
    } catch (e) {
      setProblem(e instanceof ApiError && e.unreachable
        ? "strazad is unreachable, state unknown. Check the server, then press Sign in again."
        : e instanceof Error ? e.message : String(e));
      setPhase("stopped");
    }
  };

  const idle = phase === "idle" || phase === "stopped";
  const line = "self-stretch border-l-[3px] py-2 pl-3 text-[13px] leading-relaxed text-muted-foreground";
  return (
    <div className="flex min-h-screen items-center justify-center bg-background">
      <div className="flex w-[380px] max-w-full flex-col items-center gap-4 rounded-md border border-border bg-card p-8" data-sign-in-card>
        <div className="flex items-center gap-2 text-xl font-semibold text-foreground">
          <StrazaMark className="size-6 shrink-0" />
          Straza
        </div>
        <div className="text-[13px] uppercase tracking-[.06em] text-muted-foreground">admin console</div>

        {notice && phase === "idle" && (reason.kind === "lost" || reason.kind === "unreachable"
          ? <p className="text-center text-[13px] leading-relaxed text-warn">{notice}</p>
          : <p className={line + " border-border"}>{notice}</p>)}

        {idle && (
          <>
            {problem && <p className={line + " border-unknown"}>{problem}</p>}
            <p className="text-center text-sm leading-relaxed text-text-2">{whereLine(host)}</p>
            <Button className="w-full" onClick={() => void start()}>Sign in</Button>
            <p className="text-center text-[13px] leading-relaxed text-muted-foreground">
              Admin roles only. Anyone else lands on the self-service page, still signed in.
            </p>
            <Button variant="link" size="sm" className="text-[13px] text-muted-foreground" onClick={() => void start(true)} title="For the break-glass admin when the identity provider cannot sign you in. The server's own sign-in page accepts that one account.">
              Emergency sign-in
            </Button>
          </>
        )}

        {phase === "leaving" && <Waiting>Taking you to the sign-in page</Waiting>}
        {phase === "finishing" && <Waiting>Signing you in</Waiting>}

        {phase === "authorized" && (
          <div className="flex items-center gap-2 text-[15px] font-semibold text-ok">
            <CheckIcon className="size-[18px]" aria-hidden="true" /> Authorized, loading the console
          </div>
        )}
      </div>
    </div>
  );
}
