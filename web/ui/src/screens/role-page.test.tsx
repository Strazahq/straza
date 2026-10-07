import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { RolePage } from "./role-page";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type BindingRow, type Draft, type DraftItemIn, type DraftVerdict, type PackRow, type PoliciesAnswer, type RoleRow, catalogPreview, deleteRole, exportRole, listApps, listBindings, listImplications, listPacks, listPolicies, listRoles, listTools, listUsers, updateRole } from "@/lib/api";
import { checkDraft, createDraft, getDraft, listDrafts, publishDraft, updateDraft } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { navigate, pathFor } from "@/lib/router";
import { ADMINISTERS_HINT, CANCEL, DELETE_ROLE, EDIT_ACCESS, EXPORT_YAML, LEVEL_WORD, NO_AREAS, NO_SERVER_ROLE, OPEN_SERVER, PRIMARY, PRODUCT_ROLE, TAB, deleteBody, deleteTitle, deletedToast, mintedRole, missingRole } from "@/lib/role-words";
import { ownedChip, ownedChipTitle } from "@/lib/server-roles-words";
import { VERDICT } from "@/test/drafts-fixture";

const devTools: RoleRow = { id: "r-dev-tools", name: "dev-tools", kind: "application", description: "Tool reach for the developer seat.", holder_count: 2, assigned_count: 0 };
const dev: RoleRow = { id: "r-dev", name: "dev", kind: "business", description: "Developer seat.", holder_count: 2, assigned_count: 2, implies: ["dev-tools"] };
const sec: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver", description: "Approval deciders.", holder_count: 2, assigned_count: 2, decider_in: ["dev-guardrails"] };
const admin: RoleRow = { id: "r-admin", name: "straza-admin", kind: "straza", description: "Straza administration", holder_count: 2, assigned_count: 2, areas: ["full"] };
// The role Straza mints with an MCP server, and the server that names it.
const jiraAdmin: RoleRow = { id: "r-m-jira", name: "mcp-admin-finance-jira", kind: "straza", description: "Administers the MCP server finance/jira.", holder_count: 1, assigned_count: 1, areas: [] };
const jira: AppRow = { id: "app-jira", name: "finance/jira", runtime: "remote", status: "running", reached_by: [], admin_role: jiraAdmin.name, admin_role_id: jiraAdmin.id };
// The role a server's admin defined: it reaches that server alone, so the
// page names the owner and edits its one row.
const owned: RoleRow = { id: "r-owned", name: "demo-tools-readers", kind: "application", description: "Read-only tools of demo-tools.", holder_count: 0, assigned_count: 0, server: "demo-tools", tools: ["echo", "add"] };
const demo: AppRow = { id: "app-demo", name: "demo-tools", runtime: "remote", status: "running", reached_by: [] };
// An application role that belongs to no server and has no access row: it
// reaches no server and nothing on its page gives it one.
const fresh: RoleRow = { id: "r-fresh", name: "qa-demo-tools", kind: "application", description: "QA seat on the sample server.", holder_count: 0, assigned_count: 0 };
// A role its server owns whose row is gone: its primary edits the access
// of that server.
const ownedEmpty: RoleRow = { id: "r-owned-empty", name: "demo-tools-writers", kind: "application", holder_count: 0, assigned_count: 0, server: "demo-tools" };
const roles = [devTools, dev, sec, admin, jiraAdmin, owned, fresh, ownedEmpty];

// The owner beside the kind badge is a link to the server's Roles tab when
// the servers list was read, and plain words when it was not.
const ownerCases: [string, AppRow[], string][] = [
  ["opens the server's Roles tab when the servers list was read", [demo], "app-demo"],
  ["stays plain words when the servers list was not read", [], ""],
];

// The primary of an application role reads Edit access. The last field is
// the server whose editor the click opens: a role its server owns opens on
// its one row, or on that server when the row is gone, and a role that
// belongs to no server opens on its row.
const primaryCases: [string, string, string, string][] = [
  ["opens the editor on its one row for a role its server owns", "r-owned", EDIT_ACCESS, "demo-tools"],
  ["opens the editor on the owning server for a role its server owns with no row", "r-owned-empty", EDIT_ACCESS, "demo-tools"],
  ["opens the row's editor on a role that reaches its server", "r-dev-tools", EDIT_ACCESS, "demo-tools"],
];

