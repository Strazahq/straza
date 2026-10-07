import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServerActivity } from "./server-activity";
import { type AppRow, appLogs, listAudit, listChanges } from "@/lib/api";

vi.mock("@/lib/api", () => ({
  appLogs: vi.fn(),
  listAudit: vi.fn(),
  listChanges: vi.fn(),
}));

const NOW = Date.now();
const iso = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();

// app is one row in the GET /v1/admin/apps shape, a degraded container
// server by default.
function app(extra: Partial<AppRow> = {}): AppRow {
  return {
    id: "a-gh", name: "github", status: "degraded", version: "2.4.1", runtime: "oci",
    detail: "inventory drift: +[create_release] -[delete_repo]",
    last_probe_at: iso(20), last_healthy_at: iso(800), status_since: iso(720),
    tools: ["create_release", "list_issues"], reached_by: ["dev", "release"], ...extra,
  };
}

// auditRow is one record as GET /v1/admin/audit returns it: ce is a JSON string.
const auditRow = (seq: number, type: string, secondsAgo: number, data: Record<string, unknown>, username: string) =>
  ({ seq, username, ce: JSON.stringify({ specversion: "1.0", type, time: iso(secondsAgo), data }) });

// A gateway record's data.user is the user's id; the list row carries the
// username the server resolved. A call to a name no role reaches has no
// app, only the namespaced name the caller sent (gateway_tools.go).
const AUDIT = [
  auditRow(10, "straza.audit.mcp", 100, { app: "", user: "u-0192-joe", harness: "claude-code", toolName: "github__list_issues", effect: "deny", reason: 'unknown tool "github__list_issues": no access row for this session\'s roles admits it' }, "joe"),
  auditRow(9, "straza.audit.mcp", 300, { app: "github", user: "u-0192-joe", harness: "claude-code", toolName: "delete_repo", effect: "deny", reason: "Straza: delete_repo is refused by policy" }, "joe"),
  auditRow(8, "straza.audit.admin", 400, { action: "apps.recheck", app: "github", status: "degraded", detail: "inventory drift: +[create_release] -[delete_repo]" }, "alice"),
  auditRow(7, "straza.audit.mcp", 500, { app: "github", user: "u-0193-sam", harness: "exec-wrapper", toolName: "create_release", effect: "approve", reason: "Straza: create_release held for approval" }, "sam-sre-agent"),
  auditRow(6, "straza.audit.mcp", 900, { app: "github", user: "u-0192-joe", toolName: "list_issues", effect: "allow" }, "joe"),
  auditRow(5, "straza.audit.admin", 6000, { action: "apps.install", app: "github", runtime: "oci" }, "alice"),
  auditRow(4, "straza.audit.admin", 6100, { action: "apps.install", app: "github-old", runtime: "oci" }, "alice"),
  auditRow(3, "straza.audit.mcp", 6200, { app: "", user: "u-0192-joe", toolName: "github-old__ping", effect: "deny", reason: 'unknown tool "github-old__ping"' }, "joe"),
];

// The change feed names the server by id and carries no tool names.
const CHANGES = [
  { cursor: "c3", type: "tool", op: "update", id: "a-other", at: iso(6500) },
  { cursor: "c7", type: "tool", op: "update", id: "a-gh", at: iso(720) },
];

const LOGS = { app: "github", lines: [], entries: [
  { t: iso(900), line: "info: github-mcp 2.4.1 starting, transport stdio" },
  { t: iso(890), line: "warn: GITHUB_TOKEN scope lacks delete_repo; hiding tool" },
  { t: iso(720), line: "status → degraded inventory drift: +[create_release] -[delete_repo]" },
  { t: iso(300), line: "error: tools/call delete_repo: unknown tool" },
  { t: iso(200), line: "Error: connect ECONNREFUSED 10.0.0.12:443" },
  { t: iso(190), line: "Error: connect ECONNREFUSED 10.0.0.12:443" },
  { t: iso(180), line: "Error: connect ECONNREFUSED 10.0.0.12:443" },
  { t: iso(170), line: "debug: retry 1/3 in 2000 ms" },
  { t: iso(10), line: "info: registered 9 tools" },
  { t: iso(5), line: "fatal: upstream unreachable after 3 retries, exiting" },
] };

