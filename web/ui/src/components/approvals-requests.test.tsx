import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RequestsTab } from "./approvals-requests";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApprovalRow, type RoleRow, decideApproval, listApprovals, listRoles, pageApprovals } from "@/lib/api";
import { type Seat, mine, own } from "@/lib/approval-model";
import {
  APPROVE,
  EMPTY_DECIDED_TITLE,
  EMPTY_SEARCH,
  EMPTY_WAITING_TITLE,
  NOBODY_CAN,
  NOBODY_HOLDS,
  OPEN_SELF_SERVICE,
  OWN_REQUEST,
  OWN_REQUEST_WHERE,
  SUBJECT_REQUESTS,
  WAITING_LINE,
  YOURS,
  decideAction,
  decideTitle,
  decidedCount,
  decidesWord,
  requestsCount,
  waitingCount,
} from "@/lib/approval-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApprovals: vi.fn(),
  listRoles: vi.fn(),
  pageApprovals: vi.fn(),
  decideApproval: vi.fn(),
  getApproval: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const at = (s: number) => new Date(Date.now() + s * 1000).toISOString();

const base: ApprovalRow = {
  id: "", state: "pending", createdAt: "", expiresAt: "", decidedAt: null,
  user: "u-joe", username: "joe-java-developer-agent", session: "0199c1a2-7e3f-7000-8000-000000000001",
  rule: "dev-mcp-sum-approval-showcase", set: "dev-guardrails", lane: "gateway",
  summary: "mcp.call demo-tools:get-sum", justification: "", approverRoles: [], approverUsers: ["alice"],
  selfApproval: false, mode: "approve", decidedBy: "", decidedByName: "",
};

// The four waiting rows and the four decided ones of the fixture: two
// of alice's own, one routed to a role nobody holds, one of bob's, and the
// decided shapes the State cell has to read.
let hold: ApprovalRow;
let ticket: ApprovalRow;
let held: ApprovalRow;
let foreign: ApprovalRow;
let denied: ApprovalRow;
let granted: ApprovalRow;
let used: ApprovalRow;
let lapsedRole: ApprovalRow;

function build() {
  hold = { ...base, id: "apr_hold", createdAt: at(-28), expiresAt: at(92), justification: "Adding the two build durations the user asked about." };
  ticket = { ...base, id: "apr_ticket", class: "ticket", createdAt: at(-7200), expiresAt: at(79200), rule: "dev-mcp-env-ticket", summary: "mcp.call demo-tools:get-env" };
  held = { ...base, id: "apr_held", createdAt: at(-75), expiresAt: at(45), lane: "hook", summary: "shell.exec: printf lf-q4-x", rule: "hold-printf", set: "lf-q4-hold", approverUsers: undefined, approverRoles: ["sec-approvers"] };
  foreign = { ...base, id: "apr_foreign", class: "ticket", user: "u-sam", username: "sam-sre-agent", createdAt: at(-3600), expiresAt: at(82800), rule: "dev-mcp-env-ticket", summary: "mcp.call demo-tools:get-env", approverUsers: ["bob"] };
  denied = { ...base, id: "apr_denied", state: "denied", createdAt: at(-3720), expiresAt: at(-3600), decidedAt: at(-120), decidedBy: "u-alice", decidedByName: "alice", channel: "console", decidedReason: "walk cleanup" };
  granted = { ...base, id: "apr_granted", class: "ticket", state: "approved", createdAt: at(-5400), expiresAt: at(80000), decidedAt: at(-120), grantExpiresAt: at(3480), decidedBy: "u-alice", decidedByName: "alice", channel: "console", summary: "mcp.call demo-tools:get-env" };
  used = { ...base, id: "apr_used", class: "ticket", state: "approved", createdAt: at(-9000), expiresAt: at(-5400), decidedAt: at(-8000), grantExpiresAt: at(-4000), consumedAt: at(-7900), consumedBy: "01998f22-c0de-7000-8000-000000000002", decidedBy: "u-alice", decidedByName: "alice", channel: "phone", summary: "mcp.call demo-tools:get-time" };
  lapsedRole = { ...base, id: "apr_expired", state: "expired", createdAt: at(-7320), expiresAt: at(-7200), lane: "hook", summary: "shell.exec: printf lf-q4-y", rule: "hold-printf", set: "lf-q4-hold", approverUsers: undefined, approverRoles: ["sec-approvers"] };
}

const waitingRows = () => [hold, ticket, held, foreign];
const decidedRows = () => [denied, granted, used, lapsedRole];

