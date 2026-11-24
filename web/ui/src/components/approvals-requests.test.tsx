import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { BeforeSetup } from "./approval-dialog";
import { RequestsTab } from "./approvals-requests";
import { TooltipProvider } from "@/components/ui/tooltip";
import * as SW from "@/self-service/words";
import { ApiError, type ApprovalRow, type RoleRow, decideApproval, getApproval, listApprovals, listRoles, pageApprovals } from "@/lib/api";
import { type Seat, mine, own } from "@/lib/approval-model";
import {
  APPROVE,
  DENY,
  EMPTY_DECIDED_TITLE,
  EMPTY_SEARCH,
  EMPTY_WAITING_TITLE,
  NOBODY_CAN,
  NOBODY_HOLDS,
  OPEN_SELF_SERVICE,
  OWN_REQUEST,
  OWN_REQUEST_WHERE,
  PHASE_WORD,
  REASON_LABEL,
  SUBJECT_REQUESTS,
  WAITING_LINE,
  YOURS,
  YOU_SPONSOR,
  atZero,
  decideAction,
  decidedCount,
  decidesWord,
  requestsCount,
  requestTitle,
  sponsoredWords,
  stateLine,
  waitingCount,
} from "@/lib/approval-words";
import { phaseOf, timeLeft } from "@/lib/approval-model";

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

