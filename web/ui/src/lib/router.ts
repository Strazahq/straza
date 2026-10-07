// The router: a typed hook over the History API with no router dependency.
// The base path comes from Vite, the area is the first segment under it,
// and the segments after it belong to the area's screen (a server's id and
// its tab, or "new" for a wizard). The module holds the location so any
// component can navigate without a prop chain. Every route change retitles
// the document and moves focus to the page's h1.
import * as React from "react";
import { type Route, type RouteKey, landing, routeByKey, routeByPath } from "./routes";

export type Location = { kind: "route"; key: RouteKey; rest: string[] } | { kind: "not-found"; path: string };

// BASE is the path the app is served under, with its trailing slash.
export const BASE: string = (import.meta.env.BASE_URL || "/").replace(/\/?$/, "/");

const subs = new Set<(l: Location) => void>();
let current: Location | null = null;

// pathFor is the address of an area, base included, with the screen's own
// segments after it when given.
export function pathFor(key: RouteKey, rest: string[] = []): string {
  const tail = rest.filter(Boolean).map(encodeURIComponent);
  return BASE + routeByKey(key).path + (tail.length ? "/" + tail.join("/") : "");
}

// resolve maps a pathname onto a location. The base alone is the landing
// redirect, so it reads as the landing route.
export function resolve(pathname: string): Location {
  const bare = pathname.replace(/\/+$/, "") + "/";
  if (bare === BASE) return { kind: "route", key: landing.key, rest: [] };
  if (!pathname.startsWith(BASE)) return { kind: "not-found", path: pathname };
  const segments = pathname.slice(BASE.length).split("/").filter(Boolean).map(decodeURIComponent);
  const route = routeByPath(segments[0] || "");
  return route ? { kind: "route", key: route.key, rest: segments.slice(1) } : { kind: "not-found", path: pathname };
}

// readLocation resolves the window's address and replaces the bare base
// with the landing address in history, so a reload lands on the same page.
function readLocation(): Location {
  const l = resolve(window.location.pathname);
  if (l.kind === "route" && window.location.pathname.replace(/\/+$/, "") + "/" === BASE) {
    window.history.replaceState(null, "", pathFor(l.key));
  }
  return l;
}

function same(a: Location, b: Location): boolean {
  if (a.kind !== b.kind) return false;
  if (a.kind === "not-found") return a.path === (b as { path: string }).path;
  const o = b as { key: RouteKey; rest: string[] };
  return a.key === o.key && a.rest.join("/") === o.rest.join("/");
}

// set tells every subscriber inside a transition, so a screen that loads
// its code on first use keeps the old screen up until it is ready, and the
// focus move below finds the new screen's title.
function set(next: Location) {
  if (current && same(current, next)) return;
  current = next;
  React.startTransition(() => {
    for (const f of subs) f(next);
  });
}

// LeaveGuard is what a page with unsaved work registers: it is asked
// before a navigation away from the current location and calls go when
// the departure may happen, or never, when the person chose to stay.
export type LeaveGuard = (next: Location, go: () => void) => void;
let guard: LeaveGuard | null = null;

// setLeaveGuard registers the guard of the page on screen, or clears it.
// One guard at a time: a page sets it while it holds unsaved work and
// clears it on unmount. The back button is not guarded: the history has
// moved by the time the page hears of it.
export function setLeaveGuard(g: LeaveGuard | null) {
  guard = g;
}

// navigate pushes the area's address, with the screen's segments when
// given, and tells every subscriber. replace swaps the current entry
// instead of pushing, for a tab change that should not grow the back stack.
// A registered guard is asked first when the location changes.
export function navigate(key: RouteKey, rest: string[] = [], replace = false) {
  const next: Location = { kind: "route", key, rest: rest.filter(Boolean) };
  const go = () => {
    const path = pathFor(key, rest);
    if (window.location.pathname !== path) {
      if (replace) window.history.replaceState(null, "", path);
      else window.history.pushState(null, "", path);
    }
    set(next);
  };
  if (guard && current && !same(current, next)) {
    guard(next, go);
    return;
  }
  go();
}

// titleOf is the document title's page part.
export function titleOf(l: Location): string {
  return l.kind === "route" ? routeByKey(l.key).label : "Page not found";
}

// routeOf is the table row of a location, or null off the table.
export function routeOf(l: Location): Route | null {
  return l.kind === "route" ? routeByKey(l.key) : null;
}

// useRouter returns the current location and the navigate function, and
// owns the popstate listener, the document title and the focus move.
export function useRouter(): { location: Location; navigate: typeof navigate } {
  // The address is read at mount, not at import, so the root can mount more
  // than once in one page with the address it finds each time.
  const [location, setLocation] = React.useState<Location>(() => {
    current = readLocation();
    return current;
  });
  React.useEffect(() => {
    subs.add(setLocation);
    const onPop = () => set(readLocation());
    window.addEventListener("popstate", onPop);
    if (current) setLocation(current);
    return () => {
      subs.delete(setLocation);
      window.removeEventListener("popstate", onPop);
    };
  }, []);
  // A tab change under the same screen keeps focus where it is; a new
  // screen moves it to the title.
  const screenKey = location.kind === "route" ? location.key + "/" + (location.rest[0] || "") : location.path;
  React.useEffect(() => {
    document.title = "Straza · " + titleOf(location);
    const h1 = document.querySelector("main h1");
    if (h1 instanceof HTMLElement) h1.focus();
  }, [screenKey]); // eslint-disable-line react-hooks/exhaustive-deps
  return { location, navigate };
}

// isPlainClick tells a left click without modifiers from one the browser
// should keep, such as a middle click or a Ctrl click opening a new tab.
export function isPlainClick(e: React.MouseEvent): boolean {
  return e.button === 0 && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey;
}