const roles: RoleRow[] = [
  { id: "r1", name: "sec-approvers", kind: "approver", holder_count: 0 },
  { id: "r2", name: "straza-admin", kind: "access", holder_count: 2 },
];

const seat: Seat = { user: "alice", roles: ["straza-admin"] };
const changed = vi.fn();
const mount = (openID?: string) => render(
  <TooltipProvider>
    <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} openID={openID} onChanged={changed} />
  </TooltipProvider>,
);

const row = (name: string) => screen.getByRole("button", { name: "Open " + name });
const counted = () => (document.querySelector("[data-row-count]") as HTMLElement).textContent;
const seg = (label: string) => screen.getByRole("button", { name: label });

describe("the requests queue", () => {
  beforeEach(() => {
    build();
    vi.mocked(listApprovals).mockResolvedValue({ approvals: waitingRows() });
    vi.mocked(pageApprovals).mockResolvedValue({ items: [...waitingRows(), ...decidedRows()], next_cursor: "" });
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(decideApproval).mockResolvedValue({ approval: { ...hold, state: "denied" } });
    changed.mockClear();
  });
  afterEach(() => { vi.useRealTimers(); });

  it("counts every waiting request once, whether it is held in place or a standing approval", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(container.querySelectorAll("[data-request]").length).toBe(4);
    expect(counted()).toBe(waitingCount(4, 2));
  });

  it("offers Deny and Approve on the rows that are yours and says who decides the others", async () => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    const ownRow = row("get-sum for joe-java-developer-agent");
    expect(within(ownRow).getByRole("button", { name: decideAction("deny", hold) })).toBeTruthy();
    expect(within(ownRow).getByRole("button", { name: decideAction("approve", hold) })).toBeTruthy();
    expect(within(ownRow).getByText(YOURS)).toBeTruthy();

    const bobs = row("get-env for sam-sre-agent");
    expect(within(bobs).queryByRole("button")).toBe(null);
    expect(within(bobs).getByText(decidesWord("bob"))).toBeTruthy();

    const nobodys = row("printf lf-q4-x for joe-java-developer-agent");
    await waitFor(() => expect(within(nobodys).getByText(NOBODY_HOLDS)).toBeTruthy());
    expect(within(nobodys).getByText(NOBODY_CAN)).toBeTruthy();

    await userEvent.click(seg("Decided"));
    const past = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(within(past).queryByRole("button")).toBe(null);
  });

  it("gives the seat's own request a sentence and the door to the page that signs where the lane cannot sign, and the buttons where it may decide", async () => {
    const mineOwn: ApprovalRow = { ...base, id: "apr_own", user: "u-alice", username: "alice", mode: "confirm", approverUsers: undefined, createdAt: at(-10), expiresAt: at(110) };
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [mineOwn] });
    const strict = render(
      <TooltipProvider>
        <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} needsDevice={(r) => own(r, seat)} onChanged={changed} />
      </TooltipProvider>,
    );
    const ownRow = await screen.findByRole("button", { name: "Open get-sum for alice" });
    expect(within(ownRow).getByText(YOURS)).toBeTruthy();
    expect(within(ownRow).queryByRole("button")).toBe(null);
    const cell = ownRow.querySelector("[data-own-request]") as HTMLElement;
    expect(cell.textContent).toBe(OWN_REQUEST + " " + OPEN_SELF_SERVICE);
    // The table's cells do not wrap by default, and a cell that does not wrap clips the link out of the 152 px column.
    expect(cell.className).toContain("whitespace-normal");
    expect(within(cell).getByRole("link", { name: OPEN_SELF_SERVICE }).getAttribute("href")).toBe("/self-service/");

    await userEvent.click(ownRow);
    const dialog = await screen.findByRole("dialog");
    expect((dialog.querySelector("[data-who]") as HTMLElement).textContent).toBe("You, as the requester. Only you can confirm it, on a device that signs.");
    expect((dialog.querySelector("[data-own-request]") as HTMLElement).textContent).toBe(OWN_REQUEST + " " + OWN_REQUEST_WHERE + " " + OPEN_SELF_SERVICE);
    expect(within(dialog).queryByRole("button", { name: APPROVE })).toBe(null);
    strict.unmount();

    render(
      <TooltipProvider>
        <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} needsDevice={() => false} onChanged={changed} />
      </TooltipProvider>,
    );
    const relaxed = await screen.findByRole("button", { name: "Open get-sum for alice" });
    expect(relaxed.querySelector("[data-own-request]")).toBe(null);
    expect(within(relaxed).getByRole("button", { name: decideAction("approve", mineOwn) })).toBeTruthy();
  });

  it("counts a held call down in seconds and a standing approval in hours", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    build();
    vi.mocked(listApprovals).mockResolvedValue({ approvals: waitingRows() });
    vi.mocked(pageApprovals).mockResolvedValue({ items: waitingRows(), next_cursor: "" });
    const { container } = mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const clock = (id: string) => (container.querySelector('[data-request="' + id + '"] [data-left]') as HTMLElement).textContent;
    expect(clock("get-sum for joe-java-developer-agent")).toBe("1 m 32 s left");
    expect(clock("get-env for joe-java-developer-agent")).toBe("22 h 00 min left");
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(clock("get-sum for joe-java-developer-agent")).toBe("32 s left");
  });

  it.each([
    ["a held call", "get-sum for joe-java-developer-agent", "hold", "text-warn", "text-warn", "the agent is waiting now"],
    ["a standing approval", "get-env for joe-java-developer-agent", "ticket", "text-teal", "text-foreground", "runs later, after a yes"],
  ])("leads %s with its kind as a chip, the time left at body size and what the agent does beneath", async (_what, name, kind, chipTone, clockTone, does) => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    const head = row(name).querySelector("[data-phase]") as HTMLElement;
    expect(head.getAttribute("data-phase")).toBe("waiting");
    const chip = head.firstElementChild as HTMLElement;
    expect(chip.getAttribute("data-kind")).toBe(kind);
    expect(chip.textContent).toBe(kind);
    expect(chip.className).toContain(chipTone);
    const clock = head.querySelector("[data-left]") as HTMLElement;
    expect(clock.className).toContain("text-base");
    expect(clock.className).toContain(clockTone);
    expect((row(name).querySelector("[data-state-line]") as HTMLElement).textContent).toBe(does);
    expect(WAITING_LINE[kind as "hold" | "ticket"]).toBe(does);
  });

  it("drains a hold's bar with the clock, hides it from assistive technology and draws none for a standing approval", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    build();
    vi.mocked(listApprovals).mockResolvedValue({ approvals: waitingRows() });
    vi.mocked(pageApprovals).mockResolvedValue({ items: waitingRows(), next_cursor: "" });
    const { container } = mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    const bar = (id: string) => container.querySelector('[data-request="' + id + '"] [data-drain]') as HTMLElement | null;
    const share = (id: string) => (bar(id)!.firstElementChild as HTMLElement).style.width;
    // The hold asked 28 s ago and runs out in 92 s, so 92 of 120 seconds are left.
    expect(share("get-sum for joe-java-developer-agent")).toBe("77%");
    expect(bar("get-sum for joe-java-developer-agent")!.getAttribute("aria-hidden")).toBe("true");
    expect(bar("get-env for joe-java-developer-agent")).toBe(null);
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(share("get-sum for joe-java-developer-agent")).toBe("27%");
    await act(async () => { await vi.advanceTimersByTimeAsync(40000); });
    expect(share("get-sum for joe-java-developer-agent")).toBe("0%");
  });

  it("gives every column but Call a width, which leave Call room on a 1280 px page, and never cuts a name", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    const widths = [...container.querySelectorAll("colgroup col")].map((c) => parseInt((c as HTMLElement).style.width, 10));
    expect(widths.length).toBe(6);
    expect(widths.filter((w) => Number.isNaN(w)).length).toBe(1);
    // A 1280 px page less the 220 px sidebar, the two 24 px gutters and the table's border leaves 1010 px,
    // and the Call column needs 145 px of it for a server chip beside a tool name.
    expect(widths.filter((w) => w > 0).reduce((a, b) => a + b, 0)).toBeLessThanOrEqual(1010 - 145);
    const who = within(row("get-sum for joe-java-developer-agent")).getByText("joe-java-developer-agent");
    expect(who.className).not.toContain("truncate");
    expect(who.className).toContain("whitespace-normal");
  });

  it("reads a decided row with the phase word, who decided from where and their reason", async () => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    await userEvent.click(seg("Decided"));
    const line = (name: string) => (row(name).querySelector("[data-state-line]") as HTMLElement).textContent || "";
    const phase = (name: string) => (row(name).querySelector("[data-phase]") as HTMLElement);

    expect(phase("get-sum for joe-java-developer-agent").textContent).toContain("Denied");
    expect(line("get-sum for joe-java-developer-agent")).toContain("by alice from the console");
    expect(line("get-sum for joe-java-developer-agent")).toContain("“walk cleanup”");
    expect(line("get-env for joe-java-developer-agent")).toContain("the same call within 58 min runs");
    expect(line("get-time for joe-java-developer-agent")).toContain("by session 01998f22-c0de…");
    expect(phase("get-time for joe-java-developer-agent").textContent).toContain("Used");
    // A decided row keeps its phase word and carries no kind chip, clock or bar.
    expect(document.querySelector("[data-request] [data-kind], [data-request] [data-left], [data-request] [data-drain]")).toBe(null);
  });

  it("moves the rows and the count words with the filter segment", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(counted()).toBe(waitingCount(4, 2));

    await userEvent.click(seg("Decided"));
    await waitFor(() => expect(counted()).toBe(decidedCount(4)));
    expect(container.querySelectorAll("[data-request]").length).toBe(4);
    expect(screen.queryByRole("button", { name: "Open printf lf-q4-x for joe-java-developer-agent" })).toBe(null);

    await userEvent.click(seg("All"));
    await waitFor(() => expect(counted()).toBe(requestsCount(8)));
    const names = [...container.querySelectorAll("[data-request]")].map((el) => el.getAttribute("data-request"));
    expect(names[0]).toBe("printf lf-q4-x for joe-java-developer-agent");
    expect(names.slice(0, 4)).toEqual([
      "printf lf-q4-x for joe-java-developer-agent",
      "get-sum for joe-java-developer-agent",
      "get-env for joe-java-developer-agent",
      "get-env for sam-sre-agent",
    ]);
  });

  it("finds a row by its rule id and by its call, and says so when nothing matches", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    const box = screen.getByLabelText("Search requests");

    await userEvent.type(box, "hold-printf");
    await waitFor(() => expect(container.querySelectorAll("[data-request]").length).toBe(1));
    expect(container.querySelector("[data-request]")?.getAttribute("data-request")).toBe("printf lf-q4-x for joe-java-developer-agent");

    await userEvent.clear(box);
    await userEvent.type(box, "get-env");
    await waitFor(() => expect(container.querySelectorAll("[data-request]").length).toBe(2));

    await userEvent.clear(box);
    await userEvent.type(box, "nothing-matches-this");
    expect(await screen.findByText(EMPTY_SEARCH)).toBeTruthy();
  });

  it("says what would be here when nothing waits and when nothing was decided", async () => {
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [] });
    vi.mocked(pageApprovals).mockResolvedValue({ items: [], next_cursor: "" });
    mount();
    expect(await screen.findByText(EMPTY_WAITING_TITLE)).toBeTruthy();
    await userEvent.click(seg("Decided"));
    expect(await screen.findByText(EMPTY_DECIDED_TITLE)).toBeTruthy();
  });

  it("keeps the rows on screen when a later read fails, and stands alone when the first one does", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    build();
    vi.mocked(listApprovals)
      .mockResolvedValueOnce({ approvals: waitingRows() })
      .mockRejectedValue(new ApiError("unreachable", 0, true));
    vi.mocked(pageApprovals).mockResolvedValue({ items: waitingRows(), next_cursor: "" });
    const view = mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByRole("button", { name: "Open get-sum for joe-java-developer-agent" })).toBeTruthy();
    await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
    const block = screen.getByRole("status");
    expect(block.textContent).toContain(SUBJECT_REQUESTS + ": unreachable, state unknown.");
    expect(screen.getByRole("button", { name: "Open get-sum for joe-java-developer-agent" })).toBeTruthy();
    view.unmount();

    vi.mocked(listApprovals).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(screen.getByRole("status").textContent).toContain(SUBJECT_REQUESTS);
    expect(screen.queryByRole("button", { name: /^Open / })).toBe(null);
  });

  it("opens a row's dialog on a click, and opens the request named in the address on its own", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" }));
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-request-dialog")).toBe("apr_hold");

    vi.mocked(listApprovals).mockResolvedValue({ approvals: waitingRows() });
    const deep = mount("apr_ticket");
    await waitFor(() => {
      const sheets = [...document.querySelectorAll("[data-request-dialog]")].map((el) => el.getAttribute("data-request-dialog"));
      expect(sheets).toContain("apr_ticket");
    });
    deep.unmount();
  });

  it("asks before it decides, and sends nothing while the question is open or cancelled", async () => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    await userEvent.click(screen.getByRole("button", { name: decideAction("approve", hold) }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(decideTitle("approve", hold))).toBeTruthy();
    expect(vi.mocked(decideApproval)).not.toHaveBeenCalled();
    // The row behind the button never opened its sheet.
    expect(screen.queryByRole("dialog")).toBe(null);

    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBe(null));
    expect(vi.mocked(decideApproval)).not.toHaveBeenCalled();
  });
});
