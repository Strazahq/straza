import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import userEvent from "@testing-library/user-event";
import { HourglassIcon, ShieldAlertIcon, ShieldCheckIcon } from "lucide-react";
import { Sessions } from "./sessions";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type OverviewAnswer, type SessionRow, listSessions, overview, revokeSession, revokeSessions } from "@/lib/api";
import { put } from "@/lib/handoff";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { ADMIN_HEALTH_TITLE, ATT_TITLE, OWN_SESSION_WARNING, WIRING_TITLE, hideGroup, revokeBody, showGroup } from "@/lib/session-words";
import { snapshot } from "@/lib/session";

// The stamps are relative to now so relTimeText reads the same words on
// every run.
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

// Each id has its own first eight characters, the part the list shows.
const OWN = "7c1f02aa-5e10-7a3c-9d21-0b6e4f2a8c10";
const CAROL = "01a1da57-9a2d-7b44-8e10-4c7d1f905b22";
const CAROL_OLD = "01a1d9f0-1c2d-7b44-8e10-4c7d1f905b23";
const JOE_ONE = "01a1da3d-934c-7be8-bd1a-92e51c045044";
const JOE_TWO = "01a1da41-77aa-7d66-a032-6e9f3b127d44";
const DEAD = "01a1d8c2-0011-7c55-9f21-5d8e2a016c33";
const DAVE_ID = "0199aa00-1111-7000-8000-000000000004";
const JOE = "joe-java-developer-agent";

const base = { client_version: "2.2.0", started_at: ago(2 * 3600 * 1000), last_seen: ago(4 * 60 * 1000) };

// The console's own session runs no agent, so nothing is measured.
const own: SessionRow = { ...base, id: OWN, user_id: "u-alice", username: "alice", harness: "console/2.2.0", attestation: "none", status: "active" };
const carol: SessionRow = { ...base, id: CAROL, user_id: "u-carol", username: "carol", harness: "codex/0.9.1", attestation: "advisory", status: "active", wiring_status: "current", wiring_hash: "sha256:11aa22bb33cc44dd" };
const carolOld: SessionRow = { ...base, id: CAROL_OLD, user_id: "u-carol", username: "carol", harness: "codex/0.9.0", attestation: "managed", status: "closed", wiring_status: "current" };
const joeOne: SessionRow = { ...base, id: JOE_ONE, user_id: "u-joe", username: JOE, harness: "claude-code/2.1.0", attestation: "managed", status: "active", wiring_status: "current", wiring_hash: "sha256:9c1e04b7aa1132f6" };
const joeTwo: SessionRow = { ...base, id: JOE_TWO, user_id: "u-joe", username: JOE, harness: "some-kit/1.0", attestation: "none", status: "active" };
const dead: SessionRow = { ...base, id: DEAD, user_id: DAVE_ID, username: undefined, harness: "claude-code/2.0.9", attestation: "managed", status: "revoked", wiring_status: "unmeasured" };

// all is every session in the server's order by user, so one person's
// sessions run together.
const all = [own, carol, carolOld, joeOne, joeTwo, dead];
const page = (items: SessionRow[], next = "") => ({ items, next_cursor: next });

// answer is the server the mock plays: the filters and the sort decide the
// page, the way the real list does.
const answer = (q: string) => {
  const p = new URLSearchParams(q);
  let items = all;
  const status = p.get("status");
  if (status) items = items.filter((r) => r.status === status);
  const user = p.get("user");
  if (user) items = items.filter((r) => r.user_id === user);
  const search = p.get("q");
  if (search) items = items.filter((r) => (r.username || "").includes(search) || r.id.startsWith(search));
  if (p.get("sort") === "status") items = [...items].sort((a, b) => a.status.localeCompare(b.status));
  return page(items);
};

// many is n active sessions of three people, in the order by user.
const many = (n: number) => Array.from({ length: n }, (_, i): SessionRow => {
  const who = ["amy", "ben", "cyd"][Math.floor((i * 3) / n)];
  return { ...base, id: String(i).padStart(8, "0") + "-0000-7000-8000-000000000000", user_id: "u-" + who, username: who, harness: "claude-code/2.2.0", attestation: "managed", status: "active", wiring_status: "current" };
});

// The server's answer with the kind and the sponsor of each user and the
// waiting requests of each session.
const told: SessionRow[] = [
  { ...own, user_kind: "person" },
  { ...carol, user_kind: "person", waiting: 2 },
  { ...joeOne, user_kind: "agent", sponsor: "carol", waiting: 1 },
  { ...joeTwo, user_kind: "agent", sponsor: "carol" },
];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listSessions: vi.fn(),
  overview: vi.fn(),
  revokeSession: vi.fn(),
  revokeSessions: vi.fn(),
}));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = () => render(<TooltipProvider><Sessions /></TooltipProvider>);

