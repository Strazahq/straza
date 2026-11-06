import * as React from "react";
import { codeTokens } from "@/lib/code-tokens";
import { HIDDEN_RE, codepoint, hiddenName } from "@/lib/shell-read";
import { cn } from "@/lib/utils";

// Marked renders text with every hidden character, such as a zero-width
// space or a right-to-left override, as a visible code mark in the
// advisory hue, so the text on screen is the text that ran.
export function Marked({ text, className }: { text: string; className?: string }) {
  const parts: React.ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(HIDDEN_RE)) {
    const at = m.index ?? 0;
    if (at > last) parts.push(text.slice(last, at));
    const cp = codepoint(m[0]);
    parts.push(
      <span key={at} data-hidden-char={cp} title={cp + " " + hiddenName(m[0])} className="mx-px inline-block rounded-[3px] border border-warn px-[3px] align-[1px] font-mono text-[10.5px] leading-tight text-warn">
        {cp}
      </span>,
    );
    last = at + m[0].length;
  }
  if (last < text.length) parts.push(text.slice(last));
  return className ? <span className={className}>{parts}</span> : <>{parts}</>;
}

const TONE: Record<string, string> = { kw: "text-link", str: "text-teal", com: "italic text-muted-foreground", num: "text-text-2" };

// CodeLines lays text out one row per line with its number, so a wrapped
// line keeps its number and a selection copies no numbers. lang picks the
// coloring; "text" colors nothing. cutEnd is a closing line for a body
// the record cut.
export function CodeLines({ text, lang, wrap = true, cutEnd }: { text: string; lang: string; wrap?: boolean; cutEnd?: string }) {
  const lines = React.useMemo(() => {
    const out: React.ReactNode[][] = [[]];
    let key = 0;
    for (const tk of codeTokens(text, lang)) {
      tk.s.split("\n").forEach((piece, k) => {
        if (k > 0) out.push([]);
        if (piece) out[out.length - 1].push(<Marked key={key++} text={piece} className={TONE[tk.t]} />);
      });
    }
    return out;
  }, [text, lang]);
  const row = "grid grid-cols-[3.2em_minmax(0,1fr)]";
  const body = cn("pr-2.5", wrap ? "whitespace-pre-wrap [overflow-wrap:anywhere]" : "whitespace-pre");
  return (
    <div className="max-h-[460px] overflow-auto py-1.5 font-mono text-[12.5px] leading-[1.55]" data-code={lang} data-wrap={wrap ? "on" : "off"}>
      {lines.map((parts, k) => (
        <div key={k} className={row} data-line={k + 1}>
          <span className="pr-3 text-right text-muted-foreground tabular-nums select-none" aria-hidden="true">{k + 1}</span>
          <span className={body}>{parts.length ? parts : " "}</span>
        </div>
      ))}
      {cutEnd && (
        <div className={row}>
          <span aria-hidden="true" />
          <span className="italic text-warn" data-cut-end>{cutEnd}</span>
        </div>
      )}
    </div>
  );
}
