import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { BY_PUSH, MUST_CHECK_LINE, REVOKE_DEVICE, SOFTWARE_BROWSER } from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { ApproverError, pending, selfUnenroll } from "./approver-api";
import { BrowserTab } from "./browser-tab";
import { SelfContext, type SelfState } from "./context";
import { disable, enable, retireOldWorker, support, sync, teardown } from "./push";
import type { Enrollment } from "./store";
import { wipeEnrollment } from "./store";
import * as W from "./words";

// The tab's servers are the approver lane and the push lane, so both are the
// seam. The enrol sheet is its own screen with its own suite, and here it is
// only asked whether the page head's request opened it.
vi.mock("./approver-api", async (orig) => ({
  ...(await orig<typeof import("./approver-api")>()),
  pending: vi.fn(),
  selfUnenroll: vi.fn(),
}));
vi.mock("./push", () => ({
  support: vi.fn(),
  enable: vi.fn(),
  disable: vi.fn(),
  sync: vi.fn(),
  teardown: vi.fn(async () => undefined),
  retireOldWorker: vi.fn(async () => undefined),
}));
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  wipeEnrollment: vi.fn(async () => undefined),
}));
vi.mock("./enable-sheet", () => ({
  EnableSheet: ({ open }: { open: boolean }) => (open ? <div data-enable-sheet-open /> : null),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const enrollment = {
  deviceId: "apd_0199c1a27e3f", deviceToken: "dt-live", tokenExpiresAt: Date.now() + 86400000,
  project: { id: "prj_01", name: "acme-prod" }, user: { id: "u-alice", username: "alice" },
  webpush: { vapid_public_key: "BFxc" }, keys: {} as CryptoKeyPair,
  deviceName: "Firefox on Windows", enrolledAt: new Date(Date.now() - 4 * 86400000).toISOString(),
} as Enrollment;

const setEnrollment = vi.fn();
const refreshSelf = vi.fn(async () => undefined);

function shared(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] },
    refreshSelf,
    enrollment,
    setEnrollment,
    storage: { usable: true, outlook: "durable" },
    openSignIn: () => undefined,
    signedIn: vi.fn(),
    notice: null,
    setNotice: () => undefined,
    sheets: { enable: false, phone: false },
    setSheet: vi.fn(),
    setCount: () => undefined,
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><BrowserTab /></SelfContext.Provider>
  </TooltipProvider>,
);

const cell = (attr: string) => document.querySelector("[" + attr + "]") as HTMLElement | null;
const row = () => screen.findByRole("cell", { name: /Firefox on Windows/ });

describe("the This browser tab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(pending).mockResolvedValue([]);
    vi.mocked(selfUnenroll).mockResolvedValue(true);
    vi.mocked(sync).mockResolvedValue({ on: true, why: "" });
    vi.mocked(support).mockReturnValue({ available: true, why: "" });
    vi.mocked(disable).mockResolvedValue("");
  });

  it("says what enrolling means and the one next step, by account", async () => {
    const { unmount } = mount({ enrollment: null, session: { kind: "signed-out" }, self: null });
    expect(screen.getByText(W.NOT_ENROLLED_TITLE)).toBeTruthy();
    expect(screen.getByText(new RegExp(W.DECIDING_MEANS.slice(0, 40)))).toBeTruthy();
    expect(screen.getByText(new RegExp(W.ENABLE_SIGN_IN_FIRST))).toBeTruthy();
    expect(vi.mocked(pending)).not.toHaveBeenCalled();
    unmount();

    mount({ enrollment: null });
    expect(screen.getByText(new RegExp(W.ENABLE_HERE))).toBeTruthy();
  });

  it("tells a phone-only account and a role-less account what is true for them", () => {
    const { unmount } = mount({ enrollment: null, self: { username: "judy", user_kind: "human", enroll_channels: ["mobile"], sponsored: [] } });
    expect(screen.getByText(new RegExp(W.ENABLE_PHONE_ONLY))).toBeTruthy();
    unmount();

    mount({ enrollment: null, self: { username: "nhi", user_kind: "nhi", enroll_channels: [], sponsored: [] } });
    expect(screen.getByText(new RegExp(W.NOT_ENROLLED_NO_ROLE))).toBeTruthy();
  });

  it("offers Try again when the server could not say what the account may enrol", async () => {
    mount({ enrollment: null, self: "error" });
    expect(screen.getByText(new RegExp(W.ELIGIBILITY_UNREAD))).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: W.TRY_AGAIN }));
    expect(refreshSelf).toHaveBeenCalledTimes(1);
  });

  it("offers nothing when this browser will not keep a key", () => {
    mount({ enrollment: null, storage: { usable: false, outlook: "unknown" } });
    expect(screen.getByText(W.NO_STORAGE)).toBeTruthy();
    expect(screen.queryByText(W.NOT_ENROLLED_TITLE)).toBe(null);
  });

  it("reads this browser as the console's device row", async () => {
    mount();
    await row();
    expect(screen.getByText("Firefox on Windows")).toBeTruthy();
    expect(cell("data-this-browser")?.textContent).toBe(W.THIS_BROWSER_BADGE);
    expect(cell("data-device-kind")?.textContent).toBe("browser");
    expect(screen.getByText(SOFTWARE_BROWSER)).toBeTruthy();
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByText(W.NO_EXPIRY_LINE)).toBeTruthy();
    await waitFor(() => expect(cell("data-last-seen")?.textContent).toBe(W.SEEN_NOW));
  });

  it("keeps the row and says so when the credential could not be proved", async () => {
    vi.mocked(pending).mockRejectedValue(new ApproverError("the server is unreachable", { unreachable: true }));
    mount();
    await row();
    await waitFor(() => expect(cell("data-fetch-error")).toBeTruthy());
    expect(cell("data-last-seen")?.textContent).toBe(W.SEEN_UNKNOWN);
    expect(screen.getByText("Firefox on Windows")).toBeTruthy();
  });

  it("retires the worker the replaced approvals page left behind", async () => {
    mount();
    await waitFor(() => expect(vi.mocked(retireOldWorker)).toHaveBeenCalledTimes(1));
  });

  it("opens the enrol sheet when the page head asks for it", async () => {
    const { rerender } = mount({ enrollment: null });
    expect(cell("data-enable-sheet-open")).toBe(null);
    rerender(
      <TooltipProvider>
        <SelfContext.Provider value={shared({ enrollment: null, sheets: { enable: true, phone: false } })}><BrowserTab /></SelfContext.Provider>
      </TooltipProvider>,
    );
    await waitFor(() => expect(cell("data-enable-sheet-open")).toBeTruthy());
  });

  it("says why notifications cannot be offered, and offers nothing", async () => {
    vi.mocked(support).mockReturnValue({ available: false, why: W.PUSH_OFF_DEPLOYMENT });
    mount();
    await row();
    expect(screen.getByText(W.PUSH_OFF_DEPLOYMENT)).toBeTruthy();
    expect(screen.queryByRole("button", { name: W.TURN_ON })).toBe(null);
  });

  it("reads a stored subscription the sync proved as on", async () => {
    mount({ enrollment: { ...enrollment, push: { kind: "webpush", token_or_endpoint: "https://push.example/wp/s", p256dh: "p", auth: "a" } } });
    await row();
    await waitFor(() => expect(cell("data-notified")?.textContent).toBe(BY_PUSH));
    expect(screen.getByRole("button", { name: W.TURN_OFF })).toBeTruthy();
  });

  it("asks in the page before the browser is ever prompted", async () => {
    mount();
    await row();
    expect(screen.getByText(MUST_CHECK_LINE)).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: W.TURN_ON }));
    expect(screen.getByText(W.CONSENT_TITLE)).toBeTruthy();
    expect(vi.mocked(enable)).not.toHaveBeenCalled();

    vi.mocked(enable).mockResolvedValue({ kind: "webpush", token_or_endpoint: "https://push.example/wp/s", p256dh: "p", auth: "a" });
    await userEvent.click(screen.getByRole("button", { name: W.CONTINUE }));
    await waitFor(() => expect(vi.mocked(enable)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.NOTIFY_ON_TOAST);
  });

  it("reads a declined browser prompt as an outcome, not a failure", async () => {
    vi.mocked(enable).mockResolvedValue(null);
    mount();
    await row();
    await userEvent.click(screen.getByRole("button", { name: W.TURN_ON }));
    await userEvent.click(screen.getByRole("button", { name: W.CONTINUE }));

    await waitFor(() => expect(vi.mocked(notify.warn)).toHaveBeenCalledWith(W.NOTIFY_DECLINED));
    expect(vi.mocked(notify.failed)).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: W.TURN_ON })).toBeTruthy();
  });

  it("turns notifications off and says what changed", async () => {
    mount({ enrollment: { ...enrollment, push: { kind: "webpush", token_or_endpoint: "https://push.example/wp/s", p256dh: "p", auth: "a" } } });
    await row();
    await waitFor(() => expect(screen.getByRole("button", { name: W.TURN_OFF })).toBeTruthy());

    await userEvent.click(screen.getByRole("button", { name: W.TURN_OFF }));
    await waitFor(() => expect(vi.mocked(disable)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.NOTIFY_OFF_TOAST);
  });

  it("asks before revoking, then retires the row and destroys the key", async () => {
    mount();
    await row();

    await userEvent.click(screen.getByRole("button", { name: "Revoke Firefox on Windows" }));
    expect(screen.getByText(W.REVOKE_BODY)).toBeTruthy();
    expect(vi.mocked(selfUnenroll)).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: REVOKE_DEVICE }));
    await waitFor(() => expect(vi.mocked(selfUnenroll)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(teardown)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(wipeEnrollment)).toHaveBeenCalledTimes(1);
    expect(setEnrollment).toHaveBeenCalledWith(null);
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.REVOKED_TOAST);
  });

  it("destroys the key anyway when the server would not confirm the revoke", async () => {
    vi.mocked(selfUnenroll).mockResolvedValue(false);
    mount();
    await row();
    await userEvent.click(screen.getByRole("button", { name: "Revoke Firefox on Windows" }));
    await userEvent.click(screen.getByRole("button", { name: REVOKE_DEVICE }));

    await waitFor(() => expect(vi.mocked(wipeEnrollment)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(notify.warn)).toHaveBeenCalledWith(W.REVOKE_UNCONFIRMED);
  });
});
