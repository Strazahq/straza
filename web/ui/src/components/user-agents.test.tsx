import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserAgents } from "./user-agents";
import type { UserRow } from "@/lib/api";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { READING_AGENTS, SUBJECT_AGENTS } from "@/lib/user-words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const ago = new Date(Date.now() - 4 * 60000).toISOString();
const agent = (id: string, extra: Partial<UserRow>): UserRow => ({
  id, username: id, status: "active", origin: "local", kind: "nhi", user_type: "agent", created_at: "", updated_at: "", effective_roles: [], locks: [], sponsored_count: 0, ...extra,
});
const altair = agent("altair", { display: "Altair (coding agent)", effective_roles: ["github-readers"], last_seen: ago });
const sam = agent("sam-sre-agent", { effective_roles: ["demo-tools-sandbox", "straza-admin", "x", "y"], status: "disabled" });

describe("the Agents tab", () => {
  it("lists each sponsored agent as a row that opens its page", async () => {
    render(<UserAgents name="alice" agents={{ kind: "ready", data: { items: [altair, sam], next_cursor: "" } }} />);
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Agents alice sponsors");
    const row = screen.getByRole("button", { name: "Open altair" });
    expect(row.textContent).toBe("Altair (coding agent)altairgithub-readers4 m agoactive");
    expect(screen.getByRole("button", { name: "Open sam-sre-agent" }).textContent).toBe("sam-sre-agentstraza-admin+3not yetdisabled");
    await userEvent.click(row);
    expect(navigate).toHaveBeenCalledWith("users", ["altair"]);
    expect(screen.queryByRole("button", { name: "All agents alice sponsors" })).toBeNull();
  });

  it("opens the list filtered by the sponsor when the read was cut at 50", async () => {
    render(<UserAgents name="alice" agents={{ kind: "ready", data: { items: [altair], next_cursor: "c" } }} />);
    await userEvent.click(screen.getByRole("button", { name: "All agents alice sponsors" }));
    const [key, rest, , before] = vi.mocked(navigate).mock.calls.at(-1) as [string, string[], boolean, () => void];
    expect([key, rest]).toEqual(["users", []]);
    before();
    expect(take("users")).toEqual({ sponsor: "alice" });
  });

  it.each<[string, "loading" | "failed", string]>([
    ["while the read is out", "loading", READING_AGENTS],
    ["when the read failed", "failed", SUBJECT_AGENTS + " could not be read."],
  ])("says so %s", (_name, kind, want) => {
    render(<UserAgents name="alice" agents={kind === "loading" ? { kind } : { kind, message: "x" }} />);
    expect(screen.getByRole("status").textContent).toContain(want);
  });
});
