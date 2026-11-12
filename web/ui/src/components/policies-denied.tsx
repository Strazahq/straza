import * as React from "react";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { HEAD } from "@/components/data-table";
import { FetchError } from "@/components/error-state";
import { type PolicyDoor, openNewPolicy } from "@/components/policies-by-role";
import { type ApiError, type DefaultDenials, type DeniedTool, listDefaultDenials } from "@/lib/api";
import { mayWritePolicy } from "@/lib/access-read";
import { put } from "@/lib/handoff";
import {
  DENIED_COLUMN, DENIED_CUT, DENIED_FOOT, DENIED_LEDE, DENIED_NONE, DENIED_SERVERS, DENIED_SERVERS_LINE, DENIED_TOOLS, DENIED_TOOLS_LINE, NAMELESS_HELP, NAMELESS_TOOL,
  OPEN_RECORD, SUBJECT_DENIED, WRITE_RULE, harnessWords, openRecord, writeRuleFor,
} from "@/lib/harness-words";
import { navigate } from "@/lib/router";
import { readFailed } from "@/lib/say";
import { absTime } from "@/lib/words";

// The Denied by default view of Policies: the tools people's agents asked
// for in the last day that no rule covers, read from the decision records.
// A row opens the New policy wizard with Which calls answered, and names
// the newest record, which Audit shows to a seat that may read it.

const WIDTHS = ["34%", "18%", "10%", "16%", "22%"];
const CHIP = "inline-flex items-center rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] whitespace-nowrap";

// openInAudit hands Audit the record's sequence number and goes there.
function openInAudit(seq: number) {
  put("audit", { seq });
  navigate("audit");
}

type RowProps = { name: React.ReactNode; label: string; runs: string[]; count: number; lastSeq: number; lastTime: string; door: PolicyDoor | null };

function DeniedRow({ name, label, runs, count, lastSeq, lastTime, door }: RowProps) {
  return (
    <TableRow className="text-sm" data-denied={label}>
      <TableCell className="align-top whitespace-normal">{name}</TableCell>
      <TableCell className="align-top text-text-2">{harnessWords(runs)}</TableCell>
      <TableCell className="text-right align-top font-mono tabular-nums">{count}</TableCell>
      <TableCell className="align-top text-text-2">{absTime(lastTime)}</TableCell>
      <TableCell className="align-top">
        <span className="flex flex-wrap items-center justify-end gap-2">
          <Button variant="link" size="sm" className="h-auto p-0 text-link" aria-label={openRecord(lastSeq)} onClick={() => openInAudit(lastSeq)}>{OPEN_RECORD}</Button>
          {door && mayWritePolicy() && <Button variant="outline" size="sm" aria-label={writeRuleFor(label)} onClick={() => openNewPolicy(door)}>{WRITE_RULE}</Button>}
        </span>
      </TableCell>
    </TableRow>
  );
}

function Group({ title, line }: { title: string; line: string }) {
  return (
    <TableRow className="bg-muted/40 hover:bg-muted/40">
      <TableCell colSpan={5} className="whitespace-normal">
        <span className="font-semibold text-foreground">{title}</span> <span className="text-[13px] text-muted-foreground">{line}</span>
      </TableCell>
    </TableRow>
  );
}

export function PoliciesDenied({ needle }: { needle: string }) {
  const [denials, setDenials] = React.useState<DefaultDenials | null>(null);
  const [problem, setProblem] = React.useState<string | null>(null);

  React.useEffect(() => {
    let alive = true;
    listDefaultDenials().then(
      (d) => { if (alive) setDenials(d); },
      (e) => {
        const err = e as ApiError;
        if (alive && err.status !== 401) setProblem(readFailed(SUBJECT_DENIED, err));
      },
    );
    return () => { alive = false; };
  }, []);

  const q = needle.trim().toLowerCase();
  const hit = (name: string, tools: DeniedTool[]) => !q || name.toLowerCase().includes(q) || tools.some((t) => t.name.toLowerCase().includes(q));
  const servers = (denials ? denials.servers : []).filter((s) => hit(s.server, s.tools));
  const tools = (denials ? denials.tools : []).filter((t) => hit(t.name, []));

  return (
    <div className="flex flex-col gap-2" data-denied-view>
      <p className="max-w-[90ch] text-sm text-text-2">{DENIED_LEDE}</p>
      {problem && <FetchError subject={SUBJECT_DENIED} detail={problem} />}
      {denials && denials.truncated && <p className="text-[13px] text-warn" data-denied-cut>{DENIED_CUT}</p>}
      {denials && servers.length + tools.length === 0 && (
        <p className="rounded-md border border-border bg-card px-4 py-3 text-sm text-muted-foreground" data-denied-none>{DENIED_NONE}</p>
      )}
      {servers.length + tools.length > 0 && (
        <div className="overflow-x-auto rounded-md border border-border bg-card">
          <Table className="table-fixed">
            <colgroup>{WIDTHS.map((w, i) => <col key={i} style={{ width: w }} />)}</colgroup>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="h-10"><span className={HEAD}>{DENIED_COLUMN.asked}</span></TableHead>
                <TableHead className="h-10"><span className={HEAD}>{DENIED_COLUMN.runs}</span></TableHead>
                <TableHead className="h-10 text-right"><span className={HEAD}>{DENIED_COLUMN.count}</span></TableHead>
                <TableHead className="h-10"><span className={HEAD}>{DENIED_COLUMN.last}</span></TableHead>
                <TableHead className="h-10 text-right"><span className={HEAD}>{DENIED_COLUMN.record}</span></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {servers.length > 0 && <Group title={DENIED_SERVERS} line={DENIED_SERVERS_LINE} />}
              {servers.map((s) => (
                <DeniedRow
                  key={"server:" + s.server}
                  label={s.server}
                  name={(
                    <span className="flex flex-col gap-1">
                      <span className="font-mono font-semibold text-foreground">{s.server}</span>
                      <span className="flex flex-wrap gap-1">{s.tools.map((t) => <span key={t.name} className={CHIP}>{t.name}</span>)}</span>
                    </span>
                  )}
                  runs={s.harnesses}
                  count={s.count}
                  lastSeq={s.lastSeq}
                  lastTime={s.lastTime}
                  door={{ lane: "mcp", app: s.server, tools: s.tools.map((t) => t.name) }}
                />
              ))}
              {tools.length > 0 && <Group title={DENIED_TOOLS} line={DENIED_TOOLS_LINE} />}
              {tools.map((t) => (
                <DeniedRow
                  key={"tool:" + t.name}
                  label={t.name || NAMELESS_TOOL}
                  name={t.name
                    ? <span className="font-mono font-semibold text-foreground">{t.name}</span>
                    : <span className="flex flex-col gap-1"><span className="text-foreground">{NAMELESS_TOOL}</span><span className="text-[13px] text-muted-foreground">{NAMELESS_HELP}</span></span>}
                  runs={t.harnesses}
                  count={t.count}
                  lastSeq={t.lastSeq}
                  lastTime={t.lastTime}
                  door={t.name ? { lane: "harness", tools: [t.name] } : null}
                />
              ))}
            </TableBody>
          </Table>
        </div>
      )}
      <p className="max-w-[100ch] text-[13px] text-muted-foreground">{DENIED_FOOT}</p>
    </div>
  );
}
