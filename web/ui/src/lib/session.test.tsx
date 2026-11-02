import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { ApiError, get } from "./api";
import { NO_ANSWER, NO_STORAGE, PLAIN_HTTP, SIGNIN_PATH, adminAreas, adminServers, beginSignIn, checkin, finishSignIn, logout, onAuthLost, resume, stashProblem, takeResume, takeSignInProblem, token } from "./session";
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
  vi.restoreAllMocks();
  if (realLocation) Object.defineProperty(window, "location", realLocation);
  // A finished sign-in cleans the real address to the sign-in path.
  window.history.replaceState(null, "", "/");
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

// signInLocation stands in for the address bar of a tab on the console:
// assign is the departure to the identity provider.
function signInLocation(pathname = "/console/policies", search = "") {
  const assign = vi.fn();
  const replace = vi.fn();
  Object.defineProperty(window, "location", { configurable: true, value: { assign, replace, origin: "https://straza.example.com", pathname, search } });
  return { assign, replace };
}

const PENDING = "straza.signin";
const pendingOf = () => JSON.parse(sessionStorage.getItem(PENDING) || "null") as { state: string; verifier: string; nonce: string; back: string; started: number; sheet?: string } | null;
const b64 = (o: unknown) => btoa(JSON.stringify(o)).replace(/=+$/, "");
const idTokenWith = (nonce: string) => "h." + b64({ nonce, sub: "alice" }) + ".sig";

// leaveAndAnswer starts a sign-in, then stands the tab at the answer the
// identity provider would send back, with params over the pending state.
async function leaveAndAnswer(params: Record<string, string> = {}, opts: { sheet?: string; back?: string } = {}) {
  await beginSignIn({ back: opts.back || "/console/policies", sheet: opts.sheet });
  const p = pendingOf()!;
  const q = new URLSearchParams({ code: "c-1", state: p.state, ...params });
  signInLocation(SIGNIN_PATH, "?" + q.toString());
  return p;
}

