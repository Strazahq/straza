import { CheckIcon } from "lucide-react";
import { Chip, Pill } from "@/components/chip";
import { CLASS_LOOK } from "@/components/draft-checks";
import { HelpTip } from "@/components/help-tip";
import { Button } from "@/components/ui/button";
import type { DraftVerdict } from "@/lib/api";
import { confirmBlocks } from "@/lib/drafts-model";
import { foldedWarnings } from "@/lib/drafts-widens";
import { BAND_PILL, DISCARD, FACT2, PUBLISH_NOT_YET, PUBLISH_OPEN, STALE_BAR, WAITS_WHOLE, passedChecks, refusedCount, toConfirm, uncheckedCount, warningCount } from "@/lib/drafts-words";

// The band under the head of an open draft, which stays in view while the
// page scrolls. Its chips count what the check found, one pill says what
// stands between the reader and Publish where the chips do not say it, and
// it holds Discard and Publish.
// Publish renders only for a reader the server says may publish, and a
// refused or stale draft holds it with the reason as its title.

const DANGER_OUTLINE = "border-danger text-danger hover:bg-danger-bg hover:text-danger";

type Props = {
  verdict: DraftVerdict;
  mayPublish: boolean;
  // refusal is the server's own sentence for a reader who may not publish.
  refusal?: string;
  // needs is the grants each object needs to publish, in sentences.
  needs: string;
  blocked: false | "stale" | "refused";
  onDiscard: () => void;
  onPublish: () => void;
};

// BAND_TONE is the tone of each count. A warning reads in the plain tone,
// so the band's warm tone stays with what widens access.
const BAND_TONE = { refused: "danger", risk: "warn", warning: "plain", unchecked: "unknown" } as const;

export function DraftBand({ verdict, mayPublish, refusal, needs, blocked, onDiscard, onPublish }: Props) {
  const blocks = confirmBlocks(verdict.risks || []).length;
  // A warning folded into a row of Widens access is said there, so it is
  // not counted again.
  const folded = foldedWarnings(verdict);
  const warnings = (verdict.warnings || []).filter((w) => !folded.has(w.key)).length;
  const counts = [
    { cls: "refused" as const, n: (verdict.refused || []).length, word: refusedCount },
    { cls: "risk" as const, n: blocks, word: toConfirm },
    { cls: "warning" as const, n: warnings, word: warningCount },
    { cls: "unchecked" as const, n: (verdict.unchecked || []).length, word: uncheckedCount },
  ].filter((c) => c.n > 0);
  const passed = (verdict.passed || []).length;
  // The pill is the first of these that applies. Warnings alone need no
  // pill, since their chip says them.
  const pill: keyof typeof BAND_PILL | null = blocked || (!mayPublish ? "waits" : blocks ? "risks" : warnings ? null : "none");
  return (
    <div className="sticky top-0 z-10 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md border border-border bg-card px-3.5 py-2.5 shadow-sm" data-publish-band>
      {/* Below the sm width the counts take the whole row, so the buttons sit under them and a pill never runs under a button. */}
      <div className="flex min-w-0 flex-1 basis-full flex-wrap items-center gap-2 sm:basis-0" data-band-counts>
        {counts.map((c) => <Chip key={c.cls} tone={BAND_TONE[c.cls]} icon={CLASS_LOOK[c.cls].icon} data-band-count={c.cls}>{c.word(c.n)}</Chip>)}
        {!counts.length && passed > 0 && <Chip tone="ok" icon={CheckIcon} data-band-count="passed">{passedChecks(passed)}</Chip>}
        {pill && (
          <span className="inline-flex items-center gap-1.5">
            <Pill data-band-pill={pill}>{BAND_PILL[pill]}</Pill>
            {pill === "waits" && <HelpTip label={FACT2.may} text={[refusal, WAITS_WHOLE, needs].filter(Boolean).join(" ")} />}
          </span>
        )}
      </div>
      <span className="flex shrink-0 flex-wrap items-center gap-2">
        <Button variant="outline" className={DANGER_OUTLINE} onClick={onDiscard}>{DISCARD}</Button>
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
