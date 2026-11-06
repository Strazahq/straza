// The audit sheet's shell command reader. It splits a recorded command into
// words, operators, redirections, here-documents and comments, and groups
// them into the commands that run one after another. It is for display
// only: the policy engine matches the command as one string, and the record
// keeps that string as it ran. Every character of the command lands in a
// token, so the readable view can show all of it. A quote, $( or ${ that
// never closes is an error, and the sheet then shows the command as it ran.

export type Word = { kind: "word"; text: string; quoted: boolean; role?: "env" | "program" | "flag" | "arg" };
export type Redir = { kind: "redir"; fd: string; op: string; target: string };
export type Heredoc = { kind: "heredoc"; fd: string; op: string; delimText: string; delim: string; strip: boolean; body: string; closed: boolean };
export type Comment = { kind: "comment"; text: string };
export type Group = { kind: "group"; text: string };
type Op = { kind: "op"; text: string };
export type Part = Word | Redir | Heredoc | Comment | Group;

// Step is one command of the line: its parts in order, and the operator
// that leads to the next one ("" after the last, "&$" for a last command
// sent to the background).
export type Step = { parts: Part[]; words: Word[]; redirs: Redir[]; heredocs: Heredoc[]; env: Word[]; program: string; connector: string };
export type Reading = { steps: Step[]; error: string };

const OPS = ["&&", "||", "|&", ";;", "|", ";", "&"];
const REDIRS = ["<<<", "<<-", "&>>", "<<", ">>", ">&", "<&", ">|", "&>", ">", "<"];
const BREAK = " \t\n;&|<>()";

function lex(src: string): { items: (Part | Op)[]; error: string } {
  const items: (Part | Op)[] = [];
  const pending: Heredoc[] = [];
  const n = src.length;
  let i = 0;
  const readDq = (j: number): number => {
    while (j < n) {
      const c = src[j];
      if (c === "\\") { j += 2; continue; }
      if (c === '"') return j + 1;
      if (c === "$" && src[j + 1] === "(") { j = readSubst(j + 2); if (j < 0) return -1; continue; }
      if (c === "`") { const k = src.indexOf("`", j + 1); if (k < 0) return -1; j = k + 1; continue; }
      j++;
    }
    return -1;
  };
  const readSubst = (j: number): number => {
    let depth = 1;
    while (j < n && depth > 0) {
      const c = src[j];
      if (c === "\\") { j += 2; continue; }
      if (c === "'") { const k = src.indexOf("'", j + 1); if (k < 0) return -1; j = k + 1; continue; }
      if (c === '"') { j = readDq(j + 1); if (j < 0) return -1; continue; }
      if (c === "(") depth++;
      else if (c === ")") depth--;
      j++;
    }
    return depth === 0 ? j : -1;
  };
  // readWord answers the end of the word at j, the error of a quote that
  // never closes, and whether the word holds a quote.
  const readWord = (j: number): [number, string, boolean] => {
    let quoted = false;
    while (j < n && !BREAK.includes(src[j])) {
      const c = src[j];
      if (c === "\\") { j += 2; continue; }
      if (c === "'") { const k = src.indexOf("'", j + 1); if (k < 0) return [j, "a ' quote opens and never closes", quoted]; j = k + 1; quoted = true; continue; }
      if (c === '"') { const k = readDq(j + 1); if (k < 0) return [j, 'a " quote opens and never closes', quoted]; j = k; quoted = true; continue; }
      if (c === "$" && src[j + 1] === "'") {
        let k = j + 2;
        while (k < n && src[k] !== "'") k += src[k] === "\\" ? 2 : 1;
        if (k >= n) return [j, "a $' quote opens and never closes", quoted];
        j = k + 1; quoted = true; continue;
      }
      if (c === "$" && src[j + 1] === "(") { const k = readSubst(j + 2); if (k < 0) return [j, "a $( opens and never closes", quoted]; j = k; continue; }
      if (c === "$" && src[j + 1] === "{") { const k = src.indexOf("}", j + 2); if (k < 0) return [j, "a ${ opens and never closes", quoted]; j = k + 1; continue; }
      if (c === "`") { const k = src.indexOf("`", j + 1); if (k < 0) return [j, "a ` opens and never closes", quoted]; j = k + 1; continue; }
      j++;
    }
    return [Math.min(j, n), "", quoted];
  };
  const unquote = (w: string) => w.replace(/^'(.*)'$/s, "$1").replace(/^"(.*)"$/s, "$1").replace(/\\(.)/g, "$1");
  const atWordStart = () => i === 0 || " \t\n;&|()".includes(src[i - 1]);

  while (i < n) {
    const c = src[i];
    if (c === " " || c === "\t" || c === "\r") { i++; continue; }
    if (c === "\\" && src[i + 1] === "\n") { i += 2; continue; }
    if (c === "\n") {
      items.push({ kind: "op", text: "\n" });
      i++;
      // A here-document's body starts on the line after its operator and
      // runs to the line that is only its delimiter.
      for (const h of pending) {
        const lines: string[] = [];
        while (i < n) {
          let e = src.indexOf("\n", i);
          if (e < 0) e = n;
          const line = src.slice(i, e);
          i = e + 1;
          if ((h.strip ? line.replace(/^\t+/, "") : line) === h.delim) { h.closed = true; break; }
          lines.push(line);
        }
        i = Math.min(i, n);
        h.body = lines.join("\n");
      }
      pending.length = 0;
      continue;
    }
    if (c === "#" && atWordStart()) {
      let e = src.indexOf("\n", i);
      if (e < 0) e = n;
      items.push({ kind: "comment", text: src.slice(i, e) });
      i = e;
      continue;
    }
    let j = i;
    if (atWordStart()) while (j < n && src[j] >= "0" && src[j] <= "9") j++;
    const op = REDIRS.find((r) => src.startsWith(r, j));
    if (op) {
      const fd = src.slice(i, j);
      let k = j + op.length;
      while (k < n && (src[k] === " " || src[k] === "\t")) k++;
      const [e, err] = readWord(k);
      if (err) return { items, error: err };
      const target = src.slice(k, e);
      if (op === "<<" || op === "<<-") {
        const h: Heredoc = { kind: "heredoc", fd, op, delimText: target, delim: unquote(target), strip: op === "<<-", body: "", closed: false };
        items.push(h);
        pending.push(h);
      } else items.push({ kind: "redir", fd, op, target });
      i = e;
      continue;
    }
    const o = OPS.find((x) => src.startsWith(x, i));
    if (o) { items.push({ kind: "op", text: o }); i += o.length; continue; }
    if (c === "(" || c === ")") { items.push({ kind: "group", text: c }); i++; continue; }
    const [e, err, quoted] = readWord(i);
    if (err) return { items, error: err };
    const end = e === i ? i + 1 : e;
    const text = src.slice(i, end);
    items.push(text === "{" || text === "}" ? { kind: "group", text } : { kind: "word", text, quoted });
    i = end;
  }
  return { items, error: "" };
}