// ready waits for the first page; a short list opens every group.
const ready = () => screen.findByRole("button", { name: "Open " + OWN });

// rows returns the session ids in table order.
const rows = () => screen.queryAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-session")).map((b) => b.getAttribute("data-session"));
// rowIds reads the same rows while a modal is open, where the list sits
// behind aria-hidden and the role query cannot see it.
const rowIds = () => [...document.querySelectorAll("[data-session]")].map((e) => e.getAttribute("data-session"));
const row = (id: string) => document.querySelector('[data-session="' + id + '"]') as HTMLElement;
const cell = (id: string, column: string) => row(id).querySelector('[data-column="' + column + '"]') as HTMLElement;
// group is the header row of one person's sessions.
const group = (key: string) => document.querySelector('[data-group="' + key + '"]') as HTMLElement;
const groupDoors = () => screen.queryAllByRole("button", { name: /^(Show|Hide) the sessions of / });
const heads = () => [...document.querySelectorAll("thead th")].map((th) => th.textContent);
// headline is the count above the filter row, or null when it is not shown.
const headline = () => { const e = document.querySelector("[data-active-count]"); return e ? e.textContent : null; };
const live = () => document.querySelector("[data-live]") as HTMLElement | null;
const lastQuery = () => vi.mocked(listSessions).mock.calls[vi.mocked(listSessions).mock.calls.length - 1][0];
const failure = (message: string, status: number, unreachable = false) => new ApiError(message, status, unreachable);
// counted waits for the count beside the filters, which a group line can
// repeat.
const counted = (words: string) => screen.findByText(words, { selector: "[data-row-count]" });
const bar = () => document.querySelector("[data-bulk-bar]") as HTMLElement | null;
// glyph is the inner markup of a lucide icon, to tell one icon from another.
const glyph = (icon: ReactElement) => { const box = document.createElement("div"); box.innerHTML = renderToStaticMarkup(icon); return (box.querySelector("svg") as SVGElement).innerHTML; };

