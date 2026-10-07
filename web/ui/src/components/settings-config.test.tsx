import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConfigTab } from "./settings-config";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ConfigAnswer, type RoleRow, getConfig, listRoles } from "@/lib/api";
import {
  CAPTURE_SUB,
  CONFIG_LEDE,
  CONFIG_WHY,
  CONSOLE_ACCESS_LINE,
  CONSOLE_ACCESS_TITLE,
  LINE_COPIED,
  RELAXED_BADGE,
  SECTIONS,
  configRows,
  consoleAreasWords,
  copyLabel,
  fileLine,
  openRole,
  setWords,
  whereWords,
} from "@/lib/config-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { holders } from "@/lib/words";

// The eval stack's own configuration: plain http, the floor at none, the
// apps watcher on, one policy set capturing verbatim.
const answer: ConfigAnswer = {
  profile: "enterprise",
  public_url: "http://localhost:8420",
  tls: false,
  store_driver: "postgres",
  events: { embedded: true },
  oidc: { external_issuer: "http://localhost:8080/realms/straza", jit_provision: false },
  governance: { min_attestation: "none", offline_grace_ttl_seconds: 300, local_tool_default: "deny", audit_backpressure: "block" },
  approval: { gateway_hold_seconds: 120 },
  apps: { gitops_dir_enabled: true, upstream_timeout_seconds: 30 },
  capture: { policy_sets: 1, mode: "verbatim", retention_hours: 720, body_store: "inline" },
};

const roles: RoleRow[] = [
  { id: "r-admin", name: "straza-admin", kind: "straza", holder_count: 2, areas: ["full"] },
  { id: "r-auditor", name: "auditor", kind: "straza", holder_count: 0, areas: ["audit:read", "sessions:read", "transcripts:read"] },
  { id: "r-dev", name: "dev", kind: "business", holder_count: 2 },
];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getConfig: vi.fn(),
  listRoles: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const rows = configRows(answer);
const rowOf = (id: string) => rows.find((r) => r.id === id) as ReturnType<typeof configRows>[number];
const mount = (draftRequest = 0) => render(<TooltipProvider><ConfigTab draftRequest={draftRequest} /></TooltipProvider>);
const panel = (id: string) => document.querySelector('[data-config-row="' + id + '"]') as HTMLElement;
const sections = () => Array.from(document.querySelectorAll("[data-config-section] h2")).map((h) => h.textContent);
const name = (id: string) => within(panel(id)).getByRole("button", { name: rowOf(id).name });

