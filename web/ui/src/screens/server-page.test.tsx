import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServerPage } from "./server-page";
import type { ChangeSheetProps } from "@/components/server-change-sheet";
import type { DraftSaveOptions } from "@/components/use-draft-save";
import type { SaveItem } from "@/lib/draft-save";
import type { DraftPublished } from "@/lib/api";
import { TooltipProvider } from "@/components/ui/tooltip";
import { appLogs, catalogPreview, disableApp, enableApp, getConfig, listApps, listAudit, listBindings, listChanges, listRoles, listSecrets, listTools, listUsers, recheckApp, removeApp, removeSecret, setSecret } from "@/lib/api";
import { navigate } from "@/lib/router";
import { adminAreas } from "@/lib/session";
import { notify } from "@/lib/notify";
import { NO_DESCRIPTION } from "@/lib/server-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApps: vi.fn(), listTools: vi.fn(), listBindings: vi.fn(), listAudit: vi.fn(), listChanges: vi.fn(), listSecrets: vi.fn(), listRoles: vi.fn(), listUsers: vi.fn(),
  catalogPreview: vi.fn(), recheckApp: vi.fn(), enableApp: vi.fn(), disableApp: vi.fn(), removeApp: vi.fn(), setSecret: vi.fn(), removeSecret: vi.fn(), appLogs: vi.fn(),
  getConfig: vi.fn(),
}));
// The session's standing tells a server outside the list from one that
// does not exist: null is a full grant.
vi.mock("@/lib/session", () => ({ adminAreas: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
// The Change sheet is its own lazy module; the page suite stands it in with
// the props it was handed, so it tests the opening, closing and reload alone.
vi.mock("@/components/server-change-sheet", () => ({
  ChangeSheet: ({ app, which, tools, upstreamTimeout, onClose, onSaved }: ChangeSheetProps) => (
    <div data-change-sheet={which}>
      {"Change " + which + " of " + app.name + ", tools " + tools.join(",") + ", timeout " + upstreamTimeout}
      <button type="button" onClick={onClose}>Close the sheet</button>
      <button type="button" onClick={() => onSaved()}>Save the sheet</button>
    </div>
  ),
}));

// The origin line reads the drafts list and has its own suite.
vi.mock("@/components/origin-line", () => ({ OriginLine: ({ object, file }: { object: string; file?: string }) => <div data-origin-stub={object} data-file={file || ""} /> }));
// Remove server runs the shared saves, whose own suite pins the flow; here
// they stand in so this suite pins the removal the page hands them.
const save = vi.hoisted(() => ({
  opts: null as DraftSaveOptions | null,
  draft: vi.fn<(items: SaveItem[]) => Promise<void>>(), publish: vi.fn<(items: SaveItem[]) => Promise<void>>(),
}));
vi.mock("@/components/use-draft-save", async (orig) => ({
  ...(await orig<typeof import("@/components/use-draft-save")>()),
  useDraftSave: (o: DraftSaveOptions) => { save.opts = o; return { busy: null, note: null, saveDraft: save.draft, saveAndPublish: save.publish, dialog: null }; },
}));

// jsdom has no pointer capture; the Radix Select trigger asks for it.
Element.prototype.hasPointerCapture ??= () => false;
Element.prototype.setPointerCapture ??= () => {};
Element.prototype.releasePointerCapture ??= () => {};

const NOW = Date.now();
const iso = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();
const apiError = (status: number, message: string, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

const manifestOf = (name: string, url: string, credential: unknown, description?: string) => ({
  apiVersion: "straza.dev/v1beta1", kind: "App", metadata: { name, ...(description ? { description } : {}) }, server: { name, version: "0" },
  straza: { runtime: { kind: "remote", remote: { url } }, ...(credential ? { credential } : {}), exposure: { tools: ["*"] } },
});
const callerApp = (id: string, name: string, credential: unknown) => ({
  id, name, runtime: "remote", version: "0", status: "running", detail: "", tools: ["a", "b"], reached_by: [], last_probe_at: iso(3), url: "https://" + name + ".example/mcp",
  manifest: manifestOf(name, "https://" + name + ".example/mcp", credential),
});

const scout = {
  id: "app-9", name: "scout-tools", runtime: "remote", version: "0", status: "running", detail: "", tools: ["echo", "get-sum", "get-env"], offered: ["echo", "get-sum", "get-env", "get-time"], reached_by: ["scout-role"],
  last_probe_at: iso(12), status_since: iso(3600), url: "http://demo-tools:3001/mcp",
  manifest: manifestOf("scout-tools", "http://demo-tools:3001/mcp", { kind: "static", inject: { as: "header", name: "X-Demo-Key", template: "{{secret}}" } }, "Docs walk copy of the demo MCP server, with a header credential"),
};
const apps = [
  scout,
  { id: "app-2", name: "legacy-app", runtime: "command", version: "1.0", status: "running", detail: "", tools: ["ping"], reached_by: [], last_probe_at: iso(3) },
  { id: "app-3", name: "flaky", runtime: "remote", status: "degraded", detail: "dial tcp: connection refused", tools: [], reached_by: ["dev"], last_probe_at: iso(20), status_since: iso(720), url: "http://flaky:3001/mcp" },
  { id: "app-4", name: "sleeper", runtime: "command", status: "stopped", detail: "", tools: ["a"], reached_by: [], paused: true, last_probe_at: iso(30), status_since: iso(3600 * 18) },
  callerApp("app-5", "github", { kind: "token", agents: "sponsor", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } }),
  callerApp("app-6", "github-shared", { kind: "token", agents: "shared", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } }),
  callerApp("app-7", "midpoint", { kind: "oauth", agents: "own", oauth: { provider: "keycloak" }, inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } }),
  callerApp("app-8", "open-tools", { kind: "none" }),
  { ...callerApp("app-10", "midpoint-file", { kind: "static", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } }), file: "/etc/straza/apps/midpoint-file.app.yaml" },
];
const tools = [
  { id: "t1", app: "scout-tools", app_id: "app-9", name: "echo", description: "Echoes back the input string" },
  { id: "t2", app: "scout-tools", app_id: "app-9", name: "get-sum", description: "Returns the sum of two numbers" },
  { id: "t3", app: "scout-tools", app_id: "app-9", name: "get-env", description: "Returns all environment variables" },
];
const bindings = [{ id: "b9", app: "scout-tools", role: "scout-role", tools: ["echo", "get-sum"] }, { id: "b3", app: "flaky", role: "dev" }];
const ce = (type: string, secondsAgo: number, data: Record<string, unknown>) => JSON.stringify({ specversion: "1.0", type, time: iso(secondsAgo), data });
// data.user is an id; the list row carries the username the server resolved.
const mcpRows = [
  { seq: 13, username: "sam", ce: ce("straza.audit.mcp", 30, { app: "", user: "u-0193-sam", toolName: "scout-tools-old__echo", effect: "deny" }) },
  { seq: 12, username: "sam", ce: ce("straza.audit.mcp", 60, { app: "scout-tools-old", user: "u-0193-sam", toolName: "echo", effect: "allow" }) },
  { seq: 11, username: "joe", ce: ce("straza.audit.mcp", 300, { app: "scout-tools", user: "u-0192-joe", toolName: "get-sum", effect: "allow" }) },
];
let secrets: Record<string, unknown[]>;