const refused = (status: number, message: string) => Object.assign(new Error(message), { status, unreachable: false });
const box = (sel: string) => document.querySelector(sel) as HTMLElement;

describe("the Activity tab of a server's page", () => {
  beforeEach(() => {
    vi.mocked(listAudit).mockResolvedValue(AUDIT);
    vi.mocked(listChanges).mockResolvedValue({ changes: CHANGES });
    vi.mocked(appLogs).mockReset().mockResolvedValue(LOGS);
  });
  afterEach(() => vi.useRealTimers());

  it("lists Straza's own records about this server as sentences, newest first across both sources", async () => {
    render(<ServerActivity app={app()} />);
    await screen.findByText(/installed it \(oci\)\./);
    const et = box("[data-server-events]").textContent || "";
    expect(listAudit).toHaveBeenCalledWith("q=github&order=desc&limit=100");
    expect(listChanges).toHaveBeenCalledWith("tool", 1000);
    expect(et).toContain('joe\'s claude-code session called list_issues. Denied: unknown tool "github__list_issues": no access row');
    expect(et).toContain("joe's claude-code session called delete_repo. Denied: Straza: delete_repo is refused by policy");
    expect(et).not.toContain("u-0192");
    expect(et).not.toContain("github-old__ping");
    expect(et).toContain("alice ran Recheck: degraded, inventory drift: +[create_release] -[delete_repo].");
    expect(et).toContain("sam-sre-agent's exec-wrapper session called create_release. Needs approval: Straza: create_release held for approval");
    expect(et).toContain("Health check found the tool list changed.");
    expect(et).toContain("alice installed it (oci).");
    expect(et.match(/tool list changed/g)?.length).toBe(1);
    expect(et).not.toContain("github-old");
    expect(et.match(/called list_issues/g)?.length).toBe(1);
    const order = ["unknown tool", "refused by policy", "ran Recheck", "Needs approval", "tool list changed", "installed it"].map((w) => et.indexOf(w));
    expect(order).toEqual([...order].sort((a, b) => a - b));
    for (const src of ["denied call", "admin action", "hold", "drift"]) expect(et).toContain(src);
  });

  it("starts the record after the newest removal of a server with this name", async () => {
    vi.mocked(listAudit).mockResolvedValue([
      auditRow(23, "straza.audit.admin", 60, { action: "apps.recheck", app: "github", status: "running" }, "alice"),
      auditRow(22, "straza.audit.admin", 70, { action: "apps.install", app: "github", runtime: "oci" }, "alice"),
      auditRow(21, "straza.audit.admin", 80, { action: "apps.remove", app: "github", roles: ["dev"] }, "alice"),
      auditRow(20, "straza.audit.mcp", 90, { app: "github", user: "u-0192-joe", toolName: "delete_repo", effect: "deny", reason: "refused" }, "joe"),
      auditRow(19, "straza.audit.admin", 100, { action: "apps.install", app: "github", runtime: "oci" }, "alice"),
    ]);
    vi.mocked(listChanges).mockResolvedValue({ changes: [] });
    render(<ServerActivity app={app()} />);
    await screen.findByText(/ran Recheck: running\./);
    const et = box("[data-server-events]").textContent || "";
    expect(et.match(/installed it/g)?.length).toBe(1);
    expect(et).not.toContain("removed it");
    expect(et).not.toContain("delete_repo");
  });

  // An install record carries update and the changed manifest field paths,
  // never their values, because an address or a variable can carry a token.
  const INSTALLS: [string, Record<string, unknown>, string][] = [
    ["a first install names its runtime", { runtime: "oci" }, "alice installed it (oci)."],
    ["update false reads as a first install", { runtime: "remote", update: false, changed: ["straza.runtime.remote.url"] }, "alice installed it (remote)."],
    ["an update that changed no field", { runtime: "remote", update: true, changed: [] }, "alice installed the same manifest again."],
    ["an update strazad could not compare", { runtime: "remote", update: true },
      "alice installed it again. Straza could not compare it with the stored manifest, so which fields changed is not recorded."],
    ["one changed field", { runtime: "remote", update: true, changed: ["straza.runtime.remote.url"] }, "alice changed the address."],
    ["fields deduplicated in their words", { runtime: "oci", update: true, changed: ["straza.runtime.kind", "straza.runtime.command", "straza.runtime.oci.image", "straza.runtime.command.env", "straza.runtime.oci.env"] },
      "alice changed the transport, command, image and environment."],
    ["phrases that bring their own article", { runtime: "remote", update: true, changed: ["straza.credential.kind", "straza.credential.inject.template", "straza.credential.agents"] },
      "alice changed the credential type, the way the credential is sent and what agents with nothing of their own do."],
    ["a block that appeared whole, the registry record and the rest", { runtime: "remote", update: true, changed: ["straza.credential", "server.version", "straza.limits.cpu", "straza.limits.mem", "metadata.namespace"] },
      "alice changed the credential, registry record, resource limits and settings."],
    ["the settings", { runtime: "remote", update: true, changed: ["metadata.description", "straza.exposure.tools", "straza.limits.rps", "straza.limits.timeoutSeconds"] },
      "alice changed the description, tools exposed, rate limit and per-call timeout."],
  ];
  it.each(INSTALLS)("reads an install record: %s", async (_, data, words) => {
    vi.mocked(listAudit).mockResolvedValue([auditRow(30, "straza.audit.admin", 50, { action: "apps.install", app: "github", ...data }, "alice")]);
    vi.mocked(listChanges).mockResolvedValue({ changes: [] });
    render(<ServerActivity app={app()} />);
    await screen.findByText("admin action");
    expect(box("[data-server-events]").textContent).toContain(words + " admin action");
  });

  it("says so when nothing is recorded about the server yet", async () => {
    vi.mocked(listAudit).mockResolvedValue([]);
    vi.mocked(listChanges).mockResolvedValue({ changes: [] });
    render(<ServerActivity app={app()} />);
    expect((await screen.findByText(/^Nothing recorded about this server yet\./)).textContent)
      .toBe("Nothing recorded about this server yet. Admin actions, tool list changes, denied calls and holds land here.");
  });

  it("tallies the lines since the last check and marks where it ran", async () => {
    render(<ServerActivity app={app()} />);
    await screen.findByText("info: registered 9 tools");
    const tally = box("[data-log-tally]").textContent || "";
    expect(tally).toContain("1 error");
    expect(tally).toContain("0 warnings");
    expect(tally).toContain("2 lines since the last check");
    expect(tally).toMatch(/last error \d+ s ago/);
    const markers = [...document.querySelectorAll("[data-log-marker]")].map((m) => m.textContent || "");
    expect(markers.some((m) => m.includes("health check · degraded, inventory drift"))).toBe(true);
    expect(box("[data-log-lines]").textContent).not.toContain("ECONNREFUSED");
  });

  it("folds a repeated line once with its count and keeps the manager's status change as a marker", async () => {
    render(<ServerActivity app={app()} />);
    await screen.findByText("info: registered 9 tools");
    await userEvent.click(screen.getByRole("button", { name: "Everything" }));
    const all = box("[data-log-lines]").textContent || "";
    expect(all).toContain("Error: connect ECONNREFUSED 10.0.0.12:443×3");
    expect(all.match(/ECONNREFUSED/g)?.length).toBe(1);
    const markers = [...document.querySelectorAll("[data-log-marker]")].map((m) => m.textContent || "");
    expect(markers.some((m) => m.includes("degraded inventory drift"))).toBe(true);
    const tally = box("[data-log-tally]").textContent || "";
    expect(tally).toContain("5 errors");
    expect(tally).toContain("1 warning");
    expect(tally).toContain("9 lines in the ring");
  });

  it("hides the other lines behind a count under the Errors filter, and Show all brings them back", async () => {
    render(<ServerActivity app={app()} />);
    await screen.findByText("info: registered 9 tools");
    await userEvent.click(screen.getByRole("button", { name: "Everything" }));
    await userEvent.click(screen.getByRole("button", { name: "Errors" }));
    expect(box("[data-log-hidden]").textContent).toContain("4 other lines hidden by the filter.");
    expect(box("[data-log-lines]").textContent).not.toContain("registered 9 tools");
    expect(screen.getByRole("button", { name: "Copy 3 lines" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(document.querySelector("[data-log-hidden]")).toBeNull();
    expect(box("[data-log-lines]").textContent).toContain("registered 9 tools");
  });

  it("polls the log every 5 s while shown, stops with Follow off, and stops when the tab goes away", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    const { unmount } = render(<ServerActivity app={app()} />);
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const first = vi.mocked(appLogs).mock.calls.length;
    expect(first).toBe(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    expect(vi.mocked(appLogs).mock.calls.length).toBe(2);
    await act(async () => { screen.getByRole("button", { name: "follow the log" }).click(); });
    expect(screen.getByRole("button", { name: "follow the log" }).textContent).toBe("off");
    await act(async () => { await vi.advanceTimersByTimeAsync(12000); });
    expect(vi.mocked(appLogs).mock.calls.length).toBe(2);
    await act(async () => { screen.getByRole("button", { name: "follow the log" }).click(); });
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    const during = vi.mocked(appLogs).mock.calls.length;
    expect(during).toBeGreaterThan(2);
    unmount();
    await act(async () => { await vi.advanceTimersByTimeAsync(20000); });
    expect(vi.mocked(appLogs).mock.calls.length).toBe(during);
  });

  it("reads a stopped runtime as one sentence and never shows the raw 409", async () => {
    vi.mocked(appLogs).mockRejectedValue(refused(409, "app is not running (no log ring)"));
    render(<ServerActivity app={app({ status: "stopped", paused: true, detail: "" })} />);
    const log = await screen.findByText(/The runtime is stopped, so there is no output\./);
    expect(log.closest("[data-server-log]")?.getAttribute("data-server-log")).toBe("stopped");
    expect(document.body.textContent).not.toContain("no log ring");
  });

  it("reads a remote server's log as one sentence and never fetches it", async () => {
    render(<ServerActivity app={app({ runtime: "remote", status: "running", detail: "", url: "https://mcp.example.test/mcp" })} />);
    const log = await screen.findByText(/^A remote server keeps its own log\./);
    expect(log.textContent).toMatch(/Straza shows its health checks here: the last one answered \d+ s ago\./);
    expect(appLogs).not.toHaveBeenCalled();
  });

  it("says when the runtime has written nothing yet", async () => {
    vi.mocked(appLogs).mockResolvedValue({ app: "github", lines: [], entries: [] });
    render(<ServerActivity app={app({ status: "running", detail: "" })} />);
    expect((await screen.findByText("No log output")).textContent).toBe("No log output");
    expect(screen.getByText("The runtime has not written anything yet.")).toBeTruthy();
  });
  it("says the events are not readable when the audit area refuses them, instead of an error banner", async () => {
    vi.mocked(listAudit).mockRejectedValue(Object.assign(new Error("session lacks grant audit:read"), { status: 403 }));
    vi.mocked(listChanges).mockRejectedValue(Object.assign(new Error("session lacks grant changes:read"), { status: 403 }));
    render(<ServerActivity app={app()} />);
    expect(await screen.findByText(/Events are not readable with this account/)).toBeTruthy();
    expect(screen.queryByText(/could not be read/)).toBeNull();
  });
});
