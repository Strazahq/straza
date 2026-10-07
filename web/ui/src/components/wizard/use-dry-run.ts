import * as React from "react";
import { type ApiError, dryRunApp } from "@/lib/api";

// Dry is the server's verdict on the manifest as it stands: not asked yet,
// asked, accepted with what it read, refused with its sentence, or not
// reached.
export type Dry =
  | { state: "idle" }
  | { state: "pending" }
  | { state: "ok"; runtime: string; credential: string }
  | { state: "refused"; text: string }
  | { state: "unreachable" };

const DEBOUNCE_MS = 400;

// useDryRun asks POST /v1/admin/apps?dryRun=1, the same code path as
// strazactl spec validate. live asks 400 ms after the last change, once
// asks at once (the Review step), off forgets the verdict. An answer to a
// manifest that has since changed is dropped.
export function useDryRun(yaml: string, mode: "live" | "once" | "off"): Dry {
  const [dry, setDry] = React.useState<Dry>({ state: "idle" });
  const seq = React.useRef(0);
  React.useEffect(() => {
    const n = ++seq.current;
    if (mode === "off") {
      setDry({ state: "idle" });
      return undefined;
    }
    setDry({ state: "pending" });
    const ask = () => {
      dryRunApp(yaml).then(
        (r) => { if (n === seq.current) setDry({ state: "ok", runtime: r.runtime, credential: r.credential }); },
        (e) => {
          const err = e as ApiError;
          if (n !== seq.current || err.status === 401) return;
          setDry(err.unreachable ? { state: "unreachable" } : { state: "refused", text: err.message });
        },
      );
    };
    if (mode === "once") {
      ask();
      return undefined;
    }
    const t = setTimeout(ask, DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [yaml, mode]);
  return dry;
}
