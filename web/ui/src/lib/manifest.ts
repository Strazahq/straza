// The app.yaml the Add MCP server wizard writes, and the pure readers the
// wizard and the server page share: the form shape, the name rule, the
// three ways a secret travels, the manifest built from the answers, and
// the YAML the fold shows and the install sends. No React here, so every
// screen that speaks about a
// server speaks the same words.
import type { ManifestDoc } from "./api";

// Form is every answer the wizard can hold. mode is the runtime card that
// is open (import is a card, not a runtime); runtime is what the manifest
// writes.
export type Form = {
  name: string;
  desc: string;
  mode: string;
  runtime: string;
  url: string;
  exec: string;
  args: string;
  image: string;
  imported: { doc: ManifestDoc; text: string } | null;
  cred: string;
  agents: string;
  secret: string;
  sentAs: string;
  headerName: string;
  envName: string;
  provider: string;
  scopes: string;
};

export const EMPTY_FORM: Form = { name: "", desc: "", mode: "remote", runtime: "remote", url: "", exec: "", args: "", image: "", imported: null, cred: "none", agents: "own", secret: "", sentAs: "header", headerName: "", envName: "", provider: "", scopes: "" };

// The manifest's own name rule (internal/manager/manifest.go nameRe).
export const NAME_RE = /^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$/;
export const NAME_RULE = "A server name is lowercase letters, digits and hyphens, up to 64 characters, and starts and ends with a letter or digit.";

export type NameCheck = { level: "error" | "ok" | "note"; text: string; twin?: { id: string; name: string } };

// serverTaken is the Server step's sentence for a name a live server has,
// which a save's check refuses too with the next step after it: a draft put
// of a live server's name would change that server.
export const serverTaken = (name: string) => "A server named " + name + " is already installed.";
export const SERVER_PICK_ANOTHER = "Go back to Server and pick another name.";

// appNameCheck mirrors the role wizard's name check for servers: the pattern
// as you type, then the apps list for an exact twin, which is a hard stop
// with the row attached so the caller can offer "Open it".
export function appNameCheck(raw: string, apps: { id: string; name: string }[] | null): NameCheck | null {
  const v = (raw || "").trim();
  if (!v) return null;
  if (!NAME_RE.test(v)) return { level: "error", text: NAME_RULE };
  if (!apps) return { level: "note", text: "The servers list could not be read, so this name is not checked here. Saving still refuses a name that a server already has." };
  const exact = apps.find((a) => a.name === v);
  if (exact) return { level: "error", twin: exact, text: serverTaken(v) };
  return { level: "ok", text: v + " is free" };
}

const splitWords = (s: string) => String(s || "").split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

// The three ways a static secret travels, keyed by the picker value. HTTP
// runtimes send a header; a command or container reads an environment
// variable, which is why the picker changes shape with the runtime.
export const SENT_AS: Record<string, { label: string; as: string; name?: string; template: string }> = {
  header: { label: "A named header", as: "header", template: "{{secret}}" },
  bearer: { label: "A bearer token in Authorization", as: "header", name: "Authorization", template: "Bearer {{secret}}" },
  basic: { label: "Basic auth in Authorization", as: "header", name: "Authorization", template: "Basic {{secret}}" },
  env: { label: "An environment variable", as: "env", template: "{{secret}}" },
};
export const HTTP_SENT = ["header", "bearer", "basic"];
export const TOKEN_SENT = ["bearer", "header", "basic"];

// sentFor is the effective "sent as" choice: an HTTP runtime offers the
// three header shapes, a command or container only the environment
// variable, whatever the form still holds from an earlier runtime.
export function sentFor(f: Form): string {
  if (f.runtime !== "remote") return "env";
  return HTTP_SENT.includes(f.sentAs) ? f.sentAs : "header";
}

