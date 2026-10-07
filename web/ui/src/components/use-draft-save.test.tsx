import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type DraftSaveOptions, SaveNoteLine, useDraftSave, useWorkingDraft } from "./use-draft-save";
import type { DraftPublishProps } from "@/components/draft-publish";
import { ApiError, type Draft, type DraftPublished, type DraftVerdict } from "@/lib/api";
import type { SaveItem } from "@/lib/draft-save";
import { checkDraft, createDraft, getDraft, listDrafts, publishDraft, revertDraft, updateDraft } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { SUMMARY, VERDICT } from "@/test/drafts-fixture";

// The editors' two saves: a check first that
// stores nothing, then Save draft into the working draft, or Save and
// publish on a draft of its own that publishes at once when nothing is left
// to read, else in the one dialog, with Undo on the toast.

vi.mock("@/lib/drafts-api", async (orig) => ({
  ...(await orig<typeof import("@/lib/drafts-api")>()),
  checkDraft: vi.fn(), createDraft: vi.fn(), updateDraft: vi.fn(), publishDraft: vi.fn(), revertDraft: vi.fn(), listDrafts: vi.fn(), getDraft: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
// The dialog is C1's and has its own suite; here it stands in with the
// props it was handed and the answers a person can give it, Refused being
// a publish the server refused with the error in stub.refusal.
type Stub = DraftPublishProps & { title?: string; warnings?: boolean; quiet?: boolean };
const stub = vi.hoisted(() => ({ refusal: null as unknown }));
vi.mock("@/components/draft-publish", () => ({
  DraftPublish: (p: Stub) => p.open ? (
    <div role="dialog" data-stub-publish={p.draft.id} data-title={p.title} data-warnings={String(!!p.warnings)} data-quiet={String(!!p.quiet)} data-risks={p.verdict.risks.length}>
      <button type="button" onClick={() => p.onPublished({ draft: { ...p.draft, state: "published" }, snapshot: "3be0a1", servers: [], next: [] })}>Publish</button>
      <button type="button" onClick={() => p.onRefused?.(stub.refusal as ApiError)}>Refused</button>
      <button type="button" onClick={() => p.onOpenChange(false)}>Cancel</button>
    </div>
  ) : null,
}));

const ITEMS: SaveItem[] = [{ kind: "App", name: "demo-tools", op: "put", doc: "rps: 15" }];
const EMPTY: DraftVerdict = { ...VERDICT, draft: "50", revision: 1, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const draft = (id: string, revision = 1): Draft => ({ id, revision, state: "open", door: "console", authors: [], items: ITEMS.map((i) => ({ ...i, existed: true })), title: "Change server demo-tools", created_at: "", updated_at: "" });
const PUBLISHED = (id: string): DraftPublished => ({ draft: { ...draft(id), state: "published" }, snapshot: "3be0a1", servers: [], next: [] });
const REFUSAL = { code: "app.remove-missing", class: "refused" as const, object: "App/demo-tools", key: "k-r", sentence: "unknown server", fix: "Nothing is removed." };
const err = (status: number, body: unknown) => new ApiError((body as { error: string }).error, status, false, 0, body);

function Harness({ items, onPublished, creates }: { items: SaveItem[]; onPublished: (a: DraftPublished) => void; creates?: DraftSaveOptions["creates"] }) {
  const save = useDraftSave({ name: "demo-tools", creates, onPublished });
  return (
    <>
      <button type="button" onClick={() => void save.saveDraft(items)}>Save draft</button>
      <button type="button" onClick={() => void save.saveAndPublish(items)}>Save and publish</button>
      {save.note && <SaveNoteLine note={save.note} />}
      {save.dialog}
    </>
  );
}

function mount(items = ITEMS, creates?: DraftSaveOptions["creates"]) {
  const onPublished = vi.fn();
  const view = render(<Harness items={items} onPublished={onPublished} creates={creates} />);
  return { onPublished, unmount: view.unmount, rerender: (next: SaveItem[]) => view.rerender(<Harness items={next} onPublished={onPublished} creates={creates} />) };
}
const click = (name: string) => userEvent.click(screen.getByRole("button", { name }));
const note = () => document.querySelector("[data-save-note]") as HTMLElement | null;

describe("Save draft", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
  });

  it("checks, adds the items to the working draft, and opens it", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("39", 3), verdict: EMPTY });
    mount();
    await click("Save draft");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(checkDraft).toHaveBeenCalledWith({ items: ITEMS });
    expect(createDraft).toHaveBeenCalledWith({ items: ITEMS, working: true });
    expect(notify.ok).toHaveBeenCalledWith("Saved to your draft 39. Nothing changes until you publish it under Drafts.");
  });

  it("revises the working draft again on a second save, never through the update route", async () => {
    vi.mocked(createDraft).mockResolvedValueOnce({ draft: draft("39", 3), verdict: EMPTY }).mockResolvedValueOnce({ draft: draft("39", 4), verdict: EMPTY });
    mount();
    await click("Save draft");
    await click("Save draft");
    await waitFor(() => expect(createDraft).toHaveBeenCalledTimes(2));
    for (const call of vi.mocked(createDraft).mock.calls) expect(call[0]).toEqual({ items: ITEMS, working: true });
    expect(vi.mocked(navigate).mock.calls).toEqual([["drafts", ["39"]], ["drafts", ["39"]]]);
    expect(updateDraft).not.toHaveBeenCalled();
  });

  it("keeps a refused change in the editor with the server's sentence and fix, and stores nothing", async () => {
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: { ...EMPTY, refused: [REFUSAL] } });
    mount();
    await click("Save draft");
    await waitFor(() => expect(note()).not.toBeNull());
    expect(note()!.textContent).toBe("Nothing was saved.unknown server Nothing is removed.");
    expect(note()!.getAttribute("data-save-note")).toBe("refused");
    expect(createDraft).not.toHaveBeenCalled();
    expect(navigate).not.toHaveBeenCalled();
  });

  // A create door's put of a name that is live changes that object, so the
  // check's existed answer refuses it in the door's own words, on both saves.
  const CREATES = { object: "App/demo-tools", taken: "A server named demo-tools already exists." };
  it.each(["Save draft", "Save and publish"])("refuses a create door's %s when the check answers its object as existing, and stores nothing", async (button) => {
    vi.mocked(checkDraft).mockResolvedValue({ items: ITEMS.map((i) => ({ ...i, existed: true })), verdict: EMPTY });
    mount(ITEMS, CREATES);
    await click(button);
    await waitFor(() => expect(note()).not.toBeNull());
    expect(note()!.getAttribute("data-save-note")).toBe("refused");
    expect(note()!.textContent).toBe("Nothing was saved.A server named demo-tools already exists.");
    expect(createDraft).not.toHaveBeenCalled();
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("saves a create door's object the check answers as new, and an edit door's live object", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("39", 3), verdict: EMPTY });
    vi.mocked(checkDraft).mockResolvedValueOnce({ items: ITEMS.map((i) => ({ ...i, existed: false })), verdict: EMPTY });
    const view = mount(ITEMS, CREATES);
    await click("Save draft");
    await waitFor(() => expect(createDraft).toHaveBeenCalledTimes(1));
    view.unmount();
    vi.mocked(checkDraft).mockResolvedValueOnce({ items: ITEMS.map((i) => ({ ...i, existed: true })), verdict: EMPTY });
    mount();
    await click("Save draft");
    await waitFor(() => expect(createDraft).toHaveBeenCalledTimes(2));
    expect(note()).toBeNull();
  });

  it("reads a 422 by its findings, and an unanswered check as nothing stored", async () => {
    vi.mocked(checkDraft).mockRejectedValueOnce(err(422, { error: "x", findings: [REFUSAL] }));
    mount();
    await click("Save draft");
    await waitFor(() => expect(note()!.textContent).toContain("unknown server Nothing is removed."));
    vi.mocked(checkDraft).mockRejectedValueOnce(new ApiError("unreachable", 0, true));
    await click("Save draft");
    await waitFor(() => expect(note()!.getAttribute("data-save-note")).toBe("unreachable"));
    expect(note()!.textContent).toBe("The check could not finish because the server did not respond. Nothing was stored. Check your connection, then try again.");
  });
});

