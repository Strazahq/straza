import { afterEach, describe, expect, it, vi } from "vitest";
import { type Location, navigate, setLeaveGuard, useRouter } from "./router";
import { renderHook } from "@testing-library/react";

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
