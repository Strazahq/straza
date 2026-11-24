// The self-service page's router: four tabs in the address under
// /self-service/, with no area table of its own, and one request under the
// Requests tab at requests/<id>. Same shape as the console's router, a typed
// hook over the History API, so a reload lands on the tab and the request.
import * as React from "react";

export type SelfTab = "home" | "requests" | "credentials" | "devices";

export const BASE = "/self-service/";

export const TAB_KEYS: SelfTab[] = ["home", "requests", "credentials", "devices"];

// OLD maps the address of a renamed tab onto the tab, so a sign-in that
// returns there and an old link still land on it.
const OLD: Record<string, SelfTab> = { browser: "devices" };

// subs are told each time navigate moves the address, and each one reads
// what it follows from the address.
const subs = new Set<() => void>();

function segments(pathname: string): string[] {
  const rest = pathname.startsWith(BASE) ? pathname.slice(BASE.length) : "";
  return rest.split("/").filter(Boolean);
}

function segment(pathname: string): string {
  return segments(pathname)[0] || "";
}

// resolve maps a pathname onto a tab: a tab's own address, the old address
// of a renamed tab, and Home for anything else.
export function resolve(pathname: string): SelfTab {
  const first = segment(pathname);
  if ((TAB_KEYS as string[]).includes(first)) return first as SelfTab;
  return OLD[first] || "home";
}

// requestOf is the request a Requests address names, or "" for none. A
// name that does not decode names nothing.
export function requestOf(pathname: string): string {
  const [first, id] = segments(pathname);
  if (first !== "requests" || !id) return "";
  try {
    return decodeURIComponent(id);
  } catch {
    return "";
  }
}

// pathFor is the address of a tab, and with id of one request on the
// Requests tab. Home is the page's own address.
export function pathFor(tab: SelfTab, id = ""): string {
  if (tab === "home") return BASE;
  return BASE + tab + (tab === "requests" && id ? "/" + encodeURIComponent(id) : "");
}

function current(): SelfTab {
  return resolve(window.location.pathname);
}

// settle replaces the old address of a renamed tab with its new one in the
// address bar, keeping the query and the hash.
function settle(): void {
  const tab = OLD[segment(window.location.pathname)];
  if (tab) window.history.replaceState(null, "", pathFor(tab) + window.location.search + window.location.hash);
}

// navigate moves to a tab, and with id to one request on the Requests tab;
// replace keeps the tab switch out of the back stack, the way the console's
// tabs do.
export function navigate(tab: SelfTab, replace = true, id = ""): void {
  const path = pathFor(tab, id);
  if (window.location.pathname !== path) {
    if (replace) window.history.replaceState(null, "", path);
    else window.history.pushState(null, "", path);
  }
  for (const f of subs) f();
}

// useSelfRoute subscribes a component to the tab in the address.
export function useSelfRoute(): SelfTab {
  const [tab, setTab] = React.useState<SelfTab>(() => current());
  React.useEffect(() => {
    settle();
    const on = () => setTab(current());
    subs.add(on);
    const onPop = () => {
      settle();
      on();
    };
    window.addEventListener("popstate", onPop);
    return () => {
      subs.delete(on);
      window.removeEventListener("popstate", onPop);
    };
  }, []);
  return tab;
}

// useRequestID subscribes a component to the request the address names,
// "" for none.
export function useRequestID(): string {
  const [id, setID] = React.useState(() => requestOf(window.location.pathname));
  React.useEffect(() => {
    const on = () => setID(requestOf(window.location.pathname));
    subs.add(on);
    window.addEventListener("popstate", on);
    return () => {
      subs.delete(on);
      window.removeEventListener("popstate", on);
    };
  }, []);
  return id;
}
