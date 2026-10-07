import * as React from "react";
import { CopyIcon, Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { HelpTip } from "@/components/help-tip";
import { type ApiError, type ValidateAnswer, validatePolicy } from "@/lib/api";
import { lineDiff } from "@/lib/line-diff";
import { notify } from "@/lib/notify";
import {
  COPIED, COPY, COPY_REFUSED, DIFF_ADDED, DIFF_REMOVED, EDITOR_LABEL, LINE_NUMBERS, PARSE_FAILED, SHOW_DIFF, TAB,
  VALID_NOW, VALIDATE, YAML_HELP, YAML_LINE, advisoryLine, validWords,
} from "@/lib/policy-words";
import { bare, checkFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// The YAML tab of a policy's page: the stored text
// with its comments, the server's own verdict on it, and a switch that
// shows what the page changed against the version that runs. The text and
// the cards on Rules are one document, so typing here moves the cards.

// PAUSE is how long the editor waits after the last keystroke before it
// hands the text to the page, which re-parses it for the other tabs.
const PAUSE = 300;

type Props = {
  // text is the working text of the page; stored is the text the server
  // holds, which the diff compares against.
  text: string;
  stored: string;
  onChange: (text: string) => void;
};

// Verdict is what the last Validate answered: the server's reading of a
// text that parses, or its sentence about one that does not, with the line
// the sentence names.
type Verdict = { ok: ValidateAnswer } | { bad: string; line: number | null } | null;

const GRID = "grid grid-cols-[44px_1fr] overflow-auto rounded-md border border-border bg-background font-mono text-[13px] leading-[1.5]";
// BOX is the height of the code box: the window less the page above it,
// never shorter than a screenful of text.
const BOX = "h-[calc(100vh-280px)] min-h-[320px]";
const GUTTER = "select-none whitespace-pre border-r border-border py-2.5 pr-2 text-right text-muted-foreground";

export function PolicyYaml({ text, stored, onChange }: Props) {
  const [draft, setDraft] = React.useState(text);
  const [diff, setDiff] = React.useState(false);
  const [verdict, setVerdict] = React.useState<Verdict>(null);
  const [busy, setBusy] = React.useState(false);
  const sent = React.useRef(text);
  const gutter = React.useRef<HTMLPreElement>(null);

  // A change from elsewhere, a card edit or Discard, replaces the draft;
  // the page's echo of what this editor just sent does not.
  React.useEffect(() => {
    if (text !== sent.current) {
      sent.current = text;
      setDraft(text);
    }
  }, [text]);

  const flush = React.useCallback((next: string) => {
    if (next === sent.current) return;
    sent.current = next;
    onChange(next);
  }, [onChange]);

  React.useEffect(() => {
    if (draft === sent.current) return;
    const t = setTimeout(() => flush(draft), PAUSE);
    return () => clearTimeout(t);
  }, [draft, flush]);

  const changed = draft !== stored;
  React.useEffect(() => { if (!changed) setDiff(false); }, [changed]);

  const validate = async () => {
    flush(draft);
    setBusy(true);
    try {
      setVerdict({ ok: await validatePolicy(draft) });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const named = /line (\d+)/.exec(err.message || "");
      setVerdict(err.unreachable ? { bad: checkFailed(err), line: null } : { bad: PARSE_FAILED + ": " + bare(err.message), line: named ? Number(named[1]) : null });
    } finally {
      setBusy(false);
    }
  };

  const copy = () => {
    const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
    if (!clipboard) { notify.failed(COPY_REFUSED); return; }
    void clipboard.writeText(draft).then(() => notify.ok(COPIED), () => notify.failed(COPY_REFUSED));
  };

  const lines = draft.split("\n");
  const badLine = verdict && "bad" in verdict ? verdict.line : null;

  return (
    <div className="flex flex-col gap-3" data-yaml-tab>
      <div className="flex flex-wrap items-center gap-2 text-sm text-text-2">
        <span>{YAML_LINE}</span>
        <HelpTip label={TAB.yaml} text={YAML_HELP} />
        <span className="ml-auto flex flex-wrap items-center gap-3">
          {changed && (
            <span className="flex items-center gap-2">
              <Switch id="policy-show-diff" checked={diff} onCheckedChange={setDiff} />
              <Label htmlFor="policy-show-diff" className="text-[13px] font-normal text-text-2">{SHOW_DIFF}</Label>
            </span>
          )}
          <Button variant="outline" size="sm" onClick={() => void validate()} disabled={busy}>
            {busy && <Loader2Icon className="animate-spin" />}
            {VALIDATE}
          </Button>
          <Button variant="outline" size="sm" onClick={copy}><CopyIcon /> {COPY}</Button>
        </span>
      </div>

      {diff ? (
        <div className={cn(GRID, BOX)} data-yaml-diff>
          <pre className={GUTTER} aria-hidden="true">{diffNumbers(stored, draft)}</pre>
          <div className="min-w-0 overflow-x-auto py-2.5">
            {lineDiff(stored, draft).map((l, i) => (
              <div
                key={i}
                title={l.kind === "add" ? DIFF_ADDED : l.kind === "remove" ? DIFF_REMOVED : undefined}
                data-diff={l.kind}
                className={cn("whitespace-pre px-3 text-foreground", l.kind === "add" && "bg-ok-bg", l.kind === "remove" && "bg-danger-bg line-through")}
              >
                {l.text || " "}
              </div>
            ))}
          </div>
        </div>
      ) : (
        <div className={cn(GRID, BOX)}>
          <pre ref={gutter} className={cn(GUTTER, "overflow-hidden")} aria-label={LINE_NUMBERS}>
            {lines.map((_, i) => (
              <span key={i} className={cn("block", badLine === i + 1 && "bg-danger-bg text-danger")}>{i + 1}</span>
            ))}
          </pre>
          <textarea
            aria-label={EDITOR_LABEL}
            spellCheck={false}
            value={draft}
            data-yaml-editor
            onChange={(e) => setDraft(e.target.value)}
            onBlur={() => flush(draft)}
            onScroll={(e) => { if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop; }}
            className="min-w-0 resize-none whitespace-pre bg-transparent px-3 py-2.5 font-mono text-[13px] leading-[1.5] text-foreground outline-none"
          />
        </div>
      )}

      {verdict && "ok" in verdict && (
        <div className="flex flex-col gap-1.5">
          <div role="status" className="rounded-md border border-ok/40 bg-ok-bg px-3 py-2 text-sm text-text-2" data-verdict="ok">
            {validWords(verdict.ok, VALID_NOW)}
          </div>
          {(verdict.ok.advisories || []).map((a, i) => (
            <div key={i} className="rounded-md border border-warn/40 bg-warn-bg px-3 py-2 text-[13px] leading-relaxed text-text-2" data-advisory={a.code}>
              {advisoryLine(a.rule, a.text)}
            </div>
          ))}
        </div>
      )}
      {verdict && "bad" in verdict && (
        <div role="alert" className="rounded-md border border-danger/40 bg-danger-bg px-3 py-2 text-sm leading-relaxed text-text-2" data-verdict="bad">
          {verdict.bad}
        </div>
      )}
    </div>
  );
}

// diffNumbers numbers the diff the way the editor numbers the text: a line
// the working text still holds carries its number, a removed line carries
// none.
function diffNumbers(stored: string, draft: string): string {
  let n = 0;
  return lineDiff(stored, draft).map((l) => (l.kind === "remove" ? "" : String(++n))).join("\n");
}