// before mounts the queue as a page does before any device of the reader is
// set up, with the self-service page's own words.
const setUp = vi.fn();
const closedQueue = vi.fn();
const SETUP: BeforeSetup = { onSetUp: setUp, words: { row: SW.SETUP_TO_ANSWER, line: SW.ANSWER_NEEDS_DEVICE, button: SW.SETUP_BROWSER } };
const before = (openID?: string) => render(
  <TooltipProvider>
    <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} openID={openID} onClose={closedQueue} beforeSetup={SETUP} onChanged={changed} />
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
    setUp.mockClear();
    closedQueue.mockClear();
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
    ["a standing approval", "get-env for joe-java-developer-agent", "ticket", "text-foreground", "text-foreground", "runs later, after a yes"],
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
    expect(clock.className).toContain("text-body");
    expect(clock.className).toContain(clockTone);
    expect((row(name).querySelector("[data-state-line]") as HTMLElement).textContent).toBe(does);
    expect(WAITING_LINE[kind as "hold" | "ticket"]).toBe(does);
  });

  // The chip's tooltip says where the call runs: an MCP call where its lane
  // says, any other call on the agent's machine, as the request's own line does.
  it.each([
    ["an MCP call through the gateway", "get-sum for joe-java-developer-agent", "Runs through the MCP gateway."],
    ["a command the hook held", "printf lf-q4-x for joe-java-developer-agent", "Runs on the agent's machine."],
    ["a command an approval_request ticket asked for through the gateway", "make release for joe-java-developer-agent", "Runs on the agent's machine."],
  ])("says on the chip where %s runs", async (_what, name, runs) => {
    const cmdTicket: ApprovalRow = { ...ticket, id: "apr_cmd", summary: "shell.exec: make release" };
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [...waitingRows(), cmdTicket] });
    vi.mocked(pageApprovals).mockResolvedValue({ items: [...waitingRows(), cmdTicket, ...decidedRows()], next_cursor: "" });
    mount();
    await screen.findByRole("button", { name: "Open " + name });
    const chip = row(name).querySelector("[data-where]") as HTMLElement;
    await userEvent.hover(chip);
    await waitFor(() => {
      const id = chip.getAttribute("aria-describedby");
      expect(id).toBeTruthy();
      expect(document.getElementById(id as string)?.textContent).toBe(runs);
    });
  });

  it.each([
    ["a hold on the gateway", "get-sum for joe-java-developer-agent", "the agent is waiting now"],
    ["a hold the hook raised", "printf lf-q4-x for joe-java-developer-agent", "runs on the agent's retry, after a yes"],
  ])("says under %s what the agent does, by the lane", async (_what, name, does) => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect((row(name).querySelector("[data-state-line]") as HTMLElement).textContent).toBe(does);
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

    expect(phase("get-sum for joe-java-developer-agent").textContent).toBe("denied");
    expect(line("get-sum for joe-java-developer-agent")).toContain("by alice from the console");
    expect(line("get-sum for joe-java-developer-agent")).toContain("“walk cleanup”");
    expect(line("get-env for joe-java-developer-agent")).toContain("58 min left for the same call");
    expect(line("get-time for joe-java-developer-agent")).toContain("by session 01998f22-c0de…");
    expect(phase("get-time for joe-java-developer-agent").textContent).toBe("used");
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
    expect(block.textContent).toContain(SUBJECT_REQUESTS + " could not be read because Straza did not answer.");
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

  it.each([
    ["Deny", "deny" as const],
    ["Approve", "approve" as const],
  ])("opens the request dialog from a row's %s and decides nothing", async (_what, verdict) => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    await userEvent.click(screen.getByRole("button", { name: decideAction(verdict, hold) }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.getAttribute("data-request-dialog")).toBe("apr_hold");
    expect(within(dialog).getByRole("heading").firstChild!.textContent).toBe(requestTitle(hold));
    expect(within(dialog).getByLabelText(REASON_LABEL)).toBeTruthy();
    // No second question opens, and nothing is sent until the dialog's own button.
    expect(screen.queryByRole("alertdialog")).toBe(null);
    expect(vi.mocked(decideApproval)).not.toHaveBeenCalled();
  });

  it("decides from the dialog through the admin lane, then closes it and reads the lists again", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: decideAction("deny", hold) }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(REASON_LABEL), "walk cleanup");
    const reads = vi.mocked(listApprovals).mock.calls.length;
    await userEvent.click(within(within(dialog).getByRole("button", { name: DENY }).parentElement!).getByRole("button", { name: DENY }));
    await waitFor(() => expect(vi.mocked(decideApproval)).toHaveBeenCalledWith("apr_hold", "deny", "walk cleanup"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    expect(changed).toHaveBeenCalled();
    await waitFor(() => expect(vi.mocked(listApprovals).mock.calls.length).toBeGreaterThan(reads));
  });

  it("keeps the dialog open on the final answer when the request was decided elsewhere first", async () => {
    const elsewhere: ApprovalRow = { ...hold, state: "denied", decidedAt: at(-3), decidedBy: "u-bob", decidedByName: "bob", channel: "phone" };
    vi.mocked(decideApproval).mockRejectedValue(new ApiError("already decided", 409));
    vi.mocked(getApproval).mockResolvedValue({ approval: elsewhere });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: decideAction("approve", hold) }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: APPROVE }));
    await waitFor(() => expect(dialog.querySelector("[data-final]")).not.toBe(null));
    // The fresh record stands in for the stored one, so the dialog reads it as decided.
    expect((dialog.querySelector("[data-strip] [data-phase]") as HTMLElement).textContent).toBe("denied");
    expect(within(dialog).getAllByRole("button").filter((b) => b.textContent === APPROVE)).toHaveLength(0);
    expect(screen.getByRole("dialog")).toBe(dialog);
  });

  it("ends a row the reader may decide in the page's words before setup, with its chip and no buttons, and says who decides the others", async () => {
    before();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    for (const name of ["get-sum for joe-java-developer-agent", "get-env for joe-java-developer-agent"]) {
      expect(within(row(name)).queryByRole("button")).toBe(null);
      expect(within(row(name)).getByText(YOURS)).toBeTruthy();
      expect((row(name).querySelector("[data-setup-to-answer]") as HTMLElement).textContent).toBe(SW.SETUP_TO_ANSWER);
    }
    const bobs = row("get-env for sam-sre-agent");
    expect(within(bobs).getByText(decidesWord("bob"))).toBeTruthy();
    expect(bobs.querySelector("[data-setup-to-answer]")).toBe(null);
    await waitFor(() => expect(within(row("printf lf-q4-x for joe-java-developer-agent")).getByText(NOBODY_CAN)).toBeTruthy());
    expect(counted()).toBe(waitingCount(4, 2));
  });

  it.each([
    ["with no request named", undefined],
    ["with a request named in the address", "apr_ticket"],
  ])("draws no filter control before setup and lists the waiting requests only, %s", async (_what, openID) => {
    const { container } = before(openID);
    // A named request opens over the list, so the rows are read off the
    // container rather than by role.
    await waitFor(() => expect(container.querySelectorAll("[data-request]").length).toBe(4));
    await waitFor(() => expect(vi.mocked(pageApprovals)).toHaveBeenCalled());
    expect(container.querySelector("[role=group]")).toBe(null);
    expect([...container.querySelectorAll("button")].map((b) => b.textContent)).not.toContain("Decided");
    expect(screen.getByLabelText("Search requests")).toBeTruthy();
    const names = [...container.querySelectorAll("[data-request]")].map((el) => el.getAttribute("data-request"));
    expect(names).toEqual([
      "printf lf-q4-x for joe-java-developer-agent",
      "get-sum for joe-java-developer-agent",
      "get-env for joe-java-developer-agent",
      "get-env for sam-sre-agent",
    ]);
    expect(counted()).toBe(waitingCount(4, 2));
  });

  it("offers the setup in the dialog of a request the reader may decide before setup", async () => {
    before();
    await userEvent.click(await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog.querySelector("[data-reason-field]")).toBe(null);
    await userEvent.click(within(dialog).getByRole("button", { name: SW.SETUP_BROWSER }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    expect(setUp).toHaveBeenCalledTimes(1);
    expect(closedQueue).toHaveBeenCalledTimes(1);
  });

  it("opens a request the address names after the queue has mounted, once for each name, and says when the dialog closes", async () => {
    const tab = (openID?: string) => (
      <TooltipProvider>
        <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} openID={openID} onClose={closedQueue} onChanged={changed} />
      </TooltipProvider>
    );
    const view = render(tab());
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(screen.queryByRole("dialog")).toBe(null);

    view.rerender(tab("apr_ticket"));
    expect((await screen.findByRole("dialog")).getAttribute("data-request-dialog")).toBe("apr_ticket");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    expect(closedQueue).toHaveBeenCalledTimes(1);

    // The same name again opens nothing, and the next poll does not either.
    view.rerender(tab("apr_ticket"));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("dialog")).toBe(null);

    view.rerender(tab("apr_hold"));
    expect((await screen.findByRole("dialog")).getAttribute("data-request-dialog")).toBe("apr_hold");
  });

  it("keeps the console's five columns and their headers, with the table's floor", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    const heads = [...container.querySelectorAll("thead th")].map((th) => th.textContent);
    expect(heads).toEqual(["Asked", "Who asked", "Call", "Who decides", "State", ""]);
    expect((container.querySelector("[data-slot=table]") as HTMLElement).style.minWidth).toBe("992px");
    expect(container.querySelector("[data-card]")).toBe(null);
  });
});

