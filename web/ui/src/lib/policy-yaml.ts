// The parser and emitter for the PolicySet YAML subset the console edits
// (spec/policyset v1beta1). The app ships no YAML library, so this is a
// purpose-built
// round-tripper: the server's validate stays the authority on validity, and
// this module only turns text into values and values back into text.
// Subset: block mappings and sequences with consistent space indentation
// (tabs refused; map items keep follow-on keys two columns past the dash),
// nested flow lists and maps, quoted and plain scalars, numbers, booleans,
// null and ~, comments, one leading document marker. No block scalars, so
// a multi-line string round-trips as a double-quoted scalar; no anchors,
// aliases, tags, quoted keys or multi-document streams. The emitter is
// canonical: it drops comments, ordering quirks and empty collections.
// A parse error carries the 1-based line.

export type Yaml = null | boolean | number | string | Yaml[] | { [key: string]: Yaml };
export type YamlMap = { [key: string]: Yaml };

export class YamlError extends Error {
  line: number;
  constructor(message: string, line: number) {
    super(message + (line ? " (line " + line + ")" : ""));
    this.line = line || 0;
  }
}

type Line = { n: number; indent: number; content: string };
type State = { lines: Line[]; pos: number };
type Flow = { s: string; i: number; line: number };

// stripComment removes a trailing comment, honoring quotes: a hash inside
// quotes is content, and a hash opens a comment only at the start or after
// whitespace.
function stripComment(s: string): string {
  let inS = false;
  let inD = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inD) {
      if (c === "\\") i++;
      else if (c === '"') inD = false;
    } else if (inS) {
      if (c === "'") { if (s[i + 1] === "'") i++; else inS = false; }
    } else if (c === '"') inD = true;
    else if (c === "'") inS = true;
    else if (c === "#" && (i === 0 || s[i - 1] === " " || s[i - 1] === "\t")) {
      return s.slice(0, i);
    }
  }
  return s;
}

const KEY_RE = /^([A-Za-z0-9_.-]+):(?:[ \t]+(.*))?$/;

function toLines(text: string): Line[] {
  const out: Line[] = [];
  const raw = String(text).split(/\r?\n/);
  for (let i = 0; i < raw.length; i++) {
    const n = i + 1;
    if (/^\t/.test(raw[i])) throw new YamlError("tabs are not allowed in indentation", n);
    const noComment = stripComment(raw[i]);
    const content = noComment.trim();
    if (content === "") continue;
    if (content === "---" || content === "...") {
      if (out.length > 0 && content === "---") throw new YamlError("multiple YAML documents are not supported here", n);
      continue;
    }
    out.push({ n, indent: noComment.length - noComment.trimStart().length, content });
  }
  return out;
}

// parsePolicy parses one document of the subset above into plain values.
export function parsePolicy(text: string): Yaml {
  const lines = toLines(text);
  if (lines.length === 0) throw new YamlError("empty document", 1);
  const state: State = { lines, pos: 0 };
  const doc = parseNode(state, lines[0].indent);
  if (state.pos < lines.length) throw new YamlError("unexpected content after the document", lines[state.pos].n);
  return doc;
}

const isSeqItem = (content: string) => content === "-" || content.startsWith("- ");
const isFlowStart = (t: string) => t.startsWith("[") || t.startsWith("{");

type Scan = { depth: number; inS: boolean; inD: boolean };

function scanFlow(s: string, st: Scan): Scan {
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (st.inD) {
      if (c === "\\") i++;
      else if (c === '"') st.inD = false;
    } else if (st.inS) {
      if (c === "'") { if (s[i + 1] === "'") i++; else st.inS = false; }
    } else if (c === '"') st.inD = true;
    else if (c === "'") st.inS = true;
    else if (c === "[" || c === "{") st.depth++;
    else if (c === "]" || c === "}") st.depth--;
  }
  return st;
}