const ASSIGN = /^[A-Za-z_][A-Za-z0-9_]*=/;

// readCommand splits src into its steps. A newline right after && || | or
// before any command is layout, not a step boundary.
export function readCommand(src: string): Reading {
  const { items, error } = lex(src);
  if (error) return { steps: [], error };
  const steps: Step[] = [];
  let cur: Step | null = null;
  for (const it of items) {
    if (it.kind === "op") {
      if (cur && cur.parts.length) { cur.connector = it.text; steps.push(cur); cur = null; }
      continue;
    }
    cur = cur || { parts: [], words: [], redirs: [], heredocs: [], env: [], program: "", connector: "" };
    cur.parts.push(it);
    if (it.kind === "word") cur.words.push(it);
    else if (it.kind === "redir") cur.redirs.push(it);
    else if (it.kind === "heredoc") cur.heredocs.push(it);
  }
  if (cur) steps.push(cur);
  for (const s of steps) {
    for (const w of s.words) {
      if (!s.program && ASSIGN.test(w.text)) { w.role = "env"; s.env.push(w); continue; }
      if (!s.program) { w.role = "program"; s.program = w.text; continue; }
      w.role = w.text.startsWith("-") ? "flag" : "arg";
    }
  }
  const last = steps[steps.length - 1];
  if (last && (last.connector === "\n" || last.connector === ";")) last.connector = "";
  if (last && last.connector === "&") last.connector = "&$";
  return { steps, error: "" };
}

const LANG_OF_PROGRAM: Record<string, string> = { python: "python", python3: "python", node: "javascript", deno: "javascript", bun: "javascript", bash: "shell", sh: "shell", zsh: "shell", psql: "sql", sqlite3: "sql", mysql: "sql", ruby: "ruby", perl: "perl", php: "php" };
const LANG_OF_EXT: Record<string, string> = { py: "python", js: "javascript", mjs: "javascript", ts: "typescript", tsx: "typescript", json: "json", yaml: "yaml", yml: "yaml", sh: "shell", sql: "sql", go: "go", md: "markdown", toml: "toml" };

