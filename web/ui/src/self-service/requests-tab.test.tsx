import { beforeEach, describe, expect, it, vi } from "vitest";
import { webcrypto } from "node:crypto";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { CLOSE, OPEN_SESSION, waitingCount } from "@/lib/approval-words";
import { ApproverError, boundDevice, decide, history, pending } from "./approver-api";
import { SelfContext, type SelfState } from "./context";
import { RequestsTab } from "./requests-tab";
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
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><RequestsTab /></SelfContext.Provider>
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

// worker stands in for the service worker the page listens to; jsdom has
// none, and the relayed push is the lane that makes a notification a row.
const worker = { fn: null as ((ev: MessageEvent) => void) | null };

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

  it("says what would be here while this browser cannot decide", () => {
    mount({ enrollment: null });
    expect(screen.getByText(W.NOT_ENROLLED_TITLE)).toBeTruthy();
    expect(screen.getByText(W.NOT_ENROLLED_ENABLE)).toBeTruthy();
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

  it("counts the rows that wait for the count line and the tab strip", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    expect(counted()).toBe(waitingCount(3, 2));
    await waitFor(() => expect(setCount).toHaveBeenCalledWith("requests", 3));
    expect(scopeCalls().slice(0, 2)).toEqual(["decidable", "mine"]);
  });

  it("tells the person the decision is theirs and leaves out every console door", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open " + SUM_ROW }));
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-who]") as HTMLElement).textContent)
      .toBe("You (alice), as joe-java-developer-agent's sponsor. Nobody else.");
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

  it("posts exactly one decision, signed over the row's id, its challenge and the reason", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(screen.getByRole("button", { name: "Approve get-sum for joe-java-developer-agent" }));
    const ask = await screen.findByRole("alertdialog");
    await userEvent.type(within(ask).getByLabelText("Reason, optional"), "the freeze is over");
    expect(vi.mocked(decide)).not.toHaveBeenCalled();

    await userEvent.click(within(ask).getByRole("button", { name: "Approve request" }));
    await waitFor(() => expect(vi.mocked(decide)).toHaveBeenCalledTimes(1));
    const body = vi.mocked(decide).mock.calls[0][0];
    expect(body.request_id).toBe("apr_sum");
    expect(body.verdict).toBe("approve");
    expect(body.challenge).toBe("chal-sum");
    expect(body.reason).toBe("the freeze is over");
    expect(Math.abs(body.ts - Math.floor(Date.now() / 1000))).toBeLessThan(60);
    expect(body.signature).toBe("sig:" + await decideMessageWithReason("apr_sum", "approve", "chal-sum", body.ts, "the freeze is over"));
    // The decision reloads the queue it came from.
    await waitFor(() => expect(scopeCalls().filter((s) => s === "decidable").length).toBeGreaterThan(1));
  });

  it("refuses a reason the server would refuse for its length, before anything is signed", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(screen.getByRole("button", { name: "Deny get-sum for joe-java-developer-agent" }));
    const ask = await screen.findByRole("alertdialog");
    // 260 diacritics are 260 characters, which the box allows, and 520
    // bytes, which the server does not.
    await userEvent.click(within(ask).getByLabelText("Reason, optional"));
    await userEvent.paste("č".repeat(260));
    await userEvent.click(within(ask).getByRole("button", { name: "Deny request" }));
    expect(await within(ask).findByText(W.REASON_TOO_LONG)).toBeTruthy();
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

    await userEvent.click(screen.getByRole("button", { name: "Deny get-sum for joe-java-developer-agent" }));
    const ask = await screen.findByRole("alertdialog");
    await userEvent.click(within(ask).getByRole("button", { name: "Deny request" }));

    await waitFor(() => expect(vi.mocked(decide)).toHaveBeenCalledTimes(2));
    expect(vi.mocked(decide).mock.calls[0][0].challenge).toBe("chal-sum");
    expect(vi.mocked(decide).mock.calls[1][0].challenge).toBe("chal-sum-2");
    expect(device.onRevoked).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBe(null));
  });

  it("ends the question with Close when the request was settled first", async () => {
    vi.mocked(decide).mockRejectedValue(new ApproverError("HTTP 409", { status: 409, data: { state: "approved" } }));
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(screen.getByRole("button", { name: "Approve get-sum for joe-java-developer-agent" }));
    const ask = await screen.findByRole("alertdialog");
    await userEvent.click(within(ask).getByRole("button", { name: "Approve request" }));

    expect(await within(ask).findByText(W.alreadySettled("approved"))).toBeTruthy();
    expect(within(ask).getByRole("button", { name: CLOSE })).toBeTruthy();
    expect(within(ask).queryByRole("button", { name: "Approve request" })).toBe(null);
  });

  it("names the remedy when the server no longer knows this browser's credential", async () => {
    vi.mocked(pending).mockRejectedValue(new ApproverError("the device token is not known here", { status: 401, code: "token_invalid" }));
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("the server rejected this browser's credential (the device token is not known here)");
    expect(block.textContent).toContain("Revoke this browser under This browser, then enable it again");
    await waitFor(() => expect(setCount).toHaveBeenCalledWith("requests", null));
    // A rejected credential is not a revoked one: nothing local is dropped.
    expect(setEnrollment).not.toHaveBeenCalled();
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

    worker.fn!({ data: { type: "straza-approvals", kind: "decide" } } as MessageEvent);
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

    await userEvent.click(screen.getByRole("button", { name: "Decided" }));
    const row = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect((row.querySelector("[data-state-line]") as HTMLElement).textContent).toContain("by alice from the browser");
    expect(screen.queryByRole("button", { name: "Open " + OWN_ROW })).toBe(null);
  });

  it("asks for the older page with the server's own cursor and appends it", async () => {
    const older = { ...done, id: "apr_old", summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get-time" } };
    vi.mocked(history).mockImplementation(((cursor: string) =>
      Promise.resolve(cursor ? { items: [older], next_cursor: "" } : { items: [done], next_cursor: CURSOR })) as typeof history);
    mount();
    await screen.findByRole("button", { name: "Open " + SUM_ROW });
    await userEvent.click(screen.getByRole("button", { name: "Decided" }));
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));

    await waitFor(() => expect(screen.getByRole("button", { name: "Open get-time for joe-java-developer-agent" })).toBeTruthy());
    expect(vi.mocked(history).mock.calls.map((c) => c[0])).toEqual(["", CURSOR]);
    // The first page is still there: a page appends, it never replaces.
    expect(screen.getByRole("button", { name: "Open get-sum for joe-java-developer-agent" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load more" })).toBe(null);
  });
});