// joinFlow consumes continuation lines of a flow value until its brackets
// balance; running out of lines is an unterminated error at the opener.
function joinFlow(state: State, firstText: string, lineNo: number): string {
  const st = scanFlow(firstText, { depth: 0, inS: false, inD: false });
  let joined = firstText;
  while (st.depth > 0) {
    const next = state.lines[state.pos];
    if (!next) throw new YamlError("unterminated flow value", lineNo);
    state.pos++;
    joined += " " + next.content;
    scanFlow(next.content, st);
  }
  return joined;
}

function parseNode(state: State, indent: number): Yaml {
  const ln = state.lines[state.pos];
  if (ln.indent !== indent) throw new YamlError("unexpected indentation", ln.n);
  return isSeqItem(ln.content) ? parseSequence(state, indent) : parseMapping(state, indent);
}

function parseMapping(state: State, indent: number): YamlMap {
  const map: YamlMap = {};
  while (state.pos < state.lines.length) {
    const ln = state.lines[state.pos];
    if (ln.indent < indent) break;
    if (ln.indent > indent) throw new YamlError("unexpected indentation", ln.n);
    if (isSeqItem(ln.content)) throw new YamlError("unexpected list item in a mapping", ln.n);
    const m = KEY_RE.exec(ln.content);
    if (!m) throw new YamlError("expected `key: value`", ln.n);
    const key = m[1];
    if (Object.prototype.hasOwnProperty.call(map, key)) throw new YamlError("duplicate key " + key, ln.n);
    state.pos++;
    if (m[2] !== undefined && m[2].trim() !== "") {
      let v = m[2].trim();
      if (isFlowStart(v)) v = joinFlow(state, v, ln.n);
      map[key] = parseScalarOrFlow(v, ln.n);
      continue;
    }
    const next = state.lines[state.pos];
    if (next && next.indent > indent) {
      map[key] = parseNode(state, next.indent);
    } else if (next && next.indent === indent && isSeqItem(next.content)) {
      map[key] = parseSequence(state, indent);
    } else {
      map[key] = null;
    }
  }
  return map;
}

function parseSequence(state: State, indent: number): Yaml[] {
  const arr: Yaml[] = [];
  while (state.pos < state.lines.length) {
    const ln = state.lines[state.pos];
    if (ln.indent !== indent || !isSeqItem(ln.content)) break;
    const rest = ln.content === "-" ? "" : ln.content.slice(2).trim();
    if (rest === "") {
      state.pos++;
      const next = state.lines[state.pos];
      arr.push(next && next.indent > indent ? parseNode(state, next.indent) : null);
    } else if (KEY_RE.test(rest)) {
      // A map item with its first key on the dash line: its keys live two
      // columns past the dash, so the line is rewritten and parsed there.
      state.lines[state.pos] = { n: ln.n, indent: indent + 2, content: rest };
      arr.push(parseMapping(state, indent + 2));
    } else {
      state.pos++;
      arr.push(parseScalarOrFlow(isFlowStart(rest) ? joinFlow(state, rest, ln.n) : rest, ln.n));
    }
  }
  return arr;
}

function parseScalarOrFlow(s: string, lineNo: number): Yaml {
  const t = s.trim();
  if (isFlowStart(t)) {
    const flow: Flow = { s: t, i: 0, line: lineNo };
    const v = parseFlow(flow);
    skipWS(flow);
    if (flow.i < flow.s.length) throw new YamlError("unexpected trailing content after flow value", lineNo);
    return v;
  }
  return parseScalarToken(t, lineNo);
}

function skipWS(f: Flow) {
  while (f.i < f.s.length && (f.s[f.i] === " " || f.s[f.i] === "\t")) f.i++;
}

