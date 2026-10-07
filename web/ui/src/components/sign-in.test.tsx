import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { logout } from "@/lib/session";
import { SignIn } from "./sign-in";

type Call = { method: string; path: string; headers: Record<string, string>; body?: string };
type Answer = { status: number; body: unknown } | "down";

const ok = (body: unknown): Answer => ({ status: 200, body });
const refuse = (status: number, error: string): Answer => ({ status, body: { error } });

// stubFetch answers by "METHOD path" and records every call. An unknown
// route answers 404, which keeps login discovery on the same-origin flow.
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

const grant = { device_code: "dev-1", user_code: "ABCD-EFGH", verification_uri: "/oidc/device", verification_uri_complete: "/oidc/device?user_code=ABCD-EFGH", interval: 1, expires_in: 600 };
const good = { session_token: "tok-1", user: "alice", roles: ["straza-admin"], admin_grants: "full", expires_in: 300, session_id: "s1" };
const none = { kind: "none", detail: "" } as const;
const OUTAGE = "Straza is temporarily unavailable, try again in a moment (the sign-in itself succeeded, nothing on your side needs fixing).";

// settle lets the fetch stubs, the timers due within ms and the state they
// set run to the end under fake timers.
const settle = (ms = 0) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const clickSignIn = () => fireEvent.click(screen.getByRole("button", { name: "Sign in with a code" }));

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

