import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { TestACall, simulationOutcome } from "./test-a-call";
import { type ObjectsAnswer, appObjects, listAudit, listRoles, overview, simulate } from "@/lib/api";
import { version } from "@/lib/public";

vi.mock("@/lib/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api")>()),
  appObjects: vi.fn(),
  listAudit: vi.fn(),
  listRoles: vi.fn(),
  overview: vi.fn(),
  simulate: vi.fn(),
}));
vi.mock("@/lib/public", () => ({ version: vi.fn() }));

// The eval fixtures: one application role, one business, one approver,
// in the order the server happens to list them.
const roles = [
  { id: "r-dev", name: "dev", kind: "business" },
  { id: "r-tools", name: "dev-tools", kind: "application" },
  { id: "r-appr", name: "sec-approvers", kind: "approver" },
];

const app = { id: "a-demo", name: "demo-tools" };
const ce = (type: string, data: Record<string, unknown>) => JSON.stringify({ specversion: "1.0", type, time: "2026-09-11T09:35:24Z", data });
const failure = (message: string, status: number, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

const mount = (tools: string[] = ["echo", "get-sum"]) => render(<TooltipProvider><TestACall open onOpenChange={() => {}} app={app} tools={tools} /></TooltipProvider>);
const ready = () => screen.findByRole("button", { name: "dev-tools" });
const pills = () => within(document.querySelector("[data-role-pills]") as HTMLElement).getAllByRole("button").map((b) => [b.textContent, b.getAttribute("aria-pressed")]);
const run = () => userEvent.click(screen.getByRole("button", { name: "Run test" }));
const lastBody = () => vi.mocked(simulate).mock.calls[vi.mocked(simulate).mock.calls.length - 1][0];
const answer = (active: Record<string, unknown>) => vi.mocked(simulate).mockResolvedValue({ active: active as { effect: string }, subject: {}, snapshot: "b8c0a55073d40b84ffee" });
const result = () => screen.findByRole("status");

describe("the Test a call sheet", () => {
  beforeEach(() => {
    vi.mocked(listRoles).mockReset().mockResolvedValue(roles);
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "enterprise" });
    vi.mocked(simulate).mockReset();
    vi.mocked(overview).mockReset();
    vi.mocked(listAudit).mockReset();
    vi.mocked(appObjects).mockReset().mockResolvedValue({ app: "demo-tools", type: "", objects: [], total: 0 });
  });

  it("opens with the title, the live context line and the pinned event", async () => {
    mount();
    expect(screen.getByRole("heading", { name: "Test a call: what would happen" })).toBeTruthy();
    expect(screen.getByText("Testing the policies live right now, on demo-tools. Nothing is saved or enforced, and no audit record is written.")).toBeTruthy();
    expect(screen.getByText("tool.pre · mcp.call on demo-tools")).toBeTruthy();
    expect(screen.getByText("Pinned to this server; the Policies page tests any event.")).toBeTruthy();
    await ready();
  });

  it("lists the roles with the application roles first and the first one picked", async () => {
    mount();
    await ready();
    expect(listRoles).toHaveBeenCalledTimes(1);
    expect(pills()).toEqual([["dev-tools", "true"], ["dev", "false"], ["sec-approvers", "false"]]);
    expect(screen.getByText("Application roles carry access rows; the others are here so a wrong pick reads as a denial, not a surprise.")).toBeTruthy();
  });

  it("names the missing answer instead of running with nothing picked", async () => {
    mount();
    await userEvent.click(await ready());
    await run();
    expect(screen.getByText("Pick at least one role, or name a user.")).toBeTruthy();
    expect(simulate).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "one user" }));
    await run();
    expect(screen.getByText("Pick at least one role, or name a user.")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "user" }));
    expect(simulate).not.toHaveBeenCalled();
  });

  it("posts the pinned event with the picked roles and never a draft", async () => {
    answer({ effect: "allow" });
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "dev" }));
    await run();
    await result();
    const body = lastBody();
    expect(body).toEqual({ event: { kind: "tool.pre", tool: "mcp.call", app: "demo-tools", toolName: "echo" }, subject: { roles: ["dev-tools", "dev"] } });
    expect("draft" in body).toBe(false);
    expect("attestation" in body.subject).toBe(false);
  });

  it("posts one user with the attestation and the tool picked", async () => {
    answer({ effect: "allow" });
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "one user" }));
    await userEvent.type(screen.getByRole("textbox", { name: "user" }), "alice");
    expect(screen.getByText("The server resolves the user's roles and typology, so the answer matches a live session.")).toBeTruthy();
    await userEvent.click(screen.getByRole("combobox", { name: "attestation" }));
    await userEvent.click(await screen.findByRole("option", { name: "managed" }));
    await userEvent.click(screen.getByRole("combobox", { name: "tool name" }));
    await userEvent.click(await screen.findByRole("option", { name: "get-sum" }));
    await run();
    await result();
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "mcp.call", app: "demo-tools", toolName: "get-sum" }, subject: { user: "alice", attestation: "managed" } });
  });

  it("reads a gated allow: the effect, the human approval badge and the rule", async () => {
    answer({ effect: "allow", ruleId: "dev-mcp-sum-approval-showcase", setName: "dev-guardrails", reason: "Straza: get-sum held for approval.", approve: { deciders: ["sec-approvers"] } });
    mount();
    await ready();
    await run();
    const card = await result();
    expect(within(card).getByText("Needs approval")).toBeTruthy();
    expect(card.className).toContain("border-warn");
    expect(within(card).queryByText("allowed")).toBeNull();
    expect(card.textContent).toContain("Decided by rule dev-mcp-sum-approval-showcase in policy dev-guardrails: Straza: get-sum held for approval.");
    expect(card.textContent).toContain("subject: roles dev-tools · snapshot b8c0a55073d40b84 · Simulation only. No tool call was executed. Role access was checked before policy evaluation.");
  });

  it("reads an allow by role access as running instantly", async () => {
    answer({ effect: "allow", reason: "allowed by role access" });
    mount();
    await ready();
    await run();
    const card = await result();
    expect(within(card).getByText("Allowed")).toBeTruthy();
    expect(card.textContent).toContain("No policy matched this call. Allowed by role access. No policy rule gates this tool.");
  });

  it("reads a deny with a rule", async () => {
    answer({ effect: "deny", ruleId: "no-env", setName: "dev-guardrails", reason: "Straza: get-env blocked" });
    mount();
    await ready();
    await run();
    const card = await result();
    expect(within(card).getByText("Denied")).toBeTruthy();
    expect(card.textContent).toContain("Decided by rule no-env in policy dev-guardrails: Straza: get-env blocked.");
    expect(within(card).queryByText("allowed")).toBeNull();
  });

  it("reads a deny with no rule by the profile default", async () => {
    answer({ effect: "deny", reason: "no policy allowed" });
    mount();
    await ready();
    await run();
    expect((await result()).textContent).toContain("No policy matched this call. Denied by the enterprise profile default: a call nothing allows is denied.");
  });

  it("falls back to the plain profile sentence when the profile is unknown", async () => {
    vi.mocked(version).mockResolvedValue(null);
    answer({ effect: "deny" });
    mount();
    await ready();
    await run();
    expect((await result()).textContent).toContain("No policy matched this call. Denied by the profile default: no policy allowed this call.");
  });

  it("replays a recent call to this server from the audit window", async () => {
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: 1500 } });
    vi.mocked(listAudit).mockResolvedValue([
      { seq: 1489, username: "sam-sre-agent", ce: ce("straza.audit.mcp", { app: "demo-tools", tool: "mcp.call", toolName: "get-sum", effect: "approve", user: "sam-sre-agent" }) },
      { seq: 1490, username: "joe-java-developer-agent", ce: ce("straza.audit.mcp", { app: "midpoint", tool: "mcp.call", toolName: "midpoint__search", effect: "allow" }) },
      { seq: 1491, username: "sam-sre-agent", ce: ce("straza.audit.mcp", { app: "demo-tools", tool: "mcp.call", toolName: "echo", effect: "allow" }) },
      { seq: 1492, username: "admin", ce: ce("straza.audit.admin", { app: "demo-tools", action: "apps.recheck", status: "running" }) },
    ]);
    answer({ effect: "allow" });
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "Replay a recent call to this server" }));
    const list = await screen.findByText("#1491");
    expect(screen.getByRole("button", { name: "Close the replay list" })).toBeTruthy();
    expect(listAudit).toHaveBeenCalledWith("after=1300&limit=1000");
    const rows = within(list.closest("[data-replay-list]") as HTMLElement).getAllByRole("button").map((b) => b.textContent);
    expect(rows).toEqual(["#1491sam-sre-agentechoallow", "#1489sam-sre-agentget-sumapprove"]);
    await userEvent.click(screen.getByRole("button", { name: /#1489/ }));
    expect(document.querySelector("[data-replay-list]")).toBeNull();
    expect(screen.getByRole("button", { name: "Replay a recent call to this server" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "one user" }).getAttribute("aria-pressed")).toBe("true");
    expect((screen.getByRole("textbox", { name: "user" }) as HTMLInputElement).value).toBe("sam-sre-agent");
    expect(screen.getByRole("combobox", { name: "tool name" }).textContent).toContain("get-sum");
    await run();
    await result();
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "mcp.call", app: "demo-tools", toolName: "get-sum" }, subject: { user: "sam-sre-agent" } });
  });

  it("says when the window holds no call to this server", async () => {
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: 12 } });
    vi.mocked(listAudit).mockResolvedValue([]);
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "Replay a recent call to this server" }));
    expect(await screen.findByText("No call to this server in the recent audit window.")).toBeTruthy();
    expect(listAudit).toHaveBeenCalledWith("after=0&limit=1000");
  });

  it("hides the form when the server has no simulate endpoint", async () => {
    vi.mocked(simulate).mockRejectedValue(failure("HTTP 404", 404));
    mount();
    await ready();
    await run();
    expect(await screen.findByText("This server does not offer Test a call yet. Upgrade strazad to test calls from the console.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Run test" })).toBeNull();
    expect(screen.queryByRole("button", { name: "dev-tools" })).toBeNull();
  });

  it("shows a 404 about the user as a refusal and keeps the form", async () => {
    vi.mocked(simulate).mockRejectedValue(failure("no such user", 404));
    mount();
    await ready();
    await run();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Test a call refused.");
    expect(alert.textContent).toContain("The server answered: no such user. Check the user name and the tool name, then run the test again.");
    expect(screen.getByRole("button", { name: "Run test" })).toBeTruthy();
  });

  it("clears the result when an input changes", async () => {
    answer({ effect: "allow" });
    mount();
    await ready();
    await run();
    await result();
    await userEvent.click(screen.getByRole("button", { name: "dev" }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("sends no args and shows no picker on a server that pins no type", async () => {
    answer({ effect: "allow" });
    mount();
    await ready();
    await run();
    await result();
    expect(document.querySelector("[data-object-picker]")).toBeNull();
    expect("args" in lastBody().event).toBe(false);
  });

  it("takes a typed tool name when the server listed none", async () => {
    answer({ effect: "allow" });
    mount([]);
    await ready();
    expect(screen.getByText("The server listed no tool yet; type the name to test.")).toBeTruthy();
    await run();
    expect(screen.getByText("Type the tool name to test.")).toBeTruthy();
    expect(simulate).not.toHaveBeenCalled();
    await userEvent.type(screen.getByRole("textbox", { name: "tool name" }), "ping");
    await run();
    await result();
    expect(lastBody().event.toolName).toBe("ping");
  });
});

describe("practical simulation outcomes", () => {
  it.each([
    [{ effect: "allow", approve: {} }, "Needs approval", "warn"],
    [{ effect: "allow", confirm: true }, "Needs approval", "warn"],
    [{ effect: "allow", serverCheck: true }, "Requires additional checks", "warn"],
    [{ effect: "allow", classify: true }, "Requires additional checks", "warn"],
    [{ effect: "deny", approve: {} }, "Denied", "danger"],
    [{ effect: "allow" }, "Allowed", "ok"],
    [{ effect: "" }, "No verdict: the server answered the effect none, which this console cannot read. The line below holds the full answer.", "plain"],
    [{ effect: "hold" }, "No verdict: the server answered the effect hold, which this console cannot read. The line below holds the full answer.", "plain"],
  ] as const)("reads the complete response %j", (decision, label, tone) => {
    expect(simulationOutcome(decision)).toEqual({ label, tone });
  });
});

describe("Test a call on a server that pins repos", () => {
  const gh = { id: "a-gh", name: "ghapp" };
  const repos = [
    { value: "Strazahq/pay-api", id: "9012332", title: "pay-api" },
    { value: "Strazahq/site", id: "9012331", title: "site" },
  ];
  const pinnedAnswer = (q: string): ObjectsAnswer => ({
    app: "ghapp", type: "repo", plural: "repos", total: 2,
    pinned: { type: "repo", list: { tool: "repo_list" }, tools: { pr_create: { argument: "repo" }, get_me: "none" } },
    objects: q.includes("q=zzz") ? [] : q.includes("q=pay") ? repos.slice(0, 1) : repos,
  });
  const refusal = "Straza: pr_create on Strazahq/site was refused: none of your roles on ghapp covers that repo. If the repo is new, Straza refreshes the list within a minute, so try again then.";
  const mountGh = () => render(<TooltipProvider><TestACall open onOpenChange={() => {}} app={gh} tools={["pr_create", "get_me", "repo_create"]} /></TooltipProvider>);
  const search = () => screen.findByRole("textbox", { name: "Search repos by name" });
  const preview = () => (document.querySelector("[data-args-preview]") as HTMLElement).textContent;

  beforeEach(() => {
    vi.mocked(listRoles).mockReset().mockResolvedValue([{ id: "r-w", name: "ghapp-write", kind: "application" }]);
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "enterprise" });
    vi.mocked(simulate).mockReset();
    vi.mocked(overview).mockReset();
    vi.mocked(listAudit).mockReset();
    vi.mocked(appObjects).mockReset().mockImplementation((_id: string, q = "") => Promise.resolve(pinnedAnswer(q)));
  });

  it("picks the repo from the server's list and sends it in the tool's argument", async () => {
    vi.mocked(simulate).mockResolvedValue({
      active: { effect: "allow", reason: "allowed by role access" }, subject: {}, snapshot: "b8c0a55073d40b84ffee",
      object: { type: "repo", reach: "argument", value: "Strazahq/pay-api", id: "9012332", covered: true, role: "ghapp-write" },
    });
    mountGh();
    await search();
    expect(screen.getByText("Pick the repo the call works on. Test a call sends it in the argument repo, as the agent would.")).toBeTruthy();
    expect(preview()).toBe("args {}");
    await userEvent.click(await screen.findByRole("button", { name: /Strazahq\/pay-api/ }));
    expect((document.querySelector("[data-picked]") as HTMLElement).textContent).toContain("Strazahq/pay-api");
    expect(screen.getByText("id 9012332")).toBeTruthy();
    expect(preview()).toBe("args {\"repo\":\"Strazahq/pay-api\"}");
    await run();
    const card = await result();
    expect(within(card).getByText("Allowed")).toBeTruthy();
    expect((card.querySelector("[data-object-line]") as HTMLElement).textContent).toBe("ghapp-write covers Strazahq/pay-api, id 9012332.");
    expect(lastBody().event).toEqual({ kind: "tool.pre", tool: "mcp.call", app: "ghapp", toolName: "pr_create", args: { repo: "Strazahq/pay-api" } });
  });

  it("asks for the repo instead of running without one", async () => {
    mountGh();
    await search();
    await run();
    expect(screen.getByText("Pick the repo this call works on.")).toBeTruthy();
    expect(document.activeElement).toBe(await search());
    expect(simulate).not.toHaveBeenCalled();
  });

  it("searches the list on the server as you type, and says when nothing matches", async () => {
    mountGh();
    await userEvent.type(await search(), "pay");
    await waitFor(() => expect(appObjects).toHaveBeenCalledWith("a-gh", "q=pay&limit=20"));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Strazahq\/site/ })).toBeNull());
    expect(screen.getByRole("button", { name: /Strazahq\/pay-api/ })).toBeTruthy();
    await userEvent.clear(await search());
    await userEvent.type(await search(), "zzz");
    expect(await screen.findByText("No repo in the list matches zzz.")).toBeTruthy();
  });

  it("clears a picked repo back to the search", async () => {
    mountGh();
    await userEvent.click(await screen.findByRole("button", { name: /Strazahq\/site/ }));
    await userEvent.click(screen.getByRole("button", { name: "Clear the repo" }));
    expect(await search()).toBeTruthy();
    expect(preview()).toBe("args {}");
  });

  it("shows no picker on a tool that names no repo", async () => {
    vi.mocked(simulate).mockResolvedValue({ active: { effect: "allow" }, subject: {}, snapshot: "s" });
    mountGh();
    await search();
    await userEvent.click(screen.getByRole("combobox", { name: "tool name" }));
    await userEvent.click(await screen.findByRole("option", { name: "get_me" }));
    expect(document.querySelector("[data-object-picker]")).toBeNull();
    await run();
    await result();
    expect("args" in lastBody().event).toBe(false);
  });

  it("reads the repo check's refusal as the reason, never as a profile default", async () => {
    vi.mocked(simulate).mockResolvedValue({
      active: { effect: "deny", reason: refusal }, subject: {}, snapshot: "b8c0a55073d40b84ffee",
      object: { type: "repo", reach: "argument", value: "Strazahq/site", id: "9012331", covered: false, refusal },
    });
    mountGh();
    await userEvent.click(await screen.findByRole("button", { name: /Strazahq\/site/ }));
    await run();
    const card = await result();
    expect(card.className).toContain("border-danger");
    expect(within(card).getByText("Denied")).toBeTruthy();
    expect((card.querySelector("[data-gate]") as HTMLElement).textContent).toBe("The repo check refused this call before any policy ran. pr_create on Strazahq/site was refused: none of your roles on ghapp covers that repo. If the repo is new, Straza refreshes the list within a minute, so try again then.");
    expect(card.textContent).toContain("subject: roles ghapp-write · snapshot b8c0a55073d40b84 · Simulation only.");
    expect(card.textContent).not.toContain("No policy matched");
  });

  it.each([
    ["names no repo", undefined],
    ["cut the repo it named", { type: "repo", reach: "argument", value: "x".repeat(512), valueTruncated: true, valueSha256: "0f1e" }],
  ])("drops an earlier pick when the replayed record %s", async (_, object) => {
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: 40 } });
    vi.mocked(listAudit).mockResolvedValue([
      { seq: 38, username: "sam-sre-agent", ce: ce("straza.audit.mcp", { app: "ghapp", tool: "mcp.call", toolName: "pr_create", effect: "deny", ...(object ? { object } : {}) }) },
    ]);
    mountGh();
    await userEvent.click(await screen.findByRole("button", { name: /Strazahq\/site/ }));
    expect(document.querySelector("[data-picked]")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Replay a recent call to this server" }));
    await userEvent.click(await screen.findByRole("button", { name: /#38/ }));
    expect(document.querySelector("[data-picked]")).toBeNull();
    expect(preview()).toBe("args {}");
  });

  it.each([
    ["names the missing listing credential", { tool: "repo_list", credential: "missing", outcome: "never", version: 0, count: 0 },
      "ghapp lists its repos with repo_list. ghapp has no listing credential, so Straza cannot list its repos. Add one with strazactl apps secret set ghapp --listing, then refresh with strazactl apps objects refresh ghapp."],
    ["says when the first listing runs", { tool: "repo_list", credential: "set", outcome: "never", version: 0, count: 0 },
      "ghapp lists its repos with repo_list. No listing yet. Straza runs the first one within 30 seconds. Reload the page to see it."],
    ["points at the refresh when the list is empty", { tool: "repo_list", credential: "set", outcome: "ok", version: 2, count: 0, listedAt: "2026-10-19T09:00:00Z" },
      "The repo list is empty, so there is nothing to pick yet. If ghapp has repos now, refresh the list with strazactl apps objects refresh ghapp, then open Test a call again."],
  ])("on an empty list %s", async (_, listing, hint) => {
    vi.mocked(appObjects).mockImplementation((_id: string, q = "") => Promise.resolve({ ...pinnedAnswer(q), objects: [], total: 0, listing }));
    mountGh();
    expect((await screen.findByText(hint)).hasAttribute("data-object-empty")).toBe(true);
  });

  it("replays a recorded call with the repo it named", async () => {
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: 40 } });
    vi.mocked(listAudit).mockResolvedValue([
      { seq: 39, username: "sam-sre-agent", ce: ce("straza.audit.mcp", { app: "ghapp", tool: "mcp.call", toolName: "pr_create", effect: "deny", object: { type: "repo", reach: "argument", value: "Strazahq/site", id: "9012331" } }) },
    ]);
    mountGh();
    await search();
    await userEvent.click(screen.getByRole("button", { name: "Replay a recent call to this server" }));
    await userEvent.click(await screen.findByRole("button", { name: /#39/ }));
    expect((document.querySelector("[data-picked]") as HTMLElement).textContent).toContain("Strazahq/site");
    expect(preview()).toBe("args {\"repo\":\"Strazahq/site\"}");
  });
});
