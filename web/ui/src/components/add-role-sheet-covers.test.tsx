import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { AddRoleSheet } from "./add-role-sheet";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type BindingRow, type Draft, type DraftItemIn, type DraftVerdict, type RoleRow, type ToolRow, appObjects, catalogPreview, exportRole, getPolicy, listRoles } from "@/lib/api";
import { checkDraft, createDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { VERDICT } from "@/test/drafts-fixture";

// The Add role sheet of a server that pins repos: a new role says which
// repos it covers before it is sent, and an edit that changes only the
// covers line sends the role's row.

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  appObjects: vi.fn(),
  getPolicy: vi.fn(),
  catalogPreview: vi.fn(),
  listRoles: vi.fn(),
  exportRole: vi.fn(),
}));
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/components/draft-publish", () => ({ DraftPublish: (p: DraftPublishProps) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} /> : null) }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/lib/session", () => ({ snapshot: () => ({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: "s1" }) }));

const ghapp: AppRow = {
  id: "app-1",
  name: "ghapp",
  runtime: "remote",
  status: "running",
  reached_by: [],
  manifest: { straza: { objects: { type: "repo", plural: "repos", list: { tool: "repo_list" }, tools: { pr_create: { argument: "repo" }, pr_view: { argument: "repo" } } } } },
};
const tools: ToolRow[] = ["pr_create", "pr_view"].map((n, i) => ({ id: "t" + i, app: "ghapp", app_id: "app-1", name: n }));
const writer: RoleRow = { id: "r-1", name: "ghapp-write", kind: "application", server: "ghapp", tools: ["pr_create"] };
const row: BindingRow = { id: "b-1", app: "ghapp", role: "ghapp-write", tools: ["pr_create"], covers: { objects: [{ value: "Strazahq/pay-api", id: "R1" }] } };
const EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: ghapp-write", "spec:", "    kind: application", "    server: ghapp", "    bindings:", "        - app: ghapp", "          tools:", "            - pr_create", "          covers:", "              objects:", "                  - Strazahq/pay-api", ""].join("\n");
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
const sentItems = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];
const specOf = (i: number) => (parse(sentItems()[i].doc || "") as { spec: Record<string, unknown> }).spec;
const press = (name: string) => userEvent.click(screen.getByRole("button", { name }));
const note = () => (document.querySelector("[data-save-note]") as HTMLElement | null)?.textContent;

function mount(role: RoleRow | null = null, binding: BindingRow | null = null) {
  return render(
    <TooltipProvider>
      <AddRoleSheet app={ghapp} tools={tools} role={role} roles={[]} binding={binding} globalAdmin open onOpenChange={() => undefined} onDone={() => undefined} />
    </TooltipProvider>,
  );
}

describe("the Add role sheet of a server that pins repos", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(listRoles).mockResolvedValue([]);
    vi.mocked(exportRole).mockResolvedValue(EXPORT);
    vi.mocked(appObjects).mockResolvedValue({ app: "ghapp", type: "repo", plural: "repos", objects: [{ value: "Strazahq/pay-api", id: "R1" }, { value: "Strazahq/pay-web", id: "R2" }], total: 2 });
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockImplementation(async (body) => ({ draft: draftOf("50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("asks which repos a new role covers, then sends them with its row", async () => {
    mount();
    await waitFor(() => expect(document.querySelector("[data-access-editor]")).toBeTruthy());
    await userEvent.type(screen.getByLabelText("role name suffix"), "write");
    await userEvent.click(screen.getByRole("checkbox", { name: "tool pr_create" }));
    await press("Save and publish");
    expect(note()).toBe("Say which repos this role covers.");
    expect(checkDraft).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("radio", { name: "These repos" }));
    await userEvent.click(await screen.findByRole("checkbox", { name: "Pick Strazahq/pay-web" }));
    // strazactl roles create takes no covers line, so the preview is the
    // role file and the command that drafts it.
    const file = (document.querySelector("[data-role-file]") as HTMLElement).textContent || "";
    expect(file).toContain("  bindings:\n    - app: ghapp\n      tools:\n        - pr_create\n      covers:\n        objects:\n          - Strazahq/pay-web\n");
    expect(file.endsWith("strazactl drafts create -f role-ghapp-write.yaml")).toBe(true);
    expect(document.querySelector("[data-name-preview]")?.textContent).not.toContain("strazactl roles create");
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(specOf(0)).toEqual({ kind: "application", server: "ghapp", bindings: [{ app: "ghapp", tools: ["pr_create"], covers: { objects: ["Strazahq/pay-web"] } }] });
  });

  it("sends the row when only its covers line changes, and nothing when nothing does", async () => {
    mount(writer, row);
    await waitFor(() => expect(document.querySelector("[data-covers]")).toBeTruthy());
    await press("Save and publish");
    expect(note()).toBe("Nothing changed yet.");
    await userEvent.click(await screen.findByRole("checkbox", { name: "Pick Strazahq/pay-web" }));
    await press("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(sentItems().map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/ghapp-write:put"]);
    expect(specOf(0).bindings).toEqual([{ app: "ghapp", tools: ["pr_create"], covers: { objects: ["Strazahq/pay-api", "Strazahq/pay-web"] } }]);
    expect(exportRole).toHaveBeenCalledTimes(1);
  });
});
