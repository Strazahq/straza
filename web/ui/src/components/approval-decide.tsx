import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { type ApiError, type ApprovalRow, decideApproval, getApproval } from "@/lib/api";
import { notify } from "@/lib/notify";
import { bare, refused } from "@/lib/say";
import {
  CANCEL,
  CLOSE,
  REASON_HINT,
  REASON_LABEL,
  REASON_MAX,
  REASON_PLACEHOLDER,
  type Verdict,
  WINDOW_CLOSED,
  alreadyDecided,
  approvedToast,
  decideBody,
  decideHelp,
  decideTitle,
  deniedToast,
  finalTitle,
  notYours,
  verdictVerb,
} from "@/lib/approval-words";

// The one dialog that decides a request: the call and the requester in the
// title, one sentence of what an
// approval or a denial does, the mechanics behind the help icon, and an
// optional reason the agent and the audit record read. A server answer
// that settles the request for good ends the question: the sentence says
// what happened and Close is the only button left.

const DANGER_ACTION = "bg-danger text-white hover:bg-danger/90";

export type Ask = { row: ApprovalRow; verdict: Verdict };

// DecideRefusal is a refusal the decide lane already worded for the person,
// such as a reason its server would refuse for its length: the sentence is
// shown as it is, since nothing reached the server for us to quote. settled
// says the request is settled for good, so the question ends and Close is
// the only button left. The admin lane below never throws one.
export class DecideRefusal extends Error {
  settled: boolean;
  constructor(message: string, settled = false) {
    super(message);
    this.settled = settled;
  }
}

// The admin lane: the calls the Approvals area has always made.
const adminDecide = async (row: ApprovalRow, verdict: Verdict, reason: string) => (await decideApproval(row.id, verdict, reason)).approval;
const adminGet = async (id: string): Promise<ApprovalRow | null> => (await getApproval(id)).approval;

type Props = {
  // ask is the question being put, or null while none is.
  ask: Ask | null;
  onClose: () => void;
  // onDecided hands the fresh record back, both after a decision of ours
  // and after a decision someone else made first.
  onDecided: (row: ApprovalRow) => void;
  // decide records the verdict and answers the fresh record; the admin
  // call stands where a caller names no other lane.
  decide?: (row: ApprovalRow, verdict: Verdict, reason: string) => Promise<ApprovalRow>;
  // get re-reads one record after a 409, or null where the lane has no
  // single read and the dialog must settle on the refusal alone.
  get?: ((id: string) => Promise<ApprovalRow | null>) | null;
};

export function ApprovalDecide({ ask, onClose, onDecided, decide: send = adminDecide, get = adminGet }: Props) {
  const [reason, setReason] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [final, setFinal] = React.useState<string | null>(null);

  // Every new question starts clean, since the reason, the refusal and the
  // final sentence belong to the one decision that was asked.
  React.useEffect(() => {
    if (!ask) return;
    setReason("");
    setBusy(false);
    setProblem(null);
    setFinal(null);
  }, [ask]);

  const row = ask ? ask.row : null;
  const verdict: Verdict = ask ? ask.verdict : "approve";
  const verb = verdictVerb(verdict);

  const decide = async () => {
    if (!row || busy) return;
    setBusy(true);
    setProblem(null);
    try {
      const answer = await send(row, verdict, reason.trim());
      notify.ok(verdict === "approve" ? approvedToast(row) : deniedToast(row));
      onDecided(answer);
      onClose();
    } catch (e) {
      // A refusal the lane worded is already the person's sentence.
      if (e instanceof DecideRefusal) {
        if (e.settled) setFinal(e.message);
        else setProblem(e.message);
        return;
      }
      const err = e as ApiError;
      // A 401 belongs to the session module, which signs the person out.
      if (err.status === 401) return;
      // These three answers are definite, so the question goes and the
      // dialog says what happened. Only a strazad that did not answer
      // leaves the button, because nothing was decided.
      if (err.status === 409) {
        const fresh = get ? await get(row.id).catch(() => null) : null;
        if (fresh) {
          onDecided(fresh);
          setFinal(alreadyDecided(fresh));
        } else {
          setFinal(refused(err));
        }
        return;
      }
      if (err.status === 410) {
        setFinal(WINDOW_CLOSED);
        return;
      }
      if (err.status === 403) {
        setFinal(notYours(bare(err.message)));
        return;
      }
      setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !busy) onClose(); }}>
      <AlertDialogContent className="sm:max-w-[560px]" data-decide={verdict}>
        <AlertDialogHeader>
          <AlertDialogTitle>{final ? finalTitle(verdict) : row ? decideTitle(verdict, row) : ""}</AlertDialogTitle>
          <AlertDialogDescription asChild>
            <span className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground" data-decide-body>
              {final ? final : row ? decideBody(verdict, row) : ""}
              {!final && row && <HelpTip label={verb} text={decideHelp(verdict, row)} />}
            </span>
          </AlertDialogDescription>
        </AlertDialogHeader>

        {!final && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="decide-reason">{REASON_LABEL}</Label>
            <Textarea
              id="decide-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder={REASON_PLACEHOLDER}
              maxLength={REASON_MAX}
            />
            <span className="text-[13px] text-muted-foreground">{REASON_HINT}</span>
          </div>
        )}

        {problem && <RefusedError subject={verb} message={problem} />}

        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{final ? CLOSE : CANCEL}</AlertDialogCancel>
          {!final && (
            <AlertDialogAction
              className={verdict === "deny" ? DANGER_ACTION : undefined}
              aria-busy={busy || undefined}
              data-verdict={verdict}
              onClick={(e) => { e.preventDefault(); void decide(); }}
            >
              {busy && <Loader2Icon className="animate-spin" />} {verb}
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
