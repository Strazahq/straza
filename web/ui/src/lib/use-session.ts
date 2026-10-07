// The session's life in the app: resume
// on mount, the refresh cycle, the auth-lost route, sign-in from the card
// and sign-out. The token itself stays inside the session module.
import * as React from "react";
import { ApiError } from "./api";
import { type CheckinResponse, logout, onAuthLost, refresh, resume, revokeSelf } from "./session";

// SignedOutReason says why the tab has no session, so the sign-in card can
// word its notice: none is a fresh tab, lost carries the server's sentence,
// signed-out and unconfirmed follow a sign-out, and unreachable keeps the
// stored token for the next try.
export type SignedOutReason = { kind: "none" | "lost" | "signed-out" | "unconfirmed" | "unreachable"; detail: string };

// SessionState is what the app renders by: booting while the stored token
// is being resumed, then the card or the shell.
export type SessionState =
  | { kind: "booting" }
  | { kind: "signed-out"; reason: SignedOutReason }
  | { kind: "signed-in"; user: string; grants: string; servers: number; expiresAt: number };

// The refresh cycle re-checks in at sixty seconds remaining, on a five second tick.
const TICK_MS = 5000;
const REFRESH_UNDER_MS = 60000;

function stateFrom(resp: CheckinResponse): SessionState {
  return { kind: "signed-in", user: resp.user, grants: resp.admin_grants || "", servers: resp.admin_servers || 0, expiresAt: Date.now() + (resp.expires_in || 300) * 1000 };
}

function signedOut(kind: SignedOutReason["kind"], detail = ""): SessionState {
  return { kind: "signed-out", reason: { kind, detail } };
}

// useSession owns the session state for the app root, which calls it once.
// adminOnly is the console's gate: a resumed session with neither admin
// grants nor a server it administers goes onward to the self-service page,
// which calls this without the gate.
export function useSession({ adminOnly = true }: { adminOnly?: boolean } = {}): { state: SessionState; signedIn: (resp: CheckinResponse) => void; signOut: () => Promise<void> } {
  const [state, setState] = React.useState<SessionState>({ kind: "booting" });

  React.useEffect(() => onAuthLost((reason) => setState(signedOut("lost", reason))), []);

  // Resume on load: a page refresh re-checks in with the per-tab stored token
  // instead of demanding a fresh device flow. A refusal falls through to the
  // sign-in card; an unreachable server says so without discarding the
  // (possibly still valid) stored token.
  React.useEffect(() => {
    let alive = true;
    (async () => {
      let next: SessionState;
      try {
        const r = await resume();
        if (r && (r.admin_grants || r.admin_servers || !adminOnly)) {
          next = stateFrom(r);
        } else if (r) {
          // No admin standing at all: this person belongs on the self-service
          // page. A session that administers one MCP server has standing and
          // stays here, where that server's page is the one thing it opens.
          // The session survives the hop (no logout: the shared per-tab key is
          // what that page resumes from), and replace keeps the console out
          // of the back stack.
          if (alive) window.location.replace("/self-service/");
          return;
        } else {
          next = signedOut("none");
        }
      } catch {
        next = signedOut("unreachable");
      }
      if (alive) setState(next);
    })();
    return () => {
      alive = false;
    };
  }, [adminOnly]);

  // Refresh cycle: re-check in at sixty seconds remaining. A transport blip
  // retries next tick; a refusal fires onAuthLost through refresh.
  const expiresAt = state.kind === "signed-in" ? state.expiresAt : null;
  const refreshing = React.useRef(false);
  React.useEffect(() => {
    if (expiresAt === null) return undefined;
    const t = setInterval(async () => {
      if (refreshing.current || expiresAt - Date.now() > REFRESH_UNDER_MS) return;
      refreshing.current = true;
      try {
        const r = await refresh();
        // A refresh is a fresh check-in, so the standing it answers is the
        // current one: a role granted since the sign-in shows up here.
        setState((s) => (s.kind === "signed-in"
          ? { ...s, grants: r.admin_grants || "", servers: r.admin_servers || 0, expiresAt: Date.now() + (r.expires_in || 300) * 1000 }
          : s));
      } catch {
        // A 401 or 403 is already routed through onAuthLost; a blip retries next tick.
      }
      refreshing.current = false;
    }, TICK_MS);
    return () => clearInterval(t);
  }, [expiresAt]);

  const signedIn = React.useCallback((resp: CheckinResponse) => setState(stateFrom(resp)), []);

  // Sign out revokes server-side first and drops the token second, so a
  // delivered revoke is never followed by a still-usable session. A 401 from
  // the revoke means the session was already gone server-side, which is the
  // success outcome. The state set here lands after onAuthLost's generic one,
  // so it is the one the card words.
  const signOut = React.useCallback(async () => {
    let revoked = true;
    try {
      await revokeSelf();
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 401) revoked = false;
    }
    logout();
    setState(signedOut(revoked ? "signed-out" : "unconfirmed"));
  }, []);

  return { state, signedIn, signOut };
}
