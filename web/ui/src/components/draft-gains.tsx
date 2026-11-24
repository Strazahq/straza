import { Chip, OutcomeChip } from "@/components/chip";
import { NameChips } from "@/components/draft-checks";
import type { DraftCoverage, DraftGain, GainOutcome } from "@/lib/api";
import type { Outcome as ChipOutcome } from "@/lib/chip-words";
import { type AccessGroup, accessGroups, coverageNouns, objectsMove } from "@/lib/drafts-access";
import { gainMark } from "@/lib/drafts-model";
import {
  GAINS_HIDDEN,
  GAIN_COLUMN,
  NOTHING,
  NO_GAINS,
  OUTCOME,
  coveredWords,
  everyType,
  groupLine,
  objectsGained,
  objectsLabel,
  objectsLost,
  outcomeFoot,
  sameFold,
  toolCount,
} from "@/lib/drafts-words";

// Access after publishing: for each role the draft touches, what a holder
// gets now and after publishing, as outcome chips in the policy words. On
// a server that pins an object type the group also says which objects the
// role covers, from the verdict's coverage list, so a role that only
// reaches more objects still shows. Tools whose outcome does not change
// fold away, and a gate's words are said once under the table. When the
// server left rows out for this reader (hidden), the section says so and
// claims nothing about the whole table.

// GAIN_OUTCOME reads a gain's outcome as one of the four outcomes. unknown
// is none of them and keeps its own chip.
const GAIN_OUTCOME: Partial<Record<GainOutcome, ChipOutcome>> = { runs: "allowed", "needs-approval": "approval", denied: "denied", "not-reachable": "none" };
const TH = "px-3 py-2 text-left text-caption font-semibold text-muted-foreground";
const TD = "px-3 py-2 align-top";

type Props = {
  gains: DraftGain[];
  objects?: DraftCoverage[];
  // toolsOf answers a role's tools on a server after publishing, or null
  // when the page does not hold the role's document.
  toolsOf?: (role: string, server: string) => string[] | null;
  // isNew says whether the draft adds a role, so what it covers is where it
  // starts, not a gain. Absent, no role counts as new.
  isNew?: (role: string) => boolean;
  hidden?: string | null;
};

export function DraftGains({ gains, objects, toolsOf, isNew, hidden }: Props) {
  const groups = accessGroups(gains, objects, toolsOf || (() => null), isNew || (() => false));
  // Groups on several servers name their server, so a role reached on two
  // servers through its closure does not read the same twice.
  const named = new Set(groups.map((g) => g.server)).size > 1;
  // A gate's words are said once under the table, one line per outcome and
  // words, in the order the rows name them.
  const foot = [...new Set(gains.flatMap((g) => [g.before_words ? outcomeFoot(g.before, g.before_words) : "", g.after_words ? outcomeFoot(g.after, g.after_words) : ""]).filter(Boolean))];
  return (
    <div className="flex flex-col gap-3">
      {hidden && (
        <div className="flex max-w-[75ch] flex-col gap-1 border border-l-[3px] border-border border-l-link bg-card px-3.5 py-2.5 text-body leading-relaxed text-text-2" data-gains-hidden>
          <b className="font-semibold text-foreground">{GAINS_HIDDEN}</b>
          <span>{hidden}</span>
        </div>
      )}
      {!groups.length && !hidden && <p className="m-0 max-w-[75ch] text-body text-text-2" data-no-gains>{NO_GAINS}</p>}
      {groups.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border bg-card">
          <table className="w-full table-fixed border-collapse text-dense" style={{ minWidth: 520 }}>
            <colgroup><col style={{ width: "34%" }} /><col /><col /></colgroup>
            <thead className="border-b border-border">
              <tr><th className={TH}>{GAIN_COLUMN.tool}</th><th className={TH}>{GAIN_COLUMN.now}</th><th className={TH}>{GAIN_COLUMN.after}</th></tr>
            </thead>
            <tbody>
              {groups.map((g) => <GroupRows key={g.role + "/" + g.server} group={g} named={named} />)}
            </tbody>
          </table>
        </div>
      )}
      {foot.length > 0 && <div className="flex flex-col gap-0.5 text-caption text-muted-foreground" data-gains-foot>{foot.map((l) => <p key={l} className="m-0">{l}</p>)}</div>}
    </div>
  );
}

