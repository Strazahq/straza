import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sessions } from "./sessions";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type OverviewAnswer, type SessionRow, type UserRow, listSessions, listUsers, overview, revokeSession, revokeSessions } from "@/lib/api";
import { notify } from "@/lib/notify";
import { ATT_LEGEND, ATT_TITLE, OWN_SESSION_WARNING, WIRING_TITLE, revokeBody, hideGroup, showGroup } from "@/lib/session-words";
import { snapshot } from "@/lib/session";

// The stamps are relative to now so relTimeText reads the same words on
// every run.
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

const OWN = "0199cf12-4b1e-7a3c-9d21-0b6e4f2a8c10";
const SECOND = "0199cf12-9a2d-7b44-8e10-4c7d1f905b22";
const THIRD = "0199cf13-0011-7c55-9f21-5d8e2a016c33";
const FOURTH = "0199cf13-77aa-7d66-a032-6e9f3b127d44";

const base = { harness: "claude-code/2.1.0", client_version: "v1.0.0-1121", started_at: ago(2 * 3600 * 1000), last_seen: ago(4 * 60 * 1000) };

const joeOwn: SessionRow = { ...base, id: OWN, user_id: "u-joe", username: "joe-java-developer-agent", attestation: "managed", status: "active", wiring_status: "current", wiring_hash: "sha256:9c1e04b7aa1132f6" };
const joeSecond: SessionRow = { ...base, id: SECOND, user_id: "u-joe", username: "joe-java-developer-agent", attestation: "none", status: "active" };
const carol: SessionRow = { ...base, id: THIRD, user_id: "u-carol", username: "carol", attestation: "advisory", status: "active", wiring_status: "mismatch", wiring_hash: "sha256:11aa22bb33cc44dd" };
const dead: SessionRow = { ...base, id: FOURTH, user_id: "u-dave", username: undefined, attestation: "managed", status: "revoked", wiring_status: "unmeasured" };

const all = [joeOwn, joeSecond, carol, dead];
const page = (items: SessionRow[], next = "") => ({ items, next_cursor: next });

// answer is the server the mock plays: the filters and the sort decide the
// page, the way the real list does.
const answer = (q: string) => {
  if (q.indexOf("status=revoked") >= 0) return page([dead]);
  if (q.indexOf("user=u-carol") >= 0) return page([carol]);
  if (q.indexOf("sort=status") >= 0) return page([carol, dead]);
  return page(all);
};

const carolUser: UserRow = {
  id: "u-carol", username: "carol", status: "active", origin: "scim", kind: "user",
  created_at: base.started_at, updated_at: base.started_at, effective_roles: [], locks: [], sponsored_count: 0,
};

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listSessions: vi.fn(),
  listUsers: vi.fn(),
  overview: vi.fn(),
  revokeSession: vi.fn(),
  revokeSessions: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = () => render(<TooltipProvider><Sessions /></TooltipProvider>);

// ready waits for the grouped list and opens every folded group, since the
// list folds each person when more than one is shown.
const ready = async () => {
  await screen.findAllByRole("button", { name: /^(Show|Hide) the sessions of / });
  for (const b of screen.queryAllByRole("button", { name: /^Show the sessions of / })) await userEvent.click(b);
};

// rows returns the session ids in table order.
const rows = () => screen.getAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-session")).map((b) => b.getAttribute("data-session"));
// rowIds reads the same rows while a modal is open, where the list sits
// behind aria-hidden and the role query cannot see it.
const rowIds = () => [...document.querySelectorAll("[data-session]")].map((e) => e.getAttribute("data-session"));
const row = (id: string) => document.querySelector('[data-session="' + id + '"]') as HTMLElement;
// group is the header row of one person's sessions, and cellsOf reads it as
// the column each cell sits under and the words in it.
const group = (name: string) => document.querySelector('[data-group="' + name + '"]') as HTMLElement;
const cellsOf = (tr: HTMLElement) => [...tr.children].map((td) => [td.getAttribute("data-column"), td.textContent]);
// headline is the count above the filter row, or null when it is not shown.
const headline = () => { const e = document.querySelector("[data-active-count]"); return e ? e.textContent : null; };
const lastQuery = () => vi.mocked(listSessions).mock.calls[vi.mocked(listSessions).mock.calls.length - 1][0];
const failure = (message: string, status: number, unreachable = false) => new ApiError(message, status, unreachable);

