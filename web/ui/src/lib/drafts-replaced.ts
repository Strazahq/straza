// Whether a later publish replaced a published draft: a later draft wrote
// one of its objects, and that object is no longer live as this draft left
// it. No field of the draft names the later draft, so the page reads the
// published drafts of each object and compares live state with the change
// record. No React here.
import type { DraftDetail, DraftSummary } from "./api";
import { publishedFor } from "./drafts-api";
import { objectOf } from "./drafts-model";

// REPLACED_MAX is the most items a published draft may hold for the page to
// read whether it was replaced, since that costs one list read per item.
export const REPLACED_MAX = 8;

// Reads are the published drafts of each item's object, in the draft's
// item order, null where that read failed.
export type Reads = (DraftSummary[] | null)[];

// readLater reads the published drafts of every item of a published draft,
// in parallel. A failed read answers null and says nothing more. A draft
// that is not published, or holds more than REPLACED_MAX items, reads
// nothing.
export async function readLater(d: DraftDetail): Promise<Reads> {
  if (d.draft.state !== "published" || d.draft.items.length > REPLACED_MAX) return [];
  return Promise.all(d.draft.items.map((it) => publishedFor(objectOf(it)).then((p) => p.items || [], () => null)));
}

// moved says whether live state no longer holds an item's object as the
// draft's change record left it: another op, or for a policy set another
// text. A role and a server count by their op alone, since their live
// documents are fresh exports that may differ in fields no draft writes.
function moved(d: DraftDetail, kind: string, name: string): boolean {
  const row = (d.changes || []).find((c) => !c.implied && c.kind === kind && c.name === name);
  const live = (d.live || {})[kind + "/" + name];
  if (!row || !live) return false;
  if (live.op !== row.after_op) return true;
  return kind === "PolicySet" && live.op !== "remove" && (live.doc || "") !== (row.after_doc || "");
}

// replacedBy answers the draft that replaced a published draft, or null when
// none did or the page cannot tell. A row is later when it was decided
// after this draft, since a lower id can publish later after Check again.
// The stamp counts whole seconds, so between two drafts decided in the same
// second the higher id is the later one. The successor is the first of the
// later rows, and the draft counts as replaced only when one of its objects
// moved since.
export function replacedBy(d: DraftDetail, reads: Reads): DraftSummary | null {
  const draft = d.draft;
  const at = Date.parse(draft.decided_at || "");
  if (draft.state !== "published" || Number.isNaN(at) || draft.items.length > REPLACED_MAX) return null;
  const when = (r: { id: string; decided_at?: string }) => Date.parse(r.decided_at || "");
  // before says whether a was decided before b.
  const before = (a: { id: string; decided_at?: string }, b: { id: string; decided_at?: string }) => when(a) < when(b) || (when(a) === when(b) && Number(a.id) < Number(b.id));
  const later = reads.flatMap((r) => r || []).filter((r) => r.id !== draft.id && before(draft, r));
  if (!later.length) return null;
  const successor = later.reduce((a, b) => (before(b, a) ? b : a));
  return draft.items.some((it) => moved(d, it.kind, it.name)) ? successor : null;
}
