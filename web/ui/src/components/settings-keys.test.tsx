import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { KeysTab } from "./settings-keys";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError } from "@/lib/api";
import {
  type SigningKeyRow, assertionKeyCertificate, entraBlueprints, listSigningKeys, promoteAssertionKey, retireAssertionKey, rotateAssertionKey, thumbprintOf,
} from "@/lib/keys-api";
import {
  CERTIFICATE, CREATE_NEW_KEY, DOWNLOAD_CERT, KEYS_EMPTY_TITLE, KEY_COLUMN, OPEN_USERS, PROMOTE, PROMOTED_LINE, PROMOTE_READY, RETIRE, RETIRE_ACTIVE_BODY, RETIRE_ENTRA, RETIRE_STAGED_BODY, ROTATE_TITLE,
  keysEmptyBody, keysLede, promotePlainBody, promoteTick, promoteTitle, promotedToast, removeOldLine, removeOldTitle, retiredToast, retiresLine, rotateBody, showRetired, stagedToast, uploadLine,
} from "@/lib/keys-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";

vi.mock("@/lib/keys-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/keys-api")>()),
  listSigningKeys: vi.fn(),
  rotateAssertionKey: vi.fn(),
  promoteAssertionKey: vi.fn(),
  retireAssertionKey: vi.fn(),
  assertionKeyCertificate: vi.fn(),
  entraBlueprints: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

// FIXTURE_PEM is a throwaway self-signed certificate made with openssl for
// this suite. FIXTURE_SHA1 is what openssl x509 -fingerprint -sha1 printed
// for it, colons removed, the form Entra lists a certificate by.
const FIXTURE_PEM = `-----BEGIN CERTIFICATE-----
MIIBbTCCARSgAwIBAgIUGQAS2Qt0j9WCAvJxn2hLh0B4BdAwCgYIKoZIzj0EAwIw
HTEbMBkGA1UEAwwSdGh1bWJwcmludC1maXh0dXJlMB4XDTI2MTEwODE5MjAyNVoX
DTI2MTEwOTE5MjAyNVowHTEbMBkGA1UEAwwSdGh1bWJwcmludC1maXh0dXJlMFkw
EwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEe6ZgQGSwDFTEMhIO9JCz1uz5a1LvTjco
loFz/ARgzklqpJj7seKN6AJrlIaQe84YRl1/09TuC1ICRSvyldAtEKMyMDAwHQYD
VR0OBBYEFElhOORzgCJGAR0nE0Nz9WtS4A/yMA8GA1UdEwEB/wQFMAMBAf8wCgYI
KoZIzj0EAwIDRwAwRAIgexX+lTs1oszguHE81s6qihXsc6QNL7aP0vUhr+VXLwYC
ICdUR01RtxHG6NU/19A9H8ybM5JWT/g3Ud8J4qbkp6ZG
-----END CERTIFICATE-----
`;
const FIXTURE_SHA1 = "234C81517FDCF40953905E65390373225AD7680A";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const ACTIVE: SigningKeyRow = { kid: "6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", purpose: "client_assertion", status: "active", created_at: ago(50 * 86_400_000), certificate_expires_at: "2028-09-20T09:14:00Z" };
const STAGED: SigningKeyRow = { kid: "3a9e41c2-58d0-4f7b-b1e6-7c2d90a4f513", purpose: "client_assertion", status: "staged", created_at: ago(120_000), certificate_expires_at: "2028-11-07T15:02:00Z" };
const BLUEPRINT = "76568ffa-de8d-4d35-986d-4efe2a9f8787";
const SECOND_BLUEPRINT = "ca0735eb-c5c4-4d43-a20e-4b953c68f76a";
const RETIRED: SigningKeyRow = { kid: "1b7d0a93-2c44-4e18-8f0b-5a6e7d9c3b21", purpose: "client_assertion", status: "retired", created_at: ago(90 * 86_400_000) };

