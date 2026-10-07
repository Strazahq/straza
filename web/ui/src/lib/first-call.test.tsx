import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useFirstCall } from "./first-call";
import { ApiError, type AuditRow, listAudit } from "./api";

vi.mock("./api", async (orig) => ({
  ...(await orig<typeof import("./api")>()),
  listAudit: vi.fn(),
}));

const SINCE = Date.parse("2026-09-12T10:00:00Z");

// record is one chain row as the audit list answers it: the CloudEvent is
// a JSON string, and the reader parses it.
const record = (o: { seq: number; at: string; app: string; tool?: string; effect?: string; user?: string; setName?: string }): AuditRow => ({
  seq: o.seq,
  username: o.user,
  ce: JSON.stringify({ time: o.at, data: { app: o.app, toolName: o.tool, effect: o.effect, setName: o.setName } }),
});

const hit = record({ seq: 41, at: "2026-09-12T10:01:00Z", app: "demo-tools", tool: "get-sum", effect: "allow", user: "alice" });

describe("the first-call watcher", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ now: SINCE });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const watch = () => renderHook(() => useFirstCall("demo-tools", true, SINCE));
  const settle = async (ms = 0) => {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  };

  it("watches until a call arrives, then names it", async () => {
    vi.mocked(listAudit).mockResolvedValueOnce([]).mockResolvedValueOnce([hit]);
    const { result } = watch();
    await settle();
    expect(listAudit).toHaveBeenCalledWith("type=straza.audit.mcp&q=demo-tools&order=desc&limit=25");
    expect(result.current.state).toBe("watching");
    await settle(5000);
    expect(result.current.state).toBe("hit");
    expect(result.current.rec).toEqual({ seq: 41, time: "2026-09-12T10:01:00Z", username: "alice", tool: "get-sum", effect: "allow", setName: undefined, ruleId: undefined });
  });

  it("skips a record from before the grant and another server's record", async () => {
    const older = record({ seq: 12, at: "2026-09-12T09:59:00Z", app: "demo-tools", tool: "get-sum" });
    const elsewhere = record({ seq: 13, at: "2026-09-12T10:02:00Z", app: "midpoint", tool: "get-sum" });
    vi.mocked(listAudit).mockResolvedValue([elsewhere, older]);
    const { result } = watch();
    await settle();
    expect(result.current.state).toBe("watching");
    await settle(5000);
    expect(result.current.state).toBe("watching");
  });

  it("stops watching after five minutes and asks no more", async () => {
    vi.mocked(listAudit).mockResolvedValue([]);
    const { result } = watch();
    await settle(5 * 60 * 1000);
    expect(result.current.state).toBe("stopped");
    const asked = vi.mocked(listAudit).mock.calls.length;
    await settle(30000);
    expect(vi.mocked(listAudit).mock.calls.length).toBe(asked);
  });

  it("turns itself off when this session cannot read the audit log", async () => {
    vi.mocked(listAudit).mockRejectedValue(new ApiError("forbidden", 403));
    const { result } = watch();
    await settle();
    expect(result.current.state).toBe("off");
    await settle(5000);
    expect(listAudit).toHaveBeenCalledTimes(1);
  });

  it("keeps watching through a read that failed for another reason", async () => {
    vi.mocked(listAudit).mockRejectedValueOnce(new ApiError("unreachable", 0, true)).mockResolvedValueOnce([hit]);
    const { result } = watch();
    await settle();
    expect(result.current.state).toBe("watching");
    await settle(5000);
    expect(result.current.state).toBe("hit");
  });

  it("is off while it is not enabled, and stops asking once it is gone", async () => {
    vi.mocked(listAudit).mockResolvedValue([]);
    const off = renderHook(() => useFirstCall("demo-tools", false, SINCE));
    await settle();
    expect(off.result.current.state).toBe("off");
    expect(listAudit).not.toHaveBeenCalled();
    const live = watch();
    await settle();
    live.unmount();
    await settle(20000);
    expect(listAudit).toHaveBeenCalledTimes(1);
  });
});
