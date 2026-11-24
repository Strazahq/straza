import * as React from "react";
import { ArrowRightIcon, CheckIcon, ChevronRightIcon, PencilIcon, UserRoundIcon, WrenchIcon } from "lucide-react";
import { ChangeChip, Chip, OutcomeChip, Pill, ToolChip } from "@/components/chip";
import { ChipDiff } from "@/components/draft-chips";
import { FiveMore, HoldersPill, Muted, ObjectTag } from "@/components/draft-parts";
import { GatePills, RuleOutcome } from "@/components/draft-set-card";
import { Where } from "@/components/policy-rules";
import type { DraftCoverage, DraftDetail, DraftFinding } from "@/lib/api";
import type { Kind } from "@/lib/chip-words";
import { coverageNouns, objectsMove } from "@/lib/drafts-access";
import { type ConfirmBlock, agentAccepts, iconKind, objectOf, roleOf } from "@/lib/drafts-model";
import { GAIN_OUTCOME, type Marked, namesMove, outcomeOf, ruleAspects } from "@/lib/drafts-rules";
import type { Side, ToolRow, Widens } from "@/lib/drafts-widens";
import {
  DETAILS, EVERYONE_PILL, NOT_REGISTERED, NOTHING, ON, OUTCOME, SAME_AS_NOW, SHORT_DOES, VERSION_COLUMN, WIDENS_COLUMN,
  ackWords, appliesTo, checkLines, everyType, machineNote, objectsLabel, toolCount, undoLine,
} from "@/lib/drafts-words";
import { EVERY_WORD } from "@/lib/policy-words";
import { snapshot } from "@/lib/session";
import { cn } from "@/lib/utils";

// The body of Widens access under its heading: the draft's objects the
// lines name, then one table per kind of row, each row one tool and change
// with the roles it changes for, a policy rule a changed policy loosens, or
// the objects a role comes to cover. The lines no row can say follow in
// the server's words, then how the publish dialog asks to confirm them,
// once per block, and whether publishing the old version undoes them, once,
// for a draft that changes or removes an object that exists today.
// Details holds the versions the lines name and the check codes, the one
// place a fingerprint shows. Below the 3xl container width every row is a
// card with its labels.

// GRID is the one column template of every table of the section, so their
// boxes line up.
const GRID = "grid grid-cols-1 @3xl:grid-cols-[minmax(150px,1fr)_minmax(185px,0.9fr)_minmax(200px,1.3fr)_18px_minmax(170px,1.1fr)]";
const HEAD = "hidden border-b border-border bg-secondary/40 px-2.5 py-2 text-caption font-semibold text-muted-foreground first:pl-3.5 @3xl:block";
const CELL = "flex min-w-0 flex-wrap content-start items-center gap-1.5 px-3.5 py-1 text-caption @3xl:px-2.5 @3xl:py-2.5";
const LINE = "border-b border-border";
const CODE = "rounded-md border border-border bg-secondary px-1 font-mono text-caption font-semibold text-foreground";

// Label names a cell above its value when the row is a card.
const Label = ({ children }: { children: string }) => <span className="w-full text-caption text-muted-foreground @3xl:hidden">{children}</span>;

// Row is one row of a table: its first cell, the cells between, the arrow,
// the last cell, and a note under them across the row.
function Row({ heads, cells, note, ...data }: { heads: string[]; cells: React.ReactNode[]; note?: React.ReactNode } & { [key: `data-${string}`]: string }) {
  // A cell draws the row's line where the row is a table row with no note.
  // The class is written whole, since Tailwind reads it from the source.
  const edge = note ? "" : "border-border @3xl:border-b";
  return (
    <div className="contents" {...data}>
      <div className={cn(CELL, "pt-3 @3xl:pl-3.5", edge)}><Label>{heads[0]}</Label>{cells[0]}</div>
      <div className={cn(CELL, edge)}><Label>{heads[1]}</Label>{cells[1]}</div>
      <div className={cn(CELL, edge)}><Label>{heads[2]}</Label>{cells[2]}</div>
      <div className={cn("hidden justify-center py-2.5 text-muted-foreground @3xl:flex", edge)}><ArrowRightIcon className="size-4" aria-hidden="true" /></div>
      <div className={cn(CELL, "pb-3", note ? "" : LINE)}><Label>{heads[4]}</Label>{cells[3]}</div>
      {note && <p className={cn("col-span-full m-0 px-3.5 pb-2.5 text-dense leading-relaxed text-text-2", LINE)} data-row-note>{note}</p>}
    </div>
  );
}

