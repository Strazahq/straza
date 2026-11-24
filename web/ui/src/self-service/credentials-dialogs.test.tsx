import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type ServerRow, selfServers } from "@/lib/api";
import { CredentialsTab } from "./credentials-tab";
import type { SelfState } from "./context";
import * as W from "./credential-words";

// The dialogs of the Credentials tab against a mocked /v1/self/servers lane
// and a mocked shell: the button a row's dialog opened from is the same
// element while the dialog is open, so the focus goes back to it when the
// dialog closes.

const held = vi.hoisted(() => ({ state: null as unknown as SelfState }));
vi.mock("./context", () => ({ useSelf: () => held.state }));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfServers: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/session", () => ({ refresh: vi.fn() }));

const PAST = new Date(Date.now() - 9 * 86400000).toISOString().slice(0, 10) + "T00:00:00Z";
const tok = (over: Partial<ServerRow>): ServerRow => ({ app: "github", runtime: "remote", kind: "token", agents: "own", reached: true, connected: false, allow_agents: false, ...over });

// ROWS hold one row for each button that opens a dialog.
const ROWS: ServerRow[] = [
  tok({ app: "github" }),
  tok({ app: "jira", connected: true, fingerprint: "1a2b", expires_at: PAST }),
  tok({ app: "gitlab", reached: false, connected: true, fingerprint: "b81e" }),
  { app: "midpoint", runtime: "remote", kind: "oauth", provider: "keycloak", agents: "own", reached: true, connected: true, allow_agents: false },
];

function shell(): SelfState {
  return {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] },
    setCount: vi.fn(),
    setNotice: vi.fn(),
    openSignIn: vi.fn(),
  } as unknown as SelfState;
}

const rowOf = (name: string) => document.querySelector('[data-credential="' + name + '"]') as HTMLElement;

beforeEach(() => {
  vi.clearAllMocks();
  held.state = shell();
  window.history.replaceState(null, "", "/self-service/credentials");
  vi.mocked(selfServers).mockResolvedValue(ROWS);
});

describe("the dialogs of the Credentials tab", () => {
  it.each([
    ["Paste a token", "github", W.PASTE, "dialog"],
    ["Paste a new token", "jira", W.PASTE_NEW, "dialog"],
    ["Remove of a set token", "jira", W.REMOVE, "alertdialog"],
    ["Remove of a server out of reach", "gitlab", W.REMOVE, "alertdialog"],
    ["Disconnect", "midpoint", W.DISCONNECT, "alertdialog"],
  ])("keeps the button of %s in the page while its dialog is open, and gives it the focus back", async (_what, name, label, role) => {
    const user = userEvent.setup();
    render(<TooltipProvider><CredentialsTab /></TooltipProvider>);
    await waitFor(() => expect(rowOf(name)).toBeTruthy());
    const opener = within(rowOf(name)).getByRole("button", { name: W.actionName(label, name) });
    await user.click(opener);
    await screen.findByRole(role);
    expect(opener.isConnected).toBe(true);
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole(role)).toBe(null));
    expect(document.activeElement).toBe(opener);
  });
});