describe("the Configuration tab", () => {
  beforeEach(() => {
    vi.mocked(getConfig).mockResolvedValue(answer);
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(navigate).mockClear();
    vi.mocked(notify.ok).mockClear();
    vi.mocked(notify.failed).mockClear();
    window.history.replaceState(null, "", window.location.pathname);
  });

  it("lays the rows out in sections, with the key and the variable under each name", async () => {
    mount();
    await screen.findByText(CONFIG_LEDE);
    expect(sections()).toEqual([...SECTIONS, CONSOLE_ACCESS_TITLE]);
    expect(within(document.querySelector('[data-config-section="Recording"]') as HTMLElement).getByText(CAPTURE_SUB)).toBeTruthy();

    const floor = panel("floor");
    expect(within(floor).getByText("governance.minAttestation · STRAZA_MIN_ATTESTATION")).toBeTruthy();
    expect((document.querySelector('[data-config-value="floor"]') as HTMLElement).textContent).toBe("none" + RELAXED_BADGE);
    // Plain http with no TLS in front is the other relaxed row; a strict
    // row carries no badge.
    expect((document.querySelector('[data-config-value="tls"]') as HTMLElement).textContent).toBe("off" + RELAXED_BADGE);
    expect((document.querySelector('[data-config-value="local"]') as HTMLElement).textContent).toBe("deny");
    // 120 seconds reads as the words module says a duration, not as the
    // seconds the server sent.
    expect((document.querySelector('[data-config-value="hold"]') as HTMLElement).textContent).toBe("2 min");
    // A row with no key says where the value comes from instead.
    expect(within(panel("sets")).getByText(whereWords(rowOf("sets")))).toBeTruthy();
  });

  it("opens a row's sentence on a click on the name and closes it on the next", async () => {
    mount();
    await screen.findByText(CONFIG_LEDE);
    const sentence = rowOf("tls").help + " " + setWords(rowOf("tls"));
    expect(document.querySelector('[data-row-help="tls"]')).toBeNull();
    expect(name("tls").getAttribute("aria-expanded")).toBe("false");
    // The hover path stays: the key line keeps its title.
    expect(within(panel("tls")).getByText(whereWords(rowOf("tls"))).getAttribute("title")).toBe(whereWords(rowOf("tls")));

    await userEvent.click(name("tls"));
    expect((document.querySelector('[data-row-help="tls"]') as HTMLElement).textContent).toContain(sentence);
    expect(name("tls").getAttribute("aria-expanded")).toBe("true");

    await userEvent.click(name("tls"));
    expect(document.querySelector('[data-row-help="tls"]')).toBeNull();
  });

  it("says what the page is never sent instead of printing the word redacted", async () => {
    mount();
    await screen.findByText(CONFIG_LEDE);
    expect(document.body.textContent).not.toContain("redacted");
    await userEvent.hover(screen.getByRole("button", { name: "Help: Configuration" }));
    expect((await screen.findAllByText(CONFIG_WHY)).length).toBeGreaterThan(0);
  });

  it("opens the row the address names and scrolls to it", async () => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => undefined);
    window.history.replaceState(null, "", window.location.pathname + "#floor");
    mount();
    await screen.findByText(CONFIG_LEDE);
    expect(name("floor").getAttribute("aria-expanded")).toBe("true");
    expect((document.querySelector('[data-row-help="floor"]') as HTMLElement).textContent).toContain("sudo straza install --managed");
    await waitFor(() => expect(scroll).toHaveBeenCalled());
    scroll.mockRestore();
  });

  it("copies the config file line of a row that has a key, and offers none where there is no key", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mount();
    await screen.findByText(CONFIG_LEDE);
    const line = fileLine(rowOf("floor"));
    expect(line).toBe("governance:\n  minAttestation: \"none\"");
    await userEvent.click(within(panel("floor")).getByRole("button", { name: copyLabel(rowOf("floor").key) }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(line));
    expect(notify.ok).toHaveBeenCalledWith(LINE_COPIED);
    expect(within(panel("mode")).queryByRole("button", { name: /^Copy the line/ })).toBeNull();
  });

  it("closes the tab with the straza roles the config maps areas to, each a door", async () => {
    mount();
    await screen.findByText(CONSOLE_ACCESS_LINE);
    const listed = Array.from(document.querySelectorAll("[data-console-role]")).map((r) => r.getAttribute("data-console-role"));
    expect(listed).toEqual(["straza-admin", "auditor"]);
    const auditor = document.querySelector('[data-console-role="auditor"]') as HTMLElement;
    expect(within(auditor).getByText(consoleAreasWords(roles[1].areas))).toBeTruthy();
    expect(within(auditor).getByText(holders(0))).toBeTruthy();
    expect(within(document.querySelector('[data-console-role="straza-admin"]') as HTMLElement).getByText("every area")).toBeTruthy();
    await userEvent.click(within(auditor).getByRole("button", { name: openRole("auditor") }));
    expect(navigate).toHaveBeenCalledWith("roles", ["r-auditor"]);
  });

  it("hides the Console access section from a seat that may not read the roles", async () => {
    vi.mocked(listRoles).mockRejectedValue(new ApiError("forbidden", 403));
    mount();
    await screen.findByText(CONFIG_LEDE);
    await waitFor(() => expect(document.querySelector("[data-console-role]")).toBeNull());
    expect(screen.queryByText(CONSOLE_ACCESS_LINE)).toBeNull();
    // The settings themselves still read.
    expect(panel("floor")).toBeTruthy();
  });

  it("says what failed when the configuration cannot be read", async () => {
    vi.mocked(getConfig).mockRejectedValue(new ApiError("HTTP 503", 503));
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("The configuration could not be read");
    expect(block.textContent).toContain("Reload to try again");
  });

  it("renders a server value as text, never as markup", async () => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, public_url: "http://<b>evil</b>" });
    mount();
    await screen.findByText(CONFIG_LEDE);
    const cell = document.querySelector('[data-config-value="public_url"]') as HTMLElement;
    expect(cell.querySelector("b")).toBeNull();
    expect(cell.textContent).toBe("http://<b>evil</b>");
  });
});