const mount = (rotateRequest = 0, onRows = vi.fn()) => render(<TooltipProvider><KeysTab rotateRequest={rotateRequest} onRows={onRows} /></TooltipProvider>);
// findPanel waits for the rotation panel of one kind; waitFor retries only
// while its callback throws.
const findPanel = (kind: string) => waitFor(() => {
  const el = document.querySelector("[data-rotation=" + kind + "]");
  if (!el) throw new Error("no " + kind + " panel yet");
  return el as HTMLElement;
});
const statuses = () => Array.from(document.querySelectorAll("[data-key-status]")).map((b) => b.getAttribute("data-key-status"));

describe("the Agent signing keys tab", () => {
  beforeEach(() => {
    vi.mocked(entraBlueprints).mockReset().mockResolvedValue([BLUEPRINT]);
    vi.mocked(listSigningKeys).mockReset().mockResolvedValue([ACTIVE, RETIRED]);
    vi.mocked(rotateAssertionKey).mockReset().mockResolvedValue({ kid: STAGED.kid });
    vi.mocked(promoteAssertionKey).mockReset().mockResolvedValue({ kid: STAGED.kid, status: "active", was: "staged", previous_kid: ACTIVE.kid, previous_key_leaves_document_in_seconds: 90 });
    vi.mocked(retireAssertionKey).mockReset().mockResolvedValue({ kid: ACTIVE.kid });
    vi.mocked(assertionKeyCertificate).mockReset().mockResolvedValue(FIXTURE_PEM);
    vi.mocked(notify.ok).mockClear();
    vi.mocked(navigate).mockClear();
  });

  it("computes the thumbprint Entra lists, from the certificate's DER bytes", async () => {
    expect(await thumbprintOf(FIXTURE_PEM)).toBe(FIXTURE_SHA1);
  });

  it("lists the live keys, folds the retired ones, and hands every read to the page", async () => {
    const onRows = vi.fn();
    mount(0, onRows);
    await waitFor(() => expect(statuses()).toEqual(["active"]));
    expect(onRows).toHaveBeenCalledWith([ACTIVE, RETIRED]);
    await userEvent.click(screen.getByRole("button", { name: showRetired(1) }));
    expect(statuses()).toEqual(["active", "retired"]);
  });

  it("says there is no key yet when the list is empty", async () => {
    vi.mocked(listSigningKeys).mockResolvedValue([]);
    mount();
    await screen.findByText(KEYS_EMPTY_TITLE);
  });

  it("rotates only after the confirm, once, and reads the list again", async () => {
    const view = mount(0);
    await waitFor(() => expect(statuses()).toEqual(["active"]));
    view.rerender(<TooltipProvider><KeysTab rotateRequest={1} onRows={vi.fn()} /></TooltipProvider>);
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(ROTATE_TITLE)).toBeTruthy();
    expect(within(dialog).getByText(rotateBody(ACTIVE.kid))).toBeTruthy();
    expect(rotateAssertionKey).not.toHaveBeenCalled();
    vi.mocked(listSigningKeys).mockResolvedValue([STAGED, ACTIVE]);
    await userEvent.click(within(dialog).getByRole("button", { name: CREATE_NEW_KEY }));
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith(stagedToast(STAGED.kid)));
    expect(rotateAssertionKey).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(statuses()).toEqual(["staged", "active"]));
  });

  it("keeps Promote off until 60 seconds after staging", async () => {
    vi.mocked(listSigningKeys).mockResolvedValue([{ ...STAGED, created_at: ago(5_000) }, ACTIVE]);
    mount();
    const panel = await findPanel("staged");
    const wait = Number(panel.querySelector("[data-promote-wait]")?.getAttribute("data-promote-wait"));
    expect(wait).toBeGreaterThan(50);
    expect(wait).toBeLessThanOrEqual(55);
    expect((within(panel).getByRole("button", { name: PROMOTE }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("names the blueprint, shows the staged key's thumbprint and promotes only with the box ticked", async () => {
    vi.mocked(listSigningKeys).mockResolvedValue([STAGED, ACTIVE]);
    mount();
    const panel = await findPanel("staged");
    expect(within(panel).getByText(uploadLine([BLUEPRINT]))).toBeTruthy();
    expect(panel.querySelector("[data-blueprint]")?.getAttribute("data-blueprint")).toBe(BLUEPRINT);
    await waitFor(() => expect(panel.querySelector("[data-thumbprint]")?.getAttribute("data-thumbprint")).toBe(FIXTURE_SHA1));
    expect(within(panel).getByText(PROMOTE_READY)).toBeTruthy();
    await userEvent.click(within(panel).getByRole("button", { name: PROMOTE }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(promoteTitle(STAGED.kid))).toBeTruthy();
    expect(within(dialog).getByText(promoteTick(FIXTURE_SHA1, [BLUEPRINT]))).toBeTruthy();
    const go = within(dialog).getByRole("button", { name: PROMOTE });
    expect((go as HTMLButtonElement).disabled).toBe(true);
    await userEvent.click(within(dialog).getByRole("checkbox"));
    await userEvent.click(go);
    await waitFor(() => expect(promoteAssertionKey).toHaveBeenCalledWith(STAGED.kid));
    expect(promoteAssertionKey).toHaveBeenCalledTimes(1);
    expect(notify.ok).toHaveBeenCalledWith(promotedToast(STAGED.kid));
  });

  it("shows what is left after a promote while the previous key retires", async () => {
    vi.mocked(listSigningKeys).mockResolvedValue([{ ...STAGED, status: "active" }, { ...ACTIVE, status: "retiring" }]);
    mount();
    const panel = await findPanel("retiring");
    expect(within(panel).getByText(removeOldTitle([BLUEPRINT]))).toBeTruthy();
    expect(within(panel).getByText(removeOldLine(ACTIVE.kid, null))).toBeTruthy();
    await userEvent.click(within(panel).getByRole("button", { name: OPEN_USERS }));
    expect(navigate).toHaveBeenCalledWith("users");
  });

  it.each([
    { name: "the active key", key: ACTIVE, body: RETIRE_ACTIVE_BODY },
    { name: "a staged key", key: STAGED, body: RETIRE_STAGED_BODY },
  ])("retires $name only after a confirm that says what stops", async ({ key, body }) => {
    vi.mocked(listSigningKeys).mockResolvedValue([key]);
    mount();
    await userEvent.click(await screen.findByRole("button", { name: RETIRE + " " + key.kid.slice(0, 8) }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(body)).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: RETIRE }));
    await waitFor(() => expect(retireAssertionKey).toHaveBeenCalledWith(key.kid));
    expect(notify.ok).toHaveBeenCalledWith(retiredToast(key.kid));
  });

  it("lists every blueprint when several providers use Entra", async () => {
    vi.mocked(entraBlueprints).mockResolvedValue([BLUEPRINT, SECOND_BLUEPRINT]);
    vi.mocked(listSigningKeys).mockResolvedValue([STAGED, ACTIVE]);
    mount();
    const panel = await findPanel("staged");
    expect(within(panel).getByText(uploadLine([BLUEPRINT, SECOND_BLUEPRINT]))).toBeTruthy();
    expect(Array.from(panel.querySelectorAll("[data-blueprint]"), (e) => e.getAttribute("data-blueprint"))).toEqual([BLUEPRINT, SECOND_BLUEPRINT]);
    await waitFor(() => expect(panel.querySelector("[data-thumbprint]")).toBeTruthy());
    await userEvent.click(within(panel).getByRole("button", { name: PROMOTE }));
    expect(within(await screen.findByRole("alertdialog")).getByText(promoteTick(FIXTURE_SHA1, [BLUEPRINT, SECOND_BLUEPRINT]))).toBeTruthy();
  });

  it("keeps the Entra steps with their hedges when the providers cannot be read", async () => {
    vi.mocked(entraBlueprints).mockResolvedValue(null);
    vi.mocked(listSigningKeys).mockResolvedValue([STAGED, ACTIVE]);
    mount();
    const panel = await findPanel("staged");
    expect(within(panel).getByText(uploadLine(null))).toBeTruthy();
    expect(panel.querySelector("[data-blueprint]")).toBeNull();
    await waitFor(() => expect(panel.querySelector("[data-thumbprint]")).toBeTruthy());
    await userEvent.click(within(panel).getByRole("button", { name: PROMOTE }));
    expect(within(await screen.findByRole("alertdialog")).getByText(promoteTick(FIXTURE_SHA1, null))).toBeTruthy();
  });

  describe("when no provider uses Entra", () => {
    beforeEach(() => { vi.mocked(entraBlueprints).mockResolvedValue([]); });

    it("shows no certificate, no upload step and promotes without a box", async () => {
      vi.mocked(listSigningKeys).mockResolvedValue([STAGED, ACTIVE]);
      mount();
      const panel = await findPanel("staged");
      expect(screen.getByText(keysLede(false))).toBeTruthy();
      expect(screen.queryByText(KEY_COLUMN.until)).toBeNull();
      expect(screen.queryByRole("button", { name: new RegExp("^" + CERTIFICATE) })).toBeNull();
      expect(within(panel).queryByRole("button", { name: DOWNLOAD_CERT })).toBeNull();
      expect(Array.from(panel.querySelectorAll("[data-step]"), (e) => e.getAttribute("data-step"))).toEqual(["1", "2"]);
      expect(assertionKeyCertificate).not.toHaveBeenCalled();
      await userEvent.click(within(panel).getByRole("button", { name: PROMOTE }));
      const dialog = await screen.findByRole("alertdialog");
      expect(within(dialog).getByText(promotePlainBody(ACTIVE.kid))).toBeTruthy();
      expect(within(dialog).queryByRole("checkbox")).toBeNull();
      await userEvent.click(within(dialog).getByRole("button", { name: PROMOTE }));
      await waitFor(() => expect(promoteAssertionKey).toHaveBeenCalledWith(STAGED.kid));
    });

    it("only says when the previous key retires after a promote", async () => {
      vi.mocked(listSigningKeys).mockResolvedValue([{ ...STAGED, status: "active" }, { ...ACTIVE, status: "retiring" }]);
      mount();
      const panel = await findPanel("retiring");
      expect(within(panel).getByText(PROMOTED_LINE + " " + retiresLine(ACTIVE.kid, null))).toBeTruthy();
      expect(within(panel).queryByRole("button", { name: OPEN_USERS })).toBeNull();
    });

    it("retires without the Entra sentence and says how to start without Entra", async () => {
      vi.mocked(listSigningKeys).mockResolvedValue([ACTIVE]);
      const view = mount();
      await userEvent.click(await screen.findByRole("button", { name: RETIRE + " " + ACTIVE.kid.slice(0, 8) }));
      const dialog = await screen.findByRole("alertdialog");
      expect(within(dialog).getByText(RETIRE_ACTIVE_BODY)).toBeTruthy();
      expect(within(dialog).queryByText(RETIRE_ENTRA)).toBeNull();
      view.unmount();
      vi.mocked(listSigningKeys).mockResolvedValue([]);
      mount();
      expect(await screen.findByText(keysEmptyBody([]))).toBeTruthy();
    });
  });

  it("keeps the confirm open with the server's sentence when it refuses", async () => {
    vi.mocked(promoteAssertionKey).mockRejectedValue(new ApiError("the client assertion key was staged moments ago. Run the command again in 12 seconds.", 409));
    vi.mocked(listSigningKeys).mockResolvedValue([STAGED, ACTIVE]);
    mount();
    const panel = await findPanel("staged");
    await userEvent.click(within(panel).getByRole("button", { name: PROMOTE }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("checkbox"));
    await userEvent.click(within(dialog).getByRole("button", { name: PROMOTE }));
    await waitFor(() => expect(within(dialog).getByText(/Run the command again in 12 seconds/)).toBeTruthy());
    expect(notify.ok).not.toHaveBeenCalled();
  });
});
