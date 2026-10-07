import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApprovalDecide } from "./approval-decide";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApprovalRow, decideApproval, getApproval } from "@/lib/api";
import { notify } from "@/lib/notify";
import {
  APPROVE_REQUEST,
  CANCEL,
  CLOSE,
  DENY_REQUEST,
  REASON_LABEL,
  type Verdict,
  WINDOW_CLOSED,
  alreadyDecided,
  approvedToast,
  decideTitle,
  deniedToast,
  finalTitle,
  notYours,
} from "@/lib/approval-words";
import { refused } from "@/lib/say";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  decideApproval: vi.fn(),
  getApproval: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();

const row: ApprovalRow = {
  id: "apr_hold", state: "pending", createdAt: at(-28), expiresAt: at(92), decidedAt: null,
  user: "u-joe", username: "joe-java-developer-agent", session: "0199c1a2-7e3f-7000-8000-000000000001",
  rule: "dev-mcp-sum-approval-showcase", set: "dev-guardrails", lane: "gateway",
  summary: "mcp.call demo-tools:get-sum", justification: "", approverRoles: [], approverUsers: ["alice"],
  selfApproval: false, mode: "approve", decidedBy: "", decidedByName: "",
};
const answered: ApprovalRow = { ...row, state: "approved", decidedAt: at(0), decidedBy: "u-alice", decidedByName: "alice", channel: "console" };
const elsewhere: ApprovalRow = { ...row, state: "denied", decidedAt: at(-3), decidedBy: "u-alice", decidedByName: "alice", channel: "phone", decidedReason: "not now" };

const close = vi.fn();
const decided = vi.fn();
const mount = (verdict: Verdict = "approve") => render(
  <TooltipProvider>
    <ApprovalDecide ask={{ row, verdict }} onClose={close} onDecided={decided} />
  </TooltipProvider>,
);
const dialog = () => screen.getByRole("alertdialog");
const go = (verdict: Verdict = "approve") => within(dialog()).getByRole("button", { name: verdict === "approve" ? APPROVE_REQUEST : DENY_REQUEST });

describe("the decide dialog", () => {
  beforeEach(() => {
    close.mockClear();
    decided.mockClear();
    vi.mocked(notify.ok).mockClear();
    vi.mocked(decideApproval).mockResolvedValue({ approval: answered });
    vi.mocked(getApproval).mockResolvedValue({ approval: elsewhere });
  });

  it("names the call and the requester and sends the reason only when one was typed", async () => {
    const view = mount();
    expect(within(dialog()).getByText(decideTitle("approve", row))).toBeTruthy();
    await userEvent.click(go());
    await waitFor(() => expect(vi.mocked(decideApproval)).toHaveBeenCalledWith("apr_hold", "approve", ""));

    view.unmount();
    vi.mocked(decideApproval).mockClear();
    mount("deny");
    await userEvent.type(screen.getByLabelText(REASON_LABEL), "  walk cleanup  ");
    await userEvent.click(go("deny"));
    await waitFor(() => expect(vi.mocked(decideApproval)).toHaveBeenCalledWith("apr_hold", "deny", "walk cleanup"));
  });

  it("says what happened, hands the fresh record back and closes", async () => {
    const view = mount();
    await userEvent.click(go());
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(approvedToast(row)));
    expect(decided).toHaveBeenCalledWith(answered);
    expect(close).toHaveBeenCalled();
    view.rerender(<TooltipProvider><ApprovalDecide ask={null} onClose={close} onDecided={decided} /></TooltipProvider>);
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBe(null));
  });

  it("words a denial in the queue's own words", async () => {
    mount("deny");
    await userEvent.click(go("deny"));
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(deniedToast(row)));
  });

  it("ends the question when the request was decided elsewhere first", async () => {
    vi.mocked(decideApproval).mockRejectedValue(new ApiError("already decided", 409));
    mount();
    await userEvent.click(go());
    await waitFor(() => expect(within(dialog()).getByText(finalTitle("approve"))).toBeTruthy());
    expect(vi.mocked(getApproval)).toHaveBeenCalledWith("apr_hold");
    expect(decided).toHaveBeenCalledWith(elsewhere);
    expect(dialog().textContent).toContain(alreadyDecided(elsewhere));
    expect(screen.queryByLabelText(REASON_LABEL)).toBe(null);
    expect(within(dialog()).queryByRole("button", { name: APPROVE_REQUEST })).toBe(null);
    expect(within(dialog()).getByRole("button", { name: CLOSE })).toBeTruthy();
  });

  it("ends the question when the window closed or the request stopped being yours", async () => {
    vi.mocked(decideApproval).mockRejectedValue(new ApiError("the approval window closed", 410));
    const view = mount();
    await userEvent.click(go());
    await waitFor(() => expect(dialog().textContent).toContain(WINDOW_CLOSED));
    expect(within(dialog()).queryByRole("button", { name: APPROVE_REQUEST })).toBe(null);
    view.unmount();

    vi.mocked(decideApproval).mockRejectedValue(new ApiError("you are not an approver for this request.", 403));
    mount();
    await userEvent.click(go());
    await waitFor(() => expect(dialog().textContent).toContain(notYours("you are not an approver for this request")));
    expect(within(dialog()).queryByRole("button", { name: APPROVE_REQUEST })).toBe(null);
  });

  it("keeps the question when strazad did not answer and when it failed", async () => {
    const dead = new ApiError("unreachable", 0, true);
    vi.mocked(decideApproval).mockRejectedValue(dead);
    const view = mount();
    await userEvent.click(go());
    const block = await screen.findByRole("alert");
    expect(block.textContent).toContain(refused(dead));
    expect(go()).toBeTruthy();
    expect(within(dialog()).getByRole("button", { name: CANCEL })).toBeTruthy();
    view.unmount();

    const broke = new ApiError("the approval store is unavailable", 500);
    vi.mocked(decideApproval).mockRejectedValue(broke);
    mount();
    await userEvent.click(go());
    expect((await screen.findByRole("alert")).textContent).toContain(refused(broke));
    expect(go()).toBeTruthy();
  });

  it("styles the denial apart from the approval", async () => {
    const view = mount("deny");
    expect(go("deny").className).toContain("bg-danger");
    view.unmount();
    mount();
    expect(go().className).not.toContain("bg-danger");
  });
});
