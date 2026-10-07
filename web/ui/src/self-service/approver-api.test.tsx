import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApproverError, bindDevice, decide, pending, selfUnenroll } from "./approver-api";

type Call = { method: string; path: string; bearer: string; body: unknown };

// A strazad stub that answers by path and records every call with its
// bearer, so the suite pins which token each call carried.
function server(answers: Record<string, (c: Call) => { status: number; body?: unknown }>) {
  const calls: Call[] = [];
  const fetchMock = vi.fn(async (path: string, init: RequestInit) => {
    const headers = (init.headers || {}) as Record<string, string>;
    const c: Call = { method: init.method || "GET", path, bearer: (headers.Authorization || "").replace("Bearer ", ""), body: init.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(c);
    const a = answers[c.method + " " + path.split("?")[0]];
    if (!a) return new Response(JSON.stringify({ error: "no route " + c.method + " " + path }), { status: 404 });
    const r = a(c);
    return new Response(r.body === undefined ? null : JSON.stringify(r.body), { status: r.status, headers: { "Content-Type": "application/json" } });
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

function device(overrides: Partial<{ onToken: (t: string, e: number) => Promise<void>; onRevoked: () => Promise<void> }> = {}) {
  const d = {
    id: "dev_1",
    token: "tok_old",
    sign: vi.fn(async (m: string) => "sig(" + m + ")"),
    onToken: vi.fn(async () => undefined),
    onRevoked: vi.fn(async () => undefined),
    ...overrides,
  };
  bindDevice(d);
  return d;
}

beforeEach(() => bindDevice(null));
afterEach(() => vi.unstubAllGlobals());

describe("the device-token lane", () => {
  it("refuses to call without an enrolment", async () => {
    server({});
    await expect(pending("decidable")).rejects.toMatchObject({ notEnrolled: true });
  });

  it("carries the device token and answers the rows", async () => {
    const calls = server({ "GET /v1/approver/pending": () => ({ status: 200, body: [{ id: "apr_1" }] }) });
    device();
    const rows = await pending("decidable");
    expect(rows).toEqual([{ id: "apr_1" }]);
    expect(calls[0].bearer).toBe("tok_old");
    expect(calls[0].path).toContain("scope=decidable");
  });

  it("refreshes an expired token by signing the challenge, keeps the key and retries once", async () => {
    let expired = true;
    const calls = server({
      "GET /v1/approver/pending": (c) => (expired && c.bearer === "tok_old" ? { status: 401, body: { code: "token_expired", error: "expired" } } : { status: 200, body: [] }),
      "POST /v1/approver/refresh/challenge": () => ({ status: 200, body: { challenge: "chal_r" } }),
      "POST /v1/approver/refresh": (c) => { expired = false; expect(c.body).toEqual({ approver_device_id: "dev_1", challenge: "chal_r", signature: "sig(refresh\ndev_1\nchal_r)" }); return { status: 200, body: { device_token: "tok_new", expires_in: 60 } }; },
    });
    const d = device();
    await pending("mine");
    expect(d.sign).toHaveBeenCalledWith("refresh\ndev_1\nchal_r");
    expect(d.onToken).toHaveBeenCalledWith("tok_new", 60);
    expect(d.onRevoked).not.toHaveBeenCalled();
    expect(calls.map((c) => c.method + " " + c.path.split("?")[0] + " " + c.bearer)).toEqual([
      "GET /v1/approver/pending tok_old",
      "POST /v1/approver/refresh/challenge ",
      "POST /v1/approver/refresh ",
      "GET /v1/approver/pending tok_new",
    ]);
  });

  it("proves an unverifiable token with the key: a device the server no longer knows is wiped and offered again", async () => {
    const calls = server({
      "GET /v1/approver/pending": () => ({ status: 401, body: { code: "token_invalid", error: "approver token rejected" } }),
      "POST /v1/approver/refresh/challenge": () => ({ status: 404, body: { error: "no such approver device" } }),
    });
    const d = device();
    await expect(pending("decidable")).rejects.toMatchObject({ revoked: true, status: 404 });
    expect(d.onRevoked).toHaveBeenCalledTimes(1);
    expect(calls.map((c) => c.method + " " + c.path.split("?")[0])).toEqual(["GET /v1/approver/pending", "POST /v1/approver/refresh/challenge"]);
  });

  it("proves an unverifiable token with the key: a known device gets a fresh token and the call runs", async () => {
    const calls = server({
      "GET /v1/approver/pending": (c) => (c.bearer === "tok_old" ? { status: 401, body: { code: "token_invalid", error: "approver token rejected" } } : { status: 200, body: [] }),
      "POST /v1/approver/refresh/challenge": () => ({ status: 200, body: { challenge: "chal_r" } }),
      "POST /v1/approver/refresh": () => ({ status: 200, body: { device_token: "tok_new", expires_in: 60 } }),
    });
    const d = device();
    await pending("mine");
    expect(d.onToken).toHaveBeenCalledWith("tok_new", 60);
    expect(d.onRevoked).not.toHaveBeenCalled();
    expect(calls[calls.length - 1].bearer).toBe("tok_new");
  });

  it("wipes the credential on device_revoked and says so", async () => {
    server({ "POST /v1/approver/decide": () => ({ status: 401, body: { code: "device_revoked", error: "revoked" } }) });
    const d = device();
    await expect(decide({ request_id: "apr_1", verdict: "approve", challenge: "c", signature: "s", ts: 1 })).rejects.toMatchObject({ revoked: true, code: "device_revoked" });
    expect(d.onRevoked).toHaveBeenCalledTimes(1);
  });

  it("hands every other refusal to the caller with its status and code", async () => {
    server({ "POST /v1/approver/decide": () => ({ status: 409, body: { error: "already decided", code: "decided", state: "approved" } }) });
    device();
    const err = await decide({ request_id: "apr_1", verdict: "approve", challenge: "c", signature: "s", ts: 1 }).catch((e) => e as ApproverError);
    expect(err).toBeInstanceOf(ApproverError);
    expect((err as ApproverError).status).toBe(409);
    expect((err as ApproverError).code).toBe("decided");
    expect((err as ApproverError).message).toBe("already decided");
  });

  it("reads an unreachable server as unknown, never as an empty list", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new TypeError("network"); }));
    device();
    await expect(pending("decidable")).rejects.toMatchObject({ unreachable: true });
  });

  it("retires its own row best effort: 204 is gone, a dead token is gone, a failure is not", async () => {
    server({ "DELETE /v1/approver/enrollment": () => ({ status: 204 }) });
    device();
    expect(await selfUnenroll()).toBe(true);
    server({ "DELETE /v1/approver/enrollment": () => ({ status: 401, body: { code: "device_revoked" } }) });
    expect(await selfUnenroll()).toBe(true);
    server({ "DELETE /v1/approver/enrollment": () => ({ status: 500, body: { error: "no" } }) });
    expect(await selfUnenroll()).toBe(false);
    bindDevice(null);
    expect(await selfUnenroll()).toBe(false);
  });
});
