import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConfigTab } from "./settings-config";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ConfigAnswer, type RoleRow, getConfig, listRoles } from "@/lib/api";
import {
  CONFIG_BANNER,
  CONFIG_WHY,
  CONSOLE_ACCESS_LINE,
  CONSOLE_ACCESS_TITLE,
  DRAFT_TITLE,
  FACT,
  FROM_SETS,
  LINE_COPIED,
  NO_ENV,
  NOT_SET,
  ON_PUBLISH,
  ON_RESTART,
  OPEN_REGISTRY,
  RELAXED_BADGE,
  RELAXED_SENTENCE,
  SECTIONS,
  VALUE_UNREPORTED,
  configRows,
  consoleAreasWords,
  copyLabel,
  envCopyLabel,
  fileLine,
  openRole,
  postureBadge,
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
  admin: { second_person: false },
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
const mount = (row?: string) => render(<TooltipProvider><ConfigTab row={row} /></TooltipProvider>);
const panel = (id: string) => document.querySelector('[data-config-row="' + id + '"]') as HTMLElement;
const sections = () => Array.from(document.querySelectorAll("[data-config-section] h2")).map((h) => h.textContent);
const name = (id: string) => within(panel(id)).getByRole("button", { name: new RegExp("^" + rowOf(id).name) });
const value = (id: string) => (document.querySelector('[data-config-value="' + id + '"]') as HTMLElement).textContent;
const facts = (id: string) => document.querySelector('[data-row-facts="' + id + '"]') as HTMLElement | null;
const relaxedRows = () => Array.from(document.querySelectorAll("[data-relaxed-row]")).map((r) => r.getAttribute("data-relaxed-row"));
const clipboard = () => {
  const writeText = vi.fn(() => Promise.resolve());
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  return writeText;
};

describe("the Configuration tab", () => {
  beforeEach(() => {
    vi.mocked(getConfig).mockResolvedValue(answer);
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(navigate).mockClear();
    vi.mocked(notify.ok).mockClear();
    vi.mocked(notify.failed).mockClear();
  });

  it("lays the rows out in six sections, a closed row a name and a value with no key", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    expect(sections()).toEqual([...SECTIONS.filter((s) => s !== "Entra"), CONSOLE_ACCESS_TITLE]);
    expect(SECTIONS).toEqual(["Deployment", "Sign-in", "Entra", "Governance", "Approvals and publishing", "MCP servers", "Recording"]);

    // The relaxed chip stands before the value, and a strict row has none.
    expect(value("floor")).toBe("none");
    expect(within(panel("floor")).getByText(RELAXED_BADGE).getAttribute("data-tone")).toBe("warn");
    expect(name("floor").textContent).toBe(rowOf("floor").name + RELAXED_BADGE + "none");
    expect(value("local")).toBe("deny");
    expect(within(panel("local")).queryByText(RELAXED_BADGE)).toBeNull();
    expect(value("hold")).toBe("2 min");
    // No key and no variable shows until a row opens.
    expect(document.body.textContent).not.toContain("governance.minAttestation");
    expect(document.body.textContent).not.toContain("STRAZA_MIN_ATTESTATION");
  });

  it("says once that the tab is read-only, and copies a config change from the banner", async () => {
    mount();
    const banner = await screen.findByRole("note");
    expect(banner.textContent).toContain(CONFIG_BANNER);
    const copy = within(banner).getByRole("button", { name: DRAFT_TITLE });
    expect(copy.getAttribute("data-variant")).toBe("outline");
    expect(screen.queryByRole("button", { name: "Write a config change" })).toBeNull();
    await userEvent.click(copy);
    expect(within(await screen.findByRole("dialog")).getByText(DRAFT_TITLE)).toBeTruthy();
  });

  it("lists the relaxed settings first, each with what it means, and opens one below on a click", async () => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => undefined);
    mount();
    await screen.findByText(CONFIG_BANNER);
    expect(screen.getByRole("heading", { name: postureBadge(2) }).textContent).toBe("2 relaxed");
    expect(relaxedRows()).toEqual(["tls", "floor"]);
    const floor = document.querySelector('[data-relaxed-row="floor"]') as HTMLElement;
    expect(floor.textContent).toContain(rowOf("floor").name);
    expect(floor.textContent).toContain(RELAXED_SENTENCE.floorNone);
    expect((document.querySelector('[data-relaxed-row="tls"]') as HTMLElement).textContent).toContain(RELAXED_SENTENCE.tls);

    expect(name("floor").getAttribute("aria-expanded")).toBe("false");
    await userEvent.click(floor);
    expect(name("floor").getAttribute("aria-expanded")).toBe("true");
    expect(scroll).toHaveBeenCalled();
    scroll.mockRestore();
  });

  it("counts one relaxed setting, and draws no card when none is relaxed", async () => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, tls: true, public_url: "https://straza.example" });
    const one = mount();
    await screen.findByText(CONFIG_BANNER);
    expect(screen.getByRole("heading", { name: postureBadge(1) }).textContent).toBe("1 relaxed");
    expect(relaxedRows()).toEqual(["floor"]);
    one.unmount();

    vi.mocked(getConfig).mockResolvedValue({ ...answer, tls: true, public_url: "https://straza.example", governance: { ...answer.governance, min_attestation: "managed" } });
    mount();
    await screen.findByText(CONFIG_BANNER);
    expect(document.querySelector("[data-relaxed-card]")).toBeNull();
  });

  it("opens one row at a time on a click and closes it on the next", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    expect(document.querySelector('[data-row-help="tls"]')).toBeNull();
    await userEvent.click(name("tls"));
    expect((document.querySelector('[data-row-help="tls"]') as HTMLElement).textContent).toBe(rowOf("tls").help);
    expect(name("tls").getAttribute("aria-expanded")).toBe("true");
    await userEvent.click(name("floor"));
    expect(document.querySelector('[data-row-help="tls"]')).toBeNull();
    expect(name("floor").getAttribute("aria-expanded")).toBe("true");
    await userEvent.click(name("floor"));
    expect(document.querySelector('[data-row-help="floor"]')).toBeNull();
  });

  it("opens a row with a variable: its sentence, the file line, the environment line, when it takes effect and the registry", async () => {
    const writeText = clipboard();
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(name("floor"));
    expect((document.querySelector('[data-row-help="floor"]') as HTMLElement).textContent).toBe("The lowest proof of its install a client must give to get a session. none lets every client in. advisory asks for hashes and lets a mismatch through. managed refuses a client whose hashes are not in the registry.");
    const grid = facts("floor") as HTMLElement;
    expect(Array.from(grid.querySelectorAll("dt")).map((d) => d.textContent)).toEqual([FACT.file, FACT.env, FACT.effect]);
    expect(within(grid).getByText("governance.minAttestation: none")).toBeTruthy();
    expect(within(grid).getByText("STRAZA_MIN_ATTESTATION=none")).toBeTruthy();
    expect(within(grid).getByText(ON_RESTART)).toBeTruthy();

    // The file button copies the nested lines of the config file.
    await userEvent.click(within(grid).getByRole("button", { name: copyLabel("governance.minAttestation") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(fileLine(rowOf("floor"))));
    expect(fileLine(rowOf("floor"))).toBe("governance:\n  minAttestation: \"none\"");
    expect(notify.ok).toHaveBeenCalledWith(LINE_COPIED);
    await userEvent.click(within(grid).getByRole("button", { name: envCopyLabel("STRAZA_MIN_ATTESTATION") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("STRAZA_MIN_ATTESTATION=none"));
    expect(notify.ok).toHaveBeenLastCalledWith(LINE_COPIED);

    await userEvent.click(within(panel("floor")).getByRole("link", { name: OPEN_REGISTRY }));
    expect(navigate).toHaveBeenCalledWith("settings", ["registry"]);
  });

  it("says a setting has no environment variable and offers no button for one", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(name("local"));
    const grid = facts("local") as HTMLElement;
    expect(within(grid).getByText("governance.localToolDefault: deny")).toBeTruthy();
    expect(within(grid).getByText(NO_ENV)).toBeTruthy();
    expect(within(grid).queryByRole("button", { name: /^Copy the environment line/ })).toBeNull();
    expect(within(panel("local")).queryByRole("link", { name: OPEN_REGISTRY })).toBeNull();
  });

  it("shows the key alone and says so for a setting the file does not set", async () => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, governance: { ...answer.governance, local_tool_default: "" } });
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(name("local"));
    const grid = facts("local") as HTMLElement;
    expect(within(grid).getByText("governance.localToolDefault")).toBeTruthy();
    expect(within(grid).getByText(NOT_SET)).toBeTruthy();
  });

  it("shows the two keys and the two variables of TLS, each with its own copy button", async () => {
    const writeText = clipboard();
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(name("tls"));
    const grid = facts("tls") as HTMLElement;
    for (const key of ["server.tls.certFile", "server.tls.keyFile"]) expect(within(grid).getByRole("button", { name: copyLabel(key) })).toBeTruthy();
    for (const v of ["STRAZA_TLS_CERT_FILE", "STRAZA_TLS_KEY_FILE"]) expect(within(grid).getByRole("button", { name: envCopyLabel(v) })).toBeTruthy();
    // The answer never carries a file path, so a line is the key alone.
    expect(within(grid).getByText("server.tls.keyFile")).toBeTruthy();
    await userEvent.click(within(grid).getByRole("button", { name: envCopyLabel("STRAZA_TLS_KEY_FILE") }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("STRAZA_TLS_KEY_FILE"));
    // TLS is off in this answer, so neither file key is set.
    expect(within(grid).getByText(NOT_SET)).toBeTruthy();
  });

  it("does not call the TLS file keys unset while TLS is on", async () => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, tls: true, public_url: "https://straza.example" });
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(name("tls"));
    const grid = facts("tls") as HTMLElement;
    expect(within(grid).getByText("server.tls.certFile")).toBeTruthy();
    expect(within(grid).queryByText(NOT_SET)).toBeNull();
  });

  it.each(["sets", "mode"])("says the %s row comes from the live policy sets and offers no copy", async (id) => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(name(id));
    const grid = facts(id) as HTMLElement;
    expect(Array.from(grid.querySelectorAll("dt")).map((d) => d.textContent)).toEqual([FACT.from, FACT.effect]);
    expect(within(grid).getByText(FROM_SETS)).toBeTruthy();
    expect(within(grid).getByText(ON_PUBLISH)).toBeTruthy();
    expect(within(panel(id)).queryByRole("button", { name: /^Copy/ })).toBeNull();
  });

  it("says why a value is missing and shows no facts for it", async () => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, governance: undefined });
    mount();
    await screen.findByText(CONFIG_BANNER);
    expect(value("floor")).toBe("Unavailable");
    await userEvent.click(name("floor"));
    expect((document.querySelector('[data-row-help="floor"]') as HTMLElement).textContent).toContain(VALUE_UNREPORTED);
    expect(facts("floor")).toBeNull();
  });

  it.each<[string, ConfigAnswer["admin"], string]>([
    ["on", { second_person: true }, "on"],
    ["off", { second_person: false }, "off"],
    ["missing", undefined, "Unavailable"],
  ])("shows Second person for publishing %s, last in its section and never relaxed", async (_name, admin, word) => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, admin });
    mount();
    await screen.findByText(CONFIG_BANNER);
    const section = document.querySelector('[data-config-section="Approvals and publishing"]') as HTMLElement;
    expect(Array.from(section.querySelectorAll("[data-config-row]")).map((r) => r.getAttribute("data-config-row"))).toEqual(["hold", "ownDecisions", "secondPerson"]);
    expect(value("secondPerson")).toBe(word);
    expect(within(panel("secondPerson")).queryByText(RELAXED_BADGE)).toBeNull();
    expect(relaxedRows()).not.toContain("secondPerson");
    await userEvent.click(name("secondPerson"));
    expect((document.querySelector('[data-row-help="secondPerson"]') as HTMLElement).textContent).toContain("When it is on, a change that widens access cannot be published by the person who wrote it, by the sponsor of the agent that wrote it, or by whoever minted the API token that wrote it. Another administrator publishes it.");
    if (admin) {
      expect(within(facts("secondPerson") as HTMLElement).getByText("admin.secondPerson: " + String(admin.second_person))).toBeTruthy();
      expect(within(facts("secondPerson") as HTMLElement).getByText(NO_ENV)).toBeTruthy();
    } else {
      expect(facts("secondPerson")).toBeNull();
    }
  });

  it("names the tab by its key in the help, whatever the tab order", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.hover(screen.getByRole("button", { name: "Help: Configuration" }));
    expect((await screen.findAllByText(CONFIG_WHY)).length).toBeGreaterThan(0);
    expect(document.body.textContent).not.toContain("redacted");
  });

  it("opens the row the address names and scrolls to it", async () => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView").mockImplementation(() => undefined);
    mount("floor");
    await screen.findByText(CONFIG_BANNER);
    expect(name("floor").getAttribute("aria-expanded")).toBe("true");
    expect(facts("floor")).toBeTruthy();
    await waitFor(() => expect(scroll).toHaveBeenCalled());
    scroll.mockRestore();
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
    await screen.findByText(CONFIG_BANNER);
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
    await screen.findByText(CONFIG_BANNER);
    const cell = document.querySelector('[data-config-value="public_url"]') as HTMLElement;
    expect(cell.querySelector("b")).toBeNull();
    expect(cell.textContent).toBe("http://<b>evil</b>");
  });
});

