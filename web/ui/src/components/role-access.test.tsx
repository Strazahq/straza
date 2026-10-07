import * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { RoleAccess } from "./role-access";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type BindingRow, type Draft, type DraftItemIn, type DraftVerdict, type RoleRow, type ToolRow, activatePolicy, applyPolicy, catalogPreview, createBinding, deactivatePolicy, deletePolicy, exportRole, getPolicy, listRoles, removeBinding } from "@/lib/api";
import { listAudit } from "@/lib/api";
import { checkDraft, createDraft, getDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { VERDICT } from "@/test/drafts-fixture";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  catalogPreview: vi.fn(),
  getPolicy: vi.fn(),
  listPolicies: vi.fn(),
  listRoles: vi.fn(),
  listAudit: vi.fn(),
  createBinding: vi.fn(),
  removeBinding: vi.fn(),
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
// The publish dialog is C1's and has its own suite: here it stands in with
// the title it was handed.
vi.mock("@/components/draft-publish", () => ({
  DraftPublish: (p: DraftPublishProps & { title?: string }) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} data-title={p.title} /> : null),
}));
// The session's admin grants: full by default, and the apps grants alone
// in the case that proves the Policy column is read-only without policy.
const sess = vi.hoisted(() => ({ grants: "full" }));
vi.mock("@/lib/session", () => ({ snapshot: () => ({ user: "alice", roles: [], grants: sess.grants, expiresIn: 300, sessionID: "s1" }) }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const role: RoleRow = { id: "r-1", name: "dev-tools", kind: "application" };

const apps: AppRow[] = [
  { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: ["dev-tools"] },
  { id: "app-2", name: "midpoint", runtime: "http", status: "running", reached_by: ["dev-tools"] },
  { id: "app-3", name: "scout-tools", runtime: "http", status: "stopped", reached_by: [] },
  { id: "app-4", name: "files", runtime: "http", status: "running", reached_by: [] },
];

const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "app-9", name: "get-sum", description: "Adds two numbers" },
  { id: "t2", app: "demo-tools", app_id: "app-9", name: "list-files", description: "Lists a directory" },
  { id: "t3", app: "demo-tools", app_id: "app-9", name: "get-env", description: "Reads an environment variable" },
  { id: "t4", app: "midpoint", app_id: "app-2", name: "read-user" },
  { id: "t5", app: "files", app_id: "app-4", name: "read-file", description: "Reads one file" },
];

const glob: BindingRow = { id: "b-1", app: "demo-tools", role: "dev-tools", tools: ["*"] };
const named: BindingRow = { id: "b-2", app: "midpoint", role: "dev-tools", tools: ["read-user"] };
const elsewhere: BindingRow = { id: "b-3", app: "demo-tools", role: "sre", tools: ["*"] };

const OWN_SET = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: dev-tools-access",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [dev-tools]",
  "  rules:",
  "    - id: demo-tools-get-env-deny",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        deny: [get-env]",
  "      effect: deny",
  "",
].join("\n");

// EXPORT is the role's live export, the document every Role put starts
// from.
const EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: dev-tools", "spec:", "    kind: application", "    description: Tool reach.", "    bindings:", "        - app: demo-tools", "          tools:", "            - '*'", ""].join("\n");
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
// sentItems is what the check was sent, the one body every save starts with.
const sentItems = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];
const kinds = () => sentItems().map((i) => i.kind + "/" + i.name + ":" + i.op);
const changed = vi.fn();

// filesReaders is a role the server files owns, with no row yet.
const filesReaders: RoleRow = { id: "r-3", name: "files-readers", kind: "application", server: "files" };
const FILES_EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: files-readers", "spec:", "    kind: application", "    server: files", ""].join("\n");

// Mounted is the role page around the tab: it owns the sheet's open state
// behind the page's primary and reloads its bindings after a landed write,
// as the role page does. globalAdmin is the session's apps grant.
function Mounted({ rows = [glob, named, elsewhere], after, toolRows = tools, who = role, globalAdmin = false }: { rows?: BindingRow[]; after?: BindingRow[]; toolRows?: ToolRow[]; who?: RoleRow; globalAdmin?: boolean }) {
  const [bindings, setBindings] = React.useState(rows);
  const [open, setOpen] = React.useState(false);
  return (
    <TooltipProvider>
      <button type="button" onClick={() => setOpen(true)}>the page's primary</button>
      <RoleAccess
        role={who}
        bindings={bindings}
        apps={apps}
        tools={toolRows}
        sheetOpen={open}
        onSheetOpenChange={setOpen}
        onChanged={() => { changed(); if (after) setBindings(after); }}
        globalAdmin={globalAdmin}
      />
    </TooltipProvider>
  );
}
const primary = () => userEvent.click(screen.getByRole("button", { name: "the page's primary" }));

