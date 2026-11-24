import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { webcrypto } from "node:crypto";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApprovalRow, selfRequests } from "@/lib/api";
import { type Phase, phaseOf } from "@/lib/approval-model";
import {
  APPROVE,
  CLOSE,
  DENY,
  FILTER_LABEL,
  OPEN_SESSION,
  REASON_LABEL,
  REQUEST_COLUMN,
  RUNS_WHERE,
  RUNS_WHERE_ALONE,
  WINDOW_CLOSED,
  YOU_SPONSOR,
  decideAction,
  decidedLine,
  notYours,
  outcomeOf,
  waitingCount,
} from "@/lib/approval-words";
import { ApproverError, boundDevice, decide, history, pending } from "./approver-api";
import { SelfContext, type SelfState } from "./context";
import { RequestsTab } from "./requests-tab";
import { navigate } from "./router";
import type { ApproverRow } from "./rows";
import { decideMessageWithReason } from "./sign";
import type { Enrollment } from "./store";
import * as W from "./words";

// The approver lane is the tab's whole server, so the four calls it makes
// are the seam every case drives. Everything else in the module, the error
// class among it, stays real.
vi.mock("./approver-api", async (orig) => ({
  ...(await orig<typeof import("./approver-api")>()),
  pending: vi.fn(),
  history: vi.fn(),
  decide: vi.fn(),
  boundDevice: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfRequests: vi.fn(), selfDevices: vi.fn(async () => ({ devices: [] })),
}));
vi.mock("@/lib/session", async (orig) => ({
  ...(await orig<typeof import("@/lib/session")>()),
  token: () => "",
  lose: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

// jsdom's crypto has no subtle, and a reason is signed as its sha256.
if (!globalThis.crypto || !globalThis.crypto.subtle) {
  Object.defineProperty(globalThis, "crypto", { value: webcrypto, configurable: true });
}

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();

// CURSOR is the opaque keyset cursor's shape, base64url of "1\x00<id>".
const CURSOR = "MQBhcHJfMDFK";

// The fixture: a call of the agent alice sponsors, held in place
// with a challenge; a standing approval of another agent she sponsors, also
// with a challenge; the shell call alice raised herself, routed to a role
// and carrying no challenge; and one decided row in the history feed.
let sum: ApproverRow;
let env: ApproverRow;
let own: ApproverRow;
let done: ApproverRow;

function build() {
  sum = {
    id: "apr_sum", created_at: at(-30), expires_at: at(90),
    requester: { username: "joe-java-developer-agent", kind: "nhi" },
    origin: { actor: "agent_session" }, session_id: "0199c1a2-7e3f-7000-8000-000000000001",
    rule_id: "dev-mcp-sum-approval-showcase", set_name: "dev-guardrails",
    summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get-sum" },
    justification: "Adding the two build durations the user asked about.",
    challenge: "chal-sum", approver_users: ["alice"], args_preview: '{"a":2,"b":40}',
  };
  env = {
    ...sum, id: "apr_env", class: "ticket", created_at: at(-3600), expires_at: at(79200),
    requester: { username: "sam-sre-agent", kind: "nhi" },
    rule_id: "dev-mcp-env-ticket", summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get-env" },
    challenge: "chal-env",
  };
  own = {
    ...sum, id: "apr_own", created_at: at(-75), expires_at: at(45),
    requester: { username: "alice", kind: "human" },
    rule_id: "hold-printf", set_name: "lf-q4-hold", summary: { tool: "shell.exec" },
    args_preview: "printf lf-q4-x", justification: "",
    challenge: undefined, approver_users: undefined, approver_roles: ["sec-approvers"],
  };
  done = {
    ...sum, id: "apr_done", created_at: at(-7200), expires_at: at(-7080), challenge: undefined,
    state: "denied", decided_by: "alice", decided_at: at(-7100),
    decided_via: { surface: "browser", device_id: "apd_browser" }, decided_reason: "walk cleanup",
  };
}

const device = {
  id: "apd_browser",
  token: "dt-live",
  sign: vi.fn(async (m: string) => "sig:" + m),
  onToken: vi.fn(async () => undefined),
  onRevoked: vi.fn(async () => undefined),
};

const enrollment = {
  deviceId: "apd_browser", deviceToken: "dt-live", tokenExpiresAt: Date.now() + 86400000,
  project: { id: "prj_01", name: "acme-prod" }, user: { id: "u-alice", username: "alice" },
  webpush: null, keys: {} as CryptoKeyPair, deviceName: "Firefox on Windows", enrolledAt: at(-86400),
} as Enrollment;

const setCount = vi.fn();
const setWaiting = vi.fn();
const setEnrollment = vi.fn();

function shared(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: ["joe-java-developer-agent"] },
    refreshSelf: async () => undefined,
    enrollment,
    setEnrollment,
    storage: { usable: true, outlook: "durable" },
    openSignIn: () => undefined,
    signedIn: vi.fn(),
    notice: null,
    setNotice: () => undefined,
    sheets: { enable: false, phone: false },
    setSheet: vi.fn(),
    setCount,
    waiting: null,
    setWaiting,
    servers: null,
    readServers: () => undefined,
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}, home = false) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><RequestsTab home={home} /></SelfContext.Provider>
  </TooltipProvider>,
);

// scopes answers the two pending reads, decidable first, the way the server
// does: only a decidable row carries a challenge.
function scopes(decidable: ApproverRow[], mine: ApproverRow[]) {
  vi.mocked(pending).mockImplementation(((scope: string) => Promise.resolve(scope === "mine" ? mine : decidable)) as typeof pending);
}

const feed = (items: ApproverRow[], next = "") =>
  vi.mocked(history).mockImplementation((() => Promise.resolve({ items, next_cursor: next })) as typeof history);

