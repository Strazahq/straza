import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { RoleComposes } from "./role-composes";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type BindingRow, type Draft, type DraftItemIn, type DraftVerdict, type ImplicationRow, type RoleRow, addImplication, catalogPreview, exportRole, removeBinding, removeImplication } from "@/lib/api";
import { checkDraft, createDraft, getDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { COMPOSES_EMPTY, COMPOSE_MISSING, COMPOSE_PICK, LEGACY_ROWS, REMOVE, REMOVE_ACCESS, UNCOMPOSE, composeAdminBody, composeBody, composedToast, reachPill, uncomposeBody, uncomposeTitle, uncomposedToast } from "@/lib/role-words";
import { VERDICT } from "@/test/drafts-fixture";

const dev: RoleRow = { id: "r-dev", name: "dev", kind: "business", holder_count: 2 };
const devTools: RoleRow = { id: "r-dev-tools", name: "dev-tools", kind: "application" };
const scout: RoleRow = { id: "r-scout", name: "scout-tools", kind: "application" };
const sec: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver" };
// The role Straza mints with an MCP server: composing it makes one business
// role a team over several servers.
const jiraAdmin: RoleRow = { id: "r-m-jira", name: "mcp-admin-finance-jira", kind: "straza" };
const catalog = [dev, devTools, scout, sec];

const implications: ImplicationRow[] = [{ id: "i1", implies_id: "r-dev-tools", implies_name: "dev-tools" }];
const legacy: BindingRow[] = [{ id: "b9", app: "midpoint", role: "dev", tools: ["*"] }];
// The access rows behind the composed role: demo-tools is stored as a glob,
// midpoint names its one tool.
const reach: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b2", app: "midpoint", role: "dev-tools", tools: ["get-user"] },
];
// One pill per server, with the words the row behind it earns.
const pillCases: [string, string][] = [
  ["demo-tools", reachPill(3, "dev-tools", true)],
  ["midpoint", reachPill(1, "dev-tools")],
];

const preview = {
  entries: [
    { app: "demo-tools", tool: "echo", status: "visible", reason: "" },
    { app: "demo-tools", tool: "get-sum", status: "approve_gated", reason: "" },
    { app: "demo-tools", tool: "get-env", status: "hidden_policy", reason: "" },
    { app: "demo-tools", tool: "gone", status: "not_running", reason: "" },
    { app: "midpoint", tool: "get-user", status: "visible", reason: "" },
  ],
};

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  catalogPreview: vi.fn(), addImplication: vi.fn(), removeImplication: vi.fn(), removeBinding: vi.fn(), exportRole: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
// Composing saves through the drafts routes.
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/components/draft-publish", () => ({ DraftPublish: (p: DraftPublishProps) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} /> : null) }));

// EXPORT is dev's live export, the document an added implication starts
// from.
const EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: dev", "spec:", "    kind: business", "    description: Developer seat.", "    implies:", "        - dev-tools", ""].join("\n");
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
const sentItems = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];
const implied = () => (parse(sentItems()[0].doc || "") as { spec: { implies: string[] } }).spec.implies;

const onChanged = vi.fn();
const onComposeOpenChange = vi.fn();
const mount = (over: Partial<React.ComponentProps<typeof RoleComposes>> = {}) =>
  render(
    <TooltipProvider>
      <RoleComposes
        role={dev}
        roles={catalog}
        implications={implications}
        bindings={[]}
        composeOpen={false}
        onComposeOpenChange={onComposeOpenChange}
        onChanged={onChanged}
        problem={null}
        lastRead={new Date()}
        {...over}
      />
    </TooltipProvider>,
  );

// choose opens the dialog's select and takes one option by name.
async function choose(name: string) {
  await userEvent.click(screen.getByRole("combobox", { name: COMPOSE_PICK }));
  await userEvent.click(await screen.findByRole("option", { name }));
}

