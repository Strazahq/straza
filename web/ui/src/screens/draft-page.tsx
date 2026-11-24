import * as React from "react";
import { FileXIcon, Loader2Icon } from "lucide-react";
import { DraftBand } from "@/components/draft-band";
import { DraftChanges } from "@/components/draft-changes";
import { Chip, Pill, type Tone } from "@/components/chip";
import { ChecksHead, DraftChecks } from "@/components/draft-checks";
import { DiscardDialog, PicksDialog } from "@/components/draft-dialogs";
import { DraftGains } from "@/components/draft-gains";
import { DraftItems } from "@/components/draft-items";
import { DraftFacts, DraftMeta } from "@/components/draft-meta";
import { DraftNote } from "@/components/draft-note";
import { DecidedRow, ProofStrip, ReplacedStrip } from "@/components/draft-proof";
import { DraftPublish } from "@/components/draft-publish";
import { DraftShort } from "@/components/draft-short";
import { DraftYaml } from "@/components/draft-yaml";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { PageHead } from "@/components/page-head";
import { Button } from "@/components/ui/button";
import { type ApiError, type AppRow, type DraftConflict, type DraftDetail, type DraftPublished, type DraftSummary, type RoleRow, getConfig, listApps, listRoles } from "@/lib/api";
import { chipNotes, isNewRole, loosenings, objectsMove, roleHolders, roleToolsOf } from "@/lib/drafts-access";
import { conflictsOf, contactDraftServer, getDraft, rebaseDraft, revertDraft } from "@/lib/drafts-api";
import { itemFacts } from "@/lib/drafts-items";
import { STALE_CODE, type Shown, gainMark, gainStory, gainsHidden, stateOf } from "@/lib/drafts-model";
import { readLater, replacedBy } from "@/lib/drafts-replaced";
import { shortRows } from "@/lib/drafts-short";
import {
  ALL_OR_NOTHING,
  CHANGED_TITLE,
  CHANGES_TITLE,
  CHECKS_TITLE,
  CHECK_AGAIN,
  CHECK_AGAIN_SUBJECT,
  FILE_REFUSED,
  GAINS_HELP,
  GAINS_TITLE,
  MISSING_TITLE,
  NEXT_TITLE,
  OPEN_DRAFTS,
  STALE_TITLE,
  STATE_WORD,
  UNDO_SUBJECT,
  agentWrote,
  answered,
  checkedAgain,
  draftTitle,
  holdersLose,
  needLine,
  objectCount,
  publishedUnread,
  readingDraft,
  serverLine,
  subjectDraft,
  undoneToast,
} from "@/lib/drafts-words";
import { registeredAfter } from "@/lib/machine-servers";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { cn } from "@/lib/utils";

// The review page of one draft: the server's title with a row of pills
// that says which draft, who drafted it and when, a red warning when an
// agent's draft loosens the rules that bind that agent, the band that stays
// in view and publishes or discards, In short, the proposer's note marked
// unverified, what changes card by card, access after publishing where a
// holder gains or loses a tool, and the checks. Every line but the note is
// the server's, or built from what it answered. Publish is a person's act:
// the dialog renders only for a reader the server says may publish, and
// the server checks again when it runs.

const route = routeByKey("drafts");
const CRUMBS = [{ label: route.label, to: "drafts" as const }];
const STATE_TONE: Record<Shown, Tone> = { ready: "accent", refused: "danger", stale: "warn", published: "ok", discarded: "plain", expired: "plain" };
const BANNER = "flex flex-col gap-1 border border-l-[3px] border-border bg-card px-3.5 py-2.5 text-dense leading-relaxed text-text-2";
const H2 = "m-0 text-section leading-7 font-semibold text-foreground";

type State =
  | { kind: "loading" }
  // fromPublish marks a draft the publish answered and no read has
  // followed yet, whose stored check the page does not know.
  | { kind: "ready"; detail: DraftDetail; lastRead: Date; problem: string | null; fromPublish?: boolean }
  | { kind: "missing"; sentence: string }
  | { kind: "error"; message: string };

