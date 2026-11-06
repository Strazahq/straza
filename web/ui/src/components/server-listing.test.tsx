import * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ObjectsRead, ObjectsReadContext, useListingFact } from "./server-listing";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AppRow, type ObjectsAnswer, removeListingCredential, setListingCredential } from "@/lib/api";
import { notify } from "@/lib/notify";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), setListingCredential: vi.fn(), removeListingCredential: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const apiError = (status: number, message: string, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

// Server is one server's shape: its name, its nouns and its listing tool.
type Server = { name: string; type: string; plural: string; lister: string };
const GHAPP: Server = { name: "ghapp", type: "repo", plural: "repos", lister: "repo_list" };
const POSTGRES: Server = { name: "postgres", type: "database", plural: "databases", lister: "list_databases" };

const appOf = (s: Server, credential = "static"): AppRow => ({
  id: "app-" + s.name, name: s.name, runtime: "remote", status: "running", reached_by: [],
  manifest: { metadata: { name: s.name }, straza: { runtime: { kind: "remote" }, credential: { kind: credential } } },
});
const unpinned = (s: Server): ObjectsAnswer => ({ app: s.name, type: "", declared: { type: s.type, list: { tool: s.lister } }, objects: [], total: 0 });
const pinned = (s: Server, credential: string): ObjectsAnswer => ({
  app: s.name, type: s.type, plural: s.plural, pinned: { type: s.type, list: { tool: s.lister } },
  listing: { tool: s.lister, credential, outcome: "never", version: 0, count: 0 }, objects: [], total: 0,
});

const reload = vi.fn();

// Row renders the listing fact the way the Credential card renders a fact,
// under a provider that holds the field's open state as the page does.
function Inner({ app }: { app: AppRow }) {
  const fact = useListingFact(app);
  if (!fact) return <p data-no-row />;
  return <dl><dt>{fact.label}</dt><dd>{fact.body}</dd></dl>;
}
function Row({ app, answer }: { app: AppRow; answer: ObjectsAnswer }) {
  const [open, setOpen] = React.useState(false);
  const read: ObjectsRead = { answer, reload, open, setOpen };
  return <TooltipProvider><ObjectsReadContext.Provider value={read}><Inner app={app} /></ObjectsReadContext.Provider></TooltipProvider>;
}

const row = () => document.querySelector("[data-listing-row]") as HTMLElement;
const tip = async (name: string) => {
  const b = screen.getByRole("button", { name: "Help: " + name });
  await userEvent.hover(b);
  return waitFor(() => {
    const id = b.getAttribute("aria-describedby");
    expect(id).toBeTruthy();
    return document.getElementById(id as string)?.textContent || "";
  });
};
// inDocument says whether a value shows anywhere: as text or as a field's
// value.
const inDocument = (value: string) => (document.body.textContent || "").includes(value) || [...document.querySelectorAll("input")].some((i) => i.value === value);
const openField = async () => { await userEvent.click(within(row()).getByRole("button", { name: "Set one" })); return row(); };

describe("the listing credential row", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(setListingCredential).mockResolvedValue({ app: "ghapp", fingerprint: "4be1", setAt: "2026-11-05T10:00:00Z" });
    vi.mocked(removeListingCredential).mockResolvedValue({});
  });

  // Each case is a server's shape and what its row says before the pin and
  // behind its help icon, in that server's own words.
  it.each([
    ["ghapp", GHAPP, "Pin repos first. ghapp has nothing to list for Straza until then.", "A second token for ghapp's MCP endpoint, which ghapp accepts for repo_list only. Straza calls repo_list with it in the background to keep the list of repos."],
    ["postgres", POSTGRES, "Pin databases first. postgres has nothing to list for Straza until then.", "A second token for postgres's MCP endpoint, which postgres accepts for list_databases only. Straza calls list_databases with it in the background to keep the list of databases."],
  ])("explains why %s's row cannot be set before the pin", async (_name, s, line, help) => {
    render(<Row app={appOf(s)} answer={unpinned(s)} />);
    expect(screen.getByText("Listing credential").tagName).toBe("DT");
    expect(row().textContent).toBe(line);
    expect(within(row()).queryAllByRole("button").filter((b) => b.textContent)).toEqual([]);
    expect(await tip("Listing credential")).toBe(help);
    if (s === POSTGRES) expect(document.body.textContent).not.toMatch(/repo/);
  });

  // Each case is a server and objects answer that carry no row.
  it.each([
    ["a pinned server that needs no listing credential", appOf(GHAPP), pinned(GHAPP, "not-needed")],
    ["an unpinned server that takes no credential", appOf(GHAPP, "none"), unpinned(GHAPP)],
    ["a server whose tools declare no type", appOf(GHAPP), { app: "ghapp", type: "", declared: null, objects: [], total: 0 }],
  ])("shows no row for %s", (_case, app, answer) => {
    render(<Row app={app} answer={answer} />);
    expect(document.querySelector("[data-no-row]")).not.toBeNull();
  });

  it("opens the field with Generate, or paste, Store and Cancel, and closes it on Cancel", async () => {
    render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    expect(row().textContent).toBe("Not set. Set one");
    const field = await openField();
    expect(within(field).getAllByRole("button").map((b) => b.textContent)).toEqual(["Generate", "Store", "Cancel"]);
    expect(field.querySelector("[data-generated]")).toBeNull();
    expect(field.textContent).toContain("or paste");
    expect((within(field).getByLabelText("listing credential value") as HTMLInputElement).type).toBe("password");
    expect(field.textContent).toContain("Put the same value in ghapp's config, where it accepts the token for repo_list. Straza stores it sealed, never sends it on an agent's call, and never shows it again.");
    await userEvent.click(within(field).getByRole("button", { name: "Cancel" }));
    expect(row().textContent).toBe("Not set. Set one");
  });

  it("fills the field with a generated 43-character base64url value without storing it, then stores it on Store", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    const field = await openField();
    await userEvent.click(within(field).getByRole("button", { name: "Generate" }));
    const box = within(field).getByLabelText("listing credential value") as HTMLInputElement;
    const value = box.value;
    expect(value).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(box.type).toBe("text");
    expect(setListingCredential).not.toHaveBeenCalled();
    expect(field.querySelector("[data-generated]")?.textContent).toBe("Put this value in ghapp's config and restart ghapp, then press Store.");
    await userEvent.click(within(field).getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(value);
    expect(notify.ok).toHaveBeenCalledWith("Copied the listing credential.");
    expect(setListingCredential).not.toHaveBeenCalled();
    await userEvent.click(within(field).getByRole("button", { name: "Store" }));
    await waitFor(() => expect(setListingCredential).toHaveBeenCalledWith("app-ghapp", value));
    await waitFor(() => expect(document.querySelector("[data-listing-field]")).toBeNull());
    expect(notify.ok).toHaveBeenCalledWith("The listing credential for ghapp is stored. Straza is listing its repos now.");
    expect(reload).toHaveBeenCalledTimes(1);
    expect(inDocument(value)).toBe(false);
  });

  it("drops a generated value on Cancel without storing it", async () => {
    render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    const field = await openField();
    await userEvent.click(within(field).getByRole("button", { name: "Generate" }));
    const value = (within(field).getByLabelText("listing credential value") as HTMLInputElement).value;
    await userEvent.click(within(field).getByRole("button", { name: "Cancel" }));
    expect(inDocument(value)).toBe(false);
    expect(setListingCredential).not.toHaveBeenCalled();
  });

  it("selects the value for a copy by hand when the browser refuses the clipboard", async () => {
    Object.defineProperty(navigator, "clipboard", { value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) }, configurable: true });
    render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    const field = await openField();
    await userEvent.click(within(field).getByRole("button", { name: "Generate" }));
    const box = within(field).getByLabelText("listing credential value") as HTMLInputElement;
    await userEvent.click(within(field).getByRole("button", { name: "Copy" }));
    await waitFor(() => expect(field.textContent).toContain("The browser refused the clipboard, so the value is selected. Copy it by hand."));
    expect(box.selectionStart).toBe(0);
    expect(box.selectionEnd).toBe(box.value.length);
  });

  it.each([
    ["ghapp", GHAPP, "The listing credential for ghapp is stored. Straza is listing its repos now."],
    ["postgres", POSTGRES, "The listing credential for postgres is stored. Straza is listing its databases now."],
  ])("stores a pasted value for %s, toasts the server and its plural, and clears the field", async (_name, s, toast) => {
    render(<Row app={appOf(s)} answer={pinned(s, "missing")} />);
    const field = await openField();
    await userEvent.type(within(field).getByLabelText("listing credential value"), "pasted-secret-value");
    await userEvent.click(within(field).getByRole("button", { name: "Store" }));
    await waitFor(() => expect(setListingCredential).toHaveBeenCalledWith("app-" + s.name, "pasted-secret-value"));
    await waitFor(() => expect(document.querySelector("[data-listing-field]")).toBeNull());
    expect(notify.ok).toHaveBeenCalledWith(toast);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(inDocument("pasted-secret-value")).toBe(false);
  });

  it("says what is missing on Store with an empty field", async () => {
    render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    const field = await openField();
    await userEvent.click(within(field).getByRole("button", { name: "Store" }));
    expect(within(field).getByRole("alert").textContent).toBe("Paste the value first, or press Generate to make one.");
    expect(setListingCredential).not.toHaveBeenCalled();
  });

  it("shows the server's refusal under the field and keeps it open", async () => {
    vi.mocked(setListingCredential).mockRejectedValue(apiError(409, "ghapp pins no object type, so a listing credential has no use."));
    render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    const field = await openField();
    await userEvent.type(within(field).getByLabelText("listing credential value"), "x");
    await userEvent.click(within(field).getByRole("button", { name: "Store" }));
    await waitFor(() => expect(within(row()).getByRole("alert").textContent).toBe("The server refused it: ghapp pins no object type, so a listing credential has no use. Fix what it names, then try again."));
    expect(document.querySelector("[data-listing-field]")).not.toBeNull();
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("shows a stored credential with Set it again and Remove, and removes it after a confirm", async () => {
    render(<Row app={appOf(POSTGRES)} answer={pinned(POSTGRES, "set")} />);
    expect(row().textContent).toBe("Stored, sealed Set it againRemove");
    await userEvent.click(within(row()).getByRole("button", { name: "Remove" }));
    const d = await screen.findByRole("alertdialog");
    expect(within(d).getByRole("heading").textContent).toBe("Remove the listing credential of postgres?");
    expect(d.textContent).toContain("Straza can no longer list postgres's databases until you set one again. A failed listing changes nothing, so the list of databases stays as the last good listing left it.");
    expect(removeListingCredential).not.toHaveBeenCalled();
    await userEvent.click(within(d).getByRole("button", { name: "Remove credential" }));
    await waitFor(() => expect(removeListingCredential).toHaveBeenCalledWith("app-postgres"));
    expect(notify.ok).toHaveBeenCalledWith("The listing credential for postgres was removed.");
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("names the fingerprint the store answered", async () => {
    const { rerender } = render(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "missing")} />);
    const field = await openField();
    await userEvent.type(within(field).getByLabelText("listing credential value"), "v");
    await userEvent.click(within(field).getByRole("button", { name: "Store" }));
    await waitFor(() => expect(document.querySelector("[data-listing-field]")).toBeNull());
    rerender(<Row app={appOf(GHAPP)} answer={pinned(GHAPP, "set")} />);
    expect(row().textContent).toBe("Stored, sealed 4be1… Set it againRemove");
  });
});
