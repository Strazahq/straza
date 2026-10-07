import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ServerRow, connectAgents, connectDelete, connectFinish, connectStart, connectToken, selfServers } from "@/lib/api";
import { notify } from "@/lib/notify";
import { refresh } from "@/lib/session";
import { leaveFor } from "./come-back";
import { CredentialsTab } from "./credentials-tab";
import type { SelfState } from "./context";
import * as W from "./credential-words";

// The Credentials tab against a mocked /v1/self/servers lane and a mocked
// shell: the bands and the words of every state, the paste that sends the
// value once and keeps none of it, the confirms, the agents switch, and the
// provider sign-in that leaves in this tab and is finished when the tab
// comes back. The checklist is the old approvals page's
// connections.test.js, in the new words.

const held = vi.hoisted(() => ({ state: null as unknown as SelfState }));
vi.mock("./context", () => ({ useSelf: () => held.state }));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfServers: vi.fn(),
  connectToken: vi.fn(),
  connectStart: vi.fn(),
  connectFinish: vi.fn(),
  connectAgents: vi.fn(),
  connectDelete: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/session", () => ({ refresh: vi.fn() }));
vi.mock("./come-back", async (orig) => ({ ...(await orig<typeof import("./come-back")>()), leaveFor: vi.fn() }));

const AGENT = "joe-java-developer-agent";
const SECRET = "ghp_secret_value_never_shown";
const PAST_DAY = new Date(Date.now() - 9 * 86400000).toISOString().slice(0, 10);
const PAST = PAST_DAY + "T00:00:00Z";
const FUTURE = new Date(Date.now() + 3600000).toISOString();

function ownRows(): ServerRow[] {
  return [
    { app: "demo-tools", runtime: "remote", kind: "none", reached: true, connected: false },
    { app: "filesystem", runtime: "command", kind: "static", reached: true, connected: false },
    { app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: false, allow_agents: false },
    { app: "gitlab", runtime: "remote", kind: "token", agents: "own", reached: false, connected: true, fingerprint: "b81e", updated_at: "2026-08-02T10:00:00Z", allow_agents: false },
    { app: "jira", runtime: "remote", kind: "token", agents: "own", reached: true, connected: true, fingerprint: "1a2b", expires_at: PAST, updated_at: "2026-08-20T10:00:00Z", allow_agents: false },
    { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false, allow_agents: false },
    { app: "sentry", runtime: "remote", kind: "static", reached: true, connected: false },
  ];
}

function agentRows(): ServerRow[] {
  return [
    { app: "filesystem", runtime: "command", kind: "static", reached: true, connected: false },
    { app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: false, allow_agents: false },
    { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false, allow_agents: false },
  ];
}

// SIGN_IN_ROW is a server the person has not signed in to yet.
const SIGN_IN_ROW: ServerRow = { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false, allow_agents: false };

const setCount = vi.fn();
const openSignIn = vi.fn();

function shell(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [AGENT] },
    refreshSelf: vi.fn(),
    enrollment: null,
    setEnrollment: vi.fn(),
    storage: { usable: true, outlook: "unknown" },
    openSignIn,
    notice: null,
    setNotice: vi.fn(),
    sheets: { enable: false, phone: false },
    setSheet: vi.fn(),
    setCount,
    ...over,
  } as SelfState;
}

// answers is the /v1/self/servers lane: the person's list, then each
// agent's, by the user the call names.
function answers(own: ServerRow[], agent: ServerRow[] = agentRows()) {
  vi.mocked(selfServers).mockImplementation(async (user = "") => (user ? agent : own));
}

function show() {
  return render(
    <TooltipProvider>
      <CredentialsTab />
    </TooltipProvider>,
  );
}

// lines reads the table the way a person reads it: each band caption in
// place, and the row names between them.
const lines = () =>
  [...document.querySelectorAll("tbody tr")].map((tr) => {
    const band = tr.querySelector("[data-band]");
    return band ? "band:" + band.getAttribute("data-band") : tr.getAttribute("data-credential");
  });

const rowOf = (name: string) => document.querySelector('[data-credential="' + name + '"]') as HTMLElement;
const statusOf = (name: string) => (rowOf(name).querySelector("[data-status]") as HTMLElement).textContent;
const agentRow = (app: string) => rowOf(app + " for " + AGENT);

