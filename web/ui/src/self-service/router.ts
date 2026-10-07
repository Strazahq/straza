// The self-service page's router: three tabs in the address under
// /self-service/, with no area table of its own. Same shape as the console's
// router, a typed hook over the History API, so a reload lands on the tab.
import * as React from "react";

export type SelfTab = "requests" | "credentials" | "browser";

export const BASE = "/self-service/";

export const TAB_KEYS: SelfTab[] = ["requests", "credentials", "browser"];

const subs = new Set<(t: SelfTab) => void>();

// resolve maps a pathname onto a tab; anything unknown is the first tab.
export function resolve(pathname: string): SelfTab {
  const rest = pathname.startsWith(BASE) ? pathname.slice(BASE.length) : "";
  const first = rest.split("/").filter(Boolean)[0] || "";
  return (TAB_KEYS as string[]).includes(first) ? (first as SelfTab) : "requests";
}

// pathFor is the address of a tab.
export function pathFor(tab: SelfTab): string {
  return BASE + tab;
}

function current(): SelfTab {
  return resolve(window.location.pathname);
}

// navigate moves to a tab; replace keeps the tab switch out of the back
// stack, the way the console's tabs do.
export function navigate(tab: SelfTab, replace = true): void {
  const path = pathFor(tab);
  if (window.location.pathname !== path) {
    if (replace) window.history.replaceState(null, "", path);
    else window.history.pushState(null, "", path);
  }
  for (const f of subs) f(tab);
}

// useSelfRoute subscribes a component to the tab in the address.
export function useSelfRoute(): SelfTab {
  const [tab, setTab] = React.useState<SelfTab>(() => current());
  React.useEffect(() => {
    subs.add(setTab);
    const onPop = () => setTab(current());
    window.addEventListener("popstate", onPop);
    return () => {
      subs.delete(setTab);
      window.removeEventListener("popstate", onPop);
    };
  }, []);
  return tab;
}
