// The calls of the Drafts area, over the drafts routes. Only the
// lazy drafts screens and the editors import this file, and the shell reads
// the waiting count through a dynamic import, so none of it lands in the
// entry page.
import {
  ApiError,
  type DraftAnswer,
  type DraftBody,
  type DraftCheckAnswer,
  type DraftConflict,
  type DraftConflicts,
  type DraftContacted,
  type DraftDetail,
  type DraftPublishBody,
  type DraftPublished,
  type DraftRebaseBody,
  type DraftRefused,
  type DraftSummary,
  type DraftUpdateBody,
  type Draft,
  type Page,
  request,
} from "./api";

const enc = encodeURIComponent;
const at = (id: string, verb = "") => "/v1/admin/drafts/" + enc(id) + (verb ? "/" + verb : "");

// listDrafts reads one page of the queue; q carries state, mine, door,
// object, source, working, policy_edit, limit and cursor as the caller
// builds them.
export const listDrafts = (q: string) => request<Page<DraftSummary>>("GET", "/v1/admin/drafts?" + q);
export const getDraft = (id: string) => request<DraftDetail>("GET", at(id));
export const createDraft = (body: DraftBody) => request<DraftAnswer>("POST", "/v1/admin/drafts", body);
// checkDraft runs the verdict over documents and stores nothing.
export const checkDraft = (body: DraftBody) => request<DraftCheckAnswer>("POST", "/v1/admin/drafts/check", body);
export const updateDraft = (id: string, body: DraftUpdateBody) => request<DraftAnswer>("PUT", at(id), body);
// rebaseDraft is Check again: live's fields under the draft's own changes,
// and a 409 with conflicts when both sides changed one field.
export const rebaseDraft = (id: string, body: DraftRebaseBody) => request<DraftAnswer>("POST", at(id, "rebase"), body);
// discardDraft sends the revision the person read when there is one, so a
// draft that moved since answers 409 instead of going away unread.
export const discardDraft = (id: string, reason: string, revision?: number) =>
  request<{ draft: Draft }>("POST", at(id, "discard"), revision === undefined ? { reason } : { revision, reason });
// revertDraft makes a new draft that takes a published one back.
export const revertDraft = (id: string, note: string) => request<DraftAnswer>("POST", at(id, "revert"), { note });
// CONTACT_TIMEOUT_MS outlasts the server's own 10 s bound on the dial, so
// a slow address shows the server's 502 sentence and not an unanswered call.
const CONTACT_TIMEOUT_MS = 15000;
export const contactDraftServer = (id: string, object: string) => request<DraftContacted>("POST", at(id, "contact"), { object }, { timeoutMs: CONTACT_TIMEOUT_MS });
export const publishDraft = (id: string, body: DraftPublishBody) => request<DraftPublished>("POST", at(id, "publish"), body);

// WAITING_LIMIT is the page the waiting count reads; past it the count
// reads as a floor.
const WAITING_LIMIT = 200;

// waitingLabel is the number the sidebar draws beside Drafts: the open
// drafts this reader may read, "" for none, and "200+" past one page.
export async function waitingLabel(): Promise<string> {
  const page = await listDrafts("state=open&limit=" + WAITING_LIMIT);
  const n = (page.items || []).length;
  if (!n) return "";
  return n + (page.next_cursor ? "+" : "");
}

const bodyOf = (err: unknown): Record<string, unknown> | null =>
  err instanceof ApiError && err.body && typeof err.body === "object" ? (err.body as Record<string, unknown>) : null;

// refusalOf reads an error answer as the drafts refusal body: the sentence,
// and the findings of a 422 or the verdict of a 409 when it carries them.
export function refusalOf(err: unknown): DraftRefused | null {
  const b = bodyOf(err);
  return b && typeof b.error === "string" ? (b as DraftRefused) : null;
}

// conflictsOf reads a rebase's 409 as its conflicts, null when it names
// none.
export function conflictsOf(err: unknown): DraftConflict[] | null {
  const b = bodyOf(err) as DraftConflicts | null;
  return b && Array.isArray(b.conflicts) && b.conflicts.length ? b.conflicts : null;
}
