import * as React from "react";
import { Button } from "@/components/ui/button";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { SessionActivity } from "@/components/session-activity";
import { SessionGlance } from "@/components/session-glance";
import { SessionHead } from "@/components/session-head";
import { SessionHealth } from "@/components/session-health";
import { SessionRecording } from "@/components/session-recording";
import { SessionRevoke } from "@/components/session-revoke";
import { type ApiError, type SessionRow, getSession, listApprovals, listApps, listAudit, listUsers, query, sessionTranscript } from "@/lib/api";
import { navigate } from "@/lib/router";
import { RELOAD_NOW } from "@/lib/role-words";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { adminAreas, snapshot } from "@/lib/session";
import { ACTIVITY_LIMIT, type ActivityFilter, countOf, ofSession, sessionActivity, sessionQuery } from "@/lib/session-activity";
import { BACK_TO_SESSIONS, NO_SUCH_SESSION, READING_SESSION, SUBJECT_ACTIVITY, SUBJECT_RECORDING, SUBJECT_SESSION, cutID } from "@/lib/session-words";
import { dataOf, useRead } from "@/lib/user-read";

// The page of one session at sessions/<id>: the head with Revoke session,
// the glance tiles, the activity on the left, and the Health and Recording
// cards on the right. The session is read first and every other read waits
// for it. The activity is a search of the whole audit chain, so it is read
// when the page opens, on Reload and after a revoke, never on a timer.

const route = routeByKey("sessions");

type State = { kind: "loading" } | { kind: "missing" } | { kind: "error"; message: string } | { kind: "ready"; row: SessionRow; problem: string | null };

export function SessionPage({ id }: { id: string }) {
  const areas = adminAreas();
  const may = (area: string) => areas === null || !!areas[area];
  const [nonce, setNonce] = React.useState(0);
  const [reloads, setReloads] = React.useState(0);
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [filter, setFilter] = React.useState<ActivityFilter>("all");
  const [asking, setAsking] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    getSession(id).then(
      (row) => { if (alive) setState({ kind: "ready", row, problem: null }); },
      (e) => {
        const err = e as ApiError;
        if (!alive || err.status === 401) return;
        if (err.status === 404) { setState({ kind: "missing" }); return; }
        const message = readFailed(SUBJECT_SESSION, err);
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
    return () => { alive = false; };
  }, [id, nonce]);

  const row = state.kind === "ready" ? state.row : null;
  const sponsor = row ? row.sponsor || "" : "";
  // Both nonces only grow, so their sum moves whenever either does.
  const activityNonce = nonce + reloads;
  const audit = useRead(!!row && may("audit"), () => listAudit(query({ q: sessionQuery(id), limit: ACTIVITY_LIMIT, order: "desc" })), SUBJECT_ACTIVITY, activityNonce);
  const pending = useRead(!!row && may("approvals"), () => listApprovals("pending"), SUBJECT_ACTIVITY, activityNonce);
  // The registered servers tell a server on a person's machine apart only
  // when the list is whole, which it is for a full seat alone.
  const appsRead = useRead(!!row && snapshot()?.grants === "full", () => listApps(), SUBJECT_ACTIVITY, 0);
  const transcript = useRead(!!row && may("transcripts"), () => sessionTranscript(id), SUBJECT_RECORDING, nonce);
  const sponsorRead = useRead(!!sponsor && may("identity"), () => listUsers(query({ q: sponsor, limit: 20 })), SUBJECT_SESSION, 0);

  const records = dataOf(audit);
  const pendingRows = dataOf(pending);
  const mine = React.useMemo(() => (records ? ofSession(records, id) : null), [records, id]);
  const rows = React.useMemo(() => (records ? sessionActivity(records, pendingRows ? pendingRows.approvals || [] : null, id) : null), [records, pendingRows, id]);

  if (state.kind !== "ready" || !row) {
    return (
      <>
        <PageHead label={cutID(id)} crumbs={[{ label: route.label, to: "sessions" }]} title={route.label} description="" />
        <div className="flex flex-col items-start gap-3 px-6 py-5">
          {state.kind === "loading" && <p className="m-0 text-dense text-muted-foreground" role="status">{READING_SESSION}</p>}
          {state.kind === "missing" && <p className="m-0 text-dense text-text-2" data-no-such-session>{NO_SUCH_SESSION}</p>}
          {state.kind === "missing" && <Button variant="link" className="h-auto p-0 text-link" onClick={() => navigate("sessions")}>{BACK_TO_SESSIONS}</Button>}
          {state.kind === "error" && <FetchError subject={SUBJECT_SESSION} detail={state.message} />}
          {state.kind === "error" && <Button variant="outline" size="sm" onClick={() => { setState({ kind: "loading" }); setNonce((n) => n + 1); }}>{RELOAD_NOW}</Button>}
        </div>
      </>
    );
  }

  const own = snapshot()?.sessionID === row.id;
  const sponsorID = (dataOf(sponsorRead)?.items || []).find((u) => u.username === sponsor)?.id || null;
  // The allowed and denied counts stand only for a whole read.
  const whole = rows && records && records.length < ACTIVITY_LIMIT ? rows : null;
  const turns = dataOf(transcript);
  const apps = dataOf(appsRead);

  return (
    <>
      <SessionHead row={row} own={own} sponsorID={sponsorID} onRevoke={() => setAsking(true)} />
      <div className="flex min-w-0 flex-col gap-4 px-6 py-5">
        {state.problem && <FetchError subject={SUBJECT_SESSION} detail={state.problem} />}
        <SessionGlance
          allowed={whole ? countOf(whole, "allowed") : null}
          waiting={row.waiting || 0}
          denied={whole ? countOf(whole, "denied") : null}
          turns={turns ? (turns.turns || []).length : null}
          onFilter={setFilter}
          onTurns={() => navigate("transcripts", [id])}
        />
        <div className="grid min-w-0 grid-cols-1 items-start gap-4 lg:grid-cols-3">
          <div className="min-w-0 lg:col-span-2">
            <SessionActivity
              id={id}
              allowed={may("audit")}
              approvals={may("approvals")}
              read={audit}
              rows={rows}
              records={mine ? mine.length : 0}
              full={!!records && records.length >= ACTIVITY_LIMIT}
              filter={filter}
              onFilter={setFilter}
              onReload={() => setReloads((n) => n + 1)}
              registered={apps ? apps.map((a) => a.name) : null}
            />
          </div>
          <div className="flex min-w-0 flex-col gap-4">
            <SessionHealth row={row} />
            <SessionRecording id={id} allowed={may("transcripts")} read={transcript} />
          </div>
        </div>
      </div>
      <SessionRevoke id={id} name={row.username || cutID(row.user_id)} own={own} open={asking} onOpenChange={setAsking} onRevoked={() => setNonce((n) => n + 1)} />
    </>
  );
}