// Table is one table of the section: a head row, then its rows.
function Table({ heads, children, kind }: { heads: string[]; children: React.ReactNode; kind: string }) {
  return (
    <div className={GRID} data-widens-table={kind}>
      {heads.map((h, i) => <div key={i} className={cn(HEAD, !h && "px-0")}>{h}</div>)}
      {children}
    </div>
  );
}

// SideChip is the outcome of one side, the gate's words as its hover where
// the server sent no gate.
function SideChip({ side }: { side: Side }) {
  const o = GAIN_OUTCOME[side.outcome];
  return o ? <OutcomeChip outcome={o} title={side.words} /> : <Chip tone="unknown" title={side.words} data-outcome={side.outcome}>{OUTCOME[side.outcome] || side.outcome}</Chip>;
}

const sameSide = (a: Side, b: Side) => JSON.stringify(a) === JSON.stringify(b);

function SideCell({ side, was }: { side: Side; was?: Side }) {
  if (was && sameSide(side, was)) return <><SideChip side={side} /><Muted>{SAME_AS_NOW}</Muted></>;
  return <><SideChip side={side} />{side.gate && <GatePills gate={side.gate} />}</>;
}

// roleKindOf answers the kind icon of a role the draft holds, and null for
// another role.
const roleKindOf = (d: DraftDetail) => (role: string): Kind | null => {
  const it = d.draft.items.find((x) => x.kind === "Role" && x.name === role);
  return it ? iconKind("Role", roleOf(it.doc)) : null;
};

function ToolCell({ r }: { r: ToolRow }) {
  const where = (
    <>
      <Muted>{ON}</Muted>
      <ObjectTag kind={r.local ? "local-server" : "server"} name={r.server} />
      {r.local && <Pill>{NOT_REGISTERED}</Pill>}
    </>
  );
  if (r.tools.length === 1) return <><ToolChip>{r.tools[0]}</ToolChip>{where}</>;
  return (
    <>
      <Pill icon={WrenchIcon}>{toolCount(r.tools.length)}</Pill>
      {where}
      <span className="h-0 basis-full" aria-hidden="true" />
      <FiveMore items={r.tools.map((t) => <ToolChip key={t}>{t}</ToolChip>)} />
    </>
  );
}

const TOOL_HEADS = [WIDENS_COLUMN.tool, WIDENS_COLUMN.for, WIDENS_COLUMN.now, "", WIDENS_COLUMN.after];
const RULE_HEADS = [WIDENS_COLUMN.rule, WIDENS_COLUMN.calls, WIDENS_COLUMN.now, "", WIDENS_COLUMN.after];
const COVER_HEADS = [WIDENS_COLUMN.objects, WIDENS_COLUMN.for, WIDENS_COLUMN.now, "", WIDENS_COLUMN.after];

function ToolRows({ rows, kindOf }: { rows: ToolRow[]; kindOf: (role: string) => Kind | null }) {
  return (
    <Table heads={TOOL_HEADS} kind="tools">
      {rows.map((r) => (
        <Row
          key={JSON.stringify([r.server, r.tools, r.now, r.after])}
          data-widens-row={r.server + "/" + r.tools.join(",")}
          heads={TOOL_HEADS}
          cells={[
            <ToolCell key="t" r={r} />,
            <React.Fragment key="f">{r.roles.map((role) => <ObjectTag key={role} kind={kindOf(role)} name={role} />)}{r.everyone && <Pill>{EVERYONE_PILL}</Pill>}</React.Fragment>,
            <SideCell key="n" side={r.now} />,
            <SideCell key="a" side={r.after} was={r.now} />,
          ]}
          note={r.warning ? <>{machineNote(r.server) + " "}<span className="text-muted-foreground">{r.warning.fix}</span></> : undefined}
        />
      ))}
    </Table>
  );
}

// RuleName is a loosened rule's name and the names it adds and drops.
function RuleName({ m }: { m: Marked }) {
  const moved = m.mark === "edited" && m.was && ruleAspects(m.was, m.rule).some((a) => a.aspect === "names") ? namesMove(m.was, m.rule, (r) => EVERY_WORD[r.lane]) : null;
  return (
    <>
      <span className="font-mono text-caption font-semibold break-all text-foreground">{m.rule.id}</span>
      {moved && <ChipDiff added={moved.join} removed={moved.leave} same={[]} />}
    </>
  );
}

