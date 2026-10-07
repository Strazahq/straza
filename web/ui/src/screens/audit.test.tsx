import { createHash } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Audit } from "./audit";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AuditRow, listAudit, listUsers, overview, revokeSession } from "@/lib/api";
import { chainBroken, chainVerified, loadedWords, revokeBody, searchLine } from "@/lib/audit-words";

// The Audit screen over a chain whose hashes really verify: every fixture
// is linked the way internal/audit.Link writes it, hash = sha256(prevHash
// + "\n" + ce), so the browser's re-hashing here is the check it runs
// against strazad.

const SESSION = "0199cf12-4b1e-7a3c-9d21-0b6e4f2a8c10";
const USER_ID = "0198b2c1-77aa-7f00-8e12-3c4d5e6f7a80";
const JOE = "joe-java-developer-agent";
const SECRET = "the registry key is AKIAIOSFODNN7EXAMPLE";

type Seed = { ce: unknown; username?: string; decidedByUsername?: string };

// chain links seeds into contiguous records from startSeq, each one
// hashing the record before it.
function chain(startSeq: number, seeds: Seed[], startPrev = ""): AuditRow[] {
  let prev = startPrev;
  return seeds.map((s, i) => {
    const ce = JSON.stringify(s.ce);
    const hash = createHash("sha256").update(prev + "\n" + ce).digest("hex");
    const row: AuditRow = { seq: startSeq + i, ce, hash, prevHash: prev, username: s.username, decidedByUsername: s.decidedByUsername };
    prev = hash;
    return row;
  });
}

const read = (time: string): Seed => ({ ce: { type: "straza.audit.tool", time, data: { session: SESSION, user: USER_ID, tool: "read", paths: ["src/main/java/Billing.java"], effect: "allow", ruleId: "dev-reads" } }, username: JOE });

// The window the tail bootstraps on: an approval, a capture record that
// still carries its text, a held MCP call with no reason of its own, a
// refusal, a critical sentinel verdict, three identical reads and an
// admin action, in the order the chain wrote them.
const SEEDS: Seed[] = [
  { ce: { type: "straza.audit.approval", time: "2026-09-12T10:31:45Z", data: { summary: "mcp.call midpoint:assign_role", state: "approved", decidedBy: "0198b2c1-9999-7f00-8e12-3c4d5e6f7a80", decidedReason: "reviewed the target, fine", rule: "iga-writes", user: USER_ID } }, username: JOE, decidedByUsername: "judy" },
  { ce: { type: "straza.audit.prompt", time: "2026-09-12T10:44:20Z", data: { session: SESSION, user: USER_ID, contentBytes: 412, mode: "redact", contentHash: "sha256:8f43c1a09b2e7d5f4411", content: SECRET } }, username: JOE },
  { ce: { type: "straza.audit.mcp", time: "2026-09-12T10:44:58Z", data: { session: SESSION, user: USER_ID, app: "midpoint", toolName: "assign_role", effect: "approve", ruleId: "iga-writes", setName: "iga" } }, username: JOE },
  { ce: { type: "straza.audit.tool", time: "2026-09-12T10:45:01Z", data: { session: SESSION, user: USER_ID, tool: "shell.exec", command: "rm -rf ./build", effect: "deny", ruleId: "no-destructive-delete", setName: "dev-guardrails", reason: "Straza: destructive delete is refused for role dev" } }, username: JOE },
  { ce: { type: "straza.audit.sentinel", time: "2026-09-12T10:45:30Z", data: { session: SESSION, detector: "burst-deny", severity: "critical", evidence: [1, 2, 3, 4, 5, 6, 7, 8, 9], reason: "9 refusals in 60 s in one session" } } },
  read("2026-09-12T10:49:50Z"),
  read("2026-09-12T10:49:51Z"),
  read("2026-09-12T10:49:52Z"),
  { ce: { type: "straza.audit.admin", time: "2026-09-12T10:51:07Z", data: { action: "user.unlock carol", user: "0198b2c1-0000-7f00-8e12-3c4d5e6f7a80" } }, username: "alice" },
];

