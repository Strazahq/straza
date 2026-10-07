import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { ApiError, get } from "./api";
import { adminAreas, adminServers, checkin, devicePoll, deviceStart, logout, onAuthLost, resume, token } from "./session";
import { useSession } from "./use-session";

type Call = { method: string; path: string; headers: Record<string, string>; body?: string };
type Answer = { status: number; body: unknown } | "down";

const ok = (body: unknown): Answer => ({ status: 200, body });
const refuse = (status: number, error: string): Answer => ({ status, body: { error } });

// stubFetch answers by "METHOD path" and records every call. A route that
// answers "down" makes fetch throw, the way an unreachable server does; an
// unknown route answers 404, which also keeps login discovery on the
// same-origin flow.
function stubFetch(routes: Record<string, (call: Call) => Answer>): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const call: Call = { method: init?.method || "GET", path: String(input), headers: (init?.headers as Record<string, string>) || {}, body: init?.body as string | undefined };
    calls.push(call);
    const route = routes[call.method + " " + call.path];
    const answer: Answer = route ? route(call) : refuse(404, "no route " + call.path);
    if (answer === "down") throw new TypeError("Failed to fetch");
    return { ok: answer.status < 400, status: answer.status, json: async () => answer.body } as Response;
  });
  return calls;
}

const KEY = "straza.session";
const storedRaw = () => sessionStorage.getItem(KEY);
const store = (tok = "tok-0") => sessionStorage.setItem(KEY, JSON.stringify({ token: tok, user: "alice", admin_grants: "full" }));
const good = { session_token: "tok-1", user: "alice", roles: ["straza-admin"], admin_grants: "full", expires_in: 300, session_id: "s1" };
const checkins = (calls: Call[]) => calls.filter((c) => c.path === "/v1/checkin").length;

// settle lets the fetch stubs, the timers due within ms and the state they
// set run to the end under fake timers.
const settle = (ms = 0) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

// jsdom will not let the real window.location be spied on, so the suite
// swaps the whole object and puts it back after each case.
const realLocation = Object.getOwnPropertyDescriptor(window, "location");
function stubLocation() {
  const replace = vi.fn();
  Object.defineProperty(window, "location", { configurable: true, value: { replace } });
  return replace;
}

beforeEach(() => {
  vi.useFakeTimers();
  sessionStorage.clear();
  vi.unstubAllGlobals();
  logout();
});
afterEach(() => {
  vi.useRealTimers();
  if (realLocation) Object.defineProperty(window, "location", realLocation);
});

describe("request", () => {
  it("sends the bearer from the module and a 401 drops the token before the error reaches the caller", async () => {
    const calls = stubFetch({
      "POST /v1/checkin": () => ok(good),
      "GET /v1/admin/apps": () => refuse(401, "session is no longer active"),
    });
    await checkin("id-token");
    expect(token()).toBe("tok-1");
    expect(storedRaw()).toBe('{"token":"tok-1","user":"alice","admin_grants":"full"}');
    const lost: { reason: string; token: string | null; stored: string | null }[] = [];
    const off = onAuthLost((reason) => lost.push({ reason, token: token(), stored: storedRaw() }));
    let caught: unknown = null;
    let atCatch: { token: string | null; stored: string | null } | null = null;
    try {
      await get("/v1/admin/apps");
    } catch (e) {
      caught = e;
      atCatch = { token: token(), stored: storedRaw() };
    }
    off();
    expect(calls[1].headers.Authorization).toBe("Bearer tok-1");
    expect(caught).toBeInstanceOf(ApiError);
    expect((caught as ApiError).status).toBe(401);
    expect((caught as ApiError).message).toBe("session is no longer active");
    expect(atCatch).toEqual({ token: null, stored: null });
    expect(lost).toEqual([{ reason: "session is no longer active", token: null, stored: null }]);
  });

  it("does not fire onAuthLost for a 401 when the tab had no session", async () => {
    stubFetch({ "GET /v1/admin/apps": () => refuse(401, "missing bearer") });
    const lost: string[] = [];
    const off = onAuthLost((reason) => lost.push(reason));
    await expect(get("/v1/admin/apps")).rejects.toMatchObject({ status: 401 });
    off();
    expect(lost).toEqual([]);
  });
});