function parseFlow(f: Flow): Yaml {
  skipWS(f);
  const c = f.s[f.i];
  if (c === "[") {
    f.i++;
    const arr: Yaml[] = [];
    skipWS(f);
    if (f.s[f.i] === "]") { f.i++; return arr; }
    for (;;) {
      arr.push(parseFlow(f));
      skipWS(f);
      if (f.s[f.i] === ",") { f.i++; continue; }
      if (f.s[f.i] === "]") { f.i++; return arr; }
      throw new YamlError("expected `,` or `]` in flow list", f.line);
    }
  }
  if (c === "{") {
    f.i++;
    const map: YamlMap = {};
    skipWS(f);
    if (f.s[f.i] === "}") { f.i++; return map; }
    for (;;) {
      skipWS(f);
      const key = parseFlowKey(f);
      skipWS(f);
      if (f.s[f.i] !== ":") throw new YamlError("expected `:` in flow map", f.line);
      f.i++;
      map[key] = parseFlow(f);
      skipWS(f);
      if (f.s[f.i] === ",") { f.i++; continue; }
      if (f.s[f.i] === "}") { f.i++; return map; }
      throw new YamlError("expected `,` or `}` in flow map", f.line);
    }
  }
  if (c === '"' || c === "'") return parseQuoted(f);
  let j = f.i;
  while (j < f.s.length && !",]}".includes(f.s[j])) j++;
  const tok = f.s.slice(f.i, j).trim();
  if (tok === "") throw new YamlError("empty flow scalar", f.line);
  f.i = j;
  return plainScalar(tok);
}

function parseFlowKey(f: Flow): string {
  if (f.s[f.i] === '"' || f.s[f.i] === "'") return String(parseQuoted(f));
  let j = f.i;
  while (j < f.s.length && !":,]}".includes(f.s[j])) j++;
  const key = f.s.slice(f.i, j).trim();
  if (key === "") throw new YamlError("empty key in flow map", f.line);
  f.i = j;
  return key;
}

const SIMPLE_ESCAPES: Record<string, string> = { n: "\n", t: "\t", r: "\r", '"': '"', "\\": "\\", "0": "\0" };

function parseQuoted(f: Flow): string {
  const q = f.s[f.i];
  let out = "";
  f.i++;
  while (f.i < f.s.length) {
    const c = f.s[f.i];
    if (q === '"' && c === "\\") {
      const esc = f.s[f.i + 1];
      if (esc === "u") {
        out += String.fromCharCode(parseInt(f.s.slice(f.i + 2, f.i + 6), 16));
        f.i += 6;
        continue;
      }
      if (!(esc in SIMPLE_ESCAPES)) throw new YamlError("unsupported escape \\" + esc, f.line);
      out += SIMPLE_ESCAPES[esc];
      f.i += 2;
      continue;
    }
    if (c === q) {
      if (q === "'" && f.s[f.i + 1] === "'") { out += "'"; f.i += 2; continue; }
      f.i++;
      return out;
    }
    out += c;
    f.i++;
  }
  throw new YamlError("unterminated quoted string", f.line);
}

function parseScalarToken(t: string, lineNo: number): Yaml {
  if (t.startsWith('"') || t.startsWith("'")) {
    const f: Flow = { s: t, i: 0, line: lineNo };
    const v = parseQuoted(f);
    skipWS(f);
    if (f.i < f.s.length) throw new YamlError("unexpected content after quoted string", lineNo);
    return v;
  }
  if (t.startsWith("|") || t.startsWith(">")) {
    throw new YamlError("block scalars (| and >) are not supported here, use a quoted string", lineNo);
  }
  return plainScalar(t);
}

function plainScalar(t: string): Yaml {
  if (t === "null" || t === "~") return null;
  if (t === "true") return true;
  if (t === "false") return false;
  if (/^[-+]?\d+$/.test(t)) return parseInt(t, 10);
  if (/^[-+]?\d*\.\d+$/.test(t)) return parseFloat(t);
  return t;
}

// ---------- emitter ----------

