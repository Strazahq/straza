import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ServerRow, connectAgents, connectDelete, connectFinish, connectStart, connectToken, selfServers } from "@/lib/api";
import { notify } from "@/lib/notify";
import { refresh } from "@/lib/session";
import { dayWord } from "@/lib/words";
import { leaveFor } from "./come-back";
import { CredentialsTab } from "./credentials-tab";
import type { SelfState } from "./context";
import * as W from "./credential-words";
import { NO_ANSWER } from "./words";

// The Credentials tab against a mocked /v1/self/servers lane and a mocked
// shell: the bands and the one line of every kind of row, the sentence
// behind each row's help icon, the paste that sends the value once and
// keeps none of it, the confirms, the agents switch on the person's own
// row, and the provider sign-in that leaves in this tab and is finished
// when the tab comes back.

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
const LATER = new Date(Date.now() + 30 * 86400000).toISOString().slice(0, 10) + "T00:00:00Z";
const FUTURE = new Date(Date.now() + 3600000).toISOString();
const UPDATED = "2026-09-01T10:00:00Z";

// tok and sig are a token server and a sign-in server as the lane answers
// them, each caller with its own and nothing set.
const tok = (over: Partial<ServerRow> = {}): ServerRow => ({ app: "github", runtime: "remote", kind: "token", agents: "own", reached: true, connected: false, allow_agents: false, ...over });
const sig = (over: Partial<ServerRow> = {}): ServerRow => ({ app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: false, allow_agents: false, ...over });
const shared = (app: string, runtime = "remote"): ServerRow => ({ app, runtime, kind: "static", reached: true, connected: false });

function ownRows(): ServerRow[] {
  return [
    { app: "demo-tools", runtime: "remote", kind: "none", reached: true, connected: false },
    shared("filesystem", "command"),
    tok({ agents: "sponsor" }),
    tok({ app: "gitlab", reached: false, connected: true, fingerprint: "b81e", updated_at: "2026-08-02T10:00:00Z" }),
    tok({ app: "jira", connected: true, fingerprint: "1a2b", expires_at: PAST, updated_at: "2026-08-20T10:00:00Z" }),
    sig(),
    shared("sentry"),
  ];
}

function agentRows(): ServerRow[] {
  return [shared("filesystem", "command"), tok({ agents: "sponsor" }), sig()];
}

// SIGN_IN_ROW is a server the person has not signed in to yet.
const SIGN_IN_ROW: ServerRow = sig();

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
const cellOf = (name: string, column: string) => rowOf(name).querySelector('[data-column="' + column + '"]') as HTMLElement;
const ready = (name: string) => waitFor(() => expect(rowOf(name)).toBeTruthy());

// helpOf opens the help icon beside a row's status and reads its sentence.
async function helpOf(name: string): Promise<string> {
  await userEvent.click(within(cellOf(name, "status")).getByRole("button", { name: "Help: " + W.whyName(name) }));
  return (await screen.findByRole("tooltip")).textContent || "";
}

// switchHelp opens the help icon beside the switch of the person's own row.
async function switchHelp(app: string): Promise<string> {
  await userEvent.click(screen.getByRole("button", { name: "Help: " + W.actionName(W.SWITCH_LABEL, app) }));
  return (await screen.findByRole("tooltip")).textContent || "";
}

