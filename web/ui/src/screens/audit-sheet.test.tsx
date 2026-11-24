import { createHash } from "node:crypto";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Audit } from "./audit";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AuditRow, listAudit, listUsers, overview } from "@/lib/api";

// The Audit screen's record sheet: where the focus goes when it closes, and
// the words of a sign-in record and of a tool or resource list read, in the
// table and in the sheet.

const SESSION = "0199cf12-4b1e-7a3c-9d21-0b6e4f2a8c10";
const USER_ID = "0198b2c1-77aa-7f00-8e12-3c4d5e6f7a80";

// chain links records from seq 1, each one hashing the record before it,
// the way internal/audit.Link writes them.
function chain(seeds: unknown[]): AuditRow[] {
  let prev = "";
  return seeds.map((s, i) => {
    const ce = JSON.stringify(s);
    const hash = createHash("sha256").update(prev + "\n" + ce).digest("hex");
    const row: AuditRow = { seq: i + 1, ce, hash, prevHash: prev, username: "kim" };
    prev = hash;
    return row;
  });
}

const WINDOW = chain([
  { type: "straza.audit.authn", time: "2026-11-15T08:20:00Z", data: { action: "login", outcome: "success", user: USER_ID } },
  { type: "straza.audit.mcp", time: "2026-11-15T08:21:00Z", data: { session: SESSION, user: USER_ID, event: "tools.list", tool: "", app: "", toolName: "", effect: "allow", ruleId: "", setName: "", reason: "catalog served", count: 4 } },
  { type: "straza.audit.mcp", time: "2026-11-15T08:22:00Z", data: { session: SESSION, user: USER_ID, event: "resources.list", tool: "", app: "dashboards", toolName: "", effect: "allow", ruleId: "", setName: "", reason: "views served", count: 2 } },
]);

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listAudit: vi.fn(),
  listUsers: vi.fn(),
  overview: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const mount = () => render(<TooltipProvider><Audit /></TooltipProvider>);

const TYPE = 3, WHAT = 5;
const cell = (seq: number, index: number) => ((document.querySelector('[data-seq="' + seq + '"]') as HTMLElement).children[index] as HTMLElement).textContent || "";

describe("the record sheet on Audit", () => {
  beforeEach(() => {
    vi.mocked(listAudit).mockImplementation((q: string) => Promise.resolve(new URLSearchParams(q).get("order") === "desc" ? WINDOW.slice().reverse() : []));
    vi.mocked(listUsers).mockResolvedValue({ items: [], next_cursor: "" });
    vi.mocked(overview).mockResolvedValue({ audit: { head_seq: 3 } });
  });

  it("gives the focus back to the row that opened it, after Escape and after Close", async () => {
    mount();
    const row = await screen.findByRole("button", { name: "Open 2" });
    row.focus();
    await userEvent.keyboard("{Enter}");
    await screen.findByRole("dialog");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open 2" })));

    await userEvent.click(screen.getByRole("button", { name: "Open 3" }));
    const sheet = await screen.findByRole("dialog");
    // The foot's Close, not the corner's cross that carries the same name.
    await userEvent.click(within(sheet).getAllByRole("button", { name: "Close" }).find((b) => b.getAttribute("data-slot") === "button") as HTMLElement);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open 3" })));
  });

  it("says sign-in for a sign-in record, in the Type column and the sheet's title", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open 1" }));
    expect(cell(1, TYPE)).toBe("sign-in");
    expect((await screen.findByRole("dialog")).querySelector("h2")?.textContent).toBe("Record 1 sign-in");
  });

  it("reads a record that served a tool or resource list as that read", async () => {
    mount();
    await screen.findByRole("button", { name: "Open 3" });
    expect(cell(2, WHAT)).toBe("tool list read");
    expect(cell(3, WHAT)).toBe("resource list read");
  });
});