describe("the requests queue below the sm breakpoint", () => {
  beforeEach(() => {
    build();
    vi.mocked(listApprovals).mockResolvedValue({ approvals: waitingRows() });
    vi.mocked(pageApprovals).mockResolvedValue({ items: [...waitingRows(), ...decidedRows()], next_cursor: "" });
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(decideApproval).mockResolvedValue({ approval: { ...hold, state: "denied" } });
    // Only the query under the sm breakpoint matches, the way a phone answers it.
    vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({
      matches: query === "(max-width: 39.98rem)", media: query, onchange: null,
      addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
    }) as MediaQueryList);
  });
  afterEach(() => { vi.mocked(window.matchMedia).mockRestore(); });

  const cardOf = (name: string) => screen.getByRole("button", { name: "Open " + name });

  it("draws each request as a card, not a table row, and keeps the filter and the count", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(container.querySelector("[data-slot=table]")).toBe(null);
    expect(container.querySelectorAll("[data-card]").length).toBe(4);
    expect(counted()).toBe(waitingCount(4, 2));
    expect(seg("Decided")).toBeTruthy();
  });

  it("reads a waiting card top to bottom: who asked, what it wants to run and where, the time left, then the buttons", async () => {
    mount();
    const card = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    const one = (s: string) => card.querySelector(s) as HTMLElement;
    expect(one("[data-card-meta]").textContent).toMatch(new RegExp("^" + YOU_SPONSOR + " · [23]\\d s ago$"));
    expect(one("[data-card-ask]").textContent).toBe("wants to call");
    const callLine = one("[data-card-call]");
    expect(callLine.textContent).toBe("get-sum");
    expect(callLine.getAttribute("title")).toBe("get-sum");
    for (const cls of ["font-mono", "text-section", "line-clamp-3"]) expect(callLine.className).toContain(cls);
    expect(one("[data-card-where]").textContent).toBe("on demo-tools");
    const time = one("[data-card-time]");
    expect(time.className).toContain("border-warn");
    expect(time.textContent).toMatch(/^1 m [23]\d s left, then the call is denied$/);
    expect(time.textContent!.endsWith(", " + atZero(hold))).toBe(true);
    expect(timeLeft(hold)).toMatch(/ left$/);
    expect(one("[data-drain]").getAttribute("aria-hidden")).toBe("true");
    const deny = within(card).getByRole("button", { name: decideAction("deny", hold) });
    const approve = within(card).getByRole("button", { name: decideAction("approve", hold) });
    expect(deny.parentElement!.className).toContain("grid-cols-2");
    for (const b of [deny, approve]) expect(b.className).toContain("h-11");
    expect(deny.className).toContain("border-danger");
  });

  it.each([
    ["someone else's sponsor", "get-env for sam-sre-agent", sponsoredWords("bob") + " · 1 h ago"],
    ["a request routed to a role", "printf lf-q4-x for joe-java-developer-agent", "1 m ago"],
  ])("names %s in the card's muted line", async (_what, name, meta) => {
    mount();
    await screen.findByRole("button", { name: "Open " + name });
    expect((cardOf(name).querySelector("[data-card-meta]") as HTMLElement).textContent).toBe(meta);
  });

  it("puts who decides, or that nobody can, in place of the buttons on a card the reader may not decide", async () => {
    mount();
    await screen.findByRole("button", { name: "Open get-env for sam-sre-agent" });
    expect(within(cardOf("get-env for sam-sre-agent")).queryByRole("button")).toBe(null);
    expect(within(cardOf("get-env for sam-sre-agent")).getByText(decidesWord("bob"))).toBeTruthy();
    await waitFor(() => expect(within(cardOf("printf lf-q4-x for joe-java-developer-agent")).getByText(NOBODY_CAN)).toBeTruthy());
  });

  it("puts the door to the page that signs on the reader's own request where the lane cannot sign", async () => {
    const mineOwn: ApprovalRow = { ...base, id: "apr_own", user: "u-alice", username: "alice", mode: "confirm", approverUsers: undefined, createdAt: at(-10), expiresAt: at(110) };
    vi.mocked(listApprovals).mockResolvedValue({ approvals: [mineOwn] });
    render(
      <TooltipProvider>
        <RequestsTab seat={seat} isMine={(r) => mine(r, seat)} needsDevice={(r) => own(r, seat)} onChanged={changed} />
      </TooltipProvider>,
    );
    const card = await screen.findByRole("button", { name: "Open get-sum for alice" });
    expect(within(card).queryByRole("button")).toBe(null);
    expect((card.querySelector("[data-own-request]") as HTMLElement).textContent).toBe(OWN_REQUEST + " " + OPEN_SELF_SERVICE);
    expect(within(card).getByRole("link", { name: OPEN_SELF_SERVICE }).getAttribute("href")).toBe("/self-service/");
  });

  it("ends a card the reader may decide in the page's words before setup, with no buttons and no filter, and says who decides the others", async () => {
    const { container } = before();
    const card = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect(within(card).queryByRole("button")).toBe(null);
    expect((card.querySelector("[data-setup-to-answer]") as HTMLElement).textContent).toBe(SW.SETUP_TO_ANSWER);
    expect(within(cardOf("get-env for sam-sre-agent")).getByText(decidesWord("bob"))).toBeTruthy();
    expect(cardOf("get-env for sam-sre-agent").querySelector("[data-setup-to-answer]")).toBe(null);
    expect(container.querySelector("[role=group]")).toBe(null);
    expect(container.querySelectorAll("[data-card]").length).toBe(4);
  });

  it("opens the request dialog from the card and from its two buttons, and decides nothing", async () => {
    mount();
    const card = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    await userEvent.click(within(card).getByRole("button", { name: decideAction("approve", hold) }));
    expect((await screen.findByRole("dialog")).getAttribute("data-request-dialog")).toBe("apr_hold");
    expect(vi.mocked(decideApproval)).not.toHaveBeenCalled();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBe(null));
    cardOf("get-env for sam-sre-agent").focus();
    await userEvent.keyboard("{Enter}");
    expect((await screen.findByRole("dialog")).getAttribute("data-request-dialog")).toBe("apr_foreign");
  });

  it("reads a decided card as who asked, the call and the outcome as the console's row shows it", async () => {
    mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    await userEvent.click(seg("Decided"));
    const card = await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    expect((card.querySelector("[data-card-call]") as HTMLElement).textContent).toBe("get-sum");
    expect((card.querySelector("[data-phase]") as HTMLElement).textContent).toBe(PHASE_WORD.denied);
    expect((card.querySelector("[data-state-line]") as HTMLElement).textContent).toBe(stateLine(denied, phaseOf(denied)));
    expect(card.querySelector("[data-card-time]")).toBe(null);
    expect(within(card).queryByRole("button")).toBe(null);
  });

  it("finds a card by its rule id, and says so when nothing matches", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open get-sum for joe-java-developer-agent" });
    await userEvent.type(screen.getByLabelText("Search requests"), "hold-printf");
    await waitFor(() => expect(container.querySelectorAll("[data-card]").length).toBe(1));
    await userEvent.type(screen.getByLabelText("Search requests"), "-nothing");
    expect(await screen.findByText(EMPTY_SEARCH)).toBeTruthy();
  });
});