const openRow = (name: string) => screen.getByRole("button", { name: "Open " + name });
const SUM_ROW = "get-sum for joe-java-developer-agent";
const OWN_ROW = "printf lf-q4-x for alice";
const counted = () => (document.querySelector("[data-row-count]") as HTMLElement).textContent;
const scopeCalls = () => vi.mocked(pending).mock.calls.map((c) => c[0]);
const heads = () => [...document.querySelectorAll("thead th")].map((th) => th.textContent);
const seg = (label: string) => screen.getByRole("button", { name: label });
// part waits for a part of the page to be drawn and answers it.
const part = (selector: string) => waitFor(() => {
  const el = document.querySelector(selector) as HTMLElement | null;
  if (!el) throw new Error(selector + " is not drawn yet");
  return el;
});

// worker stands in for the service worker the page listens to; jsdom has
// none, and the relayed push is the lane that makes a notification a row.
const worker = { fn: null as ((ev: MessageEvent) => void) | null };
const relay = () => worker.fn!({ data: { type: "straza-approvals", kind: "decide" } } as MessageEvent);

// ask opens the request dialog from a row's own button and answers the
// dialog, since a row's button decides nothing on its own.
async function ask(verdict: "approve" | "deny", name = SUM_ROW) {
  await screen.findByRole("button", { name: "Open " + name });
  await userEvent.click(within(openRow(name)).getByRole("button", { name: (verdict === "approve" ? APPROVE : DENY) + " " + name }));
  return screen.findByRole("dialog");
}
const press = (dialog: HTMLElement, verdict: "approve" | "deny") =>
  userEvent.click(within(dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement).getByRole("button", { name: verdict === "approve" ? APPROVE : DENY }));

