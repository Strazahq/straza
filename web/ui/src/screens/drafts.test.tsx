import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Drafts } from "./drafts";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type DraftSummary } from "@/lib/api";
import { listDrafts, waitingLabel } from "@/lib/drafts-api";
import { EMPTY, EMPTY_MINE, LEGEND, LOAD_MORE, NOTHING_TO_ACK, NOT_CHECKED_YET, READING_LIST, SUBJECT_LIST, WHOSE } from "@/lib/drafts-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { ALICE, SUMMARY } from "@/test/drafts-fixture";

// The Drafts queue: the Approvals queue's
// shape, with Waiting, Published, Discarded and Expired in the address, a
// Mine and All toggle, the check counts of each row, and a row that opens
// the draft's review page.

vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), listDrafts: vi.fn(), waitingLabel: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const page = (items: DraftSummary[], next = "") => ({ items, next_cursor: next });
const CAROL: DraftSummary = {
  ...SUMMARY, id: "43", title: "Change server demo-tools, role demo-tools-readers and approval set demo-tools-readers-access", door: "console",
  proposer: { ...ALICE, username: "carol" },
  checks: { refused: 0, risks: 0, warnings: 1, unchecked: 0, revision: 1, checked_at: "2026-09-24T10:03:00Z" }, revision: 1,
};
const OLD_CHECK: DraftSummary = { ...SUMMARY, id: "44", checks: { ...SUMMARY.checks!, revision: 1 } };
const mount = (tab?: string) => render(<TooltipProvider><Drafts tab={tab} /></TooltipProvider>);
const row = (id: string) => screen.findByRole("button", { name: "Open draft " + id });

describe("the Drafts queue", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(waitingLabel).mockResolvedValue("3");
    vi.mocked(listDrafts).mockResolvedValue(page([SUMMARY, CAROL, OLD_CHECK]));
  });

  it("says it is reading, then draws one row per draft with every column", async () => {
    mount();
    expect(screen.getByText(READING_LIST)).toBeTruthy();
    const r = await row("41");
    const cell = (col: string) => (r.querySelector('[data-column="' + col + '"]') as HTMLElement).textContent;
    expect(cell("id")).toBe("41");
    expect(cell("what")).toBe(SUMMARY.title);
    expect(cell("who")).toBe("joe-java-developer-agentan agent, sponsored by you");
    expect(cell("door")).toBe("the straza app");
    expect(cell("checks")).toBe("3 widen access1 not checked");
    expect(listDrafts).toHaveBeenCalledWith("state=open&limit=100");
    expect(screen.getByText(LEGEND)).toBeTruthy();
  });

  it("reads a draft with nothing to acknowledge, and one whose check is of an older revision", async () => {
    mount();
    expect((await row("43")).querySelector('[data-column="checks"]')?.textContent).toBe(NOTHING_TO_ACK + "1 warning");
    expect((await row("44")).querySelector('[data-column="checks"]')?.textContent).toBe(NOT_CHECKED_YET);
  });

  it("counts the drafts that wait on the Waiting tab", async () => {
    mount();
    await waitFor(() => expect(screen.getByRole("tab", { name: /Waiting/ }).textContent).toBe("Waiting 3"));
  });

  it("opens a draft's review page from its row", async () => {
    mount();
    await userEvent.click(await row("41"));
    expect(navigate).toHaveBeenCalledWith("drafts", ["41"]);
  });

  it("reads only the reader's own drafts under Mine", async () => {
    mount();
    await row("41");
    await userEvent.click(screen.getByRole("button", { name: WHOSE.mine }));
    await waitFor(() => expect(listDrafts).toHaveBeenLastCalledWith("state=open&mine=true&limit=100"));
  });

  it("moves between the tabs in the address and reads each state", async () => {
    mount();
    await row("41");
    await userEvent.click(screen.getByRole("tab", { name: "Published" }));
    expect(navigate).toHaveBeenCalledWith("drafts", ["published"], true);
  });

  it("reads a decided tab with who decided it", async () => {
    vi.mocked(listDrafts).mockResolvedValue(page([{ ...SUMMARY, state: "published", checks: undefined, decided_by: ALICE, decided_at: new Date().toISOString() }]));
    mount("published");
    const r = await row("41");
    expect(listDrafts).toHaveBeenCalledWith("state=published&limit=100");
    expect(r.querySelector('[data-column="decided"]')?.textContent).toContain("Published by alice");
    expect(r.querySelector('[data-column="checks"]')).toBeNull();
  });

  it("narrows the loaded rows by the filter", async () => {
    mount();
    await row("41");
    await userEvent.type(screen.getByRole("textbox", { name: "Filter drafts" }), "demo-tools-readers");
    await waitFor(() => expect(screen.queryByRole("button", { name: "Open draft 41" })).toBeNull());
    expect(screen.getByRole("button", { name: "Open draft 43" })).toBeTruthy();
  });

  it("walks on to the next page through the cursor", async () => {
    vi.mocked(listDrafts).mockResolvedValueOnce(page([SUMMARY], "c1")).mockResolvedValue(page([CAROL]));
    mount();
    await row("41");
    await userEvent.click(screen.getByRole("button", { name: LOAD_MORE }));
    expect(await row("43")).toBeTruthy();
    expect(listDrafts).toHaveBeenLastCalledWith("state=open&limit=100&cursor=c1");
  });

  it("says what would be here when nothing waits, and what Mine would show", async () => {
    vi.mocked(listDrafts).mockResolvedValue(page([]));
    mount();
    expect(await screen.findByText(EMPTY.waiting.title)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: WHOSE.mine }));
    expect(await screen.findByText(EMPTY_MINE.title)).toBeTruthy();
  });

  it("says why the list could not be read", async () => {
    const err = new ApiError("Straza could not read the drafts. Try again, and read the strazad log if it keeps failing.", 500);
    vi.mocked(listDrafts).mockRejectedValue(err);
    mount();
    expect((await screen.findByRole("status")).textContent).toContain(readFailed(SUBJECT_LIST, err));
    expect(within(document.body).queryByRole("button", { name: /Open draft/ })).toBeNull();
  });
});
