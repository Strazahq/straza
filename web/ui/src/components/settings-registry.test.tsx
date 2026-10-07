import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RegistryTab } from "./settings-registry";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AttestationHashRow, type ConfigAnswer, deleteAttestationHash, getConfig, listAttestationHashes } from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { CANCEL } from "@/lib/role-words";
import {
  ALLOWED,
  ALLOWED_TIP,
  COPY_HASH,
  CURRENT,
  DIRTY,
  DIRTY_TIP,
  MANAGED_PATH,
  REGISTRY_ADVISORY_LINE,
  REGISTRY_COLUMN,
  REGISTRY_EMPTY_BODY,
  REGISTRY_EMPTY_TITLE,
  REGISTRY_LEDE_MANAGED,
  REGISTRY_LEDE_NONE,
  REGISTRY_NONE_LINE,
  REGISTRY_NONE_STRIP,
  REGISTRY_VERIFY,
  RETIRE,
  RETIRE_VERB,
  TABS,
  filePathLine,
  rendersWord,
  retireTitle,
  retiredToast,
  retireBody,
  shortHash,
} from "@/lib/settings-words";
import { agoWord } from "@/lib/settings-words";

const CLEAN_NOTE = "harness-config render (strazad v1.0.0-713-g62d5f87)";
const DIRTY_NOTE = "harness-config render (strazad v1.0.0-681-g4d5d41a-dirty)";
const CC = "5b1e0c7d9a44f2e6b3d18c0a7f5e2d9c4b6a1e8f0d3c5b7a9e1f2d4c6b8a0e3f";
const CX = "0f3a7c1e5b9d2f4a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8f0a2c4e6b8d0f2a";
const CX_OLD = "e77d3b9f1c5a7e2d4b6f8a0c2e4b6d8f0a2c4e6b8d0f2a4c6e8b0d2f4a6c8e0b";

// Five registrations that fold into three renders: one per artifact, and
// the older codex render that a dirty build registered.
const hashes: AttestationHashRow[] = [
  { id: "ah-1", artifact: "hooks.claude-code", harness: "claude-code", platform: "linux/amd64", hash: CC, note: CLEAN_NOTE, created_at: "2026-09-07T09:12:00Z", current: true },
  { id: "ah-2", artifact: "hooks.claude-code", harness: "claude-code", platform: "windows/amd64", hash: CC, note: CLEAN_NOTE, created_at: "2026-09-07T09:12:00Z", current: true },
  { id: "ah-3", artifact: "hooks.codex", harness: "codex", platform: "linux/amd64", hash: CX, note: CLEAN_NOTE, created_at: "2026-09-07T09:12:00Z", current: true },
  { id: "ah-4", artifact: "hooks.codex", harness: "codex", platform: "windows/amd64", hash: CX, note: CLEAN_NOTE, created_at: "2026-09-07T09:12:00Z", current: true },
  { id: "ah-5", artifact: "hooks.codex", harness: "codex", platform: "linux/amd64", hash: CX_OLD, note: DIRTY_NOTE, created_at: "2026-08-25T17:40:00Z" },
];

const config = (floor: string): ConfigAnswer => ({ profile: "enterprise", governance: { min_attestation: floor } });

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listAttestationHashes: vi.fn(),
  deleteAttestationHash: vi.fn(),
  getConfig: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const mount = () => render(<TooltipProvider><RegistryTab /></TooltipProvider>);
const row = (hash: string) => document.querySelector('[data-render="' + hash + '"]') as HTMLElement;
const bands = () => Array.from(document.querySelectorAll("[data-band]")).map((b) => b.getAttribute("data-band"));
const count = (needle: string) => (document.body.textContent || "").split(needle).length - 1;

