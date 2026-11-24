import * as React from "react";
import { MinusIcon, WrenchIcon } from "lucide-react";
import { Chip, OutcomeChip, Pill, ToolChip } from "@/components/chip";
import { ChipDiff } from "@/components/draft-chips";
import { NameChips } from "@/components/draft-checks";
import { Arrow, Facts, FiveMore, HoldersPill, Muted, ObjectTag } from "@/components/draft-parts";
import { coversKey } from "@/lib/access-covers";
import { coversOn, coversText } from "@/lib/access-object-words";
import type { DraftCoverage, DraftGain } from "@/lib/api";
import { OUTCOME_WORD, type Outcome } from "@/lib/chip-words";
import { coverageNouns, objectsMove } from "@/lib/drafts-access";
import type { RoleView, ToolsMove } from "@/lib/drafts-model";
import { GAIN_OUTCOME, OUTCOME_RANK, type SetReading, machineRows, rulesNaming } from "@/lib/drafts-rules";
import { CARD, DESCRIPTION, EVERY_TOOL_PILL, IMPLIES, IN, NOTHING, NOT_READABLE_DOC, NO_SERVER, everyType, objectsLabel, toolCount, toolsOn } from "@/lib/drafts-words";
import { list } from "@/lib/policy-words";

// The body of a role's card on a draft's page: its description, who holds
// it, the tools it reaches behind Straza, the servers on a person's machine
// the draft's policy rules open to it, and the policy rules that name it. A
// changed role keeps its rows of what changes: the tools that join and leave
// per server, the roles it implies and the objects it covers.

// coverOf answers a role's coverage entry on a server, or undefined where
// the server pins no type or sent no list, so that row keeps the covers
// line as the file writes it.
export type CoverOf = (role: string, app: string) => DraftCoverage | undefined;

type Row = { key?: string; label: string; value: React.ReactNode };

// toolTitle is the hover of a tool chip with its outcome.
const toolTitle = (tool: string, o: Outcome) => tool + ": " + OUTCOME_WORD[o];

// ToolLines are a role's tools on one server. Where the gain rows tell a
// tool's outcome after publishing, the tools sit under their outcome, one
// line each, needs approval first, and the tools no row names follow
// plain.
function ToolLines({ tools, gains }: { tools: string[]; gains: DraftGain[] }) {
  const outcome = new Map<string, Outcome>();
  for (const g of gains) {
    const o = GAIN_OUTCOME[g.after];
    if (o) outcome.set(g.tool, o);
  }
  const names = [...new Set([...tools.filter((t) => t !== "*"), ...outcome.keys()])];
  const known = [...new Set(names.map((t) => outcome.get(t)).filter((o) => o !== undefined))].sort((a, b) => OUTCOME_RANK[a] - OUTCOME_RANK[b]);
  const plain = names.filter((t) => !outcome.has(t));
  return (
    <>
      {known.map((o) => (
        <div key={o} className="flex w-full flex-wrap items-center gap-1.5" data-tool-line={o}>
          <OutcomeChip outcome={o} />
          <FiveMore items={names.filter((t) => outcome.get(t) === o).map((t) => <ToolChip key={t} outcome={o} title={toolTitle(t, o)}>{t}</ToolChip>)} />
        </div>
      ))}
      {plain.length > 0 && (
        <div className="flex w-full flex-wrap items-center gap-1.5" data-tool-line="plain">
          <FiveMore items={plain.map((t) => <ToolChip key={t}>{t}</ToolChip>)} />
        </div>
      )}
    </>
  );
}

type Reach = { app: string; tools: string[] }[];

// gainsReach is a role's reach as the gain rows tell it, grouped by
// server. A business role has no access row of its own and reaches through
// the roles it implies, and only the server's gain rows walk that closure.
function gainsReach(gains: DraftGain[]): Reach {
  const out: Reach = [];
  for (const g of gains) {
    if (g.after === "not-reachable") continue;
    let b = out.find((x) => x.app === g.server);
    if (!b) out.push((b = { app: g.server, tools: [] }));
    if (!b.tools.includes(g.tool)) b.tools.push(g.tool);
  }
  return out;
}

