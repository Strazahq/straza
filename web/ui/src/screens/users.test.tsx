import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Users } from "./users";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type KindCounts, type LockRow, type Page, type RoleRow, type UserRow, listRoles, listUsers } from "@/lib/api";
import { NO_SPONSOR_CHIP } from "@/lib/entra-words";
import { put } from "@/lib/handoff";
import { LOAD_MORE, RELOAD_LIST, RELOAD_NOW } from "@/lib/role-words";
import { navigate } from "@/lib/router";
import { ALSO_DISABLED, ANY_ROLE, CLEAR_SPONSOR, KIND_TABS_LABEL, SEARCH_LABEL, SEARCH_PLACEHOLDER, countLine, emptyKind } from "@/lib/user-list-words";
import { NOT_YET } from "@/lib/user-look";
import { EMPTY_DIRECTORY, EMPTY_FILTERED, NEVER_SEEN, sponsorFilterWords, sponsorShow } from "@/lib/user-words";

const DAY = 86400000;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

const disable: LockRow = { origin: "admin", reason: "user disabled by admin", created_at: "2026-11-01T10:00:00Z" };
const lock: LockRow = { origin: "admin", reason: "incident 42: token pasted in a public channel", created_at: "2026-11-02T14:02:00Z" };

// The identities in the shapes the paged users lane answers: a person who
// sponsors agents, a locked person whose display name carries markup, an
// agent with no display name that never checked in, and a service made in
// Straza. The rest stand in for one state each.
const alice: UserRow = {
  id: "u-alice", username: "alice", display: "Alice Marin", email: "alice@corp.example",
  status: "active", origin: "scim", kind: "human", external_id: "3f1a",
  created_at: "2026-07-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z",
  effective_roles: ["dev", "straza-admin", "sec-approvers"], locks: [],
  last_seen: ago(5 * DAY + 3600000), sponsored_count: 3,
};
const carol: UserRow = {
  id: "u-carol", username: "carol", display: "Carol <b>Reyes</b>", email: "carol@corp.example",
  status: "active", origin: "scim", kind: "human",
  created_at: "2026-07-02T10:00:00Z", updated_at: "2026-09-09T10:00:00Z",
  effective_roles: ["dev"], locks: [lock], last_seen: ago(2 * 3600000), sponsored_count: 0,
};
const nina: UserRow = {
  id: "u-nina", username: "nina-research-agent", status: "active", origin: "scim", kind: "nhi",
  user_type: "agent", sponsor: "alice", agency_mode: "supervised",
  created_at: "2026-09-12T08:12:00Z", updated_at: "2026-09-12T08:12:00Z",
  effective_roles: [], locks: [], sponsored_count: 0,
};
const ci: UserRow = {
  id: "u-ci", username: "ci-deployer", display: "CI deployer", status: "active", origin: "local", kind: "nhi",
  user_type: "service", created_at: "2026-08-01T10:00:00Z", updated_at: "2026-08-01T10:00:00Z",
  effective_roles: ["github-readers"], locks: [], last_seen: ago(3600000), sponsored_count: 0,
};
const dave: UserRow = {
  id: "u-dave", username: "dave", display: "Dave Okafor", status: "disabled", origin: "local", kind: "human",
  created_at: "2026-06-01T10:00:00Z", updated_at: "2026-08-02T10:00:00Z",
  effective_roles: [], locks: [disable], last_seen: "2026-08-02T09:14:00Z", sponsored_count: 0,
};
const erin: UserRow = { ...dave, id: "u-erin", username: "erin", display: "Erin Vale", locks: [disable, lock] };
// fern is active again from the identity manager while a disable in
// Straza still holds her.
const fern: UserRow = { ...dave, id: "u-fern", username: "fern", display: "Fern Ito", status: "active", locks: [{ ...disable, origin: "admin-disable" }] };
const lab: UserRow = {
  ...nina, id: "u-lab", username: "lab-entra-agent@lab.example.onmicrosoft.com", display: "Lab agent from Entra", sponsor: "carol",
};

