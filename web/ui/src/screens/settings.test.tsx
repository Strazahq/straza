import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Settings } from "./settings";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApiTokenRow, type AttestationHashRow, listApiTokens, listAttestationHashes } from "@/lib/api";
import { type SigningKeyRow, listSigningKeys } from "@/lib/keys-api";
import { hiddenKeysTabLine } from "@/lib/keys-words";
import { navigate, pathFor } from "@/lib/router";
import { adminAreas } from "@/lib/session";
import { READ_ONLY, hiddenTabsLine, hiddenTokensTabLine } from "@/lib/settings-words";

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

// The four tabs are driven by their own suites, so the page's suite
// stands them in as markers and watches what the page hands them.
vi.mock("@/components/settings-config", () => ({
  ConfigTab: ({ row }: { row?: string }) => <div data-tab="configuration" data-row={row} />,
}));
vi.mock("@/components/settings-registry", () => ({ RegistryTab: () => <div data-tab="registry" /> }));
vi.mock("@/components/settings-tokens", () => ({ TokensTab: () => <div data-tab="tokens" /> }));
vi.mock("@/components/settings-keys", () => ({ KeysTab: () => <div data-tab="keys" /> }));

const mount = (tab?: string, row?: string) => render(<TooltipProvider><Settings tab={tab} row={row} /></TooltipProvider>);
const tabNames = () => screen.getAllByRole("tab").map((t) => (t.textContent || "").replace(/\s+/g, " ").trim());
const panel = (name: string) => document.querySelector('[data-tab="' + name + '"]');

describe("the Settings page", () => {
  beforeEach(() => {
    vi.mocked(listAttestationHashes).mockResolvedValue(hashes);
    vi.mocked(listApiTokens).mockResolvedValue(tokens);
    vi.mocked(listSigningKeys).mockReset().mockResolvedValue(keys);
    vi.mocked(adminAreas).mockReturnValue(null);
    vi.mocked(navigate).mockClear();
  });

  it("opens a full administrator on API tokens, puts Configuration last, and counts the other three", async () => {
    mount();
    await waitFor(() => expect(tabNames()).toEqual(["API tokens 2", "Agent signing keys 1", "Attestation registry 3", "Configuration " + READ_ONLY]));
    expect(panel("tokens")).toBeTruthy();
    expect(panel("configuration")).toBeNull();
    expect(screen.getByRole("heading", { level: 1, name: "Settings" })).toBeTruthy();
    expect(screen.queryByText("API tokens, agent signing keys, the attestation registry and the deployment's configuration.")).toBeNull();
    expect(screen.queryByText(pathFor("settings", ["tokens"]))).toBeNull();
  });

  it("marks Configuration read-only on the tab itself", async () => {
    mount();
    const config = await screen.findByRole("tab", { name: /^Configuration/ });
    expect(config.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
    expect(within(config).getByText(READ_ONLY).getAttribute("data-tone")).toBe("plain");
  });

  it("puts no action in the page head, since each tab carries its own", async () => {
    for (const tab of ["tokens", "keys", "registry", "configuration"]) {
      const view = mount(tab);
      await screen.findByRole("tab", { name: /^API tokens/ });
      expect((view.container.querySelector("[data-page-head]") as HTMLElement).querySelectorAll("button").length).toBe(0);
      view.unmount();
    }
  });

  it("hands the row the address names to the Configuration tab", async () => {
    mount("configuration", "floor");
    await screen.findByRole("tab", { name: /^Configuration/ });
    expect(panel("configuration")?.getAttribute("data-row")).toBe("floor");
  });

  it("opens the tab the address names and puts a picked tab in the address", async () => {
    mount("registry");
    await screen.findByRole("tab", { name: /Attestation registry/ });
    expect(panel("registry")).toBeTruthy();
    expect(screen.queryByText(pathFor("settings", ["registry"]))).toBeNull();
    await userEvent.click(screen.getByRole("tab", { name: /API tokens/ }));
    expect(navigate).toHaveBeenCalledWith("settings", ["tokens"], true);
  });

  it("opens a tokens-only seat on API tokens, and names the rest", async () => {
    vi.mocked(adminAreas).mockReturnValue({ tokens: true });
    mount();
    await waitFor(() => expect(tabNames()).toEqual(["API tokens 2"]));
    expect(panel("tokens")).toBeTruthy();
    expect((document.querySelector("[data-hidden-tabs]") as HTMLElement).textContent).toBe(hiddenTabsLine(["Attestation registry", "Configuration"]) + " " + hiddenKeysTabLine("Agent signing keys"));
    // The seat may not read the registry or the keys, so the page never
    // asks for them.
    expect(listAttestationHashes).not.toHaveBeenCalled();
    expect(listSigningKeys).not.toHaveBeenCalled();
  });

  it("opens a config-only seat on Attestation registry, and names the tokens tab", async () => {
    vi.mocked(adminAreas).mockReturnValue({ config: true });
    mount();
    await waitFor(() => expect(tabNames()).toEqual(["Attestation registry 3", "Configuration " + READ_ONLY]));
    expect(panel("registry")).toBeTruthy();
    expect((document.querySelector("[data-hidden-tabs]") as HTMLElement).textContent).toBe(hiddenTokensTabLine("API tokens") + " " + hiddenKeysTabLine("Agent signing keys"));
    expect(listApiTokens).not.toHaveBeenCalled();
  });

  it("leaves a count off the label when its read fails, and keeps the page", async () => {
    vi.mocked(listAttestationHashes).mockRejectedValue(new ApiError("HTTP 503", 503));
    mount();
    await waitFor(() => expect(tabNames()).toEqual(["API tokens 2", "Agent signing keys 1", "Attestation registry", "Configuration " + READ_ONLY]));
    expect(panel("tokens")).toBeTruthy();
  });

  it("counts the live client assertion keys, a staged one included", async () => {
    vi.mocked(listSigningKeys).mockResolvedValue([keys[0], { ...keys[0], kid: "3a9e41c2-58d0-4f7b-b1e6-7c2d90a4f513", status: "staged" }]);
    mount("keys");
    expect(await screen.findByRole("tab", { name: /^Agent signing keys 2/ })).toBeTruthy();
    expect(panel("keys")).toBeTruthy();
  });
});
