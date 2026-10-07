// The Requests tab of the self-service page: the Approvals area's queue,
// its request dialog and its decide
// dialog, fed by the approver lane instead of the admin list. The rows are
// one person's, the calls of the agents they sponsor and the calls routed to
// a role they hold, beside the requests they raised themselves. A decision
// is signed by this browser's key over the challenge the server put on the
// row, so Deny and Approve render where there is a challenge and nowhere
// else. Until this browser is enrolled there is nothing to read, and the tab
// says what would fill it.
import * as React from "react";
import { CircleCheckIcon } from "lucide-react";
import { DecideRefusal } from "@/components/approval-decide";
import { type RequestsSource, RequestsTab as Queue } from "@/components/approvals-requests";
import { EmptyState } from "@/components/empty-state";
import { ApiError, type ApprovalRow, type Page } from "@/lib/api";
import type { Seat } from "@/lib/approval-model";
import { ROW_HINT, type Verdict } from "@/lib/approval-words";
import { bare } from "@/lib/say";
import { type ApproverError, type DecideBody, boundDevice, decide as postDecide, history, pending } from "./approver-api";
import { useSelf } from "./context";
import { type ApproverRow, type SelfRow, decidable, toSelfRow } from "./rows";
import { decideMessageWithReason } from "./sign";
import * as W from "./words";

// REASON_MAX_BYTES is the server's own cap on a decider's reason. The
// console's dialog counts characters, which is a different measure, so the
// refusal is made here before anything is signed.
const REASON_MAX_BYTES = 500;

// WAKE_QUIET_MS keeps the focus and visibility lanes from becoming a fetch
// storm while a person switches windows. A push the worker relayed is fresh
// news and never waits on it.
const WAKE_QUIET_MS = 4000;

// The count of the rows that wait follows every read, so a decision has
// nothing more to tell the shell.
const nothingMore = () => undefined;

// stateOf reads the final state off a conflict answer, the one fact the
// approver lane's 409 body carries.
function stateOf(data: unknown): string {
  const body = data as { state?: unknown } | null;
  return body && typeof body.state === "string" ? body.state : "";
}

// readError words a failed read of the queue for the console's error line.
// The two classes the shell already answers, a revoked device and a browser
// that is no longer enrolled, carry the status the queue reads as another
// module's business, so nothing is said twice. A rejected credential names
// its remedy, which lives on the This browser tab.
function readError(e: unknown): ApiError {
  const err = e as ApproverError;
  if (err.revoked || err.notEnrolled) return new ApiError(err.message, 401);
  if (err.unreachable) return new ApiError(err.message, 0, true);
  if (err.code === "token_invalid") return new ApiError(W.credentialRejected(bare(err.message)), 0);
  return new ApiError(err.message, err.status === 401 ? 0 : err.status || 0);
}

// decideError words a refused decision. The answers the console's dialog
// settles by itself keep their status, and the ones only this lane can read
// arrive already worded, since the approver surface answers a machine code
// where the admin API answers a sentence. A 401 never travels on: on this
// lane it is the credential, not the login session.
function decideError(e: unknown): unknown {
  if (e instanceof DecideRefusal) return e;
  const err = e as ApproverError;
  if (err.unreachable) return new ApiError(err.message, 0, true);
  if (err.status === 409) return new DecideRefusal(W.alreadySettled(stateOf(err.data)), true);
  if (err.status === 410 || err.status === 403) return new ApiError(err.message, err.status);
  if (err.status === 404) return new DecideRefusal(W.REQUEST_GONE, true);
  if (err.code === "challenge_rejected") return new DecideRefusal(W.SIGNATURE_REFUSED);
  if (err.revoked || err.notEnrolled) return new DecideRefusal(W.NOT_ENROLLED_NOW, true);
  if (err.status === 401) return new DecideRefusal(W.CREDENTIAL_REFUSED);
  return new ApiError(err.message, err.status || 0);
}

// decidedRow is the record a decision leaves behind. The lane answers the
// final state alone, so the rest of the decision is what this browser just
// did: its own clock, its own person, its own device and the words it
// signed. The next read replaces it with the server's own row.
function decidedRow(row: SelfRow, state: string, reason: string, user: string, deviceID: string): SelfRow {
  return {
    ...row,
    state,
    decidedAt: new Date().toISOString(),
    decidedBy: user,
    decidedByName: user,
    channel: "browser",
    decidedReason: reason || undefined,
    decidedDeviceId: deviceID,
    challenge: undefined,
  };
}