describe("resume", () => {
  const cases: { name: string; answer: Answer; resolves: string | null | "rejects"; stored: string | null }[] = [
    { name: "a good token stores the pinned shape in field order", answer: ok(good), resolves: "tok-1", stored: '{"token":"tok-1","user":"alice","admin_grants":"full"}' },
    { name: "a refusal clears storage and resolves null", answer: refuse(401, "session token rejected"), resolves: null, stored: null },
    { name: "an unreachable server keeps the stored token and rejects", answer: "down", resolves: "rejects", stored: '{"token":"tok-0","user":"alice","admin_grants":"full"}' },
  ];
  it.each(cases)("$name", async (c) => {
    store();
    const calls = stubFetch({ "POST /v1/checkin": () => c.answer });
    if (c.resolves === "rejects") {
      await expect(resume()).rejects.toMatchObject({ unreachable: true });
    } else {
      const r = await resume();
      expect(r ? r.session_token : null).toBe(c.resolves);
    }
    expect(calls[0].headers.Authorization).toBeUndefined();
    expect(JSON.parse(calls[0].body as string)).toEqual({ session_token: "tok-0", harness: { name: "console", version: "1" }, attestation: { managed: false, hashes: {} } });
    expect(storedRaw()).toBe(c.stored);
  });

  it("resolves null without a request when nothing is stored", async () => {
    const calls = stubFetch({});
    expect(await resume()).toBeNull();
    expect(calls).toEqual([]);
  });
});

// A server admin's check-in: no admin_grants at all, and a count
// of the MCP servers whose admin role the session holds.
const erin = { session_token: "tok-1", user: "erin", roles: ["mcp-admin-finance-jira"], admin_servers: 1, expires_in: 300, session_id: "s2" };

describe("a session that administers MCP servers", () => {
  it("keeps the count beside the grants and opens no console area of its own", async () => {
    stubFetch({ "POST /v1/checkin": () => ok(erin) });
    await checkin("id-token");
    expect(adminServers()).toBe(1);
    expect(adminAreas()).toEqual({});
    expect(storedRaw()).toBe('{"token":"tok-1","user":"erin","admin_grants":"","admin_servers":1}');
    logout();
    expect(adminServers()).toBe(0);
  });

  it("leaves the count out of the mirror when the session administers none", async () => {
    stubFetch({ "POST /v1/checkin": () => ok(good) });
    await checkin("id-token");
    expect(adminServers()).toBe(0);
    expect(storedRaw()).toBe('{"token":"tok-1","user":"alice","admin_grants":"full"}');
  });

  it("stays in the console on resume instead of hopping to the self-service page", async () => {
    const replace = stubLocation();
    store();
    stubFetch({ "POST /v1/checkin": () => ok(erin) });
    const { result } = renderHook(() => useSession());
    await settle();
    expect(replace).not.toHaveBeenCalled();
    expect(result.current.state).toMatchObject({ kind: "signed-in", user: "erin", grants: "" });
  });
});