describe("the self-service requests tab", () => {
  beforeEach(() => {
    build();
    vi.clearAllMocks();
    vi.mocked(boundDevice).mockReturnValue(device);
    vi.mocked(decide).mockResolvedValue({ state: "approved" } as never);
    scopes([sum, env], [own]);
    feed([done]);
    worker.fn = null;
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: {
        addEventListener: (_type: string, fn: (ev: MessageEvent) => void) => { worker.fn = fn; },
        removeEventListener: () => { worker.fn = null; },
      },
    });
  });

  it.each([
    ["signed out", { session: { kind: "signed-out" } }, W.SIGN_IN_FOR_REQUESTS, W.SIGN_IN_FOR_REQUESTS_BODY],
    ["signed in before Straza said what the account may set up", { self: null }, W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE],
    ["signed in when that read failed", { self: "error" }, W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE],
    ["signed in with an account that may set up a browser", {}, W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE_BROWSER],
    ["signed in with an account that may set up a browser or a phone", { self: { username: "alice", user_kind: "human", enroll_channels: ["browser", "mobile"], sponsored: [] } }, W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE],
    ["signed in with an account that approves from a phone only", { self: { username: "alice", user_kind: "human", enroll_channels: ["mobile"], sponsored: [] } }, W.NOTHING_WAITS_CARD, W.WAITS_SHOW_HERE_PHONE],
    ["signed in with an account that has no role to approve", { self: { username: "alice", user_kind: "human", enroll_channels: [], sponsored: [] } }, W.NOTHING_WAITS_CARD, W.NOT_ENROLLED_NO_ROLE],
  ] as [string, Partial<SelfState>, string, string][])("says what would be here before setup when nothing waits, %s", async (_what, over, title, body) => {
    vi.mocked(selfRequests).mockResolvedValue({ requests: [] });
    mount({ enrollment: null, ...over });
    expect(await screen.findByText(title)).toBeTruthy();
    expect(screen.getByText(body)).toBeTruthy();
    expect(screen.queryByText("Requests show up after setup")).toBe(null);
    expect(vi.mocked(pending)).not.toHaveBeenCalled();
  });

  it("offers Deny and Approve on the challenged rows and nowhere else", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(within(openRow(SUM_ROW)).getByRole("button", { name: "Approve get-sum for joe-java-developer-agent" })).toBeTruthy();
    expect(within(openRow(SUM_ROW)).getByRole("button", { name: "Deny get-sum for joe-java-developer-agent" })).toBeTruthy();
    expect(within(openRow("get-env for sam-sre-agent")).getByRole("button", { name: /^Approve/ })).toBeTruthy();
    expect(within(openRow(OWN_ROW)).queryByRole("button")).toBe(null);
    expect(within(openRow(OWN_ROW)).getByText("sec-approvers decides")).toBeTruthy();
  });

  it("counts the rows that wait for the count line, hands the shell the ones that are the reader's to decide, with nothing under the queue", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(counted()).toBe(waitingCount(3, 2));
    await waitFor(() => expect(setWaiting).toHaveBeenCalled());
    expect(setWaiting.mock.lastCall![0].map((r: ApprovalRow) => r.id)).toEqual(["apr_sum", "apr_env"]);
    expect(setCount).not.toHaveBeenCalled();
    expect(scopeCalls().slice(0, 2)).toEqual(["decidable", "mine"]);
    expect(document.querySelector("[data-queue-legend]")).toBe(null);
  });

  it("draws the person's columns: when it was asked, who asked with its tile and sponsor, the call, the time left and the buttons", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(heads()).toEqual([REQUEST_COLUMN.asked, REQUEST_COLUMN.who, REQUEST_COLUMN.call, REQUEST_COLUMN.left, ""]);
    const row = openRow(SUM_ROW);
    expect((row.querySelector("[data-who-name]") as HTMLElement).className).toContain("font-mono");
    expect((row.querySelector("[data-sponsor]") as HTMLElement).textContent).toBe(YOU_SPONSOR);
    // An AI agent's tile is the only teal one.
    expect(row.querySelector("[data-kind-icon=agent]")).not.toBe(null);
    expect(row.querySelector("[data-kind-icon=agent]")!.parentElement!.className).toContain("bg-teal-bg");
    const mine = openRow(OWN_ROW);
    expect(mine.querySelector("[data-kind-icon=person]")!.parentElement!.className).not.toContain("teal");
    expect(mine.querySelector("[data-sponsor]")).toBe(null);
    expect((row.querySelector("[data-left]") as HTMLElement).textContent).toMatch(/^1 m [23]\d s left$/);
    expect((document.querySelector("[data-slot=table]") as HTMLElement).style.minWidth).toBe("700px");
  });

  it("drains a bar under the time left of a waiting row, hidden from assistive technology", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    const bar = openRow(SUM_ROW).querySelector("[data-drain]") as HTMLElement;
    expect(bar.getAttribute("aria-hidden")).toBe("true");
    // The hold asked 30 s ago and runs out in 90 s, so 90 of 120 seconds are left.
    expect(parseInt((bar.firstElementChild as HTMLElement).style.width, 10)).toBeGreaterThanOrEqual(74);
    expect(openRow("get-env for sam-sre-agent").querySelector("[data-drain]")).not.toBe(null);
  });

  it("shows the outcome under its own header on the Decided filter, and both in one column under All", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(seg("Decided"));
    await waitFor(() => expect(heads()).toEqual([REQUEST_COLUMN.asked, REQUEST_COLUMN.who, REQUEST_COLUMN.call, REQUEST_COLUMN.outcome, ""]));
    const past = await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect((past.querySelector("[data-outcome]") as HTMLElement).textContent).toBe("denied");
    expect((past.querySelector("[data-outcome-line]") as HTMLElement).textContent).toBe("you denied from this browser");

    await userEvent.click(seg("All"));
    await waitFor(() => expect(heads()).toEqual([REQUEST_COLUMN.asked, REQUEST_COLUMN.who, REQUEST_COLUMN.call, REQUEST_COLUMN.outcome, ""]));
    const cells = [...document.querySelectorAll("td[data-column=state]")];
    expect(cells.filter((c) => c.querySelector("[data-left]")).length).toBe(3);
    expect(cells.filter((c) => c.querySelector("[data-outcome]")).length).toBe(1);
  });

  it("tells the person the decision is theirs and leaves out every console door", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open " + SUM_ROW }));
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-who]") as HTMLElement).textContent)
      .toBe("You, as joe-java-developer-agent's sponsor. Nobody else.");
    expect(within(dialog).queryByRole("button", { name: OPEN_SESSION })).toBe(null);
    expect(within(dialog).queryByRole("button", { name: "dev-guardrails" })).toBe(null);
    expect(within(dialog).getByText("dev-guardrails")).toBeTruthy();
  });

  it("tells a person who decides as a holder of the role who the sponsor is, though the page's seat carries no roles", async () => {
    scopes([{ ...sum, approver_users: ["tomas"], approver_roles: ["sec-approvers"] }, env], [own]);
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open " + SUM_ROW }));
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-who]") as HTMLElement).textContent)
      .toBe("You, as a holder of sec-approvers, or tomas, the sponsor. The first to answer decides.");
  });

  it("opens the console areas for a person who may read them", async () => {
    mount({ session: { kind: "signed-in", user: "alice", grants: "full", servers: 0 } });
    await userEvent.click(await screen.findByRole("button", { name: "Open " + SUM_ROW }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: OPEN_SESSION })).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "dev-guardrails" })).toBeTruthy();
  });

  // An MCP call can come from the hook too, so the lane the server names
  // decides the words, and a row from an older server keeps the guess from
  // the call.
  it.each([
    ["a hook hold on an MCP call says it runs on the agent's retry", { lane: "hook" }, "The call runs once, when the agent retries it soon after your yes."],
    ["a gateway hold says it runs now", { lane: "gateway" }, "The call runs now, once."],
    ["an MCP row from an older server keeps the gateway words", {}, "The call runs now, once."],
  ])("words the decision by the row's lane: %s", async (_what, patch, sentence) => {
    scopes([{ ...sum, ...patch }, env], [own]);
    mount();
    const dialog = await ask("approve");
    expect((dialog.querySelector("[data-if=approve] p") as HTMLElement).textContent).toBe(sentence);
  });

  it.each([
    ["an MCP hold the server names as the hook's", { lane: "hook" }, "get-sum", "The agent was told to retry the call after your answer.", "on its own machine"],
    ["an MCP hold the server names as the gateway's", { lane: "gateway" }, "get-sum", "The agent is waiting for your answer.", "through the MCP gateway"],
    ["a ticket an approval_request asked for a command", { lane: "gateway", class: "ticket", summary: { tool: "shell.exec" }, args_preview: "make release" }, "make release", "The agent moved on. It runs this only after a yes.", "on its own machine"],
  ])("says what the agent does and where the call runs for %s", async (_what, patch, call, says, where) => {
    scopes([{ ...sum, ...patch } as ApproverRow, env], [own]);
    mount();
    const name = call + " for joe-java-developer-agent";
    await screen.findByRole("button", { name: "Open " + name });
    await userEvent.click(openRow(name));
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-kind-says]") as HTMLElement).textContent).toBe(says);
    expect((dialog.querySelector("[data-meta]") as HTMLElement).textContent!.split(" · ")[0]).toBe(where);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    // The list's chip says where the call runs by the same rule as the
    // request's own line, in words that stand on their own.
    const chip = openRow(name).querySelector("[data-where]") as HTMLElement;
    await userEvent.hover(chip);
    await waitFor(() => {
      const id = chip.getAttribute("aria-describedby");
      expect(id).toBeTruthy();
      expect(document.getElementById(id as string)?.textContent).toBe(where === RUNS_WHERE.hook ? RUNS_WHERE_ALONE.hook : RUNS_WHERE_ALONE.gateway);
    });
  });

  it("opens the dialog from a row's Approve and posts nothing until the dialog's own Approve, then exactly one decision signed over the row's id, its challenge and the reason", async () => {
    mount();
    const dialog = await ask("approve");
    expect(dialog.getAttribute("data-request-dialog")).toBe("apr_sum");
    expect(screen.queryByRole("alertdialog")).toBe(null);
    await userEvent.type(within(dialog).getByLabelText(REASON_LABEL), "the freeze is over");
    expect(vi.mocked(decide)).not.toHaveBeenCalled();

    await press(dialog, "approve");
    await waitFor(() => expect(vi.mocked(decide)).toHaveBeenCalledTimes(1));
    const body = vi.mocked(decide).mock.calls[0][0];
    expect(body.request_id).toBe("apr_sum");
    expect(body.verdict).toBe("approve");
    expect(body.challenge).toBe("chal-sum");
    expect(body.reason).toBe("the freeze is over");
    expect(Math.abs(body.ts - Math.floor(Date.now() / 1000))).toBeLessThan(60);
    expect(body.signature).toBe("sig:" + await decideMessageWithReason("apr_sum", "approve", "chal-sum", body.ts, "the freeze is over"));
    // The decision closes the dialog and reloads the queue it came from.
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    await waitFor(() => expect(scopeCalls().filter((s) => s === "decidable").length).toBeGreaterThan(1));
  });

  it("refuses a reason the server would refuse for its length, before anything is signed", async () => {
    mount();
    const dialog = await ask("deny");
    // 260 diacritics are 260 characters, which the box allows, and 520
    // bytes, which the server does not.
    await userEvent.click(within(dialog).getByLabelText(REASON_LABEL));
    await userEvent.paste("č".repeat(260));
    await press(dialog, "deny");
    expect((await within(dialog).findByRole("alert")).textContent).toContain(W.REASON_TOO_LONG);
    expect(vi.mocked(decide)).not.toHaveBeenCalled();
    expect(device.sign).not.toHaveBeenCalled();
  });

  it("retries a refused challenge exactly once, with the fresh nonce and never with the key in doubt", async () => {
    vi.mocked(decide)
      .mockRejectedValueOnce(new ApproverError("the challenge was rejected", { status: 401, code: "challenge_rejected" }))
      .mockResolvedValue({ state: "denied" } as never);
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    scopes([{ ...sum, challenge: "chal-sum-2" }, env], [own]);

    const dialog = await ask("deny");
    await press(dialog, "deny");

    await waitFor(() => expect(vi.mocked(decide)).toHaveBeenCalledTimes(2));
    expect(vi.mocked(decide).mock.calls[0][0].challenge).toBe("chal-sum");
    expect(vi.mocked(decide).mock.calls[1][0].challenge).toBe("chal-sum-2");
    expect(device.onRevoked).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
  });

  // Each refusal of a decision speaks to the person in a sentence of its
  // own. A refusal leaves the buttons, a settled answer ends the decision.
  it.each([
    ["Straza did not answer", () => new ApproverError("the server is unreachable", { unreachable: true }), "Approve request not confirmed. " + W.DECIDE_UNCONFIRMED, false],
    ["the key is no longer accepted", () => new ApproverError("HTTP 401", { status: 401 }), "Approve request refused. " + W.CREDENTIAL_REFUSED, false],
    ["the signature fails twice", () => new ApproverError("the challenge was rejected", { status: 401, code: "challenge_rejected" }), "Approve request refused. " + W.SIGNATURE_REFUSED, false],
    ["any other refusal", () => new ApproverError("the approval store is unavailable.", { status: 500 }), "Approve request refused: the approval store is unavailable. Try again, and tell your administrator if it keeps happening.", false],
    ["the request is gone", () => new ApproverError("HTTP 404", { status: 404 }), W.REQUEST_GONE, true],
    ["the browser was revoked", () => new ApproverError("revoked", { status: 401, revoked: true }), W.NOT_ENROLLED_NOW, true],
    ["it was settled first", () => new ApproverError("HTTP 409", { status: 409, data: { state: "approved" } }), W.alreadySettled("approved"), true],
    ["the window closed", () => new ApproverError("the approval window closed", { status: 410 }), WINDOW_CLOSED, true],
    ["it is no longer the person's", () => new ApproverError("you are not an approver for this request.", { status: 403 }), notYours("you are not an approver for this request"), true],
  ])("says so when %s", async (_what, err, words, settled) => {
    vi.mocked(decide).mockRejectedValue(err());
    mount();
    const dialog = await ask("approve");
    await press(dialog, "approve");
    const foot = () => within(dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement);
    if (settled) {
      await waitFor(() => expect((dialog.querySelector("[data-final]") as HTMLElement).textContent).toBe(words));
      expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE]);
      return;
    }
    expect((await within(dialog).findByRole("alert")).textContent).toBe(words);
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, DENY, APPROVE]);
  });

  it("says this browser is no longer set up when no key is bound, before anything is sent", async () => {
    vi.mocked(boundDevice).mockReturnValue(null);
    mount();
    const dialog = await ask("approve");
    await press(dialog, "approve");
    await waitFor(() => expect((dialog.querySelector("[data-final]") as HTMLElement).textContent).toBe(W.NOT_ENROLLED_NOW));
    expect(vi.mocked(decide)).not.toHaveBeenCalled();
  });

  // A failed read is one plain banner with the lane's sentence and nothing
  // of the console's heading.
  it.each([
    ["Straza did not answer", () => new ApproverError("the server is unreachable, so the state of your approvals cannot be known", { unreachable: true }), W.NO_ANSWER],
    ["the key is no longer known", () => new ApproverError("the device token is not known here", { status: 401, code: "token_invalid" }), W.keyRejected("the device token is not known here")],
    ["the answer is only an HTTP status", () => new ApproverError("HTTP 502", { status: 502 }), W.NO_ANSWER],
    ["the server says why", () => new ApproverError("the approval store is unavailable.", { status: 500 }), W.readProblem("the approval store is unavailable")],
  ])("shows a plain banner when the first read fails because %s", async (_what, err, sentence) => {
    vi.mocked(pending).mockRejectedValue(err());
    mount();
    const block = await screen.findByRole("alert");
    expect(block.textContent).toBe(sentence);
    expect(block.textContent).not.toContain("unreachable, state unknown");
    expect(document.querySelector("[data-fetch-error]")).toBe(null);
    await waitFor(() => expect(setWaiting).toHaveBeenCalledWith("error"));
    // A rejected key is not a revoked one: nothing local is dropped.
    expect(setEnrollment).not.toHaveBeenCalled();
  });

  it("keeps the rows under the banner when a later read fails, with the time of the last read that worked", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    vi.mocked(pending).mockRejectedValue(new ApproverError("unreachable", { unreachable: true }));
    relay();
    const block = await screen.findByRole("alert");
    expect(block.textContent).toMatch(new RegExp("^" + W.NO_ANSWER.replace(/\./g, "\\.") + " Last read at \\d{1,2}:\\d{2}:\\d{2}\\.$"));
    expect(openRow(SUM_ROW)).toBeTruthy();
  });

  it("words a failed read of the decided rows in the person's sentence, never inside the console's", async () => {
    vi.mocked(history).mockRejectedValue(new ApproverError("the history is unavailable", { status: 500 }));
    mount();
    const block = await screen.findByRole("alert");
    expect(block.textContent).toMatch(new RegExp("^" + W.readProblem("the history is unavailable").replace(/\./g, "\\.") + " Last read at "));
    expect(block.textContent).not.toContain("could not be read:  ");
    expect(block.textContent).not.toContain("The request list");
  });

  it("says nothing waits on an empty Waiting filter, with no sentence under it", async () => {
    scopes([], []);
    mount();
    expect(await screen.findByText(W.NOTHING_WAITS)).toBeTruthy();
    expect((document.querySelector("[data-empty-state]") as HTMLElement).textContent).toBe(W.NOTHING_WAITS);
  });

  it("re-reads the queue when the worker relays a push", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await waitFor(() => expect(scopeCalls()).toEqual(["decidable", "mine"]));
    expect(worker.fn).toBeTruthy();

    // Coming back to the window right after a read is throttled, so
    // switching windows is never a fetch storm.
    window.dispatchEvent(new Event("focus"));
    await new Promise((r) => setTimeout(r, 20));
    expect(scopeCalls()).toEqual(["decidable", "mine"]);

    relay();
    await waitFor(() => expect(scopeCalls().filter((s) => s === "decidable").length).toBe(2));
    expect(scopeCalls().filter((s) => s === "mine").length).toBe(2);

    worker.fn!({ data: { type: "something-else" } } as MessageEvent);
    await new Promise((r) => setTimeout(r, 20));
    expect(scopeCalls().filter((s) => s === "decidable").length).toBe(2);
  });

  it("reads the decided rows from the history feed, the first page with no cursor", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await waitFor(() => expect(vi.mocked(history)).toHaveBeenCalled());
    expect(vi.mocked(history).mock.calls[0][0]).toBe("");

    await userEvent.click(seg("Decided"));
    const row = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect((row.querySelector("[data-outcome-line]") as HTMLElement).textContent).toBe("you denied from this browser");
    expect(screen.queryByRole("button", { name: "Open " + OWN_ROW })).toBe(null);
  });

  it("draws no column picker and no loaded line, and its search box names requests alone", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(seg("Decided"));
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(screen.queryByRole("button", { name: "Columns" })).toBe(null);
    expect(document.querySelector("[data-loaded]")).toBe(null);
    expect(screen.getByLabelText("Search requests").getAttribute("placeholder")).toBe("Search requests");
  });

  it("asks for the older page with the server's own cursor and appends it", async () => {
    const older = { ...done, id: "apr_old", summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get-time" } };
    vi.mocked(history).mockImplementation(((cursor: string) =>
      Promise.resolve(cursor ? { items: [older], next_cursor: "" } : { items: [done], next_cursor: CURSOR })) as typeof history);
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(seg("Decided"));
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));

    await waitFor(() => expect(screen.getByRole("button", { name: "Open get-time for joe-java-developer-agent" })).toBeTruthy());
    expect(vi.mocked(history).mock.calls.map((c) => c[0])).toEqual(["", CURSOR]);
    // The first page is still there: a page appends, it never replaces.
    expect(screen.getByRole("button", { name: "Open get-sum for joe-java-developer-agent" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load more" })).toBe(null);
  });
});

