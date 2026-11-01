import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { SIGNIN_PATH, beginSignIn, logout } from "@/lib/session";
import { SignIn } from "./sign-in";

type Call = { method: string; path: string; body?: string };
type Answer = { status: number; body: unknown } | "down";

const ok = (body: unknown): Answer => ({ status: 200, body });
const refuse = (status: number, error: string): Answer => ({ status, body: { error } });

// stubFetch answers by "METHOD path" and records every call. An unknown
// route answers 404, which keeps login discovery on the server's own page.
function stubFetch(routes: Record<string, (call: Call) => Answer>): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const call: Call = { method: init?.method || "GET", path: String(input), body: init?.body as string | undefined };
    calls.push(call);
    const route = routes[call.method + " " + call.path];
    const answer: Answer = route ? route(call) : refuse(404, "no route " + call.path);
    if (answer === "down") throw new TypeError("Failed to fetch");
    return { ok: answer.status < 400, status: answer.status, json: async () => answer.body } as Response;
  });
  return calls;
}

const good = { session_token: "tok-1", user: "alice", roles: ["straza-admin"], admin_grants: "full", expires_in: 300, session_id: "s1" };
const none = { kind: "none", detail: "" } as const;
const idTokenWith = (nonce: string) => "h." + btoa(JSON.stringify({ nonce })).replace(/=+$/, "") + ".sig";
const pending = () => JSON.parse(sessionStorage.getItem("straza.signin") || "null") as { state: string; nonce: string } | null;

// settle lets the fetch stubs and the state they set run to the end.
const settle = () => act(async () => { await vi.advanceTimersByTimeAsync(0); });

// jsdom will not let the real window.location be spied on, so the suite
// swaps the whole object and puts it back after each case.
const realLocation = Object.getOwnPropertyDescriptor(window, "location");
function stubLocation(pathname = "/console/policies", search = "") {
  const assign = vi.fn();
  const replace = vi.fn();
  Object.defineProperty(window, "location", { configurable: true, value: { assign, replace, origin: "https://straza.example.com", pathname, search } });
  return { assign, replace };
}

// answered starts a sign-in from back, then stands the tab at the answer
// the identity provider sends to the sign-in address.
async function answered(back: string, extra = "") {
  stubLocation(back);
  await beginSignIn({ back });
  return stubLocation(SIGNIN_PATH, "?code=c-1&state=" + pending()!.state + extra);
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
  window.history.replaceState(null, "", "/");
});

