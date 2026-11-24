import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionPage } from "./session-page";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type AuditRow, type SessionRow, getSession, listApprovals, listApps, listAudit, listUsers, revokeSession, sessionTranscript } from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { adminAreas, snapshot } from "@/lib/session";
import { NO_SUCH_SESSION, OWN_SESSION_WARNING, READING_SESSION, revokePageBody } from "@/lib/session-words";

const ID = "01a1da3d-934c-7be8-bd1a-92e51c045044";
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

const altair: SessionRow = {
  id: ID, user_id: "u-altair", username: "altair", harness: "claude-code/2.2.0", client_version: "2.2.0", attestation: "managed", status: "active",
  started_at: ago(5 * 60000 + 5000), last_seen: ago(60000 + 5000), wiring_status: "current", wiring_hash: "sha256:236f8d3dd754", user_kind: "agent", sponsor: "alice", waiting: 1,
};

const rec = (seq: number, type: string, s: number, data: Record<string, unknown>): AuditRow =>
  ({ seq, ce: JSON.stringify({ type, time: ago(600000 - s * 1000), data: { session: ID, user: "u-altair", ...data } }), username: "altair" });

// The session's records: two allowed calls, a call a policy rule denied, a
// call no access row admits, and a call that waits for alice.
const RECORDS: AuditRow[] = [
  rec(5, "straza.audit.mcp", 50, { tool: "mcp.call", app: "elevenlabs", toolName: "search_voices", effect: "allow", ruleId: "elevenlabs-audio-allow", setName: "audio-gen-access" }),
  rec(4, "straza.audit.approval", 40, { phase: "request", approvalId: "apr-1", rule: "push-needs-yes", set: "agent-guardrails", lane: "hook", state: "pending", summary: "shell.exec: git push origin main" }),
  rec(3, "straza.audit.mcp", 30, { tool: "mcp.call", app: "", toolName: "github__delete_branch", effect: "deny", reason: 'unknown tool "github__delete_branch": no access row for this session\'s roles admits it' }),
  rec(2, "straza.audit.tool", 20, { tool: "shell.exec", command: "rm -rf build/", effect: "deny", ruleId: "deny-rm-rf", setName: "agent-guardrails", reason: "Recursive force delete is refused." }),
  rec(1, "straza.audit.mcp", 10, { tool: "mcp.call", app: "github", toolName: "list_issues", effect: "allow", role: "github-readers" }),
];

const PENDING = { id: "apr-1", state: "pending", expiresAt: new Date(Date.now() + 100000).toISOString(), approverUsers: ["alice"], approverRoles: [], mode: "approve", user: "u-altair", username: "altair" };

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  getSession: vi.fn(),
  listAudit: vi.fn(),
  listApprovals: vi.fn(),
  listApps: vi.fn(),
  listUsers: vi.fn(),
  sessionTranscript: vi.fn(),
  revokeSession: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn(), adminAreas: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = () => render(<TooltipProvider><main><SessionPage id={ID} /></main></TooltipProvider>);
const heading = () => screen.getByRole("heading", { level: 1 });
const tiles = () => [...document.querySelectorAll("[data-glance]")].map((b) => b.getAttribute("data-glance") + ": " + b.textContent);
const activity = () => [...document.querySelectorAll("[data-activity]")].map((li) => Number(li.getAttribute("data-activity")));
const filterButton = (name: RegExp) => within(screen.getByRole("group", { name: "Which calls to show" })).getByRole("button", { name });
const failure = (message: string, status: number) => new ApiError(message, status, false);