describe("the self-service home view", () => {
  beforeEach(() => {
    build();
    vi.clearAllMocks();
    vi.mocked(boundDevice).mockReturnValue(device);
    scopes([sum, env], [own]);
    feed([done]);
    worker.fn = null;
    Object.defineProperty(navigator, "serviceWorker", {
      configurable: true,
      value: {
        addEventListener: (_type: string, fn: (ev: MessageEvent) => void) => { worker.fn = fn; },
        removeEventListener: () => { worker.fn = null; },
      },
    });
    window.history.replaceState(null, "", "/self-service/");
  });
  afterEach(() => window.history.replaceState(null, "", "/"));

  const decidedRow = (id: string, ago: number): ApproverRow => ({ ...done, id, created_at: at(-ago - 120), expires_at: at(-ago), decided_at: at(-ago + 10), summary: { tool: "mcp.call", app: "demo-tools", tool_name: "tool-" + id } });

  it("draws what waits under its heading in place of the filter, the search and the count", async () => {
    mount({}, true);
    const waiting = await part("[data-home-waiting]");
    await within(waiting).findByRole("button", { name: "Open " + SUM_ROW });
    expect(within(waiting).getByRole("heading", { level: 2 }).textContent).toBe(W.WAITING_HEAD);
    expect(within(waiting).getByRole("heading", { level: 2 }).className).toContain("text-section");
    expect(within(waiting).getAllByRole("button", { name: /^Open / }).length).toBe(3);
    expect(within(waiting).getByRole("button", { name: decideAction("approve", { summary: "mcp.call demo-tools:get-sum", username: "joe-java-developer-agent" } as ApprovalRow) })).toBeTruthy();
    expect([...waiting.querySelectorAll("thead th")].map((th) => th.textContent)).toEqual([REQUEST_COLUMN.asked, REQUEST_COLUMN.who, REQUEST_COLUMN.call, REQUEST_COLUMN.left, ""]);
    expect(screen.queryByRole("button", { name: "Decided" })).toBe(null);
    expect(screen.queryByLabelText("Search requests")).toBe(null);
    expect(document.querySelector("[data-row-count]")).toBe(null);
    expect(document.querySelector("[data-queue-legend]")).toBe(null);
  });

  it("lists the three newest decided requests as muted rows with no buttons, and links to every request", async () => {
    feed([decidedRow("d1", 60), decidedRow("d2", 120), decidedRow("d3", 180), decidedRow("d4", 240)]);
    mount({}, true);
    const lately = await part("[data-home-decided]");
    expect(within(lately).getByRole("heading", { level: 2 }).textContent).toBe(W.DECIDED_HEAD);
    const rows = [...lately.querySelectorAll("[data-request]")];
    expect(rows.map((r) => r.getAttribute("data-request"))).toEqual(["tool-d1", "tool-d2", "tool-d3"].map((t) => t + " for joe-java-developer-agent"));
    for (const r of rows) {
      expect(r.className).toContain("text-muted-foreground");
      expect(within(r as HTMLElement).queryByRole("button")).toBe(null);
    }
    expect([...lately.querySelectorAll("thead th")].map((th) => th.textContent)).toEqual([REQUEST_COLUMN.asked, REQUEST_COLUMN.who, REQUEST_COLUMN.call, REQUEST_COLUMN.outcome]);
    const link = within(lately).getByRole("link", { name: W.ALL_REQUESTS });
    expect(link.getAttribute("href")).toBe("/self-service/requests");
    await userEvent.click(link);
    expect(window.location.pathname).toBe("/self-service/requests");
  });

  it("draws no waiting part when nothing waits, and no decided part or link before a decided request is loaded", async () => {
    scopes([], []);
    feed([]);
    mount({}, true);
    await waitFor(() => expect(vi.mocked(history)).toHaveBeenCalled());
    expect(document.querySelector("[data-home-waiting]")).toBe(null);
    expect(screen.queryByText(W.NOTHING_WAITS)).toBe(null);
    expect(document.querySelector("[data-home-decided]")).toBe(null);
    expect(screen.queryByRole("link", { name: W.ALL_REQUESTS })).toBe(null);
  });

  it("shows the decided part when nothing waits", async () => {
    scopes([], []);
    mount({}, true);
    const lately = await part("[data-home-decided]");
    expect(within(lately).getByRole("button", { name: "Open " + SUM_ROW })).toBeTruthy();
    expect(document.querySelector("[data-home-waiting]")).toBe(null);
  });

  it("opens the same dialog and shows the same failed-read banner as the tab", async () => {
    mount({}, true);
    await userEvent.click(await screen.findByRole("button", { name: decideAction("deny", { summary: "mcp.call demo-tools:get-sum", username: "joe-java-developer-agent" } as ApprovalRow) }));
    expect((await screen.findByRole("dialog")).getAttribute("data-request-dialog")).toBe("apr_sum");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    vi.mocked(pending).mockRejectedValue(new ApproverError("HTTP 503", { status: 503 }));
    relay();
    const block = await screen.findByRole("alert");
    expect(block.textContent!.startsWith(W.NO_ANSWER + " Last read at ")).toBe(true);
    expect(document.querySelector("[data-fetch-error]")).toBe(null);
    // The rows that were read stay under the banner.
    expect(within(document.querySelector("[data-home-waiting]") as HTMLElement).getByRole("button", { name: "Open " + SUM_ROW })).toBeTruthy();
  });
});