const head = () => within(document.querySelector("[data-page-head]") as HTMLElement);
const credCard = () => screen.getByRole("region", { name: "Server authentication" });
// credOf waits for the Credential card to read the stored rows, then
// returns it; dd reads one of its rows by label, buttons included.
const credOf = async (text: string) => waitFor(() => { const el = credCard(); expect(el.textContent).toContain(text); return el; });
const dd = (el: HTMLElement, label: string) => [...el.querySelectorAll("dt")].find((d) => d.textContent === label)?.nextElementSibling?.textContent || "";
const headButtons = () => head().getAllByRole("button").map((b) => b.textContent?.trim());
const banner = () => document.querySelector("[data-server-status]") as HTMLElement;
// policy reads the Policy cell of one Tools row: each chip as its tone and
// its word, and each muted line under a chip.
const policy = (row: HTMLElement) => {
  const cell = row.querySelectorAll("td")[3];
  return {
    chips: [...cell.querySelectorAll("[data-tone]")].map((c) => c.getAttribute("data-tone") + " " + c.textContent),
    lines: [...cell.querySelectorAll("[data-policy-sets]")].map((l) => l.textContent),
  };
};
const dialog = async () => screen.findByRole("alertdialog");
const mount = (id: string, tab?: string) => render(<TooltipProvider><ServerPage id={id} tab={tab} /></TooltipProvider>);
const opened = async (id: string, tab?: string) => { mount(id, tab); await screen.findByRole("heading", { level: 1 }); await waitFor(() => expect(banner()).not.toBeNull()); };

