import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { NewRole } from "./new-role";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type Draft, type DraftItemIn, type DraftVerdict, type PackRow, type RoleRow, type ToolRow, activatePolicy, addImplication, applyPolicy, bindPack, catalogPreview, createBinding, createRole, deleteRole, exportRole, getPolicy, listApps, listPacks, listRoles, listTools } from "@/lib/api";
import { checkDraft, createDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { put } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { downloadText } from "@/lib/utils";
import { VERDICT } from "@/test/drafts-fixture";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listRoles: vi.fn(),
  listApps: vi.fn(),
  listTools: vi.fn(),
  listPacks: vi.fn(),
  catalogPreview: vi.fn(),
  createRole: vi.fn(),
  createBinding: vi.fn(),
  getPolicy: vi.fn(),
  applyPolicy: vi.fn(),
  activatePolicy: vi.fn(),
  addImplication: vi.fn(),
  bindPack: vi.fn(),
  exportRole: vi.fn(),
  deleteRole: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
// The role goes live through the drafts routes.
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/components/draft-publish", () => ({ DraftPublish: (p: DraftPublishProps) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} /> : null) }));
vi.mock("@/lib/utils", async (orig) => ({ ...(await orig<typeof import("@/lib/utils")>()), downloadText: vi.fn() }));
// The session's admin grants: full, so the editor asks what a call does.
vi.mock("@/lib/session", () => ({ snapshot: () => ({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: "s1" }) }));

const roles: RoleRow[] = [
  { id: "r-1", name: "dev-tools", kind: "application", description: "Tool reach for the developer seat." },
  { id: "r-2", name: "dev", kind: "business", description: "The developer seat." },
  { id: "r-3", name: "sec-approvers", kind: "approver", holder_count: 4 },
  { id: "r-4", name: "straza-admin", kind: "straza" },
];

// The role Straza mints with an MCP server: a business role may compose it,
// so a team over several servers is one role.
const minted: RoleRow = { id: "r-5", name: "mcp-admin-finance-jira", kind: "straza", description: "Administers the MCP server finance/jira." };

const apps: AppRow[] = [
  { id: "app-1", name: "scout-tools", runtime: "remote", status: "running", reached_by: [] },
  { id: "app-2", name: "demo-tools", runtime: "remote", status: "running", reached_by: [] },
  { id: "app-3", name: "obsidian", runtime: "command", status: "stopped", reached_by: [] },
];

const tools: ToolRow[] = [
  { id: "t1", app: "scout-tools", app_id: "app-1", name: "search", description: "Full-text search over the scout index." },
  { id: "t2", app: "scout-tools", app_id: "app-1", name: "fetch", description: "Fetches one document by id." },
  { id: "t3", app: "scout-tools", app_id: "app-1", name: "summarize", description: "Summarizes a document." },
  { id: "t4", app: "demo-tools", app_id: "app-2", name: "get-sum", description: "Adds two numbers." },
];

const pack: PackRow = { id: "p-1", name: "onboarding", version: "3" };
const made: RoleRow = { id: "r-new", name: "scout-tools-role", kind: "application", server: "scout-tools" };

const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
// sent is what the check was sent: the items of the one draft.
const sent = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];
const kinds = () => sent().map((i) => i.kind + "/" + i.name + ":" + i.op);
const specOf = (i: number) => (parse(sent()[i].doc || "") as { spec: Record<string, unknown> }).spec;
// direct are the routes the wizard wrote through before drafts; a save
// through the drafts routes calls none of them.
const direct = () => [createRole, createBinding, applyPolicy, activatePolicy, addImplication];
// publish presses Save and publish once the roles list the wizard reads
// after the publish names the new role.
const publish = async () => {
  vi.mocked(listRoles).mockResolvedValue(roles.concat(made));
  await userEvent.click(screen.getByRole("button", { name: "Save and publish" }));
};

const foot = () => within(document.querySelector("[data-wizard-foot]") as HTMLElement);
const heading = (name: string) => screen.findByRole("heading", { level: 2, name });
const click = (name: string) => userEvent.click(screen.getByRole("button", { name }));
const steps = () =>
  within(screen.getByRole("list", { name: "wizard steps" }))
    .getAllByRole("listitem")
    .map((li) => (li.textContent || "").replace(/^[0-9]/, ""));
