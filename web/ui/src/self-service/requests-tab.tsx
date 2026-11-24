// The Requests tab of the self-service page and its home view: the
// Approvals area's queue and its request dialog, fed by the approver lane
// instead of the admin list. The rows are one person's, the calls of the
// agents they sponsor and the calls routed to a role they hold, beside the
// requests they raised themselves. A decision is signed by this browser's
// key over the challenge the server put on the row, so Deny and Approve
// render where there is a challenge and nowhere else. Until this browser is
// set up, a signed-in person's waiting requests are read with the sign-in
// session, which carries no challenge, so nothing is decided from them and
// no decided request is read: a request the person may decide offers the
// setup instead. Signed out and not set up there is nothing to read, and
// the tab says what would fill it. The address may name one request, which
// opens on its own.
import * as React from "react";
import { CircleCheckIcon } from "lucide-react";
import type { BeforeSetup } from "@/components/approval-dialog";
import { DecideRefusal } from "@/components/approval-decide";
import { type RequestsSource, RequestsTab as Queue } from "@/components/approvals-requests";
import { EmptyState } from "@/components/empty-state";
import { ApiError, type ApprovalRow, type Page, selfRequests } from "@/lib/api";
import type { Seat } from "@/lib/approval-model";
import type { Verdict } from "@/lib/approval-words";
import { bare } from "@/lib/say";
import { type ApproverError, type DecideBody, boundDevice, decide as postDecide, history, pending } from "./approver-api";
import { standingOf, useSelf } from "./context";
import { navigate, pathFor, useRequestID } from "./router";
import { type ApproverRow, type SelfRow, decidable, toSelfRow } from "./rows";
import { type Devices, approvesElsewhere, useDevices } from "./self-devices";
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

// signInRows reads one scope of the waiting requests with the sign-in
// session, the lane before this browser is set up. A server that does not
// have the read yet answers 404, which is an empty list.
async function signInRows(scope: "decidable" | "mine"): Promise<ApproverRow[]> {
  try {
    return (await selfRequests<ApproverRow>(scope)).requests || [];
  } catch (e) {
    if ((e as ApiError).status === 404) return [];
    throw e;
  }
}

// stateOf reads the final state off a conflict answer, the one fact the
// approver lane's 409 body carries.
function stateOf(data: unknown): string {
  const body = data as { state?: unknown } | null;
  return body && typeof body.state === "string" ? body.state : "";
}

// readError words a failed read of the queue as a whole sentence for the
// person, which the queue shows as it is. The two classes the shell already
// answers, a revoked device and a browser that is no longer set up, carry
// the status the queue reads as another module's business, so nothing is
// said twice. A rejected key names its remedy, which lives under Devices.
function readError(e: unknown): ApiError {
  const err = e as ApproverError;
  if (err.revoked || err.notEnrolled) return new ApiError(err.message, 401);
  if (err.unreachable) return new ApiError(W.NO_ANSWER, 0, true);
  if (err.code === "token_invalid") return new ApiError(W.keyRejected(bare(err.message)), 0);
  // A message that is only an HTTP status gives a person nothing to act on.
  if (/^HTTP \d+$/.test(err.message)) return new ApiError(W.NO_ANSWER, 0);
  return new ApiError(W.readSaid(err.message, W.readProblem), err.status === 401 ? 0 : err.status || 0);
}

