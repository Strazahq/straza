import * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { RoleAccess } from "./role-access";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type BindingRow, type Draft, type DraftItemIn, type DraftVerdict, type RoleRow, type ToolRow, appObjects, catalogPreview, exportRole, getPolicy, listAudit, listRoles } from "@/lib/api";
import { checkDraft, createDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { VERDICT } from "@/test/drafts-fixture";

// The Access tab of a role on a server that pins repos: the On which
// column with today's count, a save that changes only the covers line, and
// a save that has not said which repos the row covers.

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  appObjects: vi.fn(),
  catalogPreview: vi.fn(),
  getPolicy: vi.fn(),
  listRoles: vi.fn(),
  listAudit: vi.fn(),
  exportRole: vi.fn(),
}));
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/components/draft-publish", () => ({
  DraftPublish: (p: DraftPublishProps & { title?: string }) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} /> : null),
}));
vi.mock("@/lib/session", () => ({ snapshot: () => ({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: "s1" }) }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));

const role: RoleRow = { id: "r-1", name: "ghapp-write", kind: "application", server: "ghapp" };
const ghapp: AppRow = {
  id: "app-1",
  name: "ghapp",
  runtime: "remote",
  status: "running",
  reached_by: ["ghapp-write"],
  manifest: { straza: { objects: { type: "repo", plural: "repos", list: { tool: "repo_list" }, tools: { pr_create: { argument: "repo" }, pr_view: { argument: "repo" } } } } },
};
const tools: ToolRow[] = ["pr_create", "pr_view"].map((n, i) => ({ id: "t" + i, app: "ghapp", app_id: "app-1", name: n }));
const every: BindingRow = { id: "b-1", app: "ghapp", role: "ghapp-write", tools: ["pr_create"], covers: { every: true } };
const some: BindingRow = { ...every, covers: { objects: [{ value: "Strazahq/pay-web", id: "R2" }, { value: "Strazahq/pay-api", id: "R1" }] } };
const bare: BindingRow = { ...every, covers: undefined };
const EXPORT = ["apiVersion: straza.dev/v1beta1", "kind: Role", "metadata:", "    name: ghapp-write", "spec:", "    kind: application", "    server: ghapp", "    bindings:", "        - app: ghapp", "          tools:", "            - pr_create", "          covers: every", ""].join("\n");
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
const sentItems = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];

// gitlab pins projects, a type of its own.
const gitlab: AppRow = { ...ghapp, id: "app-2", name: "gitlab", manifest: { straza: { objects: { type: "project", list: { tool: "list_projects" } } } } };

function Mounted({ rows }: { rows: BindingRow[] }) {
  const [open, setOpen] = React.useState(false);
  return (
    <TooltipProvider>
      <RoleAccess role={role} bindings={rows} apps={[ghapp, gitlab]} tools={tools} sheetOpen={open} onSheetOpenChange={setOpen} onChanged={() => undefined} globalAdmin />
    </TooltipProvider>
  );
}

const accessRow = () => document.querySelector('[data-access="ghapp"]') as HTMLElement;
const openEdit = async () => {
  await userEvent.click(within(accessRow()).getByRole("button", { name: "Edit access" }));
  const sheet = await screen.findByRole("dialog");
  await waitFor(() => expect(sheet.querySelector("[data-access-editor]")).toBeTruthy());
  return sheet;
};

