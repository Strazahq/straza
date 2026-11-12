import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Settings } from "./settings";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApiTokenRow, type AttestationHashRow, listApiTokens, listAttestationHashes } from "@/lib/api";
import { DRAFT_TITLE } from "@/lib/config-words";
import { type SigningKeyRow, listSigningKeys } from "@/lib/keys-api";
import { CREATE_KEY, ROTATE_KEY, hiddenKeysTabLine } from "@/lib/keys-words";
import { navigate, pathFor } from "@/lib/router";
import { adminAreas } from "@/lib/session";
import { NEW_TOKEN, TABS, hiddenTabsLine, hiddenTokensTabLine } from "@/lib/settings-words";

// Three renders over two artifacts: the codex band carries an older render
// beside the current one, so the tab count is renders and not rows.
const hashes: AttestationHashRow[] = [
  { id: "ah-1", artifact: "hooks.claude-code", harness: "claude-code", platform: "linux/amd64", hash: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", current: true },
  { id: "ah-2", artifact: "hooks.claude-code", harness: "claude-code", platform: "windows/amd64", hash: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", current: true },
  { id: "ah-3", artifact: "hooks.codex", harness: "codex", platform: "linux/amd64", hash: "cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333cccc3333", current: true },
  { id: "ah-4", artifact: "hooks.codex", harness: "codex", platform: "linux/amd64", hash: "dddd4444dddd4444dddd4444dddd4444dddd4444dddd4444dddd4444dddd4444" },
];

const tokens: ApiTokenRow[] = [
  { id: "t1", name: "midpoint", scope: "scim:read,scim:write", created: "2026-09-07T09:14:00Z" },
  { id: "t2", name: "siem-reader", scope: "audit:read", created: "2026-09-01T14:40:00Z" },
];

// One active client assertion key, one retired, and a session key the tab
// leaves out, so the count is the one live client assertion key.
const keys: SigningKeyRow[] = [
  { kid: "6f1c2f0e-3b7a-4d19-9a55-2f4c8e1b7d10", purpose: "client_assertion", status: "active", created_at: "2026-09-21T09:14:00Z", certificate_expires_at: "2028-09-20T09:14:00Z" },
  { kid: "1b7d0a93-2c44-4e18-8f0b-5a6e7d9c3b21", purpose: "client_assertion", status: "retired", created_at: "2026-09-02T11:40:00Z" },
  { kid: "s-1", purpose: "session", status: "active", created_at: "2026-09-01T00:00:00Z" },
];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listAttestationHashes: vi.fn(),
  listApiTokens: vi.fn(),
}));
vi.mock("@/lib/keys-api", async (orig) => ({ ...(await orig<typeof import("@/lib/keys-api")>()), listSigningKeys: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), adminAreas: vi.fn() }));

// The three tabs are driven by their own suites, so the page's suite
// stands them in as markers and watches what the page hands them.
vi.mock("@/components/settings-config", () => ({
  ConfigTab: ({ draftRequest }: { draftRequest: number }) => <div data-tab="configuration" data-draft-request={draftRequest} />,
}));
vi.mock("@/components/settings-registry", () => ({ RegistryTab: () => <div data-tab="registry" /> }));
vi.mock("@/components/settings-tokens", () => ({
  TokensTab: ({ mintRequest }: { mintRequest: number }) => <div data-tab="tokens" data-mint-request={mintRequest} />,
}));
vi.mock("@/components/settings-keys", () => ({
  KeysTab: ({ rotateRequest }: { rotateRequest: number }) => <div data-tab="keys" data-rotate-request={rotateRequest} />,
}));

const mount = (tab?: string) => render(<TooltipProvider><Settings tab={tab} /></TooltipProvider>);
const tabNames = () => screen.getAllByRole("tab").map((t) => (t.textContent || "").replace(/\s+/g, " ").trim());
const actions = () => Array.from(document.querySelectorAll("[data-page-head] button")).map((b) => (b.textContent || "").trim()).filter(Boolean);
const panel = (name: string) => document.querySelector('[data-tab="' + name + '"]');

