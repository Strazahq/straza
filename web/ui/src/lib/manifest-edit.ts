// The edit model of the Change sheets on a server's Overview. Each card
// reads its fields out of the installed manifest and writes them back into
// a copy of it, so every key outside the card's own block, the verbatim
// server record included, stays exactly as installed. A field left as it
// was keeps its installed value, so a read then a write with no change
// gives back the same document. No React here.
import type { Credential, ManifestDoc } from "./api";
import { HTTP_SENT, SENT_AS } from "./manifest";
import { KIND_NAME, exposeWords, rpsWords, sentWords, timeoutWords, transportWords } from "./server-words";
import { NONE, agentsLine } from "./words";

type Straza = NonNullable<ManifestDoc["straza"]>;
type Runtime = NonNullable<Straza["runtime"]>;
type Inject = NonNullable<Credential["inject"]>;
type Limits = NonNullable<Straza["limits"]>;

const SECRET = "{{secret}}";

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);
const scopeWords = (s: string) => s.split(/[\s,]+/).filter(Boolean);
// callerKind says whether a credential kind gives each caller their own.
export const callerKind = (kind: string) => kind === "token" || kind === "oauth";

// joinArgs writes a command's arguments as one line of words. An argument
// that holds a space or a double quote, or is empty, is written in double
// quotes, so splitArgs reads the same arguments back.
export function joinArgs(args: string[]): string {
  return args.map((a) => (a === "" || /[\s"]/.test(a) ? JSON.stringify(a) : a)).join(" ");
}

// splitArgs reads a line of words back into arguments: a run of characters
// without a space, or a double-quoted string with its escapes.
export function splitArgs(text: string): string[] {
  const out: string[] = [];
  for (const m of text.matchAll(/"(?:[^"\\]|\\.)*"|\S+/g)) {
    const w = m[0];
    if (w.length > 1 && w.startsWith('"') && w.endsWith('"')) {
      try { out.push(String(JSON.parse(w))); continue; } catch { /* a bad escape is kept as typed */ }
    }
    out.push(w);
  }
  return out;
}

export type EnvPair = { name: string; value: string };

// envPairs reads a runtime's env entries as name and value pairs.
export function envPairs(env: unknown[] | undefined): EnvPair[] {
  return (env || []).map((e) => {
    const o = (e && typeof e === "object" ? e : {}) as Record<string, unknown>;
    return { name: String(o.name ?? ""), value: String(o.value ?? "") };
  });
}

// sentKey names the sent-as choice an inject block matches, one of the
// SENT_AS keys, or "keep" for a shape the picker cannot write, such as a
// template other than the three it offers. An absent block answers "".
export function sentKey(inj: Inject | undefined): string {
  if (!inj) return "";
  const t = inj.template || SECRET;
  if (inj.as === "env") return t === SECRET ? "env" : "keep";
  if (inj.as !== "header") return "keep";
  if (inj.name === "Authorization" && t === "Bearer " + SECRET) return "bearer";
  if (inj.name === "Authorization" && t === "Basic " + SECRET) return "basic";
  return t === SECRET ? "header" : "keep";
}

// injectOf is the inject block of a sent-as choice: a named header takes
// headerName, an environment variable takes envName.
export function injectOf(sent: string, headerName: string, envName: string): Inject {
  const s = SENT_AS[sent] || SENT_AS.header;
  return { as: s.as, name: s.name || (s.as === "env" ? envName : headerName).trim(), template: s.template };
}

const sameInject = (a: Inject, b: Inject) => a.as === b.as && a.name === b.name && (a.template || SECRET) === (b.template || SECRET);

// injectMoves says whether the installed credential's inject block no
// longer fits a runtime kind: a header is valid only on remote, an
// environment variable only on command and oci (the server's validator).
export function injectMoves(doc: ManifestDoc, kind: string): boolean {
  const inj = doc.straza?.credential?.inject;
  if (!inj) return false;
  return kind === "remote" ? inj.as !== "header" : inj.as !== "env";
}

// Connection is the Connection card: the runtime kind and its fields, and
// how a shared secret travels once the transport moves between HTTP and a
// process, which the sheet asks only then: sent and headerName for HTTP,
// envName for a command or container.
export type Connection = { kind: string; url: string; exec: string; args: string; workdir: string; env: EnvPair[]; image: string; sent: string; headerName: string; envName: string };

// readConnection reads the Connection card's fields out of doc.
export function readConnection(doc: ManifestDoc): Connection {
  const rt = doc.straza?.runtime || {};
  const inj = doc.straza?.credential?.inject;
  const key = sentKey(inj);
  return {
    kind: rt.kind || "remote",
    url: rt.remote?.url || "",
    exec: rt.command?.exec || "",
    args: joinArgs(rt.command?.args || []),
    workdir: rt.command?.workdir || "",
    env: envPairs(rt.command?.env),
    image: rt.oci?.image || "",
    sent: HTTP_SENT.includes(key) ? key : "bearer",
    headerName: inj?.as === "header" ? inj.name || "" : "",
    envName: inj?.as === "env" ? inj.name || "" : "",
  };
}

const BLANK: Connection = { kind: "", url: "", exec: "", args: "", workdir: "", env: [], image: "", sent: "", headerName: "", envName: "" };

// editConnection writes the Connection card into a copy of doc. The same
// kind keeps its installed block and changes only the fields that differ.
// A new kind drops the old block and writes its own. A credential whose
// inject no longer fits the new kind is rewritten to the shape c asks for.
export function editConnection(doc: ManifestDoc, c: Connection): ManifestDoc {
  const was = readConnection(doc);
  const out = clone(doc);
  const st = (out.straza = out.straza || {});
  const keep = c.kind === was.kind;
  const from = keep ? was : BLANK;
  const rt: Runtime = keep && st.runtime ? st.runtime : { kind: c.kind };
  if (c.kind === "remote") {
    const b = rt.remote || { url: "" };
    if (c.url !== from.url) b.url = c.url.trim();
    rt.remote = b;
  } else if (c.kind === "command") {
    const b = rt.command || { exec: "" };
    if (c.exec !== from.exec) b.exec = c.exec.trim();
    if (c.args !== from.args) {
      const args = splitArgs(c.args);
      if (args.length) b.args = args; else delete b.args;
    }
    if (c.workdir !== from.workdir) {
      if (c.workdir.trim()) b.workdir = c.workdir.trim(); else delete b.workdir;
    }
    if (!same(c.env, from.env)) {
      const env = c.env.filter((e) => e.name.trim() || e.value).map((e) => ({ name: e.name.trim(), value: e.value }));
      if (env.length) b.env = env; else delete b.env;
    }
    rt.command = b;
  } else if (c.kind === "oci") {
    const b = rt.oci || { image: "" };
    if (c.image !== from.image) b.image = c.image.trim();
    rt.oci = b;
  }
  st.runtime = rt;
  const cred = st.credential;
  if (cred && cred.inject && injectMoves(doc, c.kind)) {
    cred.inject = c.kind === "remote" ? injectOf(c.sent, c.headerName, "") : injectOf("env", "", c.envName);
  }
  return out;
}

// CredentialEdit is the Credential card: the kind, what agents with no
// credential of their own do, the sent-as choice with its header or
// variable name, and the identity provider and scopes of a sign-in.
export type CredentialEdit = { kind: string; agents: string; sent: string; headerName: string; envName: string; provider: string; scopes: string };

// defaultSent is the sent-as choice a credential starts on when its block
// says none: a bearer header on HTTP, an environment variable otherwise.
export const defaultSent = (runtime: string) => (runtime === "remote" ? "bearer" : "env");

// readCredential reads the Credential card's fields out of doc.
export function readCredential(doc: ManifestDoc): CredentialEdit {
  const cred = doc.straza?.credential;
  const inj = cred?.inject;
  return {
    kind: cred?.kind || "none",
    agents: cred?.agents || "own",
    sent: sentKey(inj) || defaultSent(doc.straza?.runtime?.kind || "remote"),
    headerName: inj?.as === "header" ? inj.name || "" : "",
    envName: inj?.as === "env" ? inj.name || "" : "",
    provider: cred?.oauth?.provider || "",
    scopes: (cred?.oauth?.scopes || []).join(" "),
  };
}

// editCredential writes the Credential card into a copy of doc. None
// drops the block. The same kind keeps its installed block and changes
// what differs. A new kind writes a fresh block in the wizard's shape. The
// sent-as choice "keep" leaves the installed inject exactly as written,
// and a sign-in always sends a bearer header.
export function editCredential(doc: ManifestDoc, c: CredentialEdit): ManifestDoc {
  const was = readCredential(doc);
  const out = clone(doc);
  const st = (out.straza = out.straza || {});
  if (c.kind === "none") {
    if (was.kind !== "none") delete st.credential;
    return out;
  }
  const keep = c.kind === was.kind && !!st.credential;
  const cred: Credential = keep && st.credential ? st.credential : { kind: c.kind };
  if (callerKind(c.kind) && (!keep || c.agents !== was.agents)) cred.agents = c.agents || "own";
  if (c.kind === "oauth" && (!keep || c.provider !== was.provider || c.scopes !== was.scopes)) {
    const scopes = scopeWords(c.scopes);
    cred.oauth = scopes.length ? { provider: c.provider, scopes } : { provider: c.provider };
  }
  const old = doc.straza?.credential?.inject;
  const untouched = keep && c.sent === was.sent && c.headerName === was.headerName && c.envName === was.envName;
  if (c.kind === "oauth") {
    if (!keep || !cred.inject) cred.inject = injectOf("bearer", "", "");
  } else if (c.sent === "keep") {
    if (old) cred.inject = clone(old);
  } else if (!untouched) {
    const next = injectOf(c.sent, c.headerName, c.envName);
    if (!(keep && old && sameInject(old, next))) cred.inject = next;
  }
  st.credential = cred;
  return out;
}

// Settings is the Settings card: the description, the tools exposed as
// all or the picked exact names plus the patterns kept as written, and the
// two limits as typed.
export type Settings = { desc: string; expose: "all" | "only"; picked: string[]; kept: string[]; rps: string; timeout: string };

const allTools = (list: string[] | undefined) => !list || list.length === 0 || list.includes("*");

const escaped = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// coveredBy names the first exposure pattern that already admits a tool,
// or "" when none does. Straza reads an exposure entry the way the manager
// does: "*" stands for any run of characters and everything else is
// literal (internal/manager/manager.go matchGlob). The binding matcher in
// words.ts is a different grammar and does not answer this question.
export function coveredBy(patterns: string[], tool: string): string {
  return patterns.find((p) => p.includes("*") && new RegExp("^" + p.split("*").map(escaped).join(".*") + "$").test(tool)) || "";
}

// readSettings sorts the exposure list against the server's current tools:
// an exact current name is a pick, anything else but "*" is kept as
// written, since the checkboxes cannot show it.
export function readSettings(doc: ManifestDoc, tools: string[]): Settings {
  const list = doc.straza?.exposure?.tools || [];
  const exact = (t: string) => !t.includes("*") && tools.includes(t);
  const lim = doc.straza?.limits || {};
  return {
    desc: doc.metadata?.description || "",
    expose: allTools(list) ? "all" : "only",
    picked: allTools(list) ? [] : list.filter(exact),
    kept: list.filter((t) => t !== "*" && !exact(t)),
    rps: lim.rps ? String(lim.rps) : "",
    timeout: lim.timeoutSeconds ? String(lim.timeoutSeconds) : "",
  };
}

// editSettings writes the Settings card into a copy of doc. An exposure
// list that keeps the same entries keeps its installed order. An emptied
// limits block goes only when no cpu or mem is left in it.
export function editSettings(doc: ManifestDoc, s: Settings, tools: string[]): ManifestDoc {
  const was = readSettings(doc, tools);
  const out = clone(doc);
  if (s.desc !== was.desc) {
    const md = (out.metadata = out.metadata || {});
    if (s.desc.trim()) md.description = s.desc.trim(); else delete md.description;
  }
  const st = (out.straza = out.straza || {});
  const now = doc.straza?.exposure?.tools || [];
  if (s.expose === "all" && !allTools(now)) st.exposure = { ...(st.exposure || {}), tools: ["*"] };
  if (s.expose === "only") {
    const want = [...s.kept, ...s.picked];
    const next = [...now.filter((t) => want.includes(t)), ...want.filter((t) => !now.includes(t))];
    if (allTools(now) || !same([...next].sort(), [...now].sort())) st.exposure = { ...(st.exposure || {}), tools: next };
  }
  if (s.rps !== was.rps || s.timeout !== was.timeout) {
    const lim: Limits = { ...(st.limits || {}) };
    if (s.rps !== was.rps) {
      if (s.rps.trim()) lim.rps = Number(s.rps.trim()); else delete lim.rps;
    }
    if (s.timeout !== was.timeout) {
      if (s.timeout.trim()) lim.timeoutSeconds = Number(s.timeout.trim()); else delete lim.timeoutSeconds;
    }
    if (Object.keys(lim).length) st.limits = lim; else delete st.limits;
  }
  return out;
}

// ChangeRow is one line of "What changes": the field, its words now, and
// its words after saving.
export type ChangeRow = [string, string, string];

const envWords = (env: EnvPair[]) => env.map((e) => e.name + "=" + e.value).join(", ") || NONE;

// changeRows lists the fields that differ between the installed manifest
// and the edited one, in the order the cards draw them. Runtime fields
// show for the kind after saving, and serverWide names the default timeout.
export function changeRows(before: ManifestDoc, after: ManifestDoc, serverWide: number | null = null): ChangeRow[] {
  const rows: ChangeRow[] = [];
  const add = (label: string, was: string, now: string) => { if (was !== now) rows.push([label, was, now]); };
  const ra = before.straza?.runtime || {};
  const rb = after.straza?.runtime || {};
  const kb = rb.kind || "remote";
  add("Transport", transportWords(ra.kind || "remote"), transportWords(kb));
  if (kb === "remote") add("Address", ra.remote?.url || NONE, rb.remote?.url || NONE);
  if (kb === "command") {
    add("Executable", ra.command?.exec || NONE, rb.command?.exec || NONE);
    add("Arguments", joinArgs(ra.command?.args || []) || NONE, joinArgs(rb.command?.args || []) || NONE);
    add("Working directory", ra.command?.workdir || "not set", rb.command?.workdir || "not set");
  }
  if (kb !== "remote") add("Environment", envWords(envPairs(ra.command?.env || ra.oci?.env)), envWords(envPairs(rb.command?.env || rb.oci?.env)));
  if (kb === "oci") add("Image", ra.oci?.image || NONE, rb.oci?.image || NONE);
  const ca = before.straza?.credential;
  const cb = after.straza?.credential;
  const ka = ca?.kind || "none";
  const kc = cb?.kind || "none";
  add("Type", KIND_NAME[ka] || ka, KIND_NAME[kc] || kc);
  if (kc !== "none") add("Sent as", sentWords(ca), sentWords(cb));
  if (callerKind(kc)) add("Agents with nothing of their own", agentsLine(ca) || "not asked", agentsLine(cb));
  if (kc === "oauth") {
    add("Provider", ca?.oauth?.provider || NONE, cb?.oauth?.provider || NONE);
    add("Scopes", (ca?.oauth?.scopes || []).join(" ") || NONE, (cb?.oauth?.scopes || []).join(" ") || NONE);
  }
  add("Description", before.metadata?.description || NONE, after.metadata?.description || NONE);
  add("Tools exposed", exposeWords(before.straza?.exposure?.tools), exposeWords(after.straza?.exposure?.tools));
  add("Rate limit", rpsWords(before.straza?.limits?.rps), rpsWords(after.straza?.limits?.rps));
  add("Per-call timeout", timeoutWords(before.straza?.limits?.timeoutSeconds, serverWide), timeoutWords(after.straza?.limits?.timeoutSeconds, serverWide));
  return rows;
}
