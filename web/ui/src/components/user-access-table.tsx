import type * as React from "react";
import { OutcomeChip, Pill, ToolChip } from "@/components/chip";
import { FiveMore, ObjectTag } from "@/components/draft-parts";
import { KindTile } from "@/components/kind-icon";
import { MUTED, PageLink, RoleTag } from "@/components/user-parts";
import type { RoleRow } from "@/lib/api";
import { OUTCOME_WORD, type Outcome } from "@/lib/chip-words";
import type { AccessRow, MachineAccess, ServerAccess } from "@/lib/user-access";
import {
  ACCESS_COLUMN, BY_PATTERN_ONLY, EVERY_TOOL, MORE_BY_PATTERN, NOT_RUNNING, ON_MACHINE, THROUGH, ofTools, toolTitle, toolsNamed,
} from "@/lib/user-words";

// COLS is the three columns from the sm width up. Below it each server is a
// block and the header row is hidden, so nothing scrolls sideways.
const COLS = "sm:grid sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_minmax(0,1fr)] sm:gap-4";

const chips = (tools: string[], outcome: Exclude<Outcome, "none">) =>
  tools.map((t) => <ToolChip key={t} outcome={outcome} title={toolTitle(t, OUTCOME_WORD[outcome])}>{t}</ToolChip>);

// Line is one outcome of a server's tools: the outcome with its count, then
// its tools.
function Line({ outcome, count, children }: { outcome: Exclude<Outcome, "none">; count: number; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-1.5" data-line={outcome}>
      <OutcomeChip outcome={outcome} count={count} />
      {children}
    </div>
  );
}

// Lines are a server's tools by outcome. The lines that need approval and
// are denied come first and name every tool. The allowed line shows five
// and folds the rest.
function Lines({ row }: { row: AccessRow }) {
  return (
    <>
      {row.approval.length > 0 && <Line outcome="approval" count={row.approval.length}>{chips(row.approval, "approval")}</Line>}
      {row.denied.length > 0 && <Line outcome="denied" count={row.denied.length}>{chips(row.denied, "denied")}</Line>}
      {row.allowed.length > 0 && <Line outcome="allowed" count={row.allowed.length}><FiveMore items={chips(row.allowed, "allowed")} /></Line>}
    </>
  );
}

// Tools is a registered server's Tools cell: how many of its tools the
// user reaches, or every tool when the user reaches them all, then the
// lines by outcome. Only the count pill says every tool, and the lines
// under it name each tool.
function Tools({ row }: { row: ServerAccess }) {
  if (!row.running) return <div><Pill data-not-running>{NOT_RUNNING}</Pill></div>;
  const reached = row.allowed.length + row.approval.length;
  return (
    <div className="flex min-w-0 flex-col items-start gap-1.5">
      <Pill>{row.total > 0 && reached === row.total ? EVERY_TOOL : ofTools(reached, row.total)}</Pill>
      <Lines row={row} />
    </div>
  );
}

// MachineTools is the Tools cell of a server on the person's machine: how
// many tools the policy rules name, the lines by outcome, and what a rule
// reaches by a pattern. The whole of the server is not known, so no pill
// says every tool.
function MachineTools({ row }: { row: MachineAccess }) {
  const named = row.allowed.length + row.approval.length + row.denied.length;
  return (
    <div className="flex min-w-0 flex-col items-start gap-1.5">
      {named > 0 && <Pill data-named>{toolsNamed(named)}</Pill>}
      <Lines row={row} />
      {row.open && (named > 0 ? <Pill data-by-pattern>{MORE_BY_PATTERN}</Pill> : <p className={"m-0 " + MUTED} data-by-pattern>{BY_PATTERN_ONLY}</p>)}
    </div>
  );
}

// PolicyTag names a policy, as a link to its page for a reader with the
// policy area.
function PolicyTag({ name, link }: { name: string; link: boolean }) {
  const tag = <ObjectTag kind="policy-set" name={name} />;
  return link ? <PageLink to="policies" rest={[name]} className="inline-flex max-w-full rounded-md hover:opacity-80">{tag}</PageLink> : tag;
}

// AccessTable is the table of what a user can do: one row per server with
// its tools under their outcome and the roles it comes through, or, for a
// server on the person's machine, the policies that decide its tools.
// policy says the reader's role includes the policy area.
export function AccessTable({ rows, roles, policy }: { rows: AccessRow[]; roles: RoleRow[]; policy: boolean }) {
  const byName = new Map(roles.map((r) => [r.name, r]));
  return (
    <div className="rounded-md border border-border" data-access-table>
      <div className={"hidden border-b border-border px-3 py-2 " + COLS + " " + MUTED}>
        <span>{ACCESS_COLUMN.server}</span>
        <span>{ACCESS_COLUMN.tools}</span>
        <span>{ACCESS_COLUMN.through}</span>
      </div>
      {rows.map((r) => {
        const through = r.onMachine ? r.policies : r.through;
        return (
          <div key={(r.onMachine ? "machine/" : "") + r.server} className={"flex flex-col gap-2 border-b border-border px-3 py-3 last:border-b-0 " + COLS} data-server={r.server} data-on-machine={r.onMachine || undefined}>
            <div className="flex min-w-0 items-center gap-2">
              <KindTile kind={r.onMachine ? "local-server" : "server"} size="sm" />
              <span className="flex min-w-0 flex-col">
                <span className="min-w-0 truncate font-mono text-dense text-foreground" title={r.server}>{r.server}</span>
                {r.onMachine && <span className={MUTED}>{ON_MACHINE}</span>}
              </span>
            </div>
            {r.onMachine ? <MachineTools row={r} /> : <Tools row={r} />}
            <div className="flex min-w-0 flex-wrap content-start items-center gap-1.5" data-through>
              {through.length > 0 && <span className={MUTED + " sm:hidden"}>{THROUGH}</span>}
              {r.onMachine
                ? r.policies.map((name) => <PolicyTag key={name} name={name} link={policy} />)
                : r.through.map((name) => <RoleTag key={name} name={name} role={byName.get(name)} />)}
            </div>
          </div>
        );
      })}
    </div>
  );
}
