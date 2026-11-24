import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RolePage } from "./role-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import {
  ApiError, type RoleRow, entraStatus, getConfig, listAllAssignments, listApps, listBindings, listImplications, listPolicies, listRoles, listTools, listUsers,
} from "@/lib/api";
import { GROUP_TITLE, NOT_APPLIED_TITLE, SUBJECT_ENTRA, notAppliedPill } from "@/lib/entra-words";
import { navigate } from "@/lib/router";
import { DELETE_ROLE, deleteBody } from "@/lib/role-words";
import { readFailed } from "@/lib/say";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listRoles: vi.fn(), listBindings: vi.fn(), listTools: vi.fn(), listApps: vi.fn(), listImplications: vi.fn(), listPolicies: vi.fn(),
  listUsers: vi.fn(), entraStatus: vi.fn(), getConfig: vi.fn(), listAllAssignments: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/components/origin-line", () => ({ OriginLine: () => <div data-origin-stub /> }));

const group = { group_id: "g-fin", state: "linked", read_at: new Date().toISOString(), not_applied: [{ id: "m1", kind: "user", reason: "no-user" }] };
const fin: RoleRow = { id: "r-fin", name: "finance-readers", kind: "application", holder_count: 2, entra: group };
const plain: RoleRow = { id: "r-plain", name: "plain-readers", kind: "application", holder_count: 0 };

const mount = (id: string, tab?: string) => render(<TooltipProvider><RolePage id={id} tab={tab} /></TooltipProvider>);
const block = () => document.querySelector("[data-entra-group]") as HTMLElement | null;

describe("a role's page with a group in Entra", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listRoles).mockResolvedValue([fin, plain]);
    vi.mocked(listBindings).mockResolvedValue([]);
    vi.mocked(listTools).mockResolvedValue([]);
    vi.mocked(listApps).mockResolvedValue([]);
    vi.mocked(listImplications).mockResolvedValue([]);
    vi.mocked(listPolicies).mockResolvedValue({ items: [] });
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(entraStatus).mockResolvedValue({ enabled: true, held: 0 });
    vi.mocked(getConfig).mockResolvedValue({ entra: { group_name_prefix: "Straza: " } });
    vi.mocked(listAllAssignments).mockResolvedValue([{ id: "a1", subject_kind: "user", subject_id: "u-ada", role_id: "r-fin", origin: "entra" }]);
  });

  it("draws the group block above the tabs, named with the config's prefix", async () => {
    mount("r-fin");
    await waitFor(() => expect(block()).toBeTruthy());
    expect(within(block() as HTMLElement).getByRole("heading").textContent).toBe(GROUP_TITLE);
    expect((block() as HTMLElement).compareDocumentPosition(screen.getByRole("tablist")) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(within(block() as HTMLElement).getByText("Straza: finance-readers")).toBeTruthy();
  });

  it("draws nothing and reads nothing more for a role with no group", async () => {
    mount("r-plain");
    await screen.findByRole("tablist");
    expect(block()).toBeNull();
    expect(entraStatus).not.toHaveBeenCalled();
    expect(getConfig).not.toHaveBeenCalled();
    expect(listAllAssignments).not.toHaveBeenCalled();
  });

  it("draws nothing while the sync is off", async () => {
    vi.mocked(entraStatus).mockResolvedValue({ enabled: false, held: 0 });
    mount("r-fin", "holders");
    await screen.findByRole("tablist");
    await waitFor(() => expect(entraStatus).toHaveBeenCalled());
    expect(block()).toBeNull();
    expect(screen.queryByText(NOT_APPLIED_TITLE)).toBeNull();
  });

  it("opens the Holders tab on the members not applied from the block's pill", async () => {
    mount("r-fin");
    await waitFor(() => expect(block()).toBeTruthy());
    await userEvent.click(screen.getByRole("button", { name: notAppliedPill(1) }));
    expect(navigate).toHaveBeenCalledWith("roles", ["r-fin", "holders"], true);
  });

  it("shows the members not applied, open, on the Holders tab the pill opened", async () => {
    const view = mount("r-fin", "holders");
    await waitFor(() => expect(block()).toBeTruthy());
    expect((document.querySelector("details") as HTMLDetailsElement).open).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: notAppliedPill(1) }));
    view.rerender(<TooltipProvider><RolePage id="r-fin" tab="holders" /></TooltipProvider>);
    await waitFor(() => expect((document.querySelector("details") as HTMLDetailsElement).open).toBe(true));
  });

  it("opens today's delete dialog from a deleted group's block, saying holders lose the role", async () => {
    vi.mocked(listRoles).mockResolvedValue([{ ...fin, entra: { ...group, state: "missing" } }]);
    mount("r-fin");
    await waitFor(() => expect(block()).toBeTruthy());
    await userEvent.click(within(block() as HTMLElement).getByRole("button", { name: DELETE_ROLE }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain(deleteBody(2, 0));
    expect(deleteBody(2, 0)).toContain("Its 2 holders lose it.");
  });

  it("names a failed read of the sync's state, and stays quiet on a seat that may not make it", async () => {
    vi.mocked(entraStatus).mockRejectedValue(new ApiError("store down", 500));
    mount("r-fin");
    await waitFor(() => expect(document.querySelector("[data-fetch-error]")?.textContent).toContain(readFailed(SUBJECT_ENTRA, new ApiError("store down", 500))));
    expect(block()).toBeTruthy();
    document.body.innerHTML = "";
    vi.mocked(entraStatus).mockRejectedValue(new ApiError("this token does not hold identity:read", 403));
    vi.mocked(listAllAssignments).mockRejectedValue(new ApiError("this token does not hold identity:read", 403));
    mount("r-fin");
    await waitFor(() => expect(block()).toBeTruthy());
    expect(document.querySelector("[data-fetch-error]")).toBeNull();
  });
});
