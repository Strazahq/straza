import * as React from "react";
import { CodeLines, Marked } from "@/components/code-lines";
import { HelpTip } from "@/components/help-tip";
import { type Part, type Step, heredocLang, pathsNamed, readCommand } from "@/lib/shell-read";
import { CONNECTOR, DOC_CUT_END, ENV_CHIP, PATHS_NAMED, PATHS_NAMED_HELP, docEnd, docWho, linesWord, redirWords, summaryLine, unreadable } from "@/lib/what-ran-words";
import { cn } from "@/lib/utils";

// The readable view of a shell command: its commands, numbered, with the
// words for how each leads to the next, every redirection said in words,
// each here-document as a code block, and the paths the text names. Every
// character of the command is on screen, only laid out. A command that
// does not read says why and shows itself as it ran.

const GLYPH: Record<string, string> = { "\n": "↵", "&$": "&" };

function partNode(p: Part, named: Set<string>, key: number) {
  if (p.kind === "word") {
    const cls = p.role === "program" ? "font-bold text-foreground"
      : p.role === "env" ? "text-teal"
      : p.role === "flag" ? "text-muted-foreground"
      : p.quoted ? "text-teal"
      : named.has(p.text) ? "underline decoration-muted-foreground decoration-dotted underline-offset-[3px]" : "";
    return <Marked key={key} text={p.text} className={cls || undefined} />;
  }
  if (p.kind === "redir") {
    const glued = p.op === ">&" || p.op === "<&";
    return (
      <React.Fragment key={key}>
        <span className="text-link">{p.fd + p.op}</span>{glued ? "" : " "}<Marked text={p.target} className={named.has(p.target) ? "underline decoration-muted-foreground decoration-dotted underline-offset-[3px]" : undefined} />
      </React.Fragment>
    );
  }
  if (p.kind === "heredoc") return <span key={key} className="text-link">{p.fd + p.op}<Marked text={p.delimText} /></span>;
  if (p.kind === "comment") return <Marked key={key} text={p.text} className="italic text-muted-foreground" />;
  return <span key={key} className="text-link">{p.text}</span>;
}

function StepBox({ s, n, named, cut }: { s: Step; n: number; named: Set<string>; cut: boolean }) {
  const chips = [
    ...s.redirs.map((r, k) => (
      <span key={"r" + k} className="inline-flex items-baseline gap-1.5 rounded-full border border-border bg-background px-2.5 py-px text-[12.5px] text-text-2" data-redir={r.fd + r.op}>
        <span className="font-mono text-link">{r.fd + r.op + (r.op === ">&" || r.op === "<&" ? "" : " ") + r.target}</span>{redirWords(r)}
      </span>
    )),
    ...(s.env.length ? [
      <span key="env" className="inline-flex items-baseline gap-1.5 rounded-full border border-border bg-background px-2.5 py-px text-[12.5px] text-text-2" data-env>
        <span className="font-mono text-teal">{s.env.map((w) => w.text.split("=")[0]).join(" ")}</span>{ENV_CHIP}
      </span>,
    ] : []),
  ];
  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-md border border-border bg-card px-3 py-2.5" data-step={n}>
      <div className="grid grid-cols-[22px_minmax(0,1fr)] items-baseline gap-2">
        <span className="text-right text-xs text-muted-foreground tabular-nums">{n}</span>
        <span className="min-w-0 font-mono text-[13px] leading-relaxed whitespace-pre-wrap [overflow-wrap:anywhere]" data-step-line>
          {s.parts.map((p, k) => <React.Fragment key={k}>{k > 0 && " "}{partNode(p, named, k)}</React.Fragment>)}
        </span>
      </div>
      {chips.length > 0 && <div className="flex flex-wrap gap-1.5 pl-[30px]">{chips}</div>}
      {s.heredocs.map((h, k) => {
        const lang = heredocLang(s);
        const cutHere = cut && !h.closed;
        return (
          <div key={k} className="ml-[30px] min-w-0 rounded-md border border-border bg-background" data-heredoc={lang}>
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 border-b border-border px-2.5 py-1.5 text-[13px] text-muted-foreground">
              <b className="font-semibold text-text-2">{docWho(s.program)}</b>
              <span>{lang}</span>
              <span>{linesWord(h.body ? h.body.split("\n").length : 0)}</span>
              <span>{docEnd(h, cutHere)}</span>
            </div>
            <CodeLines text={h.body} lang={lang} cutEnd={cutHere ? DOC_CUT_END : ""} />
          </div>
        );
      })}
    </div>
  );
}

// ShellSteps is the readable view of src, the command or the part of it
// the record kept. cut says the record cut it, so a here-document that
// does not close ends where the record does.
export function ShellSteps({ src, cut }: { src: string; cut: boolean }) {
  const reading = React.useMemo(() => readCommand(src), [src]);
  const named = React.useMemo(() => new Set(pathsNamed(src)), [src]);
  if (reading.error) {
    return (
      <div className="flex min-w-0 flex-col gap-2.5">
        <p className="rounded-md border border-border px-3 py-2 text-[13px] text-text-2" data-unreadable>{unreadable(reading.error)}</p>
        <div className="min-w-0 rounded-md border border-border bg-card" data-unreadable-text><CodeLines text={src} lang="text" /></div>
      </div>
    );
  }
  const scripts = reading.steps.reduce((n, s) => n + s.heredocs.length, 0);
  return (
    <div className="flex min-w-0 flex-col gap-2.5" data-shell-steps>
      <p className="text-[13px] text-muted-foreground" data-summary>{summaryLine(reading.steps.length, scripts)}</p>
      <ol className="flex min-w-0 flex-col">
        {reading.steps.map((s, k) => (
          <li key={k} className="flex min-w-0 flex-col">
            <StepBox s={s} n={k + 1} named={named} cut={cut} />
            {s.connector && CONNECTOR[s.connector] && (
              <div className="grid grid-cols-[22px_minmax(0,1fr)] items-center gap-2 px-3 py-1.5 text-[13px] text-muted-foreground" data-connector={s.connector}>
                <span className="text-right font-mono text-link" aria-hidden="true">{GLYPH[s.connector] ?? s.connector}</span>
                <span className={cn("flex items-center gap-2", "before:ml-1 before:h-[18px] before:w-px before:bg-border")}>{CONNECTOR[s.connector]}</span>
              </div>
            )}
          </li>
        ))}
      </ol>
      {named.size > 0 && (
        <div className="flex flex-col gap-1.5" data-paths-named>
          <div className="flex items-center gap-1.5">
            <h4 className="text-[13px] font-semibold uppercase tracking-[.06em] text-muted-foreground">{PATHS_NAMED}</h4>
            <HelpTip label={PATHS_NAMED} text={PATHS_NAMED_HELP} />
          </div>
          <div className="flex flex-wrap gap-1.5">
            {[...named].map((p) => <span key={p} className="rounded-sm border border-border bg-card px-1.5 py-px font-mono text-[12.5px] [overflow-wrap:anywhere]">{p}</span>)}
          </div>
        </div>
      )}
    </div>
  );
}
