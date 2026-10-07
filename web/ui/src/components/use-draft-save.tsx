import * as React from "react";
import { Button } from "@/components/ui/button";
import { DraftPublish } from "@/components/draft-publish";
import type { ApiError, Draft, DraftItem, DraftPublished, DraftVerdict } from "@/lib/api";
import { type SaveItem, errorLines, itemsOf, nothingToRead, ownDraft, refusedLines, sameItems } from "@/lib/draft-save";
import { checkDraft, createDraft, listDrafts, publishDraft, refusalOf, revertDraft, updateDraft } from "@/lib/drafts-api";
import { NOT_PUBLISHED, undoneToast } from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { NOTHING_SAVED, UNDO, heldLine, keptLine, liveWithChange, openDraft, publishThis, savedToDraft, stillHolds, waitsLine } from "@/lib/save-words";
import { WRITE_UNCONFIRMED, checkFailed, refused } from "@/lib/say";
import { cn } from "@/lib/utils";

// The editors' two saves, one flow for every
// editor. Both buttons check first, which stores nothing, so a refused
// change stays in the editor where it can be fixed. Save draft then adds
// the items to the person's working draft and opens it. Save and publish
// puts them in a draft of their own, so the working draft's other changes
// never ride along, and publishes it at once when the server finds nothing
// to read, else in the one dialog. Every document an editor sends comes
// from a live read, never from a draft's answer, which is masked.

// SaveNote is what the editor shows beside its buttons: a refusal, an
// unanswered call, a draft that waits for someone else, or a draft kept
// after Cancel. draft names the draft the note is about.
export type SaveNote = { tone: "refused" | "unreachable" | "waits" | "kept"; lines: string[]; draft?: string };

export type DraftSaveOptions = {
  // name is the object the change is about, as the toast and the dialog
  // title name it.
  name: string;
  // title replaces the dialog's title, as Remove server's does.
  title?: string;
  // toast replaces the toast after a publish.
  toast?: string;
  // creates names the object a create door makes, written Kind/Name, and
  // taken the sentence that says it exists. A draft put of a live object
  // changes it, so a check that answers the object as existing is refused
  // with that sentence and nothing is stored.
  creates?: { object: string; taken: string };
  onPublished: (answer: DraftPublished) => void;
};

// Made is the draft Save and publish made and has not published, with the
// items it holds, so a second press reuses it. other names the person's
// earlier draft of the same objects that holds another change, which this
// save left alone.
type Made = { items: SaveItem[]; draft: Draft; verdict: DraftVerdict; other?: string };

const TONE: Record<SaveNote["tone"], string> = { refused: "border-danger", unreachable: "border-unknown", waits: "border-warn", kept: "border-border" };

// waitsFor says a refused publish leaves the draft to someone else:
// a 403 names another person's standing, and the 409 coded second_person
// wants a person other than the draft's authors.
const waitsFor = (err: ApiError) => err.status === 403 || refusalOf(err)?.code === "second_person";

// SaveNoteLine is a note in the shape of the landed save problem: a 3 px
// left border in the note's hue, the lines, and a door to the draft it
// names, where Check again and Discard live.
export function SaveNoteLine({ note, className }: { note: SaveNote; className?: string }) {
  return (
    <div role={note.tone === "kept" ? "status" : "alert"} className={cn("m-0 flex flex-col items-start gap-1 border-l-[3px] bg-card px-3 py-2 text-sm leading-relaxed text-text-2", TONE[note.tone], className)} data-save-note={note.tone}>
      {note.lines.map((l) => <p key={l} className="m-0">{l}</p>)}
      {note.draft && (
        <Button variant="link" size="sm" className="h-auto p-0" onClick={() => navigate("drafts", [note.draft as string])}>{openDraft(note.draft)}</Button>
      )}
    </div>
  );
}