describe("the Settings page", () => {
  beforeEach(() => {
    vi.mocked(listAttestationHashes).mockResolvedValue(hashes);
    vi.mocked(listApiTokens).mockResolvedValue(tokens);
    vi.mocked(listSigningKeys).mockReset().mockResolvedValue(keys);
    vi.mocked(adminAreas).mockReturnValue(null);
    vi.mocked(navigate).mockClear();
  });

  it("opens on Configuration when the address names no tab, and counts the other three", async () => {
    mount();
    await waitFor(() => expect(tabNames()).toEqual([TABS[0].label, TABS[1].label + " 3", TABS[2].label + " 2", TABS[3].label + " 1"]));
    expect(panel("configuration")).toBeTruthy();
    expect(panel("registry")).toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeTruthy();
    expect(screen.queryByText(pathFor("settings", ["configuration"]))).toBeNull();
  });

  it("opens the tab the address names and puts a picked tab in the address", async () => {
    mount("registry");
    await screen.findByRole("tab", { name: /Attestation registry/ });
    expect(panel("registry")).toBeTruthy();
    expect(screen.queryByText(pathFor("settings", ["registry"]))).toBeNull();
    await userEvent.click(screen.getByRole("tab", { name: /API tokens/ }));
    expect(navigate).toHaveBeenCalledWith("settings", ["tokens"], true);
  });

  it("gives each tab its own action and no other", async () => {
    mount();
    await screen.findByRole("tab", { name: /Configuration/ });
    expect(actions()).toEqual([DRAFT_TITLE]);
    expect(screen.getByRole("button", { name: DRAFT_TITLE }).getAttribute("data-variant")).toBe("outline");

    mount("registry");
    await screen.findAllByRole("tab", { name: /Attestation registry/ });
    expect(Array.from(document.querySelectorAll("[data-page-head]")).map((h) => Array.from(h.querySelectorAll("button")).length)).toEqual([1, 0]);

    mount("tokens");
    await screen.findAllByRole("tab", { name: /API tokens/ });
    const mint = screen.getByRole("button", { name: new RegExp(NEW_TOKEN) });
    expect(mint.getAttribute("data-variant")).toBe("default");
  });

  it("moves the counter the tab opens its sheet from", async () => {
    mount("tokens");
    await screen.findByRole("tab", { name: /API tokens/ });
    expect(panel("tokens")?.getAttribute("data-mint-request")).toBe("0");
    await userEvent.click(screen.getByRole("button", { name: new RegExp(NEW_TOKEN) }));
    await waitFor(() => expect(panel("tokens")?.getAttribute("data-mint-request")).toBe("1"));

    mount();
    await screen.findAllByRole("tab", { name: /Configuration/ });
    await userEvent.click(screen.getAllByRole("button", { name: DRAFT_TITLE })[0]);
    await waitFor(() => expect(document.querySelectorAll('[data-tab="configuration"]')[0].getAttribute("data-draft-request")).toBe("1"));
  });

  it("shows a delegated seat only the tabs its grants cover, and names the rest", async () => {
    vi.mocked(adminAreas).mockReturnValue({ tokens: true });
    mount();
    await waitFor(() => expect(tabNames()).toEqual([TABS[2].label + " 2"]));
    expect(panel("tokens")).toBeTruthy();
    expect((document.querySelector("[data-hidden-tabs]") as HTMLElement).textContent).toBe(hiddenTabsLine([TABS[0].label, TABS[1].label]) + " " + hiddenKeysTabLine(TABS[3].label));
    // The seat may not read the registry or the keys, so the page never
    // asks for them.
    expect(listAttestationHashes).not.toHaveBeenCalled();
    expect(listSigningKeys).not.toHaveBeenCalled();
  });

  it("names the tokens tab when the seat holds config alone", async () => {
    vi.mocked(adminAreas).mockReturnValue({ config: true });
    mount();
    await waitFor(() => expect(tabNames()).toEqual([TABS[0].label, TABS[1].label + " 3"]));
    expect((document.querySelector("[data-hidden-tabs]") as HTMLElement).textContent).toBe(hiddenTokensTabLine(TABS[2].label) + " " + hiddenKeysTabLine(TABS[3].label));
    expect(listApiTokens).not.toHaveBeenCalled();
  });

  it("leaves a count off the label when its read fails, and keeps the page", async () => {
    vi.mocked(listAttestationHashes).mockRejectedValue(new ApiError("HTTP 503", 503));
    mount();
    await waitFor(() => expect(tabNames()).toEqual([TABS[0].label, TABS[1].label, TABS[2].label + " 2", TABS[3].label + " 1"]));
    expect(panel("configuration")).toBeTruthy();
  });

  it("gives the keys tab Rotate key, Create key when there is no key, and nothing while a rotation runs", async () => {
    mount("keys");
    const rotate = await screen.findByRole("button", { name: new RegExp(ROTATE_KEY) });
    expect(panel("keys")?.getAttribute("data-rotate-request")).toBe("0");
    await userEvent.click(rotate);
    await waitFor(() => expect(panel("keys")?.getAttribute("data-rotate-request")).toBe("1"));

    vi.mocked(listSigningKeys).mockResolvedValue([keys[1]]);
    const none = mount("keys");
    await waitFor(() => expect(within(none.container).getByRole("button", { name: new RegExp(CREATE_KEY) })).toBeTruthy());

    vi.mocked(listSigningKeys).mockResolvedValue([keys[0], { ...keys[0], kid: "3a9e41c2-58d0-4f7b-b1e6-7c2d90a4f513", status: "staged" }]);
    const staged = mount("keys");
    await waitFor(() => expect(within(staged.container).getByRole("tab", { name: new RegExp(TABS[3].label + " 2") })).toBeTruthy());
    expect(within(staged.container).queryByRole("button", { name: new RegExp(ROTATE_KEY + "|" + CREATE_KEY) })).toBeNull();
  });
});
