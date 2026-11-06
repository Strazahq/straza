import * as React from "react";
import { FileXIcon, Loader2Icon, Undo2Icon } from "lucide-react";
import { DraftBand } from "@/components/draft-band";
import { DraftChanges } from "@/components/draft-changes";
import { DraftChecks, DraftChip, type ChipTone } from "@/components/draft-checks";
import { DiscardDialog, PicksDialog, unlinked } from "@/components/draft-dialogs";
import { DraftGains } from "@/components/draft-gains";
import { DraftItems } from "@/components/draft-items";
import { DraftFacts, DraftMeta } from "@/components/draft-meta";
import { DraftPublish } from "@/components/draft-publish";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { PageHead } from "@/components/page-head";
import { Button } from "@/components/ui/button";
import type { ApiError, DraftConflict, DraftDetail, DraftPublished } from "@/lib/api";
import { UNVERIFIED } from "@/lib/approval-words";
import { chipNotes, isNewRole, loosenings, roleToolsOf } from "@/lib/drafts-access";
import { conflictsOf, contactDraftServer, getDraft, rebaseDraft, revertDraft } from "@/lib/drafts-api";
import { itemFacts } from "@/lib/drafts-items";
import { STALE_CODE, type Shown, confirmBlocks, gainStory, gainsHidden, stateOf } from "@/lib/drafts-model";
import {
  CHANGES_TITLE,
  CHECKS_TITLE,
  CHECK_AGAIN,
  CHECK_AGAIN_SUBJECT,
  DISCARD,
  DISCARD_KEEP_LIVE,
  FILE_REFUSED,
  GAINS_HELP,
  GAINS_TITLE,
  LOOSEN_TAIL,
  MISSING_TITLE,
  NEXT_TITLE,
  NO_NEEDS,
  OPEN_DRAFTS,
  REFUSED_BAR,
  SAYS_AGENT_HELP,
  STALE_BAR,
  STALE_TAIL,
  STALE_TITLE,
  STATE_WORD,
  UNDO,
  UNDO_HELP,
  UNDO_SUBJECT,
  WAITS_WHOLE,
  ackBar,
  agentWrote,
  answered,
  checkedAgain,
  checkCounts,
  checksLede,
  crumbLabel,
  discardKeep,
  discardedLine,
  draftTitle,
  expiredLine,
  holdersLose,
  idChip,
  needLine,
  notCheckedAgain,
  publishedLine,
  publishedUnread,
  readingDraft,
  reasonLine,
  saysPersonHelp,
  saysTitle,
  serverLine,
  storedCheckLine,
  storedNoCheck,
  subjectDraft,
  undoneToast,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { REEVALUATE, liveSince, liveSinceNoVersion } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { cn } from "@/lib/utils";
import { absTime } from "@/lib/words";

// The review page of one draft: the server's title with one line of who
// drafted it and when, a red warning when an agent's draft loosens the
// rules that bind that agent, the Before you publish band that stays in
// view and publishes or discards, the proposer's note marked unverified,
// In this draft with one row per item, what changes card by card, access
// after publishing and the checks. Every line but the note is the server's,
// and the rows and the warnings are built from them. Publish is a person's
// act: the dialog renders only for a reader the server says may publish,
// and the server checks again when it runs.

const route = routeByKey("drafts");
const STATE_TONE: Record<Shown, ChipTone> = { ready: "accent", refused: "danger", stale: "warn", published: "ok", discarded: "plain", expired: "plain" };
const BANNER = "flex flex-col gap-1 border border-l-[3px] border-border bg-card px-3.5 py-2.5 text-sm leading-relaxed text-text-2";
// PROOF is the strip of a published draft. Being live is a trust state, so
// the ok tone fills the whole strip and no edge carries it.
const PROOF = "flex flex-col gap-1 rounded-md border border-ok/40 bg-ok-bg px-4 py-3 text-base text-text-2";

type State =
  | { kind: "loading" }
  // fromPublish marks a draft the publish answered and no read has
  // followed yet, whose stored check the page does not know.
  | { kind: "ready"; detail: DraftDetail; lastRead: Date; problem: string | null; fromPublish?: boolean }
  | { kind: "missing"; sentence: string }
  | { kind: "error"; message: string };

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
  const checksRef = React.useRef<HTMLElement>(null);

  // load reads the draft, whose verdict the server computes on the read
  // while it is open. A failed reload keeps the page and says what failed
  // above it.
  const load = React.useCallback(async () => {
    try {
      const detail = await getDraft(id);
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

  const crumb = crumbLabel(route.label, id);
  if (state.kind === "loading") return <PageHead label={crumb} title={draftTitle(id)} description={readingDraft(id)} />;
  if (state.kind === "error") {
    return (
      <>
        <PageHead label={crumb} title={draftTitle(id)} description={readingDraft(id)} />
        <div className="px-6 py-5"><FetchError subject={subjectDraft(id)} detail={state.message} /></div>
      </>
    );
  }
  if (state.kind === "missing") {
    return (
      <>
        <PageHead label={crumb} title={draftTitle(id)} description={route.description} />
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
  const server = unlinked(d);

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

  const at = absTime(d.decided_at);
  // A draft that is not open answers no line and the counts of its stored
  // check, so the strip, who gains what and the check's lines are left out.
  const stored = detail.checks;
  const closedChecks = (stored ? storedCheckLine(stored.revision, absTime(stored.checked_at), checkCounts(stored)) : storedNoCheck(d.revision)) + " " + notCheckedAgain(d.state);
  // Right after a publish the page waits for its read of the draft, and says
  // so, or says that read failed, rather than claim no check was stored.
  const unread = state.fromPublish ? (state.problem ? publishedUnread(d.id) : readingDraft(d.id)) : null;
  const risks = v.risks || [];
  // The band's line says what to do before Publish. The verdict chip beside
  // it already says when there is nothing to confirm, and the band names a
  // refusal to publish beside its own help.
  const blocks = confirmBlocks(risks).length;
  const warnings = (v.warnings || []).length;
  const bandLine = shown === "stale" ? STALE_BAR : shown === "refused" ? REFUSED_BAR : !detail.may_publish ? WAITS_WHOLE : blocks || warnings ? ackBar(blocks, risks.length, warnings) : "";
  const hidden = gainsHidden(v);
  const loosened = open ? loosenings(v) : [];
  const loses = open ? gainStory(v.gains || []).lose : [];
  const needs = (v.needs || []).map((n) => needLine(n.object, n.standing)).join(" ") || NO_NEEDS;
  const facts = d.items.map((it) => itemFacts(detail, it));
  const jumpToChecks = () => checksRef.current?.scrollIntoView({ block: "start" });

  return (
    <>
      <PageHead
        label={crumb}
        title={d.title || draftTitle(d.id)}
        titleExtra={<><DraftChip word={STATE_WORD[shown]} tone={STATE_TONE[shown]} attr="data-state-badge" /><span className="font-mono"><DraftChip word={idChip(d.id)} tone="plain" attr="data-draft-id" /></span></>}
        description=""
        descriptionExtra={<DraftMeta detail={detail} me={snapshot()?.user || ""} details={details} onDetails={() => setDetails((o) => !o)} />}
      />
      <div className="flex flex-col gap-5 px-6 py-5">
        {state.problem && <FetchError subject={subjectDraft(d.id)} detail={state.problem} lastRead={state.lastRead} />}
        {details && <DraftFacts detail={detail} checked={unread || ""} needs={needs} />}
        {loosened.length > 0 && (
          // The agent is named from the draft's proposer, because a line's
          // own sentence never names a person.
          <div className={cn(BANNER, "max-w-[90ch] border-danger/60 border-l-danger bg-danger-bg text-[15px]")} data-agent-loosens>
            <b className="font-semibold text-danger">{agentWrote(proposer?.username || null)}</b>
            {loosened.map((l) => <span key={l.line.key}>{[l.line, ...l.loses].map((f) => f.sentence).join(" ")}</span>)}
            <span>{LOOSEN_TAIL}</span>
          </div>
        )}

        {d.state === "published" && (
          <div role="status" className={PROOF} data-published>
            <b className="text-lg font-semibold text-foreground">{publishedLine(d.decided_by?.username || "", at, d.decided_by?.client, d.id)}</b>
            <span>{(d.snapshot ? liveSince(at, d.snapshot.slice(0, 8)) : liveSinceNoVersion(at)) + " " + REEVALUATE}</span>
            {shownAnswer && shownAnswer.servers.map((s) => <span key={s.name} data-server={s.name}>{serverLine(s.name, s.change, s.status, s.detail)}</span>)}
            <span className="mt-1 flex items-center gap-1.5">
              <Button variant="outline" size="sm" onClick={() => void undo()} disabled={undoing}>{undoing ? <Loader2Icon className="animate-spin" /> : <Undo2Icon />}{UNDO}</Button>
              <HelpTip label={UNDO} text={UNDO_HELP} />
            </span>
            {undoProblem && <RefusedError subject={UNDO_SUBJECT} message={undoProblem} />}
          </div>
        )}
        {shownAnswer && shownAnswer.next.length > 0 && (
          <div className="flex flex-col gap-1 rounded-md border border-border bg-card px-3.5 py-2.5 text-sm text-text-2" data-next>
            <h3 className="m-0 text-sm font-semibold text-foreground">{NEXT_TITLE}</h3>
            {shownAnswer.next.map((n) => <p key={n} className="m-0">{n}</p>)}
          </div>
        )}
        {(d.state === "discarded" || d.state === "expired") && (
          <div className={BANNER} data-decided>
            <b className="font-semibold text-foreground">{d.state === "discarded" ? discardedLine(d.decided_by?.username || "", at) : expiredLine(absTime(d.decided_at || d.expires_at))}</b>
            {d.decided_reason && <span>{reasonLine(d.decided_reason)}</span>}
          </div>
        )}
        {d.refusal && <div className={BANNER + " border-l-danger"} data-file-refused><b className="font-semibold text-foreground">{FILE_REFUSED}</b><span>{d.refusal}</span></div>}
        {open && stale.length > 0 && (
          <div className={BANNER + " border-l-warn"} data-stale>
            <b className="font-semibold text-foreground">{STALE_TITLE}</b>
            {stale.map((f) => <span key={f.key}>{f.sentence}</span>)}
            <span>{STALE_TAIL}</span>
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
            line={bandLine}
            blocked={shown === "stale" || shown === "refused" ? shown : false}
            discard={server ? discardKeep(server) : d.door === "apps-directory" ? DISCARD_KEEP_LIVE : DISCARD}
            onJump={jumpToChecks}
            onDiscard={() => setDiscarding(true)}
            onPublish={() => setPublishing(true)}
          />
        )}
        {loses.length > 0 && <p className="m-0 max-w-[75ch] text-[15px] leading-relaxed text-warn" data-loses>{holdersLose(loses)}</p>}

        {d.note && proposer && (
          <div className="flex max-w-[960px] flex-col gap-1 rounded-md border border-dashed border-border bg-card px-3.5 py-2.5" data-says>
            <span className="flex items-center gap-2 text-[13px] text-muted-foreground">
              {saysTitle(proposer.agent, proposer.username)}
              <DraftChip word={UNVERIFIED} tone="warn" attr="data-unverified" />
              <HelpTip label={saysTitle(proposer.agent, proposer.username)} text={proposer.agent ? SAYS_AGENT_HELP : saysPersonHelp(proposer.username)} />
            </span>
            <p className="m-0 max-w-[96ch] text-sm whitespace-pre-wrap text-foreground" data-note>{d.note}</p>
          </div>
        )}

        <DraftItems facts={facts} />

        <section className="flex flex-col gap-3" aria-label={CHANGES_TITLE}>
          <h2 className="m-0 text-base font-semibold text-foreground">{CHANGES_TITLE}</h2>
          <DraftChanges detail={detail} />
        </section>

        {open && (
          <section className="flex flex-col gap-3" aria-label={GAINS_TITLE}>
            <div className="flex items-center gap-1.5"><h2 className="m-0 text-base font-semibold text-foreground">{GAINS_TITLE}</h2><HelpTip label={GAINS_TITLE} text={GAINS_HELP} /></div>
            <DraftGains gains={v.gains || []} objects={v.objects} toolsOf={roleToolsOf(detail)} isNew={(r) => isNewRole(detail, r)} hidden={hidden} />
          </section>
        )}

        <section ref={checksRef} className="flex scroll-mt-40 flex-col gap-3" aria-label={CHECKS_TITLE} id="draft-checks">
          <h2 className="m-0 text-base font-semibold text-foreground">{CHECKS_TITLE}</h2>
          <p className="m-0 max-w-[75ch] text-[15px] text-text-2" data-checks-lede>{unread || (open ? checksLede(absTime(v.checked_at)) : closedChecks)}</p>
          {open && <DraftChecks detail={detail} onContact={contact} omit={chipNotes(detail)} />}
        </section>
      </div>

      {open && detail.may_publish && (
        <DraftPublish
          draft={d}
          verdict={v}
          open={publishing}
          onOpenChange={setPublishing}
          onPublished={(a) => {
            setPublishing(false);
            setAnswer(a);
            setState((s) => (s.kind === "ready" ? { ...s, detail: { ...s.detail, draft: a.draft }, fromPublish: true } : s));
            void load();
          }}
          onVerdict={(next) => setDetail({ ...detail, verdict: next })}
        />
      )}
      <DiscardDialog draft={d} open={discarding} onOpenChange={setDiscarding} onDiscarded={() => { setDiscarding(false); void load(); }} />
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
