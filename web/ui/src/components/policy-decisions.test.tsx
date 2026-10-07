import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyDecisions } from "./policy-decisions";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AuditRow, listAudit } from "@/lib/api";
import { ANY_OUTCOME, ANY_WHO, BUCKET_WORD, DEC_FILTER, NO_DECISIONS, NO_DECISIONS_HERE, SINCE } from "@/lib/policy-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listAudit: vi.fn(),
}));

const NAME = "dev-guardrails";
const QUERY = "q=" + encodeURIComponent('"setName":"' + NAME + '"') + "&limit=100&order=desc";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const HOUR = 3600000;
const DAY = 86400000;

// record writes one chain row the way the audit route returns it, so the
// tab is tested on the reading the Audit screen renders.
const record = (seq: number, username: string, data: Record<string, unknown>, time: string, type = "straza.audit.tool"): AuditRow => ({
  seq,
  ce: JSON.stringify({ specversion: "1.0", type, source: "straza", time, data }),
  hash: "h" + seq,
  prevHash: "h" + (seq - 1),
  username,
});

// The newest window of one policy: two refusals an hour old, one call joe
// is waiting on, and one allow from three days back.
const ROWS: AuditRow[] = [
  record(70, "joe", { tool: "shell.exec", command: "rm -rf ./build", effect: "deny", ruleId: "no-destructive-delete", setName: NAME, session: "s1" }, ago(HOUR)),
  record(69, "alice", { tool: "mcp.call", app: "demo-tools", toolName: "get-env", effect: "deny", ruleId: "no-env", setName: NAME, session: "s2" }, ago(2 * HOUR)),
  record(68, "joe", { tool: "mcp.call", app: "midpoint", toolName: "assign_role", effect: "approve", ruleId: "iga-writes", setName: NAME, session: "s3" }, ago(3 * HOUR)),
  record(67, "alice", { tool: "shell.exec", command: "ls", effect: "allow", ruleId: "dev-reads", setName: NAME, session: "s4" }, ago(3 * DAY)),
];

const mount = () => render(<TooltipProvider><PolicyDecisions name={NAME} onOpenAudit={() => {}} /></TooltipProvider>);

const seqs = () => [...document.querySelectorAll("[data-decision]")].map((r) => r.getAttribute("data-decision"));
const outcomes = () => [...document.querySelectorAll("[data-outcome]")].map((b) => b.textContent);
const empty = () => (document.querySelector("[data-empty-text]") as HTMLElement | null)?.textContent;

// choose drives one of the three filters by the word its trigger carries.
async function choose(filter: string, option: string) {
  await userEvent.click(screen.getByRole("combobox", { name: filter }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

describe("the Decisions tab", () => {
  beforeEach(() => {
    vi.mocked(listAudit).mockReset().mockResolvedValue(ROWS);
  });

  it("reads the newest window of the records this policy decided", async () => {
    mount();
    await waitFor(() => expect(seqs().length).toBe(3));
    expect(listAudit).toHaveBeenCalledWith(QUERY);
    expect(document.querySelector("[data-decisions-tab]")).toBeTruthy();
    expect(seqs()).toEqual(["70", "69", "68"]);
    expect(outcomes()).toEqual([BUCKET_WORD.deny, BUCKET_WORD.deny, BUCKET_WORD.hum]);
    const first = document.querySelector("[data-decision=\"70\"]") as HTMLElement;
    expect(first.textContent).toContain("joe");
    expect(first.textContent).toContain("shell: rm -rf ./build");
    expect(first.textContent).toContain("no-destructive-delete");
  });

  it("carries the three filters, each with the choice that narrows nothing", async () => {
    mount();
    await waitFor(() => expect(seqs().length).toBe(3));
    expect([...document.querySelectorAll("[data-dec-filter]")].map((e) => e.getAttribute("data-dec-filter")))
      .toEqual(["who", "outcome", "since"]);
    expect(screen.getByRole("combobox", { name: DEC_FILTER.who }).textContent).toBe(ANY_WHO);
    expect(screen.getByRole("combobox", { name: DEC_FILTER.outcome }).textContent).toBe(ANY_OUTCOME);
    expect(screen.getByRole("combobox", { name: DEC_FILTER.since }).textContent).toBe(SINCE.day);
  });

  it("narrows to one person, and offers only the people the window holds", async () => {
    mount();
    await waitFor(() => expect(seqs().length).toBe(3));
    await userEvent.click(screen.getByRole("combobox", { name: DEC_FILTER.who }));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([ANY_WHO, "alice", "joe"]);
    await userEvent.click(await screen.findByRole("option", { name: "joe" }));
    await waitFor(() => expect(seqs()).toEqual(["70", "68"]));
    await choose(DEC_FILTER.who, ANY_WHO);
    await waitFor(() => expect(seqs().length).toBe(3));
  });

  it("narrows to one outcome", async () => {
    mount();
    await waitFor(() => expect(seqs().length).toBe(3));
    await choose(DEC_FILTER.outcome, BUCKET_WORD.hum);
    await waitFor(() => expect(seqs()).toEqual(["68"]));
    await choose(DEC_FILTER.outcome, BUCKET_WORD.allow);
    await waitFor(() => expect(seqs()).toEqual([]));
    expect(empty()).toBe(NO_DECISIONS_HERE);
  });

  it("opens Since to reach a decision older than a day", async () => {
    mount();
    await waitFor(() => expect(seqs().length).toBe(3));
    await choose(DEC_FILTER.since, SINCE.week);
    await waitFor(() => expect(seqs()).toEqual(["70", "69", "68", "67"]));
    await choose(DEC_FILTER.since, SINCE.all);
    await waitFor(() => expect(seqs().length).toBe(4));
  });

  it("says nothing has been decided when the chain holds no record of this policy", async () => {
    vi.mocked(listAudit).mockResolvedValue([]);
    mount();
    await waitFor(() => expect(empty()).toBe(NO_DECISIONS));
  });

  it("opens the record sheet on a row, with the why of that record", async () => {
    mount();
    await waitFor(() => expect(seqs().length).toBe(3));
    await userEvent.click(screen.getByRole("button", { name: "Open 69" }));
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-record-sheet")).toBe("69");
    expect(within(sheet).getByRole("heading", { name: "Record 69 tool" })).toBeTruthy();
    expect((sheet.querySelector("[data-why]") as HTMLElement).textContent)
      .toContain("Decided by rule no-env in policy " + NAME);
  });
});
