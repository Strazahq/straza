// The pure half of the editors' two saves:
// the items an editor sends, whether a verdict leaves anything to read
// before publishing, and the refusal lines an editor shows beside its
// buttons. The wire has no field path, so a refusal names its object and
// its sentence says which field.
import type { ApiError, DraftFinding, DraftItem, DraftItemIn, DraftSummary, DraftVerdict } from "./api";
import { getDraft, listDrafts, refusalOf } from "./drafts-api";

// SaveItem is one object an editor writes, in the items form of a draft
// body.
export type SaveItem = DraftItemIn;

// itemsOf answers the items as the body sends them, in the editor's order.
export const itemsOf = (items: SaveItem[]): SaveItem[] => items.map((it) => ({ kind: it.kind, name: it.name, op: it.op, doc: it.doc || "" }));

// sameItems reports whether two saves send the same objects and documents,
// so a second Save and publish reopens the draft the first one made.
export const sameItems = (a: SaveItem[], b: SaveItem[]) => JSON.stringify(itemsOf(a)) === JSON.stringify(itemsOf(b));

// nothingToRead reports whether a verdict publishes at once from an
// editor: nothing refused, no line to acknowledge and no warning. Unchecked
// and info lines ask nothing of the publisher, so they do not hold it.
export const nothingToRead = (v: DraftVerdict) => !(v.refused || []).length && !(v.risks || []).length && !(v.warnings || []).length;

// findingLine is one refusal as an editor shows it: the server's sentence,
// then its fix.
export const findingLine = (f: DraftFinding) => f.sentence + (f.fix ? " " + f.fix : "");

// refusedLines are the refused lines of a verdict, one per finding.
export const refusedLines = (v: DraftVerdict) => (v.refused || []).map(findingLine);

// errorLines answers what a refused write says: the findings of a 422 when
// it carries them, else the server's own sentence.
export function errorLines(err: ApiError): string[] {
  const body = refusalOf(err);
  if (body && body.findings && body.findings.length) return body.findings.map(findingLine);
  return [body ? body.error : err.message];
}

// YourDraft is the person's open working draft as the header names it.
export type YourDraft = { id: string; changes: number };

// yourDraft reads the person's working draft, null when none is open. The
// shell loads this file on first use, so the read stays out of the entry.
export async function yourDraft(): Promise<YourDraft | null> {
  const d = ((await listDrafts("working=true&limit=1")).items || [])[0];
  return d ? { id: d.id, changes: d.items.length } : null;
}

// objectsKey names the objects of a save or a list row in one sorted
// string, so two drafts of the same objects compare equal whatever order
// their items came in.
const objectsKey = (xs: { kind: string; name: string }[]) => xs.map((x) => x.kind + "/" + x.name).sort().join(",");

// sameDocs reports whether two answered item lists are one change: the
// same objects, each with the same op and document. Both lists must be in
// the form the drafts routes answer, the check's items and a draft read's,
// so a masked or canonical document compares with its like and none is
// ever sent back. A withheld document never compares equal.
export function sameDocs(a: DraftItem[], b: DraftItem[]): boolean {
  const key = (xs: DraftItem[]) => xs.map((x) => JSON.stringify([x.kind, x.name, x.op, x.withheld ? null : x.doc || ""])).sort().join("\n");
  return a.length === b.length && key(a) === key(b);
}

// OwnDraft is the earlier draft ownDraft found, and whether it holds the
// change being saved.
export type OwnDraft = { id: string; revision: number; same: boolean };

// ownDraft finds the draft an editor of this person made earlier for the
// same objects and did not publish: open, of the console door, neither a
// working draft nor a saved policy edit, and holding these objects alone.
// same says whether it holds exactly the change checked, the check's
// answered items, so Save and publish revises it only then and never
// drops another editor's kept change from it. A list that fails finds
// none, and a draft read that fails reads as another change.
export async function ownDraft(items: SaveItem[], checked: DraftItem[]): Promise<OwnDraft | null> {
  if (!items.length) return null;
  let rows: DraftSummary[] = [];
  try {
    const page = await listDrafts("mine=true&state=open&object=" + encodeURIComponent(items[0].kind + "/" + items[0].name) + "&limit=20");
    rows = (page && page.items) || [];
  } catch {
    return null;
  }
  const want = objectsKey(items);
  const d = rows.find((r) => r.door === "console" && !r.working && !r.policy_edit && objectsKey(r.items) === want);
  if (!d) return null;
  try {
    const held = await getDraft(d.id);
    return { id: d.id, revision: held.draft.revision, same: sameDocs(checked, held.draft.items || []) };
  } catch {
    return { id: d.id, revision: d.revision, same: false };
  }
}
