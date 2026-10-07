import * as React from "react";
import { FileXIcon, Loader2Icon, Undo2Icon } from "lucide-react";
import { DraftChanges } from "@/components/draft-changes";
import { DraftChecks, DraftChip, type ChipTone, VerdictStrip } from "@/components/draft-checks";
import { DiscardDialog, PicksDialog, unlinked } from "@/components/draft-dialogs";
import { DraftGains } from "@/components/draft-gains";
import { DraftPublish } from "@/components/draft-publish";
import { EmptyState } from "@/components/empty-state";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { PageHead } from "@/components/page-head";
import { Button } from "@/components/ui/button";
import type { ApiError, DraftConflict, DraftDetail, DraftPublished } from "@/lib/api";
import { UNVERIFIED } from "@/lib/approval-words";
import { conflictsOf, contactDraftServer, getDraft, rebaseDraft, revertDraft } from "@/lib/drafts-api";
import { STALE_CODE, type Shown, gainsHidden, objectOf, stateOf } from "@/lib/drafts-model";
import {
  CHANGES_TITLE,
  CHECKS_TITLE,
  CHECK_AGAIN,
  CHECK_AGAIN_SUBJECT,
  DISCARD,
  DISCARD_KEEP_LIVE,
  DOCS_MASKED,
  DOCS_TITLE,
  FACT,
  FILE_REFUSED,
  GAINS_HELP,
  GAINS_TITLE,
  MAY_PUBLISH_ALL,
  MISSING_TITLE,
  NEXT_TITLE,
  NO_NEEDS,
  OPEN_DRAFTS,
  PUBLISH_NOT_YET,
  PUBLISH_OPEN,
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
  answered,
  cameInLine,
  checkedAgain,
  checkCounts,
  checkedLine,
  checksLede,
  discardKeep,
  discardedLine,
  docsFold,
  draftTitle,
  expiredLine,
  expiresLine,
  needLine,
  notCheckedAgain,
  proposerWords,
  publishedLine,
  publishedUnread,
  readingDraft,
  reasonLine,
  removalDoc,
  revisionLine,
  saysPersonHelp,
  saysTitle,
  serverLine,
  storedCheckLine,
  storedNoCheck,
  subjectDraft,
  undoesLine,
  undoneToast,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { REEVALUATE, liveSince, liveSinceNoVersion } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { snapshot } from "@/lib/session";
import { absTime } from "@/lib/words";

// The review page of one draft: who drafted it and how it came in, the
// proposer's note marked
// unverified, the verdict strip, what changes, who gains what, the checks
// and the documents, with a sticky bar that publishes and discards. Every
// line but the note is the server's. Publish is a person's act: the dialog
// renders only for a reader the server says may publish, and the server
// checks again when it runs.

const route = routeByKey("drafts");
const STATE_TONE: Record<Shown, ChipTone> = { ready: "accent", refused: "danger", stale: "warn", published: "ok", discarded: "plain", expired: "plain" };
const DANGER_OUTLINE = "border-danger text-danger hover:bg-danger-bg hover:text-danger";
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

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <tr className="border-b border-border last:border-b-0" data-fact={label}>
      <th scope="row" className="w-[170px] py-2 pr-3 text-left align-top font-semibold whitespace-nowrap text-foreground">{label}</th>
      <td className="py-2 align-top text-text-2">{children}</td>
    </tr>
  );
}

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

  if (state.kind === "loading") return <PageHead label={route.label} title={draftTitle(id)} description={readingDraft(id)} />;
  if (state.kind === "error") {
    return (
      <>
        <PageHead label={route.label} title={draftTitle(id)} description={readingDraft(id)} />
        <div className="px-6 py-5"><FetchError subject={subjectDraft(id)} detail={state.message} /></div>
      </>
    );
  }
  if (state.kind === "missing") {
    return (
      <>
        <PageHead label={route.label} title={draftTitle(id)} description={route.description} />
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
  const who = proposer ? proposerWords(proposer, snapshot()?.user || "", d.source) : null;
  const stale = (v.refused || []).filter((f) => f.code === STALE_CODE);
  const blocked = shown === "stale" || shown === "refused";
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
  const barLine = shown === "stale" ? STALE_BAR : shown === "refused" ? REFUSED_BAR : !detail.may_publish ? (detail.publish_refusal ? detail.publish_refusal + " " : "") + WAITS_WHOLE : ackBar((v.risks || []).length, (v.warnings || []).length);

  return (
    <>
      <PageHead label={route.label} title={draftTitle(d.id)} titleExtra={<DraftChip word={STATE_WORD[shown]} tone={STATE_TONE[shown]} attr="data-state-badge" />} description={d.title} />
      <div className="flex flex-col gap-5 px-6 py-5">
        {state.problem && <FetchError subject={subjectDraft(d.id)} detail={state.problem} lastRead={state.lastRead} />}

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

        <table className="w-full max-w-[960px] border-collapse text-sm" data-facts>
          <tbody>
            {who && <Fact label={FACT.by}><b className="font-semibold text-foreground">{who.name}</b>{who.line && <span className="text-muted-foreground">{" · " + who.line}</span>}</Fact>}
            <Fact label={FACT.door}>{cameInLine(d.door, d.source, proposer?.client)}</Fact>
            <Fact label={FACT.revisions}>
              <ul className="m-0 flex list-none flex-col gap-0.5 p-0">{(detail.revisions || []).map((r) => <li key={r.revision}>{revisionLine(r.revision, r.author.username, r.door, absTime(r.created_at), !!r.mechanical)}</li>)}</ul>
            </Fact>
            <Fact label={FACT.checked}>{unread || (open || stored ? checkedLine(absTime(v.checked_at), (v.snapshot || "").slice(0, 8)) : storedNoCheck(d.revision))}</Fact>
            {open && d.expires_at && <Fact label={FACT.expires}>{expiresLine(absTime(d.expires_at))}</Fact>}
            {d.reverts && <Fact label={FACT.undoes}><Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => navigate("drafts", [d.reverts as string])}>{undoesLine(d.reverts)}</Button></Fact>}
            {open && (
              <Fact label={FACT.needs}>
                <span className="flex flex-col gap-0.5">
                  {(v.needs || []).length ? (v.needs || []).map((n) => <span key={n.object}>{needLine(n.object, n.standing)}</span>) : <span>{NO_NEEDS}</span>}
                  <span>{detail.may_publish ? MAY_PUBLISH_ALL : detail.publish_refusal}</span>
                </span>
              </Fact>
            )}
          </tbody>
        </table>

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

        {open && <VerdictStrip verdict={v} onJump={() => checksRef.current?.scrollIntoView({ block: "start" })} />}

        <section className="flex flex-col gap-3" aria-label={CHANGES_TITLE}>
          <h2 className="m-0 text-base font-semibold text-foreground">{CHANGES_TITLE}</h2>
          <DraftChanges detail={detail} />
        </section>

        {open && (
          <section className="flex flex-col gap-3" aria-label={GAINS_TITLE}>
            <div className="flex items-center gap-1.5"><h2 className="m-0 text-base font-semibold text-foreground">{GAINS_TITLE}</h2><HelpTip label={GAINS_TITLE} text={GAINS_HELP} /></div>
            <DraftGains gains={v.gains || []} hidden={gainsHidden(v)} />
          </section>
        )}

        <section ref={checksRef} className="flex scroll-mt-40 flex-col gap-3" aria-label={CHECKS_TITLE} id="draft-checks">
          <h2 className="m-0 text-base font-semibold text-foreground">{CHECKS_TITLE}</h2>
          <p className="m-0 text-sm text-text-2" data-checks-lede>{unread || (open ? checksLede(absTime(v.checked_at)) : closedChecks)}</p>
          {open && <DraftChecks detail={detail} onContact={contact} />}
        </section>

        <section className="flex flex-col gap-2" aria-label={DOCS_TITLE}>
          <h2 className="m-0 text-base font-semibold text-foreground">{DOCS_TITLE}</h2>
          <details>
            <summary className="cursor-pointer text-sm text-muted-foreground hover:text-foreground">{docsFold(d.items.length, d.revision)}</summary>
            <div className="mt-2 flex flex-col gap-2">
              {d.items.map((it) => (
                <div key={objectOf(it)} className="flex flex-col gap-1">
                  <span className="font-mono text-[13px] text-foreground">{objectOf(it)}</span>
                  {it.withheld ? <span className="text-sm text-text-2">{it.withheld}</span>
                    : it.op === "remove" ? <span className="text-sm text-text-2">{removalDoc(objectOf(it))}</span>
                    : <pre className="m-0 overflow-x-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-[12.5px] leading-relaxed text-foreground">{it.doc}</pre>}
                </div>
              ))}
            </div>
          </details>
          <p className="m-0 text-[13px] text-muted-foreground">{DOCS_MASKED}</p>
        </section>
      </div>

      {open && (
        <div className="sticky bottom-0 z-10 flex flex-wrap items-center gap-2.5 border-t border-border bg-background px-6 py-2.5" data-publish-bar>
          <span className="min-w-0 flex-1 text-sm text-text-2">{barLine}</span>
          <Button variant="outline" className={DANGER_OUTLINE} onClick={() => setDiscarding(true)}>
            {server ? discardKeep(server) : d.door === "apps-directory" ? DISCARD_KEEP_LIVE : DISCARD}
          </Button>
          {detail.may_publish && (
            <Button
              variant={blocked ? "outline" : "default"}
              aria-disabled={blocked || undefined}
              title={blocked ? (shown === "stale" ? STALE_BAR : PUBLISH_NOT_YET) : undefined}
              onClick={() => { if (!blocked) setPublishing(true); }}
            >
              {PUBLISH_OPEN}
            </Button>
          )}
        </div>
      )}

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
