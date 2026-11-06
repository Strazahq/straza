import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ServerObjects } from "./server-objects";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type ObjectListing, type ObjectRow, type ObjectsAnswer, type ObjectsBlock, appObjects, refreshAppObjects } from "@/lib/api";
import { navigate, pathFor } from "@/lib/router";
import { notify } from "@/lib/notify";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), appObjects: vi.fn(), refreshAppObjects: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const NOW = Date.now();
const iso = (secondsAgo: number) => new Date(NOW - secondsAgo * 1000).toISOString();
const apiError = (status: number, message: string, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

// The fixture server is named ghapp so the check that no shown string says
// the word for the thing in general stays meaningful.
const PINNED: ObjectsBlock = {
  type: "repo",
  plural: "repos",
  list: { tool: "repo_list", attributes: [{ name: "area", setBy: "org" }, { name: "team" }] },
  tools: { repo_read: { argument: "repo" }, search_code: "all", whoami: "none" },
};
const app = {
  id: "app-13", name: "ghapp", runtime: "remote", status: "running", reached_by: [],
  manifest: { apiVersion: "straza.dev/v1beta1", kind: "App", metadata: { name: "ghapp" }, straza: { runtime: { kind: "remote" }, objects: PINNED } },
};
const roles = [{ id: "r-read", name: "ghapp-read", kind: "application" }, { id: "r-write", name: "ghapp-write", kind: "application" }];
const ROWS: ObjectRow[] = [
  { value: "Strazahq/straza", id: "9012331", title: "straza", attributes: { area: "product", team: ["core", "web"] }, new: false, coveredBy: ["ghapp-read", "ghapp-write"] },
  { value: "Strazahq/ops-runbooks", id: "8034417", title: "ops-runbooks", attributes: { area: "ops" }, new: false, coveredBy: [] },
  { value: "Strazahq/straza-desktop", id: "9120487", attributes: {}, new: true, coveredBy: ["ghapp-read", "ghapp-legacy"] },
];

const answerOf = (over: Partial<ObjectsAnswer> = {}, listing: Partial<ObjectListing> = {}): ObjectsAnswer => ({
  app: "ghapp", type: "repo", plural: "repos",
  attributes: [{ name: "area", setBy: "org" }, { name: "team", setBy: "object" }],
  pinned: PINNED, declared: PINNED, declaredFaults: [], drift: false,
  listing: { tool: "repo_list", credential: "set", outcome: "ok", version: 3, count: 3, joined: 0, left: 0, listedAt: iso(180), attemptedAt: iso(180), dueAt: iso(-420), ...listing },
  objects: ROWS, total: 3,
  ...over,
});

const mount = () => render(<TooltipProvider><ServerObjects app={app} roles={roles} /></TooltipProvider>);
const strip = async () => waitFor(() => { const el = document.querySelector("[data-listing-strip]") as HTMLElement; expect(el).not.toBeNull(); return el; });
const table = async () => waitFor(() => { const el = document.querySelector("[data-server-objects]") as HTMLElement; expect(el).not.toBeNull(); return el; });
const rowOf = (t: HTMLElement, value: string) => within(t).getAllByRole("row").find((r) => r.querySelector("td [data-value]")?.textContent === value) as HTMLElement;
// tip hovers one help icon and reads the tooltip its trigger names, since
// an earlier tooltip may still be in the document.
const tip = async (name: string) => {
  const b = screen.getByRole("button", { name: "Help: " + name });
  await userEvent.hover(b);
  return waitFor(() => {
    const id = b.getAttribute("aria-describedby");
    expect(id).toBeTruthy();
    return document.getElementById(id as string)?.textContent || "";
  });
};

describe("the objects tab of an MCP server", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(appObjects).mockResolvedValue(answerOf());
  });

  const LEAD = "ghapp lists its repos with repo_list.";
  // R1 is the reason a listing stores when the credential is missing, and
  // MISSING the strip's own line, which names the field on Overview first.
  const R1 = "ghapp has no listing credential, so Straza cannot list its repos. Add one with strazactl apps secret set ghapp --listing, then refresh with strazactl apps objects refresh ghapp.";
  const MISSING = "ghapp has no listing credential, so Straza cannot list its repos. Set one under Server authentication on Overview, or with strazactl apps secret set ghapp --listing.";
  const R3 = "repo_list answered an error: bad credentials. Check ghapp's logs on its Activity tab.";
  // Each case is the listing state the API answers and the strip it shows:
  // its tone and each of its lines, the reason sentences verbatim.
  const strips: [string, Partial<ObjectListing>, string, string[]][] = [
    ["a listing that changed nothing", {}, "plain", [LEAD, "Last listing 3 m ago: 3 repos, no change."]],
    ["the first listing", { version: 1, joined: 3 }, "plain", [LEAD, "Last listing 3 m ago: 3 repos."]],
    ["a listing with new repos", { version: 4, count: 12, joined: 2 }, "ok", [LEAD, "Last listing 3 m ago: 12 repos, 2 new."]],
    ["a listing where repos joined and left", { version: 4, count: 1, joined: 2, left: 1 }, "ok", [LEAD, "Last listing 3 m ago: 1 repo, 2 new, 1 left the list."]],
    ["a failed listing after a good one", { outcome: "failed", reason: R3, attemptedAt: iso(20) }, "danger", [LEAD, R3, "The list below is from the last good listing, 3 m ago."]],
    ["a listing that never worked", { outcome: "failed", reason: R3, version: 0, count: 0, listedAt: undefined }, "danger", [LEAD, R3, "No listing has worked yet, so there are no repos to show."]],
    ["a missing listing credential", { credential: "missing", outcome: "never", version: 0, count: 0, listedAt: undefined, attemptedAt: undefined }, "danger", [LEAD, MISSING]],
    ["a missing credential the failed listing names in its own words", { credential: "missing", outcome: "failed", reason: R1 }, "danger", [LEAD, MISSING, "The list below is from the last good listing, 3 m ago."]],
    ["a server not listed yet", { outcome: "never", version: 0, count: 0, listedAt: undefined, attemptedAt: undefined }, "plain", [LEAD, "No listing yet. Straza runs the first one within 30 seconds. Reload the page to see it."]],
  ];
  it.each(strips)("words the strip for %s", async (_case, listing, tone, lines) => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({}, listing));
    mount();
    const el = await strip();
    expect(el.getAttribute("data-tone")).toBe(tone);
    expect([...el.querySelectorAll("[data-line]")].map((l) => l.textContent)).toEqual(lines);
  });

  // Each case is a listing state and whether its strip offers List again:
  // a failed listing with its credential set does, and nothing else.
  const agains: [string, Partial<ObjectListing>, boolean][] = [
    ["a failed listing with its credential set", { outcome: "failed", reason: R3 }, true],
    ["a failed listing of a server that needs no credential", { outcome: "failed", reason: R3, credential: "not-needed" }, true],
    ["a failed listing while the credential is missing", { outcome: "failed", reason: R1, credential: "missing" }, false],
    ["a listing that worked", {}, false],
    ["a server not listed yet", { outcome: "never", version: 0, count: 0, listedAt: undefined }, false],
  ];
  it.each(agains)("offers List again for %s only where a listing can work", async (_case, listing, offered) => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({}, listing));
    mount();
    const el = await strip();
    expect(within(el).queryByRole("button", { name: "List again" }) !== null).toBe(offered);
  });

  it("asks for a listing on List again and reads the tab again", async () => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({}, { outcome: "failed", reason: R3 }));
    vi.mocked(refreshAppObjects).mockResolvedValue({ app: "ghapp", dueAt: iso(0) });
    mount();
    await userEvent.click(within(await strip()).getByRole("button", { name: "List again" }));
    await waitFor(() => expect(refreshAppObjects).toHaveBeenCalledWith("app-13"));
    expect(notify.ok).toHaveBeenCalledWith("Straza is listing ghapp's repos again. Reload the page in a minute to see the list.");
    await waitFor(() => expect(appObjects).toHaveBeenCalledTimes(2));
  });

  it("shows the server's wait when the server was listed within the last minute", async () => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({}, { outcome: "failed", reason: R3 }));
    vi.mocked(refreshAppObjects).mockRejectedValue(apiError(429, "ghapp was listed 20 seconds ago. Refresh again in 40 seconds."));
    mount();
    await userEvent.click(within(await strip()).getByRole("button", { name: "List again" }));
    await waitFor(() => expect(notify.warn).toHaveBeenCalledWith("ghapp was listed 20 seconds ago. Refresh again in 40 seconds."));
    expect(appObjects).toHaveBeenCalledTimes(1);
  });

  it.each([
    ["set", "Straza calls repo_list every 10 minutes with ghapp's listing credential. A failed listing changes nothing, and every listing is in the audit log."],
    ["not-needed", "Straza calls repo_list every 10 minutes. A failed listing changes nothing, and every listing is in the audit log."],
  ])("explains how the listing runs behind the help icon when the credential is %s", async (credential, text) => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({}, { credential }));
    mount();
    await strip();
    expect(await tip("Listing")).toBe(text);
  });

  it("reads the first page without a filter and lays out one column per attribute", async () => {
    mount();
    const t = await table();
    expect(appObjects).toHaveBeenCalledWith("app-13", "");
    expect(within(t).getAllByRole("columnheader").map((h) => h.textContent?.trim())).toEqual(["Repo", "area", "team", "Covered by", "In open drafts"]);
    expect(t.querySelectorAll("col").length).toBe(5);
    const straza = rowOf(t, "Strazahq/straza");
    expect([...straza.querySelectorAll("[data-attr]")].map((a) => a.getAttribute("data-attr") + "=" + a.textContent)).toEqual(["area=product", "team=core", "team=web"]);
    expect(straza.querySelector("[data-new]")).toBeNull();
    const desktop = rowOf(t, "Strazahq/straza-desktop");
    expect(desktop.querySelector("[data-new]")?.textContent).toBe("new");
    expect([...desktop.querySelectorAll("[data-not-set]")].map((n) => n.textContent)).toEqual(["not set", "not set"]);
    expect(screen.getByText("3 of 3 shown")).toBeTruthy();
  });

  // Each case is how many attributes the listing declares and the share of
  // each attribute column: they shrink so Covered by keeps a fifth or more.
  it.each([
    [2, "12%"],
    [3, "8.67%"],
    [6, "4.33%"],
  ])("shares the width with %i attributes so Covered by keeps a fifth", async (n, each) => {
    const attributes = Array.from({ length: n }, (_, i) => ({ name: "field" + i, setBy: "object" }));
    vi.mocked(appObjects).mockResolvedValue(answerOf({ type: "ticket", plural: "tickets", attributes, objects: [], total: 0 }));
    mount();
    const t = await table();
    const widths = [...t.querySelectorAll("col")].map((c) => (c as HTMLElement).style.width);
    expect(widths).toEqual(["28%", ...Array(n).fill(each), "", "26%"]);
    const fixed = widths.filter(Boolean).reduce((sum, w) => sum + parseFloat(w), 0);
    expect(100 - fixed).toBeGreaterThanOrEqual(19.9);
  });

  it("links each covering role to its page and says when no role reaches a repo", async () => {
    mount();
    const t = await table();
    const straza = rowOf(t, "Strazahq/straza");
    expect([...straza.querySelectorAll("[data-covered-by]")].map((c) => c.textContent)).toEqual(["ghapp-read", "ghapp-write"]);
    const link = within(straza).getByRole("link", { name: "ghapp-write" });
    expect(link.getAttribute("href")).toBe(pathFor("roles", ["r-write"]));
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("roles", ["r-write"]);
    const runbooks = rowOf(t, "Strazahq/ops-runbooks");
    expect(runbooks.querySelectorAll("td")[3].textContent).toBe("no role No agent reaches it.");
    // A role the session's roles list does not carry is named without a link.
    const desktop = rowOf(t, "Strazahq/straza-desktop");
    expect(within(desktop).queryByRole("link", { name: "ghapp-legacy" })).toBeNull();
    expect(desktop.textContent).toContain("ghapp-legacy");
  });

  it("says who may set each attribute and what each column holds behind the header help icons", async () => {
    mount();
    await table();
    expect(await tip("area")).toBe("Only the organization's admins can set it, so the admins of one repo cannot move it into a role.");
    expect(await tip("team")).toBe("The repo's own admins can set it too.");
    expect(await tip("Covered by")).toBe("The roles whose tools on ghapp reach this repo today.");
    expect(await tip("In open drafts")).toBe("Open drafts you may read that would add a role to this repo or drop one from it. Nothing changes until a person publishes the draft.");
  });

  it("keeps each id behind a help icon on its row instead of a column", async () => {
    mount();
    const t = await table();
    expect(t.querySelector("[data-id]")).toBeNull();
    const straza = rowOf(t, "Strazahq/straza");
    expect(within(straza).getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual(["Help: Id of Strazahq/straza"]);
    expect(await tip("Id of Strazahq/straza")).toBe("The id the server gives this repo is 9012331. If it is deleted and made again under the same name, it gets a new id, so no access carries over.");
    expect(within(straza).getAllByRole("cell")[0].textContent).toBe("Strazahq/straza");
  });

  // The rows of an invented type carry the open drafts that would change
  // their roles: one that adds a role and drops another, one with none,
  // and one from a server too old to send the field at all.
  const DESK: ObjectRow[] = [
    { value: "TCK-1", id: "t-1", coveredBy: ["desk-agents"], drafts: [{ draft: "241", role: "desk-triage", change: "adds" }, { draft: "244", role: "desk-agents", change: "drops" }] },
    { value: "TCK-2", id: "t-2", coveredBy: ["desk-agents"], drafts: [] },
    { value: "TCK-3", id: "t-3", coveredBy: [] },
  ];
  const deskAnswer = () => answerOf({ type: "ticket", plural: "tickets", attributes: [], objects: DESK, total: 3 }, { tool: "ticket_list", count: 3 });
  const draftLines = (r: HTMLElement) => [...r.querySelectorAll("[data-object-draft]")].map((d) => d.textContent);

  it("lists the open drafts that add or drop a role on each row, each draft a link to its page", async () => {
    vi.mocked(appObjects).mockResolvedValue(deskAnswer());
    mount();
    const t = await table();
    expect(within(t).getAllByRole("columnheader").map((h) => h.textContent?.trim())).toEqual(["Ticket", "Covered by", "In open drafts"]);
    const one = rowOf(t, "TCK-1");
    expect(draftLines(one)).toEqual(["Draft 241 adds desk-triage", "Draft 244 drops desk-agents"]);
    const link = within(one).getByRole("link", { name: "Draft 244" });
    expect(link.getAttribute("href")).toBe(pathFor("drafts", ["244"]));
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("drafts", ["244"]);
    expect(await tip("Id of TCK-1")).toBe("The id the server gives this ticket is t-1. If it is deleted and made again under the same name, it gets a new id, so no access carries over.");
  });

  it("leaves the drafts cell empty for a row with no open draft and for a server that sends no drafts", async () => {
    vi.mocked(appObjects).mockResolvedValue(deskAnswer());
    mount();
    const t = await table();
    for (const value of ["TCK-2", "TCK-3"]) {
      const cell = rowOf(t, value).querySelector("[data-drafts]") as HTMLElement;
      expect(cell.textContent).toBe("");
      expect(draftLines(rowOf(t, value))).toEqual([]);
    }
  });

  it("shows what the server declares in a fold, and warns when it drifted from the pin", async () => {
    mount();
    await table();
    expect(document.querySelector("[data-drift]")).toBeNull();
    const fold = document.querySelector("[data-declared]") as HTMLElement;
    expect(fold.querySelector("summary")?.textContent).toBe("What ghapp declares in its tools/list answer");
    const pre = fold.querySelector("pre")?.textContent || "";
    for (const want of ["type: repo", "tool: repo_list", "setBy: org", "repo_read:\n    argument: repo", "search_code: all"]) expect(pre).toContain(want);
    document.body.innerHTML = "";

    vi.mocked(appObjects).mockResolvedValue(answerOf({ drift: true, declared: { ...PINNED, tools: { repo_read: "all" } }, declaredFaults: ["issue_create carries a mark of type issue, but ghapp declares type repo."] }));
    mount();
    const drift = await waitFor(() => { const el = document.querySelector("[data-drift]") as HTMLElement; expect(el).not.toBeNull(); return el; });
    expect(drift.textContent).toBe("ghapp's marks differ from the pinned repos. Draft the change with strazactl apps objects draft ghapp.");
    expect([...document.querySelectorAll("[data-declared-fault]")].map((f) => f.textContent)).toEqual(["issue_create carries a mark of type issue, but ghapp declares type repo."]);
  });

  it("says when no tool carries a mark", async () => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({ declared: null }));
    mount();
    await table();
    const fold = document.querySelector("[data-declared]") as HTMLElement;
    expect(fold.querySelector("pre")).toBeNull();
    expect(fold.textContent).toContain("None of ghapp's tools carries a Straza mark, so only its manifest says what its repos are.");
  });

  it("loads the next page after the last value and stops when the list ends", async () => {
    vi.mocked(appObjects).mockResolvedValueOnce(answerOf({ objects: ROWS.slice(0, 2), total: 3, next: "Strazahq/ops-runbooks" }));
    vi.mocked(appObjects).mockResolvedValueOnce(answerOf({ objects: ROWS.slice(2), total: 3 }));
    mount();
    await table();
    expect(screen.getByText("2 of 3 shown")).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Load more" }));
    await screen.findByText("3 of 3 shown");
    expect(appObjects).toHaveBeenLastCalledWith("app-13", "after=Strazahq%2Fops-runbooks");
    expect(within(await table()).getAllByRole("row").length).toBe(4);
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("sends the filter as q and says when nothing matches", async () => {
    mount();
    await table();
    vi.mocked(appObjects).mockResolvedValue(answerOf({ objects: [], total: 0 }));
    await userEvent.type(screen.getByRole("textbox", { name: "Filter repos" }), "zzz");
    await waitFor(() => expect(appObjects).toHaveBeenLastCalledWith("app-13", "q=zzz"));
    expect(await screen.findByText('No repo matches "zzz".')).toBeTruthy();
    expect(screen.getByText("0 of 0 shown")).toBeTruthy();
  });

  it("says when the list holds nothing", async () => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({ objects: [], total: 0 }, { count: 0 }));
    mount();
    expect(await screen.findByText("The list holds no repos.")).toBeTruthy();
  });

  it("takes its words from the type, the plural falling back to the type plus s", async () => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({ type: "project", plural: "", attributes: [], objects: [ROWS[1]], total: 1 }, { tool: "list_projects", count: 1 }));
    mount();
    const el = await strip();
    expect(el.textContent).toContain("ghapp lists its projects with list_projects.");
    expect(el.textContent).toContain("Last listing 3 m ago: 1 project, no change.");
    expect(within(await table()).getAllByRole("columnheader").map((h) => h.textContent?.trim())).toEqual(["Project", "Covered by", "In open drafts"]);
    expect(screen.getByRole("textbox", { name: "Filter projects" })).toBeTruthy();
  });

  it("never says object or catalog in what it shows", async () => {
    vi.mocked(appObjects).mockResolvedValue(answerOf({ drift: true, declared: null }, { credential: "missing", outcome: "failed", reason: R1 }));
    mount();
    await table();
    const tips: string[] = [];
    for (const name of ["Listing", "area", "team", "Covered by", "In open drafts", "Id of Strazahq/straza"]) tips.push(await tip(name));
    // The strazactl command names are grammar, not words, so they are cut first.
    const shown = ((document.body.textContent || "") + tips.join(" ")).replace(/strazactl apps objects/g, "").toLowerCase();
    expect(shown).not.toContain("object");
    expect(shown).not.toContain("catalog");
  });

  it("says the list could not be read in the house shape", async () => {
    vi.mocked(appObjects).mockRejectedValue(apiError(404, "unknown server"));
    mount();
    const el = await waitFor(() => { const e = document.querySelector("[data-fetch-error]") as HTMLElement; expect(e).not.toBeNull(); return e; });
    expect(el.textContent).toBe("Repos: unreachable, state unknown. The repos could not be read: unknown server. Reload to try again.");
  });
});
