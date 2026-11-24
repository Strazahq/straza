import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { HelpTip } from "@/components/help-tip";
import { Chip, Tag } from "@/components/chip";
import { RefusedError } from "@/components/error-state";
import { useDecide } from "@/components/approval-decide";
import { KIND_ICON } from "@/components/kind-icon";
import { type ApprovalRow, getPolicy } from "@/lib/api";
import { type Seat, callOf, decidersOf, durationWords, holdShare, kindOf, paramsOf, phaseOf, secondsLeft, shortID, timeLeft } from "@/lib/approval-model";
import {
  ABOUT_APPROVE,
  ABOUT_DENY,
  APPROVE,
  CARD_PARAMS,
  CLOSE,
  DECIDE_BY_HELP_HOLD,
  DECIDE_BY_HELP_HOOK,
  DECIDE_BY_HELP_TICKET,
  DENY,
  DETAILS,
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
  KIND_WORD,
  NONE_GIVEN,
  ON_SERVER,
  OPEN_RULE,
  OPEN_SELF_SERVICE,
  OPEN_SESSION,
  OPEN_TRANSCRIPT,
  OWN_REQUEST,
  OWN_REQUEST_WHERE,
  PARAMS_HELP,
  PARAMS_HELP_TOOL,
  PARAMS_HINT_COMMAND,
  PARAMS_HINT_NONE,
  PARAMS_HINT_STORED,
  PARAMS_NONE,
  PARAM_COLUMN,
  PHASE_WORD,
  REASON_LABEL,
  REASON_MAX,
  REASON_PLACEHOLDER,
  SAYS_NONE,
  SAYS_OWN,
  SELF_SERVICE_PAGE,
  SHOW_JSON,
  THEIR_REASON,
  UNVERIFIED,
  type Verdict,
  WHO_HELP,
  WINDOW_CLOSED,
  approveDoes,
  atZero,
  decideByRest,
  decideHelp,
  decidedWhere,
  denyDoes,
  doesLine,
  expiredLine,
  grantWords,
  hashLine,
  kindSays,
  laneWord,
  metaLine,
  openRole,
  paramsCut,
  paramsHint,
  phaseTone,
  reasonOf,
  requestTitle,
  signedWords,
  sponsoredWords,
  srStateLine,
  verdictVerb,
  whoLine,
} from "@/lib/approval-words";
import { navigate } from "@/lib/router";
import { absTime, agoWord } from "@/lib/words";

// The detail of one request as a dialog in the middle of the screen, and
// the one place a request is decided. The title says who asks and for what.
// A strip under it says what kind of wait this is and how long is left. The
// body then reads top to bottom as the decision does: the call, its
// parameters folded, the requester's reason, what a yes and a no do, and
// who can decide. The record rows sit folded under Details, and the reason
// field under them. Deny and Approve decide at once, so no second question
// follows. Focus lands on Close, so no tooltip opens on its own.

const TICK_MS = 1000;
const CAPTION = "text-caption font-semibold uppercase tracking-[.08em]";
const LABEL = CAPTION + " text-muted-foreground";
const FOLD = "cursor-pointer text-dense text-link";
// On a narrow page the strip's sentence takes a row of its own under the chip
// and the time left, since beside them it would wrap word by word.
const STRIP_SAYS = "order-last min-w-0 basis-full text-body leading-snug text-foreground sm:order-none sm:flex-1 sm:basis-0";
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

// BeforeSetup is a page's hook for the requests it reads before any device
// of the reader is set up. A request the reader may decide then cannot be
// answered there: its row ends in the page's words, and its dialog says why
// in place of the reason field and offers the setup in place of Deny and
// Approve.
export type BeforeSetup = {
  // onSetUp opens the page's setup. The dialog closes first.
  onSetUp: () => void;
  // words are the page's own: the end of a row or a card, the dialog's line
  // and its button. An empty button leaves the setup out of the dialog.
  words: { row: string; line: string; button: string };
};

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
  // decide records the verdict with the reason typed and answers the fresh
  // record, through the queue's lane.
  decide: (row: ApprovalRow, verdict: Verdict, reason: string) => Promise<ApprovalRow>;
  // get re-reads one record after a 409, or null where the lane cannot.
  get: ((id: string) => Promise<ApprovalRow | null>) | null;
  // onDecided hands the fresh record back, both after a decision of ours
  // and after a decision someone else made first.
  onDecided: (row: ApprovalRow) => void;
  // doors opens a console area for a fact of the dialog, or null to leave
  // every door out. The console's own doors stand where a caller names none.
  doors?: DoorOpener | null;
  // beforeSetup is the page's hook while no device of the reader is set up,
  // or undefined where the reader answers here.
  beforeSetup?: BeforeSetup;
};

