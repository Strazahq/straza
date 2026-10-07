import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import type { ApiError, Draft, DraftFinding, DraftPublished, DraftVerdict } from "@/lib/api";
import { publishDraft, refusalOf } from "@/lib/drafts-api";
import { type Acks, acknowledged, changeCounts, firstMissing, gainStory, gainsHidden, publishBody, typedWrongly } from "@/lib/drafts-model";
import {
  ACK_MISSING,
  CANCEL,
  CUT_TYPED,
  GROUP,
  NOBODY_UNTIL,
  NOT_PUBLISHED,
  PUBLISH,
  SIGN_HELP,
  SIGN_LABEL,
  answered,
  goLive,
  publishTitle,
  publishedToast,
  signLine,
  typeLabel,
  typePrompt,
  typedWrong,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";
import { snapshot } from "@/lib/session";
import { cn } from "@/lib/utils";

// The publish dialog of a draft, shared by
// the review page and the editors' Save and publish. It asks for a tick on
// each risk and the typed text where republishing the old state cannot
// undo it, sends back the keys and the digest of the verdict on screen, and
// shows every refusal as the server answered it: publish is the server's
// decision, and this dialog only collects the acknowledgments.

export type DraftPublishProps = {
  draft: Draft;
  // verdict is the reading the person reviewed; its keys and digest travel
  // back in the publish.
  verdict: DraftVerdict;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPublished: (answer: DraftPublished) => void;
  // onVerdict takes the verdict a 409 carries, so the page shows a line
  // that appeared since the check before the next try.
  onVerdict?: (verdict: DraftVerdict) => void;
  // onRefused takes every refused publish after the dialog shows it, so an
  // editor can close the dialog on a draft that waits for someone else.
  onRefused?: (err: ApiError) => void;
  // title replaces "Publish draft {id}?" for an editor's Save and publish,
  // whose person never saw the draft's id.
  title?: string;
  // warnings lists the verdict's warnings under the lead, for an editor's
  // Save and publish, which has no review page above the dialog.
  warnings?: boolean;
  // quiet leaves the toast after a publish to the caller, whose toast
  // carries Undo.
  quiet?: boolean;
};

const LINE = "flex gap-2.5 rounded-md border bg-card px-3 py-2.5 text-sm leading-relaxed text-text-2";
// FOCUS_RING rings Cancel on any focus. The dialog opens from a mouse press
// and focuses Cancel from code, which a browser does not treat as
// focus-visible, so the button's own ring would stay off.
const FOCUS_RING = "focus:border-ring focus:ring-[3px] focus:ring-ring/50";

// sponsoredAgent is the agent that proposed the draft when the person
// publishing it is its sponsor, the case where a borrowed login matters.
function sponsoredAgent(draft: Draft): string | null {
  const me = snapshot()?.user || "";
  const p = draft.authors[0];
  return p && p.agent && me && p.sponsor === me ? p.username : null;
}

// AckLine is one risk: a tick, or the sentence with the field its typed
// text goes in, or a note where this reader cannot type it.
function AckLine({ f, acks, need, onTick, onType }: { f: DraftFinding; acks: Acks; need: boolean; onTick: (on: boolean) => void; onType: (text: string) => void }) {
  const box = cn(LINE, need ? "border-warn" : "border-border");
  if (f.ack === "typed") {
    return (
      <div className={cn(box, "flex-col gap-1.5")} data-ack={f.key} data-need={need || undefined}>
        <span>{f.sentence}</span>
        {need && typedWrongly(f, acks) && <span className="text-[13px] text-danger" data-typed-wrong>{typedWrong(f.object, f.typed as string)}</span>}
        {f.typed ? (
          <label className="flex flex-wrap items-center gap-2 text-[13px]">
            {typePrompt(f.typed)}
            <Input
              aria-label={typeLabel(f.typed)}
              value={acks.typed[f.key] || ""}
              onChange={(e) => onType(e.target.value)}
              autoComplete="off"
              spellCheck={false}
              className="h-8 w-64 max-w-full font-mono text-[13px]"
            />
          </label>
        ) : (
          <span className="text-[13px] text-muted-foreground">{CUT_TYPED}</span>
        )}
      </div>
    );
  }
  return (
    <label className={box} data-ack={f.key} data-need={need || undefined}>
      <input type="checkbox" checked={!!acks.ticked[f.key]} onChange={(e) => onTick(e.target.checked)} className="mt-1 size-4 shrink-0 accent-primary" />
      <span>{f.sentence}</span>
    </label>
  );
}

function Body({ draft, verdict, onOpenChange, onPublished, onVerdict, onRefused, title, warnings, quiet }: Omit<DraftPublishProps, "open">) {
  const [acks, setAcks] = React.useState<Acks>({ ticked: {}, typed: {} });
  const [tried, setTried] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const linesRef = React.useRef<HTMLDivElement>(null);
  const risks = verdict.risks || [];
  const missing = firstMissing(risks, acks);
  const story = gainStory(verdict.gains);
  // Nobody gains is a claim about every row, so it is left out for a reader
  // the server hid rows from.
  const lead = goLive(changeCounts(draft.items)) + (story.nobodyHolds.length && !story.gain.length && !gainsHidden(verdict) ? " " + NOBODY_UNTIL : "");
  // A line typed wrong says so on itself, so the general sentence is for a
  // line left empty or unticked.
  const leftEmpty = risks.some((f) => !acknowledged(f, acks) && !typedWrongly(f, acks));

  const publish = async () => {
    if (busy) return;
    if (missing) {
      setTried(true);
      const input = linesRef.current?.querySelector('[data-ack="' + missing + '"] input') as HTMLInputElement | null;
      input?.focus();
      return;
    }
    setBusy(true);
    setRefusal(null);
    try {
      const answer = await publishDraft(draft.id, publishBody(draft.revision, verdict, acks));
      if (!quiet) notify.ok(publishedToast(draft.id));
      onPublished(answer);
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const body = refusalOf(err);
      if (body && body.verdict && onVerdict) onVerdict(body.verdict);
      setRefusal(answered(err));
      onRefused?.(err);
    } finally {
      setBusy(false);
    }
  };

  const agent = sponsoredAgent(draft);
  return (
    <>
      <DialogHeader>
        <DialogTitle>{title || publishTitle(draft.id)}</DialogTitle>
        <DialogDescription className="text-sm leading-relaxed text-text-2">{lead}</DialogDescription>
      </DialogHeader>

      {warnings && (verdict.warnings || []).length > 0 && (
        <div className="flex flex-col gap-1.5" data-warnings>
          <p className="m-0 text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground">{GROUP.warning}</p>
          {verdict.warnings.map((f) => <p key={f.key} className="m-0 border-l-[3px] border-warn bg-card px-3 py-2 text-sm leading-relaxed text-text-2">{f.sentence}</p>)}
        </div>
      )}

      {risks.length > 0 && (
        <div ref={linesRef} className="flex flex-col gap-2" data-acks>
          {risks.map((f) => (
            <AckLine
              key={f.key}
              f={f}
              acks={acks}
              need={tried && !acknowledged(f, acks)}
              onTick={(on) => setAcks((a) => ({ ...a, ticked: { ...a.ticked, [f.key]: on } }))}
              onType={(text) => setAcks((a) => ({ ...a, typed: { ...a.typed, [f.key]: text } }))}
            />
          ))}
        </div>
      )}
      {tried && missing && leftEmpty && <p className="m-0 text-sm text-danger" data-ack-missing>{ACK_MISSING}</p>}

      <p className="flex items-start gap-2 border-t border-border pt-3 text-sm leading-relaxed text-text-2" data-sign>
        <span>{signLine(agent)}</span>
        <HelpTip label={SIGN_LABEL} text={SIGN_HELP} className="mt-0.5" />
      </p>

      {refusal && <RefusedError subject={PUBLISH} heading={NOT_PUBLISHED} message={refusal} />}

      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy} className={FOCUS_RING} data-cancel>{CANCEL}</Button>
        <Button onClick={() => void publish()} disabled={busy} data-publish>{busy && <Loader2Icon className="animate-spin" />}{PUBLISH}</Button>
      </DialogFooter>
    </>
  );
}

export function DraftPublish({ open, ...rest }: DraftPublishProps) {
  return (
    <Dialog open={open} onOpenChange={rest.onOpenChange}>
      <DialogContent
        className="max-h-[90vh] overflow-y-auto sm:max-w-[560px]"
        data-draft-publish={rest.draft.id}
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).querySelector<HTMLButtonElement>("[data-cancel]")?.focus();
        }}
      >
        <Body {...rest} />
      </DialogContent>
    </Dialog>
  );
}
