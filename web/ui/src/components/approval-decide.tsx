import * as React from "react";
import type { ApiError, ApprovalRow } from "@/lib/api";
import { notify } from "@/lib/notify";
import { bare, refused } from "@/lib/say";
import { type Verdict, WINDOW_CLOSED, alreadyDecided, approvedToast, deniedToast, notYours } from "@/lib/approval-words";

// The decision of one request, made from inside the request dialog: the
// optional reason the agent and the audit record read, the verdict on its
// way, a refusal that leaves the buttons, and a server answer that settles
// the request for good and ends the decision, so Close is the only button
// left.

// DecideRefusal is a refusal the decide lane already worded for the person,
// such as a reason its server would refuse for its length: the sentence is
// shown as it is, since nothing reached the server for us to quote. settled
// says the request is settled for good, so the decision ends and Close is
// the only button left. The admin lane never throws one.
export class DecideRefusal extends Error {
  settled: boolean;
  constructor(message: string, settled = false) {
    super(message);
    this.settled = settled;
  }
}

// Answer is a refusal or a final answer with the verdict it answered.
export type Answer = { verdict: Verdict; message: string };

type Lane = {
  // decide records the verdict and answers the fresh record.
  decide: (row: ApprovalRow, verdict: Verdict, reason: string) => Promise<ApprovalRow>;
  // get re-reads one record after a 409, or null where the lane has no
  // single read and the decision must settle on the refusal alone.
  get: ((id: string) => Promise<ApprovalRow | null>) | null;
  // onDecided hands the fresh record back, both after a decision of ours
  // and after a decision someone else made first.
  onDecided: (row: ApprovalRow) => void;
  // onLanded closes the dialog once a decision of ours landed.
  onLanded: () => void;
};

// useDecide holds one request's decision. busy is the verdict on its way,
// problem a refusal the person may answer again, and final the answer that
// ends the decision.
export function useDecide(row: ApprovalRow, { decide: send, get, onDecided, onLanded }: Lane) {
  const [reason, setReason] = React.useState("");
  const [busy, setBusy] = React.useState<Verdict | null>(null);
  const [problem, setProblem] = React.useState<Answer | null>(null);
  const [final, setFinal] = React.useState<Answer | null>(null);

  // Every request starts clean, since the reason, the refusal and the final
  // sentence belong to the one request they were given on.
  React.useEffect(() => {
    setReason("");
    setBusy(null);
    setProblem(null);
    setFinal(null);
  }, [row.id]);

  const decide = async (verdict: Verdict) => {
    if (busy) return;
    setBusy(verdict);
    setProblem(null);
    const end = (message: string) => setFinal({ verdict, message });
    try {
      const answer = await send(row, verdict, reason.trim());
      notify.ok(verdict === "approve" ? approvedToast(row) : deniedToast(row));
      onDecided(answer);
      onLanded();
    } catch (e) {
      // A refusal the lane worded is already the person's sentence.
      if (e instanceof DecideRefusal) {
        if (e.settled) end(e.message);
        else setProblem({ verdict, message: e.message });
        return;
      }
      const err = e as ApiError;
      // A 401 belongs to the session module, which signs the person out.
      if (err.status === 401) return;
      // These three answers are definite, so the decision ends and the
      // dialog says what happened. Only a server that did not answer leaves
      // the buttons, because nothing was decided.
      if (err.status === 409) {
        const fresh = get ? await get(row.id).catch(() => null) : null;
        if (fresh) {
          onDecided(fresh);
          end(alreadyDecided(fresh));
        } else {
          end(refused(err));
        }
        return;
      }
      if (err.status === 410) {
        end(WINDOW_CLOSED);
        return;
      }
      if (err.status === 403) {
        end(notYours(bare(err.message)));
        return;
      }
      setProblem({ verdict, message: refused(err) });
    } finally {
      setBusy(null);
    }
  };

  return { reason, setReason, busy, problem, final, decide };
}