export function ApprovalDialog({ row, open, onOpenChange, seat, holders, isMine, needsDevice = false, isStuck, decide: send, get, onDecided, doors = consoleDoors, beforeSetup }: ApprovalDialogProps) {
  const closeRef = React.useRef<HTMLButtonElement>(null);
  const { reason, setReason, busy, problem, final, decide } = useDecide(row, { decide: send, get, onDecided, onLanded: () => onOpenChange(false) });
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
  // decides says the reader may answer here: the reason field and the two
  // buttons show until a final answer replaces them. setUpFirst says the
  // reader may decide but has no device to answer with yet.
  const decides = waiting && isMine && !needsDevice && !beforeSetup;
  const setUpFirst = waiting && isMine && !needsDevice && beforeSetup ? beforeSetup : null;

  return (
    <Dialog open={open} onOpenChange={(o) => { if (o || !busy) onOpenChange(o); }}>
      <DialogContent
        className="flex max-h-[90vh] flex-col gap-0 p-0 sm:max-w-[min(740px,calc(100%-2rem))]"
        data-request-dialog={row.id}
        onOpenAutoFocus={(e) => { e.preventDefault(); closeRef.current?.focus(); }}
      >
        <DialogHeader className="border-b border-border px-5 py-3.5 pr-12">
          {/* The call has its own box under the title. It stays in the dialog's name for a screen reader. */}
          <DialogTitle className="text-section leading-snug">{requestTitle(row)}<span className="sr-only">{" " + call.call}</span></DialogTitle>
          <DialogDescription className="sr-only">{closed ? WINDOW_CLOSED : waiting ? doesLine(row, grant) : phase === "expired" ? expiredLine(row, isStuck) : srStateLine(row, phase, now)}</DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border bg-card px-5 py-3" data-strip>
          {waiting
            ? <Chip tone={kind === "hold" ? "warn" : "plain"} icon={KIND_ICON[kind]} data-kind={KIND_WORD[kind]}>{KIND_WORD[kind]}</Chip>
            : <Chip tone={phaseTone(phase)} data-phase={PHASE_WORD[phase]}>{PHASE_WORD[phase]}</Chip>}
          {waiting && !closed && <span className={STRIP_SAYS} data-kind-says>{kindSays(row)}</span>}
          {closed && <span className={STRIP_SAYS} data-window-closed>{WINDOW_CLOSED}</span>}
          {!waiting && phase === "expired" && <span className={STRIP_SAYS} data-expired-line>{expiredLine(row, isStuck)}</span>}
          {!waiting && phase !== "expired" && <span className={STRIP_SAYS} data-decided-line>{srStateLine(row, phase, now)}</span>}
          {waiting && (
            <span className="ml-auto text-right sm:ml-0" data-time>
              <span className={"block text-body leading-6 font-semibold tabular-nums " + (kind === "hold" ? "text-warn" : "text-foreground")} data-time-left>{timeLeft(row, now)}</span>
              {!closed && <span className="block text-caption leading-snug text-muted-foreground" data-at-zero>{atZero(row)}</span>}
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
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border border-border bg-card px-4 py-3" data-call-card>
            <span className="break-all font-mono text-title leading-7 font-semibold text-foreground" data-card-tool>{call.call}</span>
            {call.whereKind === "server" && (
              <>
                <span className="text-body text-muted-foreground">{ON_SERVER}</span>
                <Tag icon={KIND_ICON.server} data-card-server>{call.where}</Tag>
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
              <table className="mt-2 w-full border-separate border-spacing-0 overflow-hidden rounded-md border border-border text-dense" data-params>
                <thead>
                  <tr>
                    <th className={LABEL + " border-b border-border bg-secondary/60 px-2.5 py-1.5 text-left"}>{PARAM_COLUMN.name}</th>
                    <th className={LABEL + " border-b border-border bg-secondary/60 px-2.5 py-1.5 text-left"}>{PARAM_COLUMN.value}</th>
                  </tr>
                </thead>
                <tbody>
                  {params.map((p) => (
                    <tr key={p.name} className="even:bg-secondary/30" data-param={p.name}>
                      <td className="w-[36%] border-r border-b border-border px-2.5 py-1.5 align-top font-mono text-caption font-medium text-foreground [tr:last-child>&]:border-b-0">{p.name}</td>
                      <td className="break-words border-b border-border px-2.5 py-1.5 align-top text-foreground [tr:last-child>&]:border-b-0">{p.value}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : unparsed && call.whereKind === "shell" ? null : (
              <p className="mt-2 text-dense text-muted-foreground" data-params-none>{unparsed ? row.argsPreview : PARAMS_NONE}</p>
            )}
            {row.argsPreview && (
              <details className="mt-1.5">
                <summary className="cursor-pointer text-caption text-muted-foreground hover:text-foreground">{SHOW_JSON}</summary>
                <pre className="mt-1 overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-caption leading-relaxed text-foreground" data-args>{row.argsPreview}</pre>
                <p className="mt-1 font-mono text-caption text-muted-foreground" data-hash>{hashLine(row.argvHashPrefix)}{row.argsTruncated ? " · " + paramsCut(row.argsBytes || 0) : ""}</p>
              </details>
            )}
          </details>

          <p className="-mt-1.5 text-dense text-muted-foreground" data-meta>{metaLine(row, sponsor, seat)}</p>

          {row.justification ? (
            <div className="rounded-md border border-dashed border-border px-3.5 py-2.5" data-says>
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-dense text-text-2" data-says-head>{reasonOf(row.username || row.user)}</span>
                <Chip tone="plain" title={SAYS_OWN} data-unverified>{UNVERIFIED}</Chip>
              </div>
              <p className="mt-1 whitespace-pre-wrap text-body leading-relaxed text-text-2" data-justification>{"“" + row.justification + "”"}</p>
            </div>
          ) : (
            <p className="-mt-1.5 text-dense text-muted-foreground" data-justification-none>{SAYS_NONE}</p>
          )}

          {waiting && !closed && (
            <div className="grid gap-3 sm:grid-cols-2" data-outcomes>
              <div className="rounded-md border border-border px-3.5 py-3" data-if="approve">
                <div className="flex items-center gap-1"><span className={CAPTION + " text-ok"}>{IF_APPROVE}</span><HelpTip label={ABOUT_APPROVE} text={decideHelp("approve", row)} /></div>
                <p className="mt-0.5 text-body leading-snug text-foreground">{approveDoes(row, grant)}</p>
              </div>
              <div className="rounded-md border border-border px-3.5 py-3" data-if="deny">
                <div className="flex items-center gap-1"><span className={CAPTION + " text-danger"}>{IF_DENY}</span><HelpTip label={ABOUT_DENY} text={decideHelp("deny", row)} /></div>
                <p className="mt-0.5 text-body leading-snug text-foreground">{denyDoes(row)}</p>
              </div>
            </div>
          )}

          <p className="text-dense leading-snug text-text-2" data-who-line>
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
            <summary className={FOLD}>{DETAILS}</summary>
            <table className="mt-2 w-full border-collapse text-dense" data-facts>
              <tbody>
                {!waiting && phase !== "expired" && (
                  <>
                    <Fact label={FACT_DECIDED_BY}>{decidedWhere(row)}<Muted>{" · " + absTime(row.decidedAt ?? undefined) + " · " + agoWord(row.decidedAt ?? undefined)}</Muted></Fact>
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
                <Fact label={FACT_WHEN}>{agoWord(row.createdAt)}<Muted>{" · " + absTime(row.createdAt)}</Muted></Fact>
                <Fact label={FACT_LANE}>{laneWord(row.lane)}</Fact>
                {waiting && (
                  <Fact label={FACT_DECIDE_BY} help={<HelpTip label={FACT_DECIDE_BY} text={kind === "ticket" ? DECIDE_BY_HELP_TICKET : row.lane === "hook" ? DECIDE_BY_HELP_HOOK : DECIDE_BY_HELP_HOLD} />}>
                    <span data-decide-by>
                      <span className={"font-mono text-caption " + (kind === "ticket" ? "text-link" : "text-warn")}>{timeLeft(row, now)}</span>
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

          {final ? (
            <p className="m-0 rounded-md border border-input bg-secondary px-3.5 py-2.5 text-body leading-snug text-foreground" role="status" data-final>
              {final.message}
            </p>
          ) : decides ? (
            <div className="flex flex-col gap-1.5" data-reason-field>
              <Label htmlFor="decide-reason">{REASON_LABEL}</Label>
              <Textarea id="decide-reason" value={reason} onChange={(e) => setReason(e.target.value)} placeholder={REASON_PLACEHOLDER} maxLength={REASON_MAX} />
            </div>
          ) : setUpFirst && (
            <p className="m-0 text-dense leading-snug text-text-2" data-needs-device>{setUpFirst.words.line}</p>
          )}
        </div>

        {problem && !final && (
          <div className="border-t border-border px-5 py-3">
            <RefusedError subject={verdictVerb(problem.verdict)} message={problem.message} />
          </div>
        )}

        {/* One row on every width, Close on the left, so a phone keeps the three buttons side by side. */}
        <DialogFooter className="flex-row items-center justify-between border-t border-border px-5 py-3 sm:justify-between">
          <Button ref={closeRef} variant="ghost" disabled={!!busy} onClick={() => onOpenChange(false)}>{CLOSE}</Button>
          {waiting && isMine && needsDevice && (
            <p className="m-0 max-w-[52ch] min-w-0 flex-1 text-right text-caption leading-snug text-text-2" data-own-request>
              {OWN_REQUEST} {OWN_REQUEST_WHERE} <a href={SELF_SERVICE_PAGE} className="text-link underline-offset-4 hover:underline">{OPEN_SELF_SERVICE}</a>
            </p>
          )}
          {decides && !final && (
            <div className="flex items-center gap-2">
              <Button variant="outline" className={DANGER_BUTTON} disabled={!!busy} aria-busy={busy === "deny" || undefined} onClick={() => void decide("deny")}>
                {busy === "deny" && <Loader2Icon className="animate-spin" data-spinner />}{DENY}
              </Button>
              <Button disabled={!!busy} aria-busy={busy === "approve" || undefined} onClick={() => void decide("approve")}>
                {busy === "approve" && <Loader2Icon className="animate-spin" data-spinner />}{APPROVE}
              </Button>
            </div>
          )}
          {setUpFirst && setUpFirst.words.button && (
            <Button onClick={() => { onOpenChange(false); setUpFirst.onSetUp(); }} data-before-setup>{setUpFirst.words.button}</Button>
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
