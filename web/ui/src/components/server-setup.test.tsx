import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SetupCard } from "./server-setup";
import type { ObjectsRead } from "./server-listing";
import type { DraftPublishProps } from "./draft-publish";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AppRow, type DraftAnswer, type ObjectsAnswer, type ObjectsBlock, draftAppObjects } from "@/lib/api";
import { publishDraft } from "@/lib/drafts-api";
import { navigate } from "@/lib/router";
import { notify } from "@/lib/notify";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), draftAppObjects: vi.fn() }));
vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), publishDraft: vi.fn() }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
// The publish dialog has its own suite; here it stands in with what it
// was handed, so this suite pins when the Pin flow opens it.
vi.mock("@/components/draft-publish", () => ({
  DraftPublish: ({ draft, verdict, open, title, onPublished, onOpenChange }: DraftPublishProps) => (open ? (
    <div data-publish-stub={draft.id} data-digest={verdict.risk_digest}>
      {title}
      <button type="button" onClick={() => onPublished({} as never)}>Publish the stub</button>
      <button type="button" onClick={() => onOpenChange(false)}>Cancel the stub</button>
    </div>
  ) : null),
}));

const apiError = (status: number, message: string, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

// Server is one server's shape: its name and nouns, the argument its tools
// name the object in, the listing tool, and its tool count, of which every
// tool but the listing one is marked with the argument.
type Server = { name: string; type: string; plural: string; arg: string; lister: string; total: number };
const GHAPP: Server = { name: "ghapp", type: "repo", plural: "repos", arg: "repo", lister: "repo_list", total: 17 };
const POSTGRES: Server = { name: "postgres", type: "database", plural: "databases", arg: "database", lister: "list_databases", total: 15 };

const declaredOf = (s: Server, args?: string[]): ObjectsBlock => {
  const tools: Record<string, string | { argument: string }> = { [s.lister]: "all" };
  for (let i = 0; i < s.total - 1; i++) tools["tool_" + i] = { argument: args ? args[i % args.length] : s.arg };
  return { type: s.type, list: { tool: s.lister }, tools };
};
const appOf = (s: Server, credential = "static"): AppRow => ({
  id: "app-" + s.name, name: s.name, runtime: "remote", status: "running", reached_by: [],
  manifest: { metadata: { name: s.name }, straza: { runtime: { kind: "remote", remote: { url: "http://" + s.name + ":8080/mcp" } }, credential: { kind: credential } } },
});
const unpinned = (s: Server, declared: ObjectsBlock | null = declaredOf(s)): ObjectsAnswer => ({ app: s.name, type: "", declared, declaredFaults: [], objects: [], total: 0 });
const pinnedAnswer = (s: Server, credential: string): ObjectsAnswer => ({
  app: s.name, type: s.type, plural: s.plural, pinned: declaredOf(s), declared: declaredOf(s),
  listing: { tool: s.lister, credential, outcome: "never", version: 0, count: 0 }, objects: [], total: 0,
});
const verdictOf = (over: Record<string, unknown> = {}) => ({
  draft: "318", revision: 1, snapshot: "s", checked_at: "", refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], risk_digest: "digest-318", ...over,
});
const draftAnswer = (over: Record<string, unknown> = {}): DraftAnswer => ({
  draft: { id: "318", revision: 1, state: "open", door: "api", authors: [], items: [], title: "Change server ghapp" },
  verdict: verdictOf(over),
} as never);

const onPublished = vi.fn();
const setOpen = vi.fn();
const mount = (app: AppRow, answer: ObjectsAnswer | null, total = 17) => {
  const read: ObjectsRead = { answer, reload: vi.fn(), open: false, setOpen };
  return render(<TooltipProvider><SetupCard app={app} read={read} toolCount={total} onPublished={onPublished} /></TooltipProvider>);
};
const card = () => document.querySelector("[data-setup-card]") as HTMLElement | null;
const steps = () => [...(card() as HTMLElement).querySelectorAll("li")].map((l) => l.textContent);
const dialog = () => screen.getByRole("alertdialog");
const dd = (el: HTMLElement, label: string) => [...el.querySelectorAll("dt")].find((d) => d.textContent === label)?.nextElementSibling?.textContent || "";
const openPin = async (s: Server) => { await userEvent.click(screen.getByRole("button", { name: "Pin " + s.plural })); return dialog(); };
const tip = async (name: string) => {
  const b = screen.getByRole("button", { name: "Help: " + name });
  await userEvent.hover(b);
  return waitFor(() => {
    const id = b.getAttribute("aria-describedby");
    expect(id).toBeTruthy();
    return document.getElementById(id as string)?.textContent || "";
  });
};