const roles: RoleRow[] = [
  { id: "r-dev", name: "dev", kind: "application" },
  { id: "r-sre", name: "sre", kind: "application" },
];

const page = (items: UserRow[], next = ""): Page<UserRow> => ({ items, next_cursor: next });
const counted = (items: UserRow[], kind_counts: KindCounts, next = "") => ({ ...page(items, next), kind_counts });
const FIRST = "sort=name&order=asc&limit=100";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listUsers: vi.fn(),
  listRoles: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = () => render(<TooltipProvider><Users /></TooltipProvider>);

// rows names the rendered rows in table order, told apart from the other
// buttons by the data-user attribute the list puts on every row.
const rows = () => screen.getAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-user")).map((b) => b.getAttribute("data-user"));
const cell = (user: string, column: string) => document.querySelector(`[data-user="${user}"] [data-column="${column}"]`) as HTMLElement;
const countText = () => (document.querySelector("[data-row-count]") as HTMLElement).textContent;
const tab = (kind: string) => document.querySelector(`[data-kind-tab="${kind}"]`) as HTMLElement;
const emptyText = () => (document.querySelector("[data-empty-text]") as HTMLElement).textContent;

describe("the Users screen", () => {
  beforeEach(() => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol, nina, ci]));
    vi.mocked(listRoles).mockResolvedValue(roles);
  });

  it("lists what the server answered, in the server's order, and sorts on the server", async () => {
    mount();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Users");
    expect(screen.getByRole("button", { name: RELOAD_LIST }).getAttribute("data-variant")).toBe("outline");
    await screen.findByRole("button", { name: "Open alice" });
    expect(listUsers).toHaveBeenCalledWith(FIRST);
    expect(rows()).toEqual(["alice", "carol", "nina-research-agent", "ci-deployer"]);
    // The header's arrow says the sort, and no line under the filters does.
    expect(document.querySelector("[data-sorted-hint]")).toBeNull();
    const byName = screen.getByRole("button", { name: "Sort by user" });
    expect(byName.getAttribute("data-sorted")).toBe("asc");

    // A time column reads newest first on the first click, oldest first on
    // the second, and the server answers both.
    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("sort=last_seen&order=desc&limit=100"));
    expect(screen.getByRole("button", { name: "Sort by last seen" }).getAttribute("data-sorted")).toBe("desc");
    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("sort=last_seen&order=asc&limit=100"));
  });

  it("offers the four kind tabs in order, with an icon on each kind and the counts the server sends", async () => {
    vi.mocked(listUsers).mockResolvedValue(counted([alice, carol, nina, ci], { person: 2, agent: 1, service: 1 }));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const group = screen.getByRole("group", { name: KIND_TABS_LABEL });
    expect(group.hasAttribute("data-kind-tabs")).toBe(true);
    const options = within(group).getAllByRole("button");
    expect(options.map((b) => b.getAttribute("data-kind-tab"))).toEqual(["all", "person", "agent", "service"]);
    expect(options.map((b) => b.textContent)).toEqual(["All 4", "People 2", "AI agents 1", "Services 1"]);
    expect(options.map((b) => b.querySelectorAll("svg").length)).toEqual([0, 1, 1, 1]);
    expect(tab("all").getAttribute("aria-pressed")).toBe("true");
    expect(countText()).toBe(countLine(4, 4, false));
    expect(countText()).toBe("4 users");
  });

  it("counts every kind on its tab while further rows wait on the server, All their sum", async () => {
    vi.mocked(listUsers).mockResolvedValue(counted([alice, carol, nina, ci], { person: 140, agent: 90, service: 10 }, "c1"));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const group = screen.getByRole("group", { name: KIND_TABS_LABEL });
    expect(within(group).getAllByRole("button").map((b) => b.textContent)).toEqual(["All 240", "People 140", "AI agents 90", "Services 10"]);
    expect(countText()).toBe(countLine(4, 240, true));
    expect(countText()).toBe("4 of 240 users");
  });

  it("shows no count on the tabs when the answer carries no kind counts", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol, nina, ci], "c1"));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const group = screen.getByRole("group", { name: KIND_TABS_LABEL });
    expect(within(group).getAllByRole("button").map((b) => b.textContent)).toEqual(["All", "People", "AI agents", "Services"]);
    expect(countText()).toBe(countLine(4, undefined, true));
    expect(countText()).toBe("4 users shown");
  });

  it("sends a kind tab to the server with the other filters, and reads the list again from its first page", async () => {
    vi.mocked(listUsers).mockResolvedValue(counted([alice, carol], { person: 2, agent: 1, service: 1 }, "c1"));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockResolvedValue(counted([ci], { person: 2, agent: 1, service: 1 }));
    await userEvent.click(screen.getByRole("button", { name: LOAD_MORE }));
    await waitFor(() => expect(rows()).toEqual(["alice", "carol", "ci-deployer"]));

    vi.mocked(listUsers).mockResolvedValue(counted([nina], { person: 2, agent: 1, service: 1 }));
    await userEvent.click(tab("agent"));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("kind=agent&" + FIRST));
    await waitFor(() => expect(rows()).toEqual(["nina-research-agent"]));
    expect(tab("agent").getAttribute("aria-pressed")).toBe("true");
    expect(countText()).toBe("1 user");

    await userEvent.click(screen.getByRole("combobox", { name: "Status" }));
    await userEvent.click(await screen.findByRole("option", { name: "active" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=active&kind=agent&" + FIRST));

    vi.mocked(listUsers).mockResolvedValue(counted([alice, carol, nina, ci], { person: 2, agent: 1, service: 1 }));
    await userEvent.click(tab("all"));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=active&" + FIRST));
    await waitFor(() => expect(rows()).toEqual(["alice", "carol", "nina-research-agent", "ci-deployer"]));
  });

  it("says no user of the kind is in the list when a kind tab with no other filter finds none", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockResolvedValue(counted([], { person: 2, agent: 0, service: 0 }));
    await userEvent.click(tab("service"));
    await waitFor(() => expect(emptyText()).toBe(emptyKind("service")));
    expect(emptyText()).toBe("No services.");
    expect(countText()).toBe("0 users");
  });

  it("says no user matches the filters when a kind tab with another filter finds none", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockResolvedValue(counted([], { person: 0, agent: 0, service: 0 }));
    await userEvent.click(screen.getByRole("combobox", { name: "Status" }));
    await userEvent.click(await screen.findByRole("option", { name: "disabled" }));
    await userEvent.click(tab("agent"));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=disabled&kind=agent&" + FIRST));
    await waitFor(() => expect(emptyText()).toBe(EMPTY_FILTERED));
  });

  it("draws each user's kind tile with the overlay of a lock or a disable", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol, nina, ci, dave]));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const tile = (user: string) => cell(user, "kind");
    const overlay = (user: string) => tile(user).querySelector("span > span[aria-hidden] svg")?.getAttribute("class") || "";
    expect(within(tile("alice")).getByRole("img", { name: "Person" })).toBeTruthy();
    expect(within(tile("nina-research-agent")).getByRole("img", { name: "AI agent" })).toBeTruthy();
    expect(within(tile("ci-deployer")).getByRole("img", { name: "Service" })).toBeTruthy();
    expect(overlay("alice")).toBe("");
    expect(overlay("carol")).toContain("text-danger");
    expect(overlay("dave")).toContain("text-muted-foreground");
    // The kind column has no header.
    expect(document.querySelector("thead th")?.textContent).toBe("");
  });

  it("names the user in one line, with and without a display name, and cuts a long sign-in name", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, nina, lab]));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const named = cell("alice", "name");
    expect(named.textContent).toBe("Alice Marin" + "alice");
    expect(within(named).getByText("Alice Marin").className).toContain("font-semibold");
    const signIn = within(named).getByText("alice");
    expect(signIn.className).toContain("font-mono");
    expect(signIn.className).toContain("text-muted-foreground");

    const bare = within(cell("nina-research-agent", "name")).getByText("nina-research-agent");
    expect(bare.className).toContain("font-mono");
    expect(bare.className).toContain("font-semibold");
    expect(bare.getAttribute("title")).toBe("nina-research-agent");

    const long = within(cell(lab.username, "name")).getByText(lab.username);
    expect(long.className).toContain("truncate");
    expect(long.getAttribute("title")).toBe(lab.username);
  });

  it("reads the sponsor of an agent and the agents a person sponsors, and leaves anyone else empty", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const link = within(cell("alice", "sponsor")).getByRole("button", { name: sponsorShow(3, "alice") });
    expect(link.textContent).toBe("3 agents");
    expect(link.getAttribute("title")).toBe(sponsorShow(3, "alice"));
    expect(link.getAttribute("data-sponsor-count")).toBe("alice");

    const sponsored = cell("nina-research-agent", "sponsor");
    expect(sponsored.textContent).toBe("alice");
    expect(sponsored.querySelector('[data-kind-icon="person"]')).toBeTruthy();
    expect(within(sponsored).getByText("alice").getAttribute("title")).toBe("alice");
    expect(within(sponsored).queryByRole("button")).toBeNull();

    expect(cell("carol", "sponsor").textContent).toBe("");
    expect(cell("ci-deployer", "sponsor").textContent).toBe("");
  });

  it("names the source of each user with its icon", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const idm = cell("alice", "source").querySelector("[data-source]") as HTMLElement;
    expect(idm.getAttribute("data-source")).toBe("Identity manager");
    expect(idm.textContent).toBe("Identity manager");
    expect(idm.querySelector('[data-kind-icon="from-idm"]')).toBeTruthy();
    const straza = cell("ci-deployer", "source").querySelector("[data-source]") as HTMLElement;
    expect(straza.getAttribute("data-source")).toBe("Straza");
    expect(straza.querySelector('[data-kind-icon="from-straza"]')).toBeTruthy();
  });

  it("names a person from Entra, active or disabled in Entra, with the cloud icon and the plain status chips", async () => {
    const fromEntra: LockRow = { origin: "entra", reason: "the account is disabled in Entra", created_at: "2026-11-17T09:12:40Z" };
    const ada: UserRow = { ...alice, id: "u-ada", username: "ada@corp.example", display: "Ada Lind", origin: "entra", external_id: "1b4e7c90", sponsored_count: 0 };
    const ellen: UserRow = { ...ada, id: "u-ellen", username: "ellen@corp.example", display: "Ellen Hart", status: "disabled", locks: [fromEntra] };
    const both: UserRow = { ...ellen, id: "u-both", username: "both@corp.example", display: "Both States", locks: [fromEntra, lock] };
    vi.mocked(listUsers).mockResolvedValue(page([ada, ellen, both]));
    mount();
    await screen.findByRole("button", { name: "Open ada@corp.example" });
    for (const u of ["ada@corp.example", "ellen@corp.example", "both@corp.example"]) {
      const src = cell(u, "source").querySelector("[data-source]") as HTMLElement;
      expect(src.textContent).toBe("Entra");
      expect(src.querySelector('[data-kind-icon="from-entra"]')).toBeTruthy();
    }
    expect(cell("ada@corp.example", "status").querySelector("[data-user-status]")?.getAttribute("data-user-status")).toBe("active");
    expect(cell("ellen@corp.example", "status").querySelector("[data-user-status]")?.getAttribute("data-user-status")).toBe("disabled");
    expect(cell("both@corp.example", "status").querySelector("[data-locked]")?.getAttribute("title")).toBe(ALSO_DISABLED + " " + lock.reason);
  });

  it("names an agent from Entra by its source and its sponsor, or marks it with no sponsor", async () => {
    const born: UserRow = { ...nina, id: "u-born", username: "lab-entra-born-agent", origin: "entra", sponsor: "ben", entra_agent_id: "d3d3d3d3-0000-4000-8000-000000000003" };
    const probe: UserRow = { ...born, id: "u-probe", username: "echo-probe", sponsor: undefined, entra_agent_id: "7c3d9a52-4e81-4b6f-a0d2-3f5e8c1b9d47" };
    const loose: UserRow = { ...nina, id: "u-loose", username: "loose-agent", sponsor: undefined };
    vi.mocked(listUsers).mockResolvedValue(page([born, probe, loose]));
    mount();
    await screen.findByRole("button", { name: "Open echo-probe" });
    expect(cell("lab-entra-born-agent", "source").textContent).toBe("Entra");
    expect(cell("lab-entra-born-agent", "sponsor").textContent).toBe("ben");
    const none = cell("echo-probe", "sponsor").querySelector("[data-no-sponsor]") as HTMLElement;
    expect(none.textContent).toBe(NO_SPONSOR_CHIP);
    expect(none.getAttribute("data-tone")).toBe("warn");
    // An agent from the identity manager with no sponsor keeps an empty cell.
    expect(cell("loose-agent", "sponsor").textContent).toBe("");
  });

  it("reads the last check-in in days past a day, and says not yet for a user never seen", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    expect(cell("alice", "last_seen").textContent).toBe("5 d ago");
    expect(cell("carol", "last_seen").textContent).toBe("2 h ago");
    const never = within(cell("nina-research-agent", "last_seen")).getByText(NOT_YET);
    expect(never.getAttribute("title")).toBe(NEVER_SEEN);
  });

  it("draws one chip: disabled for a disable, locked for a lock, and locked for a user who is both", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol, dave, erin, fern]));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const words = (user: string) => Array.from(cell(user, "status").querySelectorAll("[data-status-word]")).map((e) => e.getAttribute("data-status-word"));

    expect(words("alice")).toEqual(["active"]);
    expect(cell("alice", "status").querySelector("[data-user-status]")?.getAttribute("data-user-status")).toBe("active");
    // dave's only row is the one his disable wrote, so he is not drawn locked.
    expect(words("dave")).toEqual(["disabled"]);
    expect(cell("dave", "status").querySelector("[data-locked]")).toBeNull();
    // carol is locked and not disabled: the lock replaces the active dot.
    expect(words("carol")).toEqual(["locked"]);
    const locked = cell("carol", "status").querySelector("[data-locked]") as HTMLElement;
    expect(locked.getAttribute("data-locked")).toBe("locked");
    expect(locked.getAttribute("title")).toBe(lock.reason);
    // erin is both: the lock outranks the disable, and its hover text says so.
    expect(words("erin")).toEqual(["locked"]);
    expect((cell("erin", "status").querySelector("[data-locked]") as HTMLElement).getAttribute("title")).toBe(ALSO_DISABLED + " " + lock.reason);
    // fern reads active to the identity manager, and the disable in Straza
    // that still holds her draws her disabled, never active or locked.
    expect(words("fern")).toEqual(["disabled"]);
    expect(cell("fern", "status").querySelector("[data-locked]")).toBeNull();
  });

  it("opens the user's page from a row", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open carol" }));
    expect(navigate).toHaveBeenCalledWith("users", ["u-carol"]);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens the person's agents from the sponsor link, by click and by key, and not the row", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const link = screen.getByRole("button", { name: sponsorShow(3, "alice") });
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("users", ["u-alice", "agents"]);
    expect(navigate).toHaveBeenCalledTimes(1);

    link.focus();
    await userEvent.keyboard("{Enter}");
    expect(navigate).toHaveBeenLastCalledWith("users", ["u-alice", "agents"]);
    expect(navigate).not.toHaveBeenCalledWith("users", ["u-alice"]);
  });

  it("filters by the sponsor the hand-off box names, and clears it", async () => {
    put("users", { sponsor: "alice" });
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    expect(listUsers).toHaveBeenCalledWith("sponsor=alice&" + FIRST);
    expect((document.querySelector("[data-sponsor-chip]") as HTMLElement).textContent).toContain(sponsorFilterWords("alice"));

    await userEvent.click(screen.getByRole("button", { name: CLEAR_SPONSOR }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith(FIRST));
    expect(document.querySelector("[data-sponsor-chip]")).toBeNull();
  });

  it("sends the search to the server after the typing settles", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    const box = screen.getByRole("textbox", { name: SEARCH_LABEL });
    expect(box.getAttribute("placeholder")).toBe(SEARCH_PLACEHOLDER);
    expect(box.parentElement?.querySelector("svg")).toBeTruthy();
    vi.mocked(listUsers).mockResolvedValue(page([]));
    await userEvent.type(box, "zzz");
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("q=zzz&" + FIRST));
    // One read for the settled word, not one per keystroke.
    expect(vi.mocked(listUsers).mock.calls.filter((c) => String(c[0]).startsWith("q=")).length).toBe(1);
    expect((await screen.findByText(EMPTY_FILTERED)).textContent).toBe(EMPTY_FILTERED);
  });

  it("says the directory is empty when no filter is set", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([]));
    mount();
    expect((await screen.findByText(EMPTY_DIRECTORY)).textContent).toBe(EMPTY_DIRECTORY);
  });

  it("sends the Status and the Role pickers to the server", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    await userEvent.click(screen.getByRole("combobox", { name: "Status" }));
    expect((await screen.findAllByRole("option")).map((o) => o.textContent)).toEqual(["any", "active", "disabled"]);
    await userEvent.click(screen.getByRole("option", { name: "disabled" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=disabled&" + FIRST));

    await userEvent.click(screen.getByRole("combobox", { name: "Role" }));
    expect((await screen.findAllByRole("option")).map((o) => o.textContent)).toEqual([ANY_ROLE, "dev", "sre"]);
    await userEvent.click(screen.getByRole("option", { name: "sre" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=disabled&role=sre&" + FIRST));
  });

  it("offers any role alone when the role catalog cannot be read", async () => {
    vi.mocked(listRoles).mockRejectedValue(Object.assign(new Error("forbidden"), { status: 403 }));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    await userEvent.click(screen.getByRole("combobox", { name: "Role" }));
    expect((await screen.findAllByRole("option")).map((o) => o.textContent)).toEqual([ANY_ROLE]);
  });

  it("loads the next page from the cursor and keeps the rows it has", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol], "c1"));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockResolvedValue(page([dave]));
    await userEvent.click(screen.getByRole("button", { name: LOAD_MORE }));
    await waitFor(() => expect(rows()).toEqual(["alice", "carol", "dave"]));
    expect(listUsers).toHaveBeenLastCalledWith(FIRST + "&cursor=c1");
    expect(screen.queryByRole("button", { name: LOAD_MORE })).toBeNull();
  });

  it("keeps the rows when a reload cannot reach strazad, and says so with the last read", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockRejectedValueOnce(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    await userEvent.click(screen.getByRole("button", { name: RELOAD_LIST }));
    const block = await screen.findByRole("status");
    expect(block.textContent).not.toContain("Users could not be read.");
    expect(block.textContent).toContain("The user list could not be read because Straza did not answer.");
    expect(block.textContent).toContain("Last good read:");
    expect(rows()).toEqual(["alice", "carol", "nina-research-agent", "ci-deployer"]);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says what failed when the first read fails, and offers the read again", async () => {
    vi.mocked(listUsers).mockRejectedValue(Object.assign(new Error("database is locked"), { status: 500 }));
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("The user list could not be read: database is locked. Reload to try again.");
    vi.mocked(listUsers).mockResolvedValue(page([alice]));
    await userEvent.click(screen.getByRole("button", { name: RELOAD_NOW }));
    await screen.findByRole("button", { name: "Open alice" });
  });

  it("renders a display name that carries markup as text", async () => {
    mount();
    await screen.findByRole("button", { name: "Open carol" });
    const name = cell("carol", "name");
    expect(name.textContent).toContain("Carol <b>Reyes</b>");
    expect(name.querySelector("b")).toBeNull();
  });
});
