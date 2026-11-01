import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
import { NewRole } from "./new-role";
import type { DraftPublishProps } from "@/components/draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AppRow, type Draft, type DraftItemIn, type DraftVerdict, type RoleRow, type ToolRow, appObjects, catalogPreview, getPolicy, listApps, listPacks, listRoles, listTools } from "@/lib/api";
import { checkDraft, createDraft, listDrafts, publishDraft } from "@/lib/drafts-api";
import { VERDICT } from "@/test/drafts-fixture";

// The New role wizard on a server that pins repos: Next asks which repos
// the role covers, and the review says it on the server's line and on the
// row it writes.

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  appObjects: vi.fn(),
  listRoles: vi.fn(),
  listApps: vi.fn(),
  listTools: vi.fn(),
  listPacks: vi.fn(),
  catalogPreview: vi.fn(),
  getPolicy: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/components/draft-publish", () => ({ DraftPublish: (p: DraftPublishProps) => (p.open ? <div role="dialog" data-stub-publish={p.draft.id} /> : null) }));
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
const made: RoleRow = { id: "r-new", name: "ghapp-write", kind: "application", server: "ghapp" };
const EMPTY: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draftOf = (id: string, items: DraftItemIn[]): Draft => ({ id, revision: 1, state: "open", door: "console", authors: [], items: items.map((i) => ({ ...i, existed: true })), title: "", created_at: "", updated_at: "" });
const sent = () => (vi.mocked(checkDraft).mock.calls[0][0].items || []) as DraftItemIn[];
const click = (name: string) => userEvent.click(screen.getByRole("button", { name }));
const textOf = (selector: string) => (document.querySelector(selector) as HTMLElement).textContent;
const landed = () => Array.from(document.querySelectorAll("[data-landed] li")).map((li) => li.textContent || "");

// dev-tools is an application role a business role may compose.
const devTools: RoleRow = { id: "r-1", name: "dev-tools", kind: "application" };

describe("the New role wizard on a server that pins repos", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listRoles).mockResolvedValue([]);
    vi.mocked(listApps).mockResolvedValue([ghapp]);
    vi.mocked(listTools).mockResolvedValue(tools);
    vi.mocked(listPacks).mockResolvedValue([]);
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
    vi.mocked(getPolicy).mockRejectedValue(new ApiError("policy set not found", 404));
    vi.mocked(appObjects).mockResolvedValue({ app: "ghapp", type: "repo", plural: "repos", objects: [], total: 10 });
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(createDraft).mockImplementation(async (body) => ({ draft: draftOf("50", body.items || []), verdict: EMPTY }));
    vi.mocked(publishDraft).mockImplementation(async (id) => ({ draft: { ...draftOf(id, []), state: "published" }, snapshot: "3be0a1", servers: [], next: [] }));
  });

  it("puts the composed roles in the role file and leaves out the implication commands", async () => {
    vi.mocked(listRoles).mockResolvedValue([devTools]);
    render(
      <TooltipProvider>
        <NewRole />
      </TooltipProvider>,
    );
    await screen.findByRole("radio", { name: "kind business" });
    await userEvent.click(screen.getByRole("radio", { name: "kind business" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "write");
    await click("Next");
    await userEvent.click(await screen.findByRole("checkbox", { name: "role dev-tools" }));
    await click("Back");
    await userEvent.click(await screen.findByRole("radio", { name: "kind application" }));
    await userEvent.type(screen.getByRole("textbox", { name: "role name suffix" }), "write");
    await click("Next");
    await screen.findByRole("heading", { level: 2, name: "Access" });
    await userEvent.click(screen.getByRole("checkbox", { name: "tool pr_create" }));
    await userEvent.click(screen.getByRole("radio", { name: "Every repo" }));
    await click("Next");
    await screen.findByRole("heading", { level: 2, name: "Review" });
    await userEvent.click(screen.getByRole("button", { name: "The same role as strazactl commands" }));
    const commands = (document.querySelector("pre") as HTMLElement).textContent || "";
    expect(commands).toContain("  implies:\n    - dev-tools\n");
    expect(commands).not.toContain("strazactl roles implications add");
  });

  it("asks which repos before Review, then says the covers line on the server's line and the row", async () => {
    render(
      <TooltipProvider>
        <NewRole />
      </TooltipProvider>,
    );
    await waitFor(() => expect(document.querySelector("[data-name-preview]")).toBeTruthy());
    await userEvent.type(screen.getByRole("textbox", { name: "role name suffix" }), "write");
    await click("Next");
    await screen.findByRole("heading", { level: 2, name: "Access" });
    await userEvent.click(screen.getByRole("checkbox", { name: "tool pr_create" }));
    await click("Next");
    expect(textOf("[data-covers-miss]")).toBe("Say which repos this role covers.");
    expect(screen.queryByRole("heading", { level: 2, name: "Review" })).toBe(null);
    await userEvent.click(screen.getByRole("radio", { name: "Every repo" }));
    await click("Next");
    await screen.findByRole("heading", { level: 2, name: "Review" });
    expect(textOf('[data-server-band="ghapp"] [data-review-reach]')).toBe("1 of 2 tools, on every repo, including the repos ghapp lists later");
    expect(landed()[1]).toBe("2Write the new access row: pr_create, on every repo, including the repos ghapp lists later (ghapp)");
    await userEvent.click(screen.getByRole("button", { name: "The same role as strazactl commands" }));
    const commands = (document.querySelector("pre") as HTMLElement).textContent || "";
    expect(commands).toContain("      covers: every\nstrazactl drafts create -f role-ghapp-write.yaml");
    expect(commands).not.toContain("strazactl roles create");
    vi.mocked(listRoles).mockResolvedValue([made]);
    await click("Save and publish");
    await waitFor(() => expect(publishDraft).toHaveBeenCalled());
    expect((parse(sent()[0].doc || "") as { spec: unknown }).spec).toEqual({ kind: "application", server: "ghapp", bindings: [{ app: "ghapp", tools: ["pr_create"], covers: "every" }] });
  });
});
