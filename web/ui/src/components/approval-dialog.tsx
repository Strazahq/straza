import * as React from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { HelpTip } from "@/components/help-tip";
import { WordBadge } from "@/components/users-table";
import { type ApprovalRow, getPolicy } from "@/lib/api";
import { type Seat, callOf, decidersOf, durationWords, holdShare, kindOf, paramsOf, phaseOf, secondsLeft, shortID, timeLeft } from "@/lib/approval-model";
import {
  APPROVE,
  CARD_PARAMS,
  CLOSE,
  DECIDE_BY_HELP_HOLD,
  DECIDE_BY_HELP_TICKET,
  DENY,
  DETAILS,
  DETAILS_HINT,
  FACT_DECIDED_BY,
  FACT_DECIDE_BY,
  FACT_GRANT,
  FACT_LANE,
  FACT_REQUESTED,
  FACT_RULE,
  FACT_SESSION,
  FACT_SIGNED,
  FACT_WHEN,
  FACT_WHO,
  IF_APPROVE,
  IF_DENY,
  IN_POLICY,
  KIND_SAYS,
  KIND_WORD,
  NONE_GIVEN,
  ON_SERVER,
  OPEN_RULE,
  OPEN_SELF_SERVICE,
  OPEN_SESSION,
  OPEN_TRANSCRIPT,
  OWN_REQUEST,
  OWN_REQUEST_WHERE,
  PARAMS_COMMAND,
  PARAMS_HELP,
  PARAMS_HELP_TOOL,
  PARAMS_HINT_COMMAND,
  PARAMS_HINT_NONE,
  PARAMS_HINT_STORED,
  PARAMS_NONE,
  PARAM_COLUMN,
  PHASE_WORD,
  REQUEST_TITLE,
  SAYS_NONE,
  SAYS_NONE_HOOK,
  SAYS_OWN,
  SELF_SERVICE_PAGE,
  SHOW_JSON,
  THEIR_REASON,
  type Verdict,
  WHO_HELP,
  WINDOW_CLOSED,
  approveDoes,
  askVerb,
  atZero,
  decideByRest,
  decidedWhere,
  denyDoes,
  doesLine,
  expiredLine,
  grantWords,
  hashLine,
  laneWord,
  metaLine,
  openRole,
  paramsCut,
  paramsHint,
  phaseTone,
  signedWords,
  sponsoredWords,
  srStateLine,
  whoLine,
} from "@/lib/approval-words";
import { navigate } from "@/lib/router";
import { absTime, relTimeText } from "@/lib/words";

// The detail of one request as a dialog in the middle of the screen. A
// strip under the title says what kind of wait this is and how long is
// left. The body then reads top to bottom as the decision does: who asks,
// the call, its parameters folded, the agent's reason, what a yes and a no
// do, and who can decide. The record rows sit folded under Details. Focus
// lands on Close, so no tooltip opens on its own. The decide dialog belongs
// to the queue, so Deny and Approve hand the row back.

const TICK_MS = 1000;
const CAPTION = "text-[13px] font-semibold uppercase tracking-[.08em]";
const LABEL = CAPTION + " text-muted-foreground";
const FOLD = "cursor-pointer text-sm text-link";
// On a narrow page the strip's sentence takes a row of its own under the chip
// and the time left, since beside them it would wrap word by word.
const STRIP_SAYS = "order-last min-w-0 basis-full text-[15px] leading-snug text-foreground sm:order-none sm:flex-1 sm:basis-0";
const LINK_BUTTON = "h-auto p-0 align-baseline text-link";
const DANGER_BUTTON = "border-danger text-danger hover:bg-danger-bg hover:text-danger";

// DoorArea is a console area a fact of this dialog can open.
export type DoorArea = "sessions" | "transcripts" | "roles" | "policies";

// DoorOpener opens one of those areas for an identifier. The console opens
// its own areas; a page with no console behind it passes null, and every
// door is then left out, since a link the reader may not follow is worse
// than no link at all.
export type DoorOpener = (area: DoorArea, id: string) => void;

