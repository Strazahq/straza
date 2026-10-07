import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Transcripts } from "./transcripts";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type Transcript, captureConfig, listConversations, listUsers, searchTurns, sessionTranscript } from "@/lib/api";

// The browser hash is stubbed, so a test can hunt an exact value under
// jsdom, which has no secure context, and can take the hash away again.
const chain = vi.hoisted(() => ({ secure: true, hex: "9f2c1d" + "0a".repeat(29) }));
vi.mock("@/lib/chain", () => ({
  get canHash() { return chain.secure; },
  sha256Hex: vi.fn(async () => chain.hex),
}));

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listConversations: vi.fn(),
  searchTurns: vi.fn(),
  sessionTranscript: vi.fn(),
  captureConfig: vi.fn(),
  listUsers: vi.fn(),
}));

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const S1 = "0199cf12-4b1e-7a3d-9f21-8c0b1e2d3a44";
const S2 = "0199cd90-52ab-7c04-8e11-2f7a6b5c4d33";
const S3 = "0199c9a1-3e55-7b62-9a08-5d4c3b2a1e90";
const U3 = "0199aa00-1111-2222-3333-444455556666";
const SECRET = "AKIAIOSFODNN7EXAMPLE";
const LONG = "The migration touched every table in the billing schema, so the agent walked them one by one and wrote what it found in the log before it asked for the next step, which is more than a hundred and sixty characters of captured reply.";

const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

// Three captured conversations the way /v1/admin/transcripts answers them,
// newest activity first, the last one with no resolved username.
const conversations = [
  { session_id: S1, user_id: "u1", username: "joe-java-developer-agent", turns: 12, first_at: ago(3600000), last_at: ago(360000), preview: "The build directory is stale, remove it and rebuild." },
  { session_id: S2, user_id: "u2", username: "sam-sre-agent", turns: 4, first_at: ago(9000000), last_at: ago(7200000), preview: "Rolled the deployment back to the previous revision; health is green again." },
  { session_id: S3, user_id: U3, turns: 31, first_at: "2026-09-10T09:00:00Z", last_at: "2026-09-10T10:00:00Z", preview: "Done. The migration is applied and the tests pass." },
];

const hits = [
  { at: "2026-09-12T10:31:12Z", kind: "prompt", mode: "redact", content: "Use this key for the registry: [REDACTED credential]", content_hash: "sha256:bb01", session_id: S1, user_id: "u1", username: "joe-java-developer-agent" },
  { at: "2026-09-11T15:02:00Z", kind: "reply", mode: "redact", content: "I found the value in .env.local and will not echo it.", content_hash: "sha256:bb02", session_id: S2, user_id: "u2", username: "sam-sre-agent", agent_type: "subagent" },
  { at: "2026-09-11T14:00:00Z", kind: "reply", mode: "redact", content: "", body_missing: true, content_hash: "sha256:bb03", session_id: S3, user_id: U3, username: "sam-sre-agent" },
  { at: "2026-09-11T13:00:00Z", kind: "reply", mode: "redact", content: LONG, content_hash: "sha256:bb04", session_id: S2, user_id: "u2", username: "sam-sre-agent" },
];

const transcript: Transcript = {
  session_id: S1,
  username: "joe-java-developer-agent",
  turns: [{ at: "2026-09-12T10:20:04Z", kind: "prompt", mode: "redact", content: "Deploy the billing service to staging.", content_hash: "sha256:aa01" }],
};

const ON = { capture: { policy_sets: 1, mode: "redact", retention_hours: 720, body_store: "inline" } };

const mount = (props: { session?: string } = {}) => render(
  <TooltipProvider>
    <Transcripts {...props} />
  </TooltipProvider>,
);

// rows returns the shown rows in table order, told apart from the other
// buttons by the data-session attribute.
const rows = () => screen.getAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-session")).map((b) => b.getAttribute("data-session"));
const cells = (session: string) => [...(document.querySelector("[data-session='" + session + "']") as HTMLElement).querySelectorAll("td")].map((c) => c.textContent || "");
// cell returns the element one column of a row renders.
const cell = (session: string, column: string) => document.querySelector("[data-session='" + session + "'] [data-column=" + column + "] span") as HTMLElement;
const captureRow = () => document.querySelector("[data-capture-line]") as HTMLElement;
const hint = () => (document.querySelector("[data-hint]") as HTMLElement).textContent;
const count = () => (document.querySelector("[data-row-count]") as HTMLElement).textContent;
const box = () => screen.getByRole("textbox", { name: "Search transcripts" });

// searchAs types the needle and clicks the one primary of the page.
const searchAs = async (needle: string) => {
  await userEvent.type(box(), needle);
  await userEvent.click(screen.getByRole("button", { name: "Search" }));
};