// useDraftSave answers the two saves for one editor, the note to show
// beside its buttons, and the dialog to render. A 401 is the session
// module's, so it leaves no note.
export function useDraftSave({ name, title, toast, creates, onPublished }: DraftSaveOptions) {
  const [busy, setBusy] = React.useState<"draft" | "publish" | null>(null);
  const [note, setNote] = React.useState<SaveNote | null>(null);
  const [made, setMade] = React.useState<Made | null>(null);
  const [open, setOpen] = React.useState(false);

  const fail = (e: unknown, unanswered: string, lead: string) => {
    const err = e as ApiError;
    if (err.status === 401) return;
    setNote(err.unreachable ? { tone: "unreachable", lines: [unanswered] } : { tone: "refused", lines: [lead, ...errorLines(err)] });
  };

  // check answers the items as the server answered them when it refuses
  // nothing in them, and leaves the refusal as the note and answers null
  // otherwise.
  const check = async (items: SaveItem[]): Promise<DraftItem[] | null> => {
    try {
      const answer = await checkDraft({ items: itemsOf(items) });
      const lines = refusedLines(answer.verdict);
      if (creates && (answer.items || []).some((it) => it.existed && it.kind + "/" + it.name === creates.object)) lines.push(creates.taken);
      if (lines.length) setNote({ tone: "refused", lines: [NOTHING_SAVED, ...lines] });
      return lines.length ? null : answer.items || [];
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setNote(err.unreachable ? { tone: "unreachable", lines: [checkFailed(err)] } : { tone: "refused", lines: [NOTHING_SAVED, ...errorLines(err)] });
      return null;
    }
  };

  // alsoHeld is the line about the earlier draft a save left alone, if any.
  const alsoHeld = (m: Made | null) => (m && m.other ? [stillHolds(m.other, name)] : []);

  // refusedNote is the note of a publish of m refused with no verdict. The
  // server's sentence carries the next step, so the line after it only
  // names the draft that holds the change.
  const refusedNote = (err: ApiError, m: Made): SaveNote => {
    const body = refusalOf(err);
    const sentence = body ? body.error : err.message;
    return waitsFor(err)
      ? { tone: "waits", lines: [sentence, waitsLine(m.draft.id), ...alsoHeld(m)], draft: m.draft.id }
      : { tone: "refused", lines: [sentence, heldLine(m.draft.id), ...alsoHeld(m)], draft: m.draft.id };
  };

  const undo = async (id: string) => {
    try {
      const answer = await revertDraft(id, "");
      notify.ok(undoneToast(answer.draft.id, id));
      navigate("drafts", [answer.draft.id]);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) notify.failed(refused(err));
    }
  };

  // done ends a publish. The editor closes on it, so the line about an
  // earlier draft this save left alone goes out as a warning beside Undo.
  const done = (answer: DraftPublished, other?: string) => {
    setMade(null);
    setOpen(false);
    setNote(null);
    notify.undo(toast || liveWithChange(name), UNDO, () => void undo(answer.draft.id));
    if (other) notify.warn(stillHolds(other, name));
    onPublished(answer);
  };

  const saveDraft = async (items: SaveItem[]) => {
    if (busy) return;
    setBusy("draft");
    setNote(null);
    try {
      if (!(await check(items))) return;
      const answer = await createDraft({ items: itemsOf(items), working: true });
      notify.ok(savedToDraft(answer.draft.id));
      navigate("drafts", [answer.draft.id]);
    } catch (e) {
      fail(e, WRITE_UNCONFIRMED, NOTHING_SAVED);
    } finally {
      setBusy(null);
    }
  };

  // publishNow publishes a draft whose verdict leaves nothing to read. A
  // 409 that carries a verdict with a refusal, such as draft.stale, shows
  // the server's sentence with the draft's door, and one with a line that
  // appeared since the check opens the dialog on that verdict. Every other
  // failure keeps the draft, which waits for someone else or which the
  // person may publish again.
  const publishNow = async (m: Made) => {
    try {
      done(await publishDraft(m.draft.id, { revision: m.draft.revision, risk_digest: m.verdict.risk_digest || "", ticked: [], typed: {} }), m.other);
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      if (err.unreachable) { setNote({ tone: "unreachable", lines: [WRITE_UNCONFIRMED] }); return; }
      const body = refusalOf(err);
      if (body && body.verdict) {
        setMade({ ...m, verdict: body.verdict });
        if ((body.verdict.refused || []).length) setNote({ tone: "refused", lines: [NOT_PUBLISHED, body.error, ...alsoHeld(m)], draft: m.draft.id });
        else setOpen(true);
        return;
      }
      setNote(refusedNote(err, m));
    }
  };

  const saveAndPublish = async (items: SaveItem[]) => {
    if (busy) return;
    // The draft made earlier reopens as it is only while its verdict refuses
    // nothing; a refused one is checked and revised like a changed save.
    if (made && sameItems(made.items, items) && !refusedLines(made.verdict).length) { setNote(null); setOpen(true); return; }
    setBusy("publish");
    setNote(null);
    try {
      const checked = await check(items);
      if (!checked) return;
      // An earlier draft of these objects that another editor left holds
      // a change this save does not carry, so it is left as it is and
      // named, and only a draft holding this very change is revised.
      const found = made ? null : await ownDraft(items, checked);
      const earlier = made ? { id: made.draft.id, revision: made.draft.revision } : found && found.same ? found : null;
      const other = made ? made.other : found && !found.same ? found.id : undefined;
      const answer = earlier
        ? await updateDraft(earlier.id, { revision: earlier.revision, items: itemsOf(items) })
        : await createDraft({ items: itemsOf(items) });
      const m: Made = { items, draft: answer.draft, verdict: answer.verdict, other };
      setMade(m);
      const lines = refusedLines(answer.verdict);
      if (lines.length) { setNote({ tone: "refused", lines: [NOT_PUBLISHED, ...lines, ...alsoHeld(m)], draft: answer.draft.id }); return; }
      if (nothingToRead(answer.verdict)) await publishNow(m);
      else setOpen(true);
    } catch (e) {
      // A draft that moved elsewhere cannot be revised, so the next press
      // starts a new one.
      setMade(null);
      fail(e, WRITE_UNCONFIRMED, NOT_PUBLISHED);
    } finally {
      setBusy(null);
    }
  };

  const onOpenChange = (next: boolean) => {
    setOpen(next);
    if (!next && made) setNote({ tone: "kept", lines: [keptLine(made.draft.id), ...alsoHeld(made)], draft: made.draft.id });
  };

  const dialog = made ? (
    <DraftPublish
      draft={made.draft}
      verdict={made.verdict}
      open={open}
      onOpenChange={onOpenChange}
      onPublished={(answer) => done(answer, made.other)}
      onVerdict={(verdict) => setMade((m) => (m ? { ...m, verdict } : m))}
      onRefused={(err) => { if (waitsFor(err)) { setOpen(false); setNote(refusedNote(err, made)); } }}
      title={title || publishThis(name)}
      warnings
      quiet
    />
  ) : null;

  // edited drops a note that names a draft once the editor changes again,
  // since that draft no longer holds the editor's change.
  const edited = () => setNote((n) => (n && n.draft ? null : n));

  return { busy, note, saveDraft, saveAndPublish, dialog, edited };
}

// useWorkingDraft names the person's working draft when it holds object,
// written Kind/Name, so an editor that opens on live can say that saving
// a draft replaces the change the working draft holds. A read that fails
// names nothing.
export function useWorkingDraft(object: string): string | null {
  const [held, setHeld] = React.useState<string | null>(null);
  React.useEffect(() => {
    let alive = true;
    listDrafts("working=true&limit=1").then(
      (page) => {
        const d = (page.items || [])[0];
        if (alive) setHeld(d && d.items.some((it) => it.kind + "/" + it.name === object) ? d.id : null);
      },
      () => { if (alive) setHeld(null); },
    );
    return () => { alive = false; };
  }, [object]);
  return held;
}