// actionsOf reads a row's actions cell in order: "switch" for the switch,
// and the label and the variant of each button.
const actionsOf = (name: string) =>
  [...cellOf(name, "actions").querySelectorAll("button")]
    .filter((b) => !b.hasAttribute("data-help"))
    .map((b) => (b.getAttribute("role") === "switch" ? "switch" : b.textContent + ":" + b.getAttribute("data-variant")));

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
    await ready("github for " + AGENT);

    expect(lines()).toEqual([
      "band:Needs you", "github", "github for " + AGENT, "jira", "midpoint",
      "band:Set", "filesystem", "filesystem for " + AGENT, "sentry",
      "band:Nothing you can do here", "midpoint for " + AGENT,
      "band:Out of reach", "gitlab",
    ]);
    const counts = [...document.querySelectorAll("[data-band]")].map((b) => {
      const n = b.querySelector("[data-band-count]") as HTMLElement;
      expect(n.className).toContain("font-mono");
      return b.getAttribute("data-band") + " " + n.textContent;
    });
    expect(counts).toEqual(["Needs you 4", "Set 3", "Nothing you can do here 1", "Out of reach 1"]);
    expect(vi.mocked(selfServers).mock.calls).toEqual([[""], [AGENT]]);
    // The person's github, jira and midpoint wait, and so does the agent's
    // github, which has no token and no switch to run on.
    expect(setCount).toHaveBeenLastCalledWith("credentials", 4);
    // Whoever must act on these rows is a person, and this page is their
    // way to act, so no sentence here sends them to a command line.
    expect(document.body.textContent).not.toContain("strazactl");
    expect(document.querySelector("[data-row-count]")).toBeNull();
  });

  it("draws a band only when a row is in it", async () => {
    answers([tok({ connected: true })], []);
    show();
    await ready("github");

    expect(lines()).toEqual(["band:Set", "github"]);
  });

  it("says who each row is for, with the agent's tile and name", async () => {
    answers(ownRows());
    show();
    await ready("github for " + AGENT);

    expect(cellOf("github", "for").textContent).toBe(W.YOU);
    expect(cellOf("github", "for").querySelector("[data-kind-icon]")).toBeNull();
    const agent = cellOf("github for " + AGENT, "for");
    expect(agent.textContent).toBe(AGENT);
    expect(agent.querySelector('[data-kind-icon="agent"]')).toBeTruthy();
    expect(cellOf("github", "server").querySelector('[data-kind-icon="server"]')).toBeTruthy();
    expect(cellOf("github", "server").querySelector("[data-server]")?.getAttribute("data-server")).toBe("github");
  });

  type StatusCase = {
    name: string; own: ServerRow[]; agent?: ServerRow[]; row: string;
    uses: string; word: string; tone: string; muted: string | null; help: string | null;
  };
  const STATUS: StatusCase[] = [
    { name: "the person's row out of reach", own: [tok({ reached: false, connected: true, updated_at: UPDATED })], row: "github",
      uses: "Your own token", word: W.OUT_OF_REACH, tone: "plain", muted: null, help: W.leftover("github", "", "token") },
    { name: "an agent's sign-in out of reach", own: [], agent: [sig({ reached: false, connected: true })], row: "midpoint for " + AGENT,
      uses: "A sign-in at keycloak", word: W.OUT_OF_REACH, tone: "plain", muted: null, help: W.leftover("midpoint", AGENT, "sign-in") },
    { name: "a shared secret", own: [shared("sentry")], row: "sentry",
      uses: "One shared secret", word: W.SHARED, tone: "plain", muted: W.SHARED_LINE, help: null },
    { name: "a command server", own: [shared("filesystem", "command")], row: "filesystem",
      uses: "One shared secret", word: W.SHARED_ONLY, tone: "plain", muted: W.SHARED_LINE, help: null },
    { name: "a container server", own: [shared("fetch", "oci")], row: "fetch",
      uses: "One shared secret", word: W.SHARED_ONLY, tone: "plain", muted: W.SHARED_LINE, help: null },
    { name: "the person's sign-in, connected", own: [sig({ connected: true, expires_at: FUTURE, updated_at: UPDATED, scopes: ["openid"] })], row: "midpoint",
      uses: "Your own sign-in at keycloak", word: W.SIGNED_IN, tone: "ok", muted: W.RENEWS_LINE, help: null },
    { name: "the person's sign-in, not connected", own: [sig()], row: "midpoint",
      uses: "Your own sign-in at keycloak", word: W.NOT_SIGNED_IN, tone: "warn", muted: null, help: W.ownNotSignedIn("midpoint", "keycloak") },
    { name: "the person's token, set until a day", own: [tok({ connected: true, set_by: "alice", updated_at: UPDATED, expires_at: LATER, fingerprint: "3f9a" })], row: "github",
      uses: "Your own token", word: W.SET, tone: "ok", muted: W.untilLine(dayWord(LATER)), help: W.setSentence("", dayWord(UPDATED), "you") },
    { name: "the person's token, set with no end", own: [tok({ connected: true, set_by: "alice", updated_at: UPDATED })], row: "github",
      uses: "Your own token", word: W.SET, tone: "ok", muted: W.sinceLine(dayWord(UPDATED)), help: W.setSentence("", dayWord(UPDATED), "you") },
    { name: "the person's token, expired", own: [tok({ connected: true, expires_at: PAST, updated_at: UPDATED })], row: "github",
      uses: "Your own token", word: W.EXPIRED, tone: "danger", muted: dayWord(PAST), help: W.expiredSentence("github", dayWord(PAST), "") },
    { name: "the person's token, not set", own: [tok()], row: "github",
      uses: "Your own token", word: W.NOT_SET, tone: "warn", muted: null, help: W.ownNotSet("github") },
    { name: "an agent's token, set", own: [], agent: [tok({ connected: true, set_by: "bob", updated_at: UPDATED })], row: "github for " + AGENT,
      uses: "Its own token", word: W.SET, tone: "ok", muted: W.sinceLine(dayWord(UPDATED)), help: W.setSentence(AGENT, dayWord(UPDATED), "bob") },
    { name: "an agent's token, expired", own: [], agent: [tok({ connected: true, set_by: "alice", expires_at: PAST })], row: "github for " + AGENT,
      uses: "Its own token", word: W.EXPIRED, tone: "danger", muted: dayWord(PAST), help: W.expiredSentence("github", dayWord(PAST), AGENT) },
    { name: "an agent's token, lent the person's", own: [tok({ agents: "sponsor", connected: true, allow_agents: true, updated_at: UPDATED })], agent: [tok({ agents: "sponsor" })], row: "github for " + AGENT,
      uses: "Your token", word: W.USES_YOURS, tone: "ok", muted: null, help: W.lentSentence(AGENT, "github") },
    { name: "an agent's sign-in, lent the person's", own: [sig({ agents: "sponsor", connected: true, allow_agents: true })], agent: [sig({ agents: "sponsor" })], row: "midpoint for " + AGENT,
      uses: "Your sign-in", word: W.USES_YOURS, tone: "ok", muted: null, help: W.lentSentence(AGENT, "midpoint") },
    { name: "an agent's token, on the shared account", own: [], agent: [tok({ agents: "shared" })], row: "github for " + AGENT,
      uses: "Its own token", word: W.SHARED_ACCOUNT, tone: "plain", muted: null, help: W.sharedAccountSentence(AGENT, "github") },
    { name: "an agent's token, not set, the switch off", own: [tok({ agents: "sponsor", connected: true, allow_agents: false })], agent: [tok({ agents: "sponsor" })], row: "github for " + AGENT,
      uses: "Its own token", word: W.NOT_SET, tone: "warn", muted: null, help: W.agentNotSet(AGENT, "github", true) },
    { name: "an agent's token, not set, each caller its own", own: [], agent: [tok()], row: "github for " + AGENT,
      uses: "Its own token", word: W.NOT_SET, tone: "warn", muted: null, help: W.agentNotSet(AGENT, "github", false) },
    { name: "an agent's sign-in, each caller its own", own: [], agent: [sig()], row: "midpoint for " + AGENT,
      uses: "A sign-in at keycloak", word: W.CANNOT_SIGN_IN, tone: "warn", muted: null, help: W.cannotSignIn(AGENT, "midpoint", false) },
    { name: "an agent's sign-in, the person not signed in", own: [sig({ agents: "sponsor" })], agent: [sig({ agents: "sponsor" })], row: "midpoint for " + AGENT,
      uses: "A sign-in at keycloak", word: W.CANNOT_SIGN_IN, tone: "warn", muted: null, help: W.cannotSignIn(AGENT, "midpoint", true) },
  ];

  it.each(STATUS)("says the status of $name in one line", async ({ own, agent, row, uses, word, tone, muted, help }) => {
    answers(own, agent || []);
    show();
    await ready(row);

    expect(cellOf(row, "uses").textContent).toBe(uses);
    const chip = rowOf(row).querySelector("[data-status]") as HTMLElement;
    expect(chip.textContent).toBe(word);
    expect(chip.getAttribute("data-status")).toBe(word);
    expect(chip.getAttribute("data-tone")).toBe(tone);
    expect(word.split(" ").length).toBeLessThanOrEqual(3);
    const line = cellOf(row, "status").querySelector("[data-status-line]");
    expect(line ? line.textContent : null).toBe(muted);
    if (help === null) {
      expect(within(cellOf(row, "status")).queryByRole("button")).toBeNull();
    } else {
      expect(await helpOf(row)).toBe(help);
    }
    // A row is one line: it has no Detail column and shows neither the
    // fingerprint nor the scopes.
    expect(cellOf(row, "detail")).toBeNull();
    expect(rowOf(row).textContent).not.toContain("3f9a");
    expect(rowOf(row).textContent).not.toContain("openid");
  });

  type ActionCase = { name: string; own: ServerRow[]; agent?: ServerRow[]; row: string; actions: string[] };
  const LENDING = tok({ agents: "sponsor", connected: true, allow_agents: true });
  const ACTIONS: ActionCase[] = [
    { name: "a token out of reach", own: [tok({ reached: false, connected: true })], row: "github", actions: ["Remove:ghost"] },
    { name: "a sign-in out of reach", own: [sig({ reached: false, connected: true })], row: "midpoint", actions: ["Remove:ghost"] },
    { name: "the person's token, not set", own: [tok()], row: "github", actions: ["Paste a token:default"] },
    { name: "an agent's token, not set", own: [], agent: [tok()], row: "github for " + AGENT, actions: ["Paste a token:outline"] },
    { name: "an agent's token, lent the person's", own: [LENDING], agent: [tok({ agents: "sponsor" })], row: "github for " + AGENT, actions: ["Paste a token:outline"] },
    { name: "the person's token, set", own: [tok({ connected: true })], row: "github", actions: ["Replace:ghost", "Remove:ghost"] },
    { name: "the person's token, set, where agents may run on it", own: [tok({ agents: "sponsor", connected: true })], row: "github", actions: ["switch", "Replace:ghost", "Remove:ghost"] },
    { name: "an agent's token, set", own: [LENDING], agent: [tok({ agents: "sponsor", connected: true })], row: "github for " + AGENT, actions: ["Replace:ghost", "Remove:ghost"] },
    { name: "the person's token, expired", own: [tok({ agents: "sponsor", connected: true, expires_at: PAST })], row: "github", actions: ["Paste a new token:default", "Remove:ghost"] },
    { name: "an agent's token, expired", own: [], agent: [tok({ connected: true, expires_at: PAST })], row: "github for " + AGENT, actions: ["Paste a new token:default", "Remove:ghost"] },
    { name: "the person's sign-in, not connected", own: [sig()], row: "midpoint", actions: ["Sign in with keycloak:default"] },
    { name: "the person's sign-in, connected", own: [sig({ connected: true })], row: "midpoint", actions: ["Disconnect:ghost"] },
    { name: "the person's sign-in, connected, where agents may run on it", own: [sig({ agents: "sponsor", connected: true })], row: "midpoint", actions: ["switch", "Disconnect:ghost"] },
    { name: "an agent's sign-in, lent the person's", own: [sig({ agents: "sponsor", connected: true, allow_agents: true })], agent: [sig({ agents: "sponsor" })], row: "midpoint for " + AGENT, actions: [] },
    { name: "an agent's sign-in, not connected", own: [], agent: [sig()], row: "midpoint for " + AGENT, actions: [] },
    { name: "a shared secret", own: [shared("sentry")], row: "sentry", actions: [] },
    { name: "a command server", own: [shared("filesystem", "command")], row: "filesystem", actions: [] },
  ];

  it.each(ACTIONS)("offers the actions of $name", async ({ own, agent, row, actions }) => {
    answers(own, agent || []);
    show();
    await ready(row);

    expect(actionsOf(row)).toEqual(actions);
    if (!actions.length) expect(cellOf(row, "actions").childElementCount).toBe(0);
    for (const b of cellOf(row, "actions").querySelectorAll("button:not([data-help]):not([role=switch])")) {
      expect(b.getAttribute("aria-label")).toBe(W.actionName(b.textContent || "", row));
    }
    // No button of the table is red: the confirm it opens is.
    for (const b of document.querySelectorAll("tbody button")) {
      expect(b.getAttribute("data-variant")).not.toBe("destructive");
      expect(b.className).not.toContain("danger");
    }
  });

  it("puts the switch on the person's own row and never on an agent's", async () => {
    answers([LENDING, sig({ agents: "sponsor", connected: true, allow_agents: false })], [tok({ agents: "sponsor" }), sig({ agents: "sponsor" })]);
    show();
    await ready("github for " + AGENT);

    const switches = [...document.querySelectorAll("[data-agents-switch]")];
    expect(switches.map((s) => s.closest("tr")?.getAttribute("data-credential"))).toEqual(["github", "midpoint"]);
    expect(within(cellOf("github", "actions")).getByText(W.SWITCH_LABEL)).toBeTruthy();
    expect(within(agentRow("github")).queryByRole("switch")).toBeNull();
    expect(within(agentRow("midpoint")).queryByRole("switch")).toBeNull();
    expect(await switchHelp("github")).toBe(W.switchOn([AGENT], "github", "token"));
  });

  it("names the servers that need no credential under the table, and nothing else", async () => {
    answers(ownRows());
    show();
    await ready("midpoint for " + AGENT);

    const foot = document.querySelector("[data-credentials-foot]") as HTMLElement;
    expect([...foot.querySelectorAll("p")].map((p) => p.textContent)).toEqual([W.noneLine(["demo-tools"], "")]);
    expect(document.querySelector('[data-credential="demo-tools"]')).toBeNull();
    // The table is four columns and an action, so it offers no column picker.
    expect(screen.queryByRole("button", { name: "Columns" })).toBeNull();
  });

  it("draws no foot at all when there is nothing to say under the table", async () => {
    answers([tok(), sig()], [tok()]);
    show();
    await ready("github for " + AGENT);

    expect(document.querySelector("[data-credentials-foot]")).toBeNull();
  });

  it("cuts a long server name and a long agent name inside their fixed cells", async () => {
    const APP = "github-enterprise-cloud-for-the-platform-team";
    const LONG = "a-very-long-agent-name-that-runs-past-its-column";
    held.state = shell({ self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [LONG] } });
    answers([tok({ app: APP })], [tok({ app: APP })]);
    show();
    await ready(APP + " for " + LONG);

    const server = cellOf(APP + " for " + LONG, "server").querySelector("[title]") as HTMLElement;
    expect(server.textContent).toBe(APP);
    expect(server.getAttribute("title")).toBe(APP);
    expect(server.className).toContain("truncate");
    expect(server.className).toContain("min-w-0");
    const agent = cellOf(APP + " for " + LONG, "for").querySelector("[title]") as HTMLElement;
    expect(agent.textContent).toBe(LONG);
    expect(agent.getAttribute("title")).toBe(LONG);
    expect(agent.className).toContain("truncate");
    const table = document.querySelector("table") as HTMLTableElement;
    expect(table.className).toContain("table-fixed");
    expect(table.style.minWidth).toBe("1200px");
    expect([...table.querySelectorAll("col")].map((c) => c.style.width)).toEqual(["180px", "200px", "", "240px", "380px"]);
  });

  it("sends a pasted token once, with its expiry, and keeps none of it", async () => {
    const user = userEvent.setup();
    answers([tok({ agents: "sponsor" })], []);
    vi.mocked(connectToken).mockResolvedValue({ fingerprint: "3f9a", expires_at: "2026-12-31T00:00:00Z", set_by: "alice", allow_agents: false });
    show();
    await ready("github");

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
    expect(cellOf("github", "status").querySelector("[data-status-line]")?.textContent).toBe(W.untilLine(dayWord("2026-12-31T00:00:00Z")));
    expect(notify.ok).toHaveBeenCalledWith(W.tokenSaved("github", ""));
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("names the agent in the paste and sends its user", async () => {
    const user = userEvent.setup();
    answers(ownRows());
    vi.mocked(connectToken).mockResolvedValue({ fingerprint: "9c02", set_by: "alice", allow_agents: false });
    show();
    await ready("github for " + AGENT);

    await user.click(within(agentRow("github")).getByRole("button", { name: W.actionName(W.PASTE, "github for " + AGENT) }));
    expect(await screen.findByText(W.pasteTitle("github", false, AGENT))).toBeTruthy();
    await user.type(screen.getByLabelText(W.TOKEN_LABEL), SECRET);
    await user.click(screen.getByRole("button", { name: W.SAVE_TOKEN }));

    await waitFor(() => expect(statusOf("github for " + AGENT)).toBe(W.SET));
    expect(vi.mocked(connectToken).mock.calls).toEqual([["github", SECRET, "", AGENT]]);
    expect(notify.ok).toHaveBeenCalledWith(W.tokenSaved("github", AGENT));
    expect(await helpOf("github for " + AGENT)).toBe(W.setSentence(AGENT, dayWord(new Date().toISOString()), "you"));
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("quotes the server's sentence when a paste is refused and empties the box", async () => {
    const user = userEvent.setup();
    answers([tok({ agents: "sponsor" })], []);
    vi.mocked(connectToken).mockRejectedValue(new ApiError("github refused the token or did not answer (401 Unauthorized). Check the token and try again.", 400));
    show();
    await ready("github");

    await user.click(within(rowOf("github")).getByRole("button", { name: W.actionName(W.PASTE, "github") }));
    await user.type(await screen.findByLabelText(W.TOKEN_LABEL), SECRET);
    await user.click(screen.getByRole("button", { name: W.SAVE_TOKEN }));

    const block = await waitFor(() => {
      const el = document.querySelector("[data-paste-sheet] [data-refused-error]");
      if (!el) throw new Error("no refusal yet");
      return el;
    });
    expect(block.textContent).toBe(W.NOT_SAVED + " Straza refused it: github refused the token or did not answer (401 Unauthorized). Check the token and try again. If this keeps happening, tell your administrator.");
    expect((screen.getByLabelText(W.TOKEN_LABEL) as HTMLInputElement).value).toBe("");
    expect(statusOf("github")).toBe(W.NOT_SET);
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("asks before it removes a token, then sends one DELETE", async () => {
    const user = userEvent.setup();
    answers([tok({ connected: true, fingerprint: "3f9a", set_by: "alice", updated_at: UPDATED })], []);
    vi.mocked(connectDelete).mockResolvedValue({});
    show();
    await ready("github");

    await user.click(screen.getByRole("button", { name: W.actionName(W.REMOVE, "github") }));
    expect(await screen.findByText(W.removeTokenTitle("github", ""))).toBeTruthy();
    expect(screen.getByText(W.removeTokenBody("github", ""))).toBeTruthy();
    expect(connectDelete).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: W.CANCEL }));
    await waitFor(() => expect(screen.queryByText(W.removeTokenTitle("github", ""))).toBeNull());
    expect(statusOf("github")).toBe(W.SET);

    await user.click(screen.getByRole("button", { name: W.actionName(W.REMOVE, "github") }));
    const confirm = await screen.findByRole("button", { name: W.REMOVE_TOKEN });
    // The confirm's button carries the red that no button of the table does.
    expect(confirm.className).toContain("bg-danger");
    await user.click(confirm);
    await waitFor(() => expect(statusOf("github")).toBe(W.NOT_SET));
    expect(vi.mocked(connectDelete).mock.calls).toEqual([["github", ""]]);
    expect(notify.ok).toHaveBeenCalledWith(W.TOKEN_REMOVED);
  });

  it("names the agent when it removes the token set for it", async () => {
    const user = userEvent.setup();
    answers([], [tok({ connected: true, set_by: "alice", updated_at: UPDATED })]);
    vi.mocked(connectDelete).mockResolvedValue({});
    show();
    await ready("github for " + AGENT);

    await user.click(screen.getByRole("button", { name: W.actionName(W.REMOVE, "github for " + AGENT) }));
    expect(await screen.findByText(W.removeTokenTitle("github", AGENT))).toBeTruthy();
    expect(screen.getByText(W.removeTokenBody("github", AGENT))).toBeTruthy();
    await user.click(screen.getByRole("button", { name: W.REMOVE_TOKEN }));
    await waitFor(() => expect(statusOf("github for " + AGENT)).toBe(W.NOT_SET));
    expect(vi.mocked(connectDelete).mock.calls).toEqual([["github", AGENT]]);
  });

  it("drops a row nobody reaches any more once its credential is removed", async () => {
    const user = userEvent.setup();
    answers([tok(), tok({ app: "gitlab", reached: false, connected: true, fingerprint: "b81e", updated_at: "2026-08-02T10:00:00Z" })], []);
    vi.mocked(connectDelete).mockResolvedValue({});
    show();
    await ready("gitlab");

    await user.click(within(rowOf("gitlab")).getByRole("button", { name: W.actionName(W.REMOVE, "gitlab") }));
    expect(await screen.findByText(W.leftoverTitle("gitlab", "token"))).toBeTruthy();
    expect(screen.getByText(W.leftoverBody("gitlab", ""))).toBeTruthy();
    await user.click(screen.getByRole("button", { name: W.REMOVE_TOKEN }));

    await waitFor(() => expect(rowOf("gitlab")).toBeNull());
    expect(vi.mocked(connectDelete).mock.calls).toEqual([["gitlab", ""]]);
    expect(notify.ok).toHaveBeenCalledWith(W.LEFTOVER_GONE);
  });

  it("asks before it disconnects a sign-in, and the switch goes with it", async () => {
    const user = userEvent.setup();
    answers([sig({ agents: "sponsor", connected: true, allow_agents: true, updated_at: UPDATED })], []);
    vi.mocked(connectDelete).mockResolvedValue({});
    show();
    await ready("midpoint");

    expect(screen.getByRole("switch", { name: W.SWITCH_LABEL })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: W.actionName(W.DISCONNECT, "midpoint") }));
    expect(await screen.findByText(W.disconnectTitle("midpoint"))).toBeTruthy();
    expect(screen.getByText(W.disconnectBody("midpoint"))).toBeTruthy();
    await user.click(screen.getByRole("button", { name: W.DISCONNECT }));

    await waitFor(() => expect(statusOf("midpoint")).toBe(W.NOT_SIGNED_IN));
    expect(vi.mocked(connectDelete).mock.calls).toEqual([["midpoint", ""]]);
    expect(notify.ok).toHaveBeenCalledWith(W.DISCONNECTED);
    expect(screen.queryByRole("switch")).toBeNull();
    expect(actionsOf("midpoint")).toEqual(["Sign in with keycloak:default"]);
  });

  it("patches the agents switch once and says what it means", async () => {
    const user = userEvent.setup();
    answers([tok({ agents: "sponsor", connected: true, fingerprint: "3f9a", set_by: "alice", updated_at: UPDATED })], [tok({ agents: "sponsor" })]);
    vi.mocked(connectAgents).mockResolvedValue({ allow_agents: true });
    show();
    await ready("github for " + AGENT);

    expect(await switchHelp("github")).toBe(W.switchOff([AGENT], "github", "token"));
    expect(statusOf("github for " + AGENT)).toBe(W.NOT_SET);
    expect(setCount).toHaveBeenLastCalledWith("credentials", 1);
    await user.click(screen.getByRole("switch", { name: W.SWITCH_LABEL }));

    await waitFor(() => expect(statusOf("github for " + AGENT)).toBe(W.USES_YOURS));
    expect(vi.mocked(connectAgents).mock.calls).toEqual([["github", true, ""]]);
    expect(notify.ok).toHaveBeenCalledWith(W.switchedOn("github"));
    expect(lines()).toEqual(["band:Set", "github", "github for " + AGENT]);
    expect(setCount).toHaveBeenLastCalledWith("credentials", 0);
    expect(await switchHelp("github")).toBe(W.switchOn([AGENT], "github", "token"));
  });

  it("says under the row when the switch was refused", async () => {
    const user = userEvent.setup();
    answers([tok({ agents: "sponsor", connected: true, updated_at: UPDATED })], [tok({ agents: "sponsor" })]);
    vi.mocked(connectAgents).mockRejectedValue(new ApiError("github no longer lets agents run on a sponsor's token", 409));
    show();
    await ready("github");

    await user.click(screen.getByRole("switch", { name: W.SWITCH_LABEL }));

    const block = await waitFor(() => {
      const el = rowOf("github").querySelector("[data-refused-error]");
      if (!el) throw new Error("no refusal yet");
      return el;
    });
    expect(block.textContent).toBe(W.SWITCH_NOT_CHANGED + " Straza refused it: github no longer lets agents run on a sponsor's token. If this keeps happening, tell your administrator.");
    expect(statusOf("github for " + AGENT)).toBe(W.NOT_SET);
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("offers no switch where each caller signs in for themselves", async () => {
    answers([sig({ connected: true, expires_at: FUTURE, updated_at: UPDATED, scopes: ["openid"] })], []);
    show();
    await ready("midpoint");

    expect(statusOf("midpoint")).toBe(W.SIGNED_IN);
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.getByRole("button", { name: W.actionName(W.DISCONNECT, "midpoint") })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Sign in again/ })).toBeNull();
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
    await ready("midpoint");

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
    await ready("midpoint");

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
    expect(block.textContent).toBe(W.SIGN_IN_NOT_FINISHED + " Straza refused it: " + SENTENCE + ". If this keeps happening, tell your administrator.");
    expect(block.closest("tr")).toBeNull();
    await ready("midpoint");
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

  it("says so when the filter leaves no row", async () => {
    const user = userEvent.setup();
    answers(ownRows(), []);
    show();
    await ready("github");

    await user.click(screen.getByRole("button", { name: W.chipAgent(AGENT) + " 0" }));
    expect((await screen.findByText(W.EMPTY_FILTER)).hasAttribute("data-empty-text")).toBe(true);
  });

  it("says under the row when the sign-in could not be started", async () => {
    const user = userEvent.setup();
    answers([sig()], []);
    vi.mocked(connectStart).mockRejectedValue(new ApiError("no identity provider is configured for midpoint", 409));
    show();
    await ready("midpoint");

    await user.click(screen.getByRole("button", { name: W.actionName(W.signInWith("keycloak"), "midpoint") }));

    expect(await within(rowOf("midpoint")).findByText(/no identity provider is configured for midpoint/)).toBeTruthy();
    expect(within(rowOf("midpoint")).getByText(W.SIGN_IN_NOT_STARTED)).toBeTruthy();
    expect(leaveFor).not.toHaveBeenCalled();
    expect(statusOf("midpoint")).toBe(W.NOT_SIGNED_IN);
  });

  it("offers the sign-in and reads nothing while the person is signed out", async () => {
    const user = userEvent.setup();
    answers(ownRows());
    held.state = shell({ session: { kind: "signed-out" }, self: null });
    show();

    expect(screen.getByText(W.SIGNED_OUT_TITLE)).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Sign in" }));
    expect(openSignIn).toHaveBeenCalled();
    expect(selfServers).not.toHaveBeenCalled();
  });

  it("says it is reading, then shows the empty list when no role reaches a server", async () => {
    let answer: (rows: ServerRow[]) => void = () => {};
    vi.mocked(selfServers).mockImplementation((user = "") => (user ? Promise.resolve([]) : new Promise((r) => { answer = r; })));
    show();

    expect(await screen.findByText(W.READING)).toBeTruthy();
    await act(async () => { answer([]); });
    expect(await screen.findByText(W.emptyTitle(""))).toBeTruthy();
    expect(screen.getByText(W.EMPTY_BODY)).toBeTruthy();
    expect(document.querySelector("[data-data-table]")).toBeNull();
    expect(setCount).toHaveBeenLastCalledWith("credentials", 0);
  });

  it("keeps the rows it has under the unreachable line when a read fails", async () => {
    vi.mocked(selfServers).mockImplementation(async (user = "") => {
      if (user) throw new ApiError("unreachable", 0, true);
      return ownRows();
    });
    show();
    await ready("github");

    expect((document.querySelector("[data-read-problem]") as HTMLElement).textContent).toContain(NO_ANSWER);
    expect(document.body.textContent).not.toContain("strazad");
    expect(rowOf("github")).toBeTruthy();
    expect(agentRow("github")).toBeNull();
  });

  it("never draws an empty list when the server did not answer at all", async () => {
    vi.mocked(selfServers).mockRejectedValue(new ApiError("unreachable", 0, true));
    show();

    expect(await screen.findByText(NO_ANSWER, { exact: false })).toBeTruthy();
    expect(document.querySelector("[data-data-table]")).toBeNull();
    expect(setCount).toHaveBeenCalledWith("credentials", null);
  });

  it("offers each owner with the count of their rows, and narrows the table to one", async () => {
    const user = userEvent.setup();
    answers(ownRows());
    show();
    await ready("github for " + AGENT);

    const strip = screen.getByRole("group", { name: W.STRIP_LABEL });
    expect(within(strip).getAllByRole("button").map((b) => b.textContent)).toEqual([
      W.CHIP_ALL + " 9", W.CHIP_YOURS + " 6", W.chipAgent(AGENT) + " 3",
    ]);
    expect(within(strip).getByRole("button", { name: W.CHIP_ALL + " 9" }).getAttribute("aria-pressed")).toBe("true");

    await user.click(within(strip).getByRole("button", { name: W.chipAgent(AGENT) + " 3" }));
    await waitFor(() => expect(rowOf("github")).toBeNull());
    expect(lines()).toEqual([
      "band:Needs you", "github for " + AGENT,
      "band:Set", "filesystem for " + AGENT,
      "band:Nothing you can do here", "midpoint for " + AGENT,
    ]);

    await user.click(within(strip).getByRole("button", { name: W.CHIP_YOURS + " 6" }));
    await waitFor(() => expect(agentRow("github")).toBeNull());
    expect(rowOf("github")).toBeTruthy();
    // The count the tab reports stays the whole of what waits, whatever
    // the filter shows.
    expect(setCount).toHaveBeenLastCalledWith("credentials", 4);
  });

  it("ranks an agent's row against the person's own even while the filter hides it", async () => {
    const user = userEvent.setup();
    answers([LENDING], [tok({ agents: "sponsor" })]);
    show();
    await ready("github for " + AGENT);

    await user.click(screen.getByRole("button", { name: W.chipAgent(AGENT) + " 1" }));
    await waitFor(() => expect(rowOf("github")).toBeNull());
    expect(lines()).toEqual(["band:Set", "github for " + AGENT]);
    expect(statusOf("github for " + AGENT)).toBe(W.USES_YOURS);
  });

  it("shows no filter to a person who sponsors no agent", async () => {
    held.state = shell({ self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] } });
    answers([tok()]);
    show();
    await ready("github");

    expect(screen.queryByRole("group", { name: W.STRIP_LABEL })).toBeNull();
    expect(vi.mocked(selfServers).mock.calls).toEqual([[""]]);
  });
});
