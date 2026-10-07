// The import card's reader. The import endpoint answers the converted
// app.yaml as text, written by gopkg.in/yaml.v3 from the manifest struct,
// and the app carries no YAML parser, so this reads only what the
// conversion writes outside the verbatim server block: the runtime (kind,
// remote url, command exec, args, env and workdir, oci image and env), the
// namespace, and the static credential the one secret variable or header
// became (spec/app-manifest section 5). It reads by key path and
// indentation, so a key of the same name inside server: is never taken for
// the runtime's. A field it cannot read stays empty; nothing here guesses.
import type { ManifestDoc } from "@/lib/api";
import type { Form } from "@/lib/manifest";

export type EnvEntry = { name: string; value: string };
export type ReadCredential = { kind: string; as: string; name: string; template: string };
export type ReadImport = {
  kind: string;
  url: string;
  exec: string;
  args: string[];
  image: string;
  env: EnvEntry[];
  workdir: string;
  namespace: string;
  credential: ReadCredential | null;
};

// unquote reads one scalar: plain, double quoted with JSON escapes, or
// single quoted with a doubled quote. A block scalar is not read.
function unquote(raw: string): string {
  const v = raw.trim();
  if (v.startsWith('"')) {
    try { return String(JSON.parse(v)); } catch { return ""; }
  }
  if (v.startsWith("'")) return v.length > 1 && v.endsWith("'") ? v.slice(1, -1).replace(/''/g, "'") : "";
  if (v.startsWith("|") || v.startsWith(">")) return "";
  return v;
}

// flowList reads a one-line list such as [--stdio, "--port", '8080'].
function flowList(v: string): string[] {
  const inner = v.trim().replace(/^\[/, "").replace(/\]$/, "").trim();
  return inner ? inner.split(",").map(unquote).filter(Boolean) : [];
}

// KEY splits a mapping line into its key, plain or quoted, and the value.
const KEY = /^("(?:[^"\\]|\\.)*"|'(?:[^']|'')*'|[^\s:'"-][^:]*?):(?:\s+(.*))?$/;

const ENV_PATHS = ["straza.runtime.command.env", "straza.runtime.oci.env"];
const CRED = "straza.credential.";

// readImport walks the lines with a stack of open mapping keys. A list
// item under an env key opens one entry, and the keys indented under the
// item's dash fill it.
export function readImport(text: string): ReadImport {
  const out: ReadImport = { kind: "", url: "", exec: "", args: [], image: "", env: [], workdir: "", namespace: "", credential: null };
  const cred: ReadCredential = { kind: "", as: "", name: "", template: "" };
  const stack: { indent: number; key: string }[] = [];
  let item: { indent: number; entry: EnvEntry } | null = null;
  const setEnv = (entry: EnvEntry, key: string, value: string) => {
    if (key === "name") entry.name = unquote(value);
    if (key === "value") entry.value = unquote(value);
  };
  for (const raw of String(text || "").split(/\r?\n/)) {
    const body = raw.trim();
    if (!body || body.startsWith("#")) continue;
    const indent = raw.length - raw.trimStart().length;
    if (body === "-" || body.startsWith("- ")) {
      while (stack.length && stack[stack.length - 1].indent > indent) stack.pop();
      const path = stack.map((s) => s.key).join(".");
      if (path === "straza.runtime.command.args") {
        const arg = unquote(body.slice(1));
        if (arg) out.args.push(arg);
      } else if (ENV_PATHS.includes(path)) {
        const entry = { name: "", value: "" };
        out.env.push(entry);
        item = { indent, entry };
        const first = KEY.exec(body.slice(1).trim());
        if (first) setEnv(entry, unquote(first[1]), first[2] || "");
      }
      continue;
    }
    const m = KEY.exec(body);
    if (!m) continue;
    while (stack.length && stack[stack.length - 1].indent >= indent) stack.pop();
    const key = unquote(m[1]);
    const parent = stack.map((s) => s.key).join(".");
    const value = m[2] || "";
    if (item && indent > item.indent && ENV_PATHS.includes(parent)) {
      setEnv(item.entry, key, value);
      continue;
    }
    item = null;
    if (!value) {
      stack.push({ indent, key });
      continue;
    }
    const path = parent ? parent + "." + key : key;
    if (path === "straza.runtime.kind") out.kind = unquote(value);
    else if (path === "straza.runtime.remote.url") out.url = unquote(value);
    else if (path === "straza.runtime.command.exec") out.exec = unquote(value);
    else if (path === "straza.runtime.command.workdir") out.workdir = unquote(value);
    else if (path === "straza.runtime.oci.image") out.image = unquote(value);
    else if (path === "straza.runtime.command.args" && value.startsWith("[")) out.args = flowList(value);
    else if (path === "metadata.namespace") out.namespace = unquote(value);
    else if (path === CRED + "kind") cred.kind = unquote(value);
    else if (path === CRED + "inject.as") cred.as = unquote(value);
    else if (path === CRED + "inject.name") cred.name = unquote(value);
    else if (path === CRED + "inject.template") cred.template = unquote(value);
  }
  out.env = out.env.filter((e) => e.name);
  if (cred.kind) out.credential = cred;
  return out;
}

// importedDoc is the base manifest a converted record leaves behind: the
// runtime as read, with its env and workdir, the namespace, and the pasted
// record itself as the server block, which is what the import writes there
// verbatim. The form's runtime fields and credential overlay it in
// buildManifest. runtime is the kind the endpoint answered, used when the
// text did not say.
export function importedDoc(read: ReadImport, record: unknown, name: string, runtime: string): ManifestDoc {
  const kind = read.kind || runtime || "remote";
  const rt: NonNullable<NonNullable<ManifestDoc["straza"]>["runtime"]> = { kind };
  if (kind === "remote") rt.remote = { url: read.url };
  if (kind === "command") {
    rt.command = { exec: read.exec };
    if (read.args.length) rt.command.args = read.args;
    if (read.env.length) rt.command.env = read.env;
    if (read.workdir) rt.command.workdir = read.workdir;
  }
  if (kind === "oci") rt.oci = read.env.length ? { image: read.image, env: read.env } : { image: read.image };
  const server = record && typeof record === "object" && !Array.isArray(record) ? (record as Record<string, unknown>) : { name, version: "0" };
  const metadata: NonNullable<ManifestDoc["metadata"]> = read.namespace ? { name, namespace: read.namespace } : { name };
  return { apiVersion: "straza.dev/v1beta1", kind: "App", metadata, server, straza: { runtime: rt } };
}

// credentialFields maps the static credential a conversion wrote onto the
// credential card's answers. It answers null for an inject the cards
// cannot write, such as a template other than the three the sent-as
// picker offers, so the caller says so instead of rewriting it.
export function credentialFields(c: ReadCredential | null): Partial<Form> | null {
  if (!c || c.kind !== "static") return {};
  const template = c.template || "{{secret}}";
  if (c.as === "env" && template === "{{secret}}") return { cred: "static", sentAs: "env", envName: c.name };
  if (c.as !== "header") return null;
  if (c.name === "Authorization" && template === "Bearer {{secret}}") return { cred: "static", sentAs: "bearer" };
  if (c.name === "Authorization" && template === "Basic {{secret}}") return { cred: "static", sentAs: "basic" };
  if (template === "{{secret}}") return { cred: "static", sentAs: "header", headerName: c.name };
  return null;
}