describe("the MCP server page", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    save.opts = null;
    save.draft.mockResolvedValue(undefined);
    save.publish.mockResolvedValue(undefined);
    secrets = {
      "app-9": [{ id: "c1", scope: "app", role: "", kind: "static", fingerprint: "a1c4", set_at: "2026-09-04T07:41:00Z" }],
      "app-6": [{ id: "c6", scope: "app", role: "", kind: "static", fingerprint: "7c1e", set_at: "2026-09-10T09:12:00Z" }],
    };
    vi.mocked(listApps).mockImplementation(async () => apps as never);
    vi.mocked(listTools).mockResolvedValue(tools);
    vi.mocked(listBindings).mockResolvedValue(bindings);
    vi.mocked(listAudit).mockImplementation(async () => mcpRows);
    vi.mocked(listChanges).mockResolvedValue({ changes: [] });
    vi.mocked(listSecrets).mockImplementation(async (id) => (secrets[id] || []) as never);
    vi.mocked(listRoles).mockResolvedValue([
      { id: "r9", name: "scout-role", kind: "application" }, { id: "r1", name: "dev-tools", kind: "application" }, { id: "r2", name: "dev", kind: "business" },
    ]);
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [
      { app: "scout-tools", tool: "echo", status: "visible", reason: "" },
      { app: "scout-tools", tool: "get-sum", status: "approve_gated", setName: "scout-role-access", reason: "" },
      { app: "scout-tools", tool: "get-env", status: "matcher_miss", reason: "" },
    ] });
    vi.mocked(setSecret).mockResolvedValue({ id: "c2", scope: "app", role: "", fingerprint: "b7e2" });
    vi.mocked(removeSecret).mockResolvedValue({});
    vi.mocked(removeApp).mockResolvedValue({ id: "app-9", status: "deleted" });
    vi.mocked(getConfig).mockResolvedValue({ apps: { upstream_timeout_seconds: 45 } });
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(adminAreas).mockReturnValue(null);
    vi.mocked(appLogs).mockReset();
  });

  it("opens with the name, one sentence and the actions in order, Recheck as the primary", async () => {
    await opened("app-9");
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("scout-tools");
    expect(head().getByText("Docs walk copy of the demo MCP server, with a header credential")).toBeTruthy();
    expect(headButtons()).toEqual(["Remove server", "Pause", "Test a call", "Recheck"]);
    expect(head().getByRole("button", { name: "Recheck" }).getAttribute("data-variant")).toBe("default");
    expect(head().getByRole("button", { name: "Remove server" }).className).toContain("border-danger");
    expect(head().getByRole("button", { name: "Pause" }).getAttribute("data-variant")).toBe("outline");
  });

  it.each([
    { id: "app-8", why: "a manifest with no description" },
    { id: "app-2", why: "a row with no manifest" },
  ])("says the manifest has no description for $why", async ({ id }) => {
    await opened(id);
    expect(head().getByText(NO_DESCRIPTION)).toBeTruthy();
  });

  it("opens on Overview, with the tabs in order and the tab in the address", async () => {
    await opened("app-9");
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Overview", "Tools 3", "Server roles 0", "Activity", "Manifest"]);
    expect(screen.getByRole("tab", { name: "Overview" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.queryByText("/servers/app-9/overview")).toBeNull();
    expect(Array.from(document.querySelectorAll("[data-card]")).map((r) => document.getElementById(r.getAttribute("aria-labelledby") || "")?.textContent)).toEqual(["Connection", "Server authentication", "Tools and limits", "Server administration"]);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("lands an old Credential address on Overview with a replace", async () => {
    await opened("app-9", "credential");
    expect(navigate).toHaveBeenCalledWith("servers", ["app-9", "overview"], true);
    expect(screen.getByRole("tab", { name: "Overview" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.queryByRole("tab", { name: "Server authentication" })).toBeNull();
    await credOf("fingerprint");
  });

  it("opens the Change sheet of the card clicked, closes it, and reloads after a save", async () => {
    await opened("app-9");
    await credOf("fingerprint");
    expect(document.querySelector("[data-change-sheet]")).toBeNull();
    await userEvent.click(within(credCard()).getByRole("button", { name: "Change the credential" }));
    const sheet = await waitFor(() => { const el = document.querySelector("[data-change-sheet]") as HTMLElement; expect(el).not.toBeNull(); return el; });
    expect(sheet.getAttribute("data-change-sheet")).toBe("credential");
    expect(sheet.textContent).toContain("Change credential of scout-tools, tools echo,get-sum,get-env, timeout 45");
    await userEvent.click(screen.getByRole("button", { name: "Close the sheet" }));
    expect(document.querySelector("[data-change-sheet]")).toBeNull();
    expect(listApps).toHaveBeenCalledTimes(1);
    await userEvent.click(within(screen.getByRole("region", { name: "Tools and limits" })).getByRole("button", { name: "Change the settings" }));
    await userEvent.click(await screen.findByRole("button", { name: "Save the sheet" }));
    expect(document.querySelector("[data-change-sheet]")).toBeNull();
    await waitFor(() => expect(listApps).toHaveBeenCalledTimes(2));
  });

  it("hands the sheet no timeout and names none when the config cannot be read", async () => {
    vi.mocked(getConfig).mockRejectedValue(apiError(403, "requires the config area"));
    await opened("app-9");
    const settings = screen.getByRole("region", { name: "Tools and limits" });
    expect(dd(settings, "Per-call timeout")).toBe("The server-wide default.");
    await userEvent.click(within(settings).getByRole("button", { name: "Change the settings" }));
    expect((await screen.findByText(/^Change settings of scout-tools/)).textContent).toContain("timeout null");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("opens the Settings sheet from the Tools tab, a server a file names included", async () => {
    await opened("app-9", "tools");
    const head = document.querySelector("[data-tools-head]") as HTMLElement;
    expect(head.textContent).toContain("3 of the 4 tools the server lists are exposed.");
    const door = within(head).getByRole("button", { name: "Change the exposed tools" });
    expect(door.getAttribute("data-variant")).toBe("outline");
    await userEvent.click(door);
    const sheet = await waitFor(() => { const el = document.querySelector("[data-change-sheet]") as HTMLElement; expect(el).not.toBeNull(); return el; });
    expect(sheet.getAttribute("data-change-sheet")).toBe("settings");
    document.body.innerHTML = "";

    await opened("app-10", "tools");
    expect(screen.getByRole("button", { name: "Change the exposed tools" })).toBeTruthy();
  });

  it("renders no Change button for a row with no stored manifest, which a sheet could not edit", async () => {
    await opened("app-2");
    await credOf("No shared secret is stored yet.");
    expect(screen.queryByRole("button", { name: /^Change/ })).toBeNull();
  });

  it("gives a server a file names its origin line, Change on every card and Remove server, and keeps its secret doors", async () => {
    await opened("app-10");
    expect(document.querySelector("[data-origin-stub]")?.getAttribute("data-file")).toBe("/etc/straza/apps/midpoint-file.app.yaml");
    expect(document.querySelector("[data-file-strip]")).toBeNull();
    await credOf("No shared secret is stored yet.");
    for (const which of ["connection", "credential", "settings"]) expect(screen.getByRole("button", { name: "Change the " + which })).toBeTruthy();
    expect(head().getByRole("button", { name: "Remove server" })).toBeTruthy();
    expect(within(credCard()).getByRole("button", { name: "Set one" })).toBeTruthy();
  });

  it("makes Enable the primary on a stopped server, drops Pause and shows the admin-paused chip", async () => {
    await opened("app-4");
    expect(headButtons()).toEqual(["Remove server", "Test a call", "Recheck", "Enable"]);
    expect(head().getByRole("button", { name: "Enable" }).getAttribute("data-variant")).toBe("default");
    expect(head().getByRole("button", { name: "Recheck" }).getAttribute("data-variant")).toBe("outline");
    const b = banner().textContent || "";
    expect(b).toContain("stopped");
    expect(b).toMatch(/Status since20\d\d-\d\d-\d\d \d\d:\d\d:\d\d UTC/);
    expect(b).toContain("Paused by administrator");
    expect(b).toContain("Last callnone recorded.");
  });

  it("words the running banner: tools, up since, checked, reached by and the last call", async () => {
    await opened("app-9");
    const b = banner().textContent || "";
    expect(b).toContain("Tools3Last checked");
    expect(b).toMatch(/Last checked1\d s ago/);
    expect(b).toContain("Role accessscout-role");
    expect(b).toMatch(/Last calljoe called get-sum 5 m ago, allowed\./);
    expect(b).not.toContain("scout-tools-old");
  });

  it("leaves a removed server's last call off a server installed under its name", async () => {
    const removed = { seq: 14, username: "alice", ce: ce("straza.audit.admin", 120, { action: "apps.remove", app: "scout-tools", roles: [] }) };
    vi.mocked(listAudit).mockImplementation(async () => [removed, ...mcpRows]);
    await opened("app-9");
    const b = (document.querySelector("[data-server-status]") as HTMLElement).textContent || "";
    expect(b).toContain("Last callnone recorded.");
    expect(listAudit).toHaveBeenCalledWith("q=scout-tools&order=desc&limit=100");
  });

  // strazad omits an empty tool list, and answers a server with no live
  // instance from its stored row, which carries no tools and no status time.
  it.each([
    ["a running server that exposes no tools", { id: "app-11", name: "quiet", runtime: "remote", status: "running", detail: "", reached_by: [], last_probe_at: iso(5), status_since: iso(600) }, "0", /^20\d\d-/],
    ["a stopped server read from its row", { id: "app-12", name: "idle", runtime: "command", status: "stopped", reached_by: [], paused: true }, "Not listed while stopped", /^Not recorded while stopped$/],
  ])("reads the omitted tool list of %s from its status", async (_, row, tools, since) => {
    vi.mocked(listApps).mockResolvedValue([...apps, row] as never);
    await opened(row.id);
    expect(dd(banner(), "Tools")).toBe(tools);
    expect(dd(banner(), row.status === "running" ? "Running since" : "Status since")).toMatch(since);
    expect(dd(banner(), "Role access")).toBe("No role has access yet");
    expect(banner().textContent).not.toContain("Unavailable");
  });

  it("names the scope that reads role access when the account may not read it", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    vi.mocked(listBindings).mockRejectedValue(apiError(403, "session lacks grant apps:read"));
    vi.mocked(listApps).mockResolvedValue(apps.map((a) => (a.id === "app-9" ? { ...a, reached_by: [] } : a)) as never);
    await opened("app-9");
    expect(dd(banner(), "Role access")).toBe("Not readable with this account: which roles have access needs the scope apps:read.");
  });

  it("words the degraded banner with the reason, for how long and since when", async () => {
    await opened("app-3");
    const b = banner().textContent || "";
    expect(b).toContain("Straza could not reach http://flaky:3001/mcp (connection refused).");
    expect(b).toMatch(/Last checked2\d s ago/);
    expect(b).toContain("Role accessdev");
    expect(b).toContain("Last callnone recorded.");
  });

  it("answers who can call each tool and how it runs", async () => {
    await opened("app-9", "tools");
    const table = document.querySelector("[data-server-tools]") as HTMLElement;
    expect(within(table).getAllByRole("columnheader").map((h) => h.textContent?.trim())).toEqual(["Tool", "What it does", "Reached by", "Policy"]);
    const row = (tool: string) => within(table).getAllByRole("row").find((r) => r.querySelector("td")?.textContent?.trim() === tool) as HTMLElement;
    await waitFor(() => expect(row("echo").querySelectorAll("td")[3].textContent).toBe("allowed"));
    expect(row("echo").textContent).toContain("Echoes back the input string");
    expect(row("echo").textContent).toContain("scout-role");
    expect(policy(row("echo"))).toEqual({ chips: ["ok allowed"], lines: [] });
    expect(policy(row("get-sum"))).toEqual({ chips: ["warn needs approval"], lines: ["scout-role-access"] });
    expect(row("get-env").textContent).toContain("no role");
    expect(row("get-env").querySelectorAll("td")[3].textContent).toBe("does not exist for any session");
    expect(policy(row("get-env")).chips).toEqual([]);
    expect(catalogPreview).toHaveBeenCalledWith("scout-role", "scout-tools");
    expect(screen.getByText('This table answers "who can call X". Nothing is reachable until a role has access to it. Policies add the gates.')).toBeTruthy();
    expect(screen.queryByText("/servers/app-9/tools")).toBeNull();
  });

  // Each case is one tool three roles reach, the catalog status each role's
  // preview answers for it with the set that decides it, and the Policy cell:
  // one chip per outcome in its trust tone, with the muted line under it. A
  // null status is a role whose preview could not be read.
  const folds: [string, Record<string, [string, string?] | null>, string[], string[]][] = [
    ["one outcome and no set", { a: ["visible"], b: ["visible"], c: ["visible"] }, ["ok allowed"], []],
    ["one outcome and one set, named once", { a: ["approve_gated", "holds"], b: ["approve_gated", "holds"], c: ["approve_gated", "holds"] }, ["warn needs approval"], ["holds"]],
    ["one outcome and a set per role", { a: ["approve_gated", "a-access"], b: ["approve_gated", "b-access"], c: ["approve_gated", "a-access"] }, ["warn needs approval"], ["a-access, b-access"]],
    ["a denial", { a: ["hidden_policy", "baseline"], b: ["hidden_policy", "baseline"], c: ["hidden_policy", "baseline"] }, ["danger denied"], ["baseline"]],
    ["outcomes that differ by role, each naming its roles", { a: ["visible"], b: ["hidden_policy", "baseline"], c: ["visible"] }, ["ok allowed", "danger denied"], ["for a, c", "baseline for b"]],
    ["a preview that could not be read", { a: ["visible"], b: null, c: ["visible"] }, ["ok allowed", "unknown unknown"], ["for a, c", "for b"]],
    ["a server that is not running, which is no trust state", { a: ["not_running"], b: ["not_running"], c: ["not_running"] }, ["plain not running"], []],
    ["an allow that names a set, which the line leaves out", { a: ["visible", "a-access"], b: ["visible"], c: ["visible"] }, ["ok allowed"], []],
  ];
  it.each(folds)("folds the reaching roles' outcomes into chips for %s", async (_case, by, chips, lines) => {
    vi.mocked(listBindings).mockResolvedValue(["a", "b", "c"].map((role) => ({ id: "b-" + role, app: "scout-tools", role, tools: ["echo"] })));
    vi.mocked(catalogPreview).mockImplementation(async (role) => {
      const e = by[role];
      if (!e) throw apiError(500, "preview failed");
      return { entries: [{ app: "scout-tools", tool: "echo", status: e[0], setName: e[1], reason: "" }] };
    });
    await opened("app-9", "tools");
    const echo = within(document.querySelector("[data-server-tools]") as HTMLElement).getAllByRole("row")[1];
    await waitFor(() => expect(policy(echo)).toEqual({ chips, lines }));
  });

  it("says when no tool is known yet and a tab click changes the address in place", async () => {
    await opened("app-3", "tools");
    expect(screen.getByText("No tool is known yet: the server reads degraded, Straza could not reach http://flaky:3001/mcp (connection refused). Recheck once it answers.")).toBeTruthy();
    await userEvent.click(screen.getByRole("tab", { name: "Activity" }));
    expect(navigate).toHaveBeenCalledWith("servers", ["app-3", "activity"], true);
  });

  it("reads the credential of a static server as labeled rows with the stored fingerprint and its two doors", async () => {
    await opened("app-9");
    const cred = await credOf("fingerprint");
    expect(dd(cred, "Type")).toBe("One shared secret staticThe server sees one identity; Straza's audit keeps the person or agent.");
    expect(dd(cred, "Sent as")).toBe("The header X-Demo-Key, carrying the secret.");
    expect(dd(cred, "Stored here")).toContain("Set 2026-09-04 07:41:00 UTC, fingerprint a1c4…. Never shown again.");
    expect(within(cred).getByRole("button", { name: "Set it again" })).toBeTruthy();
    expect(within(cred).getByRole("button", { name: "Remove" })).toBeTruthy();
    expect(dd(cred, "Per-role secrets")).toContain("None.");
    expect(dd(cred, "Per-role secrets")).toContain("A role's own secret replaces the shared one for that role's calls.");
    expect(within(cred).getByRole("button", { name: "Add one for a role" })).toBeTruthy();
    expect(cred.textContent).not.toContain("strazactl apps install");
  });

  it("offers no shared-secret door on a token server with agents sponsor", async () => {
    await opened("app-5");
    const cred = await credOf("Nothing here");
    expect(dd(cred, "Type")).toBe("Each caller's own token tokenPeople paste theirs on the Credentials tab of their self-service page.");
    expect(dd(cred, "Sent as")).toBe("The Authorization header, as Bearer and the person's token.");
    expect(dd(cred, "Stored here")).toBe("Nothing here. Each person's token is sealed under their own id.");
    expect(dd(cred, "Agents with nothing of their own")).toBe("Run on their sponsor's token once the sponsor allowed it (agents sponsor).");
    expect(dd(cred, "Per-role secrets")).toBe("Not used here. Each caller's own token is the credential.");
    expect(within(cred).queryByRole("button", { name: /Set one|Set it again|Add one for a role/ })).toBeNull();
  });

  it("opens the doors on a token server with agents shared", async () => {
    await opened("app-6");
    const cred = await credOf("7c1e");
    expect(cred.textContent).toContain("Use this server's shared secret (agents shared).");
    expect(cred.textContent).toContain("Set 2026-09-10 09:12:00 UTC, fingerprint 7c1e…. Never shown again.");
    expect(within(cred).getByRole("button", { name: "Set it again" })).toBeTruthy();
    expect(within(cred).getByRole("button", { name: "Add one for a role" })).toBeTruthy();
    expect(cred.textContent).toContain("for that role's agents.");
  });

  it("reads a sign-in server and a server with no credential", async () => {
    await opened("app-7");
    let cred = await credOf("Nothing here");
    expect(dd(cred, "Type")).toBe("Each caller's own sign-in oauthNothing of theirs is stored here.");
    expect(dd(cred, "Provider and scopes")).toBe("keycloak, no scopes named");
    expect(dd(cred, "Stored here")).toBe("Nothing here. Each person's sign-in is sealed under their own id.");
    expect(dd(cred, "Agents with nothing of their own")).toBe("Get nothing, because an agent cannot sign in through a browser (agents own).");
    expect(dd(cred, "Per-role secrets")).toBe("Not used here. Each caller's own sign-in is the credential.");
    expect(within(cred).queryByRole("button", { name: "Set one" })).toBeNull();
    document.body.innerHTML = "";
    await opened("app-8");
    cred = await credOf("Nothing.");
    expect(dd(cred, "Type")).toBe("None noneNo credential is sent to this server. Role access and policies still apply.");
    expect(dd(cred, "Sent as")).toBe("");
    expect(dd(cred, "Per-role secrets")).toBe("");
  });

  it("stores the shared secret again without ever showing the value, and asks for one when the box is empty", async () => {
    await opened("app-9");
    await userEvent.click(await screen.findByRole("button", { name: "Set it again" }));
    const input = screen.getByLabelText("secret") as HTMLInputElement;
    expect(input.type).toBe("password");
    await userEvent.click(screen.getByRole("button", { name: "Store it" }));
    expect(setSecret).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toBe("Type the secret the server expects. It is stored sealed and never shown again.");
    expect(document.activeElement).toBe(input);
    await userEvent.type(input, "new-value");
    await userEvent.click(screen.getByRole("button", { name: "Store it" }));
    await waitFor(() => expect(setSecret).toHaveBeenCalledWith("app-9", "new-value", ""));
    expect(notify.ok).toHaveBeenCalledWith("The shared secret for scout-tools is stored.");
    await waitFor(() => expect(listSecrets).toHaveBeenCalledTimes(2));
    expect(document.body.textContent).not.toContain("new-value");
    expect(screen.queryByLabelText("secret")).toBeNull();
  });

  it("stores a per-role secret for an application role only", async () => {
    await opened("app-9");
    await userEvent.click(await screen.findByRole("button", { name: "Add one for a role" }));
    await userEvent.click(screen.getByRole("button", { name: "Store it" }));
    expect(screen.getByRole("alert").textContent).toBe("Pick the application role this secret is for.");
    const trigger = screen.getByRole("combobox", { name: "role for the secret" });
    await userEvent.click(trigger);
    const options = await screen.findAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual(["scout-role", "dev-tools"]);
    await userEvent.click(screen.getByRole("option", { name: "dev-tools" }));
    await userEvent.type(await screen.findByLabelText("secret for dev-tools"), "role-value");
    await userEvent.click(screen.getByRole("button", { name: "Store it" }));
    await waitFor(() => expect(setSecret).toHaveBeenCalledWith("app-9", "role-value", "dev-tools"));
    expect(notify.ok).toHaveBeenCalledWith("The secret for dev-tools on scout-tools is stored.");
  });

  it("removes a role's secret and the shared secret through their confirms", async () => {
    secrets["app-9"].push({ id: "c2", scope: "role", role: "dev-tools", kind: "static", fingerprint: "b7e2", set_at: "2026-09-04T08:00:00Z" });
    await opened("app-9");
    const roles = await waitFor(() => { const el = document.querySelector("[data-role-secrets]") as HTMLElement; expect(el.textContent).toContain("b7e2"); return el; });
    expect(roles.textContent).toContain("dev-tools, fingerprint b7e2…, set 2026-09-04 08:00:00 UTC");
    await userEvent.click(within(roles).getByRole("button", { name: "Remove" }));
    let d = await dialog();
    expect(d.textContent).toContain("Remove the secret for dev-tools?");
    expect(d.textContent).toContain("Calls by dev-tools fall back to the shared secret, or run without one when none is stored.");
    await userEvent.click(within(d).getByRole("button", { name: "Remove secret" }));
    await waitFor(() => expect(removeSecret).toHaveBeenCalledWith("app-9", "dev-tools"));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    const shared = within(credCard()).getAllByRole("button", { name: "Remove" })[0];
    await userEvent.click(shared);
    d = await dialog();
    expect(d.textContent).toContain("Remove the shared secret?");
    expect(d.textContent).toContain("Calls and health checks for scout-tools run without a credential from now on, unless a role has its own. A server that requires one reads degraded until a secret is set again.");
    await userEvent.click(within(d).getByRole("button", { name: "Remove secret" }));
    await waitFor(() => expect(removeSecret).toHaveBeenCalledWith("app-9", ""));
  });

  it("publishes a removal after a confirm that names the blast radius, then goes back to the list", async () => {
    await opened("app-9");
    await userEvent.click(head().getByRole("button", { name: "Remove server" }));
    const d = await dialog();
    expect(d.textContent).toContain("Remove scout-tools?");
    expect(d.textContent).toContain("Its access rows for scout-role and its stored secrets are deleted, and sessions calling its tools find them gone.");
    expect(d.textContent).toContain("Rules naming it in policies stay and gate nothing until a server with this name is installed again.");
    expect(within(d).getAllByRole("button").map((b) => b.textContent)).toEqual(["Cancel", "Save draft", "Remove server…"]);
    expect(save.publish).not.toHaveBeenCalled();
    await userEvent.click(within(d).getByRole("button", { name: "Remove server…" }));
    await waitFor(() => expect(save.publish).toHaveBeenCalledWith([{ kind: "App", name: "scout-tools", op: "remove", doc: "" }]));
    expect(removeApp).not.toHaveBeenCalled();
    expect([save.opts?.name, save.opts?.title, save.opts?.toast]).toEqual(["scout-tools", "Remove scout-tools?", "scout-tools was removed."]);
    expect(navigate).not.toHaveBeenCalledWith("servers");
    save.opts?.onPublished({} as DraftPublished);
    expect(navigate).toHaveBeenCalledWith("servers");
  });

  it("saves the removal to the working draft from the same confirm", async () => {
    await opened("app-9");
    await userEvent.click(head().getByRole("button", { name: "Remove server" }));
    await userEvent.click(within(await dialog()).getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(save.draft).toHaveBeenCalledWith([{ kind: "App", name: "scout-tools", op: "remove", doc: "" }]));
    expect(save.publish).not.toHaveBeenCalled();
  });

  it("pauses only after the confirm, then shows the stopped row with the admin-paused chip", async () => {
    await opened("app-9");
    await userEvent.click(head().getByRole("button", { name: "Pause" }));
    const d = await dialog();
    expect(d.textContent).toContain("Pause scout-tools?");
    expect(d.textContent).toContain("An admin pause stops live traffic: its tools leave every session's list and every call to it is denied until you enable it again. Its process is stopped; a remote server is left running on its own host.");
    expect(disableApp).not.toHaveBeenCalled();
    vi.mocked(disableApp).mockImplementation(async () => {
      vi.mocked(listApps).mockResolvedValue(apps.map((a) => (a.id === "app-9" ? { ...a, status: "stopped", paused: true } : a)) as never);
      return { ...scout, status: "stopped", paused: true } as never;
    });
    await userEvent.click(within(d).getByRole("button", { name: "Pause server" }));
    await waitFor(() => expect(disableApp).toHaveBeenCalledWith("app-9"));
    await waitFor(() => expect(banner().getAttribute("data-server-status")).toBe("stopped"));
    expect(banner().textContent).toContain("Paused by administrator");
    expect(headButtons()).toEqual(["Remove server", "Test a call", "Recheck", "Enable"]);
    expect(notify.ok).toHaveBeenCalledWith("scout-tools is paused. Its tools left every session's list.");
  });

  it("enables at once, without a confirm", async () => {
    vi.mocked(enableApp).mockResolvedValue({ ...apps[3], status: "starting", paused: false } as never);
    await opened("app-4");
    await userEvent.click(head().getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(enableApp).toHaveBeenCalledWith("app-4"));
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(notify.ok).toHaveBeenCalledWith("sleeper is enabled. Straza starts it and checks it now.");
    await waitFor(() => expect(listApps).toHaveBeenCalledTimes(2));
  });

  it("renders a 404 on enable in the refused voice and keeps the row as it was", async () => {
    vi.mocked(enableApp).mockRejectedValue(apiError(404, "no such app"));
    await opened("app-4");
    await userEvent.click(head().getByRole("button", { name: "Enable" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("Enable refused. The server no longer knows this MCP server. Reload the list.");
    expect(notify.failed).toHaveBeenCalledWith("Enable refused: the server no longer knows this MCP server. Reload the list.");
    expect(banner().getAttribute("data-server-status")).toBe("stopped");
    expect(listApps).toHaveBeenCalledTimes(1);
  });

  it("renders a 403 on pause with the server's own sentence, and an unreachable write as state unknown", async () => {
    vi.mocked(disableApp).mockRejectedValue(apiError(403, "requires role straza-admin"));
    await opened("app-9");
    await userEvent.click(head().getByRole("button", { name: "Pause" }));
    await userEvent.click(within(await dialog()).getByRole("button", { name: "Pause server" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("Pause refused. Requires role straza-admin.");
    expect(notify.failed).toHaveBeenCalledWith("Pause refused: requires role straza-admin.");
    expect(banner().getAttribute("data-server-status")).toBe("running");
    vi.mocked(recheckApp).mockRejectedValue(apiError(0, "unreachable", true));
    await userEvent.click(head().getByRole("button", { name: "Recheck" }));
    const status = await screen.findByRole("status");
    expect(status.textContent).toBe("Recheck: unreachable, state unknown. Recheck did not reach strazad. Check the connection, then try again.");
    expect(notify.failed).toHaveBeenCalledWith("Recheck did not reach strazad. Check the connection, then try again.");
  });

  it("merges a recheck into the row and keeps the role chips", async () => {
    vi.mocked(recheckApp).mockResolvedValue({ id: "app-9", name: "scout-tools", runtime: "remote", status: "degraded", detail: "timeout", tools: ["echo"], reached_by: [], url: "http://demo-tools:3001/mcp" });
    await opened("app-9");
    await userEvent.click(head().getByRole("button", { name: "Recheck" }));
    await waitFor(() => expect(banner().getAttribute("data-server-status")).toBe("degraded"));
    expect(banner().textContent).toContain("Straza could not reach http://demo-tools:3001/mcp (no answer in time).");
    expect(banner().textContent).toContain("Role accessscout-role");
    expect(notify.warn).toHaveBeenCalledWith("scout-tools reads degraded after the probe. Straza could not reach http://demo-tools:3001/mcp (no answer in time).");
  });

  it("shows the manifest as installed, and says so when an older row carries none", async () => {
    await opened("app-9", "manifest");
    const pre = document.querySelector("pre") as HTMLElement;
    for (const want of ["name: scout-tools", "url: http://demo-tools:3001/mcp", "kind: static", "name: X-Demo-Key", 'tools:\n      - "*"']) expect(pre.textContent).toContain(want);
    expect(screen.getByRole("button", { name: "Download app.yaml" })).toBeTruthy();
    document.body.innerHTML = "";
    await opened("app-2", "manifest");
    expect(screen.getByText("The console cannot read a stored manifest for this server. The file you installed, or the apps directory, holds it; the runtime is command, version 1.0.")).toBeTruthy();
  });

  it.each([
    { id: "app-9", why: "a server added through the API", line: "To change it, use the Change buttons on Overview. strazactl apps install -f scout-tools.yaml does the same from a file you keep.", code: "strazactl apps install -f scout-tools.yaml" },
    { id: "app-10", why: "a server a file names", line: "To change it, use the Change buttons on Overview, or edit midpoint-file.app.yaml in /etc/straza/apps, which proposes a draft a person publishes.", code: null },
    { id: "app-2", why: "a row with no stored manifest", line: "To change it, edit the file you installed and run strazactl apps install -f legacy-app.yaml.", code: "strazactl apps install -f legacy-app.yaml" },
  ])("names the way to change $why under the manifest", async ({ id, line, code }) => {
    await opened(id, "manifest");
    const p = document.querySelector("[data-change-path]") as HTMLElement;
    expect(p.textContent).toBe(line);
    expect(p.querySelector("code")?.textContent ?? null).toBe(code);
  });

  it("says when no server has the id and offers the list", async () => {
    mount("app-0");
    expect((await screen.findByText("No server has this id.")).textContent).toBe("No server has this id.");
    expect(screen.getByText("The list strazad answered has no row with the id app-0: it was removed, or the address is stale.")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Open MCP servers" }));
    expect(navigate).toHaveBeenCalledWith("servers");
  });

  it("shows the locked panel with the server's own sentence for a server the account does not administer", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(appLogs).mockRejectedValue(apiError(403, "this server's admin role is mcp-admin-demo-tools, which you do not hold. Ask your identity manager for mcp-admin-demo-tools, or a holder of straza-global-mcp-admin to make the change."));
    mount("demo-tools");
    await screen.findByText("demo-tools is not available to this account.");
    expect(screen.getByText("This server's admin role is mcp-admin-demo-tools, which you do not hold. Ask your identity manager for mcp-admin-demo-tools, or a holder of straza-global-mcp-admin to make the change.")).toBeTruthy();
    expect(head().getByText("A server this account does not administer.")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Open MCP servers" }));
    expect(navigate).toHaveBeenCalledWith("servers");
    expect(appLogs).toHaveBeenCalledWith("demo-tools");
  });

  it("keeps Not found for an id strazad does not know, and never asks the logs route under a full grant", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(appLogs).mockRejectedValue(apiError(404, "unknown app"));
    mount("nosuch");
    await screen.findByText("No server has this id.");
    vi.mocked(adminAreas).mockReturnValue(null);
    mount("nosuch-2");
    await screen.findAllByText("No server has this id.");
    expect(appLogs).toHaveBeenCalledTimes(1);
  });

  it("offers no Test a call door to a session whose standing lacks the policy area", async () => {
    vi.mocked(adminAreas).mockReturnValue({ apps: true });
    await opened("app-9");
    expect(headButtons()).not.toContain("Test a call");
    expect(head().getByRole("button", { name: "Remove server" })).toBeTruthy();
  });

  it("keeps the Test a call door for a full session", async () => {
    await opened("app-9");
    expect(headButtons()).toContain("Test a call");
  });
  it("stands on the row alone for a server admin whose tools and bindings reads are refused", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    vi.mocked(listTools).mockRejectedValue(apiError(403, "session lacks grant apps:read"));
    vi.mocked(listBindings).mockRejectedValue(apiError(403, "session lacks grant apps:read"));
    await opened("app-9", "tools");
    expect(screen.queryByText(/could not be read/)).toBeNull();
    const table = screen.getByRole("table");
    expect(within(table).getAllByRole("row").map((r) => r.querySelector("td")?.textContent?.trim()).filter(Boolean)).toEqual(["echo", "get-sum", "get-env"]);
    expect(within(table).getAllByText("not readable").length).toBe(3);
    // The Policy column keeps its words and claims no outcome, so it carries no chip.
    expect(within(table).getAllByText("not readable with this account").length).toBe(3);
    expect(table.querySelector("[data-tone]")).toBeNull();
    expect(screen.getByText(/not readable with this account, because the access rows belong to the apps area/)).toBeTruthy();
    expect(banner()!.textContent).toContain("scout-role");
    expect(banner()!.textContent).not.toContain("no role yet");
  });

  it("says the last call is not readable when the audit area refuses it", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    vi.mocked(listAudit).mockRejectedValue(apiError(403, "session lacks grant audit:read"));
    await opened("app-9");
    expect(banner()!.textContent).toContain("Last callnot readable with this account.");
  });

  it("keeps the reach columns for a full session", async () => {
    await opened("app-9", "tools");
    expect(screen.queryByText(/access rows belong to the apps area/)).toBeNull();
    expect(screen.getByText(/This table answers/)).toBeTruthy();
  });
  it("offers Pause but never Remove server to a server admin, since removal deletes the office", async () => {
    vi.mocked(adminAreas).mockReturnValue({});
    await opened("app-9");
    expect(headButtons()).not.toContain("Remove server");
    expect(headButtons()).toContain("Pause");
  });
});