describe("the setup card on a server's Overview", () => {
  beforeEach(() => { vi.clearAllMocks(); });

  // Each case is a server's shape and the card it shows before the pin, in
  // that server's own words.
  it.each([
    ["ghapp", GHAPP, "ghapp's tools work on repos. Pin them so roles can pick repos and Straza can list them.", "1Pin repos. You see what gets pinned before anything changes.Pin repos", "2Give Straza the listing credential, a second token that ghapp accepts for repo_list only. After step 1."],
    ["postgres", POSTGRES, "postgres's tools work on databases. Pin them so roles can pick databases and Straza can list them.", "1Pin databases. You see what gets pinned before anything changes.Pin databases", "2Give Straza the listing credential, a second token that postgres accepts for list_databases only. After step 1."],
  ])("shows both steps for %s while the tools declare a type and nothing is pinned", (_name, s, lead, one, two) => {
    mount(appOf(s), unpinned(s));
    expect((card() as HTMLElement).querySelector("p")?.textContent).toBe(lead);
    expect(steps()).toEqual([one, two]);
    expect(within(card() as HTMLElement).queryByRole("button", { name: "Set it" })).toBeNull();
    expect((card() as HTMLElement).querySelector('[data-step="2"]')?.hasAttribute("data-waits")).toBe(true);
    if (s === POSTGRES) expect((card() as HTMLElement).textContent).not.toMatch(/repo/);
  });

  it("shows step 1 alone for a server that takes no credential", () => {
    mount(appOf(GHAPP, "none"), unpinned(GHAPP));
    expect(steps()).toEqual(["1Pin repos. You see what gets pinned before anything changes.Pin repos"]);
  });

  it("marks step 1 done and opens the field from step 2 once the type is pinned", async () => {
    mount(appOf(GHAPP), pinnedAnswer(GHAPP, "missing"));
    expect(steps()).toEqual(["Repos are pinned.", "2Give Straza the listing credential, a second token that ghapp accepts for repo_list only.Set it"]);
    expect((card() as HTMLElement).querySelector('[data-step="1"]')?.hasAttribute("data-done")).toBe(true);
    await userEvent.click(screen.getByRole("button", { name: "Set it" }));
    expect(setOpen).toHaveBeenCalledWith(true);
  });

  // Each case is an objects answer that leaves the card off.
  it.each([
    ["the read failed or has not answered", null],
    ["the tools declare no type", unpinned(GHAPP, null)],
    ["the type is pinned and the credential is set", pinnedAnswer(GHAPP, "set")],
    ["the type is pinned and the server needs no credential", pinnedAnswer(GHAPP, "not-needed")],
  ])("shows no card when %s", (_case, answer) => {
    mount(appOf(GHAPP), answer);
    expect(card()).toBeNull();
  });
});

