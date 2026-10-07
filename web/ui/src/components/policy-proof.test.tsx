import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { PolicyProof } from "./policy-proof";
import { ApiError, listAudit } from "@/lib/api";
import { AUDIT_THIS_POLICY, REEVALUATE, SUBJECT_DECISIONS, WATCHING, WATCH_STOPPED, firstDecision, liveSince, liveSinceNoVersion } from "@/lib/policy-words";
import { readFailed } from "@/lib/say";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listAudit: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const NAME = "dev-tools-approvals";
const AT = "2026-09-13T11:42:00Z";
const SNAPSHOT = "7c1e9a2bdeadbeef";

const record = (time: string) => ({
  seq: 12,
  username: "joe",
  ce: JSON.stringify({
    type: "straza.audit.mcp",
    time,
    data: { effect: "approve", ruleId: "approve-demo-tools-write-file", setName: NAME, app: "demo-tools", toolName: "write-file" },
  }),
});

const mount = (snapshot?: string) => render(<PolicyProof name={NAME} snapshot={snapshot} at={AT} />);
const line = () => (document.querySelector("[data-proof]") as HTMLElement).textContent || "";
const watch = () => (document.querySelector("[data-watch]") as HTMLElement).getAttribute("data-watch");

describe("the proof banner", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(AT));
    vi.mocked(listAudit).mockResolvedValue([]);
  });
  afterEach(() => vi.useRealTimers());

  it("says since when it runs, which version, and that it is watching", () => {
    mount(SNAPSHOT);
    expect(line()).toContain(liveSince("11:42 UTC", "7c1e9a2b"));
    expect(line()).toContain(REEVALUATE);
    expect(line()).toContain(WATCHING);
    expect(watch()).toBe("watching");
  });

  it("names no version when the engine did not answer with one", () => {
    mount();
    expect(line()).toContain(liveSinceNoVersion("11:42 UTC"));
  });

  it("reads the first decision the policy makes", async () => {
    vi.mocked(listAudit).mockResolvedValue([record("2026-09-13T11:43:05Z")]);
    mount(SNAPSHOT);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(listAudit).toHaveBeenCalledWith("q=" + encodeURIComponent('"setName":"' + NAME + '"') + "&limit=1");
    expect(watch()).toBe("hit");
    expect(line()).toContain(firstDecision("11:43 UTC", "joe", "demo-tools / write-file", "needs approval"));
    expect(screen.getByRole("button", { name: AUDIT_THIS_POLICY })).toBeTruthy();
  });

  it("ignores a record older than the publish", async () => {
    vi.mocked(listAudit).mockResolvedValue([record("2026-09-13T11:40:00Z")]);
    mount(SNAPSHOT);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(watch()).toBe("watching");
  });

  it("stops watching after two minutes", async () => {
    mount(SNAPSHOT);
    await act(async () => { await vi.advanceTimersByTimeAsync(120000); });
    expect(watch()).toBe("stopped");
    expect(line()).toContain(WATCH_STOPPED);
  });

  it("stops and says so when the audit could not be read", async () => {
    const err = new ApiError("strazad did not answer", 0, true);
    vi.mocked(listAudit).mockRejectedValue(err);
    mount(SNAPSHOT);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(watch()).toBe("failed");
    expect(line()).toContain(readFailed(SUBJECT_DECISIONS, err));
  });
});
