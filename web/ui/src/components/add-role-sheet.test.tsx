import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { AddRoleSheet } from "./add-role-sheet";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  ApiError,
  type AppRow,
  type BindingRow,
  type Draft,
  type DraftItemIn,
  type DraftVerdict,
  type RoleRow,
  type ToolRow,
  activatePolicy,
  applyPolicy,
  catalogPreview,
  createBinding,
  createRole,
  deactivatePolicy,
  deletePolicy,
  exportRole,
  getPolicy,
  listRoles,
  removeBinding,
} from "@/lib/api";
import { checkDraft, createDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { VERDICT } from "@/test/drafts-fixture";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  createRole: vi.fn(),
  createBinding: vi.fn(),
  removeBinding: vi.fn(),
  getPolicy: vi.fn(),
  catalogPreview: vi.fn(),
  listRoles: vi.fn(),
  applyPolicy: vi.fn(),
  activatePolicy: vi.fn(),
  deactivatePolicy: vi.fn(),
  deletePolicy: vi.fn(),
  exportRole: vi.fn(),
}));
// The saves go through the drafts routes, faked here.
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
// The publish dialog has its own suite: here it stands in with the title
// and the verdict it was handed.
vi.mock("@/components/draft-publish", () => ({
  DraftPublish: (p: DraftPublishProps) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} data-title={p.title} data-risks={(p.verdict.risks || []).length} /> : null),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
// The session's admin grants: full by default, none for a server admin,
// whose standing comes from the servers it administers.
const sess = vi.hoisted(() => ({ grants: "full" }));
vi.mock("@/lib/session", () => ({ snapshot: () => ({ user: "alice", roles: [], grants: sess.grants, expiresIn: 300, sessionID: "s1" }) }));

const app: AppRow = { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: [] };

const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "app-9", name: "echo", description: "Echoes the input back." },
  { id: "t2", app: "demo-tools", app_id: "app-9", name: "add", description: "Adds two numbers." },
  { id: "t3", app: "demo-tools", app_id: "app-9", name: "get-sum", description: "Sums a list of numbers." },
];

const readers: RoleRow = { id: "r-1", name: "demo-tools-readers", kind: "application", server: "demo-tools", tools: ["echo"], holder_count: 0 };
const binding: BindingRow = { id: "b-1", app: "demo-tools", role: "demo-tools-readers", tools: ["echo"] };

// OWN is the role's own set with the one rule the editor wrote: echo held
// for the person behind the agent.
const OWN = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: demo-tools-readers-access",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [demo-tools-readers]",
  "  rules:",
  "    - id: demo-tools-approve",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [echo]",
  "      effect: allow",
  "      mode: approve",
  "      approve:",
  "        deciders: [sponsor]",
  "        timeoutSeconds: 120",
  "",
].join("\n");

// EXPORT is the role's live export, the document every Role put of an edit
// starts from.
const EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: demo-tools-readers", "spec:", "    kind: application", "    description: Read-only tools.", "    server: demo-tools", "    bindings:", "        - app: demo-tools", "          tools:", "            - echo", ""].join("\n");
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
// sentItems is what the check was sent, the one body every save starts with.
const sentItems = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];
const kinds = () => sentItems().map((i) => i.kind + "/" + i.name + ":" + i.op);
const specOf = (i: number) => parse(sentItems()[i].doc || "").spec;

const done = vi.fn();
const onOpenChange = vi.fn();

// mount opens the sheet as the server's admin unless globalAdmin says the
// session holds the apps grant.
function mount(role: RoleRow | null = null, row: BindingRow | null = null, roles: RoleRow[] = [], globalAdmin = false) {
  return render(
    <TooltipProvider>
      <AddRoleSheet app={app} tools={tools} role={role} roles={roles} binding={row} globalAdmin={globalAdmin} open onOpenChange={onOpenChange} onDone={done} />
    </TooltipProvider>,
  );
}

