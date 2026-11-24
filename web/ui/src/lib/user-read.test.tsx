import { describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { dataOf, useRead } from "./user-read";

const err = (message: string, status: number, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

describe("useRead", () => {
  it("stays off while the page does not make the read", () => {
    const read = vi.fn(async () => 1);
    const { result } = renderHook(() => useRead(false, read, "The thing", 0));
    expect(result.current).toEqual({ kind: "off" });
    expect(read).not.toHaveBeenCalled();
  });

  it("answers the data, and keeps it on screen while it reads again after a write", async () => {
    let n = 0;
    const read = vi.fn(async () => ++n);
    const { result, rerender } = renderHook(({ nonce }) => useRead(true, read, "The thing", nonce), { initialProps: { nonce: 0 } });
    await waitFor(() => expect(dataOf(result.current)).toBe(1));
    rerender({ nonce: 1 });
    expect(dataOf(result.current)).toBe(1);
    await waitFor(() => expect(dataOf(result.current)).toBe(2));
  });

  it("words a failure with its subject, and leaves a 401 to the session module", async () => {
    const { result } = renderHook(() => useRead(true, async () => { throw err("boom", 500); }, "The thing", 0));
    await waitFor(() => expect(result.current).toEqual({ kind: "failed", message: "The thing could not be read: boom. Reload to try again." }));
    const lost = renderHook(() => useRead(true, async () => { throw err("signed out", 401); }, "The thing", 0));
    await new Promise((r) => setTimeout(r, 0));
    expect(lost.result.current).toEqual({ kind: "loading" });
  });
});