describe("the sessions screen", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: OWN });
    vi.mocked(listSessions).mockReset().mockImplementation((q: string) => Promise.resolve(answer(q)));
    vi.mocked(overview).mockReset().mockResolvedValue({ push: { lane_up: true, connected: 4 }, sessions: { total: 9, active: 4 } });
    vi.mocked(revokeSession).mockReset().mockResolvedValue({ status: "revoked" });
    vi.mocked(revokeSessions).mockReset().mockResolvedValue({ revoked: 2 });
    vi.mocked(notify.ok).mockReset();
    vi.mocked(navigate).mockReset();
  });

  it("reads the first page of active sessions by user, groups it without a user column, and sorts on the server", async () => {
    mount();
    await ready();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Sessions");
    expect(lastQuery()).toContain("status=active");
    expect(lastQuery()).toContain("sort=user&order=asc");
    expect(lastQuery()).toContain("limit=100");
    expect(rows()).toEqual([OWN, CAROL, JOE_ONE, JOE_TWO]);
    expect(heads()).toEqual(["", "Session", "Harness", "Where", "Waiting", "Health", "Last seen", "Status"]);
    expect(document.querySelector("[data-row-count]")?.textContent).toBe("4 sessions");

    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    expect(lastQuery()).toContain("sort=last_seen&order=asc");
    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    expect(lastQuery()).toContain("sort=last_seen&order=desc");
  });

  it("draws the list flat with the user column first under another sort, and groups it again under the sort by user", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "All" }));
    await counted("6 sessions");
    await userEvent.click(screen.getByRole("button", { name: "Sort by status" }));
    expect(lastQuery()).toContain("sort=status&order=asc");
    await screen.findByRole("button", { name: "Sort by user" });
    expect(heads()).toEqual(["", "User", "Session", "Harness", "Where", "Waiting", "Health", "Last seen", "Status"]);
    expect(document.querySelector("[data-group]")).toBeNull();
    expect(groupDoors()).toEqual([]);
    expect([...row(CAROL).children].map((td) => td.getAttribute("data-column"))).toEqual(["select", "user", "session", "harness", "where", "waiting", "health", "last_seen", "status"]);
    expect(cell(CAROL, "user").textContent).toBe("carol");
    // A user with no sign-in name reads as the first eight characters of
    // its id.
    expect(cell(DEAD, "user").textContent).toBe("0199aa00");
    expect(document.querySelector("[data-groups-foot]")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Sort by user" }));
    expect(lastQuery()).toContain("sort=user&order=asc");
    await screen.findByRole("button", { name: hideGroup("carol") });
    expect(heads()).toEqual(["", "Session", "Harness", "Where", "Waiting", "Health", "Last seen", "Status"]);
    expect(screen.queryByRole("button", { name: "Sort by user" })).toBeNull();
  });

  it("labels the four status options with capitals and sends the wire words", async () => {
    mount();
    await ready();
    const filter = screen.getByRole("group", { name: "Status" });
    expect(within(filter).getAllByRole("button").map((b) => b.textContent)).toEqual(["Active", "Revoked", "Closed", "All"]);
    expect(within(filter).getByRole("button", { name: "Active" }).getAttribute("aria-pressed")).toBe("true");

    await userEvent.click(within(filter).getByRole("button", { name: "Revoked" }));
    expect(lastQuery()).toContain("status=revoked");
    expect(within(filter).getByRole("button", { name: "Revoked" }).getAttribute("aria-pressed")).toBe("true");
    await counted("1 session");
    expect(rows()).toEqual([DEAD]);

    await userEvent.click(within(filter).getByRole("button", { name: "Closed" }));
    expect(lastQuery()).toContain("status=closed");
    await screen.findByRole("button", { name: "Open " + CAROL_OLD });

    await userEvent.click(within(filter).getByRole("button", { name: "All" }));
    expect(lastQuery()).not.toContain("status=");
    await counted("6 sessions");
  });

  it("searches the sessions on the server once the typing settles", async () => {
    mount();
    await ready();
    const search = screen.getByRole("textbox", { name: "Search sessions" });
    expect(search.getAttribute("placeholder")).toBe("Search a user or a session id");
    expect(screen.queryByRole("combobox")).toBeNull();
    await userEvent.type(search, "car");
    await counted("1 session");
    expect(lastQuery()).toContain("q=car");
    expect(lastQuery()).toContain("status=active");
    expect(rows()).toEqual([CAROL]);

    await userEvent.clear(search);
    await counted("4 sessions");
    expect(lastQuery()).not.toContain("q=");
    await userEvent.type(search, "01a1da3d");
    await waitFor(() => expect(lastQuery()).toContain("q=01a1da3d"));
    await counted("1 session");
    expect(rows()).toEqual([JOE_ONE]);
  });

  it("narrows the list to the user the link All sessions of hands over, under All, as a chip that clears", async () => {
    put("sessions", { id: "u-carol", username: "carol" });
    mount();
    await screen.findByRole("button", { name: "Open " + CAROL });
    expect(vi.mocked(listSessions).mock.calls.map((c) => c[0])).toEqual(["user=u-carol&sort=user&order=asc&limit=100"]);
    expect(rows()).toEqual([CAROL, CAROL_OLD]);
    const chip = document.querySelector("[data-user-chip]") as HTMLElement;
    expect(chip.getAttribute("data-user-chip")).toBe("u-carol");
    expect(chip.textContent).toBe("Sessions of carol");

    await userEvent.click(within(chip).getByRole("button", { name: "Clear the user filter" }));
    await counted("6 sessions");
    expect(lastQuery()).not.toContain("user=");
    expect(document.querySelector("[data-user-chip]")).toBeNull();
    // The chip left with its button, so the search box holds the focus.
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Search sessions" }));
  });

  it("opens a session's page from its row", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "Open " + CAROL }));
    expect(navigate).toHaveBeenCalledWith("sessions", [CAROL]);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("leads a group row with the user's kind tile and names the sponsor", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(told));
    mount();
    await ready();
    expect(group(JOE).textContent).toBe(JOE + "sponsored by carol · 2 active sessions");
    expect(group("carol").textContent).toBe("carol1 active session");
    const tile = (key: string) => within(group(key)).getByRole("button").querySelector("[data-kind-icon]");
    expect(tile(JOE)?.getAttribute("data-kind-icon")).toBe("agent");
    expect(tile("carol")?.getAttribute("data-kind-icon")).toBe("person");
    // The default rows send no kind, so their group rows draw no tile.
    vi.mocked(listSessions).mockImplementation((q: string) => Promise.resolve(answer(q)));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    await waitFor(() => expect(group("carol").querySelector("[data-kind-icon]")).toBeNull());
  });

  it("counts the people and the agents with an active session beside the headline", async () => {
    vi.mocked(listSessions).mockImplementation((q: string) => Promise.resolve(page(told.filter((r) => !new URLSearchParams(q).get("q") || r.username === "carol"))));
    mount();
    await ready();
    const pills = () => [...document.querySelectorAll("[data-kind-pill]")].map((p) => p.getAttribute("data-kind-pill") + ": " + p.textContent);
    expect(pills()).toEqual(["person: 2 people", "agent: 1 agent"]);
    expect(headline()).toBe("4 active sessions");

    await userEvent.type(screen.getByRole("textbox", { name: "Search sessions" }), "carol");
    await waitFor(() => expect(lastQuery()).toContain("q=carol"));
    await waitFor(() => expect(pills()).toEqual([]));
  });

  it("hides the kind pills under another status filter", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(told));
    mount();
    await counted("4 sessions");
    expect(document.querySelectorAll("[data-kind-pill]").length).toBe(2);
    await userEvent.click(screen.getByRole("button", { name: "Revoked" }));
    await waitFor(() => expect(lastQuery()).toContain("status=revoked"));
    expect(document.querySelector("[data-kind-pills]")).toBeNull();
  });

  it("hides the kind pills while a further page is on the server", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(told, "c1"));
    mount();
    await counted("4 sessions loaded, more on Straza");
    expect(headline()).toBe("4 active sessions");
    expect(document.querySelector("[data-kind-pills]")).toBeNull();
  });

  it("keeps Active for a user given any other way, and hides the kind pills while the list is narrowed to one user", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(told));
    render(<TooltipProvider><Sessions user={{ id: "u-carol", username: "carol" }} /></TooltipProvider>);
    await ready();
    expect(lastQuery()).toContain("status=active&user=u-carol");
    expect(document.querySelector("[data-kind-pills]")).toBeNull();
  });

  it("shows a session's waiting requests in the Waiting column", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(told));
    mount();
    await ready();
    const chip = within(cell(CAROL, "waiting")).getByRole("img", { name: "2 waiting" });
    expect(chip.textContent).toBe("2");
    expect((chip.querySelector("[data-tone]") as HTMLElement).getAttribute("data-tone")).toBe("warn");
    expect((chip.querySelector("[data-tone]") as HTMLElement).getAttribute("title")).toBe("2 waiting");
    expect(chip.querySelector("svg")?.innerHTML).toBe(glyph(<HourglassIcon />));
    expect(within(cell(JOE_ONE, "waiting")).getByRole("img").getAttribute("aria-label")).toBe("1 waiting");
    expect(cell(JOE_TWO, "waiting").textContent).toBe("");
    expect(cell(OWN, "waiting").textContent).toBe("");
  });

  it("shows a session as its first eight characters and marks the console's own row with a plain chip", async () => {
    mount();
    await ready();
    expect(cell(CAROL, "session").textContent).toBe("01a1da57");
    expect(within(cell(CAROL, "session")).getByText("01a1da57").getAttribute("title")).toBe(CAROL);
    const chip = within(cell(OWN, "session")).getByText("this console");
    expect(chip.getAttribute("data-tone")).toBe("plain");
    expect(cell(OWN, "session").textContent).toBe("7c1f02aathis console");
    expect(document.querySelectorAll("[data-this-console]").length).toBe(1);
  });

  it.each([
    ["a named harness", JOE_ONE, "Claude Code 2.1.0"],
    ["an unnamed harness", JOE_TWO, "some-kit 1.0"],
    ["the console", OWN, "Console 2.2.0"],
  ])("words %s in the Harness cell, whole on hover", async (_case, id, words) => {
    mount();
    await ready();
    const span = cell(id, "harness").firstElementChild as HTMLElement;
    expect(span.textContent).toBe(words);
    expect(span.getAttribute("title")).toBe(words);
    expect(span.className).toContain("truncate");
  });

  it.each([
    ["ok", JOE_ONE, <ShieldCheckIcon />, "text-ok", "Verified", ATT_TITLE.managed + " " + WIRING_TITLE.current],
    ["warn", CAROL, <ShieldAlertIcon />, "text-warn", "Client not verified", ATT_TITLE.advisory + " " + WIRING_TITLE.current],
    ["danger", JOE_TWO, <ShieldAlertIcon />, "text-danger", "Client unmeasured or failed", ATT_TITLE.none],
    ["plain", OWN, null, "", "Not measured", ADMIN_HEALTH_TITLE],
  ])("draws the %s Health cell with its icon, word and title", async (level, id, icon, tone, word, title) => {
    mount();
    await ready();
    const health = cell(id, "health").querySelector("[data-health]") as HTMLElement;
    expect(health.getAttribute("data-health")).toBe(level);
    expect(health.textContent).toBe(word);
    expect(health.getAttribute("title")).toBe(title);
    const svg = health.querySelector("svg");
    if (icon === null) {
      expect(svg).toBeNull();
      expect(health.className).toContain("text-muted-foreground");
    } else {
      expect((svg as SVGElement).innerHTML).toBe(glyph(icon));
      expect(svg?.getAttribute("class")).toContain(tone);
      expect(svg?.getAttribute("aria-hidden")).toBe("true");
    }
  });

  it("keeps the raw columns in the Columns menu under their new names, hidden at first", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: /Columns/ }));
    const items = screen.getAllByRole("menuitemcheckbox");
    expect(items.map((i) => i.textContent)).toEqual(["Harness", "Where", "Waiting", "Health", "Last seen", "Status", "Client build", "Attestation", "Hook configuration", "Started"]);
    expect(items.map((i) => i.getAttribute("aria-checked"))).toEqual(["true", "true", "true", "true", "true", "true", "false", "false", "false", "false"]);
    expect(screen.queryByText(/Wiring/)).toBeNull();

    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Attestation" }));
    await userEvent.click(screen.getByRole("button", { name: /Columns/ }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Hook configuration" }));
    await userEvent.click(screen.getByRole("button", { name: /Columns/ }));
    await userEvent.click(screen.getByRole("menuitemcheckbox", { name: "Client build" }));
    expect(heads()).toEqual(["", "Session", "Harness", "Where", "Waiting", "Health", "Last seen", "Status", "Client build", "Attestation", "Hook configuration"]);
    expect(within(row(CAROL)).getByText("advisory").getAttribute("title")).toBe(ATT_TITLE.advisory);
    expect(within(row(CAROL)).getByText("current").getAttribute("title")).toBe(WIRING_TITLE.current + " 11aa22bb33cc…");
    expect(cell(OWN, "wiring").textContent).toBe("none");
    expect(cell(CAROL, "client").textContent).toBe("2.2.0");
    expect(screen.queryByText(/Wiring/)).toBeNull();
  });

  it.each([
    ["Active", [["alice", "1 active session"], ["carol", "1 active session"], [JOE, "2 active sessions"]]],
    ["Revoked", [[DAVE_ID, "1 session"]]],
    ["Closed", [["carol", "1 session"]]],
    ["All", [["alice", "1 session, 1 active"], ["carol", "2 sessions, 1 active"], [JOE, "2 sessions, 2 active"], [DAVE_ID, "1 session"]]],
  ])("words each group row under the %s filter on one line across the table", async (filter, groups) => {
    mount();
    await ready();
    if (filter !== "Active") {
      await userEvent.click(screen.getByRole("button", { name: filter }));
      await counted(groups.length === 1 ? "1 session" : "6 sessions");
    }
    const drawn = [...document.querySelectorAll("[data-group]")].map((tr) => tr.getAttribute("data-group"));
    expect(drawn).toEqual(groups.map(([key]) => key));
    for (const [key, words] of groups) {
      const tr = group(key);
      expect(tr.children.length).toBe(1);
      // A user with no sign-in name is named by the first eight characters
      // of its id.
      const name = key === DAVE_ID ? "0199aa00" : key;
      expect(tr.textContent).toBe(name + words);
      expect(within(tr).getByRole("button", { name: hideGroup(name) })).toBeTruthy();
    }
  });

  it("opens every group while the list holds 30 sessions", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(many(30)));
    mount();
    await counted("30 sessions");
    expect(groupDoors().map((b) => b.getAttribute("aria-expanded"))).toEqual(["true", "true", "true"]);
    expect(rows().length).toBe(30);
  });

  it("folds every group at 31 sessions", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(many(31)));
    mount();
    await counted("31 sessions");
    expect(groupDoors().map((b) => b.getAttribute("aria-label"))).toEqual([showGroup("amy"), showGroup("ben"), showGroup("cyd")]);
    expect(rows()).toEqual([]);
  });

  it("folds every group while a further page is on the server", async () => {
    vi.mocked(listSessions).mockResolvedValue(page(many(30), "c1"));
    mount();
    await counted("30 sessions loaded, more on Straza");
    expect(groupDoors().map((b) => b.getAttribute("aria-expanded"))).toEqual(["false", "false", "false"]);
    expect(rows()).toEqual([]);
  });

  it("folds only the group asked, from its name as well as from its chevron", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: hideGroup("carol") }));
    expect(screen.getByRole("button", { name: showGroup("carol") }).getAttribute("aria-expanded")).toBe("false");
    expect(groupDoors().map((b) => b.getAttribute("aria-label"))).toEqual([hideGroup("alice"), showGroup("carol"), hideGroup(JOE)]);
    expect(rows()).toEqual([OWN, JOE_ONE, JOE_TWO]);

    await userEvent.click(within(group("carol")).getByText("carol"));
    expect(screen.getByRole("button", { name: hideGroup("carol") }).getAttribute("aria-expanded")).toBe("true");
    expect(rows()).toEqual([OWN, CAROL, JOE_ONE, JOE_TWO]);
  });

  it("draws no line under the grouped table or the flat one, and no attestation legend", async () => {
    mount();
    await ready();
    expect(document.querySelector("[data-groups-foot]")).toBeNull();
    expect(document.querySelector("[data-att-legend]")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    await screen.findByRole("button", { name: "Sort by user" });
    expect(document.querySelector("[data-groups-foot]")).toBeNull();
  });

  // widthsOf reads each shown column's fixed width, header by header, and
  // the floor of the table.
  const widthsOf = () => {
    const names = heads();
    const cols = [...document.querySelectorAll("colgroup col")].map((c) => (c as HTMLElement).style.width);
    return { cols: Object.fromEntries(names.map((n, i) => [n || "select", cols[i]])), floor: (document.querySelector("table") as HTMLElement).style.minWidth };
  };

  it("fits the grouped list's default columns in a table of 1010 px, and gives the flat list the User column's room more", async () => {
    mount();
    await ready();
    expect(widthsOf()).toEqual({
      cols: { select: "36px", Session: "200px", Harness: "", Where: "166px", Waiting: "68px", Health: "190px", "Last seen": "104px", Status: "106px" },
      floor: "1010px",
    });

    await userEvent.click(screen.getByRole("button", { name: "Sort by status" }));
    await screen.findByRole("button", { name: "Sort by user" });
    expect(widthsOf()).toEqual({
      cols: { select: "36px", User: "140px", Session: "200px", Harness: "", Where: "166px", Waiting: "68px", Health: "190px", "Last seen": "104px", Status: "106px" },
      floor: "1150px",
    });
  });

  it("keeps the other widths for the columns shown from the Columns menu", async () => {
    mount();
    await ready();
    for (const name of ["Client build", "Attestation", "Hook configuration", "Started"]) {
      await userEvent.click(screen.getByRole("button", { name: /Columns/ }));
      await userEvent.click(screen.getByRole("menuitemcheckbox", { name }));
    }
    const { cols, floor } = widthsOf();
    expect([cols["Client build"], cols.Attestation, cols["Hook configuration"], cols.Started]).toEqual(["112px", "116px", "140px", "208px"]);
    expect(floor).toBe(1010 + 112 + 116 + 140 + 208 + "px");
  });

  it("shows where each session runs in the Where column, in each of its forms", async () => {
    vi.mocked(listSessions).mockResolvedValue(page([
      own,
      { ...carol, host: "dev-laptop", folder: "~/projects/game-audio" },
      { ...joeOne, host: "build-07" },
      { ...joeTwo, folder: "~/x" },
      { ...dead, status: "active" },
    ]));
    mount();
    await ready();
    const where = (id: string) => cell(id, "where");
    expect(where(CAROL).textContent).toBe("dev-laptop~/projects/game-audio");
    expect(where(CAROL).querySelector("[data-where-host]")?.getAttribute("title")).toBe("dev-laptop");
    expect(where(CAROL).querySelector("[data-where-folder]")?.getAttribute("title")).toBe("~/projects/game-audio");
    expect(where(JOE_ONE).textContent).toBe("build-07");
    expect(where(JOE_ONE).querySelector("[data-where-folder]")).toBeNull();
    expect(where(JOE_TWO).textContent).toBe("~/x");
    expect(where(JOE_TWO).querySelector("[data-where-host]")).toBeNull();
    expect(where(DEAD).textContent).toBe("");
    // The console's own session runs in the browser it is read in.
    expect(where(OWN).textContent).toBe("your browser");
    expect((where(OWN).querySelector("[data-where-browser]") as HTMLElement).className).toContain("text-muted-foreground");
  });

  it("edges a session that measured nothing in red", async () => {
    mount();
    await ready();
    expect(row(JOE_TWO).className).toContain("shadow-[inset_3px_0_0_var(--danger)]");
    expect(row(JOE_ONE).className).not.toContain("shadow-[inset_3px_0_0_var(--danger)]");
  });

  it("ticks only another active session, and drops the set when its rows fold away", async () => {
    mount();
    await ready();
    expect(screen.queryByRole("checkbox", { name: "Select session 7c1f02aa" })).toBeNull();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da57" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da3d" }));
    expect(bar()?.textContent).toContain("2 of 4 selected");
    expect(bar()?.textContent).not.toContain(", all ");
    expect(screen.queryByRole("group", { name: "Status" })).toBeNull();

    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: "Clear" }));
    expect(bar()).toBeNull();
    expect(screen.getByRole("group", { name: "Status" })).toBeTruthy();

    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da3d" }));
    expect(bar()).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: hideGroup(JOE) }));
    expect(bar()).toBeNull();
  });

  it("ticks no session that is no longer active", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("button", { name: "All" }));
    await counted("6 sessions");
    expect(screen.queryByRole("checkbox", { name: "Select session 01a1d8c2" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "Select session 01a1d9f0" })).toBeNull();
    expect(screen.getByRole("checkbox", { name: "Select session 01a1da41" })).toBeTruthy();
  });

  it("names the count and the person in the confirm and posts one revoke", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da3d" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da41" }));
    expect(bar()?.textContent).toContain("2 of 4 selected · " + JOE);
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: "Revoke 2 sessions" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Revoke 2 sessions?");
    expect(dialog.textContent).toContain(revokeBody(2, JOE));
    expect(dialog.textContent).not.toContain(OWN_SESSION_WARNING);

    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke 2 sessions" }));
    expect(revokeSessions).toHaveBeenCalledTimes(1);
    expect(revokeSessions).toHaveBeenCalledWith([JOE_ONE, JOE_TWO]);
    expect(revokeSession).not.toHaveBeenCalled();
    expect(notify.ok).toHaveBeenCalledWith("2 sessions revoked.");
    expect(bar()).toBeNull();
  });

  it("revokes one ticked session through the bulk bar", async () => {
    mount();
    await ready();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da57" }));
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: "Revoke session" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Revoke session?");
    expect(dialog.textContent).toContain(revokeBody(1, "carol"));
    expect(dialog.textContent).not.toContain(OWN_SESSION_WARNING);

    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke session" }));
    expect(revokeSession).toHaveBeenCalledWith(CAROL);
    expect(revokeSessions).not.toHaveBeenCalled();
    expect(notify.ok).toHaveBeenCalledWith("1 session revoked.");
  });

  it("keeps the dialog and the rows when the server refuses the revoke", async () => {
    vi.mocked(revokeSessions).mockRejectedValue(failure("session 01a1da3d is already revoked", 409));
    mount();
    await ready();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da3d" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select session 01a1da41" }));
    await userEvent.click(within(bar() as HTMLElement).getByRole("button", { name: "Revoke 2 sessions" }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Revoke 2 sessions" }));

    const refusal = await within(dialog).findByRole("alert");
    expect(refusal.textContent).toBe("Revoke 2 sessions refused: session 01a1da3d is already revoked. Fix what it names, then try again.");
    expect(screen.getByRole("alertdialog")).toBeTruthy();
    expect(rowIds()).toEqual([OWN, CAROL, JOE_ONE, JOE_TWO]);
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("offers a reload when the first read fails", async () => {
    vi.mocked(listSessions).mockRejectedValueOnce(failure("boom", 500));
    mount();
    const alert = await screen.findByRole("alert");
    await userEvent.click(within(alert).getByRole("button", { name: "Reload now" }));
    await ready();
    expect(rows()).toEqual([OWN, CAROL, JOE_ONE, JOE_TWO]);
  });

  it("appends the next page and keeps the rows when a reload cannot reach strazad", async () => {
    vi.mocked(listSessions).mockResolvedValueOnce(page([own, carol], "c1")).mockResolvedValueOnce(page([joeOne, joeTwo]));
    mount();
    await counted("2 sessions loaded, more on Straza");
    // A further page folds every group.
    expect(rows()).toEqual([]);

    await userEvent.click(screen.getByRole("button", { name: "Load more" }));
    await counted("4 sessions");
    expect(lastQuery()).toContain("cursor=c1");
    // The whole list is loaded and short now, so the groups open.
    expect(rows()).toEqual([OWN, CAROL, JOE_ONE, JOE_TWO]);

    vi.mocked(listSessions).mockRejectedValueOnce(failure("unreachable", 0, true));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    const block = await screen.findByRole("status");
    expect(block.textContent).not.toContain("Sessions could not be read.");
    expect(block.textContent).toContain("The session list could not be read because Straza did not answer. Check that it is running, then reload.");
    expect(rows()).toEqual([OWN, CAROL, JOE_ONE, JOE_TWO]);
  });

  it("reloads the list and the overview from the outline Reload", async () => {
    mount();
    await ready();
    const reload = screen.getByRole("button", { name: "Reload the list" });
    expect(reload.textContent?.trim()).toBe("Reload");
    expect(reload.getAttribute("data-variant")).toBe("outline");
    const reads = vi.mocked(listSessions).mock.calls.length;
    await userEvent.click(reload);
    expect(vi.mocked(listSessions).mock.calls.length).toBe(reads + 1);
    expect(vi.mocked(overview).mock.calls.length).toBe(2);
  });

  it("heads the list with the server's count of active sessions and says nothing when the overview cannot be read", async () => {
    mount();
    await ready();
    // The count is the server's, not the rows this page holds.
    expect(headline()).toBe("4 active sessions");
    expect(live()?.textContent).toBe("Live updates on");

    vi.mocked(overview).mockRejectedValue(failure("overview failed", 403));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    await ready();
    expect(live()).toBeNull();
    expect(headline()).toBeNull();
  });

  const LIVE: [string, OverviewAnswer, string, string, string][] = [
    ["every active client connected", { push: { lane_up: true, connected: 4 }, sessions: { total: 9, active: 4 } }, "on", "Live updates on", "Every active client is connected to Straza and hears of a change, such as a revoke, at once."],
    ["some active clients connected", { push: { lane_up: true, connected: 3 }, sessions: { total: 9, active: 7 } }, "on", "Live updates on", "3 of 7 active clients are connected to Straza and hear of a change, such as a revoke, at once. The others check every 30 s."],
    ["the push lane down", { push: { lane_up: false }, sessions: { total: 9, active: 7 } }, "off", "Live updates off", "Straza cannot push a change to its clients right now. Each client checks for changes every 30 s."],
  ];
  it.each(LIVE)("words the live updates beside the headline for %s, the rest behind help", async (_case, answer, state, words, help) => {
    vi.mocked(overview).mockResolvedValue(answer);
    mount();
    await ready();
    const part = live() as HTMLElement;
    expect(part.getAttribute("data-live")).toBe(state);
    expect(part.textContent).toBe(words);
    expect((part.querySelector("[aria-hidden=true]") as HTMLElement).className).toContain(state === "on" ? "bg-ok" : "bg-warn");
    await userEvent.hover(within(part).getByRole("button", { name: "Help: About live updates" }));
    expect((await screen.findByRole("tooltip")).textContent).toBe(help);
  });

  it.each([
    ["an answer without the push block", { sessions: { total: 9, active: 7 } }, "7 active sessions", false],
    ["an answer without the session counts", { push: { lane_up: true, connected: 2 } }, null, true],
  ] as [string, OverviewAnswer, string | null, boolean][])("draws the headline's parts the overview answers for %s", async (_case, answer, count, hasLive) => {
    vi.mocked(overview).mockResolvedValue(answer);
    mount();
    await ready();
    expect(headline()).toBe(count);
    expect(!!live()).toBe(hasLive);
  });

  it("renders a username that looks like markup as text", async () => {
    vi.mocked(listSessions).mockResolvedValue(page([{ ...joeOne, username: "<img src=x onerror=alert(1)>" }]));
    mount();
    await screen.findByRole("button", { name: "Open " + JOE_ONE });
    // The name shows once, on the group row; the grouped list draws no
    // user column.
    expect(screen.getAllByText("<img src=x onerror=alert(1)>").length).toBe(1);
    expect(document.querySelector("img")).toBeNull();
  });
});

describe("the sessions poll", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: OWN });
    vi.mocked(listSessions).mockReset().mockImplementation((q: string) => Promise.resolve(answer(q)));
    vi.mocked(overview).mockReset().mockResolvedValue({ push: { lane_up: true, connected: 4 }, sessions: { total: 9, active: 4 } });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("reads the first page again every 10 seconds and keeps a group the person folded", async () => {
    mount();
    await ready();
    fireEvent.click(screen.getByRole("button", { name: hideGroup("carol") }));
    expect(rows()).toEqual([OWN, JOE_ONE, JOE_TWO]);
    const reads = vi.mocked(listSessions).mock.calls.length;

    await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
    expect(vi.mocked(listSessions).mock.calls.length).toBe(reads + 1);
    expect(lastQuery()).toContain("status=active");
    expect(screen.getByRole("button", { name: showGroup("carol") }).getAttribute("aria-expanded")).toBe("false");
    expect(rows()).toEqual([OWN, JOE_ONE, JOE_TWO]);
  });
});
