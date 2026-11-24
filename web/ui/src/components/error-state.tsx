import type * as React from "react";
import { REFUSED_BY, UNANSWERED } from "@/lib/say";
import { absTime } from "@/lib/words";

// The two error shapes. A read that did not
// reach the server leaves the state unknown, so its block is violet and the
// caller keeps the last good data on screen below it. A refused write has
// a definite answer from the server, so its block is red and quotes the
// server's own sentence.

type FetchProps = {
  subject: string;
  detail: string;
  lastRead?: Date | null;
  // heading replaces the subject's line for a write whose outcome is
  // unknown, which names what may not have landed.
  heading?: string;
};

// SAYS_UNREAD splits a detail that readFailed made into its opening, which
// says what could not be read, and the rest, which says why and what to do.
const SAYS_UNREAD = /^(.{1,80}? could not be read)(: | because )([\s\S]*)$/;

// FetchError is the read shape. A detail that itself opens with what could
// not be read is shown as it is, once, with that opening as the heading, so
// the block names the read in the page's own words and says it one time.
// Any other detail follows the heading "<Subject> could not be read.", or
// the heading the caller gives. The block ends with the clock time of the
// last good read, in the active zone with the zone named.
export function FetchError({ subject, detail, lastRead, heading }: FetchProps) {
  const at = lastRead ? absTime(lastRead.toISOString()).slice(11) : null;
  const own = heading === undefined ? SAYS_UNREAD.exec(detail) : null;
  return (
    <div role="status" className="border-l-[3px] border-unknown bg-card px-4 py-3 text-dense leading-relaxed text-text-2" data-fetch-error>
      {own ? (
        <>
          <span className="font-semibold text-unknown">{own[1] + (own[2] === ": " ? ":" : "")}</span>
          {(own[2] === ": " ? " " : own[2]) + own[3]}
        </>
      ) : (
        <>
          <span className="font-semibold text-unknown">{heading ?? subject + " could not be read."}</span>{" "}
          {detail}
        </>
      )}
      {at && <span>{" Last good read: " + at + "."}</span>}
    </div>
  );
}

type RefusedProps = {
  subject: string;
  message: string;
  // Multi-step operations may have saved part of the change or lost a
  // response, so their caller can use an outcome-neutral heading.
  heading?: string;
  // children is a line under the message, such as what the refusal did to
  // what the person entered.
  children?: React.ReactNode;
};

// RefusedError is the write shape: "<Subject> refused." with what the
// server said. A message from refused folds its opening into the heading,
// "<Subject> refused: <why>.", so the block says refused once. A write
// Straza did not answer is headed "<Subject> not confirmed.", since it may
// have landed.
export function RefusedError({ subject, message, heading, children }: RefusedProps) {
  const said = heading === undefined && message.startsWith(REFUSED_BY) ? message.slice(REFUSED_BY.length) : null;
  const head = heading ?? (said !== null ? subject + " refused:" : message.startsWith(UNANSWERED) ? subject + " not confirmed." : subject + " refused.");
  return (
    <div role="alert" className="border-l-[3px] border-danger bg-card px-4 py-3 text-dense leading-relaxed text-text-2" data-refused-error>
      <span className="font-semibold text-danger">{head}</span>{" "}
      {said ?? message}
      {children && <p className="mt-1.5 mb-0">{children}</p>}
    </div>
  );
}
