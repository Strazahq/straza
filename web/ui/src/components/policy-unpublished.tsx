import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { HelpTip } from "@/components/help-tip";
import type { Postures } from "@/lib/policy-model";
import { DISCARD, SAVE, SAVE_HELP, SHOW_CHANGE, UNPUBLISHED_HELP, liveNow, onPage, storedNow, unpublished } from "@/lib/policy-words";
import { SAVE_PUBLISH } from "@/lib/save-words";

// The unpublished bar of a policy's page: every edit lands here and
// nowhere else until Save draft stores the text as the set's saved edit or
// Save and publish makes it the version that runs. The bar is pinned under
// the tabs while the page
// scrolls, so what is unpublished is never off screen.

type Props = {
  // changes are the rule ids and the fact words the edits touched, in the
  // order they were first touched.
  changes: string[];
  // blocked is the first parse problem of the working text, which stops a
  // publish; the button keeps its focus and says it on hover.
  blocked: string | null;
  busy: boolean;
  // pagePostures counts the rules of the page's document and
  // livePostures the rules of the stored summary, so the bar says what the
  // publish would move.
  pagePostures: Postures;
  livePostures: Postures;
  // stale says the stored text is not the version that runs, so the second
  // count names the stored text rather than claiming to be live.
  stale: boolean;
  onDiscard: () => void;
  onShowChange: () => void;
  onSave: () => void;
  onPublish: () => void;
};

export function PolicyUnpublished({ changes, blocked, busy, pagePostures, livePostures, stale, onDiscard, onShowChange, onSave, onPublish }: Props) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 rounded-md border border-link/40 bg-accent-bg px-3 py-2 text-sm" data-unpublished={changes.length}>
      <span className="text-foreground">{unpublished(changes.length)}:</span>
      <span className="min-w-0 font-mono text-[13px] text-text-2">{changes.join(", ")}</span>
      <HelpTip label={SAVE_PUBLISH} text={UNPUBLISHED_HELP} />
      <span className="text-[13px] text-muted-foreground" data-page-postures>{onPage(pagePostures)}</span>
      <span className="text-[13px] text-muted-foreground" data-live-postures={stale ? "stored" : "live"}>{stale ? storedNow(livePostures) : liveNow(livePostures)}</span>
      <span className="ml-auto flex items-center gap-2">
        <Button variant="link" size="sm" className="h-8 px-0 text-[13px]" onClick={onShowChange}>{SHOW_CHANGE}</Button>
        <Button variant="outline" size="sm" onClick={onDiscard} disabled={busy}>{DISCARD}</Button>
        <Button variant="outline" size="sm" onClick={onSave} disabled={busy}>
          {busy && <Loader2Icon className="animate-spin" />}
          {SAVE}
        </Button>
        <HelpTip label={SAVE} text={SAVE_HELP} />
        <Button
          size="sm"
          aria-disabled={blocked ? true : undefined}
          title={blocked || undefined}
          onClick={() => { if (!blocked && !busy) onPublish(); }}
        >
          {SAVE_PUBLISH}
        </Button>
      </span>
    </div>
  );
}