const bindings: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b3", app: "demo-tools", role: "demo-tools-readers", tools: ["echo", "add"] },
];
const sets = { items: [{ name: "dev-guardrails", status: "active", summary: { rules: 11, postures: { allow: 4, hold: 3 } } }] } as PoliciesAnswer;
const packs: PackRow[] = [{ id: "p1", name: "house-rules", version: "3", bindings: [{ id: "pb1", role_id: "r-dev-tools" }] }];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listRoles: vi.fn(),
  listBindings: vi.fn(),
  listTools: vi.fn(),
  listApps: vi.fn(),
  listImplications: vi.fn(),
  listPolicies: vi.fn(),
  listPacks: vi.fn(),
  listUsers: vi.fn(),
  catalogPreview: vi.fn(),
  updateRole: vi.fn(),
  deleteRole: vi.fn(),
  exportRole: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
// The description saves through the drafts routes.
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/components/draft-publish", () => ({
  DraftPublish: (p: DraftPublishProps) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id}><button type="button" onClick={() => p.onOpenChange(false)}>Stub cancel</button></div> : null),
}));
// The origin line reads the drafts list and has its own suite.
vi.mock("@/components/origin-line", () => ({ OriginLine: ({ object }: { object: string }) => <div data-origin-stub={object} /> }));

// EXPORT is dev-tools' live export, the document the description's Role
// put starts from.
const EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: dev-tools", "spec:", "    kind: application", "    description: Tool reach for the developer seat.", "    bindings:", "        - app: demo-tools", "          tools:", "            - '*'", "    packs:", "        - house-rules", ""].join("\n");
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
const sentItems = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];

const mount = (id: string, tab?: string) => render(<TooltipProvider><RolePage id={id} tab={tab} /></TooltipProvider>);
const tabs = () => screen.getAllByRole("tab").map((t) => (t.textContent || "").replace(/\s+/g, " ").trim());
// actions lists the head's worded buttons; the icon buttons beside the
// title and the description (the help icon, the pencil) carry no text.
const actions = () => Array.from(document.querySelectorAll("[data-page-head] button")).map((b) => (b.textContent || "").trim()).filter(Boolean);

