import * as React from "react";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { Attention, type Waiting, attentionItems } from "@/components/overview-attention";
import { Changed } from "@/components/overview-changed";
import { Decisions, type HourRead, Stopped } from "@/components/overview-decisions";
import { Live, readFollow, saveFollow } from "@/components/overview-live";
import { type ChainRead } from "@/components/overview-parts";
import { AccessGuide, AuditStatus, ConfigurationNote, Inventory } from "@/components/overview-summary";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Posture } from "@/components/overview-posture";
import { Setup } from "@/components/overview-setup";
import { SignedIn } from "@/components/overview-signed-in";
import { Tiles } from "@/components/overview-tiles";
import {
  type ApiError,
  type AppRow,
  type ApprovalRow,
  type AuditRow,
  type ConfigAnswer,
  type DraftSummary,
  type OverviewAnswer,
  type Page,
  type RoleRow,
  type SessionRow,
  type SinkRow,
  getConfig,
  listApprovals,
  listApps,
  listAudit,
  listRoles,
  listSessions,
  listSinks,
  overview,
  query,
} from "@/lib/api";
import { canHash, verifyChain } from "@/lib/chain";
import { listDrafts } from "@/lib/drafts-api";
import { proposerWords } from "@/lib/drafts-words";
import {
  DECIDED_PANEL,
  LAST_HOUR_READING,
  LAST_HOUR_UNREAD,
  LAST_HOUR_UNSUPPORTED,
  OVERVIEW_TABS,
  READING_OVERVIEW,
  SUBJECT_APPROVALS,
  SUBJECT_CHAIN,
  SUBJECT_CHANGES,
  SUBJECT_CONFIG,
  SUBJECT_DRAFTS,
  SUBJECT_LAST_HOUR,
  SUBJECT_LIVE,
  SUBJECT_OVERVIEW,
  SUBJECT_ROLES,
  SUBJECT_SERVERS,
  SUBJECT_SESSIONS,
  SUBJECT_SINKS,
  type SetupStep,
  VALUE_UNAVAILABLE,
  configRows,
  isAdminHarness,
  readLine,
  refreshFailed,
  seatLine,
  summaryLastRead,
  summaryUnread,
} from "@/lib/config-words";
import { version as readVersion } from "@/lib/public";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { relTimeText } from "@/lib/words";

// waitingOf reads one page of the open drafts as the attention line's
// count and the oldest one's proposer.
function waitingOf(page: Page<DraftSummary>): Waiting {
  const items = page.items || [];
  const oldest = items.reduce<DraftSummary | null>((o, d) => (!o || d.created_at < o.created_at ? d : o), null);
  return { count: items.length, more: !!page.next_cursor, who: oldest ? proposerWords(oldest.proposer, "", oldest.source).name : "", at: oldest ? oldest.created_at : "" };
}

// Overview separates daily decisions and attention from activity and system
// details. Each read keeps its permissions, last successful value and timestamp
// across tabs; a forbidden read clears only the affected data.

const route = routeByKey("overview");

// The page re-reads every 30 s, the interval the head's line names.
const POLL_MS = 30000;
// The last hour view re-reads its minutes every 5 s while it is on screen.
const HOUR_POLL_MS = 5000;
// The chain window this browser re-hashes on every read. The Audit tile's
// sentence names the same 25 records.
const CHAIN_WINDOW = 25;
const LIVE_WINDOW = 20;
const SESSION_WINDOW = 10;
const CHANGED_WINDOW = 6;
// What changed reads the control-plane record types. The endpoint's type
// parameter applies after its window, so a busy chain would answer empty;
// its q parameter is a substring the store matches over the record text
// before the limit, and the type key of a stored record reads verbatim
// as "type":"straza.audit.admin", so each type is one q read and the
// screen merges the answers by sequence.
const CHANGE_TYPES = ["straza.audit.admin", "straza.identity", "straza.policy"];
const CHANGE_SCAN = 200;
const typeNeedle = (type: string) => '"type":"' + type;

type Read<T> = { value: T | null; denied: boolean; problem: string };

// fold reads one settled answer: the value, the seat that may not ask, or
// the sentence a failed read prints. A 401 is the session lane's to
// handle, so it says nothing here.
function fold<T>(r: PromiseSettledResult<T>, subject: string): Read<T> {
  if (r.status === "fulfilled") return { value: r.value, denied: false, problem: "" };
  const err = r.reason as ApiError;
  if (err.status === 401) return { value: null, denied: false, problem: "" };
  if (err.status === 403) return { value: null, denied: true, problem: "" };
  return { value: null, denied: false, problem: readFailed(subject, err) };
}

