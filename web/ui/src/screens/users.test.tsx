import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Users } from "./users";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type Page, type RoleRow, type UserRow, getUser, listAssignments, listDevices, listRoles, listUsers } from "@/lib/api";
import { EMPTY_FILTERED, countWords, sortedWords } from "@/lib/user-words";

// Three identities in the shapes the paged users lane answers: a person who
// sponsors agents, a locked person whose display name carries markup, and
// an agent that never checked in.
const alice: UserRow = {
  id: "u-alice", username: "alice", display: "Alice Marin", email: "alice@corp.example",
  status: "active", origin: "scim", kind: "human", external_id: "3f1a",
  created_at: "2026-07-01T10:00:00Z", updated_at: "2026-09-01T10:00:00Z",
  effective_roles: ["dev", "straza-admin", "sec-approvers"], locks: [],
  last_seen: "2026-09-10T10:51:00Z", sponsored_count: 3,
};
const carol: UserRow = {
  id: "u-carol", username: "carol", display: "Carol <b>Reyes</b>", email: "carol@corp.example",
  status: "active", origin: "scim", kind: "human",
  created_at: "2026-07-02T10:00:00Z", updated_at: "2026-09-09T10:00:00Z",
  effective_roles: ["dev"],
  locks: [{ origin: "admin", reason: "incident 42: token pasted in a public channel", created_at: "2026-09-11T14:02:00Z" }],
  last_seen: "2026-09-09T11:20:00Z", sponsored_count: 0,
};
const nina: UserRow = {
  id: "u-nina", username: "nina-research-agent", status: "active", origin: "scim", kind: "nhi",
  user_type: "agent", sponsor: "alice", agency_mode: "supervised",
  created_at: "2026-09-12T08:12:00Z", updated_at: "2026-09-12T08:12:00Z",
  effective_roles: [], locks: [], sponsored_count: 0,
};
const dave: UserRow = {
  id: "u-dave", username: "dave", display: "Dave Okafor", status: "disabled", origin: "scim", kind: "human",
  created_at: "2026-06-01T10:00:00Z", updated_at: "2026-08-02T10:00:00Z",
  effective_roles: [], locks: [], last_seen: "2026-08-02T09:14:00Z", sponsored_count: 0,
};

const roles: RoleRow[] = [
  { id: "r-dev", name: "dev", kind: "application" },
  { id: "r-sre", name: "sre", kind: "application" },
];

const page = (items: UserRow[], next = ""): Page<UserRow> => ({ items, next_cursor: next });
const FIRST = "sort=name&order=asc&limit=100";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listUsers: vi.fn(),
  listRoles: vi.fn(),
  getUser: vi.fn(),
  listAssignments: vi.fn(),
  listDevices: vi.fn(),
  keyPosture: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = () => render(<TooltipProvider><Users /></TooltipProvider>);

// rows names the rendered rows in table order, told apart from the other
// buttons by the data-user attribute the list puts on every row.
const rows = () => screen.getAllByRole("button", { name: /^Open / }).filter((b) => b.hasAttribute("data-user")).map((b) => b.getAttribute("data-user"));
const cell = (user: string, column: number) => within(screen.getByRole("button", { name: "Open " + user })).getAllByRole("cell")[column];
const hint = () => (document.querySelector("[data-sorted-hint]") as HTMLElement).textContent;

