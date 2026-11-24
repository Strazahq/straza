import { afterEach, describe, expect, it, vi } from "vitest";
import { type Location, navigate, pathFor, setLeaveGuard, useRouter } from "./router";
import { renderHook, waitFor } from "@testing-library/react";

// The leave guard: a page holding unsaved work is
// asked before the console leaves it, and a tab change under the same
// object passes without a word.

const at = (path: string) => window.history.replaceState(null, "", path);

describe("the leave guard", () => {
  afterEach(() => setLeaveGuard(null));

  it("asks the guard before a departure and goes when the guard says so", () => {
    at("/policies/dev-guardrails/rules");
    renderHook(() => useRouter());
    const asked: Location[] = [];
    let release: (() => void) | null = null;
    setLeaveGuard((next, go) => { asked.push(next); release = go; });
    navigate("audit");
    expect(window.location.pathname).toBe("/policies/dev-guardrails/rules");
    expect(asked).toEqual([{ kind: "route", key: "audit", rest: [] }]);
    expect(release).not.toBeNull();
    (release as unknown as () => void)();
    expect(window.location.pathname).toBe("/audit");
  });

  it("hands over only on a departure that happens: never when the person stays, at the move when the guard lets it go, at once with no guard", () => {
    at("/policies/dev-guardrails/rules");
    renderHook(() => useRouter());
    const seen: string[] = [];
    let release: (() => void) | null = null;
    setLeaveGuard((_next, go) => { release = go; });
    navigate("audit", [], false, () => seen.push("handed over at " + window.location.pathname));
    expect(seen).toEqual([]);
    (release as unknown as () => void)();
    expect(seen).toEqual(["handed over at /policies/dev-guardrails/rules"]);
    expect(window.location.pathname).toBe("/audit");
    setLeaveGuard(null);
    navigate("users", [], false, () => seen.push("handed over at " + window.location.pathname));
    expect(seen[1]).toBe("handed over at /audit");
    expect(window.location.pathname).toBe("/users");
  });

  it("is not asked for the location the console is already at, and goes at once with no guard", () => {
    at("/policies/dev-guardrails/rules");
    renderHook(() => useRouter());
    const guard = vi.fn();
    setLeaveGuard(guard);
    navigate("policies", ["dev-guardrails", "rules"], true);
    expect(guard).not.toHaveBeenCalled();
    setLeaveGuard(null);
    navigate("users");
    expect(window.location.pathname).toBe("/users");
  });
});

describe("the focus move", () => {
  // page puts a title and a tab on the page, as a screen with a row of tabs has.
  function page() {
    document.body.innerHTML = '<main><h1 tabindex="-1">Title</h1><button id="tab">Tab</button></main>';
    return { title: document.querySelector("h1") as HTMLElement, tab: document.getElementById("tab") as HTMLElement };
  }
  // settled gives a focus move that is still on its way the time to land.
  const settled = () => new Promise((done) => setTimeout(done, 30));
  afterEach(() => { document.body.innerHTML = ""; });

  it("leaves focus on the tab when a tab change swaps the address, and moves it to the title on a new screen", async () => {
    at(pathFor("settings", ["tokens"]));
    const { title, tab } = page();
    const { result } = renderHook(() => useRouter());
    await waitFor(() => expect(document.activeElement).toBe(title));
    tab.focus();
    navigate("settings", ["keys"], true);
    await waitFor(() => expect(result.current.location).toMatchObject({ key: "settings", rest: ["keys"] }));
    await settled();
    expect(document.activeElement).toBe(tab);
    navigate("users");
    await waitFor(() => expect(document.activeElement).toBe(title));
  });

  it("moves focus to the title when the back button leaves a screen after a tab change", async () => {
    at(pathFor("users"));
    const { title, tab } = page();
    const { result } = renderHook(() => useRouter());
    await waitFor(() => expect(document.activeElement).toBe(title));
    tab.focus();
    navigate("settings", ["tokens"]);
    await waitFor(() => expect(document.activeElement).toBe(title));
    tab.focus();
    navigate("settings", ["keys"], true);
    await waitFor(() => expect(result.current.location).toMatchObject({ key: "settings", rest: ["keys"] }));
    await settled();
    expect(document.activeElement).toBe(tab);
    at(pathFor("users"));
    window.dispatchEvent(new PopStateEvent("popstate"));
    await waitFor(() => expect(document.activeElement).toBe(title));
  });
});