describe("the sign-in card", () => {
  const notices = [
    { kind: "signed-out", text: "Signed out. The session was revoked on the server. Your identity provider may still have you signed in, so on a shared computer sign out there too." },
    { kind: "unconfirmed", text: "Signed out in this browser, but the server did not confirm the revoke: the session expires on its own within minutes, or sign back in and revoke it from Sessions." },
    { kind: "unreachable", text: "strazad is unreachable, state unknown. Sign in once it is back." },
  ] as const;
  it.each(notices)("words the $kind notice", ({ kind, text }) => {
    stubFetch({});
    stubLocation();
    render(<SignIn reason={{ kind, detail: "" }} returnTo="Overview" onAuthed={vi.fn()} />);
    expect(screen.getByText(text)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /with a code/ })).toBeNull();
    expect(screen.getByText("Admin roles only. Anyone else lands on the self-service page, still signed in.")).toBeTruthy();
  });

  it("says where Sign in goes, the server's own page when idp.json is absent", async () => {
    stubFetch({});
    stubLocation();
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    await settle();
    expect(screen.getByText("You sign in on this server's own sign-in page and come back to this page.")).toBeTruthy();
  });

  it("leaves this tab for the sign-in page on Sign in", async () => {
    stubFetch({});
    const { assign } = stubLocation();
    render(<SignIn reason={none} returnTo="Policies" onAuthed={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    await act(async () => { await vi.waitFor(() => expect(assign).toHaveBeenCalled()); });
    expect(String(assign.mock.calls[0][0])).toMatch(/^\/oidc\/authorize\?response_type=code&client_id=console&/);
    expect(screen.getByText("Taking you to the sign-in page")).toBeTruthy();
  });

  it("finishes an admin's sign-in, returns to the page they started on and hands over the session", async () => {
    stubFetch({});
    const { replace } = await answered("/console/policies");
    const nonce = pending()!.nonce;
    stubFetch({ "POST /oidc/token": () => ok({ id_token: idTokenWith(nonce) }), "POST /v1/checkin": () => ok(good) });
    const replaceState = vi.spyOn(window.history, "replaceState");
    const onAuthed = vi.fn();
    render(<SignIn reason={none} returnTo="Overview" onAuthed={onAuthed} />);
    expect(screen.getByText("Signing you in")).toBeTruthy();
    await settle();
    expect(onAuthed).toHaveBeenCalledWith(good);
    expect(replaceState).toHaveBeenLastCalledWith(null, "", "/console/policies");
    expect(replace).not.toHaveBeenCalled();
  });

  it.each([
    { name: "a person without admin standing", back: "/console/policies", resp: { session_token: "tok-2", user: "joe" }, to: "/self-service/" },
    { name: "a sign-in started on the self-service page", back: "/self-service/browser", resp: good, to: "/self-service/browser" },
  ])("sends $name on to the self-service page, signed in", async ({ back, resp, to }) => {
    const { replace } = await answered(back);
    const nonce = pending()!.nonce;
    stubFetch({ "POST /oidc/token": () => ok({ id_token: idTokenWith(nonce) }), "POST /v1/checkin": () => ok(resp) });
    const onAuthed = vi.fn();
    render(<SignIn reason={none} returnTo="Overview" onAuthed={onAuthed} />);
    await settle();
    expect(replace).toHaveBeenCalledWith(to);
    expect(onAuthed).not.toHaveBeenCalled();
    expect(sessionStorage.getItem("straza.session")).toContain("tok-");
  });

  it("shows why an answer was not used and offers Sign in again", async () => {
    await answered("/console/policies", "&error=access_denied");
    stubFetch({});
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    await settle();
    expect(screen.getByText("Your identity provider did not sign you in. Press Sign in to try again, or ask your administrator whether your account may use Straza.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeTruthy();
  });

  it("hands a failure of a self-service sign-in back to that page", async () => {
    const { replace } = await answered("/self-service/browser", "&error=access_denied");
    stubFetch({});
    render(<SignIn reason={none} returnTo="Overview" onAuthed={vi.fn()} />);
    await settle();
    expect(replace).toHaveBeenCalledWith("/self-service/browser");
    expect(sessionStorage.getItem("straza.signin.problem")).toContain("did not sign you in");
  });

  it("says what to register when a sign-in left and came back without an answer", async () => {
    stubFetch({});
    stubLocation();
    await beginSignIn();
    render(<SignIn reason={none} returnTo="Policies" onAuthed={vi.fn()} />);
    expect(screen.getByText("The sign-in did not come back from your identity provider. If it showed an error about a redirect address, your administrator must add https://straza.example.com/console/signin to Straza's registration there. Press Sign in to try again.")).toBeTruthy();
  });

  it("the emergency sign-in goes to the server's own page with the console id", async () => {
    stubFetch({ "GET /.well-known/straza/idp.json": () => ok({ issuer: "https://sso.example.com", client_id: "straza" }) });
    const { assign } = stubLocation();
    render(<SignIn reason={none} returnTo="Policies" onAuthed={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: "Emergency sign-in" }));
    await act(async () => { await vi.waitFor(() => expect(assign).toHaveBeenCalled()); });
    expect(String(assign.mock.calls[0][0])).toMatch(/^\/oidc\/authorize\?.*client_id=console/);
  });
});