describe("the page of one session", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "carol", roles: [], grants: "full", expiresIn: 300, sessionID: "7c1f02aa-0000-7000-8000-000000000000" });
    vi.mocked(adminAreas).mockReturnValue(null);
    vi.mocked(getSession).mockReset().mockResolvedValue(altair);
    vi.mocked(listAudit).mockReset().mockResolvedValue(RECORDS);
    vi.mocked(listApprovals).mockReset().mockResolvedValue({ approvals: [PENDING] } as never);
    vi.mocked(listApps).mockReset().mockResolvedValue([{ name: "github" }] as never);
    vi.mocked(listUsers).mockReset().mockResolvedValue({ items: [{ id: "u-alice", username: "alice" }, { id: "u-alicex", username: "alicex" }], next_cursor: "" } as never);
    vi.mocked(sessionTranscript).mockReset().mockResolvedValue({ session_id: ID, turns: [{ mode: "verbatim" }, { mode: "verbatim" }, { mode: "verbatim" }] } as never);
    vi.mocked(revokeSession).mockReset().mockResolvedValue({ status: "revoked" });
    vi.mocked(notify.ok).mockReset();
    vi.mocked(navigate).mockReset();
  });

  it("says it reads the session under the Sessions head", async () => {
    vi.mocked(getSession).mockReturnValue(new Promise(() => {}));
    mount();
    expect(screen.getByRole("status").textContent).toBe(READING_SESSION);
    expect(heading().textContent).toBe("Sessions");
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(crumbs).getByRole("link", { name: "Sessions" })).toBeTruthy();
    expect(crumbs.textContent).toContain("01a1da3d");
  });

  it("says no session has the id and offers the way back, without an error box", async () => {
    vi.mocked(getSession).mockRejectedValue(failure("no session with that id", 404));
    mount();
    expect((await screen.findByText(NO_SUCH_SESSION)).hasAttribute("data-no-such-session")).toBe(true);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(document.querySelector("[data-fetch-error]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Back to Sessions" }));
    expect(navigate).toHaveBeenCalledWith("sessions");
  });

  it("says any other failed read and reads again from Reload now", async () => {
    vi.mocked(getSession).mockRejectedValueOnce(failure("database unavailable", 503));
    mount();
    const block = await screen.findByText("This session could not be read:").then((e) => e.closest("[data-fetch-error]") as HTMLElement);
    expect(block.textContent).toContain("This session could not be read: database unavailable. Reload to try again.");
    await userEvent.click(screen.getByRole("button", { name: "Reload now" }));
    expect((await screen.findByRole("heading", { level: 1, name: "altair on Claude Code 2.2.0" }))).toBeTruthy();
    expect(getSession).toHaveBeenCalledTimes(2);
  });

  it("heads an agent's session with its sponsor as a link once the users read found it", async () => {
    mount();
    expect((await screen.findByRole("heading", { level: 1, name: /Claude Code/ })).textContent).toBe("altair on Claude Code 2.2.0");
    expect(screen.getByRole("navigation", { name: "Breadcrumb" }).textContent).toBe("Straza/Sessions/01a1da3d");
    expect(document.querySelector("[data-status-word]")?.textContent).toBe("active");
    expect(document.querySelector("[data-session-times]")?.textContent).toBe("Started 5 minutes ago. Last seen 1 minute ago.");
    const line = document.querySelector("[data-session-user]") as HTMLElement;
    expect(line.textContent).toBe("altairAI agent, sponsored byalice");
    expect(within(line).getByRole("link", { name: "altair" }).getAttribute("data-user-link")).toBe("u-altair");
    expect(line.querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("agent");
    const sponsor = await within(line).findByRole("link", { name: "alice" });
    expect(sponsor.getAttribute("data-sponsor-link")).toBe("u-alice");
    expect((line.querySelector("[data-sponsor]") as HTMLElement).querySelector("[data-kind-icon]")?.getAttribute("data-kind-icon")).toBe("person");
    expect(vi.mocked(listUsers).mock.calls[0][0]).toBe("q=alice&limit=20");
    await userEvent.click(within(line).getByRole("link", { name: "altair" }));
    expect(navigate).toHaveBeenCalledWith("users", ["u-altair"]);
  });

  it("names the sponsor as plain text when the users read does not find it", async () => {
    vi.mocked(listUsers).mockResolvedValue({ items: [{ id: "u-alicex", username: "alicex" }], next_cursor: "" } as never);
    mount();
    await screen.findByRole("heading", { level: 1, name: "altair on Claude Code 2.2.0" });
    await waitFor(() => expect(listUsers).toHaveBeenCalled());
    const sponsor = document.querySelector("[data-sponsor]") as HTMLElement;
    expect(sponsor.textContent).toBe("alice");
    expect(within(sponsor).queryByRole("link")).toBeNull();
  });

  it("names the sponsor as plain text and reads no user without the identity area", async () => {
    vi.mocked(adminAreas).mockReturnValue({ sessions: true, audit: true, approvals: true, transcripts: true });
    mount();
    await screen.findByRole("heading", { level: 1, name: "altair on Claude Code 2.2.0" });
    expect(within(document.querySelector("[data-sponsor]") as HTMLElement).queryByRole("link")).toBeNull();
    expect(listUsers).not.toHaveBeenCalled();
  });

  it("heads a person's session with no sponsor, and marks the console's own session", async () => {
    vi.mocked(getSession).mockResolvedValue({ ...altair, username: "carol", user_id: "u-carol", user_kind: "person", sponsor: undefined, harness: "console/2.2.0" });
    vi.mocked(snapshot).mockReturnValue({ user: "carol", roles: [], grants: "full", expiresIn: 300, sessionID: ID });
    mount();
    expect((await screen.findByRole("heading", { level: 1, name: / on / })).textContent).toBe("carol on Console 2.2.0");
    expect(document.querySelector("[data-session-user]")?.textContent).toBe("carolPerson");
    expect(document.querySelector("[data-sponsor]")).toBeNull();
    const chip = document.querySelector("[data-this-console]") as HTMLElement;
    expect(chip.textContent).toBe("this console");
    expect(chip.getAttribute("data-tone")).toBe("plain");
    expect(listUsers).not.toHaveBeenCalled();
  });

  it("names a session whose user has no row by the user id in the title alone", async () => {
    vi.mocked(getSession).mockResolvedValue({ ...altair, username: undefined, user_kind: undefined, sponsor: undefined, user_id: "0199aa00-1111-7000-8000-000000000004" });
    mount();
    expect((await screen.findByRole("heading", { level: 1, name: / on / })).textContent).toBe("0199aa00 on Claude Code 2.2.0");
    expect(document.querySelector("[data-session-user]")).toBeNull();
  });

  it("asks before it revokes, revokes, says so and reads the page again", async () => {
    mount();
    const button = await screen.findByRole("button", { name: "Revoke session" });
    expect(button.getAttribute("data-variant")).toBe("outline");
    await userEvent.click(button);
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Revoke this session?");
    expect(dialog.textContent).toContain(revokePageBody("altair"));
    expect(dialog.textContent).not.toContain(OWN_SESSION_WARNING);
    expect(within(dialog).queryByRole("textbox")).toBeNull();
    vi.mocked(getSession).mockResolvedValue({ ...altair, status: "revoked" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke session" }));
    expect(revokeSession).toHaveBeenCalledWith(ID);
    expect(notify.ok).toHaveBeenCalledWith("Session revoked.");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    await waitFor(() => expect(getSession).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Revoke session" })).toBeNull());
    expect(document.querySelector("[data-status-word]")?.textContent).toBe("revoked");
    expect(listAudit).toHaveBeenCalledTimes(2);
  });

  it("warns in the confirm that revoking the console's own session signs the reader out", async () => {
    vi.mocked(snapshot).mockReturnValue({ user: "carol", roles: [], grants: "full", expiresIn: 300, sessionID: ID });
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke session" }));
    expect((await screen.findByRole("alertdialog")).textContent).toContain(OWN_SESSION_WARNING);
  });

  it("keeps a refusal inside the confirm and gives the focus back to the button when it closes", async () => {
    vi.mocked(revokeSession).mockRejectedValue(failure("session is already revoked", 409));
    mount();
    const button = await screen.findByRole("button", { name: "Revoke session" });
    await userEvent.click(button);
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke session" }));
    const refusal = await within(dialog).findByRole("alert");
    expect(refusal.textContent).toBe("Revoke session refused: session is already revoked. Fix what it names, then try again.");
    expect(notify.ok).not.toHaveBeenCalled();
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Revoke session" })));
  });

  it("draws no Revoke session for a session that is no longer active", async () => {
    vi.mocked(getSession).mockResolvedValue({ ...altair, status: "closed" });
    mount();
    await screen.findByRole("heading", { level: 1, name: "altair on Claude Code 2.2.0" });
    expect(screen.queryByRole("button", { name: "Revoke session" })).toBeNull();
  });

  it("draws the glance tiles from the activity, the session and the transcript, each a door", async () => {
    mount();
    await waitFor(() => expect(tiles()).toEqual(["allowed: 2 calls allowed", "waiting: 1 waiting for approval", "denied: 2 denied", "turns: 3 turns recorded"]));
    expect(activity()).toEqual([5, 4, 3, 2, 1]);
    await userEvent.click(screen.getByRole("button", { name: "2 denied" }));
    expect(filterButton(/^Denied/).getAttribute("aria-pressed")).toBe("true");
    expect(activity()).toEqual([3, 2]);
    await userEvent.click(screen.getByRole("button", { name: "1 waiting for approval" }));
    expect(filterButton(/^Waiting/).getAttribute("aria-pressed")).toBe("true");
    expect(activity()).toEqual([4]);
    await userEvent.click(screen.getByRole("button", { name: "2 calls allowed" }));
    expect(filterButton(/^All/).getAttribute("aria-pressed")).toBe("true");
    expect(activity()).toEqual([5, 4, 3, 2, 1]);
    await userEvent.click(screen.getByRole("button", { name: "3 turns recorded" }));
    expect(navigate).toHaveBeenCalledWith("transcripts", [ID]);
  });

  it("counts once each call the hook handed to the gateway", async () => {
    const handOver = (seq: number, s: number) => rec(seq, "straza.audit.tool", s, { tool: "mcp.call", app: "objects-demo", toolName: "repo_read", effect: "allow", ruleId: "",
      reason: "Straza: decision deferred to the gateway PEP, which evaluates current policy server-side on this call (single gate)" });
    const gateway = (seq: number, s: number) => rec(seq, "straza.audit.mcp", s, { tool: "mcp.call", app: "objects-demo", toolName: "repo_read", effect: "allow", role: "objects-demo-tools" });
    vi.mocked(listAudit).mockResolvedValue([gateway(9, 70), handOver(8, 70), gateway(7, 60), handOver(6, 60), ...RECORDS]);
    mount();
    await waitFor(() => expect(tiles()).toEqual(["allowed: 4 calls allowed", "waiting: 1 waiting for approval", "denied: 2 denied", "turns: 3 turns recorded"]));
    expect(activity()).toEqual([9, 7, 5, 4, 3, 2, 1]);
    expect(filterButton(/^All/).textContent).toBe("All 7");
  });

  it("leaves out the allowed and denied tiles while the activity is cut at its limit, and a tile at zero", async () => {
    // A full read of turns and one call: the read is cut, and the page draws
    // one row.
    const many = [
      ...Array.from({ length: 999 }, (_, i) => rec(i + 2, "straza.audit.prompt", i % 500, { mode: "verbatim", contentHash: "sha256:aa" })),
      rec(1, "straza.audit.mcp", 1, { tool: "mcp.call", app: "github", toolName: "list_issues", effect: "allow", role: "github-readers" }),
    ];
    vi.mocked(listAudit).mockResolvedValue(many);
    vi.mocked(getSession).mockResolvedValue({ ...altair, waiting: 0 });
    vi.mocked(sessionTranscript).mockResolvedValue({ session_id: ID, turns: [] } as never);
    mount();
    await screen.findByText("Open all records in Audit");
    expect(tiles()).toEqual([]);
    expect(document.querySelector("[data-glance-row]")).toBeNull();
  });

  it("reads the whole chain for the session once, again on Reload, newest first at most 1000", async () => {
    mount();
    await waitFor(() => expect(activity().length).toBe(5));
    expect(listAudit).toHaveBeenCalledTimes(1);
    const q = new URLSearchParams(vi.mocked(listAudit).mock.calls[0][0]);
    expect(q.get("q")).toBe('"session":"' + ID + '"');
    expect(q.get("limit")).toBe("1000");
    expect(q.get("order")).toBe("desc");
    await userEvent.click(screen.getByRole("button", { name: "Reload the activity" }));
    await waitFor(() => expect(listAudit).toHaveBeenCalledTimes(2));
    expect(getSession).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["audit", "The activity is not shown, because your role does not include Audit."],
    ["transcripts", "The recording is not shown, because your role does not include Transcripts."],
  ])("says in one line that the role does not include %s", async (area, line) => {
    const all: Record<string, true> = { sessions: true, audit: true, approvals: true, transcripts: true, identity: true };
    delete all[area];
    vi.mocked(adminAreas).mockReturnValue(all);
    mount();
    expect((await screen.findByText(line)).hasAttribute("data-needs-area")).toBe(true);
    expect(area === "audit" ? listAudit : sessionTranscript).not.toHaveBeenCalled();
    if (area === "audit") expect(screen.queryByRole("button", { name: "Reload the activity" })).toBeNull();
  });

  it("draws the Health and the Recording cards beside the activity", async () => {
    mount();
    expect(await screen.findByText("Client verified")).toBeTruthy();
    expect(screen.getByText("Hook configuration current")).toBeTruthy();
    expect(await screen.findByText("Recorded, word for word")).toBeTruthy();
    expect(screen.getByText("3 turns so far.")).toBeTruthy();
  });
});

describe("the session page's reads", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.mocked(snapshot).mockReturnValue({ user: "carol", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    vi.mocked(adminAreas).mockReturnValue(null);
    vi.mocked(getSession).mockReset().mockResolvedValue(altair);
    vi.mocked(listAudit).mockReset().mockResolvedValue(RECORDS);
    vi.mocked(listApprovals).mockReset().mockResolvedValue({ approvals: [] } as never);
    vi.mocked(listApps).mockReset().mockResolvedValue([] as never);
    vi.mocked(listUsers).mockReset().mockResolvedValue({ items: [], next_cursor: "" } as never);
    vi.mocked(sessionTranscript).mockReset().mockResolvedValue({ session_id: ID, turns: [] } as never);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("never reads the activity on a timer", async () => {
    mount();
    await screen.findByRole("heading", { level: 1, name: "altair on Claude Code 2.2.0" });
    await waitFor(() => expect(listAudit).toHaveBeenCalledTimes(1));
    await act(async () => { await vi.advanceTimersByTimeAsync(120000); });
    expect(listAudit).toHaveBeenCalledTimes(1);
    expect(getSession).toHaveBeenCalledTimes(1);
  });
});
