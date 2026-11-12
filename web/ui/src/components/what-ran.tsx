import * as React from "react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CodeLines, Marked } from "@/components/code-lines";
import { Section } from "@/components/sheet-parts";
import { ShellSteps } from "@/components/what-ran-shell";
import { COPIED, COPY_REFUSED } from "@/lib/audit-words";
import { notify } from "@/lib/notify";
import { cutOf, hiddenChars } from "@/lib/shell-read";
import { ARGS_NONE, CUT_TAIL, FILE_NOTE, HIDDEN_TAIL, INPUT_CUT_BODY, INPUT_CUT_HEAD, RAW, READABLE, WHAT_RAN, WRAP_LINES, copyLabel, cutBody, cutHead, fileVerb, hiddenHead, rawCount } from "@/lib/what-ran-words";

// The What ran section of the audit record sheet: the call a decision
// record holds, in a Readable view that lays it out and a Raw view that is
// the record's own text. A shell command reads as its commands, and a
// gateway call or a harness tool whose record holds its input as its
// arguments. A file tool whose record holds no input reads as its paths.
// The Raw view is the evidence; the Readable view only lays the same text
// out.

type View = "readable" | "raw";
const VIEW_KEY = "straza.audit.view";

// useView remembers the view this browser picked last. Storage that is
// blocked or empty reads as Readable.
function useView(): [View, (v: string) => void] {
  const [view, setView] = React.useState<View>(() => {
    try { return window.localStorage.getItem(VIEW_KEY) === "raw" ? "raw" : "readable"; } catch { return "readable"; }
  });
  const pick = (v: string) => {
    const next: View = v === "raw" ? "raw" : "readable";
    setView(next);
    try { window.localStorage.setItem(VIEW_KEY, next); } catch { /* the choice lasts for this sheet only */ }
  };
  return [view, pick];
}

function RawBox({ text }: { text: string }) {
  const [wrap, setWrap] = React.useState(true);
  const id = React.useId();
  return (
    <div className="min-w-0 rounded-md border border-border bg-card" data-raw>
      <div className="flex flex-wrap items-center gap-2.5 border-b border-border px-2.5 py-1.5 text-[13px] text-muted-foreground">
        <span>{rawCount(text.split("\n").length, text.length)}</span>
        <label htmlFor={id} className="ml-auto inline-flex items-center gap-1.5">
          <Switch id={id} checked={wrap} onCheckedChange={setWrap} />
          {WRAP_LINES}
        </label>
      </div>
      <CodeLines text={text} lang="text" wrap={wrap} />
    </div>
  );
}

type Cut = { kept: string; bytes: number; sha256: string };

// cutOfCommand reads both ways a record cuts a command (spec/events
// section 4): the client keeps 64 KiB and adds commandTruncated and
// commandHash, and strazad's record bound ends the text with the length
// and hash of the whole.
function cutOfCommand(data: Record<string, unknown>, command: string): Cut | null {
  const bound = cutOf(command);
  if (bound) return bound;
  if (data.commandTruncated === true && typeof data.commandHash === "string") return { kept: command, bytes: 0, sha256: data.commandHash.replace(/^sha256:/, "") };
  return null;
}

// Notices are the facts both views share: hidden characters in the text,
// and a text the record cut. input says the text is a tool's input and not
// a command.
function Notices({ text, cut, input }: { text: string; cut: Cut | null; input: boolean }) {
  const hidden = hiddenChars(text);
  if (!hidden.length && !cut) return null;
  const kinds = new Map<string, number[]>();
  for (const h of hidden) kinds.set(h.cp + " " + h.name, [...(kinds.get(h.cp + " " + h.name) || []), h.line]);
  return (
    <div className="flex flex-col gap-2">
      {hidden.length > 0 && (
        <div className="flex flex-col gap-1 rounded-md border border-warn/45 px-3 py-2 text-[13px] text-text-2" data-notice="hidden">
          <span className="font-semibold text-warn">{hiddenHead(hidden.length, input)}</span>
          <span>
            {[...kinds].map(([k, lines], n) => (
              <React.Fragment key={k}>{n > 0 && ", "}<span className="font-mono text-[12.5px]">{k}</span>{" on line " + [...new Set(lines)].join(", ")}</React.Fragment>
            ))}
            {". " + HIDDEN_TAIL}
          </span>
        </div>
      )}
      {cut && (
        <div className="flex flex-col gap-1 rounded-md border border-border px-3 py-2 text-[13px] text-text-2" data-notice="cut">
          <span className="font-semibold text-foreground">{input ? INPUT_CUT_HEAD : cutHead(cut.kept.length)}</span>
          <span>{input ? INPUT_CUT_BODY : cutBody(cut.bytes)}<span className="font-mono text-[12.5px] break-all">{cut.sha256}</span>{input ? "." : CUT_TAIL}</span>
        </div>
      )}
    </div>
  );
}

