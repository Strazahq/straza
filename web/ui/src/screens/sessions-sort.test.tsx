import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sessions } from "./sessions";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type SessionRow, listSessions, overview } from "@/lib/api";
import { snapshot } from "@/lib/session";
import { groupLine, hideGroup } from "@/lib/session-words";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();
const base = { client_version: "2.2.0", started_at: ago(2 * 3600 * 1000), last_seen: ago(4 * 60 * 1000), harness: "claude-code/2.2.0", attestation: "managed", wiring_status: "current" };
const session = (n: number, who: string, status: string): SessionRow =>
  ({ ...base, id: "01a1da" + n + "0-0000-7000-8000-000000000000", user_id: "u-" + who, username: who, status });

// all is every session in the server's order by user. Each person has one
// active session and one that is not, so the order by status interleaves
// the people.
const all = [session(1, "amy", "active"), session(2, "amy", "closed"), session(3, "ben", "active"), session(4, "ben", "revoked"), session(5, "cyd", "active"), session(6, "cyd", "closed")];
const BY_STATUS = [all[0], all[2], all[4], all[1], all[5], all[3]].map((r) => r.id);

// The server the mock plays answers each read from its filters and its
// sort, and holds a read while hold is set until go answers it.
let hold = false;
const held: (() => void)[] = [];
const answer = (q: string) => {
  const p = new URLSearchParams(q);
  const status = p.get("status");
  const items = all.filter((r) => !status || r.status === status);
  return { items: p.get("sort") === "status" ? [...items].sort((a, b) => a.status.localeCompare(b.status)) : items, next_cursor: "" };
};
const go = () => act(async () => { hold = false; held.splice(0).forEach((f) => f()); });

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listSessions: vi.fn(), overview: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));

const rows = () => [...document.querySelectorAll("tr[data-session]")].map((e) => e.getAttribute("data-session"));
const groups = () => [...document.querySelectorAll("[data-group]")].map((e) => e.getAttribute("data-group"));
const lastQuery = () => vi.mocked(listSessions).mock.calls[vi.mocked(listSessions).mock.calls.length - 1][0];

// underAll mounts the list and shows every session, still sorted by user.
async function underAll() {
  render(<TooltipProvider><Sessions /></TooltipProvider>);
  await screen.findByRole("button", { name: hideGroup("amy") });
  await userEvent.click(screen.getByRole("button", { name: "All" }));
  await screen.findByText("6 sessions", { selector: "[data-row-count]" });
}

describe("the sessions list across a change of sort", () => {
  beforeEach(() => {
    hold = false;
    held.length = 0;
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(overview).mockReset().mockResolvedValue({ push: { lane_up: true, connected: 3 }, sessions: { total: 6, active: 3 } });
    vi.mocked(listSessions).mockReset().mockImplementation((q: string) => new Promise((resolve) => {
      if (hold) held.push(() => resolve(answer(q))); else resolve(answer(q));
    }));
  });

  it("groups the rows only once the page sorted by user lands, one line per user", async () => {
    await underAll();
    await userEvent.click(screen.getByRole("button", { name: "Sort by status" }));
    await screen.findByRole("button", { name: "Sort by user" });
    expect(rows()).toEqual(BY_STATUS);

    hold = true;
    await userEvent.click(screen.getByRole("button", { name: "Sort by user" }));
    expect(lastQuery()).toContain("sort=user&order=asc");
    expect(groups()).toEqual([]);
    expect(rows()).toEqual(BY_STATUS);

    await go();
    expect(groups()).toEqual(["amy", "ben", "cyd"]);
    expect(rows()).toEqual(all.map((r) => r.id));
    for (const who of ["amy", "ben", "cyd"]) {
      expect((document.querySelector('[data-group="' + who + '"]') as HTMLElement).textContent).toContain(groupLine(2, 1, false));
    }
  });

  it("keeps the groups while the page sorted by status is read, and drops them once it lands", async () => {
    await underAll();
    expect(groups()).toEqual(["amy", "ben", "cyd"]);

    hold = true;
    await userEvent.click(screen.getByRole("button", { name: "Sort by status" }));
    expect(lastQuery()).toContain("sort=status&order=asc");
    expect(groups()).toEqual(["amy", "ben", "cyd"]);
    expect(rows()).toEqual(all.map((r) => r.id));

    await go();
    expect(groups()).toEqual([]);
    expect(rows()).toEqual(BY_STATUS);
  });
});
