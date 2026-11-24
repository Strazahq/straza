import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type SelfAnswer, type SelfDevice, selfDevices, selfRequests } from "@/lib/api";
import { CLOSE } from "@/lib/approval-words";
import { ApproverError, boundDevice, history, pending } from "./approver-api";
import { SelfContext, type SelfState } from "./context";
import { RequestsTab } from "./requests-tab";
import type { ApproverRow } from "./rows";
import type { Devices } from "./self-devices";
import type { Enrollment } from "./store";
import * as W from "./words";

// What the Requests tab says before this browser is set up, by what the
// person's device list says, and what it says when a read of the requests
// fails. The lanes are mocked at the same seams as the tab's main suite.
vi.mock("./approver-api", async (orig) => ({
  ...(await orig<typeof import("./approver-api")>()),
  pending: vi.fn(),
  history: vi.fn(),
  decide: vi.fn(),
  boundDevice: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfRequests: vi.fn(),
  selfDevices: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({
  ...(await orig<typeof import("@/lib/session")>()),
  token: () => "",
  lose: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();

// sum is a call of the agent alice sponsors, which she may decide, read
// with the sign-in session and so without a challenge.
const sum: ApproverRow = {
  id: "apr_sum", created_at: at(-30), expires_at: at(90),
  requester: { username: "joe-java-developer-agent", kind: "nhi" },
  origin: { actor: "agent_session" }, session_id: "0199c1a2-7e3f-7000-8000-000000000001",
  rule_id: "dev-mcp-sum-approval-showcase", set_name: "dev-guardrails",
  summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get-sum" },
  justification: "", approver_users: ["alice"], args_preview: '{"a":2,"b":40}',
};
const SUM_ROW = "get-sum for joe-java-developer-agent";

const iphone: SelfDevice = { id: "apd_iphone", name: "iPhone", platform: "ios", key_security_level: "hardware", created_at: at(-864000), notifies: true };

const enrollment = {
  deviceId: "apd_browser", deviceToken: "dt-live", tokenExpiresAt: Date.now() + 86400000,
  project: null, user: { id: "u-alice", username: "alice" },
  webpush: null, keys: {} as CryptoKeyPair, deviceName: "Firefox on Windows", enrolledAt: at(-86400),
} as Enrollment;

const BROWSER: SelfAnswer = { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: ["joe-java-developer-agent"] };
const PHONE_ONLY: SelfAnswer = { ...BROWSER, enroll_channels: ["mobile"] };
const SERVER_SENTENCE = "Straza could not read the waiting requests, so it cannot list them. Try again, and tell your administrator if it keeps failing.";

const setWaiting = vi.fn();

function shared(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: BROWSER,
    refreshSelf: async () => undefined,
    enrollment: null,
    setEnrollment: vi.fn(),
    storage: { usable: true, outlook: "durable" },
    openSignIn: () => undefined,
    signedIn: vi.fn(),
    notice: null,
    setNotice: () => undefined,
    sheets: { enable: false, phone: false },
    setSheet: vi.fn(),
    setCount: vi.fn(),
    waiting: null,
    setWaiting,
    servers: null,
    readServers: () => undefined,
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}, home = false, devices?: Devices) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><RequestsTab home={home} devices={devices} /></SelfContext.Provider>
  </TooltipProvider>,
);

const signIn = (decidable: ApproverRow[], mine: ApproverRow[]) =>
  vi.mocked(selfRequests).mockImplementation(((scope: string) => Promise.resolve({ requests: scope === "mine" ? mine : decidable })) as typeof selfRequests);
const listed = (devices: SelfDevice[]) => vi.mocked(selfDevices).mockResolvedValue({ devices });
const rowEnd = async () => {
  const row = await screen.findByRole("button", { name: "Open " + SUM_ROW });
  return () => (row.querySelector("[data-setup-to-answer]") as HTMLElement).textContent;
};

beforeEach(() => {
  vi.clearAllMocks();
  signIn([sum], []);
  listed([iphone]);
  vi.mocked(history).mockResolvedValue({ items: [], next_cursor: "" } as never);
});

describe("a person who approves from another device, before this browser is set up", () => {
  it("ends a row the person may decide in the words to answer on a device, and its dialog keeps the setup", async () => {
    mount();
    const end = await rowEnd();
    await waitFor(() => expect(end()).toBe(W.ANSWER_ON_DEVICE));
    await userEvent.click(screen.getByRole("button", { name: "Open " + SUM_ROW }));
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-needs-device]") as HTMLElement).textContent).toBe(W.ANSWER_ON_DEVICE_LINE);
    const foot = dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement;
    expect(within(foot).getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, W.SETUP_BROWSER]);
  });

  it("ends a card in the same words below the sm breakpoint", async () => {
    const narrow = vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
      matches: query === "(max-width: 39.98rem)", media: query, onchange: null,
      addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
    }) as MediaQueryList);
    mount();
    const end = await rowEnd();
    expect(screen.getByRole("button", { name: "Open " + SUM_ROW }).getAttribute("data-card")).not.toBe(null);
    await waitFor(() => expect(end()).toBe(W.ANSWER_ON_DEVICE));
    narrow.mockRestore();
  });

  it("says a request is answered on a device when nothing waits", async () => {
    signIn([], []);
    mount();
    expect(await screen.findByText(W.WAITS_SHOW_HERE_DEVICE)).toBeTruthy();
    expect(screen.getByText(W.NOTHING_WAITS_CARD)).toBeTruthy();
  });

  it("keeps the phone's sentence for an account that approves from a phone only", async () => {
    signIn([], []);
    mount({ self: PHONE_ONLY });
    await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(W.WAITS_SHOW_HERE_PHONE)).toBeTruthy();
    expect(screen.queryByText(W.WAITS_SHOW_HERE_DEVICE)).toBe(null);
  });

  it.each([
    ["while the devices are read", () => vi.mocked(selfDevices).mockReturnValue(new Promise(() => undefined))],
    ["when their read failed", () => vi.mocked(selfDevices).mockRejectedValue(new ApiError("unreachable", 0, true))],
    ["when the list is empty", () => listed([])],
  ])("keeps the words to set up a device %s", async (_what, arrange) => {
    arrange();
    mount();
    const end = await rowEnd();
    await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(end()).toBe(W.SETUP_TO_ANSWER);
    await userEvent.click(screen.getByRole("button", { name: "Open " + SUM_ROW }));
    expect(((await screen.findByRole("dialog")).querySelector("[data-needs-device]") as HTMLElement).textContent).toBe(W.ANSWER_NEEDS_BROWSER);
  });

  it("takes Home's read of the devices in the home view and reads none of its own", async () => {
    mount({}, true, { list: [iphone], failed: false, reread: () => undefined });
    const end = await rowEnd();
    expect(end()).toBe(W.ANSWER_ON_DEVICE);
    expect(vi.mocked(selfDevices)).not.toHaveBeenCalled();
  });

  it("reads no devices once this browser is set up", async () => {
    vi.mocked(boundDevice).mockReturnValue(null);
    vi.mocked(pending).mockResolvedValue([] as never);
    mount({ enrollment });
    await waitFor(() => expect(vi.mocked(pending)).toHaveBeenCalled());
    expect(vi.mocked(selfDevices)).not.toHaveBeenCalled();
  });
});

