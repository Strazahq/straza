import * as React from "react";
import { XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HEAD } from "@/components/data-table";
import { CAPS, HINT } from "@/components/wizard/parts";
import type { DeniedTool } from "@/lib/api";
import {
  ADD_BY_NAME, HARNESS_COLUMN, HARNESS_NAME_HINT, MCP_NAME_HINT, NEW_ROLE_HINT, NEW_ROLE_NAME, NEW_ROLE_TAKEN, NO_PATTERN, NO_PREFIX, NO_TOOL_SEEN, NOTHING_SEEN,
  TODAY, TODAY_DENIED, harnessWords,
} from "@/lib/harness-words";
import { slug } from "@/lib/policy-model";
import { ADD_TOOL, NAME_BAD, TOOL_COLUMN, removePick } from "@/lib/policy-words";

// The parts of the New policy wizard that write a rule for a tool on a
// person's own machine: the tool picker of an MCP server Straza has not
// registered or of the harness itself, and the name of a role the policy
// makes.

type ToolsProps = {
  // lane says which names the picker takes: the tools of an MCP server,
  // which may be patterns, or harness tools, which are whole names.
  lane: "mcp" | "harness";
  // denied are the tools people asked for and no rule covers.
  denied: DeniedTool[];
  picked: string[];
  // whole says the rule covers every tool, so nothing is picked by name.
  whole?: boolean;
  onPick: (names: string[]) => void;
};

// MachineTools picks tools Straza cannot list: the ones the denied calls
// named, ticked, and any other by its name. It refuses the two names the
// server would refuse, so the reason is read before the review.
export function MachineTools({ lane, denied, picked, whole, onPick }: ToolsProps) {
  const [text, setText] = React.useState("");
  const [problem, setProblem] = React.useState("");
  const seen = denied.map((t) => t.name);
  const typed = picked.filter((n) => !seen.includes(n));
  const toggle = (name: string) => onPick(picked.includes(name) ? picked.filter((x) => x !== name) : [...picked, name]);
  const add = () => {
    const value = text.trim();
    if (!value) return;
    if (lane === "harness" && (/[*?]/.test(value) || value.startsWith("re:"))) { setProblem(NO_PATTERN); return; }
    if (lane === "mcp" && value.includes("__")) { setProblem(NO_PREFIX); return; }
    setProblem("");
    if (!picked.includes(value)) onPick([...picked, value]);
    setText("");
  };
  const cols = lane === "harness" ? 4 : 3;
  return (
    <div className="flex max-w-[760px] flex-col gap-2" data-machine-tools={lane}>
      <div className="overflow-x-auto rounded-md border border-border bg-card">
        <Table className="table-fixed">
          <colgroup>
            <col style={{ width: "44px" }} /><col style={{ width: "220px" }} />{lane === "harness" && <col style={{ width: "160px" }} />}<col />
          </colgroup>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="h-10 px-2" />
              <TableHead className="h-10"><span className={HEAD}>{TOOL_COLUMN.tool}</span></TableHead>
              {lane === "harness" && <TableHead className="h-10"><span className={HEAD}>{HARNESS_COLUMN}</span></TableHead>}
              <TableHead className="h-10"><span className={HEAD}>{TODAY}</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {denied.length === 0 && (
              <TableRow><TableCell colSpan={cols} className="whitespace-normal text-sm text-muted-foreground">{lane === "harness" ? NO_TOOL_SEEN : NOTHING_SEEN}</TableCell></TableRow>
            )}
            {denied.map((t) => (
              <TableRow key={t.name} data-tool={t.name} className="text-sm">
                <TableCell className="px-2">
                  <input type="checkbox" aria-label={t.name} checked={whole || picked.includes(t.name)} disabled={whole} onChange={() => toggle(t.name)} className="size-4 accent-primary" />
                </TableCell>
                <TableCell className="truncate font-mono text-[13px] text-foreground">{t.name}</TableCell>
                {lane === "harness" && <TableCell className="truncate text-[13px] text-text-2">{harnessWords(t.harnesses)}</TableCell>}
                <TableCell className="whitespace-normal text-[13px] text-muted-foreground" data-today={t.name}>{TODAY_DENIED(t.count)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      {!whole && (
        <>
          {typed.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5" data-typed-tools>
              {typed.map((n) => (
                <span key={n} className="inline-flex items-center gap-1 rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px]">
                  {n}
                  <button type="button" aria-label={removePick(n)} className="text-muted-foreground hover:text-foreground" onClick={() => onPick(picked.filter((x) => x !== n))}>
                    <XIcon className="size-3" aria-hidden="true" />
                  </button>
                </span>
              ))}
            </div>
          )}
          <div className="flex items-center gap-2">
            <Input
              aria-label={ADD_BY_NAME}
              value={text}
              spellCheck={false}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); add(); } }}
              className="max-w-xs font-mono"
            />
            <Button variant="outline" size="sm" onClick={add}>{ADD_TOOL}</Button>
          </div>
          <span className={HINT}>{lane === "harness" ? HARNESS_NAME_HINT : MCP_NAME_HINT}</span>
          {problem && <p className="text-[13px] text-danger" data-name-problem>{problem}</p>}
        </>
      )}
    </div>
  );
}

// newRoleProblem says why the wizard cannot make a role under name: it is
// outside the server's grammar, or a role carries it already.
export const newRoleProblem = (name: string, taken: string[]): "" | "bad" | "taken" => (!name || slug(name) !== name ? "bad" : taken.includes(name) ? "taken" : "");

// NewRoleName is the name field of the role a policy makes.
export function NewRoleName({ name, taken, onName }: { name: string; taken: string[]; onName: (name: string) => void }) {
  const problem = name ? newRoleProblem(name, taken) : "";
  return (
    <div className="flex max-w-[560px] flex-col gap-1" data-new-role>
      <label htmlFor="new-role-name" className={CAPS}>{NEW_ROLE_NAME}</label>
      <Input id="new-role-name" value={name} spellCheck={false} onChange={(e) => onName(e.target.value)} className="font-mono" />
      {problem && <span className="text-[13px] text-danger" data-role-problem={problem}>{problem === "taken" ? NEW_ROLE_TAKEN : NAME_BAD}</span>}
      <span className={HINT}>{NEW_ROLE_HINT}</span>
    </div>
  );
}
