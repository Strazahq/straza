import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserSessions } from "./user-sessions";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type SessionRow, listSessions } from "@/lib/api";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { NO_SESSION } from "@/lib/user-words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listSessions: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const ago = new Date(Date.now() - 60000).toISOString();
const session = (id: string, extra: Partial<SessionRow> = {}): SessionRow => ({ id, user_id: "u1", harness: "claude-code/2.2.0", client_version: "0.9.1", attestation: "none", status: "active", started_at: ago, last_seen: ago, ...extra });

function draw(extra: Partial<{ allowed: boolean; active: number }> = {}) {
  const onRevokeAll = vi.fn();
  render(<TooltipProvider><UserSessions id="u1" name="altair" allowed={extra.allowed ?? true} active={extra.active ?? 1} nonce={0} onRevokeAll={onRevokeAll} /></TooltipProvider>);
  return { onRevokeAll };
}

describe("the Sessions tab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(listSessions).mockResolvedValue({ items: [session("01a1da3d-aaaa"), session("9f00bb11-bbbb", { status: "revoked", client_version: undefined, harness: "codex" })], next_cursor: "" });
  });

  it("lists the user's sessions of every status, each row opening its session", async () => {
    draw();
    const row = await screen.findByRole("button", { name: "Open session 01a1da3d" });
    expect(listSessions).toHaveBeenCalledWith("user=u1&limit=50");
    expect(row.textContent).toBe("01a1da3dClaude Code 2.2.01 m agoactive");
    expect(screen.getByRole("button", { name: "Open session 9f00bb11" }).textContent).toBe("9f00bb11Codex1 m agorevoked");
    await userEvent.click(row);
    expect(navigate).toHaveBeenCalledWith("sessions", ["01a1da3d-aaaa"]);
  });

  it("offers Revoke all sessions only while a session is active", async () => {
    const { onRevokeAll } = draw();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke all sessions" }));
    expect(onRevokeAll).toHaveBeenCalled();
  });

  it("offers no Revoke all sessions with none active", async () => {
    draw({ active: 0 });
    await screen.findByRole("button", { name: "Open session 01a1da3d" });
    expect(screen.queryByRole("button", { name: "Revoke all sessions" })).toBeNull();
  });

  it("says the user has no session yet", async () => {
    vi.mocked(listSessions).mockResolvedValue({ items: [], next_cursor: "" });
    draw({ active: 0 });
    expect(await screen.findByText(NO_SESSION)).toBeTruthy();
  });

  it("opens Sessions filtered to the user when more rows exist", async () => {
    vi.mocked(listSessions).mockResolvedValue({ items: [session("01a1da3d-aaaa")], next_cursor: "c1" });
    draw();
    await userEvent.click(await screen.findByRole("button", { name: "All sessions of altair" }));
    const [key, rest, , before] = vi.mocked(navigate).mock.calls.at(-1) as [string, string[], boolean, () => void];
    expect([key, rest]).toEqual(["sessions", []]);
    before();
    expect(take("sessions")).toEqual({ id: "u1", username: "altair" });
  });

  it("stands one line for the tab without the sessions area, and reads nothing", async () => {
    draw({ allowed: false });
    expect((document.querySelector("[data-needs-area]") as HTMLElement).textContent).toBe("The sessions are not shown, because your role does not include Sessions.");
    await waitFor(() => expect(listSessions).not.toHaveBeenCalled());
  });
});