const Outcome = ({ o, words }: { o: GainOutcome; words?: string }) => {
  const outcome = GAIN_OUTCOME[o];
  return outcome ? <OutcomeChip outcome={outcome} title={words} /> : <Chip tone="unknown" title={words} data-outcome={OUTCOME[o] || o}>{OUTCOME[o] || o}</Chip>;
};

// line words the group's head: the role, on its server when named, who
// holds it, the objects it covers when no row shows them, and the tools and
// objects a holder gains and loses. The objects of a role the draft adds
// are where it starts, so they read "on", never as a gain.
function line(g: AccessGroup, named: boolean): string {
  const gains = g.rows.filter((r) => gainMark(r) === "gains").length;
  const loses = g.rows.filter((r) => gainMark(r) === "loses").length;
  const server = named ? g.server : "";
  const c = g.coverage;
  if (!c) return groupLine(g.role, server, g.holders, g.count, "", gains ? [toolCount(gains)] : [], loses ? [toolCount(loses)] : []);
  const w = coverageNouns(c);
  const m = objectsMove(c);
  const moved = m.changed && !g.isNew;
  const on = moved ? "" : coveredWords(w, c.every_after, c.after);
  return groupLine(g.role, server, g.holders, g.count, on, [gains ? toolCount(gains) : "", moved ? objectsGained(w, m) : ""].filter(Boolean), [loses ? toolCount(loses) : "", moved ? objectsLost(w, m) : ""].filter(Boolean));
}

function GroupRows({ group: g, named }: { group: AccessGroup; named: boolean }) {
  const c = g.coverage;
  const m = c ? objectsMove(c) : null;
  return (
    <>
      <tr className="border-b border-border bg-secondary/40" data-gain-group={g.role} data-gain-server={g.server}>
        <td colSpan={3} className="px-3 py-1.5 text-caption font-semibold text-foreground">{line(g, named)}</td>
      </tr>
      {c && m && m.changed && !g.isNew && <ObjectsRow c={c} />}
      {g.rows.map((r) => {
        // A row whose outcome stays and whose gate changes differs only in
        // its words, so that row says them in its cells.
        const gate = gainMark(r) === "gate";
        return (
          <tr key={r.tool} className="border-b border-border last:border-b-0" data-gain={r.tool}>
            <td className={TD + " break-all font-mono text-caption text-foreground"}>{r.tool}</td>
            <td className={TD}><Outcome o={r.before} words={r.before_words} />{gate && r.before_words && <span className="mt-1 block text-caption text-muted-foreground">{r.before_words}</span>}</td>
            <td className={TD}><Outcome o={r.after} words={r.after_words} />{gate && r.after_words && <span className="mt-1 block text-caption text-muted-foreground">{r.after_words}</span>}</td>
          </tr>
        );
      })}
      {g.same.length > 0 && (
        <tr className="border-b border-border last:border-b-0" data-gain-same={g.role}>
          <td colSpan={3} className={TD}>
            <details>
              <summary className="cursor-pointer text-dense text-link">{sameFold(g.same.length)}</summary>
              <div className="mt-1.5"><NameChips names={g.same} /></div>
            </details>
          </td>
        </tr>
      )}
    </>
  );
}

// ObjectsRow is the objects a role covers now and after publishing, in its
// server's own word, nothing where it covers none. The group line above it
// names the ones added.
function ObjectsRow({ c }: { c: DraftCoverage }) {
  const w = coverageNouns(c);
  const cell = (every: boolean, names: string[]) => (every ? <Chip tone="accent">{everyType(w)}</Chip> : names.length ? <NameChips names={names} /> : <span className="text-muted-foreground">{NOTHING}</span>);
  return (
    <tr className="border-b border-border last:border-b-0" data-gain-objects={c.role}>
      <td className={TD + " text-foreground"}>{objectsLabel(w)}</td>
      <td className={TD}>{cell(c.every_before, c.before.map((o) => o.value))}</td>
      <td className={TD}>{cell(c.every_after, c.after.map((o) => o.value))}</td>
    </tr>
  );
}