describe("the requests before this browser is set up", () => {
  // The sign-in lane answers the device lane's rows without a challenge.
  const bare = (r: ApproverRow): ApproverRow => ({ ...r, challenge: undefined });
  const signIn = (decidable: ApproverRow[], mine: ApproverRow[]) =>
    vi.mocked(selfRequests).mockImplementation(((scope: string) => Promise.resolve({ requests: scope === "mine" ? mine : decidable })) as typeof selfRequests);
  const signInCalls = () => vi.mocked(selfRequests).mock.calls.map((c) => c[0]);

  beforeEach(() => {
    build();
    vi.clearAllMocks();
    signIn([bare(sum), bare(env)], [own]);
    feed([done]);
  });

  it("reads the two scopes with the sign-in session, never the device lane, and reads no decided request", async () => {
    mount({ enrollment: null });
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(signInCalls().slice(0, 2)).toEqual(["decidable", "mine"]);
    expect(vi.mocked(pending)).not.toHaveBeenCalled();
    expect(vi.mocked(history)).not.toHaveBeenCalled();
  });

  it("hands the shell the rows the decidable scope named, for the tab count and Home's first card", async () => {
    mount({ enrollment: null });
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await waitFor(() => expect(setWaiting).toHaveBeenCalled());
    expect(setWaiting.mock.lastCall![0].map((r: ApprovalRow) => r.id)).toEqual(["apr_sum", "apr_env"]);
  });

  it("offers no Deny or Approve on a row, and the request opens with no reason field", async () => {
    mount({ enrollment: null });
    const row = await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(within(row).queryByRole("button")).toBe(null);
    await userEvent.click(row);
    const dialog = await screen.findByRole("dialog");
    const foot = dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement;
    expect(dialog.querySelector("[data-reason-field]")).toBe(null);
    expect(within(foot).queryByRole("button", { name: APPROVE })).toBe(null);
    expect(within(foot).queryByRole("button", { name: DENY })).toBe(null);
  });

  it("draws no filter control and lists the waiting requests only, since no decided request is read", async () => {
    mount({ enrollment: null });
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(screen.queryByRole("group", { name: FILTER_LABEL })).toBe(null);
    expect(screen.queryByRole("button", { name: "Decided" })).toBe(null);
    expect(screen.queryByRole("button", { name: "All" })).toBe(null);
    expect(heads()[3]).toBe(REQUEST_COLUMN.left);
    expect([...document.querySelectorAll("[data-request]")].map((el) => el.getAttribute("data-request"))).toEqual([OWN_ROW, SUM_ROW, "get-env for sam-sre-agent"]);
    expect(vi.mocked(history)).not.toHaveBeenCalled();
  });

  it("ends a row the reader may decide in the words to set up a device, keeps who decides on the others, and offers the setup in its dialog", async () => {
    const setSheet = vi.fn();
    mount({ enrollment: null, setSheet });
    const row = await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect((row.querySelector("[data-setup-to-answer]") as HTMLElement).textContent).toBe(W.SETUP_TO_ANSWER);
    expect(within(row).queryByRole("button")).toBe(null);
    expect(within(openRow(OWN_ROW)).getByText("sec-approvers decides")).toBeTruthy();
    expect(openRow(OWN_ROW).querySelector("[data-setup-to-answer]")).toBe(null);
    expect(counted()).toBe(waitingCount(3, 2));

    await userEvent.click(row);
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-who]") as HTMLElement).textContent).toBe("You, as joe-java-developer-agent's sponsor. Nobody else.");
    expect((dialog.querySelector("[data-needs-device]") as HTMLElement).textContent).toBe(W.ANSWER_NEEDS_BROWSER);
    const foot = dialog.querySelector("[data-slot=dialog-footer]") as HTMLElement;
    expect(within(foot).getAllByRole("button").map((b) => b.textContent)).toEqual([CLOSE, W.SETUP_BROWSER]);
    await userEvent.click(within(foot).getByRole("button", { name: W.SETUP_BROWSER }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    expect(setSheet).toHaveBeenCalledWith("enable", true);
  });

  it("passes the setup to the queue only before setup", async () => {
    scopes([sum], []);
    const { unmount } = mount();
    const row = await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(row.querySelector("[data-setup-to-answer]")).toBe(null);
    expect(within(row).getByRole("button", { name: decideAction("approve", { summary: "mcp.call demo-tools:get-sum", username: "joe-java-developer-agent" } as ApprovalRow) })).toBeTruthy();
    expect(screen.getByRole("group", { name: FILTER_LABEL })).toBeTruthy();
    unmount();

    mount({ enrollment: null });
    const before = await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(before.querySelector("[data-setup-to-answer]")).not.toBe(null);
  });

  it("says what a browser that is not set up says when nothing waits, and keeps reading", async () => {
    signIn([], []);
    mount({ enrollment: null });
    expect(await screen.findByText(W.NOTHING_WAITS_CARD)).toBeTruthy();
    expect(screen.getByText(W.WAITS_SHOW_HERE_BROWSER)).toBeTruthy();
    expect(setWaiting).toHaveBeenLastCalledWith([]);
  });

  it("reads an older server's 404 as an empty list, with no banner", async () => {
    vi.mocked(selfRequests).mockRejectedValue(new ApiError("HTTP 404", 404));
    mount({ enrollment: null });
    expect(await screen.findByText(W.NOTHING_WAITS_CARD)).toBeTruthy();
    expect(screen.queryByRole("alert")).toBe(null);
  });

  it("says Straza did not answer when the read fails", async () => {
    vi.mocked(selfRequests).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount({ enrollment: null });
    expect((await screen.findByRole("alert")).textContent).toBe(W.NO_ANSWER);
    expect(setWaiting).toHaveBeenLastCalledWith("error");
  });

  it("draws the waiting rows in the home view, and nothing under the cards when none waits", async () => {
    const { unmount } = mount({ enrollment: null }, true);
    const waiting = await part("[data-home-waiting]");
    await within(waiting).findByRole("button", { name: "Open " + SUM_ROW });
    expect(document.querySelector("[data-home-decided]")).toBe(null);
    unmount();

    signIn([], []);
    mount({ enrollment: null }, true);
    await waitFor(() => expect(setWaiting).toHaveBeenLastCalledWith([]));
    expect(screen.queryByText(W.NOTHING_WAITS_CARD)).toBe(null);
    expect(document.querySelector("[data-home-waiting]")).toBe(null);
  });
});

describe("a request named in the address", () => {
  beforeEach(() => {
    build();
    vi.clearAllMocks();
    vi.mocked(boundDevice).mockReturnValue(device);
    scopes([sum, env], [own]);
    feed([done]);
  });
  afterEach(() => window.history.replaceState(null, "", "/"));

  const dialogID = () => screen.findByRole("dialog").then((d) => d.getAttribute("data-request-dialog"));
  const close = async () => {
    await userEvent.click(within(screen.getByRole("dialog").querySelector("[data-slot=dialog-footer]") as HTMLElement).getByRole("button", { name: CLOSE }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
  };

  it("opens on the Requests tab, again after a reload, and leaves the address when the dialog closes", async () => {
    window.history.replaceState(null, "", "/self-service/requests/apr_env");
    const first = mount();
    expect(await dialogID()).toBe("apr_env");
    first.unmount();

    mount();
    expect(await dialogID()).toBe("apr_env");
    await close();
    expect(window.location.pathname).toBe("/self-service/requests");
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("dialog")).toBe(null);
  });

  it("opens a request the address names once the tab is open, once", async () => {
    window.history.replaceState(null, "", "/self-service/requests");
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(screen.queryByRole("dialog")).toBe(null);
    act(() => navigate("requests", true, "apr_sum"));
    expect(await dialogID()).toBe("apr_sum");
    await close();
    expect(window.location.pathname).toBe("/self-service/requests");
    act(() => navigate("requests", true, "apr_env"));
    expect(await dialogID()).toBe("apr_env");
  });

  it("opens before setup too, with the setup in place of the answer", async () => {
    vi.mocked(selfRequests).mockImplementation(((scope: string) => Promise.resolve({ requests: scope === "mine" ? [own] : [{ ...sum, challenge: undefined }] })) as typeof selfRequests);
    window.history.replaceState(null, "", "/self-service/requests/apr_sum");
    mount({ enrollment: null });
    expect(await dialogID()).toBe("apr_sum");
    expect(screen.getByRole("dialog").querySelector("[data-needs-device]")).not.toBe(null);
  });

  it("opens nothing and says nothing for a request that is not among the rows", async () => {
    window.history.replaceState(null, "", "/self-service/requests/apr_gone");
    mount();
    // A named request opens the All filter, where the decided row of the same call shows too.
    await screen.findAllByRole("button", { name: "Open " + SUM_ROW });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("dialog")).toBe(null);
    expect(screen.queryByRole("alert")).toBe(null);
    expect(window.location.pathname).toBe("/self-service/requests/apr_gone");
  });

  it("does not open the named request in the home view", async () => {
    window.history.replaceState(null, "", "/self-service/requests/apr_sum");
    mount({}, true);
    await part("[data-home-decided]");
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("dialog")).toBe(null);
  });
});