export const consoleDoors: DoorOpener = (area, id) => navigate(area, [id]);

export type ApprovalDialogProps = {
  row: ApprovalRow;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  seat: Seat;
  // holders maps a role name to its holder count, for the Who can decide
  // sentence; a role missing from it leaves the number out.
  holders: Record<string, number>;
  isMine: boolean;
  // needsDevice says the request is the seat's own and this lane cannot
  // sign it: the footer says where to confirm it and carries no buttons.
  needsDevice?: boolean;
  isStuck: boolean;
  onDecide: (row: ApprovalRow, verdict: Verdict) => void;
  // doors opens a console area for a fact of the dialog, or null to leave
  // every door out. The console's own doors stand where a caller names none.
  doors?: DoorOpener | null;
};

export function ApprovalDialog({ row, open, onOpenChange, seat, holders, isMine, needsDevice = false, isStuck, onDecide, doors = consoleDoors }: ApprovalDialogProps) {
  const closeRef = React.useRef<HTMLButtonElement>(null);
  const [now, setNow] = React.useState(() => Date.now());
  // grant is the standing approval's grant window in words, read from the
  // rule that held the call; null until read, or when the read failed.
  const [grant, setGrant] = React.useState<string | null>(null);

  // One clock in the dialog, so the countdown moves while it is open.
  React.useEffect(() => {
    if (!open) return;
    const t = setInterval(() => setNow(Date.now()), TICK_MS);
    return () => clearInterval(t);
  }, [open]);

  // The record carries no grant window until it is approved, so the
  // approve sentence reads it from the rule. The policy model loads on
  // first use, since it carries the YAML parser. Reading a policy is a
  // console read, so it rides the same standing as the doors: without them
  // the sentence falls back to the words for an unread window.
  React.useEffect(() => {
    if (!open || !doors || row.state !== "pending" || kindOf(row) !== "ticket") return;
    let alive = true;
    setGrant(null);
    getPolicy(row.set)
      .then(async (doc) => {
        const m = await import("@/lib/policy-model");
        const rule = m.rulesOf(m.openDoc(doc.yaml || "")).find((x) => x.id === row.rule);
        if (alive && rule) setGrant(durationWords(rule.grantTTLSeconds));
      })
      .catch(() => undefined);
    return () => { alive = false; };
  }, [open, doors, row.id, row.state, row.set, row.rule]); // eslint-disable-line react-hooks/exhaustive-deps

  const call = callOf(row.summary);
  const phase = phaseOf(row, now);
  const waiting = row.state === "pending";
  // closed says the window ran out while the dialog was open. The record
  // reads pending until the list is read again, but the server decides
  // nothing after the window, so the strip says so and the two boxes go.
  const closed = waiting && secondsLeft(row.expiresAt, now) <= 0;
  const kind = kindOf(row);
  const deciders = decidersOf(row);
  const params = paramsOf(row.argsPreview);
  const sponsor = deciders.kind === "sponsor" || deciders.kind === "both" ? deciders.users[0] : "";
  // unparsed is a stored preview that is no JSON object: a command line, or
  // a bare value the fold shows as it is.
  const unparsed = !!row.argsPreview && !params;
  const hint = params && params.length > 0 ? paramsHint(params.length) : unparsed ? (call.whereKind === "shell" ? PARAMS_HINT_COMMAND : PARAMS_HINT_STORED) : PARAMS_HINT_NONE;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="flex max-h-[90vh] flex-col gap-0 p-0 sm:max-w-[min(740px,calc(100%-2rem))]"
        data-request-dialog={row.id}
        onOpenAutoFocus={(e) => { e.preventDefault(); closeRef.current?.focus(); }}
      >
        <DialogHeader className="border-b border-border px-5 py-3.5 pr-12">
          {/* The call left the visible title for its own box. It stays in the dialog's name for a screen reader. */}
          <DialogTitle className="text-lg leading-snug">{REQUEST_TITLE}<span className="sr-only">{" " + call.call}</span></DialogTitle>
          <DialogDescription className="sr-only">{closed ? WINDOW_CLOSED : waiting ? doesLine(row, grant) : phase === "expired" ? expiredLine(row, isStuck) : srStateLine(row, phase, now)}</DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border bg-card px-5 py-3" data-strip>
          {waiting
            ? <WordBadge word={KIND_WORD[kind]} tone={kind === "hold" ? "warn" : "teal"} mono attr="data-kind" />
            : <WordBadge word={PHASE_WORD[phase]} tone={phaseTone(phase)} attr="data-phase" />}
          {waiting && !closed && <span className={STRIP_SAYS} data-kind-says>{KIND_SAYS[kind]}</span>}
          {closed && <span className={STRIP_SAYS} data-window-closed>{WINDOW_CLOSED}</span>}
          {!waiting && phase === "expired" && <span className={STRIP_SAYS} data-expired-line>{expiredLine(row, isStuck)}</span>}
          {!waiting && phase !== "expired" && <span className={STRIP_SAYS} data-decided-line>{srStateLine(row, phase, now)}</span>}
          {waiting && (
            <span className="ml-auto text-right sm:ml-0" data-time>
              <span className={"block text-base font-semibold tabular-nums " + (kind === "hold" ? "text-warn" : "text-foreground")} data-time-left>{timeLeft(row, now)}</span>
              {!closed && <span className="block text-[13px] leading-snug text-muted-foreground" data-at-zero>{atZero(row)}</span>}
            </span>
          )}
        </div>
        {waiting && kind === "hold" && (
          // The bar repeats the time left above it, so it is hidden from
          // assistive technology. It steps with the dialog's clock.
          <span className="block h-1 shrink-0 bg-border" aria-hidden="true" data-drain>
            <span className="block h-full bg-warn" style={{ width: holdShare(row, now) + "%" }} />
          </span>
        )}

        <div className="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-5 py-4">
          <p className="text-base leading-snug text-text-2" data-ask><b className="font-semibold text-foreground">{row.username || row.user}</b> {askVerb(row)}</p>

          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border bg-card px-4 py-3" data-call-card>
            <span className="break-all font-mono text-xl font-semibold text-foreground" data-card-tool>{call.call}</span>
            {call.whereKind === "server" && (
              <>
                <span className="text-[15px] text-muted-foreground">{ON_SERVER}</span>
                <span className="rounded-md border border-link/40 bg-accent-bg px-1.5 py-0.5 font-mono text-sm text-link" data-card-server>{call.where}</span>
              </>
            )}
          </div>

          <details className="-mt-1.5" data-params-fold>
            <summary className={FOLD}>
              {CARD_PARAMS}
              <span className="text-muted-foreground" data-params-hint>{" · " + hint + " "}</span>
              <HelpTip label={CARD_PARAMS} text={row.bindingScope === "tool_identity" ? PARAMS_HELP_TOOL : PARAMS_HELP} />
            </summary>
            {params && params.length > 0 ? (
              <table className="mt-2 w-full border-separate border-spacing-0 overflow-hidden rounded-md border border-border text-sm" data-params>
                <thead>
                  <tr>
                    <th className={LABEL + " border-b border-border bg-secondary/60 px-2.5 py-1.5 text-left"}>{PARAM_COLUMN.name}</th>
                    <th className={LABEL + " border-b border-border bg-secondary/60 px-2.5 py-1.5 text-left"}>{PARAM_COLUMN.value}</th>
                  </tr>
                </thead>
                <tbody>
                  {params.map((p) => (
                    <tr key={p.name} className="even:bg-secondary/30" data-param={p.name}>
                      <td className="w-[36%] border-r border-b border-border px-2.5 py-1.5 align-top font-mono text-[13px] font-medium text-foreground [tr:last-child>&]:border-b-0">{p.name}</td>
                      <td className="break-words border-b border-border px-2.5 py-1.5 align-top text-foreground [tr:last-child>&]:border-b-0">{p.value}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p className="mt-2 text-sm text-muted-foreground" data-params-none>{unparsed ? (call.whereKind === "shell" ? PARAMS_COMMAND : row.argsPreview) : PARAMS_NONE}</p>
            )}
            {row.argsPreview && (
              <details className="mt-1.5">
                <summary className="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{SHOW_JSON}</summary>
                <pre className="mt-1 overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-[12.5px] leading-relaxed text-foreground" data-args>{row.argsPreview}</pre>
                <p className="mt-1 font-mono text-[12.5px] text-muted-foreground" data-hash>{hashLine(row.argvHashPrefix)}{row.argsTruncated ? " · " + paramsCut(row.argsBytes || 0) : ""}</p>
              </details>
            )}
          </details>

          <p className="-mt-1.5 text-sm text-muted-foreground" data-meta>{metaLine(row, sponsor, seat)}</p>

          {row.justification ? (
            <div className="rounded-md border border-dashed border-border px-3.5 py-2.5" data-says>
              <p className="whitespace-pre-wrap text-[15px] leading-relaxed text-text-2" data-justification>{"“" + row.justification + "”"}</p>
              <p className="mt-0.5 text-[13px] text-muted-foreground" data-says-own>{SAYS_OWN}</p>
            </div>
          ) : (
            <p className="-mt-1.5 text-sm text-muted-foreground" data-justification-none>{row.lane === "hook" ? SAYS_NONE_HOOK : SAYS_NONE}</p>
          )}

          {waiting && !closed && (
            <div className="grid gap-3 sm:grid-cols-2" data-outcomes>
              <div className="rounded-md border border-border px-3.5 py-3" data-if="approve">
                <div className={CAPTION + " text-ok"}>{IF_APPROVE}</div>
                <p className="mt-0.5 text-[15px] leading-snug text-foreground">{approveDoes(row, grant)}</p>
              </div>
              <div className="rounded-md border border-border px-3.5 py-3" data-if="deny">
                <div className={CAPTION + " text-danger"}>{IF_DENY}</div>
                <p className="mt-0.5 text-[15px] leading-snug text-foreground">{denyDoes(row)}</p>
              </div>
            </div>
          )}

          <p className="text-sm leading-snug text-text-2" data-who-line>
            <span className="mr-1.5 inline-flex items-center gap-1 font-semibold text-foreground">{FACT_WHO}<HelpTip label={FACT_WHO} text={WHO_HELP} /></span>
            <span data-who>{whoLine(row, seat, holders, isMine, isStuck, needsDevice)}</span>
            {isStuck && waiting && doors && (
              <>
                {" "}
                <Button variant="link" size="sm" className={LINK_BUTTON} onClick={() => doors("roles", deciders.roles[0] || "")}>{openRole(deciders.roles[0] || "")}</Button>
                {" · "}
                <Button variant="link" size="sm" className={LINK_BUTTON} onClick={() => doors("policies", row.set)}>{OPEN_RULE}</Button>
              </>
            )}
          </p>

          {/* A waiting request keeps its record folded so the decision fits the screen. A decided one is the record, so it opens. */}
          <details open={!waiting} data-details>
            <summary className={FOLD}>{DETAILS}<span className="text-muted-foreground">{" · " + DETAILS_HINT}</span></summary>
            <table className="mt-2 w-full border-collapse text-sm" data-facts>
              <tbody>
                {!waiting && phase !== "expired" && (
                  <>
                    <Fact label={FACT_DECIDED_BY}>{decidedWhere(row)}<Muted>{" · " + absTime(row.decidedAt ?? undefined) + " · " + relTimeText(row.decidedAt ?? undefined)}</Muted></Fact>
                    <Fact label={THEIR_REASON}>{row.decidedReason ? <span data-decided-reason>{"“" + row.decidedReason + "”"}</span> : <span className="text-muted-foreground">{NONE_GIVEN}</span>}</Fact>
                    <Fact label={FACT_SIGNED}><span data-signed>{signedWords(row)}</span></Fact>
                    {kind === "ticket" && row.state === "approved" && <Fact label={FACT_GRANT}><span data-grant>{grantWords(row, phase, now)}</span></Fact>}
                  </>
                )}
                <Fact label={FACT_REQUESTED}><b className="font-semibold">{row.username || row.user}</b>{sponsor && <Muted>{" · " + sponsoredWords(sponsor)}</Muted>}</Fact>
                <Fact label={FACT_SESSION}>
                  <span className="font-mono">{shortID(row.session)}</span>
                  {doors && (
                    <>
                      {" · "}
                      <Button variant="link" size="sm" className={LINK_BUTTON} onClick={() => doors("sessions", row.session)}>{OPEN_SESSION}</Button>
                      {" · "}
                      <Button variant="link" size="sm" className={LINK_BUTTON} onClick={() => doors("transcripts", row.session)}>{OPEN_TRANSCRIPT}</Button>
                    </>
                  )}
                </Fact>
                <Fact label={FACT_WHEN}>{relTimeText(row.createdAt)}<Muted>{" · " + absTime(row.createdAt)}</Muted></Fact>
                <Fact label={FACT_LANE}>{laneWord(row.lane)}</Fact>
                {waiting && (
                  <Fact label={FACT_DECIDE_BY} help={<HelpTip label={FACT_DECIDE_BY} text={kind === "hold" ? DECIDE_BY_HELP_HOLD : DECIDE_BY_HELP_TICKET} />}>
                    <span data-decide-by>
                      <span className={"font-mono text-[13px] " + (kind === "ticket" ? "text-link" : "text-warn")}>{timeLeft(row, now)}</span>
                      <Muted>{" · " + decideByRest(row)}</Muted>
                    </span>
                  </Fact>
                )}
                <Fact label={FACT_RULE}>
                  <span className="font-mono">{row.rule}</span>
                  <Muted>{" " + IN_POLICY + " "}</Muted>
                  {doors
                    ? <Button variant="link" size="sm" className={LINK_BUTTON} onClick={() => doors("policies", row.set)}>{row.set}</Button>
                    : <span className="font-mono">{row.set}</span>}
                </Fact>
              </tbody>
            </table>
          </details>
        </div>

        <DialogFooter className="border-t border-border px-5 py-3 sm:justify-between">
          <Button ref={closeRef} variant="outline" onClick={() => onOpenChange(false)}>{CLOSE}</Button>
          {waiting && isMine && needsDevice && (
            <p className="m-0 max-w-[52ch] text-[13px] leading-snug text-text-2 sm:text-right" data-own-request>
              {OWN_REQUEST} {OWN_REQUEST_WHERE} <a href={SELF_SERVICE_PAGE} className="text-link underline-offset-4 hover:underline">{OPEN_SELF_SERVICE}</a>
            </p>
          )}
          {waiting && isMine && !needsDevice && (
            <div className="flex items-center gap-2">
              <Button variant="outline" className={DANGER_BUTTON} onClick={() => onDecide(row, "deny")}>{DENY}</Button>
              <Button onClick={() => onDecide(row, "approve")}>{APPROVE}</Button>
            </div>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// Fact is one row of the facts table: the label, its help icon when the
// label alone could still leave a question, and the value.
function Fact({ label, help, children }: { label: string; help?: React.ReactNode; children: React.ReactNode }) {
  return (
    <tr className="border-b border-border last:border-b-0" data-fact={label}>
      <th scope="row" className="w-[170px] whitespace-nowrap py-2 pr-3 text-left align-top font-semibold text-foreground">
        <span className="inline-flex items-center gap-1">{label}{help}</span>
      </th>
      <td className="py-2 align-top text-foreground">{children}</td>
    </tr>
  );
}

function Muted({ children }: { children: React.ReactNode }) {
  return <span className="text-muted-foreground">{children}</span>;
}