// The three records before the window, so Load older has a batch that
// links into the oldest record already held.
const OLDER_SEEDS: Seed[] = [
  { ce: { type: "straza.audit.tool", time: "2026-09-12T10:38:00Z", data: { session: SESSION, user: USER_ID, tool: "read", paths: ["deploy/helm/values.yaml"], effect: "allow", ruleId: "sre-reads" } }, username: "sam-sre-agent" },
  { ce: { type: "straza.audit.tool", time: "2026-09-12T10:38:02Z", data: { session: SESSION, user: USER_ID, tool: "read", paths: ["deploy/helm/Chart.yaml"], effect: "allow", ruleId: "sre-reads" } }, username: "sam-sre-agent" },
  { ce: { type: "straza.audit.tool", time: "2026-09-12T10:38:04Z", data: { session: SESSION, user: USER_ID, tool: "read", paths: ["deploy/helm/templates/deployment.yaml"], effect: "allow", ruleId: "sre-reads" } }, username: "sam-sre-agent" },
];

const OLDER = chain(4208, OLDER_SEEDS);
const WINDOW = chain(4211, SEEDS, OLDER[OLDER.length - 1].hash);
const HEAD = 4219;

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listAudit: vi.fn(),
  listUsers: vi.fn(),
  overview: vi.fn(),
  revokeSession: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

// serve answers each query the screen builds: the newest window, a live
// tick with nothing new, the batch before the window, or the server
// search when a filter is set.
const serve = (q: string, hits: AuditRow[] = []) => {
  const p = new URLSearchParams(q);
  if (p.get("limit") === "1000") return [];
  if (p.get("order") !== "desc") return OLDER;
  if (p.get("q") || p.get("effect") || p.get("user") || p.get("type")) return hits;
  return WINDOW.slice().reverse();
};