describe("the transcripts screen", () => {
  beforeEach(() => {
    chain.secure = true;
    vi.mocked(listConversations).mockResolvedValue(conversations);
    vi.mocked(searchTurns).mockResolvedValue(hits);
    vi.mocked(sessionTranscript).mockResolvedValue(transcript);
    vi.mocked(captureConfig).mockResolvedValue(ON);
    vi.mocked(listUsers).mockResolvedValue({ items: [{ id: "u1", username: "joe-java-developer-agent", status: "active", origin: "scim", kind: "agent", created_at: "2026-09-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z", effective_roles: [], locks: [], sponsored_count: 0 }], next_cursor: "" });
  });

  it("lists one row per captured session in the server's order, with the turn count and the preview", async () => {
    mount();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Transcripts");
    await screen.findByRole("button", { name: "Open " + S1 });
    expect(listConversations).toHaveBeenCalledWith(200);
    expect(rows()).toEqual([S1, S2, S3]);
    expect(cells(S1)).toEqual(["6 m ago", "joe-java-developer-agent", "12", "The build directory is stale, remove it and rebuild.", "0199cf12-4b1e…"]);
    // A row whose user the server did not resolve reads as the id prefix.
    expect(cells(S3)[1]).toBe("0199aa00-1111…");
    expect(count()).toBe("3 conversations loaded");
    expect(hint()).toBe("One row per recorded session, newest activity first. Click a row for the whole conversation. Recording is turned on by policy, so an empty list is normal.");
    // Only the column the server ordered by is marked as the sort.
    expect([...document.querySelectorAll("th [data-sorted]")].map((h) => h.textContent)).toEqual(["Last activity"]);
  });

  it("sets the latest turn in the text tone on one line that takes the rest of the table, whole on hover", async () => {
    vi.mocked(listConversations).mockResolvedValue([{ ...conversations[0], preview: LONG }, ...conversations.slice(1)]);
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    const turn = cell(S1, "preview");
    expect(turn.textContent).toBe(LONG);
    expect(turn.getAttribute("title")).toBe(LONG);
    for (const cls of ["truncate", "text-foreground"]) expect(turn.className).toContain(cls);
    expect(turn.className).not.toContain("max-w-");
    // The layout is fixed and Latest turn is the one column with no width,
    // so it takes what the others leave and the table never grows past it.
    const table = turn.closest("table") as HTMLElement;
    expect(table.className).toContain("table-fixed");
    expect([...table.querySelectorAll<HTMLElement>("col")].map((c) => c.style.width !== "")).toEqual([true, true, true, false, true]);
  });

  it("keeps the sibling columns inside their widths: a long name is cut and whole on hover, an old stamp wraps", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    expect(cell(S1, "username").className).toContain("truncate");
    expect(cell(S1, "username").getAttribute("title")).toBe("joe-java-developer-agent");
    // A row older than a day reads as the whole stamp.
    expect(cell(S3, "last_at").textContent).toBe("2026-09-10 10:00:00 UTC");
    expect(cell(S3, "last_at").className).toContain("whitespace-normal");
  });

  it("words the capture posture from the config", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    expect(within(captureRow()).getByText("on").getAttribute("data-tone")).toBe("ok");
    expect(captureRow().textContent).toBe("Recording policyon1 live policy records sessions, secrets masked. Recorded messages are deleted after 30 d; message bodies are stored in the database.");
  });

  it("says capture is off when no policy opts a session in", async () => {
    vi.mocked(captureConfig).mockResolvedValue({ capture: { policy_sets: 0 } });
    mount();
    await screen.findByText("off");
    expect(within(captureRow()).getByText("off").getAttribute("data-tone")).toBe("plain");
    expect(captureRow().textContent).toContain("No active policy currently records sessions.");
  });

  it("says the posture is unknown when the config could not be read", async () => {
    vi.mocked(captureConfig).mockRejectedValue(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    mount();
    await screen.findByText("unknown");
    expect(within(captureRow()).getByText("unknown").getAttribute("data-tone")).toBe("unknown");
    expect(captureRow().textContent).toContain("Recording settings are unavailable.");
  });

  it("sends the words and the picked user on a contains search", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await userEvent.type(screen.getByPlaceholderText("User: Type a name"), "joe");
    await userEvent.click(await screen.findByRole("option", { name: /joe-java-developer-agent/ }));
    await searchAs("registry");
    expect(searchTurns).toHaveBeenCalledWith("q=registry&user=joe-java-developer-agent&limit=200");
    expect(hint()).toBe("Contains text: the server scans recorded prompts and replies for the words.");
  });

  it("hashes an exact value in the browser and sends the hash, never the text", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await userEvent.click(screen.getByRole("combobox", { name: "Match" }));
    await userEvent.click(await screen.findByRole("option", { name: "exact value" }));
    await searchAs(SECRET);
    const sent = vi.mocked(searchTurns).mock.calls[0][0];
    expect(sent).toBe("hash=sha256%3A" + chain.hex + "&limit=200");
    expect(sent).not.toContain(SECRET);
    expect(hint()).toBe("Exact value: the text is hashed in this browser and only the hash is sent, so a hunted secret never leaves this machine.");
  });

  it("changes the hint with the match picked", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await searchAs("registry");
    expect(hint()).toContain("Contains text: the server scans recorded prompts and replies for the words.");
    await userEvent.click(screen.getByRole("combobox", { name: "Match" }));
    await userEvent.click(await screen.findByRole("option", { name: "exact value" }));
    expect(hint()).toContain("Exact value: the text is hashed in this browser and only the hash is sent, so a hunted secret never leaves this machine.");
  });

  it("refuses the exact match where the browser cannot hash, and says why", async () => {
    chain.secure = false;
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await userEvent.click(screen.getByRole("combobox", { name: "Match" }));
    const exact = await screen.findByRole("option", { name: "exact value" });
    expect(exact.getAttribute("aria-disabled")).toBe("true");
    expect(exact.getAttribute("title")).toBe("Hashing in the browser needs a secure context (TLS or localhost), so exact value is not available here.");
  });

  it("focuses the box and says what is missing when Search is clicked empty", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(screen.getByText("Type what to search for.")).toBeTruthy();
    expect(document.activeElement).toBe(box());
    expect(searchTurns).not.toHaveBeenCalled();
    expect(rows()).toEqual([S1, S2, S3]);
  });

  it("lists the matching turns with their kind, their content and a lost body, and opens one", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await userEvent.type(box(), "registry{Enter}");
    await screen.findByText(/Use this key for the registry/);
    expect(searchTurns).toHaveBeenCalledWith("q=registry&limit=200");
    expect(count()).toBe("4 turns match.");
    // A hit carries its whole stamp, since hits span sessions and days.
    expect(cells(S1)[0]).toBe("2026-09-12 10:31:12 UTC");
    const kinds = [...document.querySelectorAll("[data-session] [data-tone]")].map((b) => [b.textContent, b.getAttribute("data-tone")]);
    expect(kinds).toEqual([["prompt", "accent"], ["reply · subagent", "ok"], ["reply", "ok"], ["body missing", "danger"], ["reply", "ok"]]);
    expect(document.querySelector("[data-tone=danger]")?.getAttribute("title")).toBe(
      "The body of this turn should be in the body store and was not found. This is an integrity finding; the hash witness on the audit chain still stands.",
    );
    // A long reply is cut to its first 160 characters.
    const long = [...document.querySelectorAll("[data-session] td")].map((c) => c.textContent || "").find((t) => t.startsWith("The migration touched"));
    expect(long).toBe(LONG.slice(0, 160) + "…");
    await userEvent.click(screen.getByRole("button", { name: "Open " + S1 }));
    expect(sessionTranscript).toHaveBeenCalledWith(S1);
    expect((await screen.findByRole("dialog")).getAttribute("data-conversation-sheet")).toBe(S1);
  });

  it("returns to the conversations and clears the box", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await searchAs("registry");
    await screen.findByText(/Use this key for the registry/);
    await userEvent.click(screen.getByRole("button", { name: "Back to conversations" }));
    expect(rows()).toEqual([S1, S2, S3]);
    expect((box() as HTMLInputElement).value).toBe("");
    expect(count()).toBe("3 conversations loaded");
    expect(screen.queryByRole("button", { name: "Back to conversations" })).toBeNull();
  });

  it("opens the conversation of a typed session id", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    await userEvent.type(screen.getByRole("textbox", { name: "Session id" }), S1 + "{Enter}");
    expect(sessionTranscript).toHaveBeenCalledWith(S1);
    expect((await screen.findByRole("dialog")).getAttribute("data-conversation-sheet")).toBe(S1);
  });

  it("opens the conversation a row is a door to", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open " + S2 }));
    expect(sessionTranscript).toHaveBeenCalledWith(S2);
    expect((await screen.findByRole("dialog")).getAttribute("data-conversation-sheet")).toBe(S2);
  });

  it("opens the conversation the address names at once", async () => {
    mount({ session: S3 });
    expect((await screen.findByRole("dialog")).getAttribute("data-conversation-sheet")).toBe(S3);
  });

  it("keeps the rows on screen when a reload cannot reach strazad", async () => {
    mount();
    await screen.findByRole("button", { name: "Open " + S1 });
    vi.mocked(listConversations).mockRejectedValueOnce(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("Transcripts: unreachable, state unknown.");
    expect(block.textContent).toContain("The recorded conversations could not be read because strazad did not answer. Check that it is running, then reload.");
    expect(rows()).toEqual([S1, S2, S3]);
  });

  it("names the first read that failed and offers the reload", async () => {
    vi.mocked(listConversations).mockRejectedValue(Object.assign(new Error("bad gateway"), { status: 502, unreachable: false }));
    mount();
    const block = await screen.findByRole("alert");
    expect(block.textContent).toContain("The recorded conversations could not be read: bad gateway. Reload to try again.");
    expect(within(block).getByRole("button", { name: "Reload now" })).toBeTruthy();
  });
});