const rail = (server: string) => screen.getByRole("radio", { name: "server " + server });
const pick = (kind: string) => userEvent.click(screen.getByRole("radio", { name: "kind " + kind }));
const landed = () => Array.from(document.querySelectorAll("[data-landed] li")).map((li) => li.textContent || "");
// nameBox is the free name of a business or an approver role, suffixBox
// the word an application role's name takes after its server's prefix.
const nameBox = () => screen.getByRole("textbox", { name: "Name" });
const suffixBox = () => screen.getByRole("textbox", { name: "role name suffix" });
const textOf = (selector: string) => (document.querySelector(selector) as HTMLElement).textContent;

// open renders the wizard and waits for the kind cards, then for the
// servers the reads fill in under them.
async function open() {
  render(
    <TooltipProvider>
      <NewRole />
    </TooltipProvider>,
  );
  await screen.findByRole("radio", { name: "kind application" });
  await waitFor(() => expect(document.querySelector("[data-name-preview], [data-no-server]")).toBeTruthy());
}

// named types the word after the picked server's prefix on the first step,
// so the role is stored as scout-tools-role.
async function named(word = "role") {
  await open();
  await userEvent.type(suffixBox(), word);
}

// toAccess walks an application role to its Access step.
async function toAccess(word = "role") {
  await named(word);
  await click("Next");
  await heading("Access");
}

// tick puts one tool of the open server in the grant.
const tick = (tool: string) => userEvent.click(screen.getByRole("checkbox", { name: "tool " + tool }));

// hold asks for approval on one tool of the open server.
const hold = (tool: string) =>
  userEvent.click(within(document.querySelector('[data-tool="' + tool + '"]') as HTMLElement).getByRole("radio", { name: "require approval" }));