// keep holds the last good data behind an error: a read that failed leaves
// what the page already showed, and a seat that may not read drops it.
function keep<T>(previous: T | null, read: Read<T>): T | null {
  if (read.value !== null) return read.value;
  return read.denied ? null : previous;
}

// foldChanges merges the three type reads into the newest records. They
// share the audit area, so one refusal refuses all three.
function foldChanges(results: PromiseSettledResult<AuditRow[]>[]): Read<AuditRow[]> {
  const bySeq = new Map<number, AuditRow>();
  let denied = false;
  let problem = "";
  let answered = false;
  for (const r of results) {
    const read = fold(r, SUBJECT_CHANGES);
    if (read.value) {
      answered = true;
      for (const row of read.value) bySeq.set(row.seq, row);
    }
    if (read.denied) denied = true;
    if (read.problem && !problem) problem = read.problem;
  }
  if (denied) return { value: null, denied: true, problem: "" };
  if (!answered) return { value: null, denied: false, problem };
  return { value: [...bySeq.values()].sort((a, b) => b.seq - a.seq).slice(0, CHANGED_WINDOW), denied: false, problem };
}

// readChain takes the newest records and re-hashes them here, the way the
// Audit screen does, so the word intact is proven in this browser.
async function readChain(): Promise<{ head: number; broken: number }> {
  const batch = await listAudit(query({ order: "desc", limit: CHAIN_WINDOW }));
  const rows = (batch || []).slice().reverse();
  if (!rows.length) return { head: 0, broken: 0 };
  const head = rows[rows.length - 1].seq;
  if (!canHash) return { head, broken: 0 };
  const verdict = await verifyChain(rows, null);
  return { head, broken: verdict.ok ? 0 : verdict.brokenSeq };
}

type Problem = { subject: string; detail: string; lastRead?: Date | null };

type State = {
  answer: OverviewAnswer | null;
  config: ConfigAnswer | null;
  configDenied: boolean;
  approvals: ApprovalRow[] | null;
  approvalsDenied: boolean;
  drafts: Waiting | null;
  apps: AppRow[] | null;
  sinks: SinkRow[] | null;
  sessions: SessionRow[] | null;
  sessionsDenied: boolean;
  changed: AuditRow[] | null;
  auditDenied: boolean;
  chain: ChainRead;
  lastRead: Date | null;
  completed: boolean;
  answerDenied: boolean;
  readTimes: Record<string, Date>;
  problems: Problem[];
};

const START: State = {
  answer: null,
  config: null,
  configDenied: false,
  approvals: null,
  approvalsDenied: false,
  drafts: null,
  apps: null,
  sinks: null,
  sessions: null,
  sessionsDenied: false,
  changed: null,
  auditDenied: false,
  chain: { word: "unknown", seq: 0 },
  lastRead: null,
  completed: false,
  answerDenied: false,
  readTimes: {},
  problems: [],
};

type LiveState = { rows: AuditRow[] | null; base: number | null; denied: boolean; problem: string };

const LIVE_START: LiveState = { rows: null, base: null, denied: false, problem: "" };

// HourState is the last hour view: null while the panel shows the day.
// problem is the sentence a failed read prints at the top of the page, and
// at is when the minutes on screen were read.
type HourState = HourRead & { problem: string; at: Date | null };

const HOUR_START: HourState = { minutes: null, note: LAST_HOUR_READING, problem: "", at: null };