function RuleAfter({ m }: { m: Marked }) {
  if (m.mark === "removed" || !m.was) return <ChangeChip change="removed" />;
  if (ruleAspects(m.was, m.rule).some((a) => a.aspect === "outcome")) return <RuleOutcome rule={m.rule} />;
  return <><OutcomeChip outcome={outcomeOf(m.rule)} /><Muted>{SAME_AS_NOW}</Muted></>;
}

function RuleRows({ marks }: { marks: Marked[] }) {
  return (
    <Table heads={RULE_HEADS} kind="rules">
      {marks.map((m) => {
        const was = m.was || m.rule;
        return (
          <Row key={m.rule.id} data-widens-rule={m.rule.id} heads={RULE_HEADS} cells={[<RuleName key="r" m={m} />, <Where key="w" rule={was} />, <RuleOutcome key="n" rule={was} />, <RuleAfter key="a" m={m} />]} />
        );
      })}
    </Table>
  );
}

// Covered is the objects a role covers on one side: every object, the
// names with what joins and leaves after publishing, or nothing.
function Covered({ c, after }: { c: DraftCoverage; after: boolean }) {
  const w = coverageNouns(c);
  const every = after ? c.every_after : c.every_before;
  if (every) return <Chip tone="accent">{everyType(w)}</Chip>;
  const names = (after ? c.after : c.before).map((o) => o.value);
  if (!names.length) return <Muted>{NOTHING}</Muted>;
  if (!after) return <ChipDiff added={[]} removed={[]} same={names} />;
  const m = objectsMove(c);
  const added = new Set(m.added.map((o) => o.value));
  return <ChipDiff added={[...added]} removed={m.dropped.map((o) => o.value)} same={names.filter((n) => !added.has(n))} />;
}

function CoverRows({ covers, kindOf }: { covers: DraftCoverage[]; kindOf: (role: string) => Kind | null }) {
  return (
    <Table heads={COVER_HEADS} kind="covers">
      {covers.map((c) => (
        <Row
          key={c.role + "/" + c.server}
          data-widens-covers={c.role}
          heads={COVER_HEADS}
          cells={[
            <React.Fragment key="o"><Pill>{objectsLabel(coverageNouns(c))}</Pill><Muted>{ON}</Muted><ObjectTag kind="server" name={c.server} /></React.Fragment>,
            <React.Fragment key="f"><ObjectTag kind={kindOf(c.role)} name={c.role} /><HoldersPill n={c.holders_count} /></React.Fragment>,
            <Covered key="n" c={c} after={false} />,
            <Covered key="a" c={c} after />,
          ]}
        />
      ))}
    </Table>
  );
}

// Causes names the draft's objects the lines name, under what the draft
// does to them, with the pill of a changed policy that applies to the
// proposer.
function Causes({ w, kindOf, proposer }: { w: Widens; kindOf: (role: string) => Kind | null; proposer: string | null }) {
  return (
    <div className={cn("flex flex-wrap items-center gap-x-2 gap-y-1.5 px-3.5 py-2.5 text-caption", LINE)} data-widens-cause>
      {[...new Set(w.causes.map((c) => c.what))].map((what) => (
        <span key={what} className="contents">
          <span className="text-muted-foreground">{SHORT_DOES[what][0]}</span>
          {w.causes.filter((c) => c.what === what).map((c) => (
            <span key={objectOf(c.item)} className="contents" data-cause={objectOf(c.item)}>
              <ObjectTag kind={c.item.kind === "Role" ? kindOf(c.item.name) : iconKind(c.item.kind)} name={c.item.name} />
              <ChangeChip change={c.what} />
              {c.applies && <Pill icon={UserRoundIcon} data-applies>{appliesTo(proposer)}</Pill>}
            </span>
          ))}
        </span>
      ))}
    </div>
  );
}

// Ack is the words of one confirm block, the text to type set as code.
export function Ack({ b }: { b: ConfirmBlock }) {
  const [pre, code, post] = ackWords(b);
  return <span data-ack-words>{pre}{code && <> <code className={CODE}>{code}</code> {post}</>}</span>;
}

