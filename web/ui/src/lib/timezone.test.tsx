import { afterEach, describe, expect, it, vi } from "vitest";
import { setTimeZone, timeZone } from "./timezone";
import { absTime, relTimeText } from "./words";

const ISO = "2026-09-10T10:00:00Z";

describe("the time zone", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    setTimeZone("utc");
  });

  it("defaults to UTC and names it on the stamp", () => {
    expect(timeZone()).toBe("utc");
    expect(absTime(ISO)).toBe("2026-09-10 10:00:00 UTC");
  });

  it("switches every stamp to the local offset", () => {
    setTimeZone("local");
    expect(timeZone()).toBe("local");
    expect(absTime(ISO)).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [+-]\d{2}:\d{2}$/);
    expect(absTime(ISO)).not.toContain("UTC");
    expect(localStorage.getItem("straza.console.tz")).toBe("local");
  });

  it("renders a stamp older than a day as the absolute in the active zone", () => {
    expect(relTimeText("2026-01-01T00:00:00Z")).toBe("2026-01-01 00:00:00 UTC");
  });

  it("keeps the choice for the page when storage refuses the write", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("QuotaExceededError"); });
    expect(() => setTimeZone("local")).not.toThrow();
    expect(timeZone()).toBe("local");
  });
});