describe("the self-service queue below the sm breakpoint", () => {
  beforeEach(() => {
    build();
    vi.clearAllMocks();
    vi.mocked(boundDevice).mockReturnValue(device);
    scopes([sum, env], [own]);
    feed([done]);
    vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
      matches: query === "(max-width: 39.98rem)", media: query, onchange: null,
      addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
    }) as MediaQueryList);
  });
  afterEach(() => { vi.mocked(window.matchMedia).mockRestore(); });

  it("draws an agent's waiting request as a card with its teal tile, the sponsor and the two large buttons", async () => {
    mount();
    const card = await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(card.getAttribute("data-card")).not.toBe(null);
    expect(card.querySelector("[data-kind-icon=agent]")!.parentElement!.className).toContain("bg-teal-bg");
    expect((card.querySelector("[data-card-meta]") as HTMLElement).textContent).toMatch(new RegExp("^" + YOU_SPONSOR + " · 3\\d s ago$"));
    for (const b of within(card).getAllByRole("button")) expect(b.className).toContain("h-11");
    expect(within(openRow(OWN_ROW)).getByText("sec-approvers decides")).toBeTruthy();
  });

  it("draws a decided card with the outcome chip and who answered from where", async () => {
    mount({}, true);
    const lately = await part("[data-home-decided]");
    const card = within(lately).getByRole("button", { name: "Open " + SUM_ROW });
    expect(card.className).toContain("text-muted-foreground");
    expect((card.querySelector("[data-outcome]") as HTMLElement).textContent).toBe("denied");
    expect((card.querySelector("[data-outcome-line]") as HTMLElement).textContent).toBe("you denied from this browser");
    expect(document.querySelector("[data-slot=table]")).toBe(null);
  });
});

