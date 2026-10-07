import { DraftChip, type ChipTone } from "@/components/draft-checks";
import type { DraftGain, GainOutcome } from "@/lib/api";
import { type GainMark, gainGroups, gainMark, gainStory } from "@/lib/drafts-model";
import { GAINS_FOOT, GAINS_HIDDEN, GAIN_COLUMN, GAIN_MARK, NOBODY_GAINS, NO_GAINS, groupLine, holdersGain, holdersLose, nobodyHolds, outcomeWords } from "@/lib/drafts-words";
import { cn } from "@/lib/utils";

// Who gains what: for each role the draft touches and
// each tool on its server, what a holder gets today and after publishing,
// in the policy words, with the holders named where the reader may read
// them. The lead sentence says who gains at publish, and who loses, since
// narrowing breaks work too. When the server left rows out for this reader
// (hidden), the lead says so and claims nothing about the whole table.

const HUE: Record<GainOutcome, string> = { runs: "text-foreground", "needs-approval": "text-warn", denied: "text-danger", "not-reachable": "text-muted-foreground", unknown: "text-unknown" };
const MARK_TONE: Record<GainMark, ChipTone> = { gains: "accent", loses: "danger", gate: "warn", same: "plain", unknown: "unknown" };
const TH = "px-3 py-2 text-left text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground";

export function DraftGains({ gains, hidden }: { gains: DraftGain[]; hidden?: string | null }) {
  const story = gainStory(gains);
  const groups = gainGroups(gains);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1 border border-l-[3px] border-border border-l-link bg-card px-3.5 py-2.5 text-sm leading-relaxed text-text-2" data-gains-lead>
        {hidden && <><b className="font-semibold text-foreground">{GAINS_HIDDEN}</b><span>{hidden}</span></>}
        {story.none ? (
          !hidden && <b className="font-semibold text-foreground">{NO_GAINS}</b>
        ) : (
          <>
            {story.gain.length ? <b className="font-semibold text-foreground">{holdersGain(story.gain)}</b> : !hidden && <b className="font-semibold text-foreground">{NOBODY_GAINS}</b>}
            {!story.gain.length && story.nobodyHolds.length > 0 && <span>{nobodyHolds(story.nobodyHolds)}</span>}
            {story.lose.length > 0 && <span className="text-warn">{holdersLose(story.lose)}</span>}
          </>
        )}
      </div>
      {groups.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border bg-card">
          <table className="w-full table-fixed border-collapse text-sm">
            <colgroup><col style={{ width: "26%" }} /><col /><col /><col style={{ width: "120px" }} /></colgroup>
            <thead className="border-b border-border">
              <tr><th className={TH}>{GAIN_COLUMN.tool}</th><th className={TH}>{GAIN_COLUMN.today}</th><th className={TH}>{GAIN_COLUMN.after}</th><th className={TH}><span className="sr-only">{GAIN_COLUMN.after}</span></th></tr>
            </thead>
            <tbody>
              {groups.map((g) => (
                <GroupRows key={g.role + "/" + g.server} line={groupLine(g.role, g.server, g.holders, g.count)} rows={g.rows} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {groups.length > 0 && <p className="m-0 text-[13px] text-muted-foreground">{GAINS_FOOT}</p>}
    </div>
  );
}

function GroupRows({ line, rows }: { line: string; rows: DraftGain[] }) {
  return (
    <>
      <tr className="border-b border-border bg-secondary/40">
        <td colSpan={4} className="px-3 py-1.5 text-[13px] font-semibold text-foreground">{line}</td>
      </tr>
      {rows.map((r) => {
        const mark = gainMark(r);
        return (
          <tr key={r.tool} className="border-b border-border last:border-b-0" data-gain={r.tool}>
            <td className="break-all px-3 py-2 align-top font-mono text-[13px] text-foreground">{r.tool}</td>
            <td className="px-3 py-2 align-top text-muted-foreground">{outcomeWords(r.before, r.before_words)}</td>
            <td className={cn("px-3 py-2 align-top", HUE[r.after])}>{outcomeWords(r.after, r.after_words)}</td>
            <td className="px-3 py-2 align-top">{mark === "same" ? <span className="text-[13px] text-muted-foreground">{GAIN_MARK.same}</span> : <DraftChip word={GAIN_MARK[mark]} tone={MARK_TONE[mark]} />}</td>
          </tr>
        );
      })}
    </>
  );
}