function ArgsTable({ args }: { args: Record<string, unknown> }) {
  return (
    <table className="w-full border-collapse text-[13.5px]" data-args>
      <tbody>
        {Object.entries(args).map(([k, v]) => (
          <tr key={k} className="border-b border-border last:border-b-0">
            <td className="w-px py-1.5 pr-3 align-top font-mono text-[13px] whitespace-nowrap text-muted-foreground">{k}</td>
            <td className="py-1.5 align-top">
              {typeof v === "string"
                ? <div className={v.includes("\n") || v.length > 80 ? "whitespace-pre-wrap text-text-2 [overflow-wrap:anywhere]" : "[overflow-wrap:anywhere]"}><Marked text={v} /></div>
                : <span className="font-mono text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere]">{JSON.stringify(v, null, 2)}</span>}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// jsonObject answers text parsed as a JSON object, or null.
function jsonObject(text: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(text);
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

// WhatRan renders the section for the record's data, or nothing for a
// record that holds no command, gateway call, input or paths.
export function WhatRan({ data }: { data: Record<string, unknown> }) {
  const [view, setView] = useView();
  const tool = String(data.tool || "");
  const command = typeof data.command === "string" ? data.command : "";
  const toolName = String(data.toolName || "");
  const call = toolName ? (data.app ? String(data.app) + "." : "") + toolName : "";
  const args = typeof data.arguments === "string" ? data.arguments : "";
  const paths = Array.isArray(data.paths) ? data.paths.map(String) : [];
  const workspace = String(data.workspace || "");
  const native = String(data.nativeTool || "");
  // A spawn's record carries its subagent type as the command, so only a
  // shell's command, or a command with no input beside it, reads as one.
  const kind = command && (tool === "shell.exec" || !args) ? "command" : call || args ? "arguments" : paths.length ? "paths" : null;
  if (!kind) return null;

  const parsed = kind === "arguments" ? jsonObject(args) : null;
  const raw = kind === "command" ? command : kind === "arguments" ? (parsed ? JSON.stringify(parsed, null, 2) : args) : paths.join("\n");
  const copyText = kind === "command" ? command : kind === "arguments" ? args : paths.join("\n");
  const rel = (p: string) => (workspace && p.startsWith(workspace + "/") ? p.slice(workspace.length + 1) : p);
  const clipboard = typeof navigator === "undefined" ? undefined : navigator.clipboard;
  const copy = () => {
    if (!clipboard) return;
    void clipboard.writeText(copyText).then(() => notify.ok(COPIED), () => notify.failed(COPY_REFUSED));
  };
  const argsCut = data.argumentsTruncated === true && typeof data.argumentsHash === "string" ? { kept: args, bytes: 0, sha256: data.argumentsHash.replace(/^sha256:/, "") } : null;
  const cut = kind === "command" ? cutOfCommand(data, command) : kind === "arguments" ? argsCut : null;

  let readable: React.ReactNode;
  if (kind === "command") readable = <ShellSteps src={cut ? cut.kept : command} cut={!!cut} />;
  else if (kind === "arguments") readable = !args ? <p className="text-[13px] text-muted-foreground">{ARGS_NONE}</p> : parsed && Object.keys(parsed).length ? <ArgsTable args={parsed} /> : <RawBox text={raw} />;
  else readable = (
    <div className="flex flex-col gap-1.5" data-paths>
      <div className="rounded-md border border-border bg-card px-3 py-2.5 font-mono text-[13px] [overflow-wrap:anywhere]">
        <span className="text-muted-foreground">{fileVerb(tool) + " "}</span>{paths.map(rel).join(", ")}
      </div>
      <p className="text-[13px] text-muted-foreground">{FILE_NOTE}</p>
    </div>
  );

  return (
    <Section
      title={WHAT_RAN}
      action={clipboard && copyText ? <Button variant="ghost" size="sm" onClick={copy}>{copyLabel(kind)}</Button> : undefined}
    >
      <Tabs value={view} onValueChange={setView} className="gap-2.5">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <TabsList aria-label={WHAT_RAN}>
            <TabsTrigger value="readable">{READABLE}</TabsTrigger>
            <TabsTrigger value="raw">{RAW}</TabsTrigger>
          </TabsList>
          <span className="text-[13px] text-muted-foreground" data-what-ran-meta>
            <span className="font-mono text-text-2">{kind === "arguments" && call ? call : native ? tool + " " + native : tool}</span>
            {workspace && <>{" in "}<span className="font-mono text-text-2 [overflow-wrap:anywhere]">{workspace}</span></>}
          </span>
        </div>
        <Notices text={kind === "arguments" ? args : command} cut={cut} input={kind === "arguments"} />
        <TabsContent value="readable" className="min-w-0">{readable}</TabsContent>
        <TabsContent value="raw" className="min-w-0">{kind === "arguments" && !args ? <p className="text-[13px] text-muted-foreground">{ARGS_NONE}</p> : <RawBox text={raw} />}</TabsContent>
      </Tabs>
    </Section>
  );
}