// decideError words a refused decision. The answers the console's dialog
// settles by itself keep their status, and the ones only this lane can read
// arrive already worded, since the approver surface answers a machine code
// where the admin API answers a sentence. A 401 never travels on: on this
// lane it is the key, not the login session.
function decideError(e: unknown): unknown {
  if (e instanceof DecideRefusal) return e;
  const err = e as ApproverError;
  if (err.unreachable) return new DecideRefusal(W.DECIDE_UNCONFIRMED);
  if (err.status === 409) return new DecideRefusal(W.alreadySettled(stateOf(err.data)), true);
  if (err.status === 410 || err.status === 403) return new ApiError(err.message, err.status);
  if (err.status === 404) return new DecideRefusal(W.REQUEST_GONE, true);
  if (err.code === "challenge_rejected") return new DecideRefusal(W.SIGNATURE_REFUSED);
  if (err.revoked || err.notEnrolled) return new DecideRefusal(W.NOT_ENROLLED_NOW, true);
  if (err.status === 401) return new DecideRefusal(W.CREDENTIAL_REFUSED);
  return new DecideRefusal(W.decideRefused(bare(err.message)));
}

// HOME is the home view's headings and its link to the Requests tab.
const HOME = { waitingHead: W.WAITING_HEAD, decidedHead: W.DECIDED_HEAD, allLabel: W.ALL_REQUESTS, allHref: pathFor("requests"), onAll: () => navigate("requests") };

// WORDS are the page's own words for an empty Waiting filter, which needs
// no sentence under its title, and the time of the last read that worked.
const WORDS = { emptyTitle: W.NOTHING_WAITS, emptyBody: "", lastRead: W.lastRead };

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

