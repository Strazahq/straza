import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { CANCEL } from "@/lib/approval-words";
import { notify } from "@/lib/notify";
import { ApiError, readSelf, selfEnrollToken } from "@/lib/api";
import { enrollDevice } from "./approver-api";
import { SelfContext, type SelfState } from "./context";
import { EnableSheet } from "./enable-sheet";
import { exportPublicKeyB64, generateDeviceKey } from "./sign";
import { requestDurable, saveEnrollment } from "./store";
import * as W from "./words";

// The run's three server calls and its three local ones are the seam. The
// sign-in card is the console's own screen, stubbed to the one thing this
// sheet asks of it: say when a session is in hand.
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
vi.mock("@/components/sign-in", () => ({
  SignIn: ({ onAuthed }: { onAuthed: (r: unknown) => void }) => (
    <button type="button" onClick={() => onAuthed({ session_token: "st-1", user: "alice" })}>Finish the sign-in</button>
  ),
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
    ...over,
  };
}

const mount = (over: Partial<SelfState> = {}) => render(
  <TooltipProvider>
    <SelfContext.Provider value={shared(over)}><EnableSheet open onOpenChange={onOpenChange} /></SelfContext.Provider>
  </TooltipProvider>,
);

const nameField = () => screen.getByLabelText(W.DEVICE_NAME_LABEL) as HTMLInputElement;
const primary = (label: string) => screen.getByRole("button", { name: label });
const order = (fn: { mock: { invocationCallOrder: number[] } }) => fn.mock.invocationCallOrder[0];
const refusal = () => (document.querySelector("[data-refused-error]") as HTMLElement | null)?.textContent || "";

describe("the Enable this browser sheet", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(readSelf).mockResolvedValue(ANSWER);
    vi.mocked(selfEnrollToken).mockResolvedValue(MINT);
    vi.mocked(enrollDevice).mockResolvedValue(ENROLLED);
    vi.mocked(generateDeviceKey).mockResolvedValue(KEYS);
    vi.mocked(exportPublicKeyB64).mockResolvedValue("PUB");
  });

  it("names the browser, then reads, mints, makes the key and enrols, in that order", async () => {
    mount();
    expect(nameField().value.length).toBeGreaterThan(0);
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Dana's work laptop");
    await userEvent.click(primary(W.ENABLE));

    await waitFor(() => expect(vi.mocked(enrollDevice)).toHaveBeenCalledTimes(1));
    expect(vi.mocked(readSelf)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(selfEnrollToken)).toHaveBeenCalledTimes(1);
    expect(order(vi.mocked(readSelf))).toBeLessThan(order(vi.mocked(selfEnrollToken)));
    // The key is made only once the one-time token is in hand, so a refused
    // mint leaves no orphan key behind.
    expect(order(vi.mocked(selfEnrollToken))).toBeLessThan(order(vi.mocked(generateDeviceKey)));
    expect(vi.mocked(requestDurable)).toHaveBeenCalledTimes(1);
  });

  it("enrols with the honest posture of a browser key", async () => {
    mount();
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Dana's work laptop");
    await userEvent.click(primary(W.ENABLE));

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

  it("keeps the record, closes and says who this browser decides as", async () => {
    mount();
    await userEvent.click(primary(W.ENABLE));

    await waitFor(() => expect(setEnrollment).toHaveBeenCalledTimes(1));
    const rec = setEnrollment.mock.calls[0][0];
    expect(rec.deviceId).toBe("apd_01");
    expect(rec.deviceToken).toBe("dt-live");
    expect(rec.keys).toBe(KEYS);
    expect(rec.webpush).toEqual({ vapid_public_key: "BFxc" });
    expect(vi.mocked(saveEnrollment)).toHaveBeenCalledTimes(1);
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(W.enabledToast("alice"));
  });

  it("refuses an empty name without calling anything", async () => {
    mount();
    await userEvent.clear(nameField());
    await userEvent.click(primary(W.ENABLE));

    expect(screen.getByText(new RegExp(W.DEVICE_NAME_REQUIRED))).toBeTruthy();
    expect(vi.mocked(readSelf)).not.toHaveBeenCalled();
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();
  });

  it("signs in inside the sheet when the page has no session, then runs", async () => {
    mount({ session: { kind: "signed-out" }, self: null });
    await userEvent.click(primary(W.SIGN_IN_AND_ENABLE));
    expect(screen.getByRole("button", { name: "Finish the sign-in" })).toBeTruthy();
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Finish the sign-in" }));
    await waitFor(() => expect(vi.mocked(enrollDevice)).toHaveBeenCalledTimes(1));
    expect(refreshSelf).toHaveBeenCalledTimes(1);
  });

  it("never enrols a flow the person cancelled", async () => {
    let release = (v: typeof MINT) => { void v; };
    vi.mocked(selfEnrollToken).mockReturnValue(new Promise((res) => { release = res; }));
    mount();
    await userEvent.click(primary(W.ENABLE));
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
    await userEvent.click(primary(W.ENABLE));

    await waitFor(() => expect(screen.getByText(W.cooldownLine(30))).toBeTruthy());
    expect(screen.getByRole("button", { name: W.TRY_AGAIN })).toBeTruthy();
    expect(vi.mocked(enrollDevice)).not.toHaveBeenCalled();
  });

  it("leaves the sheet for the honest resting state when the account may not enrol a browser", async () => {
    vi.mocked(readSelf).mockResolvedValue({ ...ANSWER, enroll_channels: ["mobile"] });
    mount();
    await userEvent.click(primary(W.ENABLE));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(vi.mocked(selfEnrollToken)).not.toHaveBeenCalled();
    expect(refreshSelf).toHaveBeenCalledTimes(1);
  });

  it("quotes the server when the enrolment is refused", async () => {
    vi.mocked(selfEnrollToken).mockRejectedValue(new ApiError("browser enrollment is not allowed for this account", 403));
    mount();
    await userEvent.click(primary(W.ENABLE));

    await waitFor(() => expect(screen.getByText(/browser enrollment is not allowed for this account/)).toBeTruthy());
    expect(vi.mocked(enrollDevice)).not.toHaveBeenCalled();
  });

  it("names the device an administrator has to revoke when the record cannot be kept", async () => {
    vi.mocked(saveEnrollment).mockRejectedValue(new Error("the quota is exhausted"));
    mount();
    await userEvent.clear(nameField());
    await userEvent.type(nameField(), "Kiosk");
    await userEvent.click(primary(W.ENABLE));

    await waitFor(() => expect(refusal()).toContain(W.storeRefused("the quota is exhausted", "Kiosk")));
    expect(setEnrollment).not.toHaveBeenCalled();
  });

  it("warns before the key is made that a private window throws it away", () => {
    mount({ storage: { usable: true, outlook: "ephemeral" } });
    expect(screen.getByText(W.PRIVATE_WINDOW)).toBeTruthy();
  });
});