describe("the browser sign-in", () => {
  it("leaves for the server's own page with PKCE S256, the console id and the one return address when idp.json is absent", async () => {
    stubFetch({});
    const { assign } = signInLocation();
    await beginSignIn({ back: "/console/policies" });
    const to = new URL(assign.mock.calls[0][0], "https://straza.example.com");
    const p = pendingOf()!;
    expect(to.pathname).toBe("/oidc/authorize");
    const q = to.searchParams;
    expect([q.get("response_type"), q.get("client_id"), q.get("redirect_uri"), q.get("scope"), q.get("code_challenge_method")]).toEqual(["code", "console", "https://straza.example.com/console/signin", "openid profile", "S256"]);
    expect([q.get("state"), q.get("nonce"), q.get("prompt")]).toEqual([p.state, p.nonce, null]);
    const sum = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(p.verifier)));
    expect(q.get("code_challenge")).toBe(btoa(String.fromCharCode(...sum)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""));
    expect(p.back).toBe("/console/policies");
  });

  it("leaves for the provider the server names, with the server's client id", async () => {
    vi.resetModules(); // the discovered flow is cached per page load
    const fresh = await import("./session");
    stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "https://sso.example.com/realms/straza", client_id: "straza" }),
      "GET https://sso.example.com/realms/straza/.well-known/openid-configuration": () => ok({ authorization_endpoint: "https://sso.example.com/auth", token_endpoint: "https://sso.example.com/token" }),
    });
    const { assign } = signInLocation();
    expect(await fresh.signInHost()).toBe("sso.example.com");
    await fresh.beginSignIn();
    const to = new URL(assign.mock.calls[0][0]);
    expect(to.origin + to.pathname).toBe("https://sso.example.com/auth");
    expect(to.searchParams.get("client_id")).toBe("straza");
  });

  it("the emergency sign-in goes to the server's own page and never reads idp.json", async () => {
    const calls = stubFetch({ "GET /.well-known/straza/idp.json": () => ok({ issuer: "https://sso.example.com", client_id: "straza" }) });
    const { assign } = signInLocation();
    await beginSignIn({ emergency: true });
    expect(calls).toEqual([]);
    expect(String(assign.mock.calls[0][0])).toMatch(/^\/oidc\/authorize\?.*client_id=console/);
  });

  it("names the identity provider when this browser cannot reach its discovery document", async () => {
    vi.resetModules();
    const fresh = await import("./session");
    stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "http://localhost:8480/realms/straza", client_id: "straza" }),
      "GET http://localhost:8480/realms/straza/.well-known/openid-configuration": () => "down",
    });
    signInLocation();
    await expect(fresh.beginSignIn()).rejects.toThrow("This browser cannot reach your identity provider at http://localhost:8480/realms/straza. Open that address in this browser to check that it answers, then press Sign in again.");
  });

  it("refuses to leave from a page without Web Crypto, as plain HTTP from a network address is", async () => {
    vi.stubGlobal("crypto", { getRandomValues: (a: Uint8Array) => a });
    const { assign } = signInLocation();
    await expect(beginSignIn()).rejects.toThrow(PLAIN_HTTP);
    expect(assign).not.toHaveBeenCalled();
  });

  it("does not leave when the tab cannot keep the sign-in record", async () => {
    stubFetch({});
    const { assign } = signInLocation();
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("blocked"); });
    await expect(beginSignIn()).rejects.toThrow(NO_STORAGE);
    expect(assign).not.toHaveBeenCalled();
  });

  it("gives a second call with the cleaned address the first call's outcome, and checks in once", async () => {
    let p = { nonce: "" };
    const calls = stubFetch({ "POST /oidc/token": () => ok({ id_token: idTokenWith(p.nonce) }), "POST /v1/checkin": () => ok(good) });
    signInLocation();
    p = await leaveAndAnswer();
    const first = finishSignIn(window.location.search);
    const second = finishSignIn("");
    expect(await second).toEqual(await first);
    expect(checkins(calls)).toBe(1);
  });

  it("finishes: cleans the address, redeems the code with the verifier, checks the nonce and checks in", async () => {
    let p = { nonce: "" };
    const calls = stubFetch({
      "POST /oidc/token": () => ok({ id_token: idTokenWith(p.nonce) }),
      "POST /v1/checkin": () => ok(good),
    });
    signInLocation();
    const replaceState = vi.spyOn(window.history, "replaceState");
    p = await leaveAndAnswer();
    const done = await finishSignIn(window.location.search);
    expect(done).toEqual({ resp: good, back: "/console/policies" });
    expect(replaceState).toHaveBeenCalledWith(null, "", SIGNIN_PATH);
    expect(pendingOf()).toBeNull();
    const redeem = new URLSearchParams(calls.find((c) => c.path === "/oidc/token")!.body);
    expect(Object.fromEntries(redeem)).toEqual({ grant_type: "authorization_code", code: "c-1", redirect_uri: "https://straza.example.com/console/signin", client_id: "console", code_verifier: (p as { verifier?: string }).verifier });
    expect(JSON.parse(calls.find((c) => c.path === "/v1/checkin")!.body!).id_token).toBe(idTokenWith(p.nonce));
    expect(token()).toBe("tok-1");
  });

  const refusals: { name: string; params?: Record<string, string>; token?: Answer; age?: number; nonce?: string; want: string }[] = [
    { name: "an answer to another tab's sign-in", params: { state: "someone-else" }, want: "This sign-in answer was not started in this tab, or it is more than 10 minutes old, so Straza did not use it. Press Sign in to start again." },
    { name: "an answer more than ten minutes late", age: 10 * 60 * 1000 + 1, want: "This sign-in answer was not started in this tab" },
    { name: "a token for another nonce", nonce: "another", want: "This sign-in answer was not started in this tab" },
    { name: "the provider's refusal", params: { error: "access_denied", error_description: "The user cancelled the sign-in" }, want: "Your identity provider did not sign you in. It said: The user cancelled the sign-in. Press Sign in to try again, or ask your administrator whether your account may use Straza." },
    { name: "a spent code", token: { status: 400, body: { error: "invalid_grant", error_description: "the code is unknown" } }, want: "Your identity provider says the sign-in answer expired or was already used. Press Sign in to start again." },
    { name: "an unreachable strazad", token: "down", want: "strazad is unreachable, state unknown. Check the server, then press Sign in again." },
  ];
  it.each(refusals)("refuses $name with its sentence and checks in nothing", async ({ params, token: tokenAnswer, age, nonce, want }) => {
    let p = { nonce: "" };
    const calls = stubFetch({ "POST /oidc/token": () => tokenAnswer || ok({ id_token: idTokenWith(nonce || p.nonce) }), "POST /v1/checkin": () => ok(good) });
    signInLocation();
    p = await leaveAndAnswer(params);
    if (age) vi.setSystemTime(Date.now() + age);
    const done = await finishSignIn(window.location.search);
    expect("problem" in done && done.problem).toContain(want);
    expect(checkins(calls)).toBe(0);
    expect(pendingOf()).toBeNull();
  });

  it("says the provider must allow this origin when its token endpoint will not answer this page", async () => {
    vi.resetModules();
    const fresh = await import("./session");
    stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "https://sso.example.com", client_id: "straza" }),
      "GET https://sso.example.com/.well-known/openid-configuration": () => ok({ authorization_endpoint: "https://sso.example.com/auth", token_endpoint: "https://sso.example.com/token" }),
      "POST https://sso.example.com/token": () => "down",
    });
    signInLocation();
    await fresh.beginSignIn();
    const p = pendingOf()!;
    signInLocation(SIGNIN_PATH, "?code=c-1&state=" + p.state);
    const done = await fresh.finishSignIn(window.location.search);
    expect("problem" in done && done.problem).toBe("Your identity provider at sso.example.com did not let this page finish the sign-in. Straza's registration there must allow sign-ins from https://straza.example.com: a single-page application address on Entra, a web origin on Keycloak, a trusted origin on Okta. Ask your administrator, then press Sign in.");
  });

  it.each([
    { status: 403, error: "user is disabled. Contact your administrator", want: "You signed in at your identity provider, but Straza refused the session: user is disabled. Contact your administrator." },
    { status: 503, error: "store unavailable", want: "Straza is temporarily unavailable, try again in a moment (the sign-in itself succeeded, nothing on your side needs fixing)." },
  ])("words a check-in answered $status", async ({ status, error, want }) => {
    let p = { nonce: "" };
    stubFetch({ "POST /oidc/token": () => ok({ id_token: idTokenWith(p.nonce) }), "POST /v1/checkin": () => refuse(status, error) });
    signInLocation();
    p = await leaveAndAnswer();
    const done = await finishSignIn(window.location.search);
    expect("problem" in done && done.problem).toBe(want);
  });

  it("keeps an Enable this browser name for the page to resume, once", async () => {
    let p = { nonce: "" };
    stubFetch({ "POST /oidc/token": () => ok({ id_token: idTokenWith(p.nonce) }), "POST /v1/checkin": () => ok(good) });
    signInLocation("/self-service/browser");
    p = await leaveAndAnswer({}, { back: "/self-service/browser", sheet: "Firefox on Linux" });
    expect(await finishSignIn(window.location.search)).toEqual({ resp: good, back: "/self-service/browser" });
    expect(takeResume()).toBe("Firefox on Linux");
    expect(takeResume()).toBeNull();
  });

  it("returns only to this server's two pages, whatever the stored address says", async () => {
    stubFetch({});
    signInLocation();
    await beginSignIn({ back: "//evil.example/x" });
    expect(pendingOf()!.back).toBe("/console/");
  });

  it("says once that a sign-in left and never came back, and a stashed failure comes first", async () => {
    stubFetch({});
    signInLocation();
    await beginSignIn();
    stashProblem("the stashed one");
    expect(takeSignInProblem()).toBe("the stashed one");
    expect(takeSignInProblem()).toBe(NO_ANSWER + "https://straza.example.com/console/signin to Straza's registration there. Press Sign in to try again.");
    expect(takeSignInProblem()).toBeNull();
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