// Confirm says once per block how the publish dialog asks to confirm it,
// the agent's lines left to their own line, then once whether publishing
// the old version undoes them where undo says the draft has one.
function Confirm({ blocks, undo }: { blocks: ConfirmBlock[]; undo: boolean }) {
  const said = new Set<string>();
  const shown = blocks.filter((b) => {
    if (b.lines.some(agentAccepts)) return false;
    const key = ackWords(b).join(" ");
    if (said.has(key)) return false;
    said.add(key);
    return true;
  });
  return (
    <div className="flex flex-col gap-1 bg-secondary/40 px-3.5 py-2.5 text-dense leading-relaxed text-text-2" data-confirm>
      {shown.map((b) => {
        const Icon = b.blocked ? null : b.kind === "tick" ? CheckIcon : PencilIcon;
        return (
          <p key={b.id} className="m-0 flex items-start gap-1.5">
            {Icon && <Icon className="mt-1 size-3.5 shrink-0 text-warn" aria-hidden="true" />}
            <Ack b={b} />
          </p>
        );
      })}
      {undo && <p className="m-0 text-muted-foreground" data-undo>{undoLine(blocks.some((b) => b.kind !== "tick"))}</p>}
    </div>
  );
}

// codesOf groups the section's lines by check code, each with the names of
// the objects its lines name, in the order the codes first come.
function codesOf(lines: DraftFinding[]): { code: string; objects: string[] }[] {
  const out: { code: string; objects: string[] }[] = [];
  for (const f of lines) {
    let c = out.find((x) => x.code === f.code);
    if (!c) {
      c = { code: f.code, objects: [] };
      out.push(c);
    }
    const name = (f.object || "").split("/").slice(1).join("/");
    if (name && !c.objects.includes(name)) c.objects.push(name);
  }
  return out;
}

function Details({ w }: { w: Widens }) {
  return (
    <details className="group border-t border-border" data-widens-details>
      <summary className="flex cursor-pointer list-none items-center gap-1 px-3.5 py-2 text-dense text-link [&::-webkit-details-marker]:hidden">
        <ChevronRightIcon className="size-4 transition-transform group-open:rotate-90" aria-hidden="true" />
        {DETAILS}
      </summary>
      <div className="flex flex-col gap-2 px-3.5 pb-3 text-caption text-text-2">
        {w.versions.length > 0 && (
          <table className="w-full table-fixed border-collapse" data-versions>
            <colgroup><col style={{ width: "44%" }} /><col /><col /></colgroup>
            <thead>
              <tr>{[VERSION_COLUMN.name, VERSION_COLUMN.now, VERSION_COLUMN.after].map((h) => <th key={h} className="py-1 pr-2 text-left font-semibold text-muted-foreground">{h}</th>)}</tr>
            </thead>
            <tbody>
              {w.versions.map((v) => (
                <tr key={v.name + v.now + v.after} data-version={v.name}>
                  {[v.name, v.now, v.after].map((x, i) => <td key={i} className="py-1 pr-2 align-top font-mono [overflow-wrap:anywhere]">{x}</td>)}
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="m-0 text-muted-foreground" data-check-lines>{checkLines(codesOf(w.said), [...new Set(w.folded.map((f) => f.code))])}</p>
      </div>
    </details>
  );
}

// WidensBody is the section under its heading. children are the lines no
// row says, drawn the way every other class draws its lines.
export function WidensBody({ detail, w, blocks, children }: { detail: DraftDetail; w: Widens; blocks: ConfirmBlock[]; children: React.ReactNode }) {
  const kindOf = roleKindOf(detail);
  const proposer = detail.draft.authors[0]?.username || "";
  const me = snapshot()?.user || "";
  return (
    <div className="@container" data-widens>
      {w.causes.length > 0 && <Causes w={w} kindOf={kindOf} proposer={proposer === me ? null : proposer} />}
      {w.tools.length > 0 && <ToolRows rows={w.tools} kindOf={kindOf} />}
      {w.covers.length > 0 && <CoverRows covers={w.covers} kindOf={kindOf} />}
      {w.rules.length > 0 && <RuleRows marks={w.rules} />}
      {children}
      {/* A draft that only adds objects has no old version to publish again. */}
      <Confirm blocks={blocks} undo={detail.draft.items.some((it) => it.existed)} />
      <Details w={w} />
    </div>
  );
}