describe("the outcome words of a decided request", () => {
  const base: ApprovalRow = {
    id: "apr_x", state: "approved", createdAt: "2026-11-14T14:00:00Z", expiresAt: "2026-11-14T14:05:00Z", decidedAt: "2026-11-14T14:01:00Z",
    user: "u-joe", username: "joe-java-developer-agent", session: "s", rule: "r", set: "p", lane: "gateway",
    summary: "mcp.call demo-tools:get-sum", justification: "", approverRoles: [], approverUsers: ["alice"], selfApproval: false, mode: "approve",
    decidedBy: "alice", decidedByName: "alice", channel: "browser", decidedDeviceId: "apd_here",
  };
  const row = (patch: Partial<ApprovalRow>): ApprovalRow => ({ ...base, ...patch });

  it.each([
    ["approved", "allowed"],
    ["granted", "allowed"],
    ["used", "allowed"],
    ["lapsed", "allowed"],
    ["denied", "denied"],
    ["expired", "denied"],
    ["waiting", "allowed"],
  ] as [Phase, string][])("reads the phase %s as %s", (phase, outcome) => {
    expect(outcomeOf(phase)).toBe(outcome);
  });

  it.each([
    ["the reader approved a hold from this browser", row({}), "approved", "you approved from this browser"],
    ["the reader denied from this browser", row({ state: "denied" }), "denied", "you denied from this browser"],
    ["the reader approved from a phone", row({ channel: "phone", decidedDeviceId: "apd_phone" }), "approved", "you approved from your phone"],
    ["the reader approved from another browser", row({ decidedDeviceId: "apd_other" }), "approved", "you approved from a browser"],
    ["the reader approved from the console", row({ channel: "console", decidedDeviceId: undefined }), "approved", "you approved from the console"],
    ["the reader's ticket is still open", row({ class: "ticket" }), "granted", "you approved from this browser, not used yet"],
    ["the reader's ticket was used", row({ class: "ticket" }), "used", "you approved from this browser"],
    ["the reader's ticket closed unused", row({ class: "ticket" }), "lapsed", "you approved from this browser, never used"],
    ["somebody else approved from the console", row({ decidedBy: "bob", decidedByName: "bob", channel: "console" }), "approved", "bob approved from the console"],
    ["somebody else approved from Slack", row({ decidedBy: "bob", decidedByName: "bob", channel: "slack" }), "approved", "bob approved from Slack"],
    ["somebody else denied from a phone", row({ decidedBy: "bob", decidedByName: "bob", channel: "phone" }), "denied", "bob denied from a phone"],
    ["somebody else approved from a browser", row({ decidedBy: "bob", decidedByName: "bob", channel: "browser" }), "approved", "bob approved from a browser"],
    ["somebody else approved through the API", row({ decidedBy: "bob", decidedByName: "bob", channel: "api" }), "approved", "bob approved from the API"],
    ["a channel the page does not know reads as the console", row({ decidedBy: "bob", decidedByName: "bob", channel: "carrier-pigeon" }), "approved", "bob approved from the console"],
    ["a record that names no decider", row({ decidedBy: "", decidedByName: "", channel: "console" }), "approved", "somebody approved from the console"],
    ["nobody decided within the window", row({ state: "expired" }), "expired", "nobody decided within 5 minutes"],
  ] as [string, ApprovalRow, Phase, string][])("words %s", (_what, r, phase, line) => {
    expect(decidedLine(r, phase, "alice", "apd_here")).toBe(line);
  });

  it("agrees with the phase the record reads as", () => {
    expect(phaseOf(row({ state: "denied" }))).toBe("denied");
    expect(outcomeOf(phaseOf(row({ state: "expired" })))).toBe("denied");
  });
});
