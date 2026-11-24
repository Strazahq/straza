import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { CANCEL } from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { ApiError, readSelf, selfEnrollToken } from "@/lib/api";
import { beginSignIn, takeResume } from "@/lib/session";
import { ApproverError, enrollDevice } from "./approver-api";
import { SelfContext, type SelfState } from "./context";
import { enable, support } from "./push";
import { SetupDialog } from "./setup-dialog";
import { exportPublicKeyB64, generateDeviceKey } from "./sign";
import { requestDurable, saveEnrollment } from "./store";
import * as W from "./words";

// The run's three server calls and its three local ones are the seam, with
// the push lane for the box that turns notifications on. The sign-in leaves
// the tab, so the dialog's half of it is the departure with the name and
// the resume with the name a returning sign-in kept.
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  readSelf: vi.fn(),
  selfEnrollToken: vi.fn(),
}));
vi.mock("./approver-api", async (orig) => ({
  ...(await orig<typeof import("./approver-api")>()),
  enrollDevice: vi.fn(),
}));
vi.mock("./sign", async (orig) => ({
  ...(await orig<typeof import("./sign")>()),
  generateDeviceKey: vi.fn(),
  exportPublicKeyB64: vi.fn(),
}));
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  requestDurable: vi.fn(async () => true),
  saveEnrollment: vi.fn(async () => undefined),
}));
vi.mock("./push", () => ({ enable: vi.fn(), support: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({
  ...(await orig<typeof import("@/lib/session")>()),
  beginSignIn: vi.fn(async () => undefined),
  takeResume: vi.fn(() => null),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const ANSWER = { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] };
const MINT = { enroll_token: "etk-1", expires_in: 120, project: { id: "prj_01", name: "acme-prod" }, user: { id: "u-alice", username: "alice" } };
const ENROLLED = {
  approver_device_id: "apd_01", device_token: "dt-live", expires_in: 2592000,
  project: { id: "prj_01", name: "acme-prod" }, user: { id: "u-alice", username: "alice" },
  webpush: { vapid_public_key: "BFxc" },
};
const KEYS = { privateKey: "private", publicKey: "public" } as unknown as CryptoKeyPair;
const PUSHED = { kind: "webpush" as const, token_or_endpoint: "https://push.example/wp/s", p256dh: "p", auth: "a" };

const setEnrollment = vi.fn();
const refreshSelf = vi.fn(async () => undefined);
const onOpenChange = vi.fn();

function shared(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: ANSWER,
    refreshSelf,
    enrollment: null,
    setEnrollment,
    storage: { usable: true, outlook: "durable" },
    openSignIn: () => undefined,
    signedIn: vi.fn(),
    notice: null,
    setNotice: () => undefined,
    sheets: { enable: false, phone: false },
    setSheet: vi.fn(),
    setCount: () => undefined,
    waiting: null,
    setWaiting: () => undefined,
    servers: null,
    readServers: () => undefined,
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><SetupDialog open onOpenChange={onOpenChange} /></SelfContext.Provider>
  </TooltipProvider>,
);

const nameField = () => screen.getByLabelText(W.DEVICE_NAME_LABEL) as HTMLInputElement;
const primary = (label: string) => screen.getByRole("button", { name: label });
const order = (fn: { mock: { invocationCallOrder: number[] } }) => fn.mock.invocationCallOrder[0];
const refusal = () => (document.querySelector("[data-refused-error]") as HTMLElement | null)?.textContent || "";
const box = () => screen.queryByRole("checkbox", { name: new RegExp(W.NOTIFY_ME) }) as HTMLInputElement | null;

// pushable stands this browser up as one that could receive notifications:
// a service worker, the Push API and a permission not yet asked.
function pushable(permission: NotificationPermission = "default", answer: NotificationPermission = "granted") {
  const requestPermission = vi.fn(async () => answer);
  Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: {} });
  vi.stubGlobal("PushManager", function PushManager() { return undefined; });
  vi.stubGlobal("Notification", { permission, requestPermission });
  return requestPermission;
}

describe("the Set up this browser dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(readSelf).mockResolvedValue(ANSWER);
    vi.mocked(selfEnrollToken).mockResolvedValue(MINT);
    vi.mocked(enrollDevice).mockResolvedValue(ENROLLED);
    vi.mocked(generateDeviceKey).mockResolvedValue(KEYS);
    vi.mocked(exportPublicKeyB64).mockResolvedValue("PUB");
    vi.mocked(saveEnrollment).mockResolvedValue(undefined);
    vi.mocked(support).mockReturnValue({ available: true, why: "" });
    vi.mocked(enable).mockResolvedValue(PUSHED);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    Reflect.deleteProperty(navigator, "serviceWorker");
  });

  it("is a dialog titled Set up this browser that says what the key does", () => {
    mount();
    const dialog = screen.getByRole("dialog", { name: W.SETUP_BROWSER });
    expect(dialog.hasAttribute("data-setup-dialog")).toBe(true);
    expect(screen.getByText(W.SETUP_SUB)).toBeTruthy();
    expect(screen.getByText(W.DEVICE_NAME_HINT)).toBeTruthy();
    expect(nameField().id).toBe("device-name");
    expect(primary(W.SET_UP).hasAttribute("data-setup-run")).toBe(true);
  });

  it("names the browser, then reads, mints, makes the key and sets up, in that order", async () => {
    mount();
    expect(nameField().value.length).toBeGreaterThan(0);
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Dana's work laptop");
    await userEvent.click(primary(W.SET_UP));

    await waitFor(() => expect(vi.mocked(enrollDevice)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(readSelf)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(selfEnrollToken)).toHaveBeenCalledTimes(1);
    expect(order(vi.mocked(readSelf))).toBeLessThan(order(vi.mocked(selfEnrollToken)));
    // The key is made only once the one-time code is in hand, so a refused
    // mint leaves no orphan key behind.
    expect(order(vi.mocked(selfEnrollToken))).toBeLessThan(order(vi.mocked(generateDeviceKey)));
    expect(vi.mocked(requestDurable)).toHaveBeenCalledTimes(1);
  });

  it("sets up with the honest posture of a browser key", async () => {
    mount();
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Dana's work laptop");
    await userEvent.click(primary(W.SET_UP));

    await waitFor(() => expect(vi.mocked(enrollDevice)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(enrollDevice).mock.calls[0][0]).toEqual({
      enroll_token: "etk-1",
      device: {
        name: "Dana's work laptop",
        platform: "browser",
        key_alg: "ES256",
        public_key: "PUB",
        key_security_level: "software",
        attestation: { kind: "none", blob: "" },
      },
    });
  });

  it("keeps the record, closes and says who this browser approves as", async () => {
    mount();
    await userEvent.click(primary(W.SET_UP));

    await waitFor(() => expect(setEnrollment).toHaveBeenCalledTimes(1));
    const rec = setEnrollment.mock.calls[0][0];
    expect(rec.deviceId).toBe("apd_01");
    expect(rec.deviceToken).toBe("dt-live");
    expect(rec.keys).toBe(KEYS);
    expect(rec.webpush).toEqual({ vapid_public_key: "BFxc" });
    expect(vi.mocked(saveEnrollment)).toHaveBeenCalledTimes(1);
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.setUpToast("alice"));
  });

  it("asks for a name at the field without calling anything", async () => {
    mount();
    await userEvent.clear(nameField());
    await userEvent.click(primary(W.SET_UP));

    const said = document.querySelector("[data-name-required]") as HTMLElement;
    expect(said.textContent).toBe(W.DEVICE_NAME_REQUIRED);
    expect(nameField().getAttribute("aria-invalid")).toBe("true");
    expect(nameField().getAttribute("aria-describedby")).toBe(said.id);
    expect(refusal()).toBe("");
    expect(vi.mocked(readSelf)).not.toHaveBeenCalled();
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();
  });

  it("leaves to sign in with the name when the page has no session", async () => {
    mount({ session: { kind: "signed-out" }, self: null });
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Work laptop");
    await userEvent.click(primary(W.SIGN_IN));
    expect(vi.mocked(beginSignIn)).toHaveBeenCalledWith({ back: window.location.pathname, sheet: "Work laptop" });
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();
  });

  it("says Straza did not answer when the sign-in cannot leave for that reason", async () => {
    vi.mocked(beginSignIn).mockRejectedValueOnce(new ApiError("unreachable", 0, true));
    mount({ session: { kind: "signed-out" }, self: null });
    await userEvent.click(primary(W.SIGN_IN));
    await waitFor(() => expect(refusal()).toContain(W.NO_ANSWER));
  });

  it("carries on with the name a returning sign-in kept", async () => {
    vi.mocked(takeResume).mockReturnValueOnce("Work laptop");
    mount();
    await waitFor(() => expect(vi.mocked(enrollDevice)).toHaveBeenCalledTimes(1));
    expect((vi.mocked(enrollDevice).mock.calls[0][0] as { device: { name: string } }).device.name).toBe("Work laptop");
  });

  it("asks to sign in again when the session ends during the run, and leaves with the name", async () => {
    vi.mocked(readSelf).mockRejectedValueOnce(new ApiError("session expired", 401));
    mount();
    await userEvent.click(primary(W.SET_UP));
    expect(await screen.findByText(W.SHEET_SIGN_IN)).toBeTruthy();
    await userEvent.click(primary(W.SIGN_IN));
    expect(vi.mocked(beginSignIn)).toHaveBeenCalledWith({ back: window.location.pathname, sheet: expect.any(String) });
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();
  });

  it("says the setup runs while it works", async () => {
    let release = (v: typeof MINT) => { void v; };
    vi.mocked(selfEnrollToken).mockReturnValue(new Promise((res) => { release = res; }));
    mount();
    await userEvent.click(primary(W.SET_UP));
    expect(await screen.findByText(W.SETTING_UP)).toBeTruthy();
    release(MINT);
    await waitFor(() => expect(setEnrollment).toHaveBeenCalledTimes(1));
  });

  it("never sets up a run the person cancelled", async () => {
    let release = (v: typeof MINT) => { void v; };
    vi.mocked(selfEnrollToken).mockReturnValue(new Promise((res) => { release = res; }));
    mount();
    await userEvent.click(primary(W.SET_UP));
    await waitFor(() => expect(vi.mocked(selfEnrollToken)).toHaveBeenCalledTimes(1));

    await userEvent.click(primary(CANCEL));
    release(MINT);
    await new Promise((r) => setTimeout(r, 10));

    expect(vi.mocked(enrollDevice)).not.toHaveBeenCalled();
    expect(setEnrollment).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("counts down a rate limited mint and offers the way back", async () => {
    vi.mocked(selfEnrollToken).mockRejectedValue(new ApiError("too many enrolment tokens", 429, false, 30));
    mount();
    await userEvent.click(primary(W.SET_UP));

    await waitFor(() => expect(screen.getByText(W.cooldownLine(30))).toBeTruthy());
    expect(screen.getByRole("button", { name: W.TRY_AGAIN })).toBeTruthy();
    expect(vi.mocked(enrollDevice)).not.toHaveBeenCalled();
  });

  it("leaves the dialog for the page's resting state when the account may not set up a browser", async () => {
    vi.mocked(readSelf).mockResolvedValue({ ...ANSWER, enroll_channels: ["mobile"] });
    mount();
    await userEvent.click(primary(W.SET_UP));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();
    expect(refreshSelf).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["Straza could not say what the account may set up", () => vi.mocked(readSelf).mockRejectedValue(new ApiError("database is down", 500)), W.ELIGIBILITY_FAILED],
    ["the read of the account found no server", () => vi.mocked(readSelf).mockRejectedValue(new ApiError("unreachable", 0, true)), W.NO_ANSWER],
    ["the code was refused", () => vi.mocked(selfEnrollToken).mockRejectedValue(new ApiError("browser setup is not allowed for this account", 403)), "Straza refused it: browser setup is not allowed for this account. If this keeps happening, tell your administrator."],
    ["the code found no server", () => vi.mocked(selfEnrollToken).mockRejectedValue(new ApiError("unreachable", 0, true)), W.NO_ANSWER],
    ["the setup was refused", () => vi.mocked(enrollDevice).mockRejectedValue(new ApproverError("the code was already used", { status: 410 })), "Straza refused it: the code was already used. If this keeps happening, tell your administrator."],
    ["the setup answered a bare status", () => vi.mocked(enrollDevice).mockRejectedValue(new ApproverError("HTTP 502", { status: 502 })), "Straza refused it. If this keeps happening, tell your administrator."],
  ])("says why nothing was set up when %s", async (_what, arrange, said) => {
    arrange();
    mount();
    await userEvent.click(primary(W.SET_UP));
    await waitFor(() => expect(refusal()).toBe(W.SETUP_FAILED + " " + said));
    expect(setEnrollment).not.toHaveBeenCalled();
  });

  it("names the device to remove under Devices when the record cannot be kept", async () => {
    vi.mocked(saveEnrollment).mockRejectedValue(new Error("the quota is exhausted"));
    mount();
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Kiosk");
    await userEvent.click(primary(W.SET_UP));

    await waitFor(() => expect(refusal()).toContain(W.storeRefused("the quota is exhausted", "Kiosk")));
    expect(setEnrollment).not.toHaveBeenCalled();
  });

  it("says the key could not be made instead of spinning on", async () => {
    vi.mocked(generateDeviceKey).mockRejectedValue(new Error("NotSupportedError"));
    mount();
    await userEvent.click(primary(W.SET_UP));
    await waitFor(() => expect(refusal()).toContain(W.KEY_FAILED));
    expect(document.querySelector("[data-setup-step]")).toBeNull();
    expect(vi.mocked(enrollDevice)).not.toHaveBeenCalled();
  });

  it.each([
    ["a private window", "ephemeral", W.PRIVATE_WINDOW],
    ["a browser that may clear the key", "unknown", W.OUTLOOK_UNKNOWN],
  ] as const)("warns before the key is made in %s", (_what, outlook, said) => {
    mount({ storage: { usable: true, outlook } });
    expect(screen.getByText(said)).toBeTruthy();
  });

  it("draws no notify box where this browser could not receive notifications", () => {
    const { unmount } = mount();
    expect(box()).toBeNull();
    unmount();

    pushable("denied");
    const second = mount();
    expect(box()).toBeNull();
    second.unmount();

    pushable();
    mount({ storage: { usable: true, outlook: "ephemeral" } });
    expect(box()).toBeNull();
  });

  it("draws the notify box ticked, asks the browser inside the press and turns notifications on once set up", async () => {
    const ask = pushable();
    mount();
    expect(box()!.checked).toBe(true);
    expect(screen.getByText(W.NOTIFY_HINT)).toBeTruthy();
    await userEvent.click(primary(W.SET_UP));
    expect(ask).toHaveBeenCalledTimes(1);
    expect(order(ask)).toBeLessThan(order(vi.mocked(readSelf)));

    await waitFor(() => expect(vi.mocked(enable)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.setUpToast("alice"));
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.NOTIFY_ON_TOAST));
    // The shell gets a fresh copy, so a tab that shows this browser reads
    // the subscription.
    await waitFor(() => expect(setEnrollment).toHaveBeenCalledTimes(2));
  });

  it("leaves notifications off when the box is unticked", async () => {
    const ask = pushable();
    mount();
    await userEvent.click(box()!);
    expect(box()!.checked).toBe(false);
    await userEvent.click(primary(W.SET_UP));
    await waitFor(() => expect(setEnrollment).toHaveBeenCalledTimes(1));
    expect(ask).not.toHaveBeenCalled();
    expect(vi.mocked(enable)).not.toHaveBeenCalled();
  });

  it("keeps the setup when the person says no at the browser's prompt", async () => {
    pushable("default", "denied");
    mount();
    await userEvent.click(primary(W.SET_UP));
    await waitFor(() => expect(vi.mocked(notify.warn)).toHaveBeenCalledWith(W.NOTIFY_DECLINED));
    expect(setEnrollment).toHaveBeenCalledTimes(1);
    expect(vi.mocked(enable)).not.toHaveBeenCalled();
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.setUpToast("alice"));
  });

  it("keeps the setup when notifications could not be turned on", async () => {
    pushable();
    vi.mocked(enable).mockRejectedValue(new Error(W.WORKER_SLOW));
    mount();
    await userEvent.click(primary(W.SET_UP));
    await waitFor(() => expect(vi.mocked(notify.failed)).toHaveBeenCalledWith(W.notifyFailed(W.WORKER_SLOW)));
    expect(setEnrollment).toHaveBeenCalledTimes(1);
  });
});
