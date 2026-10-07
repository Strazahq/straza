import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
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