describe("the device flow", () => {
  it("deviceStart posts the form to the same-origin endpoint when idp.json is absent", async () => {
    const calls = stubFetch({ "POST /oidc/device_authorization": () => ok({ device_code: "d", user_code: "ABCD-EFGH", verification_uri: "/oidc/device", interval: 1, expires_in: 600 }) });
    const g = await deviceStart();
    expect(g.user_code).toBe("ABCD-EFGH");
    const c = calls.find((x) => x.path === "/oidc/device_authorization")!;
    expect(c.headers["Content-Type"]).toBe("application/x-www-form-urlencoded");
    expect(c.body).toBe("client_id=console&scope=openid");
    expect(c.headers.Authorization).toBeUndefined();
  });

  it("devicePoll returns an RFC 6749 error body as the answer instead of throwing", async () => {
    const calls = stubFetch({ "POST /oidc/token": () => ({ status: 400, body: { error: "authorization_pending" } }) });
    expect(await devicePoll("d")).toEqual({ error: "authorization_pending" });
    const c = calls.find((x) => x.path === "/oidc/token")!;
    expect(c.body).toBe("grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code&device_code=d&client_id=console");
  });

  it("names the identity provider when this browser cannot reach its discovery document", async () => {
    vi.resetModules(); // the discovered flow is cached per page load
    const fresh = await import("./session");
    stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "http://localhost:8480/realms/straza", client_id: "straza" }),
      "GET http://localhost:8480/realms/straza/.well-known/openid-configuration": () => "down",
    });
    await expect(fresh.deviceStart()).rejects.toThrow("This browser cannot reach your identity provider at http://localhost:8480/realms/straza. Open that address in this browser to check that it answers, then press Start again.");
  });

  it("the emergency sign-in posts same-origin with the console id and never reads idp.json", async () => {
    const calls = stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "https://idp.example", client_id: "straza-console" }),
      "POST /oidc/device_authorization": () => ok({ device_code: "d", user_code: "ABCD-EFGH", verification_uri: "/oidc/device", interval: 1, expires_in: 600 }),
      "POST /oidc/token": () => ({ status: 400, body: { error: "authorization_pending" } }),
    });
    expect((await deviceStart(true)).user_code).toBe("ABCD-EFGH");
    expect(await devicePoll("d", true)).toEqual({ error: "authorization_pending" });
    expect(calls.map((c) => c.path)).toEqual(["/oidc/device_authorization", "/oidc/token"]);
    expect(calls[0].body).toBe("client_id=console&scope=openid");
    expect(calls[1].body).toBe("grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code&device_code=d&client_id=console");
  });
});

