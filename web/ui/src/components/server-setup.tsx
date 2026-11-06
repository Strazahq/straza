import * as React from "react";
import { CheckIcon, Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { DraftPublish } from "@/components/draft-publish";
import { HelpTip } from "@/components/help-tip";
import { type ObjectsRead, declaredOf, listingOf, takesListing } from "@/components/server-listing";
import { Facts } from "@/components/server-overview";
import { type SaveNote, SaveNoteLine } from "@/components/use-draft-save";
import { type ApiError, type AppRow, type Draft, type DraftAnswer, type DraftVerdict, type ObjectsBlock, draftAppObjects } from "@/lib/api";
import { errorLines, nothingToRead, refusedLines } from "@/lib/draft-save";
import { publishDraft, refusalOf } from "@/lib/drafts-api";
import { notify } from "@/lib/notify";
import { type Nouns, nounsOf } from "@/lib/object-words";
import { navigate } from "@/lib/router";
import { SAVE_DRAFT, heldLine, keptLine, publishThis, savedToDraft, waitsLine } from "@/lib/save-words";
import { WRITE_UNCONFIRMED } from "@/lib/say";
import {
  CANCEL, PIN_FACT, PIN_FOOT_LABEL, PIN_PUBLISH, SET_IT,
  listedBy, listingStep, pinBody, pinButton, pinFoot, pinFootHelp, pinStep, pinTitle, pinnedStep, pinnedToast, readFrom, setupLead, typeFact,
} from "@/lib/server-setup-words";
import { cn } from "@/lib/utils";

// The setup card at the top of a server's Overview, while its tools
// declare an object type nobody pinned or its listing credential is not
// set, and the Pin dialog its first step opens. The dialog makes the
// objects draft strazad builds from the marks: Save draft opens it, Pin
// and publish publishes it the way the editors' Save and publish does.

const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";

// argumentOf answers the argument every marked tool reads the object from,
// "" when they read different ones, and how many tools read one.
function argumentOf(block: ObjectsBlock): { arg: string; n: number } {
  const args = Object.values(block.tools || {}).flatMap((r) => (typeof r === "object" && r && r.argument ? [r.argument] : []));
  return { arg: new Set(args).size === 1 ? args[0] : "", n: args.length };
}

// Step is one numbered step: its circle, a check once done, its sentence,
// and its button while it is the step to take.
function Step({ n, done, waits, text, action }: { n: number; done: boolean; waits: boolean; text: string; action?: React.ReactNode }) {
  return (
    <li className={cn("flex items-center gap-2.5 rounded-md border border-border bg-card px-3 py-2 text-sm", waits && "opacity-65")} data-step={n} data-done={done || undefined} data-waits={waits || undefined}>
      <span className={cn("inline-flex size-[22px] shrink-0 items-center justify-center rounded-full border text-xs", done ? "border-ok bg-ok-bg text-ok" : "border-border text-muted-foreground")} aria-hidden="true">
        {done ? <CheckIcon className="size-3" /> : n}
      </span>
      <span className={cn("min-w-0 flex-1", done ? "text-muted-foreground" : "text-text-2")}>{text}</span>
      {action}
    </li>
  );
}

type CardProps = {
  app: AppRow;
  read: ObjectsRead;
  // toolCount is the server's tool count as the Tools tab shows it.
  toolCount: number;
  onPublished: () => void;
};

// SetupCard is the card, or nothing when the objects answer is not read,
// declares no type with nothing pinned, or has both steps done. Step 2
// shows only for a server that takes a listing credential.
export function SetupCard({ app, read, toolCount, onPublished }: CardProps) {
  const [pinOpen, setPinOpen] = React.useState(false);
  const answer = read.answer;
  const of = answer && listingOf(answer);
  if (!answer || !of) return null;
  const pinned = !!answer.type;
  const declared = declaredOf(answer);
  const takes = takesListing(app, answer);
  if (pinned ? !(takes && answer.listing?.credential === "missing") : !declared) return null;
  const setIt = () => {
    read.setOpen(true);
    document.querySelector("[data-listing-row]")?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  };
  return (
    <section className="mb-4 flex max-w-[900px] flex-col gap-2.5 rounded-md border border-link/40 bg-accent-bg px-4 py-3" data-setup-card>
      <p className="m-0 text-[15px] font-semibold text-foreground">{setupLead(app.name, of.w)}</p>
      <ol className="m-0 flex list-none flex-col gap-2 p-0">
        <Step n={1} done={pinned} waits={false} text={pinned ? pinnedStep(of.w) : pinStep(of.w)} action={!pinned && <Button size="sm" onClick={() => setPinOpen(true)}>{pinButton(of.w)}</Button>} />
        {takes && <Step n={2} done={false} waits={!pinned} text={listingStep(app.name, of.lister, !pinned)} action={pinned && <Button size="sm" onClick={setIt}>{SET_IT}</Button>} />}
      </ol>
      {!pinned && declared && <PinDialog app={app} declared={declared} toolCount={toolCount} open={pinOpen} onOpenChange={setPinOpen} onPublished={onPublished} />}
    </section>
  );
}

type PinProps = { app: AppRow; declared: ObjectsBlock; toolCount: number; open: boolean; onOpenChange: (open: boolean) => void; onPublished: () => void };
type Made = { draft: Draft; verdict: DraftVerdict };

// PinDialog says what pinning does and what gets pinned, read from the
// declared block, then makes the draft. A refusal or an unchanged answer
// shows the server's sentence with Cancel alone. A draft that refuses a
// line opens on its own page. One with nothing to read publishes at once,
// and any other opens the publish dialog.
function PinDialog({ app, declared, toolCount, open, onOpenChange, onPublished }: PinProps) {
  const w: Nouns = nounsOf(declared);
  const [busy, setBusy] = React.useState<"draft" | "publish" | null>(null);
  const [said, setSaid] = React.useState<SaveNote | null>(null);
  const [made, setMade] = React.useState<Made | null>(null);
  const [publishing, setPublishing] = React.useState(false);
  React.useEffect(() => { if (open) setSaid(null); }, [open]);
  const { arg, n } = argumentOf(declared);

  const post = async (): Promise<DraftAnswer | null> => {
    try {
      const answer = await draftAppObjects(app.id);
      if ("unchanged" in answer) { setSaid({ tone: "kept", lines: [answer.sentence] }); return null; }
      return answer;
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setSaid(err.unreachable ? { tone: "unreachable", lines: [WRITE_UNCONFIRMED] } : { tone: "refused", lines: errorLines(err) });
      return null;
    }
  };

  const toDraft = (id: string) => { onOpenChange(false); navigate("drafts", [id]); };
  const done = () => {
    onOpenChange(false);
    setPublishing(false);
    setMade(null);
    notify.ok(pinnedToast(app.name, w));
    onPublished();
  };

  const saveDraft = async () => {
    setBusy("draft");
    setSaid(null);
    const answer = await post();
    setBusy(null);
    if (!answer) return;
    notify.ok(savedToDraft(answer.draft.id));
    toDraft(answer.draft.id);
  };

  // publishNow publishes a draft whose verdict leaves nothing to read. A
  // 409 with a verdict sends a refused line to the draft's page and opens
  // the publish dialog on a line that appeared since. Any other refusal
  // stays in the dialog with the draft that holds the change.
  const publishNow = async (m: Made) => {
    try {
      await publishDraft(m.draft.id, { revision: m.draft.revision, risk_digest: m.verdict.risk_digest || "", ticked: [], typed: {} });
      done();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      if (err.unreachable) { setSaid({ tone: "unreachable", lines: [WRITE_UNCONFIRMED], draft: m.draft.id }); return; }
      const body = refusalOf(err);
      if (body && body.verdict) {
        if (refusedLines(body.verdict).length) { toDraft(m.draft.id); return; }
        setMade({ ...m, verdict: body.verdict });
        onOpenChange(false);
        setPublishing(true);
        return;
      }
      const waits = err.status === 403 || body?.code === "second_person";
      setSaid({ tone: waits ? "waits" : "refused", lines: [body ? body.error : err.message, waits ? waitsLine(m.draft.id) : heldLine(m.draft.id)], draft: m.draft.id });
    }
  };

  const pinAndPublish = async () => {
    setBusy("publish");
    setSaid(null);
    try {
      const answer = await post();
      if (!answer) return;
      const m = { draft: answer.draft, verdict: answer.verdict };
      if (refusedLines(m.verdict).length) { toDraft(m.draft.id); return; }
      if (nothingToRead(m.verdict)) { await publishNow(m); return; }
      setMade(m);
      onOpenChange(false);
      setPublishing(true);
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <AlertDialog open={open} onOpenChange={(o) => { if (!busy) onOpenChange(o); }}>
        <AlertDialogContent className="sm:max-w-[600px]" data-pin-dialog>
          <AlertDialogHeader>
            <AlertDialogTitle>{pinTitle(app.name, w)}</AlertDialogTitle>
            <AlertDialogDescription className="text-sm leading-relaxed text-text-2">{pinBody(w)}</AlertDialogDescription>
          </AlertDialogHeader>
          {said ? <SaveNoteLine note={said} /> : (
            <>
              <div className="[&_dt]:tracking-normal [&_dt]:normal-case sm:[&_dl]:grid-cols-[130px_minmax(0,1fr)]">
              <Facts facts={[
                { label: PIN_FACT.type, body: typeFact(w) },
                { label: PIN_FACT.from, body: readFrom(arg, n, Math.max(n, toolCount)) },
                { label: PIN_FACT.list, body: <><code className={CODE}>{declared.list.tool}</code>{listedBy(w)}</> },
              ]} />
              </div>
              <p className="m-0 flex items-center gap-1.5 text-[13px] text-muted-foreground" data-pin-foot>
                {pinFoot(app.name)}
                <HelpTip label={PIN_FOOT_LABEL} text={pinFootHelp(app.name, w)} />
              </p>
            </>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={!!busy}>{CANCEL}</AlertDialogCancel>
            {!said && (
              <>
                <Button variant="outline" onClick={() => void saveDraft()} disabled={!!busy} aria-busy={busy === "draft" || undefined}>{busy === "draft" && <Loader2Icon className="animate-spin" />}{SAVE_DRAFT}</Button>
                <Button onClick={() => void pinAndPublish()} disabled={!!busy} aria-busy={busy === "publish" || undefined}>{busy === "publish" && <Loader2Icon className="animate-spin" />}{PIN_PUBLISH}</Button>
              </>
            )}
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {made && (
        <DraftPublish
          draft={made.draft}
          verdict={made.verdict}
          open={publishing}
          onOpenChange={(o) => { setPublishing(o); if (!o) notify.warn(keptLine(made.draft.id)); }}
          onPublished={done}
          onVerdict={(verdict) => setMade((m) => (m ? { ...m, verdict } : m))}
          title={publishThis(app.name)}
          warnings
          quiet
        />
      )}
    </>
  );
}