export function injectFor(f: Form): { as: string; name: string; template: string } {
  const s = SENT_AS[sentFor(f)];
  const name = s.name || (s.as === "env" ? f.envName : f.headerName).trim();
  return { as: s.as, name, template: s.template };
}

// leaveRemote drops a caller kind when the runtime stops being HTTP: the
// caller cards grey on the Credential step, so the form must not keep
// writing a kind the server refuses on a process runtime.
export const leaveRemote = (f: Form, runtime: string): Partial<Form> => (runtime !== "remote" && (f.cred === "token" || f.cred === "oauth") ? { cred: "none" } : {});

// A sign-in token is always a bearer, so oauth writes that inject without
// asking. The wizard stores a shared secret for a static server, and for a
// token server only when agents with no token of their own fall back to it.
const OAUTH_INJECT = { as: "header", name: "Authorization", template: "Bearer {{secret}}" };
export const needsSecret = (f: Form) => f.cred === "static" || (f.cred === "token" && f.agents === "shared");

// buildManifest turns the wizard's answers into the document the server
// installs. Exposure stays every tool and limits stay the defaults: the
// wizard writes only what it asked. An imported registry record is the
// base when one was pasted.
export function buildManifest(f: Form): ManifestDoc {
  const name = f.name.trim();
  const doc: ManifestDoc = f.imported && f.imported.doc
    ? JSON.parse(JSON.stringify(f.imported.doc))
    : { apiVersion: "straza.dev/v1beta1", kind: "App", metadata: { name }, server: { name, version: "0" }, straza: { runtime: { kind: f.runtime } } };
  doc.metadata = doc.metadata || {};
  doc.metadata.name = name;
  if (f.desc.trim()) doc.metadata.description = f.desc.trim();
  else delete doc.metadata.description;
  doc.straza = doc.straza || {};
  // The form's runtime fields overlay whatever the base carries, so an
  // imported record keeps its environment and workdir while an edited url
  // or command still applies.
  const rt: NonNullable<NonNullable<ManifestDoc["straza"]>["runtime"]> = { ...(doc.straza.runtime || {}), kind: f.runtime };
  (["remote", "command", "oci"] as const).forEach((k) => { if (k !== f.runtime) delete rt[k]; });
  if (f.runtime === "remote") rt.remote = { ...(rt.remote || {}), url: f.url.trim() };
  if (f.runtime === "command") {
    rt.command = { ...(rt.command || {}), exec: f.exec.trim() };
    const args = splitWords(f.args);
    if (args.length) rt.command.args = args; else delete rt.command.args;
  }
  if (f.runtime === "oci") rt.oci = { ...(rt.oci || {}), image: f.image.trim() };
  doc.straza.runtime = rt;
  delete doc.straza.credential;
  if (f.cred === "static") doc.straza.credential = { kind: "static", inject: injectFor(f) };
  if (f.cred === "token") doc.straza.credential = { kind: "token", agents: f.agents || "own", inject: injectFor(f) };
  if (f.cred === "oauth") {
    const scopes = splitWords(f.scopes);
    doc.straza.credential = { kind: "oauth", agents: f.agents || "own", oauth: scopes.length ? { provider: f.provider, scopes } : { provider: f.provider }, inject: OAUTH_INJECT };
  }
  if (!doc.straza.exposure) doc.straza.exposure = { tools: ["*"] };
  return doc;
}

// A string that a YAML decoder would resolve to something other than a
// string, so it has to travel quoted to come back as it was written: the
// boolean words of YAML 1.1 and 1.2, null, an integer in any base and with
// underscores, a float with a leading or trailing dot or an exponent,
// infinity and not-a-number, and anything opening with a date. A pasted
// registry record keeps values of every shape, and the server block is
// stored verbatim, so a rewrite here would change what it says.
const NOT_A_STRING = new RegExp([
  "^(?:true|false|yes|no|on|off|y|n|null|~)$",
  "^[-+]?(?:0x[0-9a-f_]+|0o[0-7_]+|0b[01_]+|\\d[\\d_]*)$",
  "^[-+]?\\d[\\d_]*\\.[\\d_]*(?:e[-+]?\\d+)?$",
  "^[-+]?\\.[\\d_]+(?:e[-+]?\\d+)?$",
  "^[-+]?\\d[\\d_]*e[-+]?\\d+$",
  "^[-+]?\\.(?:inf|nan)$",
  "^\\d{4}-\\d{1,2}-\\d{1,2}",
].join("|"), "i");