// RequestsTab is the Requests tab, or with home the home view's part of it:
// the waiting requests and the newest decided ones in place of the filter,
// the search and the count. Home hands over its read of the person's
// devices as devices.
export function RequestsTab({ home = false, devices }: { home?: boolean; devices?: Devices }) {
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
  // empty says the sign-in lane's last read found nothing waiting, which the
  // tab says with the words of a browser that is not set up.
  const [empty, setEmpty] = React.useState(false);
  // mayDecide holds the ids the decidable scope answered last, the requests
  // the reader may decide, which before setup carry no challenge.
  const mayDecide = React.useRef(new Set<string>());
  // openID is the request the address names, which the Requests tab opens.
  const address = useRequestID();
  const openID = home ? "" : address;

  const user = (enrollment && enrollment.user && enrollment.user.username) || (self && self !== "error" ? self.username : "") || "";
  const grants = session.kind === "signed-in" ? session.grants : "";
  const deviceID = enrollment ? enrollment.deviceId : "";
  // Without Home's read the tab reads the devices itself, and only while
  // this browser is not set up, the one state whose words they change.
  const own = useDevices(!devices && !enrollment && session.kind === "signed-in");
  const elsewhere = approvesElsewhere(!!enrollment, devices || own);

  const source = React.useMemo<RequestsSource>(() => {
    const device = deviceID !== "";
    // waiting merges the two pending scopes, read one after the other and
    // never raced, so an expired token heals once. The decidable copy wins,
    // because it is the one carrying the challenge this browser signs. The
    // shell is handed the rows that are the reader's to decide: the ones this
    // browser can sign, or before setup the ones the decidable scope named.
    const waiting = async () => {
      try {
        const rows = new Map<string, SelfRow>();
        const named = new Set<string>();
        for (const r of device ? await pending<ApproverRow>("decidable") : await signInRows("decidable")) {
          rows.set(r.id, toSelfRow(r));
          named.add(r.id);
        }
        for (const r of device ? await pending<ApproverRow>("mine") : await signInRows("mine")) if (!rows.has(r.id)) rows.set(r.id, toSelfRow(r));
        const all = [...rows.values()];
        read.current = Date.now();
        mayDecide.current = named;
        shared.current.setWaiting(all.filter((r) => r.state === "pending" && (device ? decidable(r) : named.has(r.id))));
        if (!device) setEmpty(!all.some((r) => r.state === "pending"));
        return all;
      } catch (e) {
        setEmpty(false);
        // A 401 on the sign-in lane has already ended the session, so it
        // keeps its status and the queue says nothing of it.
        if (!device && (e as ApiError).status === 401) {
          shared.current.setWaiting(null);
          throw e;
        }
        // The queue shows every failure but a 401, which another part of the
        // page answers, so the shell hears of a failure the queue shows.
        const err = readError(e);
        shared.current.setWaiting(err.status === 401 ? null : "error");
        throw err;
      }
    };

    // page answers one page of the decided rows. The history feed takes a
    // cursor and nothing else, so the cursor is all this lane reads out of
    // the query the paged list builds.
    const page = async (q: string): Promise<Page<ApprovalRow>> => {
      if (!device) return { items: [], next_cursor: "" };
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
      words: WORDS,
    };
  }, [grants, user, deviceID]);

  const seat = React.useMemo<Seat>(() => ({ user, roles: [] }), [user]);

  // Before setup the reader may decide what the decidable scope named, and
  // a request of theirs offers the setup dialog in place of an answer. A
  // person who approves from another device is told to answer there.
  const isMine = React.useCallback((r: ApprovalRow) => (deviceID ? decidable(r) : mayDecide.current.has(r.id)), [deviceID]);
  // An account that approves from a phone only is offered no setup of a
  // browser, so its words name the phone and the dialog has no button.
  const standing = standingOf({ session, self });
  const phoneOnly = standing.kind === "read" && !standing.browser && standing.mobile;
  // A phone is named only where the account may add one.
  const browserOnly = standing.kind === "read" && !standing.mobile;
  const beforeSetup = React.useMemo<BeforeSetup | undefined>(() => (deviceID ? undefined : {
    onSetUp: () => shared.current.setSheet("enable", true),
    words: phoneOnly
      ? (elsewhere ? { row: W.ANSWER_ON_PHONE, line: W.ANSWER_ON_PHONE_LINE, button: "" } : { row: W.ADD_PHONE_TO_ANSWER, line: W.ANSWER_NEEDS_PHONE, button: "" })
      : elsewhere
        ? { row: W.ANSWER_ON_DEVICE, line: W.ANSWER_ON_DEVICE_LINE, button: W.SETUP_BROWSER }
        : { row: W.SETUP_TO_ANSWER, line: browserOnly ? W.ANSWER_NEEDS_BROWSER : W.ANSWER_NEEDS_DEVICE, button: W.SETUP_BROWSER },
  }), [deviceID, elsewhere, phoneOnly, browserOnly]);
  // Closing the request the address names takes the name out of it.
  const onClose = React.useCallback(() => { if (openID) navigate("requests"); }, [openID]);

  const notSetUp = standing.kind === "signed-out"
    ? <EmptyState icon={CircleCheckIcon} title={W.SIGN_IN_FOR_REQUESTS}>{W.SIGN_IN_FOR_REQUESTS_BODY}</EmptyState>
    : (
      <EmptyState icon={CircleCheckIcon} title={W.NOTHING_WAITS_CARD}>
        {standing.kind !== "read" || standing.browser ? (elsewhere ? W.WAITS_SHOW_HERE_DEVICE : browserOnly ? W.WAITS_SHOW_HERE_BROWSER : W.WAITS_SHOW_HERE) : standing.mobile ? W.WAITS_SHOW_HERE_PHONE : W.NOT_ENROLLED_NO_ROLE}
      </EmptyState>
    );
  if (!enrollment && session.kind !== "signed-in") return notSetUp;

  // An empty sign-in lane keeps its queue mounted and hidden, so the poll
  // goes on and the first request that arrives shows. Home's first card
  // already says why nothing shows there.
  const hide = !enrollment && empty;
  return (
    <div className="flex flex-col gap-4">
      {hide && !home && notSetUp}
      <div className="flex flex-col gap-4" hidden={hide}>
        <Queue
          seat={seat}
          source={source}
          isMine={isMine}
          openID={openID}
          onClose={onClose}
          beforeSetup={beforeSetup}
          onChanged={nothingMore}
          page="self-service"
          here={deviceID}
          home={home ? HOME : undefined}
        />
      </div>
    </div>
  );
}
