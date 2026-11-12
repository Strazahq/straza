// The words of the audit sheet's What ran section: how one command leads
// to the next, what a redirection does, and the notices both views share.
import type { Heredoc, Redir } from "./shell-read";

export const WHAT_RAN = "What ran";
export const READABLE = "Readable";
export const RAW = "Raw";
export const WRAP_LINES = "Wrap lines";
export const PATHS_NAMED = "Paths named in the command";
export const PATHS_NAMED_HELP = "Read from the text of the command, so this is what it names, not proof of what it touched.";
export const RAW_NOTE = "exactly as recorded";
export const ARGS_NONE = "The record holds no arguments for this call.";
export const FILE_NOTE = "The record holds the path. It does not hold what changed in the file.";

// CONNECTOR says in words how a command leads to the next, by the operator
// between them. "&$" is a last command sent to the background.
export const CONNECTOR: Record<string, string> = {
  "\n": "then",
  ";": "then",
  "&&": "then, only if it succeeded",
  "||": "only if it failed",
  "|": "its output goes into",
  "|&": "its output and errors go into",
  "&": "runs in the background, and then",
  "&$": "runs in the background",
  ";;": "ends the case",
};

// redirWords says what one redirection does.
export function redirWords(r: Redir): string {
  const fd = r.fd || (r.op.startsWith("<") ? "0" : "1");
  const t = r.target;
  if (r.op === ">&" && fd === "2" && t === "1") return "errors into output";
  if (r.op === ">&" && fd === "1" && t === "2") return "output into errors";
  if (r.op === ">&" || r.op === "<&") return "stream " + fd + " into stream " + t;
  if (r.op === "&>" || r.op === "&>>") return t === "/dev/null" ? "output and errors dropped" : "output and errors " + (r.op === "&>>" ? "appended to " : "to ") + t;
  if (r.op === "<<<") return "input is the text " + t;
  if (r.op === "<") return "input from " + t;
  const what = fd === "2" ? "errors" : fd === "1" ? "output" : "stream " + fd;
  if (t === "/dev/null") return what + " dropped";
  return what + (r.op === ">>" ? " appended to " : " to ") + t;
}

export const ENV_CHIP = "set for this command only";

const plural = (n: number, one: string, many: string) => n + " " + (n === 1 ? one : many);
export const linesWord = (n: number) => plural(n, "line", "lines");
export const summaryLine = (commands: number, scripts: number) =>
  [plural(commands, "command", "commands"), scripts ? plural(scripts, "inline script", "inline scripts") : ""].filter(Boolean).join(" · ");

// docHead is the head of a here-document's block: who reads it, and how
// it ends.
export const docWho = (program: string) => (program ? "Given to " + program + " as its input" : "Input");
export const docEnd = (h: Heredoc, cut: boolean) =>
  h.closed ? "ends at " + h.delim : cut ? "cut here by the record bound" : "never closed, so it runs to the end of the command";
export const DOC_CUT_END = "… the record keeps no more of this command";

export const unreadable = (error: string) => "Straza cannot split this command into steps: " + error + ". It is shown as it ran.";

export const hiddenHead = (n: number, input = false) => (input ? "This call's input holds " : "This command holds ") + plural(n, "character", "characters") + " that do not show on screen.";
export const HIDDEN_TAIL = "They can make the text read differently from what ran. Both views mark them.";

export const cutHead = (kept: number) => "Straza kept the first " + kept.toLocaleString("en-US") + " characters of this command.";
// cutBody opens the sentence that gives the whole command's hash, with its
// length when the record says it.
export const cutBody = (bytes: number) => (bytes > 0 ? "The whole command was " + bytes.toLocaleString("en-US") + " bytes. Its SHA-256 is " : "The whole command's SHA-256 is ");
export const CUT_TAIL = ", so a copy the client kept can be checked against it.";
// INPUT_CUT_HEAD and INPUT_CUT_BODY are the same notice for a tool's input,
// which the record shortens by its longest values and not from its end.
export const INPUT_CUT_HEAD = "The record keeps a shortened copy of what this call was given.";
export const INPUT_CUT_BODY = "The SHA-256 of the whole text is ";

export const rawCount = (lines: number, chars: number) => linesWord(lines) + ", " + chars.toLocaleString("en-US") + " characters, " + RAW_NOTE;

export const fileVerb = (tool: string) => (tool === "file.read" ? "Reads" : tool === "file.write" ? "Writes" : tool === "file.edit" ? "Edits" : "Names");
export const copyLabel = (kind: "command" | "arguments" | "paths") => "Copy " + kind;
export const moreLines = (n: number) => "+" + linesWord(n);
export const CUT_CHIP = "cut";