describe("the sign-in card", () => {
  const notices = [
    { kind: "signed-out", text: "Signed out. The session was revoked on the server." },
    { kind: "unconfirmed", text: "Signed out in this browser, but the server did not confirm the revoke: the session expires on its own within minutes, or sign back in and revoke it from Sessions." },
    { kind: "unreachable", text: "strazad is unreachable, state unknown. Sign in once it is back." },
  ] as const;
  it.each(notices)("words the $kind notice", ({ kind, text }) => {
    render(<SignIn reason={{ kind, detail: "" }} returnTo="Overview" onAuthed={vi.fn()} />);
    expect(screen.getByText(text)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in with a code" })).toBeTruthy();
    expect(screen.getByText("Admin roles only. Anyone else lands on the self-service page, still signed in.")).toBeTruthy();
  });

  it("shows no notice on a fresh tab", () => {
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    expect(screen.queryByText(/Signed out|session ended|unreachable/)).toBeNull();
    expect(screen.getByText("You get a short code, enter it at your identity provider, and this page signs you in.")).toBeTruthy();
  });

  it("shows the lost notice with the page label and runs the device flow through to onAuthed", async () => {
    let polls = 0;
    const calls = stubFetch({
      "POST /oidc/device_authorization": () => ok(grant),
      "POST /oidc/token": () => (++polls < 2 ? { status: 400, body: { error: "authorization_pending" } } : ok({ id_token: "idt" })),
      "POST /v1/checkin": () => ok(good),
    });
    const onAuthed = vi.fn();
    render(<SignIn reason={{ kind: "lost", detail: "session is no longer active" }} returnTo="Sessions" onAuthed={onAuthed} />);
    expect(screen.getByText("Your session ended: session is no longer active. Sign in again and you return to Sessions.")).toBeTruthy();

    clickSignIn();
    await settle();
    expect(screen.getByText("Enter this code at your identity provider:")).toBeTruthy();
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy the code" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open the sign-in page" })).toBeTruthy();
    expect(screen.getByText("Waiting for authorization")).toBeTruthy();
    expect(screen.queryByText(/Your session ended/)).toBeNull();

    await settle(1000);
    expect(polls).toBe(1);
    expect(onAuthed).not.toHaveBeenCalled();
    await settle(1000);
    expect(polls).toBe(2);
    expect(onAuthed).toHaveBeenCalledTimes(1);
    expect(onAuthed).toHaveBeenCalledWith(good);
    expect(screen.getByText("Authorized, loading the console")).toBeTruthy();
    const ck = calls.find((c) => c.path === "/v1/checkin")!;
    expect(JSON.parse(ck.body as string)).toEqual({ id_token: "idt", harness: { name: "console", version: "1" }, attestation: { managed: false, hashes: {} } });
    expect(sessionStorage.getItem("straza.session")).toBe('{"token":"tok-1","user":"alice","admin_grants":"full"}');
  });

  it("opens the verification page in a new tab with a handle", async () => {
    stubFetch({ "POST /oidc/device_authorization": () => ok(grant), "POST /oidc/token": () => ({ status: 400, body: { error: "authorization_pending" } }) });
    const open = vi.fn(() => null);
    vi.stubGlobal("open", open);
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    clickSignIn();
    await settle();
    fireEvent.click(screen.getByRole("button", { name: "Open the sign-in page" }));
    expect(open).toHaveBeenCalledWith("/oidc/device?user_code=ABCD-EFGH", "_blank");
  });

  it("sends a signed-in person without admin grants to the self-service page", async () => {
    const replace = stubLocation();
    stubFetch({
      "POST /oidc/device_authorization": () => ok(grant),
      "POST /oidc/token": () => ok({ id_token: "idt" }),
      "POST /v1/checkin": () => ok({ ...good, admin_grants: "" }),
    });
    const onAuthed = vi.fn();
    render(<SignIn reason={none} returnTo="Overview" onAuthed={onAuthed} />);
    clickSignIn();
    await settle();
    await settle(1000);
    expect(replace).toHaveBeenCalledWith("/self-service/");
    expect(onAuthed).not.toHaveBeenCalled();
    expect(sessionStorage.getItem("straza.session")).toBe('{"token":"tok-1","user":"alice","admin_grants":""}');
  });

  const exchanges: { name: string; answer: Answer; problem: string }[] = [
    { name: "a 503", answer: refuse(503, "store unavailable"), problem: OUTAGE },
    { name: "an unreachable server", answer: "down", problem: OUTAGE },
    { name: "a refusal", answer: refuse(403, "user is disabled. Contact your administrator"), problem: "signed in, but the session exchange failed: user is disabled. Contact your administrator. Start again." },
  ];
  it.each(exchanges)("says what happened when checkin answers $name", async (c) => {
    stubFetch({
      "POST /oidc/device_authorization": () => ok(grant),
      "POST /oidc/token": () => ok({ id_token: "idt" }),
      "POST /v1/checkin": () => c.answer,
    });
    const onAuthed = vi.fn();
    render(<SignIn reason={none} returnTo="Overview" onAuthed={onAuthed} />);
    clickSignIn();
    await settle();
    await settle(1000);
    expect(screen.getByText(c.problem)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
    expect(onAuthed).not.toHaveBeenCalled();
  });

  const polls: { name: string; error: string; problem: string }[] = [
    { name: "expired_token", error: "expired_token", problem: "the code expired before it was authorized. Start again." },
    { name: "access_denied", error: "access_denied", problem: "sign-in was denied at the verification page." },
    { name: "another error", error: "invalid_grant", problem: "sign-in refused: invalid_grant" },
  ];
  it.each(polls)("stops with the $name sentence", async (c) => {
    stubFetch({
      "POST /oidc/device_authorization": () => ok(grant),
      "POST /oidc/token": () => ({ status: 400, body: { error: c.error } }),
    });
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    clickSignIn();
    await settle();
    await settle(1000);
    expect(screen.getByText(c.problem)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
  });

  it("waits five seconds longer after slow_down and keeps polling through a blip", async () => {
    let polls = 0;
    stubFetch({
      "POST /oidc/device_authorization": () => ok(grant),
      "POST /oidc/token": () => {
        polls += 1;
        if (polls === 1) return { status: 400, body: { error: "slow_down" } };
        if (polls === 2) return "down";
        return { status: 400, body: { error: "authorization_pending" } };
      },
    });
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    clickSignIn();
    await settle();
    await settle(1000);
    expect(polls).toBe(1);
    await settle(5999);
    expect(polls).toBe(1);
    await settle(1);
    expect(polls).toBe(2);
    await settle(6000);
    expect(polls).toBe(3);
    expect(screen.getByText("Waiting for authorization")).toBeTruthy();
  });

  it("says the server is unreachable when the device code cannot be requested", async () => {
    stubFetch({ "POST /oidc/device_authorization": () => "down" });
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    clickSignIn();
    await settle();
    expect(screen.getByText("strazad is unreachable, state unknown. Check the server and retry.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
  });
  it("names the identity provider, not the server, when this browser cannot reach it", async () => {
    vi.resetModules(); // the discovered flow is cached per page load
    const { SignIn: Fresh } = await import("./sign-in");
    stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "http://localhost:8480/realms/straza", client_id: "straza" }),
      "GET http://localhost:8480/realms/straza/.well-known/openid-configuration": () => "down",
    });
    render(<Fresh reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    clickSignIn();
    await settle();
    expect(screen.getByText("This browser cannot reach your identity provider at http://localhost:8480/realms/straza. Open that address in this browser to check that it answers, then press Start again.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start again" })).toBeTruthy();
  });
  it("runs the emergency sign-in at the server's own page and never reads idp.json", async () => {
    let polls = 0;
    const calls = stubFetch({
      "GET /.well-known/straza/idp.json": () => ok({ issuer: "https://idp.example", client_id: "straza-console" }),
      "POST /oidc/device_authorization": () => ok(grant),
      "POST /oidc/token": () => (++polls < 2 ? { status: 400, body: { error: "authorization_pending" } } : ok({ id_token: "idt" })),
      "POST /v1/checkin": () => ok(good),
    });
    const onAuthed = vi.fn();
    render(<SignIn reason={none} returnTo="Overview" onAuthed={onAuthed} />);
    fireEvent.click(screen.getByRole("button", { name: "Emergency sign-in" }));
    await settle();
    expect(screen.getByText("Enter this code on the server's emergency sign-in page, as break-glass:")).toBeTruthy();
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    await settle(1000);
    await settle(1000);
    expect(onAuthed).toHaveBeenCalledWith(good);
    expect(calls.some((c) => c.path === "/.well-known/straza/idp.json")).toBe(false);
    expect(calls.find((c) => c.path === "/oidc/device_authorization")!.body).toBe("client_id=console&scope=openid");
    const poll = calls.filter((c) => c.path === "/oidc/token");
    expect(poll.length).toBe(2);
    expect(poll[0].body).toBe("grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code&device_code=dev-1&client_id=console");
  });

  it("offers no emergency sign-in on the self-service dialog", () => {
    render(<SignIn reason={none} returnTo="Self-service" onAuthed={vi.fn()} adminOnly={false} embedded />);
    expect(screen.queryByRole("button", { name: "Emergency sign-in" })).toBeNull();
  });
});