describe("the New role wizard", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(listApps).mockResolvedValue(apps);
    vi.mocked(listTools).mockResolvedValue(tools);
    vi.mocked(listPacks).mockResolvedValue([]);
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(createRole).mockResolvedValue(made);
    vi.mocked(createBinding).mockResolvedValue({ id: "b-1", app: "scout-tools", role: "scout-role", tools: ["search"] });
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    vi.mocked(applyPolicy).mockResolvedValue({ name: "scout-role-access", status: "draft" });
    vi.mocked(activatePolicy).mockResolvedValue({});
    vi.mocked(addImplication).mockResolvedValue({ role_id: "r-new", implies_role_id: "r-1" });
    vi.mocked(bindPack).mockResolvedValue({});
    vi.mocked(exportRole).mockResolvedValue("kind: Role\n");
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockImplementation(async (body) => ({ draft: draftOf(body.working ? "39" : "50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("draws each kind card with the kind's own glyph, hidden from assistive technology", async () => {
    await open();
    for (const kind of ["application", "business", "approver"]) {
      const glyph = screen.getByRole("radio", { name: "kind " + kind }).querySelector('[data-kind-glyph="' + kind + '"]') as HTMLElement;
      expect(glyph).toBeTruthy();
      expect(glyph.getAttribute("aria-hidden")).toBe("true");
    }
  });

  it("gives each kind its own steps and never offers a straza role", async () => {
    await open();
    expect(steps()).toEqual(["Kind, server and name", "Access", "Review", "Done"]);
    expect(screen.getByRole("heading", { level: 2, name: "Kind, server and name" })).toBeTruthy();
    expect(screen.getByText("Pick the kind, then the server, then the name. None of them can change later.")).toBeTruthy();
    expect(screen.getByRole("radio", { name: "kind application" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.queryByRole("radio", { name: "kind straza" })).toBeNull();
    expect(screen.getByText("Straza roles are not made here.")).toBeTruthy();

    await pick("business");
    expect(steps()).toEqual(["Name and kind", "Compose", "Review", "Done"]);
    expect(screen.getByText("Pick the kind, then the name. It cannot be renamed later.")).toBeTruthy();
    await pick("approver");
    expect(steps()).toEqual(["Name and kind", "Review", "Done"]);

    await userEvent.type(nameBox(), "sec-approvers-2");
    await click("Next");
    await heading("Review");
    expect(textOf("[data-approver-next]")).toBe("Name it in a policy's approve.roles; your identity manager assigns who holds it.");
  });

  it("stops on a free name the server already has and offers to open it", async () => {
    await open();
    await pick("business");
    await userEvent.type(nameBox(), "DEV-TOOLS");
    expect(textOf('[data-name-check="error"]')).toContain("A role named dev-tools already exists.");
    await click("Open it");
    await screen.findByRole("alertdialog");
    await click("Discard the answers");
    expect(navigate).toHaveBeenCalledWith("roles", ["r-1"]);
  });

  it("warns on a near free name, refuses the reserved ones and reads a free name as free", async () => {
    await open();
    await pick("business");
    await userEvent.type(nameBox(), "devtools");
    expect(textOf('[data-name-check="warn"]')).toContain("Close to existing: dev-tools");

    await userEvent.clear(nameBox());
    await userEvent.type(nameBox(), "straza-admin");
    expect(textOf('[data-name-check="error"]')).toContain("straza-admin is the root role");

    await userEvent.clear(nameBox());
    await userEvent.type(nameBox(), "straza-ops");
    expect(textOf('[data-name-check="error"]')).toContain("Names beginning with straza- are reserved");

    await userEvent.clear(nameBox());
    await userEvent.type(nameBox(), "scout-seat");
    expect(textOf('[data-name-check="free"]')).toBe("Name available.");
  });

  it("keeps Next clickable and names the answer it is missing, checking the stored name", async () => {
    vi.mocked(listRoles).mockResolvedValue(roles.concat({ id: "r-6", name: "scout-tools-readers", kind: "application", server: "scout-tools" }));
    await open();
    const next = foot().getByRole("button", { name: "Next" });
    expect(next.hasAttribute("disabled")).toBe(false);
    await userEvent.click(next);
    expect(screen.getByText("Name the role.")).toBeTruthy();
    expect(document.activeElement).toBe(suffixBox());
    expect(screen.getByRole("heading", { level: 2, name: "Kind, server and name" })).toBeTruthy();

    await userEvent.type(suffixBox(), "readers");
    expect(textOf('[data-name-check="error"]')).toContain("A role named scout-tools-readers already exists.");
    await userEvent.click(foot().getByRole("button", { name: "Next" }));
    expect(screen.getByText("Pick another name.")).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Kind, server and name" })).toBeTruthy();
  });

  it("rails every server on the first step, disables one with no tools and keeps each server's ticks", async () => {
    await open();
    expect(screen.getAllByRole("radio", { name: /^server / }).map((b) => b.getAttribute("aria-label"))).toEqual(["server scout-tools", "server demo-tools", "server obsidian"]);
    expect(rail("obsidian").getAttribute("aria-disabled")).toBe("true");
    expect(rail("obsidian").textContent).toContain("no tools known: the server is stopped");
    expect(rail("scout-tools").getAttribute("aria-checked")).toBe("true");
    expect(rail("scout-tools").textContent).toContain("0/3");
    expect(rail("demo-tools").textContent).toContain("1 tool");
    expect(document.querySelector("[data-switch-warning]")).toBeNull();

    await userEvent.type(suffixBox(), "role");
    await click("Next");
    await heading("Access");
    await tick("search");
    await click("Back");
    await heading("Kind, server and name");
    expect(rail("scout-tools").textContent).toContain("1/3");
    await userEvent.click(rail("demo-tools"));
    expect(rail("demo-tools").getAttribute("aria-checked")).toBe("true");
    expect(rail("demo-tools").textContent).toContain("0/1");
    // The other server's ticks are not shown as a count and not granted,
    // and the first step says so under the servers.
    expect(rail("scout-tools").textContent).toContain("3 tools");
    expect(textOf('[data-switch-warning="scout-tools"]')).toBe("An application role reaches one server: the 1 tool ticked on scout-tools is not given while demo-tools is picked.");
    expect(textOf("[data-name-prefix]")).toBe("demo-tools-");
    await userEvent.click(rail("scout-tools"));
    expect(document.querySelector("[data-switch-warning]")).toBeNull();
    await click("Next");
    await heading("Access");
    expect((screen.getByRole("checkbox", { name: "tool search" }) as HTMLInputElement).checked).toBe(true);
  });

  it("names the picked server on the Access step and lists no server there", async () => {
    await toAccess();
    expect(textOf("[data-access-server]")).toBe("scout-tools-role reaches scout-tools. To pick another server, go back to the first step.");
    expect(screen.getByText("Tick the tools, then pick what policy does on a call.")).toBeTruthy();
    expect(screen.queryByRole("radio", { name: /^server / })).toBeNull();
    expect(document.querySelector('[data-access-editor="scout-tools"]')).toBeTruthy();
  });

  it("stops Next on the Access step while no tool is given, and moves on once one is", async () => {
    await toAccess();
    await click("Next");
    expect(textOf("[data-access-miss]")).toBe("Tick at least one tool.");
    expect(screen.getByRole("heading", { level: 2, name: "Access" })).toBeTruthy();
    await userEvent.click(screen.getByRole("radio", { name: "Every tool, and tools added later" }));
    expect(document.querySelector("[data-access-miss]")).toBeNull();
    await click("Next");
    await heading("Review");
  });

  it("reviews and writes the picked server only, owned by it, whatever was ticked elsewhere", async () => {
    await toAccess();
    await tick("search");
    await tick("summarize");
    await click("Back");
    await heading("Kind, server and name");
    await userEvent.click(rail("demo-tools"));
    expect(textOf('[data-switch-warning="scout-tools"]')).toContain("the 2 tools ticked on scout-tools are not given while demo-tools is picked.");
    await click("Next");
    await heading("Access");
    await tick("get-sum");
    await click("Next");
    await heading("Review");

    expect(document.querySelectorAll("[data-server-band]").length).toBe(1);
    expect(textOf('[data-server-band="demo-tools"]')).toBe("demo-tools1 of 1 tools");
    expect(landed()).toEqual(["1Create demo-tools-role, owned by demo-tools", "2Write the new access row: every tool (1) (demo-tools)"]);
    await click("The same role as strazactl commands");
    expect(screen.getByText(/strazactl roles create/).textContent).toBe("strazactl roles create demo-tools-role --app demo-tools --tools get-sum");

    await publish();
    await heading("Done");
    await waitFor(() => expect(landed()).toEqual(["doneCreate demo-tools-role, owned by demo-tools", "doneWrite the new access row: every tool (1) (demo-tools)"]));
    expect(kinds()).toEqual(["Role/demo-tools-role:put"]);
    expect(specOf(0)).toEqual({ kind: "application", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["get-sum"] }] });
    for (const call of direct()) expect(call).not.toHaveBeenCalled();
  });

  it("asks the preview what the draft grant reaches, with the tools it would write", async () => {
    await toAccess();
    await tick("search");
    await waitFor(() => expect(catalogPreview).toHaveBeenCalledWith("scout-tools-role", "scout-tools", ["search"]));
  });

  it("says so on the first step when no server serves a tool, and stops Next there", async () => {
    vi.mocked(listTools).mockResolvedValue([]);
    await open();
    expect(textOf("[data-no-server]")).toContain("No MCP server is running with tools.");
    await userEvent.type(suffixBox(), "role");
    await click("Next");
    expect(textOf("[data-server-miss]")).toBe("Pick the server to give access to.");
    expect(screen.getByRole("heading", { level: 2, name: "Kind, server and name" })).toBeTruthy();
  });

  it("keeps a business role's tools out of the draft when the kind moved off Application role", async () => {
    await toAccess();
    await tick("search");
    await click("Back");
    await heading("Kind, server and name");
    await pick("business");
    await userEvent.clear(nameBox());
    await userEvent.type(nameBox(), "dev-seat");
    await click("Next");
    await heading("Compose");
    await click("Next");
    await heading("Review");
    expect(landed()).toEqual(["1Create the role dev-seat"]);
    await publish();
    await waitFor(() => expect(checkDraft).toHaveBeenCalled());
    expect(specOf(0)).toEqual({ kind: "business" });
  });

  it("composes the application roles and names the tool counts they bring", async () => {
    vi.mocked(catalogPreview).mockResolvedValue({
      entries: [
        { app: "demo-tools", tool: "get-sum", status: "visible", reason: "" },
        { app: "demo-tools", tool: "list-files", status: "approve_gated", reason: "" },
        { app: "midpoint", tool: "read-user", status: "visible", reason: "" },
      ],
    });
    await open();
    await pick("business");
    await userEvent.type(nameBox(), "dev-seat");
    await click("Next");
    await heading("Compose");
    expect(screen.getAllByRole("checkbox").map((b) => b.getAttribute("aria-label"))).toEqual(["role dev-tools"]);
    expect(screen.getByText("Nothing yet. Compose an application role and its servers appear here.")).toBeTruthy();

    await userEvent.click(screen.getByRole("checkbox", { name: "role dev-tools" }));
    await waitFor(() => expect(document.querySelector('[data-reach-pill="demo-tools"]')).toBeTruthy());
    expect(textOf('[data-reach-pill="demo-tools"]')).toContain("2 tools via dev-tools");
    expect(textOf('[data-reach-pill="midpoint"]')).toContain("1 tool via dev-tools");

    await click("Next");
    await heading("Review");
    expect(textOf("[data-composes]")).toBe("dev-tools");
  });

  it("offers the minted admin role of a server beside the application roles", async () => {
    vi.mocked(listRoles).mockResolvedValue(roles.concat(minted));
    await open();
    await pick("business");
    await userEvent.type(nameBox(), "jira-team");
    await click("Next");
    await heading("Compose");
    expect(screen.getAllByRole("checkbox").map((b) => b.getAttribute("aria-label"))).toEqual(["role dev-tools", "role mcp-admin-finance-jira"]);

    await userEvent.click(screen.getByRole("checkbox", { name: "role mcp-admin-finance-jira" }));
    // A role that carries no tool is never asked what its sessions reach, so
    // the step says nothing about a tool set it could not read.
    expect(catalogPreview).not.toHaveBeenCalled();
    await click("Next");
    await heading("Review");
    expect(textOf("[data-composes]")).toBe("mcp-admin-finance-jira");
  });

  it("has no Packs step without a pack", async () => {
    await toAccess();
    expect(steps()).toEqual(["Kind, server and name", "Access", "Review", "Done"]);
    expect(screen.queryByRole("switch", { name: /^pack / })).toBeNull();
  });

  it("adds the Packs step and a Packs row on Review when a pack exists", async () => {
    vi.mocked(listPacks).mockResolvedValue([pack]);
    await toAccess();
    await tick("search");
    await waitFor(() => expect(steps()).toEqual(["Kind, server and name", "Access", "Knowledge packs", "Review", "Done"]));
    await click("Next");
    await heading("Knowledge packs");
    await userEvent.click(screen.getByRole("switch", { name: "pack onboarding" }));
    expect(screen.getByText("version 3")).toBeTruthy();
    await click("Next");
    await heading("Review");
    expect(textOf("[data-packs-row]")).toBe("onboarding");
  });

  it("reviews the reach per server, the numbered rows and the one-line command", async () => {
    await toAccess();
    await tick("search");
    await tick("summarize");
    await hold("summarize");
    await click("Next");
    await heading("Review");

    expect(textOf('[data-server-band="scout-tools"]')).toBe("scout-tools2 of 3 tools");
    const row = (tool: string) => within(document.querySelector('[data-review-tool="' + tool + '"]') as HTMLElement).getAllByRole("cell").map((c) => c.textContent);
    expect(row("search")).toEqual(["search", "Full-text search over the scout index.", "allowed"]);
    expect(row("summarize")).toEqual(["summarize", "Summarizes a document.", "needs approval: hold, up to 2 minutes"]);
    expect(document.querySelector('[data-review-tool="fetch"]')).toBeNull();

    expect(landed()).toEqual([
      "1Create scout-tools-role, owned by scout-tools",
      "2Write the new access row: search and summarize (scout-tools)",
      "3Publish scout-tools-role-access: 1 tool is allowed. 1 needs approval by the person behind the agent within 2 minutes.",
    ]);

    expect(screen.queryByText(/strazactl roles create/)).toBeNull();
    await click("The same role as strazactl commands");
    const code = screen.getByText(/strazactl roles create/);
    expect(code.textContent).toBe(
      "strazactl roles create scout-tools-role --app scout-tools --tools search,summarize\nstrazactl policy apply -f scout-tools-role-access.yaml\nstrazactl policy activate scout-tools-role-access",
    );
    // The rules go live with the role, so the Review asks nothing about them.
    expect(screen.queryByRole("radiogroup", { name: "The rules" })).toBeNull();
    expect(foot().getAllByRole("button").map((b) => b.textContent)).toEqual(["Back", "Save draft", "Save and publish"]);
  });

  it("lists every tool under a glob, with no stand-in row, and quotes the star in the command", async () => {
    await toAccess();
    await userEvent.click(screen.getByRole("radio", { name: "Every tool, and tools added later" }));
    await click("Back");
    await heading("Kind, server and name");
    await userEvent.type(screen.getByRole("textbox", { name: "Description (optional)" }), "Scout seat.");
    await click("Next");
    await heading("Access");
    await click("Next");
    await heading("Review");
    expect(textOf('[data-server-band="scout-tools"]')).toBe("scout-toolsevery tool (3), and tools added later");
    expect(document.querySelector("[data-every]")).toBeNull();
    expect(document.querySelectorAll("[data-review-tool]").length).toBe(3);
    await click("The same role as strazactl commands");
    expect(screen.getByText(/strazactl roles create/).textContent).toBe("strazactl roles create scout-tools-role --app scout-tools --tools '*' --description \"Scout seat.\"");
  });

  // withPack walks an application role with summarize held for approval
  // to its Review, with the onboarding pack picked.
  const withPack = async () => {
    vi.mocked(listPacks).mockResolvedValue([pack]);
    await toAccess();
    await tick("summarize");
    await hold("summarize");
    await waitFor(() => expect(steps()).toContain("Knowledge packs"));
    await click("Next");
    await heading("Knowledge packs");
    await userEvent.click(screen.getByRole("switch", { name: "pack onboarding" }));
    await click("Next");
    await heading("Review");
  };

  it("publishes the role owned by its server, its row and its rules as one draft, then binds the packs, exports and opens the role", async () => {
    const order: string[] = [];
    vi.mocked(publishDraft).mockImplementation(async (id) => { order.push("publishDraft"); return { draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }; });
    vi.mocked(bindPack).mockImplementation(async () => { order.push("bindPack"); return {}; });
    await withPack();
    expect(textOf("[data-packs-after]")).toBe("A draft never binds a knowledge pack, so Save and publish binds these once the role is live. After Save draft, bind them on the role's page once the draft is published.");
    await publish();
    await heading("Done");

    await waitFor(() => expect(order).toEqual(["publishDraft", "bindPack"]));
    expect(kinds()).toEqual(["Role/scout-tools-role:put", "PolicySet/scout-tools-role-access:put"]);
    // The Role document names its server and no pack: a draft never
    // applies one.
    expect(specOf(0)).toEqual({ kind: "application", server: "scout-tools", bindings: [{ app: "scout-tools", tools: ["summarize"] }] });
    expect(sent()[1].doc).toContain("- id: scout-tools-approve");
    expect(createDraft).toHaveBeenCalledWith({ items: sent() });
    expect(bindPack).toHaveBeenCalledWith("p-1", "r-new");
    expect(vi.mocked(notify.undo).mock.calls[0].slice(0, 2)).toEqual(["scout-tools-role is live.", "Undo"]);
    for (const call of direct()) expect(call).not.toHaveBeenCalled();
    await waitFor(() => expect(landed()).toEqual([
      "doneCreate scout-tools-role, owned by scout-tools",
      "doneWrite the new access row: summarize (scout-tools)",
      "donePublish scout-tools-role-access: 1 needs approval by the person behind the agent within 2 minutes.",
      "doneBind pack onboarding",
    ]));
    expect(textOf("[data-live-role]")).toContain("scout-tools-role is live.");

    await click("Export role-scout-tools-role.yaml");
    await waitFor(() => expect(exportRole).toHaveBeenCalledWith("r-new"));
    expect(downloadText).toHaveBeenCalledWith("role-scout-tools-role.yaml", "kind: Role\n", "application/yaml");

    await click("Open scout-tools-role");
    expect(navigate).toHaveBeenCalledWith("roles", ["r-new"]);
  });

  it("adds the role to the working draft with Save draft, and leaves its packs for the role's page", async () => {
    await withPack();
    await click("Save draft");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: sent(), working: true });
    expect(kinds()).toEqual(["Role/scout-tools-role:put", "PolicySet/scout-tools-role-access:put"]);
    expect(publishDraft).not.toHaveBeenCalled();
    expect(bindPack).not.toHaveBeenCalled();
  });

  // A name that became a live role after the first step read the roles is
  // caught by the check's existed answer, since a draft put would change it.
  it.each(["Save draft", "Save and publish"])("stops %s in the first step's words when the check answers the role as live, and stores nothing", async (button) => {
    vi.mocked(checkDraft).mockResolvedValue({ items: [{ kind: "Role", name: "scout-tools-role", op: "put", doc: "", existed: true }], verdict: EMPTY });
    await toAccess();
    await tick("search");
    await click("Next");
    await heading("Review");
    await click(button);
    await waitFor(() => expect(document.querySelector('[data-save-note="refused"]')).toBeTruthy());
    expect(textOf('[data-save-note="refused"]')).toBe("Nothing was saved.A role named scout-tools-role already exists. Pick another name.");
    for (const fn of [createDraft, publishDraft]) expect(fn).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { level: 2, name: "Review" })).toBeTruthy();
  });

  it("opens on the first step with the server a door hands it picked", async () => {
    put("roles-new", { server: "demo-tools" });
    await open();
    expect(textOf("[data-from-server]")).toBe("Access opens on demo-tools, the server you just added.");
    expect(screen.getByRole("heading", { level: 2, name: "Kind, server and name" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: "kind application" }).getAttribute("aria-checked")).toBe("true");
    expect(rail("demo-tools").getAttribute("aria-checked")).toBe("true");
    expect(rail("scout-tools").getAttribute("aria-checked")).toBe("false");
    expect(textOf("[data-name-prefix]")).toBe("demo-tools-");
    await userEvent.type(suffixBox(), "sum");
    expect(textOf("[data-name-preview]")).toBe("Stored as demo-tools-sum, in midPoint as AR:demo-tools-sum.");
    await click("Next");
    await heading("Access");
    expect(textOf("[data-access-server]")).toBe("demo-tools-sum reaches demo-tools. To pick another server, go back to the first step.");
  });

  it("keeps the published role when a pack is refused, and says where to finish it", async () => {
    vi.mocked(listPacks).mockResolvedValue([pack]);
    vi.mocked(bindPack).mockRejectedValue(new ApiError("pack onboarding is retired", 409));

    await toAccess();
    await tick("search");
    await waitFor(() => expect(steps()).toContain("Knowledge packs"));
    await click("Next");
    await heading("Knowledge packs");
    await userEvent.click(screen.getByRole("switch", { name: "pack onboarding" }));
    await click("Next");
    await heading("Review");
    await publish();
    await heading("Done");

    await waitFor(() => expect(landed()).toEqual([
      "doneCreate scout-tools-role, owned by scout-tools",
      "doneWrite the new access row: search (scout-tools)",
      "failedBind pack onboarding",
    ]));
    expect(textOf("[data-half-landed]")).toBe("The role exists. 2 of 3 steps landed. Finish under Packs on the role's page.");
    expect(textOf("[data-refused-error]")).toContain("Binding the pack refused. The server refused it: pack onboarding is retired.");
    expect(deleteRole).not.toHaveBeenCalled();
    expect(foot().getByRole("button", { name: "Open scout-tools-role" })).toBeTruthy();
  });

  it("asks before discarding the answers and names what would be lost", async () => {
    await toAccess();
    await tick("search");
    await click("Cancel");
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Discard these answers?")).toBeTruthy();
    expect(within(dialog).getByText("Nothing has been created yet. Discarding loses the name scout-tools-role and the tools ticked on scout-tools.")).toBeTruthy();

    await click("Keep editing");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(navigate).not.toHaveBeenCalled();
    expect((screen.getByRole("checkbox", { name: "tool search" }) as HTMLInputElement).checked).toBe(true);

    await click("Cancel");
    await screen.findByRole("alertdialog");
    await click("Discard the answers");
    expect(navigate).toHaveBeenCalledWith("roles", []);
  });

  it("leaves without a question when nothing was answered", async () => {
    await open();
    await click("Cancel");
    expect(screen.queryByRole("alertdialog")).toBeNull();
    expect(navigate).toHaveBeenCalledWith("roles", []);
  });
});
