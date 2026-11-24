import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type SelfDevice, removeSelfDevice, selfDevices } from "@/lib/api";
import { notify } from "@/lib/notify";
import { pending, selfUnenroll } from "./approver-api";
import { DevicesTab } from "./devices-tab";
import { SelfContext, type SelfState } from "./context";
import { disable, enable, retireOldWorker, support, sync, teardown } from "./push";
import type { Enrollment } from "./store";
import { wipeEnrollment } from "./store";
import * as W from "./words";

// The tab's servers are the sign-in lane's device list, the approver lane's
// own removal and the push lane, so all three are the seam.
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfDevices: vi.fn(),
  removeSelfDevice: vi.fn(),
}));
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
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const enrollment = {
  deviceId: "apd_0199c1a27e3f", deviceToken: "dt-live", tokenExpiresAt: Date.now() + 86400000,
  project: { id: "prj_01", name: "acme-prod" }, user: { id: "u-alice", username: "alice" },
  webpush: { vapid_public_key: "BFxc" }, keys: {} as CryptoKeyPair,
  deviceName: "Firefox on Windows", enrolledAt: new Date(Date.now() - 4 * 86400000).toISOString(),
} as Enrollment;

const PUSHED = { kind: "webpush" as const, token_or_endpoint: "https://push.example/wp/s", p256dh: "p", auth: "a" };

const here: SelfDevice = { id: enrollment.deviceId, name: "Firefox on Windows", platform: "browser", key_security_level: "software", created_at: enrollment.enrolledAt, notifies: false };
const iphone: SelfDevice = { id: "apd_iphone", name: "iPhone", platform: "ios", key_security_level: "hardware", attestation: "apple_app_attest", created_at: new Date(Date.now() - 20 * 86400000).toISOString(), last_seen: new Date(Date.now() - 2 * 3600000).toISOString(), notifies: true };
const pixel: SelfDevice = { id: "apd_pixel", name: "Pixel", platform: "android", key_security_level: "software", created_at: new Date(Date.now() - 9 * 86400000).toISOString(), notifies: false };
const laptop: SelfDevice = { id: "apd_laptop", name: "Chrome on macOS", platform: "browser", key_security_level: "software", attestation: "none", created_at: new Date(Date.now() - 3 * 86400000).toISOString(), last_seen: new Date(Date.now() - 600000).toISOString(), notifies: true };

const setEnrollment = vi.fn();
const refreshSelf = vi.fn(async () => undefined);
const setSheet = vi.fn();
const openSignIn = vi.fn();

function shared(over: Partial<SelfState> = {}): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] },
    refreshSelf,
    enrollment,
    setEnrollment,
    storage: { usable: true, outlook: "durable" },
    openSignIn,
    signedIn: vi.fn(),
    notice: null,
    setNotice: () => undefined,
    sheets: { enable: false, phone: false },
    setSheet,
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
    <SelfContext.Provider value={shared(over)}><DevicesTab /></SelfContext.Provider>
  </TooltipProvider>,
);

const part = (attr: string) => document.querySelector("[" + attr + "]") as HTMLElement | null;
const row = () => waitFor(() => {
  const el = part("data-this-browser");
  if (!el) throw new Error("this browser's row is not drawn yet");
  return el;
});
const lineOf = (el: HTMLElement) => (el.querySelector("[data-device-line]") as HTMLElement).textContent;
const listed = (devices: SelfDevice[]) => vi.mocked(selfDevices).mockResolvedValue({ devices });