describe("useSession", () => {
  const boots: { name: string; store: boolean; answer: Answer; state: object; stored: string | null }[] = [
    { name: "a stored token with admin grants signs in", store: true, answer: ok(good), state: { kind: "signed-in", user: "alice", grants: "full" }, stored: '{"token":"tok-1","user":"alice","admin_grants":"full"}' },
    { name: "an empty tab gives a fresh sign-in", store: false, answer: ok(good), state: { kind: "signed-out", reason: { kind: "none", detail: "" } }, stored: null },
    { name: "a refusal gives a fresh sign-in", store: true, answer: refuse(401, "session token rejected"), state: { kind: "signed-out", reason: { kind: "none", detail: "" } }, stored: null },
    { name: "an unreachable server says so and keeps the stored token", store: true, answer: "down", state: { kind: "signed-out", reason: { kind: "unreachable", detail: "" } }, stored: '{"token":"tok-0","user":"alice","admin_grants":"full"}' },
  ];
  it.each(boots)("$name", async (c) => {
    if (c.store) store();
    stubFetch({ "POST /v1/checkin": () => c.answer });
    const { result } = renderHook(() => useSession());
    expect(result.current.state).toEqual({ kind: "booting" });
    await settle();
    expect(result.current.state).toMatchObject(c.state);
    expect(storedRaw()).toBe(c.stored);
  });

  it("routes a session without admin grants to the self-service page and stays booting", async () => {
    const replace = stubLocation();
    store();
    stubFetch({ "POST /v1/checkin": () => ok({ ...good, admin_grants: "" }) });
    const { result } = renderHook(() => useSession());
    await settle();
    expect(replace).toHaveBeenCalledWith("/self-service/");
    expect(result.current.state).toEqual({ kind: "booting" });
    expect(storedRaw()).toBe('{"token":"tok-1","user":"alice","admin_grants":""}');
  });

  it("turns a lost session into signed-out with the server's sentence", async () => {
    store();
    stubFetch({
      "POST /v1/checkin": () => ok(good),
      "GET /v1/admin/apps": () => refuse(401, "session is no longer active"),
    });
    const { result } = renderHook(() => useSession());
    await settle();
    expect(result.current.state.kind).toBe("signed-in");
    await act(async () => { await get("/v1/admin/apps").catch(() => undefined); });
    expect(result.current.state).toEqual({ kind: "signed-out", reason: { kind: "lost", detail: "session is no longer active" } });
    expect(token()).toBeNull();
  });

  it("refreshes under sixty seconds and sends no checkin after signOut", async () => {
    store();
    const calls = stubFetch({
      "POST /v1/checkin": () => ok({ ...good, expires_in: 30 }),
      "POST /v1/session/revoke": () => ok({ session_id: "s1" }),
    });
    const { result } = renderHook(() => useSession());
    await settle();
    expect(result.current.state.kind).toBe("signed-in");
    expect(checkins(calls)).toBe(1);
    await settle(5000);
    expect(checkins(calls)).toBe(2);
    await act(async () => { await result.current.signOut(); });
    expect(result.current.state).toEqual({ kind: "signed-out", reason: { kind: "signed-out", detail: "" } });
    const revoke = calls.find((c) => c.path === "/v1/session/revoke")!;
    expect(revoke.headers.Authorization).toBe("Bearer tok-1");
    expect(token()).toBeNull();
    expect(storedRaw()).toBeNull();
    await settle(20000);
    expect(checkins(calls)).toBe(2);
  });

  it("re-learns the standing at a refresh, so a role granted after the sign-in shows up", async () => {
    store();
    let n = 0;
    const calls = stubFetch({
      "POST /v1/checkin": () => ok(n++ === 0 ? { ...good, expires_in: 30 } : { ...good, admin_grants: "", admin_servers: 1, expires_in: 300 }),
    });
    const { result } = renderHook(() => useSession({ adminOnly: false }));
    await settle();
    expect(result.current.state).toMatchObject({ kind: "signed-in", grants: "full", servers: 0 });
    await settle(5000);
    expect(checkins(calls)).toBe(2);
    expect(result.current.state).toMatchObject({ kind: "signed-in", grants: "", servers: 1 });
  });

  it("leaves the token alone while more than sixty seconds remain", async () => {
    store();
    const calls = stubFetch({ "POST /v1/checkin": () => ok(good) });
    renderHook(() => useSession());
    await settle();
    await settle(15000);
    expect(checkins(calls)).toBe(1);
  });

  const signOuts: { name: string; revoke: Answer; kind: string }[] = [
    { name: "a delivered revoke", revoke: ok({ session_id: "s1" }), kind: "signed-out" },
    { name: "a revoke the server answered 401", revoke: refuse(401, "session is no longer active"), kind: "signed-out" },
    { name: "a revoke that was not delivered", revoke: "down", kind: "unconfirmed" },
  ];
  it.each(signOuts)("$name signs out as $kind", async (c) => {
    store();
    stubFetch({ "POST /v1/checkin": () => ok(good), "POST /v1/session/revoke": () => c.revoke });
    const { result } = renderHook(() => useSession());
    await settle();
    await act(async () => { await result.current.signOut(); });
    expect(result.current.state).toEqual({ kind: "signed-out", reason: { kind: c.kind, detail: "" } });
    expect(token()).toBeNull();
    expect(storedRaw()).toBeNull();
  });

  it("signedIn takes the card's checkin response", async () => {
    stubFetch({});
    const { result } = renderHook(() => useSession());
    await settle();
    act(() => result.current.signedIn({ ...good, expires_in: 120 }));
    expect(result.current.state).toMatchObject({ kind: "signed-in", user: "alice", grants: "full", expiresAt: Date.now() + 120000 });
  });
});
