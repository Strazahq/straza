import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, selfServers } from "@/lib/api";
import { CredentialsTab } from "./credentials-tab";
import type { SelfState } from "./context";
import * as W from "./credential-words";

// A failed read of the Credentials tab against a mocked /v1/self/servers
// lane and a mocked shell: a whole sentence of the server is shown as it
// is, and any other message inside the tab's own sentence.

const held = vi.hoisted(() => ({ state: null as unknown as SelfState }));
vi.mock("./context", () => ({ useSelf: () => held.state }));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  selfServers: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/session", () => ({ refresh: vi.fn() }));

// SENTENCE stands for any whole sentence of the server.
const SENTENCE = "Straza could not read the waiting requests, so it cannot list them. Try again, and tell your administrator if it keeps failing.";

beforeEach(() => {
  vi.clearAllMocks();
  held.state = {
    session: { kind: "signed-in", user: "alice", grants: "", servers: 0 },
    self: { username: "alice", user_kind: "human", enroll_channels: ["browser"], sponsored: [] },
    setCount: vi.fn(),
    setNotice: vi.fn(),
    openSignIn: vi.fn(),
  } as unknown as SelfState;
  window.history.replaceState(null, "", "/self-service/credentials");
});

describe("a failed read of the Credentials tab", () => {
  it.each([
    ["a whole sentence of the server as it is", SENTENCE, SENTENCE],
    ["a message that does not open with a capital letter inside the tab's sentence", "the store is unavailable.", W.readProblem("the store is unavailable")],
    ["a message that does not end in a full stop inside the tab's sentence", "Service Unavailable", W.readProblem("Service Unavailable")],
  ])("shows %s", async (_what, message, sentence) => {
    vi.mocked(selfServers).mockRejectedValue(new ApiError(message, 500));
    render(<TooltipProvider><CredentialsTab /></TooltipProvider>);
    expect((await screen.findByRole("alert")).textContent).toBe(sentence);
  });
});