// Behind is a new role's reach on the servers behind Straza: per server its
// tag, how many tools, and the tools.
function Behind({ reach, gains }: { reach: Reach; gains: DraftGain[] }) {
  if (!reach.length) return <Pill icon={MinusIcon}>{NO_SERVER}</Pill>;
  return (
    <>
      {reach.map((b) => (
        <div key={b.app} className="flex w-full flex-col gap-1.5" data-behind={b.app}>
          <span className="flex flex-wrap items-center gap-2">
            <ObjectTag kind="server" name={b.app} />
            <Pill icon={WrenchIcon}>{b.tools.includes("*") ? EVERY_TOOL_PILL : toolCount(b.tools.length)}</Pill>
          </span>
          <ToolLines tools={b.tools} gains={gains.filter((g) => g.server === b.app)} />
        </div>
      ))}
    </>
  );
}

// Objects names the objects a new role covers, every object as one chip,
// and nothing where it covers none.
function Objects({ c }: { c: DraftCoverage }) {
  if (c.every_after) return <Chip tone="accent">{everyType(coverageNouns(c))}</Chip>;
  const names = c.after.map((o) => o.value);
  return names.length ? <NameChips names={names} /> : <span className="text-muted-foreground">{NOTHING}</span>;
}

// ObjectsDiff is a changed role's objects as one row of chips: what it
// gains, what it drops, and what it keeps, with every object as one chip.
function ObjectsDiff({ c }: { c: DraftCoverage }) {
  const every = everyType(coverageNouns(c));
  const m = objectsMove(c);
  const values = (os: { value: string }[]) => os.map((o) => o.value);
  if (m.every === "kept") return <ChipDiff added={[]} removed={[]} same={[every]} />;
  if (m.every === "gained") return <ChipDiff added={[every]} removed={[]} same={values(c.before)} />;
  if (m.every === "lost") return <ChipDiff added={[]} removed={[every]} same={values(c.after)} />;
  const added = new Set(values(m.added));
  return <ChipDiff added={[...added]} removed={values(m.dropped)} same={values(c.after).filter((v) => !added.has(v))} />;
}

// Changed is a field a changed role's draft writes differently: the value
// now, the arrow, and the value after publishing.
const Changed = ({ was, now }: { was: string; now: string }) => (
  <><span className="text-muted-foreground" data-was>{was}</span><Arrow /><span data-now>{now}</span></>
);

type Props = {
  name: string;
  after: RoleView | null;
  before: RoleView | null;
  tools: ToolsMove[];
  coverOf: CoverOf;
  // holders is how many hold the role, null where the page does not know.
  holders: number | null;
  // gains are the verdict's rows for this role, empty on a decided draft.
  gains: DraftGain[];
  sets: SetReading[];
  // registered is the registered server list after publishing, null where
  // the page cannot tell a server on a person's machine apart.
  registered: string[] | null;
};