describe("Save and publish", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
    vi.mocked(listDrafts).mockResolvedValue({ items: [], next_cursor: "" });
  });

  it("publishes a change with nothing to read at once, on a draft of its own, and offers Undo", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("50", 1), verdict: EMPTY });
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED("50"));
    vi.mocked(revertDraft).mockResolvedValue({ draft: draft("51"), verdict: EMPTY });
    const { onPublished } = mount();
    await click("Save and publish");
    await waitFor(() => expect(onPublished).toHaveBeenCalledWith(PUBLISHED("50")));
    expect(listDrafts).toHaveBeenCalledWith("mine=true&state=open&object=App%2Fdemo-tools&limit=20");
    expect(createDraft).toHaveBeenCalledWith({ items: ITEMS });
    expect(publishDraft).toHaveBeenCalledWith("50", { revision: 1, risk_digest: "", ticked: [], typed: {} });
    expect(screen.queryByRole("dialog")).toBeNull();
    const [text, label, onUndo] = vi.mocked(notify.undo).mock.calls[0];
    expect([text, label]).toEqual(["demo-tools is live with your change.", "Undo"]);
    onUndo();
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["51"]));
    expect(revertDraft).toHaveBeenCalledWith("50", "");
    expect(notify.ok).toHaveBeenCalledWith("Draft 51 takes back draft 50. Review it, then publish it.");
  });

  it("opens the one dialog for a risk or a warning, and publishes nothing before it", async () => {
    for (const verdict of [{ ...EMPTY, risks: VERDICT.risks }, { ...EMPTY, warnings: VERDICT.warnings }]) {
      vi.clearAllMocks();
      vi.mocked(checkDraft).mockResolvedValue({ items: [], verdict: EMPTY });
      vi.mocked(createDraft).mockResolvedValue({ draft: draft("52"), verdict });
      const { onPublished } = mount();
      await click("Save and publish");
      const d = await screen.findByRole("dialog");
      expect(d.getAttribute("data-title")).toBe("Publish this change to demo-tools?");
      expect(d.getAttribute("data-warnings")).toBe("true");
      expect(d.getAttribute("data-quiet")).toBe("true");
      expect(publishDraft).not.toHaveBeenCalled();
      await click("Publish");
      await waitFor(() => expect(onPublished).toHaveBeenCalled());
      expect(notify.undo).toHaveBeenCalledWith("demo-tools is live with your change.", "Undo", expect.any(Function));
      document.body.innerHTML = "";
    }
  });

  it("keeps the draft on Cancel, reopens it for the same change and revises it for a changed one", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("53", 1), verdict: { ...EMPTY, warnings: VERDICT.warnings } });
    vi.mocked(updateDraft).mockResolvedValue({ draft: draft("53", 2), verdict: { ...EMPTY, warnings: VERDICT.warnings } });
    const { rerender } = mount();
    await click("Save and publish");
    await screen.findByRole("dialog");
    await click("Cancel");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(note()!.textContent).toBe("Draft 53 holds this change. It waits under Drafts until someone who may publish it does.Open draft 53");
    await click("Save and publish");
    await screen.findByRole("dialog");
    expect(createDraft).toHaveBeenCalledTimes(1);
    await click("Cancel");
    const changed: SaveItem[] = [{ ...ITEMS[0], doc: "rps: 20" }];
    rerender(changed);
    await click("Save and publish");
    await screen.findByRole("dialog");
    expect(updateDraft).toHaveBeenCalledWith("53", { revision: 1, items: changed });
    expect(createDraft).toHaveBeenCalledTimes(1);
  });

  // SECOND is the 409 of admin.secondPerson, which carries its code.
  const SECOND = { error: "You changed this draft, and this deployment needs a second person to publish a change that widens access (admin.secondPerson). Ask another administrator with standing over every object in it to review and publish it.", code: "second_person" };

  it.each([
    { name: "a standing refusal", status: 403, body: { error: "You cannot publish draft 54: this draft also changes policy sets, which needs the scope policy:write." } },
    { name: "a second-person refusal", status: 409, body: SECOND },
  ])("leaves the draft waiting for someone who may publish it after $name", async ({ status, body }) => {
    const error = body.error;
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("54"), verdict: EMPTY });
    vi.mocked(publishDraft).mockRejectedValue(err(status, body));
    const { onPublished } = mount();
    await click("Save and publish");
    await waitFor(() => expect(note()).not.toBeNull());
    expect(note()!.getAttribute("data-save-note")).toBe("waits");
    expect(note()!.textContent).toBe(error + "Draft 54 holds the change and waits under Drafts for someone who may publish it.Open draft 54");
    expect(onPublished).not.toHaveBeenCalled();
    await click("Open draft 54");
    expect(navigate).toHaveBeenCalledWith("drafts", ["54"]);
  });

  it("closes the dialog on a second-person refusal and leaves the draft waiting, and keeps it open on any other", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("60"), verdict: { ...EMPTY, risks: VERDICT.risks } });
    mount();
    await click("Save and publish");
    await screen.findByRole("dialog");
    stub.refusal = err(409, { error: "Publish refused: other publishes kept changing live state while this one was checked, so nothing was published. Publish again." });
    await click("Refused");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(note()).toBeNull();
    stub.refusal = err(409, SECOND);
    await click("Refused");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(note()!.getAttribute("data-save-note")).toBe("waits");
    expect(note()!.textContent).toBe(SECOND.error + "Draft 60 holds the change and waits under Drafts for someone who may publish it.Open draft 60");
  });

  it.each([
    { status: 409, error: "Publish refused: other publishes kept changing live state while this one was checked, so nothing was published. Publish again." },
    { status: 503, error: "Straza could not publish draft 54, because the database turned the change away three times while other changes were written. Nothing was published. Publish again in a moment." },
    { status: 500, error: "Straza could not publish draft 54, so nothing was published. Try again, and read the strazad log if it keeps failing." },
  ])("keeps the draft after a $status that carries no verdict, with the server's next step alone, never saying it waits for someone else", async ({ status, error }) => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("54"), verdict: EMPTY });
    vi.mocked(publishDraft).mockRejectedValue(err(status, { error }));
    mount();
    await click("Save and publish");
    await waitFor(() => expect(note()).not.toBeNull());
    expect(note()!.getAttribute("data-save-note")).toBe("refused");
    expect(note()!.textContent).toBe(error + "Draft 54 holds this change.Open draft 54");
    expect(note()!.textContent).not.toContain("someone who may publish it");
  });

  it("shows a refusal a 409 carries as the server's sentence with the draft's door, and checks and revises the draft on the next press", async () => {
    const stale = "Draft 58 cannot be published: demo-tools changed after this draft was checked. Check again.";
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("58", 1), verdict: EMPTY });
    vi.mocked(publishDraft).mockRejectedValue(err(409, { error: stale, verdict: { ...EMPTY, refused: [{ ...REFUSAL, code: "draft.stale" }] } }));
    vi.mocked(updateDraft).mockResolvedValue({ draft: draft("58", 2), verdict: { ...EMPTY, refused: [REFUSAL] } });
    mount();
    await click("Save and publish");
    await waitFor(() => expect(note()).not.toBeNull());
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(note()!.getAttribute("data-save-note")).toBe("refused");
    expect(note()!.textContent).toBe("Nothing was published." + stale + "Open draft 58");
    await click("Save and publish");
    await waitFor(() => expect(updateDraft).toHaveBeenCalledWith("58", { revision: 1, items: ITEMS }));
    expect(checkDraft).toHaveBeenCalledTimes(2);
    expect(createDraft).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(note()!.textContent).toBe("Nothing was published.unknown server Nothing is removed.Open draft 58");
  });

  // held53 is the person's earlier console draft 53 of demo-tools, whose
  // item is the document doc as a draft read answers it.
  const held53 = (doc: string) => {
    vi.mocked(listDrafts).mockResolvedValue({ items: [{ ...SUMMARY, id: "53", door: "console", revision: 2, items: [{ kind: "App", name: "demo-tools", op: "put" }] }], next_cursor: "" });
    vi.mocked(getDraft).mockResolvedValue({ draft: { ...draft("53", 2), items: [{ kind: "App", name: "demo-tools", op: "put", doc, existed: true }] }, verdict: EMPTY, revisions: [], live: {}, may_publish: true });
  };
  // The check answers the item as a draft read would, so the two compare.
  const CHECKED = [{ ...ITEMS[0], existed: true }];
  const STILL = "Draft 53 still holds an earlier change to demo-tools that this change leaves out. Publish or discard draft 53 under Drafts.";

  it("revises the person's open console draft that holds this same change, after the editor closed", async () => {
    held53("rps: 15");
    vi.mocked(checkDraft).mockResolvedValue({ items: CHECKED, verdict: EMPTY });
    vi.mocked(updateDraft).mockResolvedValue({ draft: draft("53", 3), verdict: { ...EMPTY, warnings: VERDICT.warnings } });
    mount();
    await click("Save and publish");
    await screen.findByRole("dialog");
    expect(listDrafts).toHaveBeenCalledWith("mine=true&state=open&object=App%2Fdemo-tools&limit=20");
    expect(getDraft).toHaveBeenCalledWith("53");
    expect(updateDraft).toHaveBeenCalledWith("53", { revision: 2, items: ITEMS });
    expect(createDraft).not.toHaveBeenCalled();
  });

  it("makes a draft of its own when the earlier draft holds another change, and says the earlier one still holds it", async () => {
    held53("rps: 20");
    vi.mocked(checkDraft).mockResolvedValue({ items: CHECKED, verdict: EMPTY });
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("59"), verdict: { ...EMPTY, warnings: VERDICT.warnings } });
    mount();
    await click("Save and publish");
    await screen.findByRole("dialog");
    expect(createDraft).toHaveBeenCalledWith({ items: ITEMS });
    expect(updateDraft).not.toHaveBeenCalled();
    await click("Cancel");
    await waitFor(() => expect(note()).not.toBeNull());
    expect(note()!.textContent).toBe("Draft 59 holds this change. It waits under Drafts until someone who may publish it does." + STILL + "Open draft 59");
  });

  it("says the earlier draft still holds its change after a publish that went at once", async () => {
    held53("rps: 20");
    vi.mocked(checkDraft).mockResolvedValue({ items: CHECKED, verdict: EMPTY });
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("59"), verdict: EMPTY });
    vi.mocked(publishDraft).mockResolvedValue(PUBLISHED("59"));
    const { onPublished } = mount();
    await click("Save and publish");
    await waitFor(() => expect(onPublished).toHaveBeenCalledWith(PUBLISHED("59")));
    expect(updateDraft).not.toHaveBeenCalled();
    expect(notify.warn).toHaveBeenCalledWith(STILL);
  });

  it("makes a draft of its own when the earlier draft cannot be read, and names it", async () => {
    held53("rps: 15");
    vi.mocked(getDraft).mockRejectedValue(new ApiError("unreachable", 0, true));
    vi.mocked(checkDraft).mockResolvedValue({ items: CHECKED, verdict: EMPTY });
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("59"), verdict: { ...EMPTY, refused: [REFUSAL] } });
    mount();
    await click("Save and publish");
    await waitFor(() => expect(note()).not.toBeNull());
    expect(updateDraft).not.toHaveBeenCalled();
    expect(note()!.textContent).toBe("Nothing was published.unknown server Nothing is removed." + STILL + "Open draft 59");
  });

  it.each([
    { name: "the working draft", row: { working: true } },
    { name: "a saved policy edit", row: { policy_edit: "dev-guardrails" } },
    { name: "a draft of another door", row: { door: "straza-app" as const } },
    { name: "a draft that holds other objects too", row: { items: [{ kind: "App" as const, name: "demo-tools", op: "put" as const }, { kind: "Role" as const, name: "r", op: "put" as const }] } },
  ])("leaves $name alone and makes a draft of its own", async ({ row }) => {
    vi.mocked(listDrafts).mockResolvedValue({ items: [{ ...SUMMARY, id: "53", door: "console", items: [{ kind: "App", name: "demo-tools", op: "put" }], ...row }], next_cursor: "" });
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("59"), verdict: { ...EMPTY, warnings: VERDICT.warnings } });
    mount();
    await click("Save and publish");
    await screen.findByRole("dialog");
    expect(createDraft).toHaveBeenCalledWith({ items: ITEMS });
    expect(updateDraft).not.toHaveBeenCalled();
  });

  it("makes a draft of its own when the list of the person's drafts fails", async () => {
    vi.mocked(listDrafts).mockRejectedValue(new ApiError("forbidden", 403));
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("59"), verdict: { ...EMPTY, warnings: VERDICT.warnings } });
    mount();
    await click("Save and publish");
    await screen.findByRole("dialog");
    expect(createDraft).toHaveBeenCalledTimes(1);
  });

  it("opens the dialog on the verdict a 409 carries when a line appeared since the check", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("55"), verdict: EMPTY });
    vi.mocked(publishDraft).mockRejectedValue(err(409, { error: "Publish refused: a new line. Acknowledge it and publish again.", verdict: { ...EMPTY, risks: VERDICT.risks } }));
    mount();
    await click("Save and publish");
    const d = await screen.findByRole("dialog");
    expect(d.getAttribute("data-risks")).toBe("2");
    expect(note()).toBeNull();
  });

  it("refuses at the check with nothing stored, and a change refused on its draft keeps it for the next press", async () => {
    vi.mocked(checkDraft).mockResolvedValueOnce({ items: [], verdict: { ...EMPTY, refused: [REFUSAL] } });
    mount();
    await click("Save and publish");
    await waitFor(() => expect(note()!.getAttribute("data-save-note")).toBe("refused"));
    expect(createDraft).not.toHaveBeenCalled();
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("56"), verdict: { ...EMPTY, refused: [REFUSAL] } });
    await click("Save and publish");
    await waitFor(() => expect(createDraft).toHaveBeenCalledTimes(1));
    expect(note()!.textContent).toBe("Nothing was published.unknown server Nothing is removed.Open draft 56");
    expect(publishDraft).not.toHaveBeenCalled();
    vi.mocked(updateDraft).mockResolvedValue({ draft: draft("56", 2), verdict: { ...EMPTY, refused: [REFUSAL] } });
    await click("Save and publish");
    await waitFor(() => expect(updateDraft).toHaveBeenCalledWith("56", { revision: 1, items: ITEMS }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("says an unanswered publish may have landed", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: draft("57"), verdict: EMPTY });
    vi.mocked(publishDraft).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    await click("Save and publish");
    await waitFor(() => expect(note()!.getAttribute("data-save-note")).toBe("unreachable"));
    expect(note()!.textContent).toBe("The server did not respond. The change may have been saved. Reload to check before trying again.");
  });
});

describe("useWorkingDraft", () => {
  function Probe({ object }: { object: string }) {
    const held = useWorkingDraft(object);
    return <span data-held>{held || "none"}</span>;
  }
  const held = () => document.querySelector("[data-held]")!.textContent;

  it("names the working draft that holds the object, and nothing for another object or no draft", async () => {
    vi.mocked(listDrafts).mockResolvedValue({ items: [{ ...SUMMARY, id: "39", working: true, items: [{ kind: "App", name: "demo-tools", op: "put" }] }], next_cursor: "" });
    render(<Probe object="App/demo-tools" />);
    await waitFor(() => expect(held()).toBe("39"));
    expect(listDrafts).toHaveBeenCalledWith("working=true&limit=1");
    document.body.innerHTML = "";
    render(<Probe object="App/github" />);
    await waitFor(() => expect(listDrafts).toHaveBeenCalledTimes(2));
    expect(held()).toBe("none");
    document.body.innerHTML = "";
    vi.mocked(listDrafts).mockRejectedValue(new ApiError("forbidden", 403));
    render(<Probe object="App/demo-tools" />);
    await waitFor(() => expect(listDrafts).toHaveBeenCalledTimes(3));
    expect(held()).toBe("none");
  });
});
