import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { RefusedError } from "@/components/error-state";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import type { ApiError, Draft, DraftConflict } from "@/lib/api";
import { discardDraft } from "@/lib/drafts-api";
import { pickKey } from "@/lib/drafts-model";
import {
  BASE_TEXT,
  CANCEL,
  CHECK_AGAIN_SUBJECT,
  DISCARD_BODY,
  DISCARD_GO,
  DISCARD_SUBJECT,
  KEEP_DRAFT_TEXT,
  KEEP_LIVE_TEXT,
  PICKS_BODY,
  PICKS_GO,
  PICKS_MISSING,
  PICKS_TITLE,
  REASON_HINT,
  REASON_LABEL,
  SHOW_TEXT,
  answered,
  discardTitle,
  discardUnlinks,
  discardedToast,
  keepDraft,
  keepLive,
  objectWords,
  pickWas,
} from "@/lib/drafts-words";
import { notify } from "@/lib/notify";

// The two small dialogs of the review page: Discard, with the reason the
// author reads, and the picks Check again asks for when a field changed on
// both sides since the draft was checked.

type DiscardProps = { draft: Draft; open: boolean; onOpenChange: (open: boolean) => void; onDiscarded: () => void };

// unlinked is the server a discard keeps running when the draft is the
// apps directory's proposal to remove it.
export function unlinked(draft: Draft): string | null {
  if (draft.door !== "apps-directory") return null;
  const it = draft.items.find((i) => i.kind === "App" && i.op === "remove");
  return it ? it.name : null;
}

function DiscardBody({ draft, onOpenChange, onDiscarded }: Omit<DiscardProps, "open">) {
  const [reason, setReason] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [refusal, setRefusal] = React.useState<string | null>(null);
  const server = unlinked(draft);
  const discard = async () => {
    if (busy) return;
    setBusy(true);
    setRefusal(null);
    try {
      await discardDraft(draft.id, reason.trim(), draft.revision);
      notify.ok(discardedToast(draft.id));
      onDiscarded();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal(answered(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <DialogHeader>
        <DialogTitle>{discardTitle(draft.id)}</DialogTitle>
        <DialogDescription className="text-sm leading-relaxed text-text-2">{server ? discardUnlinks(server) : DISCARD_BODY}</DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-1.5">
        <label htmlFor="discard-reason" className="text-sm font-medium text-foreground">{REASON_LABEL}</label>
        <Textarea id="discard-reason" value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />
        <span className="text-[13px] text-muted-foreground">{REASON_HINT}</span>
      </div>
      {refusal && <RefusedError subject={DISCARD_SUBJECT} message={refusal} />}
      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy} data-cancel>{CANCEL}</Button>
        <Button variant="destructive" onClick={() => void discard()} disabled={busy}>{busy && <Loader2Icon className="animate-spin" />}{DISCARD_GO}</Button>
      </DialogFooter>
    </>
  );
}

export function DiscardDialog({ open, ...rest }: DiscardProps) {
  return (
    <Dialog open={open} onOpenChange={rest.onOpenChange}>
      <DialogContent className="sm:max-w-[520px]" data-discard-dialog onOpenAutoFocus={(e) => { e.preventDefault(); (e.currentTarget as HTMLElement).querySelector<HTMLButtonElement>("[data-cancel]")?.focus(); }}>
        <DiscardBody {...rest} />
      </DialogContent>
    </Dialog>
  );
}

type PicksProps = {
  conflicts: DraftConflict[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  busy: boolean;
  // refusal is the server's sentence for the last try, shown as answered.
  refusal: string | null;
  onPick: (picks: Record<string, "draft" | "live">) => void;
};

// textual says a field's values run over several lines, as a set's text
// does, so each value goes in a fold instead of into a sentence or a label.
const textual = (c: DraftConflict) => [c.base, c.draft, c.live].some((v) => v.includes("\n"));

function TextFold({ summary, text, className }: { summary: string; text: string; className?: string }) {
  return (
    <details className={className}>
      <summary className="cursor-pointer text-[13px] text-muted-foreground hover:text-foreground">{summary}</summary>
      <pre className="mt-1 max-h-48 overflow-auto rounded-md border border-border bg-secondary/40 px-3 py-2 font-mono text-[12.5px] leading-relaxed text-foreground">{text}</pre>
    </details>
  );
}

function PicksBody({ conflicts, onOpenChange, busy, refusal, onPick }: Omit<PicksProps, "open">) {
  const [picks, setPicks] = React.useState<Record<string, "draft" | "live">>({});
  const [tried, setTried] = React.useState(false);
  const listRef = React.useRef<HTMLDivElement>(null);
  const missing = conflicts.find((c) => !picks[pickKey(c)]);
  const go = () => {
    if (missing) {
      setTried(true);
      listRef.current?.querySelector<HTMLInputElement>('[data-pick="' + pickKey(missing) + '"] input')?.focus();
      return;
    }
    onPick(picks);
  };
  return (
    <>
      <DialogHeader>
        <DialogTitle>{PICKS_TITLE}</DialogTitle>
        <DialogDescription className="text-sm leading-relaxed text-text-2">{PICKS_BODY}</DialogDescription>
      </DialogHeader>
      <div ref={listRef} className="flex flex-col gap-3">
        {conflicts.map((c) => {
          const key = pickKey(c);
          return (
            <fieldset key={key} className="m-0 flex flex-col gap-1.5 rounded-md border border-border p-3 text-sm" data-pick={key}>
              <legend className="px-1 font-semibold text-foreground">{objectWords(c.object)} <span className="font-mono text-[13px]">{c.field}</span></legend>
              {textual(c) ? <TextFold summary={BASE_TEXT} text={c.base} /> : <span className="text-[13px] text-muted-foreground">{pickWas(c.base)}</span>}
              {(["draft", "live"] as const).map((side) => (
                <div key={side} className="flex flex-col gap-1">
                  <label className="flex items-center gap-2 text-text-2">
                    <input type="radio" name={key} checked={picks[key] === side} onChange={() => setPicks((p) => ({ ...p, [key]: side }))} className="size-4 accent-primary" />
                    {textual(c) ? (side === "draft" ? KEEP_DRAFT_TEXT : KEEP_LIVE_TEXT) : side === "draft" ? keepDraft(c.draft) : keepLive(c.live)}
                  </label>
                  {textual(c) && <TextFold summary={SHOW_TEXT} text={side === "draft" ? c.draft : c.live} className="ml-6" />}
                </div>
              ))}
            </fieldset>
          );
        })}
      </div>
      {tried && missing && <p className="m-0 text-sm text-danger">{PICKS_MISSING}</p>}
      {refusal && <RefusedError subject={CHECK_AGAIN_SUBJECT} message={refusal} />}
      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy} data-cancel>{CANCEL}</Button>
        <Button onClick={go} disabled={busy}>{busy && <Loader2Icon className="animate-spin" />}{PICKS_GO}</Button>
      </DialogFooter>
    </>
  );
}

export function PicksDialog({ open, ...rest }: PicksProps) {
  return (
    <Dialog open={open} onOpenChange={rest.onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-[560px]" data-picks-dialog onOpenAutoFocus={(e) => { e.preventDefault(); (e.currentTarget as HTMLElement).querySelector<HTMLButtonElement>("[data-cancel]")?.focus(); }}>
        <PicksBody {...rest} />
      </DialogContent>
    </Dialog>
  );
}
