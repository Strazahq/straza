import { VerdictStrip } from "@/components/draft-checks";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import type { DraftVerdict } from "@/lib/api";
import { BAND_LABEL, FACT, GO_TO_CHECKS, MAY_PUBLISH_ALL, PUBLISH_NOT_YET, PUBLISH_OPEN, STALE_BAR } from "@/lib/drafts-words";

// Before you publish: the band under the head of an open draft, which stays
// in view while the page scrolls. It counts the verdict's lines, says who
// may publish with the grants each object needs behind its help, says what
// to do before Publish, and holds Discard and Publish. Publish renders only
// for a reader the server says may publish, and a refused or stale draft
// holds it with the reason as its title.

const DANGER_OUTLINE = "border-danger text-danger hover:bg-danger-bg hover:text-danger";

type Props = {
  verdict: DraftVerdict;
  mayPublish: boolean;
  refusal?: string;
  // needs is the help of Who may publish: the grant each object needs.
  needs: string;
  // line says what to do before Publish, empty when the verdict chip
  // already says there is nothing to confirm.
  line: string;
  blocked: false | "stale" | "refused";
  discard: string;
  onJump: () => void;
  onDiscard: () => void;
  onPublish: () => void;
};

export function DraftBand({ verdict, mayPublish, refusal, needs, line, blocked, discard, onJump, onDiscard, onPublish }: Props) {
  return (
    <div className="sticky top-0 z-10 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-border bg-card px-3.5 py-2.5 shadow-sm" data-publish-band>
      <div className="flex min-w-[260px] flex-1 flex-wrap items-center gap-x-3 gap-y-1.5 text-sm text-text-2">
        <span className="font-semibold text-foreground">{BAND_LABEL}</span>
        <VerdictStrip verdict={verdict} onJump={onJump} />
        <span className="inline-flex items-center gap-1.5" data-may-publish>
          {mayPublish ? MAY_PUBLISH_ALL : refusal}
          <HelpTip label={FACT.needs} text={needs} />
        </span>
        {line && <span data-band-line>{line}</span>}
        <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={onJump}>{GO_TO_CHECKS}</Button>
      </div>
      <span className="flex shrink-0 items-center gap-2">
        <Button variant="outline" className={DANGER_OUTLINE} onClick={onDiscard}>{discard}</Button>
        {mayPublish && (
          <Button
            variant={blocked ? "outline" : "default"}
            aria-disabled={blocked ? true : undefined}
            title={blocked === "stale" ? STALE_BAR : blocked ? PUBLISH_NOT_YET : undefined}
            onClick={() => { if (!blocked) onPublish(); }}
          >
            {PUBLISH_OPEN}
          </Button>
        )}
      </span>
    </div>
  );
}
