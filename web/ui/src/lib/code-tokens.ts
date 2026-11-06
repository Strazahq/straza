// codeTokens cuts the body of a here-document into strings, comments,
// keywords and numbers for the audit sheet's code block. It colors only;
// every character of src is in exactly one token, in order.

export type CodeToken = { t: "" | "kw" | "str" | "com" | "num"; s: string };

const KW: Record<string, string> = {
  python: "and as assert async await break class continue def del elif else except False finally for from global if import in is lambda None nonlocal not or pass raise return True try while with yield",
  javascript: "async await break case catch class const continue default delete do else export extends false finally for from function if import in instanceof let new null of return switch this throw true try typeof undefined var void while yield",
  typescript: "async await break case catch class const continue default delete do else export extends false finally for from function if import in instanceof interface let new null of return switch this throw true try type typeof undefined var void while yield",
  shell: "if then else elif fi for in do done case esac while until function select",
  sql: "SELECT FROM WHERE INSERT INTO VALUES UPDATE SET DELETE CREATE TABLE ALTER DROP JOIN LEFT RIGHT INNER OUTER ON GROUP BY ORDER HAVING LIMIT AND OR NOT NULL AS DISTINCT UNION",
  json: "true false null",
  yaml: "true false null",
};
const HASH_COMMENT = new Set(["python", "shell", "yaml", "ruby", "perl", "toml"]);
const SLASH_COMMENT = new Set(["javascript", "typescript", "go"]);

export function codeTokens(src: string, lang: string): CodeToken[] {
  if (!KW[lang] && !HASH_COMMENT.has(lang) && !SLASH_COMMENT.has(lang)) return [{ t: "", s: src }];
  const kw = new Set((KW[lang] || "").split(" "));
  const out: CodeToken[] = [];
  const push = (t: CodeToken["t"], s: string) => { if (s) out.push({ t, s }); };
  const n = src.length;
  let i = 0;
  while (i < n) {
    const c = src[i];
    if (lang === "python" && (src.startsWith("'''", i) || src.startsWith('"""', i))) {
      const e = src.indexOf(src.slice(i, i + 3), i + 3);
      const end = e < 0 ? n : e + 3;
      push("str", src.slice(i, end)); i = end; continue;
    }
    if (c === "'" || c === '"' || (c === "`" && SLASH_COMMENT.has(lang))) {
      let j = i + 1;
      while (j < n && src[j] !== c && !(src[j] === "\n" && c !== "`")) j += src[j] === "\\" ? 2 : 1;
      const end = Math.min(j + 1, n);
      push("str", src.slice(i, end)); i = end; continue;
    }
    if ((HASH_COMMENT.has(lang) && c === "#") || (SLASH_COMMENT.has(lang) && src.startsWith("//", i)) || (lang === "sql" && src.startsWith("--", i))) {
      let e = src.indexOf("\n", i);
      if (e < 0) e = n;
      push("com", src.slice(i, e)); i = e; continue;
    }
    if (SLASH_COMMENT.has(lang) && src.startsWith("/*", i)) {
      const e = src.indexOf("*/", i + 2);
      const end = e < 0 ? n : e + 2;
      push("com", src.slice(i, end)); i = end; continue;
    }
    if (/[A-Za-z_]/.test(c)) {
      let j = i;
      while (j < n && /[A-Za-z0-9_]/.test(src[j])) j++;
      const w = src.slice(i, j);
      push(kw.has(lang === "sql" ? w.toUpperCase() : w) ? "kw" : "", w); i = j; continue;
    }
    if (/[0-9]/.test(c)) {
      let j = i;
      while (j < n && /[0-9._]/.test(src[j])) j++;
      push("num", src.slice(i, j)); i = j; continue;
    }
    let j = i + 1;
    while (j < n && !/[A-Za-z0-9_'"`#/-]/.test(src[j])) j++;
    push("", src.slice(i, j)); i = j;
  }
  return out;
}