describe("a role's page", () => {
  beforeEach(() => {
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(listBindings).mockResolvedValue(bindings);
    vi.mocked(listTools).mockResolvedValue([]);
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(listImplications).mockResolvedValue([]);
    vi.mocked(listPolicies).mockResolvedValue(sets);
    vi.mocked(listPacks).mockResolvedValue([]);
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(navigate).mockClear();
    vi.mocked(notify.ok).mockClear();
    vi.mocked(notify.undo).mockClear();
    vi.mocked(notify.warn).mockClear();
    vi.mocked(updateDraft).mockReset();
    vi.mocked(getDraft).mockReset();
    vi.mocked(exportRole).mockResolvedValue(EXPORT);
    vi.mocked(listDrafts).mockReset().mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockReset().mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockReset().mockImplementation(async (body) => ({ draft: draftOf(body.working ? "39" : "50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockReset().mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("heads the page with the name, the kind badge and the kind's own actions", async () => {
    mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    expect(screen.getByText("Tool reach for the developer seat.")).toBeTruthy();
    expect((document.querySelector("[data-kind]") as HTMLElement).textContent).toBe("application role");
    expect(screen.getByRole("button", { name: "Change the description" })).toBeTruthy();
    expect(actions()).toEqual([DELETE_ROLE, EXPORT_YAML, EDIT_ACCESS]);
  });

  it("puts the origin line of the role above its tabs", async () => {
    mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    expect(document.querySelector("[data-origin-stub]")?.getAttribute("data-origin-stub")).toBe("Role/dev-tools");
  });

  it("offers no delete on a role the product made, and says why", async () => {
    mount("r-admin");
    await screen.findByRole("heading", { level: 1, name: "straza-admin" });
    expect(actions()).toEqual([EXPORT_YAML, PRIMARY.straza]);
    // The reason sits behind the kind badge's help icon, beside the title.
    await userEvent.hover(screen.getByRole("button", { name: "Help: straza" }));
    expect((await screen.findAllByText(new RegExp(PRODUCT_ROLE.replace(".", "\\.")))).length).toBeGreaterThan(0);
  });

  it("gives each kind its own tabs, with the counts the reads answered", async () => {
    const want: [string, string, string[]][] = [
      ["r-dev-tools", "dev-tools", [TAB.access + " 1", TAB.holders + " 2", TAB.policies + " 1"]],
      ["r-dev", "dev", [TAB.composes + " 0", TAB.holders + " 2", TAB.policies + " 1"]],
      ["r-sec", "sec-approvers", [TAB.holders + " 2", TAB.policies + " 1"]],
      ["r-admin", "straza-admin", [TAB.areas + " 12", TAB.holders + " 2", TAB.policies + " 1"]],
    ];
    for (const [id, name, labels] of want) {
      const view = mount(id);
      await screen.findByRole("heading", { level: 1, name });
      expect(tabs()).toEqual(labels);
      view.unmount();
    }
    // An approver role counts its pools off the row, so it asks for no
    // policy list of its own.
    expect(vi.mocked(listPolicies).mock.calls.map((c) => c[0])).not.toContain("role=sec-approvers&limit=0");
  });

  it("gives a minted role an Administers tab of the servers that name it", async () => {
    vi.mocked(listApps).mockResolvedValue([jira, { id: "app-db", name: "finance/db", runtime: "remote", status: "running", reached_by: [], admin_role: "mcp-admin-finance-db", admin_role_id: "r-m-db" }]);
    mount("r-m-jira", "administers");
    await screen.findByRole("heading", { level: 1, name: "mcp-admin-finance-jira" });
    expect(tabs()).toEqual([TAB.areas + " 0", TAB.administers + " 1", TAB.holders + " 1", TAB.policies + " 1"]);
    const row = document.querySelector('[data-administers="finance/jira"]') as HTMLElement;
    expect(row.textContent).toBe("finance/jira");
    expect(screen.queryByText("finance/db")).toBeNull();
    expect(screen.getByText(ADMINISTERS_HINT)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: OPEN_SERVER }));
    expect(navigate).toHaveBeenCalledWith("servers", ["app-jira"]);
  });

  it("offers no delete on a minted role, and says the server owns it", async () => {
    vi.mocked(listApps).mockResolvedValue([jira]);
    mount("r-m-jira");
    await screen.findByRole("heading", { level: 1, name: "mcp-admin-finance-jira" });
    expect(actions()).toEqual([EXPORT_YAML, PRIMARY.straza]);
    await userEvent.hover(screen.getByRole("button", { name: "Help: straza" }));
    expect((await screen.findAllByText(new RegExp(mintedRole("finance/jira").replace(/[.]/g, "\\.")))).length).toBeGreaterThan(0);
  });

  it("leaves the Areas tab of a minted role reading no console area", async () => {
    vi.mocked(listApps).mockResolvedValue([jira]);
    mount("r-m-jira", "areas");
    await screen.findByRole("heading", { level: 1, name: "mcp-admin-finance-jira" });
    expect(screen.getByText(NO_AREAS)).toBeTruthy();
    const levels = Array.from(document.querySelectorAll("[data-area] [data-level]")).map((n) => n.textContent);
    expect(levels.length).toBe(12);
    expect(new Set(levels)).toEqual(new Set([LEVEL_WORD.none]));
  });

  it.each(ownerCases)("names the server that owns the role, and %s", async (_case, appRows, appId) => {
    vi.mocked(listApps).mockResolvedValue(appRows);
    mount("r-owned");
    await screen.findByRole("heading", { level: 1, name: "demo-tools-readers" });
    const chip = document.querySelector('[data-owned-by="demo-tools"]') as HTMLElement;
    expect(chip.textContent).toBe(ownedChip("demo-tools"));
    expect(chip.getAttribute("title")).toBe(ownedChipTitle("demo-tools"));
    expect(chip.tagName).toBe(appId ? "BUTTON" : "SPAN");
    await userEvent.click(chip);
    if (appId) expect(navigate).toHaveBeenCalledWith("servers", [appId, "roles"]);
    else expect(navigate).not.toHaveBeenCalled();
  });

  it.each(primaryCases)("%s", async (_case, id, word, sheet) => {
    vi.mocked(listApps).mockResolvedValue([demo]);
    mount(id);
    await screen.findByRole("heading", { level: 1 });
    // The row's pencil is named Edit access too, so the head is the scope.
    const primary = within(document.querySelector("[data-page-head]") as HTMLElement).getByRole("button", { name: word });
    expect(primary.getAttribute("aria-disabled")).toBeNull();
    expect(primary.getAttribute("title")).toBeNull();
    await userEvent.click(primary);
    await waitFor(() => expect(document.querySelector('[data-access-sheet="' + sheet + '"]')).toBeTruthy());
    expect(document.querySelector("[data-refused-error]")).toBeNull();
  });

  it("offers no access act on a role that belongs to no server and has no row, and says why", async () => {
    vi.mocked(listApps).mockResolvedValue([demo]);
    mount("r-fresh");
    await screen.findByRole("heading", { level: 1, name: "qa-demo-tools" });
    expect(actions()).toEqual([DELETE_ROLE, EXPORT_YAML]);
    expect((await screen.findByText(NO_SERVER_ROLE)).textContent).toBe("This role reaches no MCP server and is matched by policy rules only. A role for a server is made in New role or on the server's page.");
  });

  it("carries the tab in the address, and falls back to the first on an unknown one", async () => {
    const view = mount("r-dev-tools", "policies");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    expect(screen.getByRole("tab", { selected: true }).textContent).toContain(TAB.policies);
    expect(screen.queryByText(pathFor("roles", ["r-dev-tools", "policies"]))).toBeNull();
    await userEvent.click(screen.getByRole("tab", { name: /Holders/ }));
    expect(navigate).toHaveBeenCalledWith("roles", ["r-dev-tools", "holders"], true);
    view.unmount();

    mount("r-dev-tools", "nonsense");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    expect(screen.getByRole("tab", { selected: true }).textContent).toContain(TAB.access);
  });

  it("shows the Packs tab only where a pack exists", async () => {
    const view = mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    expect(tabs().some((t) => t.startsWith(TAB.packs))).toBe(false);
    view.unmount();

    vi.mocked(listPacks).mockResolvedValue(packs);
    mount("r-dev-tools", "packs");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    await waitFor(() => expect(tabs().some((t) => t === TAB.packs + " 1")).toBe(true));
    expect(await screen.findByText("house-rules")).toBeTruthy();
  });

  // openDescription opens the description dialog on dev-tools and types text.
  const openDescription = async (text: string) => {
    mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    await userEvent.click(screen.getByRole("button", { name: "Change the description" }));
    const box = await screen.findByRole("textbox");
    await userEvent.clear(box);
    if (text) await userEvent.type(box, text);
    return screen.getByRole("dialog");
  };

  it("publishes the description as the live Role document with that one change", async () => {
    const dialog = await openDescription("Reach for the developer seat.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalledWith("50", { revision: 1, risk_digest: "", ticked: [], typed: {} }));
    expect(exportRole).toHaveBeenCalledWith("r-dev-tools");
    expect(sentItems().map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/dev-tools:put"]);
    // Every other field goes back as the export has it, the pack included.
    expect(parse(sentItems()[0].doc || "").spec).toEqual({ kind: "application", description: "Reach for the developer seat.", bindings: [{ app: "demo-tools", tools: ["*"] }], packs: ["house-rules"] });
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems() });
    expect(vi.mocked(notify.undo).mock.calls[0].slice(0, 2)).toEqual(["dev-tools is live with your change.", "Undo"]);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(vi.mocked(listRoles).mock.calls.length).toBeGreaterThan(1));
    expect(updateRole).not.toHaveBeenCalled();
    expect(getDraft).not.toHaveBeenCalled();
  });

  it("adds the description to the working draft with Save draft and opens it", async () => {
    const dialog = await openDescription("Reach for the developer seat.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems(), working: true });
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("keeps a refused description in the dialog with the server's sentence, and stores nothing", async () => {
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: { ...EMPTY, refused: [{ code: "role.parse", class: "refused", object: "Role/dev-tools", key: "k1", sentence: "Role dev-tools does not read as a Role document: spec.description holds a tab.", fix: "Compare it with strazactl roles export dev-tools and send the draft again." }] } });
    const dialog = await openDescription("Reach.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(dialog.querySelector('[data-save-note="refused"]')).toBeTruthy());
    expect((dialog.querySelector('[data-save-note="refused"]') as HTMLElement).textContent).toBe("Nothing was saved.Role dev-tools does not read as a Role document: spec.description holds a tab. Compare it with strazactl roles export dev-tools and send the draft again.");
    expect(createDraft).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBe(dialog);
  });

  it("never revises a draft another role editor kept: Compose makes its own draft and names the kept one", async () => {
    const EXPORT_DEV = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: dev", "spec:", "    kind: business", "    description: Developer seat.", ""].join("\n");
    const WARN: DraftVerdict = { ...EMPTY, warnings: [{ code: "ready.nobody", class: "warning", object: "Role/dev", key: "w1", sentence: "Nobody holds dev." }] };
    vi.mocked(exportRole).mockResolvedValue(EXPORT_DEV);
    // The check answers each item as a draft read would, so the kept draft
    // reads back what the description saved into it.
    let kept: DraftItemIn[] = [];
    vi.mocked(checkDraft).mockImplementation(async (body) => ({ items: (body.items || []).map((i) => ({ ...i, existed: true })), verdict: EMPTY }));
    vi.mocked(createDraft).mockImplementationOnce(async (body) => { kept = body.items || []; return { draft: draftOf("60", kept), verdict: WARN }; })
      .mockImplementationOnce(async (body) => ({ draft: draftOf("61", body.items || []), verdict: EMPTY }));
    vi.mocked(listDrafts).mockImplementation(async (q: string) => (q.includes("mine=true")
      ? { items: [{ id: "60", title: "", state: "open", door: "console", revision: 1, items: kept.map((i) => ({ kind: i.kind, name: i.name, op: i.op })), created_at: "", updated_at: "" }], next_cursor: "" }
      : { items: [], next_cursor: "" }) as never);
    vi.mocked(getDraft).mockImplementation(async () => ({ draft: draftOf("60", kept), verdict: WARN, revisions: [], live: {}, may_publish: true }));

    mount("r-dev", "composes");
    await screen.findByRole("heading", { level: 1, name: "dev" });
    await userEvent.click(screen.getByRole("button", { name: "Change the description" }));
    const box = await screen.findByRole("textbox");
    await userEvent.clear(box);
    await userEvent.type(box, "Seat for the platform team.");
    await userEvent.click(screen.getByRole("button", { name: "Save and publish" }));
    await userEvent.click(await screen.findByRole("button", { name: "Stub cancel" }));
    await waitFor(() => expect(document.querySelector('[data-save-note="kept"]')).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("textbox")).toBeNull());

    await userEvent.click(within(document.querySelector("[data-page-head]") as HTMLElement).getByRole("button", { name: "Compose a role" }));
    await userEvent.click(await screen.findByRole("combobox", { name: "Pick a role to compose" }));
    await userEvent.click(await screen.findByRole("option", { name: "dev-tools" }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalledWith("61", expect.anything()));
    expect(updateDraft).not.toHaveBeenCalled();
    expect(parse(kept[0].doc || "").spec.description).toBe("Seat for the platform team.");
    expect(notify.warn).toHaveBeenCalledWith("Draft 60 still holds an earlier change to dev that this change leaves out. Publish or discard draft 60 under Drafts.");
  });

  it("says nothing changed when the description is the one live has, and sends nothing", async () => {
    const dialog = await openDescription("Tool reach for the developer seat.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Save draft" }));
    expect((dialog.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Nothing changed yet.");
    expect(checkDraft).not.toHaveBeenCalled();
    expect(exportRole).not.toHaveBeenCalled();
  });

  it("restates the role, its holders and its access rows before deleting it", async () => {
    vi.mocked(deleteRole).mockResolvedValue({ status: "deleted" });
    mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    await userEvent.click(screen.getByRole("button", { name: DELETE_ROLE }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(deleteTitle("dev-tools"))).toBeTruthy();
    expect(dialog.textContent).toContain(deleteBody(2, 1));
    expect(within(dialog).getByRole("button", { name: CANCEL })).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: DELETE_ROLE }));
    await waitFor(() => expect(deleteRole).toHaveBeenCalledWith("r-dev-tools"));
    expect(notify.ok).toHaveBeenCalledWith(deletedToast("dev-tools"));
    expect(navigate).toHaveBeenCalledWith("roles");
  });

  it("names in the toast the policy set the delete turned off", async () => {
    vi.mocked(deleteRole).mockResolvedValue({ status: "deleted", sets_off: ["dev-tools-access"] });
    mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    await userEvent.click(screen.getByRole("button", { name: DELETE_ROLE }));
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: DELETE_ROLE }));
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith("dev-tools is deleted, and its policy set dev-tools-access is turned off."));
  });

  it("keeps the delete dialog open on a refusal and quotes the server", async () => {
    vi.mocked(deleteRole).mockRejectedValue(new ApiError('role "sec-approvers" is a decider pool in an active policy (dev-guardrails)', 409));
    mount("r-sec");
    await screen.findByRole("heading", { level: 1, name: "sec-approvers" });
    await userEvent.click(screen.getByRole("button", { name: DELETE_ROLE }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: DELETE_ROLE }));
    const refusal = await within(dialog).findByText(/decider pool in an active policy/);
    expect(refusal.textContent).toContain('role "sec-approvers" is a decider pool in an active policy (dev-guardrails)');
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(navigate).not.toHaveBeenCalledWith("roles");
  });

  it("says so when the address names no role", async () => {
    mount("r-gone");
    expect(await screen.findByText(missingRole("r-gone"))).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open Roles" })).toBeTruthy();
  });

  it("keeps the page when a read beside the role fails, and names what failed", async () => {
    vi.mocked(listBindings).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount("r-dev-tools");
    await screen.findByRole("heading", { level: 1, name: "dev-tools" });
    const block = document.querySelector("[data-fetch-error]") as HTMLElement;
    expect(block.textContent).toContain("Access could not be read because strazad did not answer");
    expect(tabs()).toEqual([TAB.access + " 0", TAB.holders + " 2", TAB.policies + " 1"]);
  });
});
