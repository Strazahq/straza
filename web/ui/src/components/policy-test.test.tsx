import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyTest, type PolicyTestPrefill } from "./policy-test";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type BindingRow, type SimulateAnswer, type ToolRow, type UserRow, listApps, listBindings, listTools, listUsers, simulate } from "@/lib/api";
import { version } from "@/lib/public";
import { ENGINE_FIELDS, LIVE_NOW, TEST_EVERY_SERVER, TEST_MISSING_WHAT, TEST_MISSING_WHO, TEST_SERVERS_HINT, WITH_EDITS, WITH_EDITS_LINE } from "@/lib/policy-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApps: vi.fn(),
  listBindings: vi.fn(),
  listTools: vi.fn(),
  listUsers: vi.fn(),
  simulate: vi.fn(),
}));
vi.mock("@/lib/public", () => ({ version: vi.fn() }));

// The eval deployment: three servers, of which dev-tools reaches two, and
// one person who holds the role.
const apps: AppRow[] = [
  { id: "a-mid", name: "midpoint", runtime: "remote", status: "healthy", reached_by: ["dev-tools"] },
  { id: "a-demo", name: "demo-tools", runtime: "command", status: "healthy", reached_by: ["dev-tools"] },
  { id: "a-git", name: "github", runtime: "remote", status: "healthy", reached_by: [] },
];

const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "a-demo", name: "get-sum" },
  { id: "t2", app: "demo-tools", app_id: "a-demo", name: "get-env" },
  { id: "t3", app: "midpoint", app_id: "a-mid", name: "search_users" },
];

const bindings: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b2", app: "midpoint", role: "dev-tools", tools: ["search_users"] },
];

const joe: UserRow = {
  id: "u-joe", username: "joe", status: "active", origin: "scim", kind: "user",
  created_at: "2026-09-01T08:00:00Z", updated_at: "2026-09-01T08:00:00Z",
  effective_roles: ["dev", "dev-tools"], locks: [], sponsored_count: 0,
};

const DRAFT = { name: "dev-guardrails", yaml: "apiVersion: straza.dev/v1beta1\nkind: PolicySet\n" };

const held = { effect: "allow", ruleId: "dev-mcp-env-ticket", setName: "dev-guardrails", reason: "reading the environment needs a ticket", approve: { class: "ticket", deciders: ["sponsor"] } };

const answers = (a: Partial<SimulateAnswer>) =>
  vi.mocked(simulate).mockResolvedValue({ active: { effect: "allow" }, subject: {}, snapshot: "7c1e9a2b4f0d", ...a } as SimulateAnswer);

const mount = (props: { draft?: { name: string; yaml: string } | null; prefill?: PolicyTestPrefill } = {}) =>
  render(<TooltipProvider><PolicyTest open onOpenChange={() => {}} {...props} /></TooltipProvider>);

const lastBody = () => vi.mocked(simulate).mock.calls[vi.mocked(simulate).mock.calls.length - 1][0];
const lane = (label: string) => userEvent.click(screen.getByRole("button", { name: label }));
const test = () => userEvent.click(screen.getByRole("button", { name: "Test" }));
const card = (mark: string) => document.querySelector("[data-why-card=\"" + mark + "\"]") as HTMLElement;

// openFields opens one card's Engine fields fold, which is closed on every
// answer and keeps the wire line out of the document until it is opened.
const openFields = (mark: string) => userEvent.click(within(card(mark)).getByRole("button", { name: ENGINE_FIELDS }));

// pick names the person the call is made by, through the directory search
// the picker runs.
async function pickWho(username = "joe") {
  await userEvent.type(screen.getByRole("combobox", { name: "Who" }), username);
  await userEvent.click(await screen.findByRole("option", { name: new RegExp(username) }));
}