describe("the Entra section of the Configuration tab", () => {
  const entra = {
    tenant: "8a3c5e1f-2b4d-4f6a-9c8e-0d1f2a3b4c5d", client_id: "5b7d9f1a-3c5e-4a7b-8d9f-1a2b3c4d5e6f", read: ["people", "agentIdentities"], role_groups: true,
    group_name_prefix: "Straza: ", delta_interval_seconds: 60, reconcile_interval_seconds: 3600, removal_guard_percent: 50, removal_guard_min_members: 5,
  };
  const ids = ["entraTenant", "entraClient", "entraRead", "entraGroups", "entraPrefix", "entraDelta", "entraFull", "entraGuard"];

  beforeEach(() => {
    vi.mocked(getConfig).mockResolvedValue({ ...answer, entra });
    vi.mocked(listRoles).mockResolvedValue(roles);
  });

  it("reads the section out after Sign-in, every value in words", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    expect(sections()).toEqual([...SECTIONS, CONSOLE_ACCESS_TITLE]);
    const section = document.querySelector('[data-config-section="Entra"]') as HTMLElement;
    expect(Array.from(section.querySelectorAll("[data-config-row]")).map((r) => r.getAttribute("data-config-row"))).toEqual(ids);
    expect(ids.map((id) => [within(panel(id)).getByRole("button").textContent?.replace(value(id) || "", ""), value(id)])).toEqual([
      ["Tenant", entra.tenant],
      ["Service app", entra.client_id],
      ["Kinds read", "people, agent identities"],
      ["Role groups", "on"],
      ["Group name prefix", "\"Straza: \""],
      ["Read changes", "every minute"],
      ["Read all members", "every hour"],
      ["Removal guard", "more than half, at least 5"],
    ]);
  });

  it("opens a row with its sentence, the file line, no variable and when it takes effect", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(within(panel("entraDelta")).getByRole("button"));
    expect((document.querySelector('[data-row-help="entraDelta"]') as HTMLElement).textContent).toBe("How often Straza reads what changed in the role groups. Entra itself takes 6 to 16 seconds to show a change.");
    const grid = facts("entraDelta") as HTMLElement;
    expect(within(grid).getByText("identity.entra.deltaInterval: 1m")).toBeTruthy();
    expect(within(grid).getByText(NO_ENV)).toBeTruthy();
    expect(within(grid).getByText(ON_RESTART)).toBeTruthy();
  });

  it("shows a kind it does not know as the config holds it, and other intervals and guards in words", () => {
    const r = configRows({ ...answer, entra: { ...entra, read: ["people", "robots"], delta_interval_seconds: 90, reconcile_interval_seconds: 7200, removal_guard_percent: 30, removal_guard_min_members: 2, role_groups: false } });
    const v = (id: string) => (r.find((x) => x.id === id) as ReturnType<typeof configRows>[number]).value;
    expect([v("entraRead"), v("entraDelta"), v("entraFull"), v("entraGuard"), v("entraGroups")]).toEqual(["people, robots", "every 90 seconds", "every 2 hours", "more than 30%, at least 2", "off"]);
    expect(configRows({ ...answer, entra: { ...entra, read: [] } }).find((x) => x.id === "entraRead")?.value).toBe("none");
  });

  it("leaves the section out of Prepare a change", async () => {
    mount();
    await screen.findByText(CONFIG_BANNER);
    await userEvent.click(screen.getByRole("button", { name: new RegExp(DRAFT_TITLE) }));
    const sheet = await waitFor(() => document.querySelector("[data-draft-sheet]") as HTMLElement);
    await userEvent.click(within(sheet).getByRole("combobox"));
    expect(screen.queryByRole("option", { name: "Tenant" })).toBeNull();
    expect(screen.getByRole("option", { name: rowOf("floor").name })).toBeTruthy();
  });
});