beforeEach(() => {
  vi.clearAllMocks();
  held.state = shell();
  window.history.replaceState(null, "", "/self-service/credentials");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the Credentials tab", () => {
  it("bands the rows, one read for the person and one for each agent", async () => {
    answers(ownRows());
    show();
    await screen.findByText(W.countWords(9, 4));

    expect(lines()).toEqual([
      "band:Needs you", "github", "github for " + AGENT, "jira", "midpoint",
      "band:No action available here", "filesystem", "filesystem for " + AGENT, "midpoint for " + AGENT, "sentry",
      "band:No longer in reach", "gitlab",
    ]);
    expect(vi.mocked(selfServers).mock.calls).toEqual([[""], [AGENT]]);
    expect(setCount).toHaveBeenCalledWith("credentials", 4);
    // Whoever must act on these rows is a person, and this page is their
    // way to act, so no sentence here sends them to a command line.
    expect(document.body.textContent).not.toContain("strazactl");
  });

  it("says the kind of every credential from the person's side", async () => {
    answers(ownRows());
    show();
    await screen.findByText(W.countWords(9, 4));

    expect(within(rowOf("github")).getByText("your own token")).toBeTruthy();
    expect(within(rowOf("midpoint")).getByText("your own sign-in at keycloak")).toBeTruthy();
    expect(within(rowOf("sentry")).getByText("one shared secret")).toBeTruthy();
    expect(within(agentRow("github")).getByText("its own token")).toBeTruthy();
    expect(within(agentRow("midpoint")).getByText("its own sign-in at keycloak")).toBeTruthy();

    expect(statusOf("github")).toBe(W.NOT_SET);
    expect(statusOf("jira")).toBe(W.expiredWord(PAST_DAY));
    expect(statusOf("midpoint")).toBe(W.NOT_SIGNED_IN);
    expect(statusOf("midpoint for " + AGENT)).toBe(W.AGENT_SIGN_IN_UNAVAILABLE);
    expect(agentRow("midpoint").textContent).toContain("cannot sign in at keycloak");
    expect(within(agentRow("midpoint")).queryByRole("button", { name: /Sign in/ })).toBeNull();
    expect(statusOf("filesystem")).toBe(W.SHARED_ONLY);
    expect(statusOf("sentry")).toBe(W.SHARED);
    expect(statusOf("gitlab")).toBe(W.NOT_IN_YOUR_REACH);
    expect(within(rowOf("gitlab")).getByText("b81e")).toBeTruthy();
    expect(within(rowOf("gitlab")).getByText(W.leftover("gitlab", "", "token"))).toBeTruthy();
    expect(within(rowOf("jira")).getByText(W.expiredSentence("jira", PAST_DAY, ""))).toBeTruthy();
  });

  it("names the servers that need no credential and says once why some rows do nothing", async () => {
    answers(ownRows());
    show();
    await screen.findByText(W.countWords(9, 4));

    const foot = document.querySelector("[data-credentials-foot]") as HTMLElement;
    expect(within(foot).getByText(W.noneLine(["demo-tools"], ""))).toBeTruthy();
    expect(within(foot).getByText(W.HINTS_TITLE)).toBeTruthy();
    expect(within(foot).getByText(W.oneProcessHint("command"))).toBeTruthy();
    // The same sentence under every command row is what stops people
    // reading the table, so each is said once for the whole table.
    expect(screen.getAllByText(W.oneProcessHint("command"))).toHaveLength(1);
    expect(screen.getAllByText(W.ADMIN_ROUTE)).toHaveLength(1);
    expect(document.querySelector('[data-credential="demo-tools"]')).toBeNull();
  });

  it("reads an agent's row as running on the person's credential once the switch is on", async () => {
    answers([
      { app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: true, fingerprint: "3f9a", set_by: "alice", updated_at: "2026-09-01T10:00:00Z", allow_agents: true },
      { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "sponsor", reached: true, connected: true, expires_at: FUTURE, updated_at: "2026-09-10T08:00:00Z", scopes: ["openid", "profile"], allow_agents: true },
    ], [
      { app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: false, allow_agents: false },
      { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "sponsor", reached: true, connected: false, allow_agents: false },
    ]);
    show();
    await screen.findByText(W.countWords(4, 1));

    expect(statusOf("github")).toBe(W.SET);
    expect(within(rowOf("github")).getByText(W.setByLine("you", "2026-09-01"))).toBeTruthy();
    expect(statusOf("github for " + AGENT)).toBe(W.USES_YOUR_TOKEN);
    expect(statusOf("midpoint for " + AGENT)).toBe(W.USES_YOUR_SIGN_IN);
    expect(within(agentRow("github")).getByText(W.agentNoToken(AGENT, "github", "sponsor", true))).toBeTruthy();
    expect(within(rowOf("midpoint")).getByText("openid profile")).toBeTruthy();
  });

  it("sends a pasted token once, with its expiry, and keeps none of it", async () => {
    const user = userEvent.setup();
    answers([{ app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: false, allow_agents: false }], []);
    vi.mocked(connectToken).mockResolvedValue({ fingerprint: "3f9a", expires_at: "2026-12-31T00:00:00Z", set_by: "alice", allow_agents: false });
    show();
    await screen.findByText(W.countWords(1, 1));

    await user.click(within(rowOf("github")).getByRole("button", { name: W.actionName(W.PASTE, "github") }));
    expect(await screen.findByText(W.pasteTitle("github", false, ""))).toBeTruthy();
    expect(screen.getByText(W.sealedLine("github"))).toBeTruthy();

    // An empty save says so at the box and never reaches the server.
    await user.click(screen.getByRole("button", { name: W.SAVE_TOKEN }));
    expect(screen.getByText(W.tokenRequired("github"))).toBeTruthy();
    expect(connectToken).not.toHaveBeenCalled();

    const box = screen.getByLabelText(W.TOKEN_LABEL) as HTMLInputElement;
    await user.type(box, SECRET);
    const day = screen.getByLabelText(W.EXPIRY_LABEL) as HTMLInputElement;
    await act(async () => {
      day.value = "2026-12-31";
      day.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await user.click(screen.getByRole("button", { name: W.SAVE_TOKEN }));

    await waitFor(() => expect(statusOf("github")).toBe(W.SET));
    expect(vi.mocked(connectToken).mock.calls).toEqual([["github", SECRET, "2026-12-31T00:00:00Z", ""]]);
    expect(within(rowOf("github")).getByText("3f9a")).toBeTruthy();
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("names the agent in the paste and sends its user", async () => {
    const user = userEvent.setup();
    answers(ownRows());
    vi.mocked(connectToken).mockResolvedValue({ fingerprint: "9c02", set_by: "alice", allow_agents: false });
    show();
    await screen.findByText(W.countWords(9, 4));

    await user.click(within(agentRow("github")).getByRole("button", { name: W.actionName(W.PASTE, "github for " + AGENT) }));
    expect(await screen.findByText(W.pasteTitle("github", false, AGENT))).toBeTruthy();
    await user.type(screen.getByLabelText(W.TOKEN_LABEL), SECRET);
    await user.click(screen.getByRole("button", { name: W.SAVE_TOKEN }));

    await waitFor(() => expect(statusOf("github for " + AGENT)).toBe(W.SET));
    expect(vi.mocked(connectToken).mock.calls).toEqual([["github", SECRET, "", AGENT]]);
    expect(within(agentRow("github")).getByText(W.agentOwnToken(AGENT, "github"))).toBeTruthy();
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("quotes the server's sentence when a paste is refused and empties the box", async () => {
    const user = userEvent.setup();
    answers([{ app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: false, allow_agents: false }], []);
    vi.mocked(connectToken).mockRejectedValue(new ApiError("github refused the token or did not answer (401 Unauthorized). Check the token and try again.", 400));
    show();
    await screen.findByText(W.countWords(1, 1));

    await user.click(within(rowOf("github")).getByRole("button", { name: W.actionName(W.PASTE, "github") }));
    await user.type(await screen.findByLabelText(W.TOKEN_LABEL), SECRET);
    await user.click(screen.getByRole("button", { name: W.SAVE_TOKEN }));

    expect(await screen.findByText(/github refused the token or did not answer/)).toBeTruthy();
    expect((screen.getByLabelText(W.TOKEN_LABEL) as HTMLInputElement).value).toBe("");
    expect(statusOf("github")).toBe(W.NOT_SET);
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("asks before it removes a token, then sends one DELETE", async () => {
    const user = userEvent.setup();
    answers([{ app: "github", runtime: "remote", kind: "token", agents: "own", reached: true, connected: true, fingerprint: "3f9a", set_by: "alice", updated_at: "2026-09-01T10:00:00Z", allow_agents: false }], []);
    vi.mocked(connectDelete).mockResolvedValue({});
    show();
    await screen.findByText(W.countWords(1, 0));

    await user.click(screen.getByRole("button", { name: W.actionName(W.REMOVE, "github") }));
    expect(await screen.findByText(W.removeTokenTitle("github", ""))).toBeTruthy();
    expect(screen.getByText(W.removeTokenBody("github", ""))).toBeTruthy();
    expect(connectDelete).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: W.CANCEL }));
    await waitFor(() => expect(screen.queryByText(W.removeTokenTitle("github", ""))).toBeNull());
    expect(statusOf("github")).toBe(W.SET);

    await user.click(screen.getByRole("button", { name: W.actionName(W.REMOVE, "github") }));
    await user.click(await screen.findByRole("button", { name: W.REMOVE_TOKEN }));
    await waitFor(() => expect(statusOf("github")).toBe(W.NOT_SET));
    expect(vi.mocked(connectDelete).mock.calls).toEqual([["github", ""]]);
  });

  it("drops a row nobody reaches any more once its credential is removed", async () => {
    const user = userEvent.setup();
    answers([
      { app: "github", runtime: "remote", kind: "token", agents: "own", reached: true, connected: false, allow_agents: false },
      { app: "gitlab", runtime: "remote", kind: "token", agents: "own", reached: false, connected: true, fingerprint: "b81e", updated_at: "2026-08-02T10:00:00Z", allow_agents: false },
    ], []);
    vi.mocked(connectDelete).mockResolvedValue({});
    show();
    await screen.findByText(W.countWords(2, 1));

    await user.click(within(rowOf("gitlab")).getByRole("button", { name: W.actionName(W.REMOVE, "gitlab") }));
    expect(await screen.findByText(W.leftoverTitle("gitlab", "token"))).toBeTruthy();
    await user.click(screen.getByRole("button", { name: W.REMOVE_TOKEN }));

    await waitFor(() => expect(rowOf("gitlab")).toBeNull());
    expect(vi.mocked(connectDelete).mock.calls).toEqual([["gitlab", ""]]);
  });

  it("patches the agents switch once and says what it means", async () => {
    const user = userEvent.setup();
    answers([{ app: "github", runtime: "remote", kind: "token", agents: "sponsor", reached: true, connected: true, fingerprint: "3f9a", set_by: "alice", updated_at: "2026-09-01T10:00:00Z", allow_agents: false }]);
    vi.mocked(connectAgents).mockResolvedValue({ allow_agents: true });
    show();
    await screen.findByText(W.countWords(4, 1));

    expect(screen.getByText(W.switchOff([AGENT], "github", "token"))).toBeTruthy();
    await user.click(screen.getByRole("switch", { name: W.SWITCH_LABEL }));

    expect(await screen.findByText(W.switchOn([AGENT], "github", "token"))).toBeTruthy();
    expect(vi.mocked(connectAgents).mock.calls).toEqual([["github", true, ""]]);
    expect(statusOf("github for " + AGENT)).toBe(W.USES_YOUR_TOKEN);
  });

  it("offers no switch where each caller signs in for themselves", async () => {
    answers([{ app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: true, expires_at: FUTURE, updated_at: "2026-09-10T08:00:00Z", scopes: ["openid"], allow_agents: false }], []);
    show();
    await screen.findByText(W.countWords(1, 0));

    expect(statusOf("midpoint")).toBe(W.SIGNED_IN);
    expect(screen.getByText(W.NO_SWITCH_HERE)).toBeTruthy();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.getByRole("button", { name: W.actionName(W.SIGN_IN_AGAIN, "midpoint") })).toBeTruthy();
    expect(screen.getByRole("button", { name: W.actionName(W.DISCONNECT, "midpoint") })).toBeTruthy();
  });

  it("renews the session inside the press and leaves for the provider in this tab", async () => {
    const user = userEvent.setup();
    const AUTH = "https://kc.example/auth?state=abc";
    answers([SIGN_IN_ROW], []);
    const order: string[] = [];
    vi.mocked(refresh).mockImplementation(async () => { order.push("refresh"); return {} as never; });
    vi.mocked(connectStart).mockImplementation(async () => { order.push("start"); return { authorize_url: AUTH, expires_in: 600 }; });
    vi.mocked(leaveFor).mockImplementation(() => { order.push("leave"); });
    const open = vi.fn();
    window.open = open;
    show();
    await screen.findByText(W.countWords(1, 1));

    await user.click(screen.getByRole("button", { name: W.actionName(W.signInWith("keycloak"), "midpoint") }));

    await waitFor(() => expect(leaveFor).toHaveBeenCalledWith(AUTH));
    expect(order).toEqual(["refresh", "start", "leave"]);
    expect(open).not.toHaveBeenCalled();
    expect(statusOf("midpoint")).toBe(W.NOT_SIGNED_IN);
  });

  it("finishes a sign-in that came back, with the address wiped before the request leaves", async () => {
    const done: ServerRow = { ...SIGN_IN_ROW, connected: true, updated_at: "2026-09-14T08:00:00Z", expires_at: FUTURE, scopes: ["openid"] };
    let rows = [SIGN_IN_ROW];
    vi.mocked(selfServers).mockImplementation(async () => rows);
    let addressAtPost = "";
    vi.mocked(connectFinish).mockImplementation(async () => {
      addressAtPost = window.location.href;
      rows = [done];
      return { app: "midpoint", provider: "keycloak", allow_agents: false };
    });
    held.state = shell({ self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] } });
    window.history.replaceState(null, "", "/self-service/credentials#connect&code=c0de&state=st4te");
    show();

    await waitFor(() => expect(statusOf("midpoint")).toBe(W.SIGNED_IN));
    expect(vi.mocked(connectFinish).mock.calls).toEqual([["c0de", "st4te"]]);
    expect(addressAtPost.endsWith("/self-service/credentials")).toBe(true);
    expect(window.location.hash).toBe("");
    expect(notify.ok).toHaveBeenCalledWith(W.signedInToast("midpoint"));
    expect(document.querySelector("[data-finishing]")).toBeNull();
  });

  it("sends a code once, so a second mount of the tab finds nothing to finish", async () => {
    answers([SIGN_IN_ROW], []);
    vi.mocked(connectFinish).mockResolvedValue({ app: "midpoint" });
    window.history.replaceState(null, "", "/self-service/credentials#connect&code=c0de&state=st4te");
    const first = show();
    await waitFor(() => expect(connectFinish).toHaveBeenCalledTimes(1));
    first.unmount();
    show();
    await screen.findByText(W.countWords(1, 1));

    expect(connectFinish).toHaveBeenCalledTimes(1);
  });

  it("prints the server's sentence above the table when the sign-in is someone else's", async () => {
    const SENTENCE = "ivan started this sign-in to midpoint, and you are signed in as alice, so nothing was stored. If someone sent you the link, tell an administrator. To connect your own account, start the sign-in yourself";
    answers([SIGN_IN_ROW], []);
    vi.mocked(connectFinish).mockRejectedValue(new ApiError(SENTENCE, 403));
    window.history.replaceState(null, "", "/self-service/credentials#connect&code=c0de&state=ivans");
    show();

    const block = await waitFor(() => {
      const el = document.querySelector("[data-refused-error]");
      if (!el) throw new Error("no refusal yet");
      return el;
    });
    expect(block.textContent).toBe(W.SUBJECT_SIGN_IN + " refused. " + SENTENCE + ".");
    expect(statusOf("midpoint")).toBe(W.NOT_SIGNED_IN);
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("drops the code and says so when the tab came back signed out", async () => {
    answers([SIGN_IN_ROW], []);
    const setNotice = vi.fn();
    held.state = shell({ session: { kind: "signed-out" }, self: null, setNotice });
    window.history.replaceState(null, "", "/self-service/credentials#connect&code=c0de&state=st4te");
    show();

    await waitFor(() => expect(setNotice).toHaveBeenCalledWith({ tone: "warn", text: W.CAME_BACK_SIGNED_OUT }));
    expect(window.location.hash).toBe("");
    expect(connectFinish).not.toHaveBeenCalled();
    expect(screen.getByText(W.SIGNED_OUT_TITLE)).toBeTruthy();
  });

  it("holds a come-back while the session is still resuming", async () => {
    answers([SIGN_IN_ROW], []);
    vi.mocked(connectFinish).mockResolvedValue({ app: "midpoint" });
    const setNotice = vi.fn();
    held.state = shell({ session: { kind: "booting" }, self: null, setNotice });
    window.history.replaceState(null, "", "/self-service/credentials#connect&code=c0de&state=st4te");
    const view = show();
    await act(async () => {});
    expect(connectFinish).not.toHaveBeenCalled();
    expect(setNotice).not.toHaveBeenCalled();

    held.state = shell({ self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] } });
    view.rerender(<TooltipProvider><CredentialsTab /></TooltipProvider>);
    await waitFor(() => expect(vi.mocked(connectFinish).mock.calls).toEqual([["c0de", "st4te"]]));
  });

  it("says what the provider answered when it said no", async () => {
    answers([SIGN_IN_ROW], []);
    window.history.replaceState(null, "", "/self-service/credentials#connect&error=access_denied");
    show();

    expect(await screen.findByText(W.providerSaidNo("access_denied"))).toBeTruthy();
    expect(connectFinish).not.toHaveBeenCalled();
    expect(window.location.hash).toBe("");
  });

  it("says what happens next for an agent that reaches no server", async () => {
    const user = userEvent.setup();
    answers(ownRows(), []);
    show();
    await screen.findByText(W.countWords(6, 3));

    await user.click(screen.getByRole("button", { name: W.chipAgent(0, AGENT) }));
    expect(await screen.findByText(W.emptyBody(AGENT))).toBeTruthy();
  });

  it("says under the row when the sign-in could not be started", async () => {
    const user = userEvent.setup();
    answers([{ app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false, allow_agents: false }], []);
    vi.mocked(connectStart).mockRejectedValue(new ApiError("no identity provider is configured for midpoint", 409));
    show();
    await screen.findByText(W.countWords(1, 1));

    await user.click(screen.getByRole("button", { name: W.actionName(W.signInWith("keycloak"), "midpoint") }));

    expect(await screen.findByText(/no identity provider is configured for midpoint/)).toBeTruthy();
    expect(leaveFor).not.toHaveBeenCalled();
    expect(statusOf("midpoint")).toBe(W.NOT_SIGNED_IN);
  });

  it("offers the sign-in and reads nothing while the person is signed out", async () => {
    const user = userEvent.setup();
    answers(ownRows());
    held.state = shell({ session: { kind: "signed-out" }, self: null });
    show();

    expect(screen.getByText(W.SIGNED_OUT_TITLE)).toBeTruthy();
    expect(screen.getByText(W.SIGNED_OUT_BODY)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(openSignIn).toHaveBeenCalled();
    expect(selfServers).not.toHaveBeenCalled();
  });

  it("keeps the rows it has under the unreachable line when a read fails", async () => {
    vi.mocked(selfServers).mockImplementation(async (user = "") => {
      if (user) throw new ApiError("unreachable", 0, true);
      return ownRows();
    });
    show();
    await screen.findByText(W.countWords(6, 3));

    expect(document.querySelector("[data-fetch-error]")).toBeTruthy();
    expect(screen.getByText(/could not be read because strazad did not answer/)).toBeTruthy();
    expect(rowOf("github")).toBeTruthy();
    expect(agentRow("github")).toBeNull();
  });

  it("never draws an empty list when the server did not answer at all", async () => {
    vi.mocked(selfServers).mockRejectedValue(new ApiError("unreachable", 0, true));
    show();

    expect(await screen.findByText(/could not be read because strazad did not answer/)).toBeTruthy();
    expect(document.querySelector("[data-data-table]")).toBeNull();
    expect(setCount).toHaveBeenCalledWith("credentials", null);
  });

  it("narrows the table to one owner from the chip strip", async () => {
    const user = userEvent.setup();
    answers(ownRows());
    show();
    await screen.findByText(W.countWords(9, 4));

    await user.click(screen.getByRole("button", { name: W.chipAgent(3, AGENT) }));
    await screen.findByText(W.countWords(3, 1));
    expect(rowOf("github")).toBeNull();
    expect(agentRow("github")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: W.chipYours(6) }));
    await screen.findByText(W.countWords(6, 3));
    expect(agentRow("github")).toBeNull();
  });
});