describe("the Access tab of a role on a server that pins repos", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [{ app: "ghapp", tool: "pr_create", status: "visible", reason: "" }] });
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    vi.mocked(listRoles).mockResolvedValue([role]);
    vi.mocked(listAudit).mockResolvedValue([]);
    vi.mocked(exportRole).mockResolvedValue(EXPORT);
    vi.mocked(appObjects).mockResolvedValue({ app: "ghapp", type: "repo", plural: "repos", objects: [{ value: "Strazahq/pay-api", id: "R1" }], total: 10 });
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockImplementation(async (body) => ({ draft: draftOf("50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  // Each case is a stored row and what its On which cell says: a row on
  // every repo with today's count under it, a row on named repos, and a
  // row with no line, which covers nothing.
  const cases: [string, BindingRow, string, string | null][] = [
    ["every repo, with today's count", every, "every repo", "10 repos today"],
    ["the named repos", some, "2 repos: Strazahq/pay-api and Strazahq/pay-web", null],
    ["no line yet", bare, "Say which repos this role covers.", null],
  ];
  it.each(cases)("shows the On which column for %s", async (_case, row, line, count) => {
    render(<Mounted rows={[row]} />);
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Server", "Tools", "On which repos", "Policy", ""]);
    const cell = accessRow().querySelector("[data-covers-cell]") as HTMLElement;
    if (count) await waitFor(() => expect(cell.querySelector("[data-covers-count]")?.textContent).toBe(count));
    else expect(appObjects).not.toHaveBeenCalled();
    expect(cell.firstChild?.textContent).toBe(line);
  });

  it("leaves today's count out when the list could not be read", async () => {
    vi.mocked(appObjects).mockRejectedValue(new ApiError("strazad did not answer", 0, true));
    render(<Mounted rows={[every]} />);
    await waitFor(() => expect(appObjects).toHaveBeenCalled());
    expect(accessRow().querySelector("[data-covers-count]")).toBe(null);
    expect((accessRow().querySelector("[data-covers-cell]") as HTMLElement).textContent).toBe("every repo");
  });

  it("heads the column Covers when the role's servers pin different types, each cell naming its own", async () => {
    render(<Mounted rows={[every, { id: "b-9", app: "gitlab", role: "ghapp-write", tools: ["pr_view"], covers: { every: true } }]} />);
    expect(screen.getAllByRole("columnheader").map((h) => h.textContent)).toEqual(["Server", "Tools", "Covers", "Policy", ""]);
    expect((document.querySelector('[data-access="gitlab"] [data-covers-cell]') as HTMLElement).firstChild?.textContent).toBe("every project");
    expect((accessRow().querySelector("[data-covers-cell]") as HTMLElement).firstChild?.textContent).toBe("every repo");
  });

  it("leaves today's count out while the listing has given no list", async () => {
    vi.mocked(appObjects).mockResolvedValue({ app: "ghapp", type: "repo", plural: "repos", objects: [], total: 0, listing: { tool: "repo_list", credential: "missing", outcome: "failed", version: 0, count: 0 } });
    render(<Mounted rows={[every]} />);
    await waitFor(() => expect(appObjects).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(accessRow().querySelector("[data-covers-count]")).toBe(null);
  });

  it("saves a row read with its repos' current values, the line unchanged", async () => {
    const current: BindingRow = { ...every, covers: { objects: [{ value: "Strazahq/payments-api", id: "R1" }] } };
    vi.mocked(exportRole).mockResolvedValue(EXPORT.replace("          covers: every\n", "          covers:\n              objects:\n                  - Strazahq/payments-api\n"));
    render(<Mounted rows={[current]} />);
    const sheet = await openEdit();
    await userEvent.click(screen.getByRole("checkbox", { name: "tool pr_view" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    const doc = parse(sentItems()[0].doc || "") as { spec: { bindings: unknown[] } };
    expect(doc.spec.bindings).toEqual([{ app: "ghapp", tools: ["pr_create", "pr_view"], covers: { objects: ["Strazahq/payments-api"] } }]);
  });

  it("publishes a change of the covers line alone as the role's row", async () => {
    render(<Mounted rows={[every]} />);
    const sheet = await openEdit();
    await userEvent.click(screen.getByRole("radio", { name: "These repos" }));
    await userEvent.click(await screen.findByRole("checkbox", { name: "Pick Strazahq/pay-api" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect(sentItems().map((i) => i.kind + "/" + i.name + ":" + i.op)).toEqual(["Role/ghapp-write:put"]);
    const doc = parse(sentItems()[0].doc || "") as { spec: { bindings: unknown[] } };
    expect(doc.spec.bindings).toEqual([{ app: "ghapp", tools: ["pr_create"], covers: { objects: ["Strazahq/pay-api"] } }]);
    expect(vi.mocked(notify.undo).mock.calls[0][0]).toBe("ghapp-write reaches ghapp now.");
  });

  it("asks which repos the row covers before it sends anything", async () => {
    render(<Mounted rows={[bare]} />);
    const sheet = await openEdit();
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Say which repos this role covers.");
    await userEvent.click(screen.getByRole("radio", { name: "These repos" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Save and publish" }));
    expect((document.querySelector("[data-save-note]") as HTMLElement).textContent).toBe("Say which repos this role covers.");
    expect(checkDraft).not.toHaveBeenCalled();
  });
});