describe("the Devices tab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    listed([here]);
    vi.mocked(removeSelfDevice).mockResolvedValue(undefined);
    vi.mocked(selfUnenroll).mockResolvedValue(true);
    vi.mocked(sync).mockResolvedValue({ on: true, why: "" });
    vi.mocked(support).mockReturnValue({ available: true, why: "" });
    vi.mocked(disable).mockResolvedValue("");
  });

  it("shows the setup card while this browser is not set up, with Add a phone for an account that may add one", async () => {
    const { unmount } = mount({ enrollment: null });
    expect(part("data-setup-card")).not.toBeNull();
    // The title and the button say it all for a signed-in person.
    expect(part("data-setup-card")!.querySelector("p")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: W.SETUP_BROWSER }));
    expect(setSheet).toHaveBeenCalledWith("enable", true);
    expect(screen.queryByRole("button", { name: W.ADD_PHONE })).toBeNull();
    unmount();

    mount({ enrollment: null, self: { username: "alice", user_kind: "human", enroll_channels: ["browser", "mobile"], sponsored: [] } });
    await userEvent.click(screen.getByRole("button", { name: W.ADD_PHONE }));
    expect(setSheet).toHaveBeenCalledWith("phone", true);
  });

  it("puts a red line or the failed read in the setup card's place", async () => {
    const { unmount } = mount({ enrollment: null, storage: { usable: false, outlook: "unknown" } });
    expect(screen.getByText(W.NO_STORAGE)).toBeTruthy();
    expect(part("data-setup-card")).toBeNull();
    unmount();

    mount({ enrollment: null, self: "error" });
    expect(screen.getByText(W.ELIGIBILITY_UNREAD)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: W.TRY_AGAIN }));
    expect(refreshSelf).toHaveBeenCalledTimes(1);
  });

  it("asks a signed-out visitor to sign in before setting up", async () => {
    mount({ enrollment: null, session: { kind: "signed-out" }, self: null });
    expect(screen.getByText(W.SETUP_SIGN_IN)).toBeTruthy();
    await userEvent.click(within(part("data-setup-card")!).getByRole("button", { name: W.SIGN_IN }));
    expect(openSignIn).toHaveBeenCalledTimes(1);
    expect(vi.mocked(selfDevices)).not.toHaveBeenCalled();
  });

  it("draws this browser's row: its name, the badge, who it approves as and when it was set up", async () => {
    mount();
    const el = await row();
    expect(within(el).getByText("Firefox on Windows").className).toContain("font-semibold");
    expect(within(el).getByText(W.THIS_BROWSER_BADGE).getAttribute("data-tone")).toBe("accent");
    expect(el.querySelector("[data-kind-icon=browser]")).not.toBeNull();
    expect(lineOf(el)).toBe(W.deviceLine("alice", "off", "4 days ago"));
    expect(screen.getByRole("button", { name: W.removeName("Firefox on Windows") })).toBeTruthy();
    expect(part("data-devices-foot")!.textContent).toBe(W.DEVICES_FOOT);
  });

  it("makes no read of the queue", async () => {
    mount();
    await row();
    await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(pending)).not.toHaveBeenCalled();
  });

  it("retires the worker the replaced approvals page left behind", async () => {
    mount();
    await waitFor(() => expect(vi.mocked(retireOldWorker)).toHaveBeenCalledTimes(1));
  });

  it.each([
    ["the deployment sends none", W.PUSH_OFF_DEPLOYMENT],
    ["the browser cannot receive them", W.PUSH_OFF_PLATFORM],
    ["the window is private", W.PUSH_OFF_PRIVATE],
    ["they are blocked for the site", W.PUSH_OFF_BLOCKED],
  ])("says behind the help icon why notifications cannot be offered when %s, and offers no button", async (_what, why) => {
    vi.mocked(support).mockReturnValue({ available: false, why });
    mount();
    const el = await row();
    expect(lineOf(el)).toBe(W.deviceLine("alice", "none", "4 days ago"));
    const help = screen.getByRole("button", { name: "Help: " + W.ABOUT_NOTIFICATIONS });
    await userEvent.click(help);
    await waitFor(() => expect(document.getElementById(help.getAttribute("aria-describedby") || "")?.textContent).toBe(why));
    expect(screen.queryByRole("button", { name: W.TURN_ON })).toBeNull();
    expect(screen.queryByRole("button", { name: W.TURN_OFF })).toBeNull();
  });

  it("reads a stored subscription the sync proved as on, with Turn off notifications", async () => {
    mount({ enrollment: { ...enrollment, push: PUSHED } });
    const el = await row();
    await waitFor(() => expect(lineOf(el)).toBe(W.deviceLine("alice", "on", "4 days ago")));
    expect(screen.getByRole("button", { name: W.TURN_OFF })).toBeTruthy();
  });

  it("says when a subscription could not be renewed", async () => {
    vi.mocked(sync).mockResolvedValue({ on: false, why: W.pushLost("the push service refused") });
    mount({ enrollment: { ...enrollment, push: PUSHED } });
    await waitFor(() => expect(part("data-push-lost")?.textContent).toBe(W.pushLost("the push service refused")));
    expect(screen.getByRole("button", { name: W.TURN_ON })).toBeTruthy();
  });

  it("asks in the page before the browser is ever prompted, then says notifications are on", async () => {
    mount();
    await row();
    await userEvent.click(screen.getByRole("button", { name: W.TURN_ON }));
    expect(screen.getByText(W.CONSENT_TITLE)).toBeTruthy();
    expect(screen.getByText(W.CONSENT_BODY)).toBeTruthy();
    expect(vi.mocked(enable)).not.toHaveBeenCalled();

    vi.mocked(enable).mockResolvedValue(PUSHED);
    await userEvent.click(screen.getByRole("button", { name: W.CONTINUE }));
    await waitFor(() => expect(vi.mocked(enable)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.NOTIFY_ON_TOAST);
    await waitFor(() => expect(screen.getByRole("button", { name: W.TURN_OFF })).toBeTruthy());
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

  it("says why notifications could not be turned on", async () => {
    vi.mocked(enable).mockRejectedValue(new Error(W.WORKER_SLOW));
    mount();
    await row();
    await userEvent.click(screen.getByRole("button", { name: W.TURN_ON }));
    await userEvent.click(screen.getByRole("button", { name: W.CONTINUE }));
    await waitFor(() => expect(vi.mocked(notify.failed)).toHaveBeenCalledWith(W.notifyFailed(W.WORKER_SLOW)));
  });

  it("turns notifications off and says what changed, or that Straza could not be told", async () => {
    const { unmount } = mount({ enrollment: { ...enrollment, push: PUSHED } });
    await waitFor(() => expect(screen.getByRole("button", { name: W.TURN_OFF })).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: W.TURN_OFF }));
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.NOTIFY_OFF_TOAST));
    unmount();

    vi.mocked(disable).mockResolvedValue("the server is unreachable");
    mount({ enrollment: { ...enrollment, push: PUSHED } });
    await waitFor(() => expect(screen.getByRole("button", { name: W.TURN_OFF })).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: W.TURN_OFF }));
    await waitFor(() => expect(vi.mocked(notify.warn)).toHaveBeenCalledWith(W.notifyRemovedLocally("the server is unreachable")));
  });

  it("asks before removing this browser, then retires the device and deletes the key", async () => {
    mount();
    await row();
    await userEvent.click(screen.getByRole("button", { name: W.removeName("Firefox on Windows") }));
    expect(screen.getByText(W.removeTitle("Firefox on Windows"))).toBeTruthy();
    expect(screen.getByText(W.REMOVE_BODY)).toBeTruthy();
    expect(vi.mocked(selfUnenroll)).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: W.REMOVE_DEVICE }));
    await waitFor(() => expect(vi.mocked(selfUnenroll)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(teardown)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(wipeEnrollment)).toHaveBeenCalledTimes(1);
    expect(setEnrollment).toHaveBeenCalledWith(null);
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.REMOVED_TOAST);
  });

  it("deletes the key anyway when Straza would not confirm it forgot this browser", async () => {
    vi.mocked(selfUnenroll).mockResolvedValue(false);
    mount();
    await row();
    await userEvent.click(screen.getByRole("button", { name: W.removeName("Firefox on Windows") }));
    await userEvent.click(screen.getByRole("button", { name: W.REMOVE_DEVICE }));
    await waitFor(() => expect(vi.mocked(wipeEnrollment)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(notify.warn)).toHaveBeenCalledWith(W.REMOVE_UNCONFIRMED);
  });

  it("lists the person's other devices after this browser: a phone with an attested key, one without, and another browser", async () => {
    listed([laptop, pixel, iphone, here]);
    mount();
    await waitFor(() => expect(part("data-device=apd_iphone")).not.toBeNull());
    const rows = [...document.querySelectorAll("[data-devices] > li")];
    expect(rows[0].hasAttribute("data-this-browser")).toBe(true);
    expect(rows.map((r) => r.getAttribute("data-device"))).toEqual([null, "apd_laptop", "apd_pixel", "apd_iphone"]);

    const phone = part("data-device=apd_iphone")!;
    expect(phone.querySelector("[data-kind-icon=phone]")).not.toBeNull();
    expect(lineOf(phone)).toBe(W.otherDeviceLine(true, true, "2 hours ago"));
    expect(lineOf(part("data-device=apd_pixel")!)).toBe(W.otherDeviceLine(false, false, ""));
    const other = part("data-device=apd_laptop")!;
    expect(other.querySelector("[data-kind-icon=browser]")).not.toBeNull();
    expect(lineOf(other)).toBe(W.otherDeviceLine(false, true, "10 minutes ago"));
  });

  it("removes another device after the confirm, then reads the list again", async () => {
    listed([iphone, here]);
    mount();
    await waitFor(() => expect(part("data-device=apd_iphone")).not.toBeNull());
    await userEvent.click(screen.getByRole("button", { name: W.removeName("iPhone") }));
    expect(screen.getByText(W.removeTitle("iPhone"))).toBeTruthy();
    expect(screen.getByText(W.removeOtherBody("iPhone"))).toBeTruthy();
    expect(within(screen.getByRole("alertdialog")).queryByRole("button", { name: /^Help/ })).toBeNull();

    listed([here]);
    await userEvent.click(screen.getByRole("button", { name: W.REMOVE_DEVICE }));
    await waitFor(() => expect(vi.mocked(removeSelfDevice)).toHaveBeenCalledWith("apd_iphone"));
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.removedOtherToast("iPhone"));
    await waitFor(() => expect(part("data-device=apd_iphone")).toBeNull());
    expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(2);
  });

  it("keeps the confirm open with the server's sentence when a removal is refused", async () => {
    listed([iphone, here]);
    vi.mocked(removeSelfDevice).mockRejectedValue(new ApiError("no such device for this person", 404));
    mount();
    await waitFor(() => expect(part("data-device=apd_iphone")).not.toBeNull());
    await userEvent.click(screen.getByRole("button", { name: W.removeName("iPhone") }));
    await userEvent.click(screen.getByRole("button", { name: W.REMOVE_DEVICE }));
    await waitFor(() => expect(part("data-refused-error")?.textContent).toBe(W.DEVICE_NOT_REMOVED + " Straza refused it: no such device for this person. If this keeps happening, tell your administrator."));
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(vi.mocked(notify.ok)).not.toHaveBeenCalled();
  });

  it("keeps this browser's row and offers Try again when the list could not be read", async () => {
    vi.mocked(selfDevices).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    await waitFor(() => expect(part("data-devices-unread")).not.toBeNull());
    expect(screen.getByText(W.DEVICES_UNREAD)).toBeTruthy();
    expect(part("data-this-browser")).not.toBeNull();
    listed([here, iphone]);
    await userEvent.click(screen.getByRole("button", { name: W.TRY_AGAIN }));
    await waitFor(() => expect(part("data-device=apd_iphone")).not.toBeNull());
    expect(part("data-devices-unread")).toBeNull();
  });

  it("reads an older server's 404 as no list and shows no error", async () => {
    vi.mocked(selfDevices).mockRejectedValue(new ApiError("HTTP 404", 404));
    mount();
    await waitFor(() => expect(vi.mocked(selfDevices)).toHaveBeenCalledTimes(1));
    await row();
    expect(part("data-devices-unread")).toBeNull();
    expect(document.querySelectorAll("[data-devices] > li").length).toBe(1);
  });

  it("shows this browser alone to a signed-out person, with Sign in for the other devices", async () => {
    mount({ session: { kind: "signed-out" }, self: null });
    await row();
    expect(vi.mocked(selfDevices)).not.toHaveBeenCalled();
    expect(part("data-sign-in-for-devices")!.textContent).toContain(W.SIGN_IN_FOR_DEVICES);
    await userEvent.click(within(part("data-sign-in-for-devices")!).getByRole("button", { name: W.SIGN_IN }));
    expect(openSignIn).toHaveBeenCalledTimes(1);
  });

  it("shows the setup card first, then the other devices, when this browser is not set up", async () => {
    listed([iphone]);
    mount({ enrollment: null });
    await waitFor(() => expect(part("data-device=apd_iphone")).not.toBeNull());
    const card = part("data-setup-card")!;
    const list = part("data-devices")!;
    expect(card.compareDocumentPosition(list) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(part("data-this-browser")).toBeNull();
    expect(part("data-devices-foot")).not.toBeNull();
  });

  it("tells a phone-only account and a role-less account what is true for them", () => {
    listed([]);
    const { unmount } = mount({ enrollment: null, self: { username: "judy", user_kind: "human", enroll_channels: ["mobile"], sponsored: [] } });
    expect(screen.getByText(W.PHONE_APPROVES)).toBeTruthy();
    expect(part("data-setup-card")).toBeNull();
    unmount();

    mount({ enrollment: null, self: { username: "nhi", user_kind: "nhi", enroll_channels: [], sponsored: [] } });
    expect(screen.getByText(W.NOT_ENROLLED_NO_ROLE)).toBeTruthy();
  });
});