// KEY_ORDER is the canonical key order per block, the spec's own; keys not
// listed follow in insertion order so nothing silently vanishes.
const KEY_ORDER: Record<string, string[]> = {
  "": ["apiVersion", "kind", "metadata", "spec"],
  metadata: ["name", "description"],
  spec: ["priority", "match", "capture", "rules", "escape"],
  match: ["roles", "users", "identity"],
  identity: ["userType", "agencyMode", "swarmId"],
  capture: ["conversations", "mode"],
  rules: ["id", "events", "tools", "apps", "toolNames", "command", "paths", "interpreters", "require", "mode", "approve", "effect", "reason", "obligations"],
  approve: ["class", "deciders", "roles", "timeoutSeconds", "selfApproval", "retryTTLSeconds", "ticketTTLSeconds", "grantTTLSeconds", "bind", "binding", "notify"],
  toolNames: ["allow", "deny"],
  paths: ["allow", "deny"],
  interpreters: ["allow", "deny"],
  command: ["allowPatterns", "denyPatterns"],
  require: ["attestation", "deviceCert", "harness"],
  escape: ["rego"],
};

const PLAIN_SAFE = /^[A-Za-z0-9_][A-Za-z0-9_./-]*$/;
const RESERVED = ["true", "false", "null", "yes", "no", "on", "off"];

function emitScalar(v: Yaml): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  const s = String(v);
  if (PLAIN_SAFE.test(s) && !RESERVED.includes(s.toLowerCase()) && !/^[\d+-]/.test(s)) return s;
  return JSON.stringify(s);
}

const isScalar = (v: Yaml): boolean => v === null || typeof v !== "object";

function orderedKeys(obj: YamlMap, block: string): string[] {
  const order = KEY_ORDER[block] || [];
  const keys = Object.keys(obj).filter((k) => obj[k] !== undefined);
  const rank = (k: string) => {
    const i = order.indexOf(k);
    return i === -1 ? order.length + keys.indexOf(k) : i;
  };
  return keys.slice().sort((a, b) => rank(a) - rank(b));
}

function emptyValue(v: Yaml | undefined): boolean {
  if (v === undefined) return true;
  if (Array.isArray(v)) return v.length === 0;
  if (v !== null && typeof v === "object") return orderedKeys(v, "").every((k) => emptyValue(v[k]));
  return false;
}

function emitBlock(v: Yaml, indent: number, block: string, out: string[]) {
  const pad = "  ".repeat(indent);
  if (Array.isArray(v)) {
    for (const item of v) {
      if (isScalar(item)) {
        out.push(pad + "- " + emitScalar(item));
      } else {
        const map = item as YamlMap;
        const keys = orderedKeys(map, block).filter((k) => !emptyValue(map[k]));
        if (keys.length === 0) { out.push(pad + "- {}"); continue; }
        let first = true;
        for (const k of keys) {
          emitEntry(k, map[k], first ? pad + "- " : pad + "  ", indent + 1, out);
          first = false;
        }
      }
    }
    return;
  }
  const map = (v || {}) as YamlMap;
  for (const k of orderedKeys(map, block)) {
    if (emptyValue(map[k])) continue;
    emitEntry(k, map[k], pad, indent, out);
  }
}

function emitEntry(key: string, v: Yaml, prefix: string, indent: number, out: string[]) {
  if (isScalar(v)) {
    out.push(prefix + key + ": " + emitScalar(v));
  } else if (Array.isArray(v) && v.every(isScalar)) {
    out.push(prefix + key + ": [" + v.map(emitScalar).join(", ") + "]");
  } else {
    out.push(prefix + key + ":");
    emitBlock(v, indent + 1, key, out);
  }
}

// emitPolicy renders a document as canonical PolicySet YAML. Empty lists
// and maps are left out: they mean absent.
export function emitPolicy(doc: Yaml): string {
  const out: string[] = [];
  emitBlock(doc || {}, 0, "", out);
  return out.join("\n") + "\n";
}

// asMap reads a value as a mapping, or an empty one.
export function asMap(v: Yaml | undefined): YamlMap {
  return v && typeof v === "object" && !Array.isArray(v) ? v : {};
}

// asList reads a value as a list, or an empty one.
export function asList(v: Yaml | undefined): Yaml[] {
  return Array.isArray(v) ? v : [];
}