describe("the Users screen", () => {
  beforeEach(() => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol, nina]));
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(getUser).mockResolvedValue({ ...carol, counts: { sessions: 4, active_sessions: 0, devices: 1, approver_devices: 1, approvals: 2 } });
    vi.mocked(listAssignments).mockResolvedValue([]);
    vi.mocked(listDevices).mockResolvedValue([]);
  });

  it("lists what the server answered, in the server's order, and sorts on the server", async () => {
    mount();
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Users");
    await screen.findByRole("button", { name: "Open alice" });
    expect(listUsers).toHaveBeenCalledWith(FIRST);
    expect(rows()).toEqual(["alice", "carol", "nina-research-agent"]);
    expect(hint()).toBe(sortedWords("name", true));

    // A time column reads newest first on the first click, oldest first on
    // the second, and the server answers both.
    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("sort=last_seen&order=desc&limit=100"));
    expect(hint()).toBe(sortedWords("last_seen", false));
    await userEvent.click(screen.getByRole("button", { name: "Sort by last seen" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("sort=last_seen&order=asc&limit=100"));
    expect(hint()).toBe(sortedWords("last_seen", true));
  });

  it("counts the loaded rows and says the server holds more", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol, nina], "c1"));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    expect((document.querySelector("[data-row-count]") as HTMLElement).textContent).toBe(countWords(3, true));
  });

  it("sends the search to the server after the typing settles", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockResolvedValue(page([]));
    await userEvent.type(screen.getByRole("textbox", { name: "Search users" }), "zzz");
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("q=zzz&" + FIRST));
    // One read for the settled word, not one per keystroke.
    expect(vi.mocked(listUsers).mock.calls.filter((c) => String(c[0]).startsWith("q=")).length).toBe(1);
    expect((await screen.findByText(EMPTY_FILTERED)).textContent).toBe(EMPTY_FILTERED);
  });

  it("sends the Status and the Role pickers to the server", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    await userEvent.click(screen.getByRole("combobox", { name: "Status" }));
    await userEvent.click(await screen.findByRole("option", { name: "disabled" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=disabled&" + FIRST));

    await userEvent.click(screen.getByRole("combobox", { name: "Role" }));
    await userEvent.click(await screen.findByRole("option", { name: "sre" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("status=disabled&role=sre&" + FIRST));
  });

  it("loads the next page from the cursor and keeps the rows it has", async () => {
    vi.mocked(listUsers).mockResolvedValue(page([alice, carol], "c1"));
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockResolvedValue(page([dave]));
    await userEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() => expect(rows()).toEqual(["alice", "carol", "dave"]));
    expect(listUsers).toHaveBeenLastCalledWith(FIRST + "&cursor=c1");
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("reads sponsorship both ways and filters by the sponsor from the cell", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    // A person shows the count, an agent sponsors nobody so its cell is empty.
    expect(cell("alice", 2).textContent).toBe("3");
    expect(cell("carol", 2).textContent).toBe("0");
    expect(cell("nina-research-agent", 2).textContent).toBe("");

    await userEvent.click(screen.getByRole("button", { name: "Show the 3 agents alice sponsors" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith("sponsor=alice&" + FIRST));
    expect((document.querySelector("[data-sponsor-chip]") as HTMLElement).textContent).toContain("Sponsored by alice");
    // The sheet did not open: the cell's button is its own door.
    expect(screen.queryByRole("dialog")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Clear the sponsor filter" }));
    await waitFor(() => expect(listUsers).toHaveBeenLastCalledWith(FIRST));
  });

  it("opens the user's sheet from a row and marks the open row", async () => {
    mount();
    await screen.findByRole("button", { name: "Open carol" });
    await userEvent.click(screen.getByRole("button", { name: "Open carol" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByRole("heading", { level: 2 }).textContent).toContain("carol");
    // The open sheet takes the accessibility tree, so the marked row is
    // read off the list itself.
    expect((document.querySelector('[data-user="carol"]') as HTMLElement).getAttribute("data-open")).toBe("true");
    expect(getUser).toHaveBeenCalledWith("u-carol");
  });

  it("keeps the rows when a reload cannot reach strazad, and says so with the last read", async () => {
    mount();
    await screen.findByRole("button", { name: "Open alice" });
    vi.mocked(listUsers).mockRejectedValueOnce(Object.assign(new Error("unreachable"), { status: 0, unreachable: true }));
    await userEvent.click(screen.getByRole("button", { name: "Reload the list" }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("Users: unreachable, state unknown.");
    expect(block.textContent).toContain("The user list could not be read because strazad did not answer.");
    expect(block.textContent).toContain("last successful read");
    expect(rows()).toEqual(["alice", "carol", "nina-research-agent"]);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says what failed when the first read fails, and offers the read again", async () => {
    vi.mocked(listUsers).mockRejectedValue(Object.assign(new Error("database is locked"), { status: 500 }));
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("The user list could not be read: database is locked. Reload to try again.");
    vi.mocked(listUsers).mockResolvedValue(page([alice]));
    await userEvent.click(screen.getByRole("button", { name: "Reload now" }));
    await screen.findByRole("button", { name: "Open alice" });
  });

  it("renders a display name that carries markup as text", async () => {
    mount();
    await screen.findByRole("button", { name: "Open carol" });
    const name = cell("carol", 0);
    expect(name.textContent).toContain("Carol <b>Reyes</b>");
    expect(name.querySelector("b")).toBeNull();
  });
});