const failure = (message: string, status: number, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

const mount = () => render(<TooltipProvider><Audit /></TooltipProvider>);

// rows returns the seq of every row on screen, in table order.
const rows = () => screen.queryAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-seq")).map((b) => b.getAttribute("data-seq"));

// cell reads one column of one row, by the seq the row carries.
const cell = (seq: number, index: number) => {
  const row = document.querySelector("[data-seq=\"" + seq + "\"]") as HTMLElement;
  return (row.children[index] as HTMLElement).textContent || "";
};

const SEQ = 0, TIME = 1, WHO = 2, TYPE = 3, EFFECT = 4, WHAT = 5, REASON = 6;

describe("the Audit screen", () => {
  beforeEach(() => {
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(serve(q)));
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: HEAD } });
    vi.mocked(revokeSession).mockResolvedValue({ status: "revoked" });
  });

  it("does not report a verified or empty chain before the first read finishes", async () => {
    let finish!: (rows: AuditRow[]) => void;
    vi.mocked(listAudit).mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    mount();
    expect(screen.getByRole("status").textContent).toContain("Checking audit chain");
    expect(screen.queryByText("Chain verified")).toBeNull();
    expect(screen.queryByText(/Nothing has been decided/)).toBeNull();
    finish(WINDOW.slice().reverse());
    await screen.findByRole("button", { name: "Open 4219" });
    expect(screen.getByRole("status").textContent).toContain("Chain verified");
  });

  it("does not report verification when the initial audit read fails", async () => {
    vi.mocked(listAudit).mockRejectedValue(failure("unreachable", 0, true));
    mount();
    await screen.findByText("Audit unavailable");
    expect(screen.queryByText("Chain verified")).toBeNull();
    expect(screen.getByText("No audit records available.")).toBeTruthy();
  });

  it("bootstraps the tail from the newest window, renders it newest first, and says the chain is verified", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Audit");
    expect(vi.mocked(listAudit).mock.calls[0][0]).toBe("order=desc&limit=200");
    expect(rows()).toEqual(["4219", "4218", "4215", "4214", "4213", "4212", "4211"]);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("Chain verified" + chainVerified(9, HEAD)));
    expect(screen.getByRole("status").getAttribute("data-chain-line")).toBe("ok");
    expect(screen.getByText(loadedWords(9, HEAD))).toBeTruthy();
    expect(screen.getByText("7 rows shown")).toBeTruthy();
    expect(cell(4214, SEQ)).toBe("4214");
    expect(cell(4214, TIME)).toBe("10:45:01");
    expect(cell(4214, TYPE)).toBe("tool");
    // A refusal and a critical verdict wear the danger edge; an allowance does not.
    expect(document.querySelector("[data-seq=\"4214\"]")?.className).toContain("border-l-danger");
    expect(document.querySelector("[data-seq=\"4215\"]")?.className).toContain("border-l-danger");
    expect(document.querySelector("[data-seq=\"4218\"]")?.className).not.toContain("border-l-danger");
    // The refusal alone is tinted, on the stripe of an even row too.
    expect(document.querySelector("[data-seq=\"4214\"]")?.className).toContain("even:bg-danger-bg");
    expect(document.querySelector("[data-seq=\"4215\"]")?.className).not.toContain("bg-danger-bg");
    expect(document.querySelector("[data-seq=\"4218\"]")?.className).not.toContain("bg-danger-bg");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("fixes the column widths, so the table fits a 1280 px viewport and the headers stay put", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    expect(document.querySelector("table")?.className).toContain("table-fixed");
    expect([...document.querySelectorAll("colgroup col")].map((c) => (c as HTMLElement).style.width)).toEqual(["68px", "84px", "236px", "88px", "96px", "28%", ""]);
  });

  it.each([
    { seq: 4218, kind: "an allowance", tone: "ok", word: "allow" },
    { seq: 4214, kind: "a refusal", tone: "danger", word: "deny" },
    { seq: 4213, kind: "a held call", tone: "warn", word: "needs approval" },
    { seq: 4211, kind: "an approval", tone: "ok", word: "approved" },
    { seq: 4215, kind: "a critical verdict", tone: "danger", word: "critical" },
  ])("shows the effect of $kind as a chip in its trust tone and leaves its row bright", async ({ seq, tone, word }) => {
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    const row = document.querySelector("[data-seq=\"" + seq + "\"]") as HTMLElement;
    const chip = row.querySelector("[data-column=\"effect\"] [data-tone]") as HTMLElement;
    expect(chip.getAttribute("data-tone")).toBe(tone);
    expect(chip.textContent).toBe(word);
    expect(row.querySelector("[data-column=\"seq\"] div")?.className).not.toContain("text-muted-foreground");
    expect(row.querySelector("[data-column=\"what\"] div")?.className).not.toContain("text-muted-foreground");
  });

  it.each([
    { seq: 4219, kind: "an admin action" },
    { seq: 4212, kind: "a prompt" },
  ])("sets $kind, which decided nothing, back in the muted tone", async ({ seq }) => {
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    const row = document.querySelector("[data-seq=\"" + seq + "\"]") as HTMLElement;
    expect(row.querySelector("[data-column=\"effect\"] [data-tone]")).toBeNull();
    expect(cell(seq, EFFECT)).toBe("none");
    for (const column of ["seq", "what"]) expect(row.querySelector("[data-column=\"" + column + "\"] div")?.className).toContain("text-muted-foreground");
    const who = row.querySelector("[data-column=\"who\"] button") as HTMLElement;
    expect(who.className).toContain("text-muted-foreground");
    expect(who.className).not.toContain("text-link");
    // A decision beside it keeps the link tone on its name and leads with the call.
    const decided = document.querySelector("[data-seq=\"4214\"]") as HTMLElement;
    expect(decided.querySelector("[data-column=\"who\"] button")?.className).toContain("text-link");
    expect(decided.querySelector("[data-column=\"what\"] div")?.className).toContain("font-semibold");
  });

  it("counts the loaded records against the newest record it holds when the overview cannot be read", async () => {
    vi.mocked(overview).mockRejectedValue(failure("unreachable", 0, true));
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    await waitFor(() => expect(screen.getByText(loadedWords(9, 4219))).toBeTruthy());
  });

  it("renders the broken alert with the seq of the record that fails re-hashing", async () => {
    const altered = WINDOW.map((r) => (r.seq === 4214 ? { ...r, ce: r.ce.replace("rm -rf ./build", "rm -rf /") } : r));
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(new URLSearchParams(q).get("order") === "desc" ? altered.slice().reverse() : []));
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe(chainBroken(4214));
    expect(screen.queryByText(/Chain verified/)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Open 4214" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("Not verified in this browser. See the audit chain status for details.")).toBeTruthy();
    expect(within(sheet).queryByText("Hash matches the loaded chain.")).toBeNull();
  });

  it("opens the record sheet on a row and keeps the row marked while it is open", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4214" });
    await userEvent.click(screen.getByRole("button", { name: "Open 4214" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByRole("heading", { name: /Record 4214/ })).toBeTruthy();
    expect(document.querySelector("[data-seq=\"4214\"]")?.getAttribute("data-open")).toBe("true");
    // The open row wears the fill of the selected state in place of the tint.
    expect(document.querySelector("[data-seq=\"4214\"]")?.className).toContain("bg-accent-bg");
    expect(document.querySelector("[data-seq=\"4214\"]")?.className).not.toContain("bg-danger-bg");
  });

  it("renders an approval as its verdict and decider, and falls back to the rule id when a decision carries no reason", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4211" });
    expect(cell(4211, WHAT)).toBe("joe-java-developer-agent's assign role in midpoint approved by judy (rule iga-writes)");
    expect(cell(4211, EFFECT)).toBe("approved");
    expect(cell(4211, REASON)).toBe("reviewed the target, fine");
    expect(cell(4213, EFFECT)).toBe("needs approval");
    expect(cell(4213, REASON)).toBe("rule iga-writes");
    expect(cell(4214, EFFECT)).toBe("deny");
    expect(cell(4214, REASON)).toBe("Straza: destructive delete is refused for role dev");
  });

  it("renders a capture record as its reference row and never the text it witnessed", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4212" });
    expect(cell(4212, WHAT)).toBe("content 412 B · redact · 8f43c1a09b2e…");
    expect(document.body.textContent).not.toContain("AKIAIOSFODNN7EXAMPLE");
  });

  it("narrows the tail to the chosen lens, renders its note, and says so when the lens hides everything", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    await userEvent.click(screen.getByRole("combobox", { name: "Lens" }));
    await userEvent.click(screen.getByRole("option", { name: "recording" }));
    expect(rows()).toEqual(["4212"]);
    expect(screen.getByText("recording = type prompt and reply: conversation witnesses, size and hash only, never the text")).toBeTruthy();
    await userEvent.click(screen.getByRole("combobox", { name: "Lens" }));
    await userEvent.click(screen.getByRole("option", { name: "policy" }));
    expect(rows()).toEqual([]);
    expect(screen.getByText(/Nothing in the loaded tail under this lens/)).toBeTruthy();
  });

  it("folds a run of identical rows into one and shows every row of the run when it is expanded", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4218" });
    expect(cell(4218, WHAT)).toContain("×3 identical · show all");
    await userEvent.click(screen.getByRole("button", { name: "×3 identical · show all" }));
    expect(rows()).toEqual(["4219", "4218", "4217", "4216", "4215", "4214", "4213", "4212", "4211"]);
    await userEvent.click(screen.getByRole("button", { name: "collapse" }));
    expect(rows()).toEqual(["4219", "4218", "4215", "4214", "4213", "4212", "4211"]);
  });

  it("asks for the window before the oldest record it holds and puts it in front", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4211" });
    await userEvent.click(screen.getByRole("button", { name: "Load older" }));
    await screen.findByRole("button", { name: "Open 4210" });
    expect(vi.mocked(listAudit).mock.calls.some((c) => c[0] === "after=4010&limit=200")).toBe(true);
    expect(rows().slice(-3)).toEqual(["4210", "4209", "4208"]);
    await waitFor(() => expect(screen.getByText(loadedWords(12, HEAD))).toBeTruthy());
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("switches to the server search on what is typed and on the effect, and returns to the tail when both are cleared", async () => {
    const hit = WINDOW.filter((r) => r.seq === 4214);
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(serve(q, hit)));
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    const box = screen.getByRole("textbox", { name: "Search the chain" });
    await userEvent.type(box, "rm -rf");
    await waitFor(() => expect(rows()).toEqual(["4214"]));
    expect(vi.mocked(listAudit).mock.calls.some((c) => c[0] === "order=desc&limit=200&q=rm%20-rf")).toBe(true);
    expect(screen.getByRole("status").textContent).toBe("Whole-chain search" + searchLine("rm -rf", "", "", false));
    expect(screen.getByRole("status").getAttribute("data-chain-line")).toBe("unknown");
    expect(screen.queryByText(/Chain verified/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Load older" })).toBeNull();
    expect(screen.getByText("1 match")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "deny" }));
    await waitFor(() => expect(vi.mocked(listAudit).mock.calls.some((c) => c[0] === "order=desc&limit=200&q=rm%20-rf&effect=deny")).toBe(true));
    expect(screen.getByRole("button", { name: "deny" }).getAttribute("aria-pressed")).toBe("true");

    await userEvent.click(screen.getByRole("button", { name: "any effect" }));
    await userEvent.clear(box);
    await waitFor(() => expect(rows()).toEqual(["4219", "4218", "4215", "4214", "4213", "4212", "4211"]));
    expect(screen.getByRole("status").textContent).toBe("Chain verified" + chainVerified(9, HEAD));
  });

  it("sets the user filter from a name in the Who column and sends it to the server", async () => {
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(serve(q, WINDOW.filter((r) => r.seq === 4219))));
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    expect(cell(4215, WHO)).toBe("none");
    await userEvent.click(screen.getByRole("button", { name: "alice" }));
    await waitFor(() => expect(rows()).toEqual(["4219"]));
    expect(vi.mocked(listAudit).mock.calls.some((c) => c[0] === "order=desc&limit=200&user=alice")).toBe(true);
    expect(screen.getByRole("button", { name: "Clear the user filter" })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toBe("Whole-chain search" + searchLine("", "", "alice", false));
  });

  it("revokes the session a critical sentinel flagged, and leaves the refusal on the confirm when the server refuses", async () => {
    vi.mocked(revokeSession).mockRejectedValueOnce(failure("session 0199cf12 is already revoked", 409));
    mount();
    await screen.findByRole("button", { name: "Open 4215" });
    expect(cell(4215, WHAT)).toBe("burst-deny sentinel verdict · evidence ×9");
    await userEvent.click(screen.getByRole("button", { name: "Revoke session" }));
    const ask = await screen.findByRole("alertdialog");
    expect(within(ask).getByRole("heading", { name: "Revoke session?" })).toBeTruthy();
    expect(ask.textContent).toContain(revokeBody(SESSION, "burst-deny"));

    await userEvent.click(within(ask).getByRole("button", { name: "Revoke session" }));
    const refusal = await within(ask).findByRole("alert");
    expect(refusal.textContent).toBe("Revoke session refused. The server refused it: session 0199cf12 is already revoked. Fix what it names, then try again.");
    expect(screen.queryByText("session revoked")).toBeNull();

    await userEvent.click(within(ask).getByRole("button", { name: "Revoke session" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(revokeSession).toHaveBeenLastCalledWith(SESSION);
    expect(cell(4215, REASON)).toBe("9 refusals in 60 s in one sessionsession revoked");
  });

  it("opens on the subject a preset names, in server search", async () => {
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(serve(q, WINDOW.filter((r) => r.seq === 4214))));
    render(<TooltipProvider><Audit preset={{ user: JOE, session: SESSION }} /></TooltipProvider>);
    await waitFor(() => expect(rows()).toEqual(["4214"]));
    expect(vi.mocked(listAudit).mock.calls.some((c) => c[0] === "order=desc&limit=200&q=" + SESSION + "&user=" + JOE)).toBe(true);
    expect(screen.getByRole("button", { name: "Clear the user filter" })).toBeTruthy();
  });

  it("keeps the rows on screen and says what failed when a reload cannot reach strazad", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    vi.mocked(listAudit).mockRejectedValueOnce(failure("unreachable", 0, true));
    await userEvent.click(screen.getByRole("button", { name: "Reload the chain" }));
    await waitFor(() => expect(document.querySelector("[data-fetch-error]")?.textContent).toContain("The audit chain could not be read because strazad did not answer."));
    expect(rows()).toEqual(["4219", "4218", "4215", "4214", "4213", "4212", "4211"]);
  });

  it("exports the rows on screen as CSV, with the header and one line per row", async () => {
    let blob: Blob | null = null;
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: (b: Blob) => { blob = b; return "#"; } });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => {} });
    mount();
    await screen.findByRole("button", { name: "Open 4219" });
    await userEvent.click(screen.getByRole("button", { name: "Export loaded rows" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "CSV" }));
    await waitFor(() => expect(blob).not.toBeNull());
    const text = await (blob as unknown as Blob).text();
    const lines = text.trim().split("\n");
    // Every record the lens leaves on screen, unfolded, newest first.
    expect(lines.length).toBe(10);
    expect(lines[0]).toBe("seq,time,user,session,type,effect,what,reason");
    expect(lines[1]).toBe("4219,2026-09-12T10:51:07Z,alice,,straza.audit.admin,,user.unlock carol,");
    expect(lines[2]).toBe("4218,2026-09-12T10:49:52Z," + JOE + "," + SESSION + ",straza.audit.tool,allow,src/main/java/Billing.java,rule dev-reads");
    expect(lines[9]).toBe("4211,2026-09-12T10:31:45Z," + JOE + ",,straza.audit.approval,approved,joe-java-developer-agent's assign role in midpoint approved by judy (rule iga-writes),\"reviewed the target, fine\"");
  });
});