describe("a failed read of the requests", () => {
  afterEach(() => window.history.replaceState(null, "", "/"));

  it.each([
    ["before setup, a whole sentence of the server", () => vi.mocked(selfRequests).mockRejectedValue(new ApiError(SERVER_SENTENCE, 500)), {}, SERVER_SENTENCE],
    ["set up, a whole sentence of the server", () => vi.mocked(pending).mockRejectedValue(new ApproverError(SERVER_SENTENCE, { status: 500 })), { enrollment }, SERVER_SENTENCE],
    ["a message that does not open with a capital letter", () => vi.mocked(pending).mockRejectedValue(new ApproverError("the approval store is unavailable.", { status: 500 })), { enrollment }, W.readProblem("the approval store is unavailable")],
    ["a message that does not end in a full stop", () => vi.mocked(pending).mockRejectedValue(new ApproverError("Service Unavailable", { status: 503 })), { enrollment }, W.readProblem("Service Unavailable")],
  ] as [string, () => void, Partial<SelfState>, string][])("shows %s as it is, and wraps any other", async (_what, arrange, over, sentence) => {
    arrange();
    mount(over);
    expect((await screen.findByRole("alert")).textContent).toBe(sentence);
  });

  it("tells the shell the read failed, so Home's first card says so", async () => {
    vi.mocked(selfRequests).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    await screen.findByRole("alert");
    expect(setWaiting).toHaveBeenLastCalledWith("error");
  });

  it.each([
    ["the sign-in session ended", () => vi.mocked(selfRequests).mockRejectedValue(new ApiError("session expired", 401)), {}],
    ["an administrator removed this browser", () => vi.mocked(pending).mockRejectedValue(new ApproverError("revoked", { status: 401, revoked: true })), { enrollment }],
  ] as [string, () => void, Partial<SelfState>][])("tells the shell nothing failed when %s, which another part of the page says", async (_what, arrange, over) => {
    arrange();
    mount(over);
    await waitFor(() => expect(setWaiting).toHaveBeenCalled());
    expect(setWaiting).toHaveBeenLastCalledWith(null);
    expect(screen.queryByRole("alert")).toBe(null);
  });
});

describe("a person whose role lets them approve from a phone only, before any browser is set up", () => {
  const open = async () => {
    await userEvent.click(await screen.findByRole("button", { name: "Open " + SUM_ROW }));
    const dialog = await screen.findByRole("dialog");
    const foot = dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement;
    return { line: (dialog.querySelector("[data-needs-device]") as HTMLElement).textContent, buttons: within(foot).getAllByRole("button").map((b) => b.textContent) };
  };

  it("with a phone: the row and the dialog say to answer on the phone, and no setup of a browser is offered", async () => {
    mount({ self: PHONE_ONLY });
    const end = await rowEnd();
    await waitFor(() => expect(end()).toBe(W.ANSWER_ON_PHONE));
    expect(await open()).toEqual({ line: W.ANSWER_ON_PHONE_LINE, buttons: [CLOSE] });
  });

  it("with no phone yet: the row and the dialog say to add a phone, and no setup of a browser is offered", async () => {
    listed([]);
    mount({ self: PHONE_ONLY });
    const end = await rowEnd();
    await waitFor(() => expect(end()).toBe(W.ADD_PHONE_TO_ANSWER));
    expect(await open()).toEqual({ line: W.ANSWER_NEEDS_PHONE, buttons: [CLOSE] });
  });
});
