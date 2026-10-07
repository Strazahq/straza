// The zone every timestamp in the console is rendered in, stated and
// switchable in one place. UTC is the default: it is the zone the audit
// chain, the CLI and every exported CSV speak, so it is the one that
// correlates. The zone is module state with a subscriber set rather than
// React context: absTime is a plain function called from render sites, and
// one subscriber at the shell root repaints every stamp. localStorage, not
// sessionStorage, because this is a display preference an operator sets
// once; it holds no session state.
import * as React from "react";

export type Zone = "utc" | "local";

const KEY = "straza.console.tz";
const subs = new Set<(z: Zone) => void>();
let zone: Zone = readZone();

function readZone(): Zone {
  try {
    return window.localStorage.getItem(KEY) === "local" ? "local" : "utc";
  } catch {
    return "utc";
  }
}

// timeZone is the active zone.
export function timeZone(): Zone {
  return zone;
}

// setTimeZone stores the zone and tells every subscriber; a storage
// failure keeps the choice for this page.
export function setTimeZone(z: Zone) {
  zone = z === "local" ? "local" : "utc";
  try {
    window.localStorage.setItem(KEY, zone);
  } catch {
    // Private mode: the choice lasts this page only.
  }
  for (const f of subs) f(zone);
}

// useTimeZone returns the active zone and re-renders the caller when it
// changes.
export function useTimeZone(): Zone {
  const [z, set] = React.useState<Zone>(zone);
  React.useEffect(() => {
    subs.add(set);
    set(zone);
    return () => {
      subs.delete(set);
    };
  }, []);
  return z;
}