// The live tail on fake timers, so each read and each step of the reveal is
// told apart. tick moves the clock. until waits in real time for the work no
// timer drives: a mocked read and WebCrypto's re-hashing of what it answered.
const realTimeout = setTimeout;
const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const until = async (ok: () => boolean) => {
  for (let i = 0; i < 300 && !ok(); i++) await act(async () => { await new Promise((done) => realTimeout(done, 10)); });
};
// loaded waits until the window holds n records, counted from seq 4211.
const loaded = (n: number) => until(() => !!screen.queryByText(loadedWords(n, HEAD - 9 + n)));

// batch is n distinct reads that link to the newest record of the window,
// so no two of them fold into one row.
const batch = (n: number) => chain(HEAD + 1, Array.from({ length: n }, (_, i): Seed => (
  { ce: { type: "straza.audit.tool", time: "2026-09-12T10:52:00Z", data: { session: SESSION, user: USER_ID, tool: "read", paths: ["src/File" + i + ".java"], effect: "allow", ruleId: "dev-reads" } }, username: JOE }
)), WINDOW[WINDOW.length - 1].hash);

// tailServes answers the first read after the window with the batch and
// every later one with nothing new.
const tailServes = (fresh: AuditRow[]) => vi.mocked(listAudit).mockImplementation((q: string) => {
  const after = new URLSearchParams(q).get("after");
  if (after === String(HEAD) && new URLSearchParams(q).get("limit") === "1000") return Promise.resolve(fresh);
  return Promise.resolve(serve(q));
});