export function Overview() {
  const [state, setState] = React.useState<State>(START);
  const [roles, setRoles] = React.useState<RoleRow[] | null>(null);
  const [rolesProblem, setRolesProblem] = React.useState("");
  const [follow, setFollow] = React.useState<boolean>(readFollow);
  const [live, setLive] = React.useState<LiveState>(LIVE_START);
  const [hour, setHour] = React.useState<HourState | null>(null);
  const [strazad, setStrazad] = React.useState("");
  const [tick, setTick] = React.useState(0);
  const [tab, setTab] = React.useState("summary");
  const systemTrigger = React.useRef<HTMLButtonElement>(null);
  const openSystem = () => { setTab("system"); systemTrigger.current?.focus(); };

  // The version the sidebar prints is the one a box's client build is
  // measured against, so it is read from the same place, once.
  React.useEffect(() => {
    let alive = true;
    void readVersion().then((v) => { if (alive && v) setStrazad(v.version); });
    return () => { alive = false; };
  }, []);

  const readAll = React.useCallback(async () => {
    const changeQuery = (type: string) => listAudit(query({ order: "desc", limit: CHANGE_SCAN, q: typeNeedle(type) }));
    const [ovR, cfgR, apR, chainR, sinkR, sessR, appsR, adminR, identityR, policyR, draftsR] = await Promise.allSettled([
      overview(),
      getConfig(),
      listApprovals("pending"),
      readChain(),
      listSinks(),
      listSessions(query({ status: "active", sort: "last_seen", order: "desc", limit: SESSION_WINDOW })),
      listApps(),
      changeQuery(CHANGE_TYPES[0]),
      changeQuery(CHANGE_TYPES[1]),
      changeQuery(CHANGE_TYPES[2]),
      listDrafts("state=open&limit=200"),
    ]);

    const ov = fold(ovR, SUBJECT_OVERVIEW);
    const cfg = fold(cfgR, SUBJECT_CONFIG);
    const approvals = fold(apR, SUBJECT_APPROVALS);
    const chain = fold(chainR, SUBJECT_CHAIN);
    const sinks = fold(sinkR, SUBJECT_SINKS);
    const sessions = fold(sessR, SUBJECT_SESSIONS);
    const apps = fold(appsR, SUBJECT_SERVERS);
    const changes = foldChanges([adminR, identityR, policyR]);
    const drafts = fold(draftsR, SUBJECT_DRAFTS);

    const problems: Problem[] = [
      { subject: SUBJECT_OVERVIEW, detail: ov.problem },
      { subject: SUBJECT_CONFIG, detail: cfg.problem },
      { subject: SUBJECT_APPROVALS, detail: approvals.problem },
      { subject: SUBJECT_CHAIN, detail: chain.problem },
      { subject: SUBJECT_SINKS, detail: sinks.problem },
      { subject: SUBJECT_SESSIONS, detail: sessions.problem },
      { subject: SUBJECT_SERVERS, detail: apps.problem },
      { subject: SUBJECT_CHANGES, detail: changes.problem },
      { subject: SUBJECT_DRAFTS, detail: drafts.problem },
    ].filter((p) => p.detail);

    setState((prev) => ({
      answer: keep(prev.answer, ov),
      config: keep(prev.config, cfg),
      configDenied: cfg.denied,
      approvals: approvals.value ? approvals.value.approvals || [] : approvals.denied ? null : prev.approvals,
      approvalsDenied: approvals.denied,
      drafts: drafts.value ? waitingOf(drafts.value) : drafts.denied ? null : prev.drafts,
      apps: keep(prev.apps, apps),
      sinks: keep(prev.sinks, sinks),
      sessions: sessions.value ? sessions.value.items || [] : sessions.denied ? null : prev.sessions,
      sessionsDenied: sessions.denied,
      changed: keep(prev.changed, changes),
      auditDenied: chain.denied || changes.denied,
      chain: chain.denied
        ? { word: "seat", seq: 0 }
        : !chain.value
          ? prev.chain
          : !canHash
            ? { word: "unverified", seq: chain.value.head }
            : chain.value.broken
              ? { word: "broken", seq: chain.value.broken }
              : { word: "intact", seq: chain.value.head },
      lastRead: ov.denied ? null : ov.value !== null ? new Date() : prev.lastRead,
      completed: true,
      answerDenied: ov.denied,
      readTimes: Object.fromEntries([
        [SUBJECT_OVERVIEW, ov], [SUBJECT_CONFIG, cfg], [SUBJECT_APPROVALS, approvals], [SUBJECT_CHAIN, chain],
        [SUBJECT_SINKS, sinks], [SUBJECT_SESSIONS, sessions], [SUBJECT_SERVERS, apps], [SUBJECT_CHANGES, changes], [SUBJECT_DRAFTS, drafts],
      ].flatMap(([subject, read]) => {
        const key = subject as string;
        const r = read as Read<unknown>;
        const stamp = r.denied ? undefined : r.value !== null && !r.problem ? new Date() : prev.readTimes[key];
        return stamp ? [[key, stamp]] : [];
      })),
      problems,
    }));
  }, []);

  React.useEffect(() => { void readAll(); }, [readAll, tick]);

  React.useEffect(() => {
    const t = setInterval(() => { if (!document.hidden) setTick((n) => n + 1); }, POLL_MS);
    return () => clearInterval(t);
  }, []);

  const readLive = React.useCallback(async () => {
    try {
      const batch = await listAudit(query({ order: "desc", limit: LIVE_WINDOW }));
      const rows = batch || [];
      setLive((prev) => ({ rows, base: prev.base === null && rows.length ? rows[0].seq : prev.base, denied: false, problem: "" }));
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setLive((prev) => ({ ...prev, denied: err.status === 403, problem: err.status === 403 ? "" : readFailed(SUBJECT_LIVE, err) }));
    }
  }, []);

  // The live window rides the same tick, and turning the switch on reads at
  // once rather than waiting for the next one.
  React.useEffect(() => {
    if (!follow) return;
    void readLive();
  }, [follow, readLive, tick]);

  const onFollow = (on: boolean) => {
    saveFollow(on);
    setFollow(on);
    if (!on) setLive(LIVE_START);
  };

  // readHour asks for the minutes. An answer that lands after the view went
  // back to the day is dropped, a failed refresh keeps the minutes on
  // screen, and a seat that may not read drops them.
  const readHour = React.useCallback(async () => {
    try {
      const minutes = (await overview("hour")).decisions?.minutes;
      setHour((prev) => prev && (minutes ? { minutes, note: "", problem: "", at: new Date() } : { ...HOUR_START, note: LAST_HOUR_UNSUPPORTED }));
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setHour((prev) => prev && (err.status === 403
        ? { ...HOUR_START, note: seatLine(DECIDED_PANEL, "config:read") }
        : { ...prev, note: LAST_HOUR_UNREAD, problem: readFailed(SUBJECT_LAST_HOUR, err) }));
    }
  }, []);

  // The last hour is read at once when the view comes on screen, then on its
  // own interval, which ends when the view or its tab goes and skips a
  // hidden page.
  const hourOn = hour !== null && tab === "summary";
  React.useEffect(() => {
    if (!hourOn) return;
    void readHour();
    const t = setInterval(() => { if (!document.hidden) void readHour(); }, HOUR_POLL_MS);
    return () => clearInterval(t);
  }, [hourOn, readHour]);

  const answer = state.answer;
  const rows = state.config ? configRows(state.config) : null;
  const governed = state.sessions ? state.sessions.filter((s) => !isAdminHarness(s.harness)) : null;

  // Day zero: the setup list takes the place of Needs attention while no
  // agent has ever checked in and the first three steps are not done.
  const marks: Partial<Record<SetupStep["key"], boolean>> = {
    server: answer?.apps?.total === undefined ? undefined : answer.apps.total > 0,
    role: roles ? roles.some((r) => r.kind !== "straza") : undefined,
    policy: answer?.policies?.active === undefined ? undefined : answer.policies.active > 0,
    floor: state.config?.governance?.min_attestation === undefined ? undefined : state.config.governance.min_attestation === "managed",
  };
  const showSetup = governed !== null && governed.length === 0 && answer?.apps?.total !== undefined && answer?.policies?.active !== undefined && !(marks.server && marks.role && marks.policy);

  // The roles list is read only while the setup list is up, because the
  // only thing it answers there is whether a role exists yet.
  React.useEffect(() => {
    if (!showSetup) return;
    void listRoles().then(
      (rs) => { setRoles(rs || []); setRolesProblem(""); },
      (e) => {
        const err = e as ApiError;
        // A seat that may not read roles leaves the step unmarked; any
        // other failure says so above the list.
        setRolesProblem(err.status === 401 || err.status === 403 ? "" : readFailed(SUBJECT_ROLES, err));
      },
    );
  }, [showSetup, tick]); // eslint-disable-line react-hooks/exhaustive-deps

  const problems = [
    ...state.problems,
    ...(rolesProblem && showSetup ? [{ subject: SUBJECT_ROLES, detail: rolesProblem }] : []),
    ...(live.problem ? [{ subject: SUBJECT_LIVE, detail: live.problem }] : []),
    ...(hour?.problem ? [{ subject: SUBJECT_LAST_HOUR, detail: hour.problem, lastRead: hour.at }] : []),
  ];
  const fresh = live.rows && live.base !== null ? live.rows.filter((r) => r.seq > (live.base as number)).length : 0;
  const stale = (subject: string) => state.problems.some((p) => p.subject === subject) && state.readTimes[subject]
    ? relTimeText(state.readTimes[subject].toISOString()) : undefined;
  const attentionProps = { chain: state.chain, apps: state.apps, approvals: state.approvals, drafts: state.drafts, push: answer?.push, sinks: state.sinks, relaxed: [] };
  const hasAttention = attentionItems(attentionProps).length > 0;
  const attention = <Attention {...attentionProps} compact onDetails={tab === "system" ? undefined : openSystem} incomplete={!rows || rows.some((r) => r.value === VALUE_UNAVAILABLE) || problems.length > 0} />;

  if (!state.completed) {
    return (
      <>
        <PageHead label={route.label} description="" />
        <p className="px-6 py-5 text-sm text-muted-foreground">{READING_OVERVIEW}</p>
      </>
    );
  }

  return (
    <div className="overview-page" data-overview>
      <PageHead label={route.label} description="" actions={<AccessGuide />} />
      <div className="overview-content">
        <Tabs value={tab} onValueChange={setTab}>
          <div className="overview-tabs-row">
            <TabsList variant="line" aria-label={OVERVIEW_TABS.label}>
              <TabsTrigger value="summary">{OVERVIEW_TABS.summary}</TabsTrigger>
              <TabsTrigger value="activity">{OVERVIEW_TABS.activity}</TabsTrigger>
              <TabsTrigger ref={systemTrigger} value="system">{OVERVIEW_TABS.system}</TabsTrigger>
            </TabsList>
            <span className="overview-read-line" data-read-line>{state.lastRead ? (stale(SUBJECT_OVERVIEW) ? summaryLastRead(stale(SUBJECT_OVERVIEW) || "") : readLine(relTimeText(state.lastRead.toISOString()))) : summaryUnread(state.answerDenied)}</span>
          </div>
          {problems.length > 0 && <details className="overview-read-errors" data-read-errors>
            <summary>{refreshFailed(problems.length)}</summary>
            {problems.map((p) => <FetchError key={p.subject} subject={p.subject} detail={p.detail} lastRead={p.lastRead || state.readTimes[p.subject] || null} />)}
          </details>}
          <TabsContent value={tab}>
            {tab === "system" ? <div className="overview-system">
              {attention}
              <Tiles answer={answer} approvals={state.approvals} approvalsDenied={state.approvalsDenied} chain={state.chain} denied={state.answerDenied} stale={{ summary: stale(SUBJECT_OVERVIEW), approvals: stale(SUBJECT_APPROVALS), chain: stale(SUBJECT_CHAIN) }} />
              {stale(SUBJECT_CONFIG) && <p className="overview-stale">Configuration last read {stale(SUBJECT_CONFIG)}. Values may be outdated.</p>}
              <Posture rows={rows} denied={state.configDenied} expanded />
              {stale(SUBJECT_SESSIONS) && <p className="overview-stale">Sessions last read {stale(SUBJECT_SESSIONS)}. Values may be outdated.</p>}
              <SignedIn rows={state.sessions} denied={state.sessionsDenied} strazad={strazad} expanded />
            </div> : tab === "activity" ? <div className="overview-activity">
              {state.chain.word === "broken" && attention}
              <Changed rows={state.changed} denied={state.auditDenied} stale={stale(SUBJECT_CHANGES)} />
              <Live on={follow} onChange={onFollow} rows={live.rows} fresh={fresh} denied={live.denied} />
              <AuditStatus chain={state.chain} stale={stale(SUBJECT_CHAIN)} onDetails={openSystem} />
            </div> : <>
              <Inventory answer={answer} denied={state.answerDenied} />
              {showSetup && <Setup marks={marks} />}
              <div className="overview-monitor">
                <div>
                  {stale(SUBJECT_OVERVIEW) && <p className="overview-stale">Decision counts last read {stale(SUBJECT_OVERVIEW)}.</p>}
                  <Decisions block={answer?.decisions || null} denied={state.answerDenied} hour={hour} onHour={(on) => setHour((prev) => (on ? prev || HOUR_START : null))} />
                </div>
                <div className="overview-monitor-side">
                  {(!showSetup || hasAttention) && attention}
                  <ConfigurationNote rows={rows} denied={state.configDenied} stale={stale(SUBJECT_CONFIG)} onDetails={openSystem} />
                </div>
              </div>
              <div className="overview-monitor-lower">
                <Stopped block={answer?.decisions || null} denied={state.answerDenied} />
              </div>
              <AuditStatus chain={state.chain} stale={stale(SUBJECT_CHAIN)} onDetails={openSystem} />
            </>}
          </TabsContent>
        </Tabs>
      </div>
    </div>
  );
}