describe("the Composes tab", () => {
  beforeEach(() => {
    vi.mocked(catalogPreview).mockResolvedValue(preview);
    vi.mocked(addImplication).mockResolvedValue({ role_id: "r-dev", implies_role_id: "r-dev-tools" });
    vi.mocked(removeImplication).mockResolvedValue({ status: "removed" });
    vi.mocked(removeBinding).mockResolvedValue({ status: "removed" });
    onChanged.mockClear();
    onComposeOpenChange.mockClear();
    vi.mocked(notify.ok).mockClear();
    vi.mocked(notify.undo).mockClear();
    vi.mocked(navigate).mockClear();
    vi.mocked(exportRole).mockResolvedValue(EXPORT);
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockReset().mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockReset().mockImplementation(async (body) => ({ draft: draftOf(body.working ? "39" : "50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockReset().mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("lists the composed roles with their kind and a way to remove one", async () => {
    mount();
    expect(document.querySelector('[data-implies="dev-tools"]')).toBeTruthy();
    expect(screen.getByText("application role")).toBeTruthy();
    expect(screen.getByRole("button", { name: REMOVE })).toBeTruthy();
  });

  it("counts the tools a holder reaches on each server, through the role they arrive by", async () => {
    mount();
    await waitFor(() => expect(document.querySelector('[data-reach-pill="demo-tools"]')).toBeTruthy());
    expect(catalogPreview).toHaveBeenCalledWith("dev-tools");
    // Three of the four demo-tools entries exist for the session; the one
    // on a stopped server does not.
    expect((document.querySelector('[data-reach-pill="demo-tools"]') as HTMLElement).textContent).toBe("demo-tools" + reachPill(3, "dev-tools"));
    expect((document.querySelector('[data-reach-pill="midpoint"]') as HTMLElement).textContent).toBe("midpoint" + reachPill(1, "dev-tools"));
  });

  it.each(pillCases)("says on the %s pill whether the access row behind it reaches tools added later", async (server, words) => {
    mount({ bindings: reach });
    await waitFor(() => expect(document.querySelector('[data-reach-pill="' + server + '"]')).toBeTruthy());
    expect((document.querySelector('[data-reach-pill="' + server + '"]') as HTMLElement).textContent).toBe(server + words);
  });

  it("says what to do when nothing is composed yet", () => {
    mount({ implications: [] });
    expect(screen.getByText(COMPOSES_EMPTY)).toBeTruthy();
  });

  it("restates the loss before it stops composing a role", async () => {
    mount();
    await userEvent.click(screen.getByRole("button", { name: REMOVE }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(uncomposeTitle("dev-tools"))).toBeTruthy();
    expect(dialog.textContent).toContain(uncomposeBody("dev", "dev-tools"));
    await userEvent.click(within(dialog).getByRole("button", { name: UNCOMPOSE }));
    await waitFor(() => expect(removeImplication).toHaveBeenCalledWith("r-dev", "r-dev-tools"));
    expect(notify.ok).toHaveBeenCalledWith(uncomposedToast("dev", "dev-tools"));
  });

  it("offers no business or approver role, and publishes the live document with the picked role added", async () => {
    mount({ composeOpen: true });
    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("combobox", { name: COMPOSE_PICK }));
    const offered = (await screen.findAllByRole("option")).map((o) => o.textContent);
    expect(offered).toEqual(["scout-tools"]);
    await userEvent.click(screen.getByRole("option", { name: "scout-tools" }));
    expect(await screen.findByText(composeBody("dev", "scout-tools"))).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(exportRole).toHaveBeenCalledWith("r-dev");
    expect(sentItems().map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/dev:put"]);
    expect(implied()).toEqual(["dev-tools", "scout-tools"]);
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems() });
    expect(vi.mocked(notify.undo).mock.calls[0].slice(0, 2)).toEqual([composedToast("dev", "scout-tools"), "Undo"]);
    expect(onComposeOpenChange).toHaveBeenCalledWith(false);
    expect(onChanged).toHaveBeenCalled();
    expect(addImplication).not.toHaveBeenCalled();
    expect(getDraft).not.toHaveBeenCalled();
  });

  it("adds the composition to the working draft with Save draft and opens it", async () => {
    mount({ composeOpen: true });
    await screen.findByRole("dialog");
    await choose("scout-tools");
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: sentItems(), working: true });
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("offers the minted admin role of a server and says what composing it gives", async () => {
    mount({ composeOpen: true, roles: catalog.concat(jiraAdmin) });
    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("combobox", { name: COMPOSE_PICK }));
    expect((await screen.findAllByRole("option")).map((o) => o.textContent)).toEqual(["scout-tools", "mcp-admin-finance-jira"]);
    await userEvent.click(screen.getByRole("option", { name: "mcp-admin-finance-jira" }));
    expect(await screen.findByText(composeAdminBody("dev", "mcp-admin-finance-jira"))).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(implied()).toEqual(["dev-tools", "mcp-admin-finance-jira"]);
  });

  it("names the missing pick instead of sending", async () => {
    mount({ composeOpen: true });
    await screen.findByRole("dialog");
    await userEvent.click(screen.getByRole("button", { name: "Save and publish" }));
    expect(await screen.findByText(COMPOSE_MISSING)).toBeTruthy();
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("keeps a refused edge in the dialog with the server's own sentence, and stores nothing", async () => {
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: { ...EMPTY, refused: [{ code: "imply.cycle", class: "refused", object: "Role/dev", key: "k1", sentence: "dev would imply scout-tools, which already implies dev, and roles may not imply each other in a circle.", fix: "Leave the edge out." }] } });
    mount({ composeOpen: true });
    await screen.findByRole("dialog");
    await choose("scout-tools");
    await userEvent.click(screen.getByRole("button", { name: "Save and publish" }));
    const said = await screen.findByText(/in a circle/);
    expect(said.textContent).toBe("dev would imply scout-tools, which already implies dev, and roles may not imply each other in a circle. Leave the edge out.");
    expect(createDraft).not.toHaveBeenCalled();
    expect(onComposeOpenChange).not.toHaveBeenCalledWith(false);
  });

  it("lists a business role's own access rows as legacy, and removes one", async () => {
    mount({ bindings: legacy });
    expect(screen.getByText(LEGACY_ROWS)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: REMOVE_ACCESS }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: REMOVE_ACCESS }));
    await waitFor(() => expect(removeBinding).toHaveBeenCalledWith("b9"));
  });
});
