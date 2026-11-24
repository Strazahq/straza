// The two error sentence shapes every screen uses: a read that failed says
// what could not be read, why, and what to do,
// and keeps the last good data on screen; a refused write quotes the
// server's own sentence and says to fix what it names.
import type { ApiError } from "./api";

// UNANSWERED opens every sentence about a call Straza did not answer.
export const UNANSWERED = "Straza did not answer";
export const WRITE_UNCONFIRMED = UNANSWERED + ". The change may have been saved. Reload to check before trying again.";

// bare trims the server's sentence of its final period so it sits inside
// ours.
export function bare(sentence: string): string {
  return sentence.replace(/\.\s*$/, "");
}

// readFailed is the sentence under a FetchError block: subject is the
// thing that could not be read, in the words of the page ("The user list").
export function readFailed(subject: string, err: ApiError): string {
  if (err.unreachable) return subject + " could not be read because " + UNANSWERED + ". Check that it is running, then reload.";
  return subject + " could not be read: " + bare(err.message) + ". Reload to try again.";
}

// AREA_WORD names an area of the console by the screens it opens.
export const AREA_WORD: Record<string, string> = {
  identity: "Users and Roles", sessions: "Sessions", policy: "Policies", apps: "MCP servers", audit: "Audit",
  transcripts: "Transcripts", approvals: "Approvals", drafts: "Drafts", config: "Overview and Settings", tokens: "API tokens",
};

// needsArea stands where a part of a page is not read because the reader's
// role does not include its area: needsArea("Tool decisions are", "config").
export const needsArea = (what: string, area: string) => what + " not shown, because your role does not include " + (AREA_WORD[area] || area) + ".";

// REFUSED_BY opens refused's sentence. RefusedError folds it into its
// heading, so the block says refused once.
export const REFUSED_BY = "Straza refused it: ";

// refused is the sentence of a RefusedError block or a failed toast: the
// server spoke, so the answer is definite.
export function refused(err: ApiError): string {
  if (err.unreachable) return WRITE_UNCONFIRMED;
  return REFUSED_BY + bare(err.message) + ". Fix what it names, then try again.";
}

// checkFailed is refused for a read-only check (validate, simulate): the
// server stores nothing on these calls, so an unanswered one changed nothing
// and the draft on screen is still the one to retry.
export function checkFailed(err: ApiError): string {
  if (err.unreachable) return "The check could not finish because " + UNANSWERED + ". Nothing was stored. Check your connection, then try again.";
  return refused(err);
}