// heredocLang is the language of a here-document's body: the program it is
// given to, else the extension of the file the step writes, else text.
export function heredocLang(s: Step): string {
  const prog = s.program.split("/").pop() || "";
  if (LANG_OF_PROGRAM[prog]) return LANG_OF_PROGRAM[prog];
  for (const r of s.redirs) {
    const m = /\.([A-Za-z0-9]+)$/.exec(r.target);
    if (m && LANG_OF_EXT[m[1].toLowerCase()]) return LANG_OF_EXT[m[1].toLowerCase()];
  }
  return "text";
}

// partText is one part as the command spells it.
export function partText(p: Part): string {
  if (p.kind === "redir") return p.fd + p.op + (p.op === ">&" || p.op === "<&" ? "" : " ") + p.target;
  if (p.kind === "heredoc") return p.fd + p.op + p.delimText;
  return p.text;
}

// opText is the operator after a step as the list row writes it.
export const opText = (c: string) => (c === "\n" ? ";" : c === "&$" ? "&" : c);

// oneLine is the list row's form of a command: each step's parts joined,
// the steps joined by their operators, a here-document as its operator and
// delimiter only. lines is the command's own line count. A command that
// does not read keeps its first line.
export function oneLine(src: string): { text: string; lines: number } {
  const lines = src.split("\n").length;
  const r = readCommand(src);
  if (r.error) return { text: src.split("\n")[0], lines };
  return { text: r.steps.map((s) => s.parts.map(partText).join(" ") + (s.connector ? " " + opText(s.connector) : "")).join(" "), lines };
}

// HIDDEN_RE matches a character that shows as nothing or as a plain space:
// zero-width and direction controls, the no-break space, the soft hyphen
// and the C0 controls other than tab, newline and carriage return.
export const HIDDEN_RE = /[ ­​-‏‪-‮⁠⁦-⁩﻿\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g;
const HIDDEN_NAME: Record<string, string> = {
  " ": "no-break space", "­": "soft hyphen", "​": "zero-width space", "‌": "zero-width non-joiner",
  "‍": "zero-width joiner", "‎": "left-to-right mark", "‏": "right-to-left mark", "‪": "left-to-right embedding",
  "‫": "right-to-left embedding", "‬": "pop directional formatting", "‭": "left-to-right override",
  "‮": "right-to-left override", "⁠": "word joiner", "⁦": "left-to-right isolate", "⁧": "right-to-left isolate",
  "⁨": "first strong isolate", "⁩": "pop directional isolate", "﻿": "zero-width no-break space",
};
export const hiddenName = (ch: string) => HIDDEN_NAME[ch] || "control character";
export const codepoint = (ch: string) => "U+" + (ch.codePointAt(0) || 0).toString(16).toUpperCase().padStart(4, "0");

// hiddenChars lists each hidden character of src with its line.
export function hiddenChars(src: string): { cp: string; name: string; line: number }[] {
  const out: { cp: string; name: string; line: number }[] = [];
  src.split("\n").forEach((l, k) => {
    for (const m of l.matchAll(HIDDEN_RE)) out.push({ cp: codepoint(m[0]), name: hiddenName(m[0]), line: k + 1 });
  });
  return out;
}

// cutOf reads the bound a producer puts on a long string (spec/events
// section 4): a prefix, then "... (<n> bytes, sha256 <hex>)".
export function cutOf(src: string): { kept: string; bytes: number; sha256: string } | null {
  const m = /\.\.\. \((\d+) bytes, sha256 ([0-9a-f]{64})\)$/.exec(src);
  return m ? { kept: src.slice(0, m.index), bytes: Number(m[1]), sha256: m[2] } : null;
}

const PATH_RE = /(?:^|[\s'"=(,:<>])((?:~|\.{1,2})?\/?(?:[\w@.+-]+\/)+[\w@.+-]+\/?|[\w@+-][\w@.+-]*\.(?:ts|tsx|js|mjs|cjs|jsx|go|py|rs|java|kt|rb|php|c|h|cpp|cs|json|ya?ml|toml|md|sh|ps1|css|scss|html|sql|txt|lock|env|ini|cfg|conf|xml|csv|log|mod|sum))(?=$|[\s'"),;:|&<>])/g;

// pathsNamed lists the file and folder names in the text of src, in order,
// leaving out addresses and /dev/null. It reads the text only, so it says
// what the command names, not what it touched.
export function pathsNamed(src: string): string[] {
  const seen = new Set<string>();
  for (const m of src.matchAll(PATH_RE)) {
    const p = m[1];
    const at = m.index || 0;
    if (/^[\d.]+$/.test(p) || p === "/dev/null" || p.length < 4) continue;
    if (/[a-z][a-z0-9+.-]*:\/\/\S*$/i.test(src.slice(Math.max(0, at - 200), at + 1))) continue;
    seen.add(p);
  }
  return [...seen];
}