const reads = (prefix: string) => vi.mocked(listAudit).mock.calls.filter((c) => c[0].startsWith(prefix)).length;
// arrived counts the rows of the batch that are on screen.
const arrived = () => rows().filter((seq) => Number(seq) > HEAD).length;

describe("the Audit tail", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(listAudit).mockReset().mockImplementation((q: string) => Promise.resolve(serve(q)));
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: HEAD } });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it("reads the tail every second and a search every three seconds", async () => {
    mount();
    await loaded(9);
    expect(rows()[0]).toBe("4219");
    expect(screen.getByRole("status").getAttribute("title")).toBe("Every loaded record was re-hashed in this browser and linked to its predecessor. Live tail, polled every second.");
    expect(reads("after=")).toBe(0);
    await tick(999);
    expect(reads("after=")).toBe(0);
    await tick(1);
    expect(reads("after=4219&limit=1000")).toBe(1);
    await tick(1000);
    expect(reads("after=4219&limit=1000")).toBe(2);

    // A search stops the tail reads and is read again every three seconds.
    fireEvent.click(screen.getByRole("button", { name: "deny" }));
    await until(() => !!screen.queryByText("No record in the chain matches this search."));
    expect(reads("order=desc&limit=200&effect=deny")).toBe(1);
    await tick(2999);
    expect(reads("order=desc&limit=200&effect=deny")).toBe(1);
    await tick(1);
    expect(reads("order=desc&limit=200&effect=deny")).toBe(2);
    expect(reads("after=")).toBe(2);
  });

  it.each([
    { n: 4, step: 1, gap: 250 },
    { n: 25, step: 3, gap: 100 },
    { n: 120, step: 12, gap: 100 },
  ])("reveals a batch of $n records $step at a time and ends inside one tail interval", async ({ n, step, gap }) => {
    tailServes(batch(n));
    mount();
    await loaded(9);
    await tick(1000);
    await loaded(9 + n);
    // The batch is checked and counted when it lands. Its rows follow.
    expect(screen.getByRole("status").textContent).toBe("Chain verified" + chainVerified(9 + n, HEAD + n));
    expect(arrived()).toBe(step);
    expect(rows()[0]).toBe(String(HEAD + step));
    await tick(gap - 1);
    expect(arrived()).toBe(step);
    await tick(1);
    expect(arrived()).toBe(2 * step);
    // Every row is on screen before the next tail read is due.
    await tick(999 - gap);
    expect(arrived()).toBe(n);
    expect(reads("after=")).toBe(1);
    expect(document.querySelector("[data-seq=\"" + (HEAD + n) + "\"]")?.className).toContain("motion-safe:animate-in");
    expect(document.querySelector("[data-seq=\"" + HEAD + "\"]")?.className).not.toContain("animate-in");
  });

  it("exports every loaded record while the reveal is still under way", async () => {
    let blob: Blob | null = null;
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: (b: Blob) => { blob = b; return "#"; } });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: () => {} });
    tailServes(batch(4));
    mount();
    await loaded(9);
    await tick(1000);
    await loaded(13);
    expect(arrived()).toBe(1);
    fireEvent.keyDown(screen.getByRole("button", { name: "Export loaded rows" }), { key: "Enter" });
    fireEvent.click(screen.getByRole("menuitem", { name: "CSV" }));
    expect(blob).not.toBeNull();
    const lines = (await (blob as unknown as Blob).text()).trim().split("\n");
    expect(lines.length).toBe(1 + 9 + 4);
    expect(lines[1].startsWith("4223,")).toBe(true);
  });

  it("shows the whole batch at once when the person asked for reduced motion", async () => {
    vi.spyOn(window, "matchMedia").mockImplementation((query: string) => ({ matches: query === "(prefers-reduced-motion: reduce)" }) as MediaQueryList);
    tailServes(batch(25));
    mount();
    await loaded(9);
    await tick(1000);
    await loaded(34);
    expect(arrived()).toBe(25);
    expect(document.querySelector("[data-seq=\"4244\"]")?.className).not.toContain("animate-in");
  });

  it("shows the broken alert at once when a batch fails re-hashing, with no row waiting on the reveal", async () => {
    const altered = batch(25).map((r) => (r.seq === 4230 ? { ...r, ce: r.ce.replace("File10", "File99") } : r));
    tailServes(altered);
    mount();
    await loaded(9);
    expect(screen.queryByRole("alert")).toBeNull();
    await tick(1000);
    // The clock stands still from here: the alert and the rows need no step of the reveal.
    await until(() => !!screen.queryByRole("alert"));
    expect(screen.getByRole("alert").textContent).toBe(chainBroken(4230));
    expect(screen.queryByText("Chain verified")).toBeNull();
    expect(arrived()).toBe(25);
    // A broken chain is read no further.
    await tick(3000);
    expect(reads("after=")).toBe(1);
  });

  it("starts no second tail read while one is unanswered, so no record lands twice", async () => {
    let answer!: (fresh: AuditRow[]) => void;
    vi.mocked(listAudit).mockImplementation((q: string) => {
      if (new URLSearchParams(q).get("after") === String(HEAD)) return new Promise((resolve) => { answer = resolve; });
      return Promise.resolve(serve(q));
    });
    mount();
    await loaded(9);
    await tick(3000);
    expect(reads("after=")).toBe(1);
    answer(batch(2));
    await loaded(11);
    await tick(1000);
    expect(reads("after=4219")).toBe(1);
    expect(reads("after=4221")).toBe(1);
    expect(rows().filter((seq) => seq === "4220").length).toBe(1);
  });
});