const accessRow = (app: string) => document.querySelector('[data-access="' + app + '"]') as HTMLElement;
const cells = (app: string) => within(accessRow(app)).getAllByRole("cell").map((c) => c.textContent);
const openEdit = async (app: string) => {
  await userEvent.click(within(accessRow(app)).getByRole("button", { name: "Edit access" }));
  const sheet = await screen.findByRole("dialog");
  await waitFor(() => expect(sheet.querySelector("[data-access-editor]")).toBeTruthy());
  return sheet;
};
const toolRow = (tool: string) => document.querySelector('[data-tool="' + tool + '"]') as HTMLElement;
const card = (name: string) => screen.getByRole("radio", { name });

describe("the Access tab of a role", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sess.grants = "full";
    vi.mocked(catalogPreview).mockImplementation(async (_role: string, app = "") => {
      if (app === "midpoint") throw new ApiError("strazad did not answer", 0, true);
      return {
        entries: [
          { app, tool: "get-sum", status: "visible", reason: "" },
          { app, tool: "list-files", status: "approve_gated", reason: "", setName: "dev-guardrails" },
          { app, tool: "get-env", status: "hidden_policy", reason: "", setName: "dev-guardrails" },
        ],
      };
    });
    vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", yaml: OWN_SET });
    vi.mocked(listRoles).mockResolvedValue([role, { id: "r-9", name: "sec-approvers", kind: "approver", holder_count: 4 }]);
    vi.mocked(listAudit).mockResolvedValue([]);
    vi.mocked(createBinding).mockResolvedValue({ id: "b-4", app: "files", role: "dev-tools", tools: ["read-file"] });
    vi.mocked(removeBinding).mockResolvedValue({ status: "deleted" });
    vi.mocked(applyPolicy).mockResolvedValue({ name: "dev-tools-access", status: "draft" });
    vi.mocked(activatePolicy).mockResolvedValue({});
    vi.mocked(deactivatePolicy).mockResolvedValue({});
    vi.mocked(deletePolicy).mockResolvedValue({ status: "deleted" });
    vi.mocked(exportRole).mockResolvedValue(EXPORT);
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockImplementation(async (body) => ({ draft: draftOf(body.working ? "39" : "50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("lists one row per server of this role, in words, with what policy does", async () => {
    render(<Mounted />);
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Server", "Tools", "Policy", ""]);
    expect(document.querySelectorAll("[data-access]")).toHaveLength(2);
    expect(cells("demo-tools")[1]).toBe("every tool (3), and tools added later");
    expect(within(accessRow("demo-tools")).getAllByRole("cell")[1].getAttribute("title")).toBe("stored as *: every tool, including ones added later");
    expect(cells("midpoint")[1]).toBe("every tool (1)");
    await waitFor(() => expect(cells("demo-tools")[2]).toBe("1 allowed · 1 needs approval (dev-guardrails) · 1 denied (dev-guardrails)"));
    expect(cells("midpoint")[2]).toBe("could not be read");
  });

  it("says hold or ticket in the Policy cell, reading each set once", async () => {
    const approveRule = (id: string, tool: string, approve: string[]) =>
      ["    - id: " + id, "      tools: [mcp.call]", "      apps: [demo-tools]", "      toolNames:", "        allow: [" + tool + "]", "      effect: allow", "      mode: approve", "      approve:"].concat(approve.map((l) => "        " + l));
    const own = OWN_SET.trimEnd().split("\n").concat(approveRule("demo-tools-get-sum-approve", "get-sum", ["deciders: [sponsor]", "timeoutSeconds: 120"])).join("\n") + "\n";
    const guard = ["apiVersion: straza.dev/v1beta1", "kind: PolicySet", "metadata:", "  name: dev-guardrails", "spec:", "  rules:"]
      .concat(approveRule("list-files-ticket", "list-files", ["roles: [sec-approvers]", "class: ticket", "ticketTTLSeconds: 86400", "grantTTLSeconds: 3600"])).join("\n") + "\n";
    vi.mocked(getPolicy).mockImplementation(async (name: string) => ({ name, status: "active", yaml: name === "dev-guardrails" ? guard : own }));
    vi.mocked(catalogPreview).mockImplementation(async (_role: string, app = "") => {
      if (app === "midpoint") throw new ApiError("strazad did not answer", 0, true);
      return {
        entries: [
          { app, tool: "get-sum", status: "approve_gated", reason: "", setName: "dev-tools-access", ruleId: "demo-tools-get-sum-approve" },
          { app, tool: "list-files", status: "approve_gated", reason: "", setName: "dev-guardrails", ruleId: "list-files-ticket" },
          { app, tool: "get-env", status: "hidden_policy", reason: "", setName: "dev-guardrails" },
        ],
      };
    });
    render(<Mounted />);
    await waitFor(() => expect(cells("demo-tools")[2]).toBe("1 hold · 1 ticket (dev-tools-access, dev-guardrails) · 1 denied (dev-guardrails)"));
    const cell = accessRow("demo-tools").querySelector("[data-policy-cell]") as HTMLElement;
    expect(cell.getAttribute("title")).toContain("get-sum: hold, up to 2 minutes, the person behind the agent");
    expect(cell.getAttribute("title")).toContain("list-files: ticket within a day, then an hour to run, the approver role sec-approvers");
    expect(vi.mocked(getPolicy).mock.calls.map((c) => c[0]).sort()).toEqual(["dev-guardrails", "dev-tools-access"]);
  });

  // Each case is a role with no row, and the line its empty tab reads.
  const empty: [string, RoleRow, string][] = [
    ["says a role that belongs to no server reaches none, and where a role for a server is made", role, "This role reaches no MCP server and is matched by policy rules only. A role for a server is made in New role or on the server's page."],
    ["says a role its server owns has no tool of it yet, and how to pick them", filesReaders, "No tool of files yet. Edit access to pick the tools this role reaches."],
  ];
  it.each(empty)("%s", (_case, who, line) => {
    render(<Mounted who={who} rows={[elsewhere]} />);
    expect((document.querySelector("[data-no-access]") as HTMLElement).textContent).toBe(line);
  });

  it("opens nothing on a role that belongs to no server and has no row", async () => {
    render(<Mounted rows={[elsewhere]} />);
    await primary();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("opens Edit access on a glob with its own rules read back and another set's rule fixed", async () => {
    render(<Mounted />);
    await waitFor(() => expect(catalogPreview).toHaveBeenCalled());
    const sheet = await openEdit("demo-tools");
    expect(within(sheet).getByRole("heading", { name: "Edit dev-tools's access to demo-tools" })).toBeTruthy();
    expect(card("Every tool, and tools added later").getAttribute("aria-checked")).toBe("true");
    expect(card("Choose per tool").getAttribute("aria-checked")).toBe("true");
    expect((document.querySelector('[data-fixed="list-files"]') as HTMLElement).textContent).toBe("needs approval (dev-guardrails)");
    expect((document.querySelector('[data-fixed="get-env"]') as HTMLElement).textContent).toBe("denied (dev-guardrails)");
    expect(within(sheet).getByRole("button", { name: "Help: list-files" })).toBeTruthy();
    expect(within(toolRow("get-sum")).getByRole("radio", { name: "allow" }).getAttribute("aria-checked")).toBe("true");
  });

  it("says nothing changed when a save would write neither a row nor a rule", async () => {
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Nothing changed yet.");
    expect(checkDraft).not.toHaveBeenCalled();
    expect(createBinding).not.toHaveBeenCalled();
  });

  it("publishes a new rule written into the role's own set in place, keeping its rule, as a draft of its own", async () => {
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("The tools stay as they are, and saving changes only the rules.");
    await userEvent.click(within(sheet).getByRole("button", { name: "The 2 rules this writes, in dev-tools-access" }));
    const fold = (sheet.querySelector("pre") as HTMLElement).textContent || "";
    expect(fold).toContain("- id: demo-tools-get-env-deny");
    expect(fold).toContain('reason: "Straza: get-sum on demo-tools needs approval, set in the console"');
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    // The tools stay, so only the set is sent, and the export is not read.
    expect(kinds()).toEqual(["PolicySet/dev-tools-access:put"]);
    const text = sentItems()[0].doc || "";
    expect(text).toContain("- id: demo-tools-get-env-deny");
    expect(text).toContain("- id: demo-tools-approve");
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems() });
    expect(exportRole).not.toHaveBeenCalled();
    expect(vi.mocked(notify.undo).mock.calls[0].slice(0, 2)).toEqual(["dev-tools is live with your change.", "Undo"]);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(changed).toHaveBeenCalled();
    for (const direct of [applyPolicy, activatePolicy, createBinding, removeBinding]) expect(direct).not.toHaveBeenCalled();
    expect(getDraft).not.toHaveBeenCalled();
  });

  it("asks before Cancel drops a change nobody saved, and closes on the answer", async () => {
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    let ask = await screen.findByRole("alertdialog");
    expect(within(ask).getByRole("heading", { name: "Close without saving?" })).toBeTruthy();
    await userEvent.click(within(ask).getByRole("button", { name: "Keep editing" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(screen.getByRole("dialog")).toBe(sheet);
    await userEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    ask = await screen.findByRole("alertdialog");
    await userEvent.click(within(ask).getByRole("button", { name: "Close and drop the changes" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("adds the change to the working draft with Save draft and opens it", async () => {
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems(), working: true });
    expect(publishDraft).not.toHaveBeenCalled();
    expect(activatePolicy).not.toHaveBeenCalled();
  });

  it("opens the one publish dialog when the server finds a line to acknowledge", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draftOf("50", []), verdict: VERDICT });
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    const dialog = await waitFor(() => document.querySelector("[data-stub-publish]") as HTMLElement);
    expect(dialog.getAttribute("data-title")).toBe("Publish this change to dev-tools?");
    expect(publishDraft).not.toHaveBeenCalled();
  });

  // allowEnv opens the sheet on a set whose one rule denies get-env, then
  // allows it, which leaves the set with no rule.
  const allowEnv = async () => {
    vi.mocked(catalogPreview).mockResolvedValue({
      entries: [
        { app: "demo-tools", tool: "get-sum", status: "visible", reason: "" },
        { app: "demo-tools", tool: "get-env", status: "hidden_policy", reason: "", setName: "dev-tools-access", ruleId: "demo-tools-get-env-deny" },
      ],
    });
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    expect(within(toolRow("get-env")).getByRole("radio", { name: "deny" }).getAttribute("aria-checked")).toBe("true");
    await userEvent.click(within(toolRow("get-env")).getByRole("radio", { name: "allow" }));
    expect(document.querySelector("[data-save-note]")).toBeNull();
    return sheet;
  };

  it("asks before a publish that removes the role's own set, in the words of the publish, and removes it only on the answer", async () => {
    const sheet = await allowEnv();
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    let ask = await screen.findByRole("alertdialog");
    expect(within(ask).getByRole("heading", { name: "Remove dev-tools-access with this change?" })).toBeTruthy();
    expect(ask.textContent).toContain("No rule is left in it, so publishing this change removes it, and calls dev-tools makes to demo-tools then run with no approval or deny from it.");
    await userEvent.click(within(ask).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(checkDraft).not.toHaveBeenCalled();

    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    ask = await screen.findByRole("alertdialog");
    await userEvent.click(within(ask).getByRole("button", { name: "Remove it" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(sentItems()).toEqual([{ kind: "PolicySet", name: "dev-tools-access", op: "remove", doc: "" }]);
    expect(deactivatePolicy).not.toHaveBeenCalled();
    expect(deletePolicy).not.toHaveBeenCalled();
  });

  it("says the removal waits in the draft when Save draft asks, and adds it to the working draft", async () => {
    const sheet = await allowEnv();
    await userEvent.click(within(sheet).getByRole("button", { name: "Save draft" }));
    const ask = await screen.findByRole("alertdialog");
    expect(ask.textContent).toContain("No rule is left in it, so your draft removes it when the draft is published, and calls dev-tools makes to demo-tools then run with no approval or deny from it. Until then it stays as it is.");
    await userEvent.click(within(ask).getByRole("button", { name: "Remove it" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: [{ kind: "PolicySet", name: "dev-tools-access", op: "remove", doc: "" }], working: true });
    expect(publishDraft).not.toHaveBeenCalled();
  });

  const SAVED_EDIT = "dev-tools-access has a saved edit that is not published. Publish or discard it under Drafts, then save again.";
  const drift = () => vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", drift: true, yaml: OWN_SET });

  it("says at open that the own set has an unpublished saved edit, and saves nothing", async () => {
    drift();
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await waitFor(() => expect((sheet.querySelector("[data-saved-edit]") as HTMLElement).textContent).toBe(SAVED_EDIT));
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(document.querySelector('[data-save-note="refused"]')).toBeTruthy());
    expect((document.querySelector('[data-save-note="refused"]') as HTMLElement).textContent).toBe("Nothing was saved." + SAVED_EDIT);
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("saves nothing when a saved edit of the own set appeared after the sheet opened", async () => {
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    expect(sheet.querySelector("[data-saved-edit]")).toBeNull();
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    drift();
    await userEvent.click(within(sheet).getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(document.querySelector('[data-save-note="refused"]')).toBeTruthy());
    expect((document.querySelector('[data-save-note="refused"]') as HTMLElement).textContent).toBe("Nothing was saved." + SAVED_EDIT);
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("reads the Policy column for a session that may not publish policy, and still saves the tools", async () => {
    sess.grants = "apps:read,apps:write";
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    expect((sheet.querySelector("[data-read-only]") as HTMLElement).textContent).toBe("You may change which tools dev-tools reaches. Only an administrator who may publish policies changes what a call does, so the Policy column is read-only for you.");
    expect((sheet.querySelector('[data-policy-word="get-sum"]') as HTMLElement).textContent).toBe("allowed");
    await userEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await openEdit("midpoint");
    const dark = await screen.findByRole("dialog");
    expect((dark.querySelector("[data-read-only]") as HTMLElement).textContent).toBe("You may change which tools dev-tools reaches, but your session cannot read the other policies that decide its calls, so the Policy column cannot say what a call does. An administrator who may read policies sees them under Policies.");
    expect(within(dark).queryByRole("radiogroup", { name: "On a call" })).toBeNull();
    expect((dark.querySelector('[data-policy-word="read-user"]') as HTMLElement).textContent).toBe("not readable here");
    await userEvent.click(within(dark).getByRole("checkbox", { name: "tool read-user" }));
    await userEvent.click(within(dark).getByRole("button", { name: "Save and publish" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Tick at least one tool.");
    expect(listRoles).not.toHaveBeenCalled();
  });

  it("warns a policy writer when the preview could not be read, and only then", async () => {
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    expect(sheet.querySelector("[data-others-unread]")).toBeNull();
    await userEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const dark = await openEdit("midpoint");
    expect((dark.querySelector("[data-others-unread]") as HTMLElement).textContent).toBe("Other policies could not be read, so the rows show only this role's own rules, and a rule in another policy may still hold or deny a tool.");
  });

  // Each case is whether the session is the global admin, and whether the
  // glob card is off on a role its server owns.
  const ownedGlob: [string, boolean, string | null][] = [
    ["names the tools of a role its server owns for anyone but the global admin", false, "true"],
    ["offers the glob on a role its server owns to the global admin", true, null],
  ];
  it.each(ownedGlob)("%s", async (_case, globalAdmin, off) => {
    const owned: RoleRow = { id: "r-2", name: "demo-tools-readers", kind: "application", server: "demo-tools" };
    const row: BindingRow = { id: "b-9", app: "demo-tools", role: "demo-tools-readers", tools: ["get-sum"] };
    render(<Mounted who={owned} rows={[row]} globalAdmin={globalAdmin} />);
    await openEdit("demo-tools");
    expect(card("Every tool, and tools added later").getAttribute("aria-disabled")).toBe(off);
    expect(card("Only the tools you tick").getAttribute("aria-checked")).toBe("true");
  });

  it("names the rules that would be left behind before it removes a server", async () => {
    render(<Mounted rows={[glob, named]} after={[named]} />);
    await waitFor(() => expect(getPolicy).toHaveBeenCalled());
    await userEvent.click(within(accessRow("demo-tools")).getByRole("button", { name: "Remove access" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading", { name: "Remove access to demo-tools?" })).toBeTruthy();
    expect(dialog.textContent).toContain("dev-tools loses every tool of demo-tools in every live session.");
    expect((document.querySelector("[data-stale-rules]") as HTMLElement).textContent).toBe(" Rules in dev-tools's own policy still name demo-tools: demo-tools-get-env-deny. They stay and gate nothing until access is given again.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove access" }));
    await waitFor(() => expect(removeBinding).toHaveBeenCalledWith("b-1"));
    expect(notify.ok).toHaveBeenCalledWith("dev-tools no longer reaches demo-tools.");
    expect(changed).toHaveBeenCalled();
  });

  it("keeps the confirm open with the server's own sentence when the remove is refused", async () => {
    vi.mocked(removeBinding).mockRejectedValue(new ApiError("no access row with that id", 404));
    render(<Mounted />);
    await userEvent.click(within(accessRow("demo-tools")).getByRole("button", { name: "Remove access" }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Remove access" }));
    await waitFor(() => expect(dialog.textContent).toContain("The server refused it: no access row with that id. Fix what it names, then try again."));
    expect(changed).not.toHaveBeenCalled();
  });

  it("opens the sheet on the row when the page's primary is clicked and the role has one", async () => {
    render(<Mounted />);
    await primary();
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByRole("heading", { name: "Edit dev-tools's access to demo-tools" })).toBeTruthy();
    expect(document.querySelector('[data-access-sheet="demo-tools"]')).toBeTruthy();
    expect(within(sheet).queryByRole("combobox", { name: "Server" })).toBeNull();
    await waitFor(() => expect(card("Every tool, and tools added later").getAttribute("aria-checked")).toBe("true"));
    expect(within(sheet).getByRole("button", { name: "Save and publish" })).toBeTruthy();
  });

  it("opens a role its server owns that has no row on that server, then watches for the first call", async () => {
    const landedRow: BindingRow = { id: "b-4", app: "files", role: "files-readers", tools: ["read-file"] };
    vi.mocked(exportRole).mockResolvedValue(FILES_EXPORT);
    render(<Mounted who={filesReaders} rows={[elsewhere]} after={[elsewhere, landedRow]} />);
    await primary();
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByRole("heading", { name: "Edit files-readers's access to files" })).toBeTruthy();
    expect(document.querySelector('[data-access-sheet="files"]')).toBeTruthy();
    expect(within(sheet).queryByRole("combobox")).toBeNull();
    await waitFor(() => expect(sheet.querySelector("[data-access-editor]")).toBeTruthy());
    await userEvent.click(within(sheet).getByRole("checkbox", { name: "tool read-file" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    // The Role put is the live export with the one row added, and nothing
    // else about the role moves.
    expect(exportRole).toHaveBeenCalledWith("r-3");
    expect(kinds()).toEqual(["Role/files-readers:put"]);
    expect(parse(sentItems()[0].doc || "").spec).toEqual({ kind: "application", server: "files", bindings: [{ app: "files", tools: ["read-file"] }] });
    expect(createBinding).not.toHaveBeenCalled();
    expect(vi.mocked(notify.undo).mock.calls[0][0]).toBe("files-readers reaches files now.");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.querySelector("[data-first-call]")).toBeTruthy());
    expect((document.querySelector("[data-first-call]") as HTMLElement).textContent).toContain("First call to files since this access row: none yet, watching the audit log for 5 minutes.");
  });

  it("asks for a tool before it writes an empty grant", async () => {
    render(<Mounted who={filesReaders} rows={[elsewhere]} />);
    await primary();
    const sheet = await screen.findByRole("dialog");
    await waitFor(() => expect(sheet.querySelector("[data-access-editor]")).toBeTruthy());
    await userEvent.click(within(sheet).getByRole("button", { name: "Save draft" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Tick at least one tool.");
    expect(checkDraft).not.toHaveBeenCalled();
  });

  // tickGetSum opens the row on demo-tools of a role with no own set,
  // narrows it to get-sum and presses Save and publish.
  const tickGetSum = async () => {
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    render(<Mounted rows={[glob]} />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(sheet).getByRole("radio", { name: "Only the tools you tick" }));
    await userEvent.click(within(sheet).getByRole("checkbox", { name: "tool get-sum" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    return sheet;
  };
  const saveNote = () => document.querySelector('[data-save-note="refused"], [data-save-note="unreachable"]') as HTMLElement | null;

  it("keeps a live row the page did not show in the Role put, so the server's one-server refusal answers", async () => {
    // Another admin gave dev-tools midpoint after the page read its rows.
    vi.mocked(exportRole).mockResolvedValue(EXPORT.replace("        - app: demo-tools\n", "        - app: midpoint\n          tools:\n            - read-user\n        - app: demo-tools\n"));
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: { ...EMPTY, refused: [{ code: "role.bindings", class: "refused", object: "Role/dev-tools", key: "k1", sentence: "Role dev-tools lists 2 bindings, and an application role reaches one MCP server.", fix: "Make a role for each server and compose them from a business role." }] } });
    await tickGetSum();
    await waitFor(() => expect(saveNote()).not.toBeNull());
    expect(parse(sentItems()[0].doc || "").spec.bindings.map((b: { app: string }) => b.app)).toEqual(["midpoint", "demo-tools"]);
    expect(saveNote()!.textContent).toBe("Nothing was saved.Role dev-tools lists 2 bindings, and an application role reaches one MCP server. Make a role for each server and compose them from a business role.");
    expect(createDraft).not.toHaveBeenCalled();
  });

  it("keeps the server's one-server refusal in the sheet with its fix, and stores nothing", async () => {
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: { ...EMPTY, refused: [{ code: "access.role", class: "refused", object: "Role/dev-tools", key: "k1", sentence: "dev-tools already reaches midpoint, and an application role reaches one server.", fix: "Make a role for files and compose both from a business role." }] } });
    const sheet = await tickGetSum();
    await waitFor(() => expect(saveNote()).not.toBeNull());
    expect(saveNote()!.textContent).toBe("Nothing was saved.dev-tools already reaches midpoint, and an application role reaches one server. Make a role for files and compose both from a business role.");
    expect(createDraft).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBe(sheet);
  });

  // Each case is a read the save starts from that fails, and the note that
  // says nothing was sent.
  const unread: [string, () => void, string][] = [
    [
      "the role's live export, unanswered",
      () => vi.mocked(exportRole).mockRejectedValue(new ApiError("unreachable", 0, true)),
      "Nothing was saved.The live document of dev-tools could not be read because strazad did not answer. Check that it is running, then reload.",
    ],
    [
      "the role's own set, which matches another role",
      () => vi.mocked(getPolicy).mockResolvedValue({ name: "dev-tools-access", status: "active", yaml: OWN_SET.replace("roles: [dev-tools]", "roles: [sre]") }),
      "Nothing was saved.dev-tools-access exists but does not match exactly this role, so nothing was written into it. Open it under Policies and set its match to dev-tools, or rename it.",
    ],
  ];
  it.each(unread)("sends nothing when %s cannot be used, and says why", async (_case, arrange, words) => {
    arrange();
    render(<Mounted />);
    const sheet = await openEdit("demo-tools");
    await userEvent.click(within(sheet).getByRole("radio", { name: "Only the tools you tick" }));
    await userEvent.click(within(sheet).getByRole("checkbox", { name: "tool get-sum" }));
    await userEvent.click(within(toolRow("get-sum")).getByRole("radio", { name: "require approval" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(saveNote()).not.toBeNull());
    expect(saveNote()!.textContent).toBe(words);
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("says when the working draft already changes the role", async () => {
    vi.mocked(listDrafts).mockResolvedValue({ items: [{ id: "39", title: "", state: "open", door: "console", revision: 2, items: [{ kind: "Role", name: "dev-tools", op: "put" }], checks: { refused: 0, risks: 0, warnings: 0, unchecked: 0, revision: 2, checked_at: "" }, created_at: "", updated_at: "" }], next_cursor: "" } as never);
    render(<Mounted />);
    await openEdit("demo-tools");
    await waitFor(() => expect(document.querySelector("[data-working-holds]")).toBeTruthy());
    expect((document.querySelector("[data-working-holds]") as HTMLElement).textContent).toBe("Your draft 39 already changes dev-tools. Saving a draft here replaces that change with this one, made on the live version.");
  });
});