export function RoleBody({ name, after, before, tools, coverOf, holders, gains, sets, registered }: Props) {
  if (!after) return <p className="m-0 text-dense text-text-2">{NOT_READABLE_DOC}</p>;
  const rows: Row[] = [];
  const one = new Set([...after.bindings, ...(before?.bindings || [])].map((b) => b.app)).size <= 1;
  if (before && before.description !== after.description) rows.push({ label: DESCRIPTION, value: <Changed was={before.description || NOTHING} now={after.description || NOTHING} /> });
  else if (after.description) rows.push({ label: DESCRIPTION, value: <span className="max-w-[75ch]">{after.description}</span> });
  if (holders !== null) rows.push({ label: CARD.holders, value: <HoldersPill n={holders} /> });
  if (!before) {
    // A role with no access row reaches what the gain rows tell. Where they
    // tell nothing, as on a published draft's card, a role that implies
    // others keeps its Implies row and has no Behind Straza row.
    const reach = after.bindings.length ? after.bindings : gainsReach(gains);
    if (reach.length || !after.implies.length) rows.push({ label: CARD.behind, value: <Behind reach={reach} gains={gains} /> });
    for (const b of after.bindings) {
      const c = coverOf(name, b.app);
      if (c) rows.push({ key: "objects/" + b.app, label: objectsLabel(coverageNouns(c)), value: <Objects c={c} /> });
      else if (b.covers) rows.push({ key: "covers/" + b.app, label: coversOn(b.app), value: <span className="font-mono text-caption">{coversText(b.covers)}</span> });
    }
    if (after.implies.length) rows.push({ label: IMPLIES, value: list(after.implies) });
  } else {
    if (list(before.implies) !== list(after.implies)) rows.push({ label: IMPLIES, value: <Changed was={list(before.implies) || NOTHING} now={list(after.implies) || NOTHING} /> });
    if (!tools.length && !after.implies.length) rows.push({ label: CARD.behind, value: <Pill icon={MinusIcon}>{NO_SERVER}</Pill> });
    // Each server reads as one row of tool chips, then its objects. Where
    // the server sent a coverage entry, the objects are compared by id, so
    // a value written differently is no change. Elsewhere the files carry
    // values, not ids, so a covers line the draft writes differently is a
    // row of its own.
    for (const m of tools) {
      rows.push({ key: "tools/" + m.server, label: toolsOn(m.server, one), value: <ChipDiff added={m.added} removed={m.removed} same={m.same} /> });
      const c = coverOf(name, m.server);
      if (c) {
        rows.push({ key: "objects/" + m.server, label: objectsLabel(coverageNouns(c)), value: <ObjectsDiff c={c} /> });
        continue;
      }
      const was = before.bindings.find((b) => b.app === m.server)?.covers;
      const now = after.bindings.find((b) => b.app === m.server)?.covers;
      if (coversKey(was) !== coversKey(now)) rows.push({ key: "covers/" + m.server, label: coversOn(m.server), value: <Changed was={coversText(was)} now={coversText(now)} /> });
    }
  }
  const machine = registered ? machineRows(sets, name, registered) : [];
  if (machine.length) {
    rows.push({
      label: CARD.machine,
      value: machine.map((m) => (
        <div key={m.set + "/" + m.rule.id + "/" + m.server} className="flex w-full flex-col gap-1.5" data-machine={m.server}>
          <span className="flex flex-wrap items-center gap-2">
            <OutcomeChip outcome={m.outcome} />
            <ObjectTag kind="local-server" name={m.server} />
            <Pill icon={WrenchIcon}>{m.rule.names ? toolCount(m.rule.names.length) : EVERY_TOOL_PILL}</Pill>
          </span>
          {m.rule.names && (
            <span className="flex flex-wrap items-center gap-1.5">
              <FiveMore items={m.rule.names.map((t) => <ToolChip key={t} outcome={m.outcome} title={toolTitle(t, m.outcome)}>{t}</ToolChip>)} />
            </span>
          )}
        </div>
      )),
    });
  }
  const naming = rulesNaming(sets, name);
  if (naming.length) {
    const many = naming.reduce((n, g) => n + g.rules.length, 0) > 1;
    rows.push({
      label: many ? CARD.rules : CARD.rule,
      value: naming.map((g) => (
        <span key={g.set} className="flex w-full flex-wrap items-center gap-2" data-rules-in={g.set}>
          <FiveMore limit={3} items={g.rules.map((id) => <span key={id} className="font-mono text-caption break-all text-foreground">{id}</span>)} />
          <Muted>{IN}</Muted>
          <ObjectTag kind="policy-set" name={g.set} />
        </span>
      )),
    });
  }
  return <Facts rows={rows} />;
}