describe("the sessions screen", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "admin", roles: [], grants: "full", expiresIn: 300, sessionID: OWN });
    vi.mocked(listSessions).mockReset().mockImplementation((q: string) => Promise.resolve(answer(q)));
    vi.mocked(listUsers).mockReset().mockResolvedValue({ items: [carolUser], next_cursor: "" });
    vi.mocked(overview).mockReset().mockResolvedValue({ push: { lane_up: true, connected: 3 }, sessions: { total: 9, active: 4 } });
    vi.mocked(revokeSession).mockReset().mockResolvedValue({ status: "revoked" });
    vi.mocked(revokeSessions).mockReset().mockResolvedValue({ revoked: 2 });
    vi.mocked(notify.ok).mockReset();
  });

  it("opens only the requested group on the first toggle", async () => {
    mount();
    const target = await screen.findByRole("button", { name: showGroup("joe-java-developer-agent") });
    const closedBefore = screen.getAllByRole("button", { name: /^Show the sessions of / }).map((b) => b.getAttribute("aria-label"));
    await userEvent.click(target);
    expect(screen.getByRole("button", { name: hideGroup("joe-java-developer-agent") }).getAttribute("aria-expanded")).toBe("true");
    expect(screen.getAllByRole("button", { name: /^Show the sessions of / }).map((b) => b.getAttribute("aria-label"))).toEqual(closedBefore.filter((label) => label !== showGroup("joe-java-developer-agent")));
  });

  it("leads the table with the user and lays each folded group under the same headers", async () => {
    mount();
    await screen.findByRole("button", { name: showGroup("carol") });
    expect([...document.querySelectorAll("thead th")].map((th) => th.textContent)).toEqual(["", "User", "Sessions", "Harness", "Attestation", "Wiring", "Status", "Last seen"]);
    expect(cellsOf(group("carol")).map(([id]) => id)).toEqual(["select", "user", "session", "harness", "attestation", "wiring", "status", "last_seen"]);
    // The group's badges keep the sentences the same badges carry on a row.
    expect(within(group("joe-java-developer-agent")).getByText("none").getAttribute("title")).toBe(ATT_TITLE.none);
    expect(within(group("carol")).getByText("mismatch").getAttribute("title")).toBe(WIRING_TITLE.mismatch);
  });

  it.each([
    ["joe-java-developer-agent", { session: "2 sessions", attestation: "none", wiring: "current", status: "2 active" }],
    ["carol", { session: "1 session", attestation: "advisory", wiring: "mismatch", status: "1 active" }],
    ["u-dave", { session: "1 session", attestation: "managed", wiring: "unmeasured", status: "none active" }],
  ])("answers every column for the sessions of %s together", async (name, cells) => {
    mount();
    await screen.findByRole("button", { name: showGroup(name) });
    expect(Object.fromEntries(cellsOf(group(name)))).toEqual({ select: "", user: name, harness: "claude-code/2.1.0", last_seen: "4 m ago", ...cells });
  });

  it("keeps a group's cells under their headers when a column is hidden", async () => {
    mount();
    await screen.findByRole("button", { name: showGroup("carol") });
    await userEvent.click(screen.getByRole("button", { name: /Columns/ }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Harness" }));
    expect([...document.querySelectorAll("thead th")].map((th) => th.textContent)).toEqual(["", "User", "Sessions", "Attestation", "Wiring", "Status", "Last seen"]);
    expect(cellsOf(group("carol"))).toEqual([["select", ""], ["user", "carol"], ["session", "1 session"], ["attestation", "advisory"], ["wiring", "mismatch"], ["status", "1 active"], ["last_seen", "4 m ago"]]);
  });

  it("opens and folds a group from its name as well as from its chevron", async () => {
    mount();
    const door = await screen.findByRole("button", { name: showGroup("carol") });
    expect(group("carol").contains(door)).toBe(true);
    await userEvent.click(within(group("carol")).getByText("carol"));
    expect(screen.getByRole("button", { name: hideGroup("carol") }).getAttribute("aria-expanded")).toBe("true");
    expect(rows()).toEqual([THIRD]);
    await userEvent.click(within(group("carol")).getByText("carol"));
    expect(screen.getByRole("button", { name: showGroup("carol") }).getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryAllByRole("button", { name: /^Open / })).toEqual([]);
  });

  it("reads the first page newest first and sorts on the server", async () => {
    mount();
    await screen.findByRole("button", { name: showGroup("joe-java-developer-agent") });
    // Three people are listed, so every group opens folded with its count.
    expect(screen.queryAllByRole("button", { name: /^Open / })).toEqual([]);
    expect(screen.getByText("2 sessions")).toBeTruthy();
    await ready();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Sessions");
    expect(rows()).toEqual([OWN, SECOND, THIRD, FOURTH]);
    expect(lastQuery()).toContain("status=active");
    expect(lastQuery()).toContain("sort=user&order=asc");
    expect(lastQuery()).toContain("limit=100");
    expect(screen.getByText(ATT_LEGEND)).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Sort by user" }));
    expect(lastQuery()).toContain("sort=user&order=desc");
  });

  it("narrows the list by status and by user, on the server", async () => {
    mount();
    await ready();

    await userEvent.click(screen.getByRole("button", { name: "revoked" }));
    expect(lastQuery()).toContain("status=revoked");
    expect(screen.getByRole("button", { name: "revoked" }).getAttribute("aria-pressed")).toBe("true");
    await screen.findByText("1 session loaded");
    expect(rows()).toEqual([FOURTH]);

    await userEvent.click(screen.getByRole("button", { name: "all" }));
    expect(lastQuery()).not.toContain("status=");
    await ready();

    // cmdk labels its own input, so the picker is found by its role and
    // pinned by the label it carries.
    const picker = screen.getByRole("combobox");
    expect(picker.getAttribute("aria-label")).toBe("User");
    await userEvent.type(picker, "car");
    await userEvent.click(await screen.findByRole("option", { name: /carol/ }));
    await screen.findByText("1 session loaded");
    expect(lastQuery()).toContain("user=u-carol");
    expect(rows()).toEqual([THIRD]);
  });

  it("marks the session this console holds", async () => {
    mount();
    await ready();
    expect(within(row(OWN)).getByRole("img", { name: "this console" })).toBeTruthy();
    expect(within(row(SECOND)).queryByText("this console")).toBeNull();
  });

  it("badges attestation and wiring with their sentences and edges a session that measured nothing", async () => {
    mount();
    await ready();

    expect(within(row(THIRD)).getByText("advisory").getAttribute("title")).toBe(ATT_TITLE.advisory);
    expect(within(row(THIRD)).getByText("mismatch").getAttribute("title")).toBe(WIRING_TITLE.mismatch + " 11aa22bb33cc…");
    expect(within(row(OWN)).getByText("current").getAttribute("title")).toBe(WIRING_TITLE.current + " 9c1e04b7aa11…");

    // The row reads "none" twice: the attestation badge with its sentence,
    // then the wiring cell, which is a word and not a badge.
    expect(within(row(SECOND)).getAllByText("none").map((e) => e.getAttribute("title"))).toEqual([ATT_TITLE.none, null]);
    expect(row(SECOND).className).toContain("shadow-[inset_3px_0_0_var(--danger)]");
    expect(row(OWN).className).not.toContain("shadow-[inset_3px_0_0_var(--danger)]");
  });

  it("opens a bulk bar for the ticked rows, ticks no dead row, and drops the set when those rows leave the list", async () => {
    mount();
    await ready();
    expect(screen.queryByRole("checkbox", { name: "Select session 0199cf13-77aa…" })).toBeNull();

    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-4b1e…" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-9a2d…" }));
    const bar = document.querySelector("[data-bulk-bar]") as HTMLElement;
    expect(bar.textContent).toContain("2 selected of 4 shown, all joe-java-developer-agent");
    expect(within(bar).getByRole("button", { name: "Revoke 2 sessions" })).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Status" })).toBeNull();

    await userEvent.click(within(bar).getByRole("button", { name: "Clear" }));
    expect(document.querySelector("[data-bulk-bar]")).toBeNull();
    expect(screen.getByRole("group", { name: "Status" })).toBeTruthy();

    // The bulk bar takes the filter row's place, so the sort headers are the
    // filter still reachable; either way the set drops with the rows it was
    // made on.
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-4b1e…" }));
    expect(document.querySelector("[data-bulk-bar]")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Sort by status" }));
    await screen.findByText("2 sessions loaded");
    expect(document.querySelector("[data-bulk-bar]")).toBeNull();
  });

  it("names the count in the confirm, warns about this console's own session, and posts one revoke", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-4b1e…" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-9a2d…" }));
    await userEvent.click(within(document.querySelector("[data-bulk-bar]") as HTMLElement).getByRole("button", { name: "Revoke 2 sessions" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Revoke 2 sessions?");
    expect(dialog.textContent).toContain(revokeBody(2, "joe-java-developer-agent"));
    expect(dialog.textContent).toContain(OWN_SESSION_WARNING);

    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke 2 sessions" }));
    expect(revokeSessions).toHaveBeenCalledTimes(1);
    expect(revokeSessions).toHaveBeenCalledWith([OWN, SECOND]);
    expect(revokeSession).not.toHaveBeenCalled();
    expect(notify.ok).toHaveBeenCalledWith("2 sessions revoked.");
    expect(document.querySelector("[data-bulk-bar]")).toBeNull();
  });

  it("revokes one session from its sheet", async () => {
    mount();
    await ready();
    await userEvent.click(await screen.findByRole("button", { name: "Open " + THIRD }));
    const sheet = await screen.findByRole("dialog");
    await userEvent.click(within(sheet).getByRole("button", { name: "Revoke session" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Revoke session?");
    expect(dialog.textContent).toContain(revokeBody(1, "carol"));
    expect(dialog.textContent).not.toContain(OWN_SESSION_WARNING);

    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke session" }));
    expect(revokeSession).toHaveBeenCalledWith(THIRD);
    expect(revokeSessions).not.toHaveBeenCalled();
    expect(notify.ok).toHaveBeenCalledWith("1 session revoked.");
  });

  it("keeps the dialog and the rows when the server refuses the revoke", async () => {
    vi.mocked(revokeSessions).mockRejectedValue(failure("session 0199cf12-9a2d is already revoked", 409));
    mount();
    await ready();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-4b1e…" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 0199cf12-9a2d…" }));
    await userEvent.click(within(document.querySelector("[data-bulk-bar]") as HTMLElement).getByRole("button", { name: "Revoke 2 sessions" }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke 2 sessions" }));

    const refusal = await within(dialog).findByRole("alert");
    expect(refusal.textContent).toContain("Revoke 2 sessions refused.");
    expect(refusal.textContent).toContain("The server refused it: session 0199cf12-9a2d is already revoked. Fix what it names, then try again.");
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(rowIds()).toEqual([OWN, SECOND, THIRD, FOURTH]);
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("appends the next page and keeps the rows when a reload cannot reach strazad", async () => {
    vi.mocked(listSessions).mockResolvedValueOnce(page([joeOwn, joeSecond], "c1")).mockResolvedValueOnce(page([carol, dead]));
    mount();
    await ready();
    expect(screen.getByText("2 sessions loaded, more on the server")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByRole("button", { name: showGroup("carol") });
    await ready();
    expect(rows()).toEqual([OWN, SECOND, THIRD, FOURTH]);
    expect(lastQuery()).toContain("cursor=c1");

    vi.mocked(listSessions).mockRejectedValueOnce(failure("unreachable", 0, true));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("Sessions: unreachable, state unknown.");
    expect(block.textContent).toContain("The session list could not be read because strazad did not answer. Check that it is running, then reload.");
    expect(rows()).toEqual([OWN, SECOND, THIRD, FOURTH]);
  });

  it("heads the list with the server's count of active sessions and says nothing when the overview cannot be read", async () => {
    mount();
    const line = await screen.findByText(/^Live updates:/);
    expect(line.textContent).toBe("Live updates: 3 connected to this server instance. Other clients check for changes every 30 seconds.");
    // The count is the server's, not the four rows this page holds.
    expect(headline()).toBe("4 active sessions");

    vi.mocked(overview).mockRejectedValue(failure("overview failed", 500));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    await ready();
    expect(document.querySelector("[data-push-line]")).toBeNull();
    expect(headline()).toBeNull();
  });

  const HEADS: [string, OverviewAnswer, string | null, string | null][] = [
    ["one session with its client connected", { push: { lane_up: true, connected: 1 }, sessions: { total: 3, active: 1 } }, "1 active session", "Live updates: 1 connected to this server instance."],
    ["no active session", { push: { lane_up: true, connected: 0 }, sessions: { total: 3, active: 0 } }, "0 active sessions", "Live updates: 0 connected to this server instance."],
    ["the push lane down", { push: { lane_up: false }, sessions: { total: 9, active: 7 } }, "7 active sessions", "Live updates are unavailable on this server instance. Clients check for changes every 30 seconds."],
    ["an answer without the push block", { sessions: { total: 9, active: 7 } }, "7 active sessions", null],
    ["an answer without the session counts", { push: { lane_up: true, connected: 2 } }, null, "Live updates: 2 connected to this server instance."],
  ];
  it.each(HEADS)("words the headline and the push line for %s", async (_case, answer, count, line) => {
    vi.mocked(overview).mockResolvedValue(answer);
    mount();
    await ready();
    expect(headline()).toBe(count);
    const push = document.querySelector("[data-push-line]");
    expect(push ? push.textContent : null).toBe(line);
  });

  it("renders a username that looks like markup as text", async () => {
    vi.mocked(listSessions).mockResolvedValue(page([{ ...joeOwn, username: "<img src=x onerror=alert(1)>" }]));
    mount();
    await screen.findByRole("button", { name: "Open " + OWN });
    // The name shows on the group header and on the row, both as text.
    expect(screen.getAllByText("<img src=x onerror=alert(1)>").length).toBe(2);
    expect(document.querySelector("img")).toBeNull();
  });
});