// choose drives one of the two What selects.
async function choose(label: string, option: string) {
  await userEvent.click(screen.getByRole("combobox", { name: label }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

describe("the Test a call sheet", () => {
  beforeEach(() => {
    vi.mocked(listApps).mockReset().mockResolvedValue(apps);
    vi.mocked(listTools).mockReset().mockResolvedValue(tools);
    vi.mocked(listBindings).mockReset().mockResolvedValue(bindings);
    vi.mocked(listUsers).mockReset().mockResolvedValue({ items: [joe], next_cursor: "" });
    vi.mocked(simulate).mockReset();
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "enterprise" });
  });

  it("opens on the MCP lane, and each other lane brings its own field", async () => {
    mount();
    expect(screen.getByRole("heading", { name: "Test a call" })).toBeTruthy();
    expect(screen.getByText("Answers for now, against the policy version live right now. It writes nothing and records nothing.")).toBeTruthy();
    const lanes = within(document.querySelector("[data-lanes]") as HTMLElement).getAllByRole("button");
    expect(lanes.map((b) => [b.textContent, b.getAttribute("aria-pressed")]))
      .toEqual([["MCP tool", "true"], ["Shell command", "false"], ["File path", "false"], ["Network", "false"]]);
    expect(await screen.findByRole("combobox", { name: "Server" })).toBeTruthy();

    await lane("Shell command");
    expect(screen.getByRole("textbox", { name: "The command line" })).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: "Server" })).toBeNull();

    await lane("File path");
    expect(screen.getByRole("textbox", { name: "The path" })).toBeTruthy();
    expect(screen.getByText("Tested as a file write.")).toBeTruthy();

    await lane("Network");
    expect(screen.getByText("Every network fetch: the rule matches the lane, not a host.")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "The path" })).toBeNull();
  });

  it("names what is missing instead of testing an empty question", async () => {
    mount();
    await test();
    expect(screen.getByText(TEST_MISSING_WHO)).toBeTruthy();
    expect(screen.getByText(TEST_MISSING_WHAT)).toBeTruthy();
    expect(simulate).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Test" }).hasAttribute("disabled")).toBe(false);
  });

  it("posts the picked tool call, with the person resolved to their id", async () => {
    answers({ active: { effect: "deny", ruleId: "no-env", setName: "dev-guardrails" }, subject: { user: "u-joe", roles: ["dev", "dev-tools"] } });
    mount();
    await pickWho();
    await choose("Server", "demo-tools");
    await choose("Tool", "get-env");
    await test();
    await screen.findByRole("status");
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "mcp.call", app: "demo-tools", toolName: "get-env" }, subject: { user: "u-joe" } });
    expect("draft" in lastBody()).toBe(false);
    expect(screen.getByText("joe holds dev and dev-tools.")).toBeTruthy();
    expect(card("live").textContent).toContain("Denied");
    expect(card("live").textContent).toContain("Decided under the policy version live right now.");
    await openFields("live");
    expect(card("live").textContent).toContain("effect=deny · ruleId=no-env · setName=dev-guardrails · snapshot=7c1e9a2b (live)");
  });

  it("posts a shell command and a file write on their own lanes", async () => {
    answers({});
    mount();
    await pickWho();
    await lane("Shell command");
    await userEvent.type(screen.getByRole("textbox", { name: "The command line" }), "rm -rf ./build");
    await test();
    await screen.findByRole("status");
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "shell.exec", command: "rm -rf ./build" }, subject: { user: "u-joe" } });

    await lane("File path");
    await userEvent.type(screen.getByRole("textbox", { name: "The path" }), "deploy/.env");
    await test();
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "file.write", paths: ["deploy/.env"] }, subject: { user: "u-joe" } });

    await lane("Network");
    await test();
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "net.fetch" }, subject: { user: "u-joe" } });
  });

  it("sends the page's unpublished text and answers twice", async () => {
    answers({ active: held, draft: { effect: "allow", ruleId: "dev-mcp-env-ticket", setName: "dev-guardrails" }, subject: { user: "u-joe" } });
    mount({ draft: DRAFT });
    expect(screen.getByText("Answers for now, against the policy version live right now and the edits on this page. It writes nothing and records nothing.")).toBeTruthy();
    await pickWho();
    await choose("Server", "demo-tools");
    await choose("Tool", "get-env");
    await test();
    await screen.findAllByRole("status");
    expect(lastBody().draft).toBe(DRAFT.yaml);
    expect(within(card("live")).getByText(LIVE_NOW)).toBeTruthy();
    expect(card("live").textContent).toContain("Needs approval");
    expect(card("live").textContent).toContain("The person behind the agent decides.");
    await openFields("live");
    expect(card("live").textContent).toContain("effect=allow · mode=approve · class=ticket · ruleId=dev-mcp-env-ticket · setName=dev-guardrails · snapshot=7c1e9a2b (live)");
    expect(within(card("draft")).getByText(WITH_EDITS)).toBeTruthy();
    expect(card("draft").textContent).toContain("Allowed");
    expect(card("draft").textContent).toContain(WITH_EDITS_LINE);
    expect(card("draft").textContent).not.toContain("snapshot=");
    await openFields("draft");
    expect(card("draft").textContent).toContain("snapshot=(draft)");
  });

  it("narrows the servers to the ones the person's roles reach", async () => {
    mount();
    await screen.findByRole("combobox", { name: "Server" });
    expect(screen.getByText(TEST_EVERY_SERVER)).toBeTruthy();
    await pickWho();
    expect(await screen.findByText(TEST_SERVERS_HINT)).toBeTruthy();
    await userEvent.click(screen.getByRole("combobox", { name: "Server" }));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["demo-tools", "midpoint"]);
  });

  it("tests at once when it opens on a record's call", async () => {
    answers({ active: { effect: "deny", ruleId: "no-destructive-delete", setName: "dev-guardrails" }, subject: { user: "u-joe" } });
    mount({ prefill: { user: "joe", lane: "shell", command: "rm -rf ./build" } });
    await screen.findByRole("status");
    expect(lastBody()).toEqual({ event: { kind: "tool.pre", tool: "shell.exec", command: "rm -rf ./build" }, subject: { user: "u-joe" } });
    expect((screen.getByRole("textbox", { name: "The command line" }) as HTMLInputElement).value).toBe("rm -rf ./build");
    expect(screen.getByText("joe")).toBeTruthy();
    expect(card("live").textContent).toContain("Denied");
  });

  it("keeps the last answer on screen when the next test is refused", async () => {
    answers({ active: { effect: "allow", ruleId: "dev-reads", setName: "dev-guardrails" }, subject: { user: "u-joe" } });
    mount();
    await pickWho();
    await lane("Shell command");
    await userEvent.type(screen.getByRole("textbox", { name: "The command line" }), "ls");
    await test();
    await screen.findByRole("status");
    vi.mocked(simulate).mockRejectedValue(new ApiError("no such user", 404));
    await test();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Test a call refused.");
    expect(alert.textContent).toContain("The server refused it: no such user. Fix what it names, then try again.");
    expect(card("live").textContent).toContain("Decided by rule dev-reads in policy dev-guardrails.");
  });
});