// Reads are what the page reads besides the draft, once per draft: the
// registered servers, the live role list of a published draft, the draft
// that replaced a published one, and the rate limit a remote server without
// its own gets. Each is null while unread or where its read failed.
type Reads = { apps: string[] | null; roles: RoleRow[] | null; successor: DraftSummary | null; remoteRps: number | null };

export function DraftPage({ id }: { id: string }) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [answer, setAnswer] = React.useState<DraftPublished | null>(null);
  const [publishing, setPublishing] = React.useState(false);
  const [discarding, setDiscarding] = React.useState(false);
  const [conflicts, setConflicts] = React.useState<DraftConflict[] | null>(null);
  const [checking, setChecking] = React.useState(false);
  const [checkProblem, setCheckProblem] = React.useState<string | null>(null);
  const [pickProblem, setPickProblem] = React.useState<string | null>(null);
  const [undoing, setUndoing] = React.useState(false);
  const [undoProblem, setUndoProblem] = React.useState<string | null>(null);
  const [details, setDetails] = React.useState(false);
  const [more, setMore] = React.useState(false);
  const [yaml, setYaml] = React.useState({ open: false, shown: "" });
  // opener is the button that opened the YAML sheet, the publish dialog or
  // the discard dialog, which gets the focus back when it closes.
  const opener = React.useRef<HTMLElement | null>(null);
  const keepOpener = () => { opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null; };
  const [reads, setReads] = React.useState<Reads>({ apps: null, roles: null, successor: null, remoteRps: null });
  // checked holds each server the publish dialog stored a value of and
  // checked: the server as its check read it, and the Next line its stored
  // secret answers.
  const [checked, setChecked] = React.useState<Record<string, { app: AppRow; answered: string | null }>>({});
  // first is the state of the draft the page first read. Only a draft that
  // was published when the page opened can have been replaced since.
  const first = React.useRef<string | null>(null);

  // load reads the draft, whose verdict the server computes on the read
  // while it is open. A failed reload keeps the page and says what failed
  // above it.
  const load = React.useCallback(async () => {
    try {
      const detail = await getDraft(id);
      if (first.current === null) first.current = detail.draft.state;
      setState({ kind: "ready", detail, lastRead: new Date(), problem: null });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      if (err.status === 404) { setState({ kind: "missing", sentence: err.message }); return; }
      const message = readFailed(subjectDraft(id), err);
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
    }
  }, [id]);
  React.useEffect(() => { void load(); }, [load]);

  const ready = state.kind === "ready" ? state.detail : null;
  const published = ready?.draft.state === "published";
  // The registered servers tell a server on a person's machine apart only
  // when the list is whole, which it is for a full seat alone, and only a
  // draft with a policy set needs it.
  const needsApps = !!ready && snapshot()?.grants === "full" && ready.draft.items.some((it) => it.kind === "PolicySet");
  React.useEffect(() => {
    if (!needsApps) return;
    let live = true;
    listApps().then((rows) => { if (live) setReads((r) => ({ ...r, apps: rows.map((a) => a.name) })); }, () => undefined);
    return () => { live = false; };
  }, [needsApps]);
  // A draft with a server reads the remote rate limit default from the
  // config, so a server's facts name it as the server page does.
  const hasApp = !!ready && ready.draft.items.some((it) => it.kind === "App");
  React.useEffect(() => {
    if (!hasApp) return;
    let live = true;
    getConfig().then((c) => { if (live) setReads((r) => ({ ...r, remoteRps: c?.apps?.default_remote_rps || null })); }, () => undefined);
    return () => { live = false; };
  }, [hasApp]);
  React.useEffect(() => {
    if (!published || !ready) return;
    let live = true;
    listRoles().then((rows) => { if (live) setReads((r) => ({ ...r, roles: rows })); }, () => undefined);
    if (first.current === "published") void readLater(ready).then((later) => { if (live) setReads((r) => ({ ...r, successor: replacedBy(ready, later) })); });
    return () => { live = false; };
    // The reads run once, when the draft first reads as published.
  }, [published]); // eslint-disable-line react-hooks/exhaustive-deps

  const label = draftTitle(id);
  if (state.kind === "loading") return <PageHead label={label} crumbs={CRUMBS} title={label} description={readingDraft(id)} />;
  if (state.kind === "error") {
    return (
      <>
        <PageHead label={label} crumbs={CRUMBS} title={label} description={readingDraft(id)} />
        <div className="px-6 py-5"><FetchError subject={subjectDraft(id)} detail={state.message} /></div>
      </>
    );
  }
  if (state.kind === "missing") {
    return (
      <>
        <PageHead label={label} crumbs={CRUMBS} title={label} description="" />
        <EmptyState icon={FileXIcon} title={MISSING_TITLE} action={<Button variant="outline" onClick={() => navigate("drafts")}>{OPEN_DRAFTS}</Button>}>{state.sentence}</EmptyState>
      </>
    );
  }

  const { detail } = state;
  const d = detail.draft;
  const v = detail.verdict;
  const shown = stateOf(detail);
  const open = d.state === "open";
  const proposer = d.authors[0];
  const stale = (v.refused || []).filter((f) => f.code === STALE_CODE);
  const shownAnswer = answer && answer.draft.id === d.id ? answer : null;
  // The strip names a server the dialog checked with that check's status,
  // and Next leaves out the line a secret stored in the dialog answers,
  // matched by the fix text of the same draft's warning.
  const answeredLines = Object.values(checked).map((c) => c.answered);
  const nextLines = shownAnswer ? shownAnswer.next.filter((n) => !answeredLines.includes(n)) : [];

  const setDetail = (next: DraftDetail) => setState((s) => (s.kind === "ready" ? { ...s, detail: next } : s));

  const checkAgain = async (picks?: Record<string, "draft" | "live">) => {
    if (checking) return;
    setChecking(true);
    setCheckProblem(null);
    setPickProblem(null);
    try {
      await rebaseDraft(d.id, picks ? { revision: d.revision, picks } : { revision: d.revision });
      setConflicts(null);
      notify.ok(checkedAgain(d.id));
      await load();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const c = conflictsOf(err);
      if (c) setConflicts(c);
      else if (picks) setPickProblem(answered(err));
      else setCheckProblem(answered(err));
    } finally {
      setChecking(false);
    }
  };

  const contact = async (object: string) => {
    await contactDraftServer(d.id, object);
    await load();
  };

  const undo = async () => {
    if (undoing) return;
    setUndoing(true);
    setUndoProblem(null);
    try {
      const made = await revertDraft(d.id, "");
      notify.ok(undoneToast(made.draft.id, d.id));
      navigate("drafts", [made.draft.id]);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setUndoProblem(answered(err));
    } finally {
      setUndoing(false);
    }
  };

  // Right after a publish the page waits for its read of the draft, and says
  // so, or says that read failed, rather than claim no check was stored.
  const unread = state.fromPublish ? (state.problem ? publishedUnread(d.id) : readingDraft(d.id)) : null;
  const hidden = gainsHidden(v);
  const gains = open ? v.gains || [] : [];
  const loosened = open ? loosenings(v) : [];
  const loses = gainStory(gains).lose;
  const needs = (v.needs || []).map((n) => needLine(n.object, n.standing)).join(" ");
  const successor = published ? reads.successor : null;
  const registered = reads.apps ? registeredAfter(reads.apps, d.items) : null;
  // A role's holders come from the verdict of an open draft, and from the
  // live role list of a published one that nothing replaced since.
  const holders = (role: string): number | null => {
    if (open) return roleHolders(v, role);
    if (d.state !== "published" || successor || !reads.roles) return null;
    const row = reads.roles.find((r) => r.name === role);
    return row ? row.holder_count || 0 : null;
  };
  const rows = shortRows(detail, { registered, replaced: !!successor, roles: reads.roles });
  // Access after publishing shows where something changes for a role that
  // somebody holds, the outcome of a tool or the objects it covers, and
  // where part of it is hidden from the reader.
  const toolMoves = gains.some((g) => g.holders_count > 0 && ["gains", "loses", "gate"].includes(gainMark(g)));
  const objectMoves = (v.objects || []).some((c) => {
    const m = objectsMove(c);
    return c.holders_count > 0 && (m.every === "gained" || m.every === "lost" || m.added.length > 0 || m.dropped.length > 0);
  });
  const gainsShown = open && (!!hidden || toolMoves || objectMoves);
  const n = d.items.length;
  const omit = chipNotes(detail);

  return (
    <>
      <PageHead
        label={label}
        crumbs={CRUMBS}
        sticky={false}
        title={d.title || draftTitle(d.id)}
        titleExtra={<Chip tone={STATE_TONE[shown]} data-state-badge={STATE_WORD[shown]}>{STATE_WORD[shown]}</Chip>}
        description=""
        descriptionExtra={<DraftMeta detail={detail} me={snapshot()?.user || ""} details={details} onDetails={() => setDetails((o) => !o)} />}
      />
      <div className="flex flex-col gap-5 px-6 py-5">
        {state.problem && <FetchError subject={subjectDraft(d.id)} detail={state.problem} lastRead={state.lastRead} />}
        {details && <DraftFacts detail={detail} needs={needs} />}
        {loosened.length > 0 && (
          // The agent is named from the draft's proposer, because a line's
          // own sentence never names a person.
          <div className={cn(BANNER, "max-w-[90ch] border-danger/60 border-l-danger bg-danger-bg text-body")} data-agent-loosens>
            <b className="font-semibold text-danger">{agentWrote(proposer?.username || null, loosened.map((l) => l.line))}</b>
            {loosened.map((l) => <span key={l.line.key}>{[l.line, ...l.loses].map((f) => f.sentence).join(" ")}</span>)}
          </div>
        )}

        {published && successor && <ReplacedStrip draft={d} successor={successor} />}
        {published && !successor && (
          <ProofStrip draft={d} undoing={undoing} onUndo={() => void undo()}>
            {shownAnswer && shownAnswer.servers.map((s) => {
              const c = checked[s.name];
              return <span key={s.name} data-server={s.name}>{serverLine(s.name, s.change, c ? c.app.status : s.status, c ? c.app.detail : s.detail)}</span>;
            })}
            {undoProblem && <RefusedError subject={UNDO_SUBJECT} message={undoProblem} />}
          </ProofStrip>
        )}
        {nextLines.length > 0 && (
          <div className="flex flex-col gap-1 rounded-md border border-border bg-card px-3.5 py-2.5 text-dense text-text-2" data-next>
            <h3 className="m-0 text-dense font-semibold text-foreground">{NEXT_TITLE}</h3>
            {nextLines.map((line) => <p key={line} className="m-0">{line}</p>)}
          </div>
        )}
        {(d.state === "discarded" || d.state === "expired") && <DecidedRow draft={d} />}
        {d.refusal && <div className={BANNER + " border-l-danger"} data-file-refused><b className="font-semibold text-foreground">{FILE_REFUSED}</b><span>{d.refusal}</span></div>}
        {open && stale.length > 0 && (
          <div className={BANNER + " border-l-warn"} data-stale>
            <b className="font-semibold text-foreground">{STALE_TITLE}</b>
            {stale.map((f) => <span key={f.key}>{f.sentence}</span>)}
            <span className="mt-1"><Button size="sm" onClick={() => void checkAgain()} disabled={checking}>{checking && <Loader2Icon className="animate-spin" />}{CHECK_AGAIN}</Button></span>
            {checkProblem && <RefusedError subject={CHECK_AGAIN_SUBJECT} message={checkProblem} />}
          </div>
        )}
        {open && (
          <DraftBand
            verdict={v}
            mayPublish={detail.may_publish}
            refusal={detail.publish_refusal}
            needs={needs}
            blocked={shown === "stale" || shown === "refused" ? shown : false}
            onDiscard={() => { keepOpener(); setDiscarding(true); }}
            onPublish={() => { keepOpener(); setPublishing(true); }}
          />
        )}
        {loses.length > 0 && <p className="m-0 max-w-[75ch] text-body leading-relaxed text-warn" data-loses>{holdersLose(loses)}</p>}

        {rows.length > 0 ? <DraftShort rows={rows} /> : <DraftItems facts={d.items.map((it) => itemFacts(detail, it))} />}

        {d.note && proposer && <DraftNote note={d.note} author={proposer} source={d.source} />}

        <section className="flex flex-col gap-3" aria-label={published ? CHANGED_TITLE : CHANGES_TITLE}>
          <div className="flex flex-wrap items-center gap-2">
            <h2 className={H2}>{published ? CHANGED_TITLE : CHANGES_TITLE}</h2>
            {n > 0 && <Pill data-object-count>{objectCount(n)}</Pill>}
            {open && n > 1 && <Pill>{ALL_OR_NOTHING}</Pill>}
          </div>
          <DraftChanges detail={detail} registered={registered} holders={holders} remoteRps={reads.remoteRps} onYaml={(shown) => { keepOpener(); setYaml({ open: true, shown }); }} />
        </section>

        {gainsShown && (
          <section className="flex flex-col gap-3" aria-label={GAINS_TITLE}>
            <div className="flex items-center gap-1.5"><h2 className={H2}>{GAINS_TITLE}</h2><HelpTip label={GAINS_TITLE} text={GAINS_HELP} /></div>
            <DraftGains gains={gains} objects={v.objects} toolsOf={roleToolsOf(detail)} isNew={(r) => isNewRole(detail, r)} hidden={hidden} />
          </section>
        )}

        <section className="flex scroll-mt-40 flex-col gap-3" aria-label={CHECKS_TITLE} id="draft-checks">
          <div className="flex flex-wrap items-center gap-2" data-checks-head>
            <h2 className={H2}>{CHECKS_TITLE}</h2>
            {unread ? <span className="text-dense text-text-2" data-checks-unread>{unread}</span> : <ChecksHead detail={detail} omit={omit} more={more} onMore={() => setMore((m) => !m)} />}
          </div>
          {open && <DraftChecks detail={detail} onContact={contact} omit={omit} more={more} />}
        </section>
      </div>

      <DraftYaml draft={d} open={yaml.open} shown={yaml.shown} onShow={(s) => setYaml({ open: true, shown: s })} onClose={() => setYaml((y) => ({ ...y, open: false }))} onClosed={() => opener.current?.focus()} />
      {((open && detail.may_publish) || publishing) && (
        // The dialog stays past the publish while it stores and checks the
        // values it was given, until the person closes it.
        <DraftPublish
          draft={d}
          verdict={v}
          live={detail.live}
          open={publishing}
          onOpenChange={setPublishing}
          onClosed={() => opener.current?.focus()}
          onPublished={(a, stays) => {
            if (!stays) setPublishing(false);
            setAnswer(a);
            setState((s) => (s.kind === "ready" ? { ...s, detail: { ...s.detail, draft: a.draft }, fromPublish: true } : s));
            void load();
          }}
          onVerdict={(next) => setDetail({ ...detail, verdict: next })}
          onChecked={(name, app, answered) => setChecked((c) => ({ ...c, [name]: { app, answered: answered || (c[name] ? c[name].answered : null) } }))}
        />
      )}
      <DiscardDialog draft={d} open={discarding} onOpenChange={setDiscarding} onClosed={() => opener.current?.focus()} onDiscarded={() => { setDiscarding(false); void load(); }} />
      <PicksDialog
        conflicts={conflicts || []}
        open={!!conflicts}
        onOpenChange={(o) => { if (!o) { setConflicts(null); setPickProblem(null); } }}
        busy={checking}
        refusal={pickProblem}
        onPick={(picks) => void checkAgain(picks)}
      />
    </>
  );
}
