import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Live, readFollow, saveFollow } from "./overview-live";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { AuditRow } from "@/lib/api";
import { EMPTY_CHAIN } from "@/lib/audit-words";
import { FOLLOW, LIVE_OFF, LIVE_TITLE, newSince, seatLine } from "@/lib/config-words";

const record = (seq: number, effect: string): AuditRow =>
  ({ seq, ce: JSON.stringify({ type: "straza.audit.mcp", time: "2026-09-13T09:00:00Z", data: { app: "demo-tools", toolName: "get-env", effect } }), username: "joe" });

const ROWS = [record(12, "deny"), record(11, "allow")];

const mount = (props: Partial<Parameters<typeof Live>[0]>) =>
  render(<TooltipProvider><Live on={false} onChange={() => {}} rows={null} fresh={0} denied={false} {...props} /></TooltipProvider>);

afterEach(() => window.localStorage.clear());

describe("the Live panel", () => {
  it("is off by default and says what turning it on does", () => {
    mount({});
    expect(screen.getByText(LIVE_OFF)).toBeTruthy();
    expect(screen.getByRole("switch", { name: FOLLOW }).getAttribute("aria-checked")).toBe("false");
  });

  it("lands the newest record first, with its decision word and the pulse", () => {
    mount({ on: true, rows: ROWS, fresh: 2 });
    const rows = Array.from(document.querySelectorAll("[data-live]"));
    expect(rows.map((r) => r.getAttribute("data-live"))).toEqual(["12", "11"]);
    expect(rows[0].querySelector("[data-pulse]")).toBeTruthy();
    expect(rows[1].querySelector("[data-pulse]")).toBeNull();
    expect(screen.getByText("deny").getAttribute("data-tone")).toBe("danger");
    expect(screen.getByText(newSince(2))).toBeTruthy();
  });

  it("hands the switch back to its owner", async () => {
    const onChange = vi.fn();
    mount({ onChange });
    await userEvent.click(screen.getByRole("switch", { name: FOLLOW }));
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("says the chain is empty, or that the seat may not read it", () => {
    mount({ on: true, rows: [] });
    expect(screen.getByText(EMPTY_CHAIN)).toBeTruthy();
    mount({ on: true, rows: [], denied: true });
    expect(screen.getByText(seatLine(LIVE_TITLE, "audit:read"))).toBeTruthy();
  });

  it("remembers the choice in this browser, and reads as off where storage is refused", () => {
    expect(readFollow()).toBe(false);
    saveFollow(true);
    expect(window.localStorage.getItem("straza.overview.live")).toBe("on");
    expect(readFollow()).toBe(true);

    const store = window.localStorage;
    const blocked = { getItem() { throw new Error("blocked"); }, setItem() { throw new Error("blocked"); } };
    Object.defineProperty(window, "localStorage", { value: blocked, configurable: true });
    expect(readFollow()).toBe(false);
    expect(() => saveFollow(true)).not.toThrow();
    Object.defineProperty(window, "localStorage", { value: store, configurable: true });
  });
});