describe("the Attestation registry tab", () => {
  beforeEach(() => {
    vi.mocked(listAttestationHashes).mockResolvedValue(hashes);
    vi.mocked(getConfig).mockResolvedValue(config("none"));
    vi.mocked(deleteAttestationHash).mockResolvedValue({});
    vi.mocked(navigate).mockClear();
    vi.mocked(notify.ok).mockClear();
  });

  it("bands the rows by artifact, one row per render, in one grid", async () => {
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    expect(bands()).toEqual(["hooks.claude-code", "hooks.codex"]);
    // The artifact is written once, on its band, and the band carries the
    // renders and the platforms it covers.
    expect(count("hooks.claude-code")).toBe(1);
    expect(count("hooks.codex")).toBe(1);
    const codex = document.querySelector('[data-band="hooks.codex"]') as HTMLElement;
    expect(within(codex).getByText(rendersWord(2, 3))).toBeTruthy();
    expect(within(codex).getByText(filePathLine(MANAGED_PATH["hooks.codex"]))).toBeTruthy();

    // One header row for every artifact, so the columns line up.
    expect(document.querySelectorAll("thead").length).toBe(1);
    expect(Array.from(document.querySelectorAll("th")).map((h) => (h.textContent || "").trim()))
      .toEqual([REGISTRY_COLUMN.hash, REGISTRY_COLUMN.platforms, REGISTRY_COLUMN.status, REGISTRY_COLUMN.registered, ""]);

    // Every render stays reachable, with its platforms and the full hash
    // on hover.
    expect(document.querySelectorAll("[data-render]").length).toBe(3);
    expect(within(row(CC)).getByText(shortHash(CC)).getAttribute("title")).toBe(CC);
    expect(Array.from(row(CC).querySelectorAll("[data-platform]")).map((p) => p.textContent)).toEqual(["linux/amd64", "windows/amd64"]);
  });

  it("says current, allowed and dirty build in the right places", async () => {
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    expect((row(CC).querySelector("[data-render-status]") as HTMLElement).textContent).toBe(CURRENT);
    const old = row(CX_OLD).querySelector("[data-render-status]") as HTMLElement;
    expect(old.textContent).toBe(ALLOWED);
    expect(old.getAttribute("title")).toBe(ALLOWED_TIP);

    // The dirty build wears the amber badge on its row alone, and its
    // title says why it should never be in production.
    const dirty = Array.from(document.querySelectorAll("[data-dirty]"));
    expect(dirty.length).toBe(1);
    expect(dirty[0].textContent).toBe(DIRTY);
    expect(dirty[0].getAttribute("title")).toBe(DIRTY_TIP);
    expect(dirty[0].closest("[data-render]")?.getAttribute("data-render")).toBe(CX_OLD);
    expect(dirty[0].className).toContain("text-warn");

    // The note rides the render that is no longer current, with the
    // absolute stamp behind the relative one.
    expect(within(row(CX_OLD)).getByText(DIRTY_NOTE)).toBeTruthy();
    expect(within(row(CX_OLD)).getByText(agoWord("2026-08-25T17:40:00Z"), { selector: "span" })).toBeTruthy();
  });

  it("names the commands a person can actually run to verify a box", async () => {
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    const lede = document.querySelector("[data-registry-lede]") as HTMLElement;
    expect(lede.textContent).toContain("sha256sum");
    expect(lede.textContent).toContain("strazactl attestation list");
    expect(lede.textContent).toContain(REGISTRY_LEDE_NONE);
    // make verify-release needs a source tree, so it is no longer offered.
    expect(document.body.textContent).not.toContain("make verify-release");
  });

  it("warns while the floor decides nothing and doors to the row that raises it", async () => {
    mount();
    const strip = await screen.findByText(REGISTRY_NONE_STRIP);
    expect(within(strip.parentElement as HTMLElement).getByText(REGISTRY_NONE_LINE)).toBeTruthy();
    await userEvent.click(screen.getByRole("link", { name: new RegExp(TABS[0].label) }));
    expect(window.location.hash).toBe("#floor");
    expect(navigate).toHaveBeenCalledWith("settings", ["configuration"], true);
  });

  it("says what advisory costs, and drops the strip once the floor is managed", async () => {
    vi.mocked(getConfig).mockResolvedValue(config("advisory"));
    mount();
    await screen.findByText(REGISTRY_ADVISORY_LINE);
    expect(screen.getByText(REGISTRY_NONE_STRIP)).toBeTruthy();

    vi.mocked(getConfig).mockResolvedValue(config("managed"));
    mount();
    await waitFor(() => expect(screen.getAllByText(new RegExp(REGISTRY_LEDE_MANAGED)).length).toBeGreaterThan(0));
    expect(screen.queryAllByText(REGISTRY_NONE_STRIP).length).toBe(1);
  });

  it("retires every registration of a render behind one dialog", async () => {
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    await userEvent.click(within(row(CC)).getByRole("button", { name: RETIRE }));
    expect(await screen.findByText(retireTitle(shortHash(CC)))).toBeTruthy();
    expect(screen.getByText(retireBody(2))).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: RETIRE_VERB }));
    await waitFor(() => expect(deleteAttestationHash).toHaveBeenCalledTimes(2));
    expect(vi.mocked(deleteAttestationHash).mock.calls.map((c) => c[0])).toEqual(["ah-1", "ah-2"]);
    expect(notify.ok).toHaveBeenCalledWith(retiredToast(shortHash(CC)));
    await waitFor(() => expect(screen.queryByText(retireTitle(shortHash(CC)))).toBeNull());
  });

  it("keeps the dialog open with the server's own sentence when a retire is refused", async () => {
    vi.mocked(deleteAttestationHash).mockRejectedValue(new ApiError("the last current render cannot be retired", 409));
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    await userEvent.click(within(row(CX_OLD)).getByRole("button", { name: RETIRE }));
    await userEvent.click(screen.getByRole("button", { name: RETIRE_VERB }));
    const refusal = await screen.findByRole("alert");
    expect(refusal.textContent).toContain(RETIRE_VERB + " refused.");
    expect(refusal.textContent).toContain("the last current render cannot be retired");
    expect(screen.getByText(retireTitle(shortHash(CX_OLD)))).toBeTruthy();
    expect(screen.getByRole("button", { name: CANCEL })).toBeTruthy();
    expect(document.querySelectorAll("[data-render]").length).toBe(3);
  });

  it("says what an empty registry means", async () => {
    vi.mocked(listAttestationHashes).mockResolvedValue([]);
    mount();
    expect(await screen.findByText(REGISTRY_EMPTY_TITLE)).toBeTruthy();
    expect(screen.getByText(REGISTRY_EMPTY_BODY)).toBeTruthy();
    expect(document.querySelector("table")).toBeNull();
  });

  it("keeps the table on screen when a later read fails, and says so", async () => {
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    vi.mocked(listAttestationHashes).mockRejectedValue(new ApiError("HTTP 503", 503));
    await userEvent.click(within(row(CX_OLD)).getByRole("button", { name: RETIRE }));
    await userEvent.click(screen.getByRole("button", { name: RETIRE_VERB }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("The attestation registry could not be read");
    expect(document.querySelectorAll("[data-render]").length).toBe(3);
  });

  it("says what failed when the first read does not land", async () => {
    vi.mocked(listAttestationHashes).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("The attestation registry could not be read because strazad did not answer");
    expect(document.querySelector("table")).toBeNull();
  });

  it("copies the full hash, not the short one", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mount();
    await screen.findByText(REGISTRY_NONE_STRIP);
    await userEvent.click(within(row(CX)).getByRole("button", { name: COPY_HASH }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(CX));
  });

  it("keeps the verify caption on screen when the floor cannot be read", async () => {
    vi.mocked(getConfig).mockRejectedValue(new ApiError("forbidden", 403));
    mount();
    await waitFor(() => expect(document.querySelectorAll("[data-render]").length).toBe(3));
    expect(screen.queryByText(REGISTRY_NONE_STRIP)).toBeNull();
    expect((document.querySelector("[data-registry-lede]") as HTMLElement).textContent).toContain(REGISTRY_VERIFY);
  });
});