// scalar writes one YAML scalar, quoted only where a bare word would be
// read as something else: empty, a line break, a mapping or comment mark
// inside or a colon at the end, a leading indicator, the secret
// placeholder, or a word a decoder reads as another type. Keys pass
// through it too, since a pasted registry record brings keys of its own.
function scalar(v: unknown): string {
  if (typeof v !== "string") return v === null || v === undefined ? "null" : String(v);
  if (v === "" || /[\n\r\t]|: | #|:$|^[-?:,[\]{}#&*!|>'"%@`]|^\s|\s$|\{\{/.test(v) || NOT_A_STRING.test(v)) return JSON.stringify(v);
  return v;
}

// toYAML renders a manifest object as the app.yaml the fold shows and the
// install sends. Keys keep insertion order; nested maps and lists indent by
// two; a map inside a list starts on the dash line; an empty list or map
// writes as [] or {}.
export function toYAML(v: unknown, indent = 0): string {
  const pad = " ".repeat(indent);
  let out = "";
  if (Array.isArray(v)) {
    for (const it of v) {
      if (it && typeof it === "object") {
        const inner = toYAML(it, indent + 2);
        out += inner ? pad + "- " + inner.slice(indent + 2) : pad + "- " + (Array.isArray(it) ? "[]" : "{}") + "\n";
      } else {
        out += pad + "- " + scalar(it) + "\n";
      }
    }
    return out;
  }
  const obj = v as Record<string, unknown>;
  for (const k of Object.keys(obj)) {
    const x = obj[k];
    if (x === undefined) continue;
    if (x && typeof x === "object") {
      if (Array.isArray(x) && x.length === 0) { out += pad + scalar(k) + ": []\n"; continue; }
      if (!Array.isArray(x) && Object.keys(x as object).length === 0) { out += pad + scalar(k) + ": {}\n"; continue; }
      out += pad + scalar(k) + ":\n" + toYAML(x, indent + 2);
    } else {
      out += pad + scalar(k) + ": " + scalar(x) + "\n";
    }
  }
  return out;
}

export const manifestYAML = (doc: ManifestDoc) => toYAML(doc);

// importFields reads the form's runtime fields out of a converted registry
// manifest, so the cards above the paste box show what the record said.
export function importFields(doc: ManifestDoc): Pick<Form, "runtime" | "url" | "exec" | "args" | "image"> {
  const rt = (doc && doc.straza && doc.straza.runtime) || {};
  const out = { runtime: rt.kind || "remote", url: "", exec: "", args: "", image: "" };
  if (rt.remote) out.url = rt.remote.url || "";
  if (rt.command) { out.exec = rt.command.exec || ""; out.args = (rt.command.args || []).join(" "); }
  if (rt.oci) out.image = rt.oci.image || "";
  return out;
}

// manifestReady says whether the form has enough to try a dry run at all
// (an empty url is a parser error the operator did not earn). A credential
// field the form itself names as missing, the header or variable name or
// the provider, waits for the person too: Next says it at the field.
export function manifestReady(f: Form): boolean {
  if (!f.name.trim()) return false;
  if ((f.cred === "static" || f.cred === "token") && !injectFor(f).name) return false;
  if (f.cred === "oauth" && !f.provider) return false;
  if (f.runtime === "remote") return !!f.url.trim();
  if (f.runtime === "command") return !!f.exec.trim();
  if (f.runtime === "oci") return !!f.image.trim();
  return false;
}
