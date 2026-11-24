import * as React from "react";
import type { ApiError } from "./api";
import { readFailed } from "./say";

// Read is one read of a user's page. Each read carries its own word and its
// own failure, so a list that did not answer never blanks the parts beside
// it. off is a read the page does not make, for the reader's role or for
// this user.
export type Read<T> = { kind: "off" } | { kind: "loading" } | { kind: "ready"; data: T } | { kind: "failed"; message: string };

// useRead runs read while on holds, and again whenever nonce moves, which
// the page moves after every write that landed. A read that answered keeps
// its data on screen while it runs again. A 401 belongs to the session
// module, which takes the app to sign-in, so it leaves the state alone.
export function useRead<T>(on: boolean, read: () => Promise<T>, subject: string, nonce: number): Read<T> {
  const [state, setState] = React.useState<Read<T>>(on ? { kind: "loading" } : { kind: "off" });
  React.useEffect(() => {
    if (!on) { setState({ kind: "off" }); return; }
    let alive = true;
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    read().then(
      (data) => { if (alive) setState({ kind: "ready", data }); },
      (e) => {
        const err = e as ApiError;
        if (alive && err.status !== 401) setState({ kind: "failed", message: readFailed(subject, err) });
      },
    );
    return () => { alive = false; };
  }, [on, nonce]); // eslint-disable-line react-hooks/exhaustive-deps
  return state;
}

// dataOf is a read's answer once it landed, or null.
export const dataOf = <T,>(r: Read<T>): T | null => (r.kind === "ready" ? r.data : null);