describe("the Pin dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(draftAppObjects).mockResolvedValue(draftAnswer());
    vi.mocked(publishDraft).mockResolvedValue({} as never);
  });

  it.each([
    ["ghapp", GHAPP, "Pin repos on ghapp?", "Straza will check every call against the repos the caller's role covers. Nobody's access changes when you pin: roles made before keep every repo.", "repo (repos)", "the repo argument, on 16 of 17 tools", "repo_list, which reaches every repo"],
    ["postgres", POSTGRES, "Pin databases on postgres?", "Straza will check every call against the databases the caller's role covers. Nobody's access changes when you pin: roles made before keep every database.", "database (databases)", "the database argument, on 14 of 15 tools", "list_databases, which reaches every database"],
  ])("says what pinning does and shows the declared facts in the words of %s", async (_name, s, title, body, type, from, list) => {
    mount(appOf(s), unpinned(s), s.total);
    const d = await openPin(s);
    expect(within(d).getByRole("heading").textContent).toBe(title);
    expect(d.textContent).toContain(body);
    expect(dd(d, "Object type")).toBe(type);
    expect(dd(d, "Read from")).toBe(from);
    expect(dd(d, "Listed by")).toBe(list);
    expect(d.querySelector("[data-pin-foot]")?.textContent).toBe("All of it comes from " + s.name + "'s own tool marks.");
    expect(await tip("tool marks")).toBe("If " + s.name + " later changes a mark, Straza keeps the pinned one and refuses that tool to roles on some " + s.plural + " until a person pins again.");
    expect(within(d).getAllByRole("button").map((b) => b.textContent).filter(Boolean)).toEqual(["Cancel", "Save draft", "Pin and publish"]);
    if (s === POSTGRES) expect(d.textContent).not.toMatch(/repo/);
  });

  it("says an argument when the marked tools read different ones", async () => {
    mount(appOf(GHAPP), unpinned(GHAPP, declaredOf(GHAPP, ["repo", "repository"])));
    expect(dd(await openPin(GHAPP), "Read from")).toBe("an argument, on 16 of 17 tools");
  });

  it("makes the draft on Save draft and opens it", async () => {
    mount(appOf(GHAPP), unpinned(GHAPP));
    await openPin(GHAPP);
    await userEvent.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["318"]));
    expect(draftAppObjects).toHaveBeenCalledWith("app-ghapp");
    expect(publishDraft).not.toHaveBeenCalled();
    expect(notify.ok).toHaveBeenCalledWith("Saved to your draft 318. Nothing changes until you publish it under Drafts.");
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("publishes at once on Pin and publish when nothing needs reading", async () => {
    mount(appOf(POSTGRES), unpinned(POSTGRES), 15);
    await openPin(POSTGRES);
    await userEvent.click(screen.getByRole("button", { name: "Pin and publish" }));
    await waitFor(() => expect(onPublished).toHaveBeenCalledTimes(1));
    expect(publishDraft).toHaveBeenCalledWith("318", { revision: 1, risk_digest: "digest-318", ticked: [], typed: {} });
    expect(notify.ok).toHaveBeenCalledWith("Databases are pinned on postgres.");
    expect(navigate).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("opens the publish dialog when a line needs confirming, and publishes from it", async () => {
    vi.mocked(draftAppObjects).mockResolvedValue(draftAnswer({ risks: [{ key: "k1", code: "role.covers-wider", class: "risk", sentence: "Widens." }], risk_digest: "digest-risk" }));
    mount(appOf(GHAPP), unpinned(GHAPP));
    await openPin(GHAPP);
    await userEvent.click(screen.getByRole("button", { name: "Pin and publish" }));
    const stub = await waitFor(() => { const el = document.querySelector("[data-publish-stub]") as HTMLElement; expect(el).not.toBeNull(); return el; });
    expect(stub.getAttribute("data-publish-stub")).toBe("318");
    expect(stub.getAttribute("data-digest")).toBe("digest-risk");
    expect(stub.textContent).toContain("Publish this change to ghapp?");
    expect(publishDraft).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Publish the stub" }));
    expect(onPublished).toHaveBeenCalledTimes(1);
    expect(notify.ok).toHaveBeenCalledWith("Repos are pinned on ghapp.");
  });

  it("says where the draft waits when the publish dialog is cancelled", async () => {
    vi.mocked(draftAppObjects).mockResolvedValue(draftAnswer({ warnings: [{ key: "w1", code: "objects.listing", class: "warning", sentence: "No listing credential yet." }] }));
    mount(appOf(GHAPP), unpinned(GHAPP));
    await openPin(GHAPP);
    await userEvent.click(screen.getByRole("button", { name: "Pin and publish" }));
    await userEvent.click(await screen.findByRole("button", { name: "Cancel the stub" }));
    expect(document.querySelector("[data-publish-stub]")).toBeNull();
    expect(notify.warn).toHaveBeenCalledWith("Draft 318 holds this change. It waits under Drafts until someone who may publish it does.");
    expect(onPublished).not.toHaveBeenCalled();
  });

  it("sends a draft with a refused line to its page without publishing", async () => {
    vi.mocked(draftAppObjects).mockResolvedValue(draftAnswer({ refused: [{ key: "r1", code: "x", class: "refused", sentence: "Refused." }] }));
    mount(appOf(GHAPP), unpinned(GHAPP));
    await openPin(GHAPP);
    await userEvent.click(screen.getByRole("button", { name: "Pin and publish" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["318"]));
    expect(publishDraft).not.toHaveBeenCalled();
  });

  // Each case is what the draft route answers without a draft, and the
  // sentence the dialog shows with Cancel alone.
  it.each([
    ["a 409 for a server that is not running", () => vi.mocked(draftAppObjects).mockRejectedValue(apiError(409, "ghapp is not running, so Straza cannot read its tool marks. Start it, then draft again.")), "ghapp is not running, so Straza cannot read its tool marks. Start it, then draft again."],
    ["a 200 for marks that already match", () => vi.mocked(draftAppObjects).mockResolvedValue({ unchanged: true, sentence: "ghapp's pinned repos already match its marks. Nothing to draft." }), "ghapp's pinned repos already match its marks. Nothing to draft."],
  ])("shows the server's sentence for %s", async (_case, answer, sentence) => {
    answer();
    mount(appOf(GHAPP), unpinned(GHAPP));
    const d = await openPin(GHAPP);
    await userEvent.click(within(d).getByRole("button", { name: "Pin and publish" }));
    await waitFor(() => expect(d.querySelector("[data-save-note]")?.textContent).toBe(sentence));
    expect(within(d).getAllByRole("button").map((b) => b.textContent)).toEqual(["Cancel"]);
    expect(publishDraft).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });
});
