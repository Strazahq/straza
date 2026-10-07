// The two error shapes. A read that did not
// reach the server leaves the state unknown, so its block is violet and the
// caller keeps the last good data on screen below it. A refused write has
// a definite answer from the server, so its block is red and quotes the
// server's own sentence.

type FetchProps = {
  subject: string;
  detail: string;
  lastRead?: Date | null;
};

// FetchError is the read shape: "<Subject>: unreachable, state unknown."
// with the detail and the time of the last successful read.
export function FetchError({ subject, detail, lastRead }: FetchProps) {
  const at = lastRead ? lastRead.toLocaleTimeString([], { hour12: false }) : null;
  return (
    <div role="status" className="border-l-[3px] border-unknown bg-card px-4 py-3 text-sm leading-relaxed text-text-2" data-fetch-error>
      <span className="font-semibold text-unknown">{subject + ": unreachable, state unknown."}</span>{" "}
      {detail}
      {at && <span>{" last successful read " + at + "."}</span>}
    </div>
  );
}

type RefusedProps = {
  subject: string;
  message: string;
  // Multi-step operations may have saved part of the change or lost a
  // response, so their caller can use an outcome-neutral heading.
  heading?: string;
};

// RefusedError is the write shape: "<Subject> refused." with what the
// server said.
export function RefusedError({ subject, message, heading }: RefusedProps) {
  return (
    <div role="alert" className="border-l-[3px] border-danger bg-card px-4 py-3 text-sm leading-relaxed text-text-2" data-refused-error>
      <span className="font-semibold text-danger">{heading ?? subject + " refused."}</span>{" "}
      {message}
    </div>
  );
}