const preview = () => document.querySelector("[data-name-preview]") as HTMLElement;
const tick = (tool: string) => screen.getByRole("checkbox", { name: "tool " + tool });
const toolRow = (tool: string) => document.querySelector('[data-tool="' + tool + '"]') as HTMLElement;
const editorOpen = () => waitFor(() => expect(document.querySelector("[data-access-editor]")).toBeTruthy());
const press = (name: string) => userEvent.click(screen.getByRole("button", { name }));
const saveNote = () => document.querySelector('[data-save-note="refused"], [data-save-note="waits"], [data-save-note="unreachable"]') as HTMLElement | null;
const direct = [createRole, createBinding, removeBinding, applyPolicy, activatePolicy, deactivatePolicy, deletePolicy];

// readersWithEcho types the suffix readers and ticks echo, the smallest new
// role the sheet saves.
const readersWithEcho = async () => {
  await userEvent.type(screen.getByLabelText("role name suffix"), "readers");
  await userEvent.click(tick("echo"));
};

describe("the Add role sheet of a server", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sess.grants = "full";
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(listRoles).mockResolvedValue([]);
    vi.mocked(exportRole).mockResolvedValue(EXPORT);
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockImplementation(async (body) => ({ draft: draftOf(body.working ? "39" : "50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("fixes the server's prefix and previews the stored name as the suffix is typed", async () => {
    mount();
    expect(screen.getByText("Add a role of demo-tools")).toBeTruthy();
    expect(screen.getByText("It reaches this server only. Your identity manager decides who holds it.")).toBeTruthy();
    expect(preview().textContent).toBe("Stored as demo-tools-, in midPoint as AR:demo-tools-.strazactl roles create demo-tools- --app demo-tools --tools …");
    await userEvent.type(screen.getByLabelText("role name suffix"), "Read Only");
    expect(preview().textContent).toBe("Stored as demo-tools-read-only, in midPoint as AR:demo-tools-read-only.strazactl roles create demo-tools-read-only --app demo-tools --tools …");
    await userEvent.click(tick("echo"));
    expect(preview().textContent).toContain("strazactl roles create demo-tools-read-only --app demo-tools --tools echo");
  });

  it("ends in Cancel, Save draft and Save and publish, and none of the stepwise words", () => {
    mount();
    const foot = document.querySelector('[data-slot="sheet-footer"]') as HTMLElement;
    expect(within(foot).getAllByRole("button").map((b) => b.textContent)).toEqual(["Cancel", "Save draft", "Save and publish"]);
    for (const old of ["Create role", "Save access", "Keep it as a draft", "Creates the role"]) expect(document.body.textContent).not.toContain(old);
  });

  it("says what is missing at the field when a save has no name or no tool, and sends nothing", async () => {
    mount();
    await press("Save and publish");
    expect(screen.getByText("Name the role.")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText("role name suffix"));
    await userEvent.type(screen.getByLabelText("role name suffix"), "readers");
    expect(screen.queryByText("Name the role.")).toBeNull();
    await press("Save draft");
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Tick at least one tool.");
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("publishes a new role as one Role document naming this server, its tools and description, and no set when no rule is asked", async () => {
    mount();
    await userEvent.type(screen.getByLabelText("role name suffix"), "readers");
    await userEvent.type(screen.getByLabelText("Description"), "Read-only tools of demo-tools for analysts.");
    await userEvent.click(tick("echo"));
    await userEvent.click(tick("add"));
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(kinds()).toEqual(["Role/demo-tools-readers:put"]);
    expect(specOf(0)).toEqual({ kind: "application", description: "Read-only tools of demo-tools for analysts.", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["add", "echo"] }] });
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems() });
    expect(vi.mocked(notify.undo).mock.calls[0].slice(0, 2)).toEqual(["demo-tools-readers is live.", "Undo"]);
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(done).toHaveBeenCalledTimes(1);
    expect(exportRole).not.toHaveBeenCalled();
    for (const fn of direct) expect(fn).not.toHaveBeenCalled();
  });

  it("names the tools for a server admin and never offers the glob", async () => {
    mount();
    const later = screen.getByRole("radio", { name: "Every tool, and tools added later" });
    expect(later.getAttribute("aria-disabled")).toBe("true");
    expect(later.textContent).toContain("Only a global admin turns tools added later on or off. Pick Every tool it has today");
    await userEvent.click(screen.getByRole("radio", { name: "Every tool it has today" }));
    expect(preview().textContent).toContain("--tools add,echo,get-sum");
  });

  it("offers a global admin every tool, and tools added later, and sends the glob with the server", async () => {
    mount(null, null, [], true);
    const later = screen.getByRole("radio", { name: "Every tool, and tools added later" });
    expect(later.getAttribute("aria-disabled")).toBeNull();
    await userEvent.click(later);
    await userEvent.type(screen.getByLabelText("role name suffix"), "all");
    expect(preview().textContent).toContain("strazactl roles create demo-tools-all --app demo-tools --tools '*'");
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(kinds()).toEqual(["Role/demo-tools-all:put"]);
    expect(specOf(0)).toEqual({ kind: "application", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["*"] }] });
  });

  // demo-tools-all is a role a global admin gave every tool, and tools
  // added later, which nobody holds.
  const all: RoleRow = { ...readers, id: "r-2", name: "demo-tools-all", tools: ["*"] };
  const allRow: BindingRow = { id: "b-2", app: "demo-tools", role: "demo-tools-all", tools: ["*"] };
  const ALL_EXPORT = EXPORT.replace("name: demo-tools-readers", "name: demo-tools-all").replace("            - echo", "            - '*'");

  it.each([["who publishes policy", "full"], ["who reads policy only", ""]])("shows a server admin %s the glob on and greyed, sends nothing unchanged, and narrows it to names", async (_case, grants) => {
    sess.grants = grants;
    vi.mocked(exportRole).mockResolvedValue(ALL_EXPORT);
    mount(all, allRow);
    await editorOpen();
    const later = screen.getByRole("radio", { name: "Every tool, and tools added later" });
    expect(later.getAttribute("aria-checked")).toBe("true");
    expect(later.getAttribute("aria-disabled")).toBe("true");
    expect(later.textContent).toContain("Only a global admin turns tools added later on or off. Tick tools below to narrow this role to them.");
    await press("Save and publish");
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Nothing changed yet.");
    expect(checkDraft).not.toHaveBeenCalled();

    await userEvent.click(tick("echo"));
    expect(preview().textContent).toContain("--tools add,get-sum");
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(kinds()).toEqual(["Role/demo-tools-all:put"]);
    expect(specOf(0).bindings).toEqual([{ app: "demo-tools", tools: ["add", "get-sum"] }]);
  });

  it("keeps the glob live for a global admin editing a role that has it", async () => {
    mount(all, allRow, [], true);
    await editorOpen();
    const later = screen.getByRole("radio", { name: "Every tool, and tools added later" });
    expect(later.getAttribute("aria-checked")).toBe("true");
    expect(later.getAttribute("aria-disabled")).toBeNull();
    expect(document.body.textContent).not.toContain("Only a global admin");
  });

  it("filters the tools to the ones the filter names", async () => {
    mount();
    await userEvent.type(screen.getByLabelText("Filter tools"), "sum");
    const rows = document.querySelectorAll("[data-tool]");
    expect([...rows].map((r) => r.getAttribute("data-tool"))).toEqual(["get-sum"]);
  });

  it("puts a new role and its own set in one draft when a call requires approval", async () => {
    mount();
    await readersWithEcho();
    await userEvent.click(within(toolRow("echo")).getByRole("radio", { name: "require approval" }));
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(kinds()).toEqual(["Role/demo-tools-readers:put", "PolicySet/demo-tools-readers-access:put"]);
    expect(specOf(0).server).toBe("demo-tools");
    expect(sentItems()[1].doc).toContain("allow: [echo]");
    for (const fn of direct) expect(fn).not.toHaveBeenCalled();
  });

  it("adds a new role to the working draft with Save draft and opens it", async () => {
    mount();
    await readersWithEcho();
    await press("Save draft");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems(), working: true });
    expect(notify.ok).toHaveBeenCalledWith("Saved to your draft 39. Nothing changes until you publish it under Drafts.");
    expect(publishDraft).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
  });

  it("edits the tools with the name fixed, sending the live export with the one row changed", async () => {
    mount(readers, binding);
    expect(screen.getByText("Edit the tools of demo-tools-readers")).toBeTruthy();
    expect(screen.getByText("The name cannot change. Its tools can, until someone holds it.")).toBeTruthy();
    expect(screen.queryByLabelText("role name suffix")).toBeNull();
    await editorOpen();
    expect((tick("echo") as HTMLInputElement).checked).toBe(true);
    await userEvent.click(tick("get-sum"));
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(exportRole).toHaveBeenCalledWith("r-1");
    expect(kinds()).toEqual(["Role/demo-tools-readers:put"]);
    expect(specOf(0)).toEqual({ kind: "application", description: "Read-only tools.", server: "demo-tools", bindings: [{ app: "demo-tools", tools: ["echo", "get-sum"] }] });
    expect(vi.mocked(notify.undo).mock.calls[0][0]).toBe("demo-tools-readers reaches demo-tools now.");
    expect(done).toHaveBeenCalledTimes(1);
    for (const fn of direct) expect(fn).not.toHaveBeenCalled();
  });

  it("says nothing changed when an edit would write neither the row nor a rule", async () => {
    mount(readers, binding);
    await editorOpen();
    await press("Save and publish");
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Nothing changed yet.");
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("closes on Cancel at once while an edit changes nothing", async () => {
    mount(readers, binding);
    await editorOpen();
    await press("Cancel");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("asks before Cancel drops a new role nobody saved, and keeps the sheet on Keep editing", async () => {
    mount();
    await readersWithEcho();
    await press("Cancel");
    let ask = await screen.findByRole("alertdialog");
    expect(within(ask).getByRole("heading", { name: "Close without saving?" })).toBeTruthy();
    expect(ask.textContent).toContain("The changes in this sheet are not stored anywhere. Save draft or Save and publish keeps them, and closing drops them.");
    await userEvent.click(within(ask).getByRole("button", { name: "Keep editing" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(onOpenChange).not.toHaveBeenCalled();
    await press("Cancel");
    ask = await screen.findByRole("alertdialog");
    await userEvent.click(within(ask).getByRole("button", { name: "Close and drop the changes" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("closes on Cancel without asking once a draft holds the change", async () => {
    const error = "You cannot publish draft 50: the role demo-tools-readers has 1 holder, so its access row changes only by the scope apps:write or the role straza-global-mcp-admin.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(error, 403, false, 0, { error }));
    mount(readers, binding);
    await editorOpen();
    await userEvent.click(tick("add"));
    await press("Save and publish");
    await waitFor(() => expect(saveNote()).not.toBeNull());
    await press("Cancel");
    expect(onOpenChange).toHaveBeenCalledWith(false);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("drops the note that names a draft on the next edit, and asks again on Cancel", async () => {
    const error = "You cannot publish draft 50: the role demo-tools-readers has 1 holder, so its access row changes only by the scope apps:write or the role straza-global-mcp-admin.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(error, 403, false, 0, { error }));
    mount(readers, binding);
    await editorOpen();
    await userEvent.click(tick("add"));
    await press("Save and publish");
    await waitFor(() => expect(saveNote()).not.toBeNull());
    await userEvent.click(tick("get-sum"));
    await waitFor(() => expect(saveNote()).toBeNull());
    await press("Cancel");
    expect(within(await screen.findByRole("alertdialog")).getByRole("heading", { name: "Close without saving?" })).toBeTruthy();
    expect(onOpenChange).not.toHaveBeenCalled();
  });

  // A draft put of a live role's name changes that role, and the check
  // refuses nothing for it, so the sheet refuses the name itself: at the
  // field from the tab's roles, and from the check's existed answer for a
  // role the tab had not read.
  it("refuses a name another role of this server holds at the field, in New role's words, and sends nothing", async () => {
    mount(null, null, [readers]);
    await readersWithEcho();
    expect((document.querySelector('[data-name-check="error"]') as HTMLElement).textContent).toBe("A role named demo-tools-readers already exists.");
    await press("Save and publish");
    expect(screen.getByText("Pick another name.")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText("role name suffix"));
    await press("Save draft");
    expect(checkDraft).not.toHaveBeenCalled();
    await userEvent.type(screen.getByLabelText("role name suffix"), "-2");
    expect(screen.queryByText("Pick another name.")).toBeNull();
    expect(document.querySelector("[data-name-check]")).toBeNull();
    for (const fn of [createDraft, publishDraft]) expect(fn).not.toHaveBeenCalled();
  });

  it.each(["Save draft", "Save and publish"])("refuses %s over the buttons when the check answers the name as a live role, and stores nothing", async (button) => {
    vi.mocked(checkDraft).mockResolvedValue({ items: [{ kind: "Role", name: "demo-tools-readers", op: "put", doc: "", existed: true }], verdict: EMPTY });
    mount();
    await readersWithEcho();
    await press(button);
    await waitFor(() => expect(saveNote()).not.toBeNull());
    expect(saveNote()!.getAttribute("data-save-note")).toBe("refused");
    expect(saveNote()!.closest('[data-slot="sheet-footer"]')).toBeTruthy();
    expect(saveNote()!.getAttribute("role")).toBe("alert");
    expect(saveNote()!.textContent).toBe("Nothing was saved.A role named demo-tools-readers already exists. Pick another name.");
    for (const fn of [createDraft, publishDraft]) expect(fn).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
  });

  it("says at open that the role's own set holds a saved edit nobody published", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-readers-access", status: "active", drift: true, yaml: OWN });
    mount(readers, binding);
    await editorOpen();
    expect((document.querySelector("[data-saved-edit]") as HTMLElement).textContent).toBe("demo-tools-readers-access has a saved edit that is not published. Publish or discard it under Drafts, then save again.");
    expect(document.querySelector("[data-saved-edit]")!.getAttribute("role")).toBe("alert");
  });

  it("says the draft waits for someone who may publish it when the server refuses this person's publish", async () => {
    const error = "You cannot publish draft 50: the role demo-tools-readers has 1 holder, so its access row changes only by the scope apps:write or the role straza-global-mcp-admin.";
    vi.mocked(publishDraft).mockRejectedValue(new ApiError(error, 403, false, 0, { error }));
    mount(readers, binding);
    await editorOpen();
    await userEvent.click(tick("add"));
    await press("Save and publish");
    await waitFor(() => expect(saveNote()).not.toBeNull());
    expect(saveNote()!.getAttribute("data-save-note")).toBe("waits");
    expect(saveNote()!.textContent).toBe(error + "Draft 50 holds the change and waits under Drafts for someone who may publish it.Open draft 50");
    expect(done).not.toHaveBeenCalled();
    await press("Open draft 50");
    expect(navigate).toHaveBeenCalledWith("drafts", ["50"]);
  });

  it("opens the one publish dialog on the verdict when the server finds a line to acknowledge", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draftOf("50", []), verdict: VERDICT });
    mount(readers, binding);
    await editorOpen();
    await userEvent.click(within(toolRow("echo")).getByRole("radio", { name: "require approval" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("The tools stay as they are, and saving changes only the rules.");
    await press("Save and publish");
    const dialog = await waitFor(() => document.querySelector("[data-stub-publish]") as HTMLElement);
    expect(dialog.getAttribute("data-title")).toBe("Publish this change to demo-tools-readers?");
    expect(dialog.getAttribute("data-risks")).toBe("2");
    // The tools stay, so only the own set is sent and the export is not read.
    expect(kinds()).toEqual(["PolicySet/demo-tools-readers-access:put"]);
    expect(exportRole).not.toHaveBeenCalled();
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("asks before a publish removes the role's own set, and sends the removal only on the answer", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-readers-access", status: "active", yaml: OWN });
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [{ app: "demo-tools", tool: "echo", status: "approve_gated", reason: "", setName: "demo-tools-readers-access", ruleId: "demo-tools-approve" }] });
    mount(readers, binding);
    await editorOpen();
    expect(within(toolRow("echo")).getByRole("radio", { name: "require approval" }).getAttribute("aria-checked")).toBe("true");
    await userEvent.click(within(toolRow("echo")).getByRole("radio", { name: "allow" }));
    await press("Save and publish");
    let ask = await screen.findByRole("alertdialog");
    expect(within(ask).getByRole("heading", { name: "Remove demo-tools-readers-access with this change?" })).toBeTruthy();
    expect(ask.textContent).toContain("No rule is left in it, so publishing this change removes it, and calls demo-tools-readers makes to demo-tools then run with no approval or deny from it.");
    await userEvent.click(within(ask).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(checkDraft).not.toHaveBeenCalled();
    await press("Save and publish");
    ask = await screen.findByRole("alertdialog");
    await userEvent.click(within(ask).getByRole("button", { name: "Remove it" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(sentItems()).toEqual([{ kind: "PolicySet", name: "demo-tools-readers-access", op: "remove", doc: "" }]);
    for (const fn of direct) expect(fn).not.toHaveBeenCalled();
  });

  it("says a call is not readable here when a server admin can read neither the own set nor the preview", async () => {
    sess.grants = "";
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("the policy area is not in this session's grants", 403));
    vi.mocked(catalogPreview).mockRejectedValue(new ApiError("the catalog preview is not in this session's grants", 403));
    mount({ ...readers, tools: ["echo", "get-sum"] }, { ...binding, tools: ["echo", "get-sum"] });
    await editorOpen();
    expect((document.querySelector("[data-read-only]") as HTMLElement).textContent).toBe("You may change which tools demo-tools-readers reaches, but your session cannot read demo-tools-readers-access or the other policies that decide its calls, so the Policy column cannot say what a call does. An administrator who may read policies sees them under Policies.");
    expect((document.querySelector('[data-policy-word="get-sum"]') as HTMLElement).textContent).toBe("not readable here");
    expect((document.querySelector('[data-policy-word="echo"]') as HTMLElement).textContent).toBe("not readable here");
    expect(within(toolRow("add")).getAllByRole("cell")[3].textContent).toBe("not reached");
    expect(document.body.textContent).not.toContain("allowed");
  });

  it("says what is missing until the name has a suffix", async () => {
    mount();
    await userEvent.click(tick("echo"));
    const sentence = () => (document.querySelector("[data-grant-sentence]") as HTMLElement).textContent;
    expect(sentence()).toBe("Type the rest of the role's name above to see what its holders reach and the rules this writes.");
    await userEvent.click(within(toolRow("echo")).getByRole("radio", { name: "require approval" }));
    expect(screen.queryByRole("button", { name: /rules? this writes/ })).toBeNull();
    expect(document.body.textContent).not.toContain("demo-tools--access");
    await userEvent.type(screen.getByLabelText("role name suffix"), "readers");
    expect(sentence()).toMatch(/^Holders of demo-tools-readers reach 1 of 3 tools of demo-tools\./);
    expect(screen.getByRole("button", { name: "The rule this writes, in demo-tools-readers-access" })).toBeTruthy();
  });

  it("folds the rules as Edit tools would store them in the role's own set", async () => {
    vi.mocked(getPolicy).mockResolvedValue({ name: "demo-tools-readers-access", status: "active", yaml: OWN.replace("        timeoutSeconds: 120\n", "        timeoutSeconds: 120\n        binding: call\n") });
    mount({ ...readers, tools: ["echo", "get-sum"] }, { ...binding, tools: ["echo", "get-sum"] });
    await editorOpen();
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    await userEvent.click(screen.getByRole("button", { name: "The rule this writes, in demo-tools-readers-access" }));
    const fold = (document.querySelector("[data-access-editor] pre") as HTMLElement).textContent || "";
    expect(fold).toContain("- id: demo-tools-approve");
    expect(fold).toContain("allow: [echo, get-sum]");
    expect(fold).toContain("binding: call");
  });

  it("gives a server admin the tools and a read-only Policy column, and sends the role alone", async () => {
    sess.grants = "";
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("the policy area is not in this session's grants", 403));
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [{ app: "demo-tools", tool: "echo", status: "approve_gated", reason: "", setName: "demo-tools-readers-access", ruleId: "demo-tools-approve" }] });
    mount(readers, binding);
    await editorOpen();
    expect((document.querySelector("[data-read-only]") as HTMLElement).textContent).toBe("You may change which tools demo-tools-readers reaches, but your session cannot read demo-tools-readers-access, the role's own rules, so the Policy column cannot say what a call does. An administrator who may read policies sees them under Policies.");
    expect((document.querySelector('[data-fixed="echo"]') as HTMLElement).textContent).toBe("needs approval (demo-tools-readers-access)");
    expect(screen.queryByRole("radiogroup", { name: "On a call" })).toBeNull();
    expect(listRoles).not.toHaveBeenCalled();
    await userEvent.click(tick("add"));
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(kinds()).toEqual(["Role/demo-tools-readers:put"]);
    expect(specOf(0).bindings).toEqual([{ app: "demo-tools", tools: ["add", "echo"] }]);
    for (const fn of direct) expect(fn).not.toHaveBeenCalled();
  });
});