export function RequestsTab() {
  const ctx = useSelf();
  const { session, self, enrollment } = ctx;
  // The lane's calls outlive the render that built them, so the shared state
  // they touch is read through a ref and never captured.
  const shared = React.useRef(ctx);
  shared.current = ctx;
  // read is when the lane last heard from the server, which is what the wake
  // lanes throttle on.
  const read = React.useRef(0);
  // seen holds the ids the paged reads have handed over, so a re-read from
  // the top after a refused cursor repeats no row.
  const seen = React.useRef(new Set<string>());

  const user = (enrollment && enrollment.user && enrollment.user.username) || (self && self !== "error" ? self.username : "") || "";
  const grants = session.kind === "signed-in" ? session.grants : "";
  const deviceID = enrollment ? enrollment.deviceId : "";

  const source = React.useMemo<RequestsSource>(() => {
    // waiting merges the two pending scopes, read one after the other and
    // never raced, so an expired token heals once. The decidable copy wins,
    // because it is the one carrying the challenge this browser signs.
    const waiting = async () => {
      try {
        const rows = new Map<string, SelfRow>();
        for (const r of await pending<ApproverRow>("decidable")) rows.set(r.id, toSelfRow(r));
        for (const r of await pending<ApproverRow>("mine")) if (!rows.has(r.id)) rows.set(r.id, toSelfRow(r));
        const all = [...rows.values()];
        read.current = Date.now();
        shared.current.setCount("requests", all.filter((r) => r.state === "pending").length);
        return all;
      } catch (e) {
        shared.current.setCount("requests", null);
        throw readError(e);
      }
    };

    // page answers one page of the decided rows. The history feed takes a
    // cursor and nothing else, so the cursor is all this lane reads out of
    // the query the paged list builds.
    const page = async (q: string): Promise<Page<ApprovalRow>> => {
      const cursor = new URLSearchParams(q).get("cursor") || "";
      if (!cursor) seen.current.clear();
      let answer: { items: ApproverRow[]; next_cursor: string };
      let fromTop = false;
      try {
        answer = await history<ApproverRow>(cursor);
      } catch (e) {
        // A cursor is opaque and fails closed, so a server that no longer
        // knows one leaves a single honest move: read the newest page again.
        if (!cursor || (e as ApproverError).code !== "invalid_cursor") throw readError(e);
        answer = await history<ApproverRow>("").catch((again) => { throw readError(again); });
        fromTop = true;
      }
      read.current = Date.now();
      let items = (answer.items || []).map(toSelfRow);
      // That second read repeats the rows the list already holds, and one
      // row twice is one row too many, so the repeats are left out.
      if (fromTop) items = items.filter((r) => !seen.current.has(r.id));
      for (const r of items) seen.current.add(r.id);
      return { items, next_cursor: answer.next_cursor || "" };
    };

    const decide = async (row: ApprovalRow, verdict: Verdict, reason: string): Promise<ApprovalRow> => {
      const words = reason.trim();
      if (new TextEncoder().encode(words).length > REASON_MAX_BYTES) throw new DecideRefusal(W.REASON_TOO_LONG);
      const post = async (r: SelfRow) => {
        const dev = boundDevice();
        if (!dev) throw new DecideRefusal(W.NOT_ENROLLED_NOW, true);
        // Each attempt stamps its own clock, since a stale one is among the
        // answers that bring a retry here.
        const ts = Math.floor(Date.now() / 1000);
        const challenge = r.challenge || "";
        const signature = await dev.sign(await decideMessageWithReason(r.id, verdict, challenge, ts, words));
        const body: DecideBody = { request_id: r.id, verdict, challenge, signature, ts };
        if (words) body.reason = words;
        return postDecide<{ state?: string }>(body);
      };
      let answer: { state?: string };
      try {
        try {
          answer = await post(row as SelfRow);
        } catch (e) {
          // challenge_rejected is the server's one coarse code for every
          // decision it could not verify, a spent nonce among them, and it
          // is never a reason to distrust the key: fresh challenges, one
          // retry, then the refusal stands.
          if ((e as ApproverError).code !== "challenge_rejected") throw e;
          const again = (await pending<ApproverRow>("decidable")).map(toSelfRow).find((r) => r.id === row.id);
          if (!again || !again.challenge) throw e;
          answer = await post(again);
        }
      } catch (e) {
        throw decideError(e);
      }
      return decidedRow(row as SelfRow, answer.state || (verdict === "approve" ? "approved" : "denied"), words, user, deviceID);
    };

    return {
      waiting,
      page,
      decide,
      // The lane reads lists and no single record, so a conflict ends the
      // question on the server's own answer.
      get: null,
      // Who holds a role is an admin read, so no row here is marked stuck.
      roles: null,
      doors: grants ? (area, id) => window.location.assign("/console/" + area + "/" + encodeURIComponent(id)) : null,
      wake: (refetch) => {
        // The worker relays every push it shows, and a push is fresh news,
        // so it never waits on the throttle. Coming back to the window is a
        // guess that something changed, so it does.
        const onMessage = (ev: MessageEvent) => {
          const data = ev.data as { type?: string } | null;
          if (data && data.type === "straza-approvals") refetch();
        };
        const onWake = () => { if (Date.now() - read.current >= WAKE_QUIET_MS) refetch(); };
        const onVisible = () => { if (!document.hidden) onWake(); };
        const worker = navigator.serviceWorker;
        if (worker) worker.addEventListener("message", onMessage);
        window.addEventListener("focus", onWake);
        document.addEventListener("visibilitychange", onVisible);
        return () => {
          if (worker) worker.removeEventListener("message", onMessage);
          window.removeEventListener("focus", onWake);
          document.removeEventListener("visibilitychange", onVisible);
        };
      },
    };
  }, [grants, user, deviceID]);

  const seat = React.useMemo<Seat>(() => ({ user, roles: [] }), [user]);

  if (!enrollment) {
    const channels = self && self !== "error" ? self.enroll_channels || [] : null;
    const body = session.kind !== "signed-in" ? W.NOT_ENROLLED_SIGN_IN
      : !channels ? W.NOT_ENROLLED_ENABLE
      : channels.includes("browser") ? W.NOT_ENROLLED_ENABLE
      : channels.includes("mobile") ? W.NOT_ENROLLED_PHONE : W.NOT_ENROLLED_NO_ROLE;
    return <EmptyState icon={CircleCheckIcon} title={W.NOT_ENROLLED_TITLE}>{body}</EmptyState>;
  }

  return (
    <div className="flex flex-col gap-4">
      <Queue seat={seat} source={source} isMine={decidable} onChanged={nothingMore} />
      <p className="text-[13px] text-muted-foreground" data-queue-legend>{ROW_HINT + " " + W.WHOSE_TO_DECIDE}</p>
    </div>
  );
}
