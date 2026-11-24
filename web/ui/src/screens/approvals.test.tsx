import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Approvals } from "./approvals";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApprovalRow, getConfig, listApprovals, listApproverDevices, listRoles } from "@/lib/api";
import { OPEN_SELF_SERVICE, OWN_REQUEST, decideAction } from "@/lib/approval-words";
import { snapshot } from "@/lib/session";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getConfig: vi.fn(),
  listApprovals: vi.fn(),
  listApproverDevices: vi.fn(),
  listRoles: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

// alice's own agent call, held for her own confirmation, as the queue
// answers it to alice.
const mineOwn: ApprovalRow = {
  id: "apr_own", state: "pending", createdAt: new Date(Date.now() - 10000).toISOString(), expiresAt: new Date(Date.now() + 110000).toISOString(), decidedAt: null,
  user: "u-alice", username: "alice", session: "0199c1a2-7e3f-7000-8000-000000000001",
  rule: "sandbox-get-sum-approve", set: "demo-tools-sandbox-access", lane: "gateway",
  summary: "mcp.call demo-tools:get-sum", justification: "", approverRoles: [], selfApproval: false, mode: "confirm", decidedBy: "", decidedByName: "",
};

const mount = () => render(<TooltipProvider><Approvals /></TooltipProvider>);
const ownRow = () => screen.findByRole("button", { name: "Open get-sum for alice" });

describe("the Approvals page and a person's own request", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: ["straza-admin"] } as ReturnType<typeof snapshot>);
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [mineOwn] });
    vi.mocked(listApproverDevices).mockResolvedValue([]);
    vi.mocked(listRoles).mockResolvedValue([]);
  });

  const cases: [string, () => void, boolean][] = [
    ["says where to confirm it while the server refuses an unsigned own decision", () => vi.mocked(getConfig).mockResolvedValue({ approval: { unsigned_own_decisions: false } }), false],
    ["keeps the strict words when the seat may not read the configuration", () => vi.mocked(getConfig).mockRejectedValue(new ApiError("requires the config area", 403)), false],
    ["offers Deny and Approve once the server accepts an unsigned own decision", () => vi.mocked(getConfig).mockResolvedValue({ approval: { unsigned_own_decisions: true } }), true],
  ];
  it.each(cases)("%s", async (_name, arrange, buttons) => {
    arrange();
    mount();
    const row = await ownRow();
    if (buttons) {
      expect(await within(row).findByRole("button", { name: decideAction("approve", mineOwn) })).toBeTruthy();
      expect(row.querySelector("[data-own-request]")).toBe(null);
      return;
    }
    expect((row.querySelector("[data-own-request]") as HTMLElement).textContent).toBe(OWN_REQUEST + " " + OPEN_SELF_SERVICE);
    expect(within(row).queryByRole("button")).toBe(null);
  });
});

// A harness tool Straza does not classify, stored as other: <name>, held
// for alice as the agent's sponsor.
const unclassified: ApprovalRow = { ...mineOwn, id: "apr_other", user: "u-joe", username: "joe", lane: "hook", summary: "other: refund_order", approverUsers: ["alice"], mode: "approve" };

describe("a request on the Approvals page", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: ["straza-admin"] } as ReturnType<typeof snapshot>);
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [unclassified] });
    vi.mocked(listApproverDevices).mockResolvedValue([]);
    vi.mocked(listRoles).mockResolvedValue([]);
    vi.mocked(getConfig).mockResolvedValue({ approval: { unsigned_own_decisions: false } });
  });

  it("names a tool Straza does not classify by its name alone, in the Call cell, the dialog's title and its call", async () => {
    mount();
    const row = await screen.findByRole("button", { name: "Open refund_order for joe" });
    expect((row.querySelector('[data-column="call"]') as HTMLElement).textContent).toBe("refund_order");
    await userEvent.click(row);
    const dialog = await screen.findByRole("dialog");
    expect(dialog.querySelector("h2")?.textContent).toBe("joe wants to call a tool refund_order");
    expect((dialog.querySelector("[data-card-tool]") as HTMLElement).textContent).toBe("refund_order");
  });

  it("gives the focus back to the row that opened the request's dialog, after Escape", async () => {
    mount();
    const row = await screen.findByRole("button", { name: "Open refund_order for joe" });
    row.focus();
    await userEvent.keyboard("{Enter}");
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open refund_order for joe" })));
  });
});
