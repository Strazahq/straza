// The sentences and small readers the console's screens share.
import type { BindingRow, Credential } from "./api";
import { OUTCOME_WORD } from "./chip-words";
import { runtimeWords } from "./server-words";
import { timeZone } from "./timezone";

export const NONE = "none";

// probeWords rewrites the raw probe detail as one plain sentence. The raw
// text stays the fallback so nothing the server said is lost. A drift
// reason is Straza's own sentence and holds tool names, which may spell a
// word the rewrites look for, so it is answered as it is.
export function probeWords(detail: string | undefined, url?: string): string {
  const d = String(detail || "");
  if (!d) return "";
  if (d.startsWith("inventory drift:")) return d;
  const where = url || "the server";
  if (/connection refused/i.test(d)) return "Straza could not reach " + where + " (connection refused).";
  if (/no such host/i.test(d)) return "Straza could not reach " + where + " (the host name does not resolve).";
  if (/timeout|deadline exceeded/i.test(d)) return "Straza could not reach " + where + " (no answer in time).";
  if (/\b401\b|unauthori[sz]ed/i.test(d)) return where + " answered 401 to the secret you stored.";
  if (/\b403\b|forbidden/i.test(d)) return where + " answered 403 to the secret you stored.";
  if (/requires a credential and none/i.test(d)) return "The server requires a credential and none is stored.";
  if (/docker.*(not found|no such file)|executable file not found/i.test(d)) return "Straza cannot run containers: docker is not on the strazad host.";
  if (/no such file or directory/i.test(d)) return "The command could not be started: " + d.replace(/^.*?fork\/exec\s*/, "");
  return d;
}

// globMatches reads one tool pattern the way the manager and the gateway
// read it: a * anywhere stands for any run of characters, and everything
// else is literal.
function globMatches(pattern: string, name: string): boolean {
  if (pattern === "*") return true;
  if (!pattern.includes("*")) return pattern === name;
  const parts = pattern.split("*").map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return new RegExp("^" + parts.join(".*") + "$").test(name);
}

// matchesTool says whether an access row's patterns admit a tool, in the
// server's grammar. A row with no pattern admits every tool.
export function matchesTool(matchers: string[] | undefined, name: string): boolean {
  const ms = matchers && matchers.length ? matchers : ["*"];
  return ms.some((m) => globMatches(m, name));
}

// reachedBy lists the roles whose binding on this app admits the tool, in
// name order.
export function reachedBy(bindings: BindingRow[] | undefined, app: string, tool: string): string[] {
  return (bindings || []).filter((b) => b.app === app && matchesTool(b.tools, tool)).map((b) => b.role).sort();
}

// matcherWords says which tools a binding admits, in words.
export function matcherWords(matchers: string[] | undefined): string {
  const ms = matchers && matchers.length ? matchers : ["*"];
  if (ms.length === 1 && ms[0] === "*") return "every tool";
  return ms.map((m) => (m === "*" ? "every tool" : m.includes("*") ? "tools matching " + m : m)).join(", ");
}

const pad = (n: number) => String(n).padStart(2, "0");

// absTime renders the full absolute stamp in the active zone with the zone
// named, "2026-07-29 14:22:01 UTC" or "2026-07-29 16:22:01 +02:00": an
// unlabeled wall clock is not evidence.
export function absTime(iso: string | undefined): string {
  if (!iso) return NONE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t);
  const utc = timeZone() === "utc";
  const parts = utc
    ? [d.getUTCFullYear(), d.getUTCMonth() + 1, d.getUTCDate(), d.getUTCHours(), d.getUTCMinutes(), d.getUTCSeconds()]
    : [d.getFullYear(), d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes(), d.getSeconds()];
  let mark = " UTC";
  if (!utc) {
    const off = -d.getTimezoneOffset();
    mark = " " + (off < 0 ? "-" : "+") + pad(Math.floor(Math.abs(off) / 60)) + ":" + pad(Math.abs(off) % 60);
  }
  return parts[0] + "-" + pad(parts[1]) + "-" + pad(parts[2]) + " " +
    pad(parts[3]) + ":" + pad(parts[4]) + ":" + pad(parts[5]) + mark;
}

// dayOf is the calendar day of a stamp in the active zone, the first ten
// characters of absTime, so a day band and the stamps beside it agree.
export function dayOf(iso: string | undefined): string {
  return absTime(iso).slice(0, 10);
}

// relTimeText renders a timestamp the operator way: relative text while
// that is the useful reading (under a day), the absolute stamp once it is
// not. A future instant reads "in 2 h" the same way.
export function relTimeText(iso: string | undefined): string {
  if (!iso) return NONE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = Math.floor((Date.now() - t) / 1000);
  const s = Math.abs(d);
  if (s < 5) return "just now";
  if (s >= 86400) return absTime(iso);
  const n = s < 60 ? s + " s" : s < 3600 ? Math.floor(s / 60) + " m" : Math.floor(s / 3600) + " h";
  return d > 0 ? n + " ago" : "in " + n;
}

// agoWord reads a past stamp the way a table column can hold it: the
// relative reading under a day and "N d ago" past it, since the absolute
// stamp the kit prints past a day is too long for a 110 px column and
// rides on hover instead.
export function agoWord(iso: string | undefined): string {
  if (!iso) return NONE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const ago = Date.now() - t;
  if (ago < 86400000) return relTimeText(iso);
  return Math.round(ago / 86400000) + " d ago";
}

// agoLong reads a past stamp the long way a sentence holds it: "just now"
// under a minute, then whole minutes and hours, rounded down, and from a
// day on the days rounded as agoWord rounds them, so one stamp never reads
// as two counts of days on one page.
export function agoLong(iso: string | undefined): string {
  if (!iso) return NONE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (s < 60) return "just now";
  const [n, unit] = s < 3600 ? [Math.floor(s / 60), "minute"] : s < 86400 ? [Math.floor(s / 3600), "hour"] : [Math.round(s / 86400), "day"];
  return n + " " + unit + (n === 1 ? "" : "s") + " ago";
}

const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

// dayWord is a stamp's day in the zone the console shows: "8 Nov 2026".
export function dayWord(iso: string | undefined): string {
  if (!iso) return NONE;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const d = new Date(t);
  const utc = timeZone() === "utc";
  const [day, month, year] = utc ? [d.getUTCDate(), d.getUTCMonth(), d.getUTCFullYear()] : [d.getDate(), d.getMonth(), d.getFullYear()];
  return day + " " + MONTHS[month] + " " + year;
}

// ---- the MCP servers area: the wizard, the server page and its log ----
// The rows say the short forms, and the long forms sit under the wizard's
// "How this works" fold.

export const LEDE: Partial<Record<string, string>> = {
  credential: "Agents never see the credential.",
};

// The three ways Straza reaches a server, in the order the wizard draws
// them. line is the short form on a radio row of the wizard's Server step,
// desc the card line of the Change connection sheet.
export const RUNTIMES: { key: string; name: string; line: string; desc: string }[] = [
  { key: "remote", name: "A server you run, over HTTP", line: "", desc: "Straza proxies streamable HTTP to it and checks health with an MCP ping." },
  { key: "command", name: "A command Straza starts", line: "Runs on the strazad host and can read Straza's keys. Only for code you trust.", desc: "A process Straza starts on its own host as its own user. It can read Straza's keys and data, so use it only for code you trust." },
  { key: "oci", name: "A container Straza runs", line: "Straza runs its image with docker on the strazad host.", desc: "An image Straza runs with docker on the strazad host, restarted with backoff." },
];
// OCI_OFF and COMMAND_OFF say in one sentence why a way is not available
// on this server. The longer forms add what to do instead.
export const OCI_OFF = "Not available: docker is not on the strazad host.";
export const OCI_MISSING = OCI_OFF + " Use HTTP or a command, or run strazad where docker is installed.";
export const OCI_MISSING_REMOTE = OCI_OFF + " Pick HTTP, or run strazad where docker is installed.";
export const COMMAND_OFF = "Not available: the enterprise profile does not run servers inside Straza.";
export const COMMAND_REFUSED = COMMAND_OFF + " Run it as its own service or pod and pick HTTP.";
// The Server step's own words: the fold of the ways this server cannot
// use, the door to a registry record, and the hints of the address and
// the name.
export const OTHER_WAYS = "Other ways to run a server";
export const IMPORT_RECORD = "Import a registry record";
export const ADDRESS_HINT = "An http or https address.";
export const DESC_HINT = "Optional. Shows under the name on the server's page.";
export const SERVER_NAME_HINT = "Agents see its tools as name__tool. It cannot change later.";

export const CALLER_OFF = "Not available on a command or container server: it is one process and one identity, so each caller's own credential cannot be injected. Use one shared secret, or run the server on the person's own machine.";
export const CALLER_OFF_SHORT = "Not on a command or container server: one process is one identity.";
export const TOKEN_DESC = "Each person pastes a token their vendor issued to them, once, on the Credentials tab of their self-service page. Straza tests it against the server, encrypts it and adds it to that person's calls, so two people are two identities at the server. Fits GitHub, GitLab, Linear, Supabase and Stripe on their hosted endpoints.";
export const ENV_WARNING = "The server process reads it from its environment. Do not grant tools that dump the environment, such as get-env: the secret would come back in the tool's answer.";
export const ENV_WARNING_SHORT = "Do not grant tools that dump the environment, such as get-env: the secret would come back in the tool's answer.";

// CREDENTIAL_CHOICES is the four credential choices in the order the rows
// draw them: the label, and the fact the pill at the row's right says.
export const CREDENTIAL_CHOICES: { kind: string; label: string; fact: string }[] = [
  { kind: "none", label: "None", fact: "no credential" },
  { kind: "static", label: "One shared secret", fact: "one identity" },
  { kind: "token", label: "Each person's own token", fact: "each person" },
  { kind: "oauth", label: "Each person's own sign-in", fact: "each person" },
];

// credentialSay is the one short sentence under the picked choice. provider
// names the identity provider of a sign-in.
export function credentialSay(kind: string, provider: string): string {
  if (kind === "static") return "Straza stores it encrypted and adds it to every call.";
  if (kind === "token") return "Pasted once on the self-service page.";
  if (kind === "oauth") return "Each person signs in once through " + (provider || "the provider") + ".";
  return "Straza's audit still names who called.";
}

// The reasons a credential choice is not available, each with what to do.
export const NOT_AVAILABLE = "not available";
export const CALLER_NOT_HERE = "Not on a container or command server, because one process has one identity. Use one shared secret, or run the server yourself and add it over HTTP.";
export const NO_PROVIDER_YET = "Straza has no identity provider. Add one under oauth.providers in the strazad config, or pick another choice.";
export const PROVIDERS_UNREAD = "The identity providers could not be read. Reload the page, or pick another choice.";

// The words of the fields under a picked credential choice.
export const HOW_THIS_WORKS = "How this works";
export const ENV_WAY = "an environment variable";
export const SECRET_HINT = "Stored encrypted. Never shown again.";
export const AGENTS_SECRET_HINT = "Stored encrypted after you publish. Only agents use it.";
export const STORED_NOW = { label: "Stored now", pills: ["nothing yet", "each person pastes their own"] };
export const BASIC_NOTE = "Type the secret as the base64 of user:password.";
export const CHANGE_ON_PAGE = "To change the choice, open the server's page.";
export const STORE_AGAIN_LINE = "Storing replaces the stored secret, then runs the check.";
// injectPill is the fact pill of where an installed secret travels.
export const injectPill = (as: string, name: string) => (as === "env" ? "variable " : "header ") + (name || "<name>");

// sendsWhat is the live preview of what travels with each call for a
// sent-as choice, the secret or the person's token as a placeholder.
export function sendsWhat(sent: string, cred: string, headerName: string, envName: string): string {
  const who = cred === "token" ? "<the person's token>" : "<secret>";
  if (sent === "env") return (envName.trim() || "<name>") + "=" + who;
  if (sent === "header") return (headerName.trim() || "<name>") + ": " + who;
  return "Authorization: " + (sent === "basic" ? "Basic " : "Bearer ") + who;
}

// The long form of each choice, for the "How this works" fold.
export const CREDENTIAL_MORE: Record<string, string> = {
  none: "The server takes no credential. People and agents reach it alike, and only Straza's audit says who called.",
  static: "Straza keeps it encrypted and adds it to every call and to its own health checks. The server sees one identity. Straza's audit keeps the person or agent. Rotation is setting it again. A different secret for one role can be set on the server's page later.",
  token: TOKEN_DESC,
};
export function oauthMore(provider: string, redirectURI: string): string {
  return "Each person signs in once through an identity provider you registered a client at, here " + (provider || "the provider") + ", and every call carries their own token, so two people are two identities at the server. This fits GitHub, GitLab and Google today. Servers that run their own sign-in with dynamic registration (Atlassian, Notion, Linear, Sentry, Stripe) are not supported yet. Use one shared secret for them. Register " + (redirectURI || "the redirect URI") + " at the provider.";
}

// What an agent with no row of its own runs on, per caller kind: the
// value it writes, the row's name, the one-line hint and the long hint.
export const AGENT_ROWS: Record<string, { value: string; name: string; line: string; more: string }[]> = {
  token: [
    { value: "own", name: "Refused", line: "Refused until a sponsor pastes a token for the agent.", more: "The default. An agent's call is refused until its sponsor pastes a token for it on the Credentials tab of the self-service page, or an administrator does with strazactl connect. Writes agents: own." },
    { value: "sponsor", name: "Run on their sponsor's token", line: "Once the sponsor allows it on the Credentials tab of their self-service page.", more: "Once the sponsor switched on My agents may use this on the Credentials tab of their self-service page. Each call's record names the agent as the caller and the sponsor as the credential's owner. Writes agents: sponsor." },
    { value: "shared", name: "Use this server's shared secret", line: "You store one secret below. Only agents fall back to it.", more: "The same secret field as One shared secret appears below, and the wizard stores it after you publish. People never fall back, only agents. Writes agents: shared." },
  ],
  oauth: [
    { value: "own", name: "Refused", line: "An agent cannot sign in through a browser, so its calls are refused.", more: "A sign-in is stored only for the person who is signed in to Straza in the browser that finishes it, and an agent has no such browser. Its calls are refused until this value changes. Writes agents: own." },
    { value: "sponsor", name: "Run on their sponsor's sign-in", line: "Once the sponsor allows it on the Credentials tab of their self-service page.", more: "The default. Once the sponsor switched on My agents may use this on the Credentials tab of their self-service page. Each call's record names the agent as the caller and the sponsor as the credential's owner. Writes agents: sponsor." },
    { value: "shared", name: "Use a shared account", line: "You store one secret on the server's page after you publish. Only agents fall back to it.", more: "An administrator stores one shared secret for the server, as for a static server, and agents with no sign-in of their own use it. People never fall back. Writes agents: shared." },
  ],
};

// sentShort is the one-line hint under the sent-as picker.
export function sentShort(sent: string, cred: string, headerName: string): string {
  const who = cred === "token" ? "the person's token" : "secret";
  if (sent === "bearer") return "Authorization: Bearer <" + who + ">";
  if (sent === "basic") return "Authorization: Basic <" + who + ">, the base64 of user:password";
  if (sent === "env") return "The process reads it from this variable.";
  return (headerName.trim() || "<name>") + ": <" + who + ">";
}

// probeKind sorts a probe detail into the three outcomes the wizard tells
// apart: refused (the upstream answered the credential), unreachable (no
// upstream answered at all), or other.
export function probeKind(detail: string | undefined): "refused" | "unreachable" | "other" {
  const d = String(detail || "");
  if (/\b401\b|\b403\b|unauthori[sz]ed|forbidden/i.test(d)) return "refused";
  if (/connection refused|no such host|timeout|deadline exceeded|dial tcp|no such file|not found in \$PATH|executable file not found/i.test(d)) return "unreachable";
  return "other";
}

export type HealthRead = { kind: "running" | "empty" | "refused" | "unreachable" | "other"; tone: "ok" | "warn" | "danger"; lead: string; text: string };

// healthRead sorts a probed app into the sentence the Check step opens
// with: running with its tools, refused (the upstream answered the secret),
// unreachable, or another degraded reason.
export function healthRead(app: { status?: string; tools?: string[]; detail?: string } | null, url?: string): HealthRead {
  const tools = (app && app.tools) || [];
  const status = (app && app.status) || "unknown";
  if (status === "running" && tools.length) return { kind: "running", tone: "ok", lead: "Running.", text: tools.length + (tools.length === 1 ? " tool: " : " tools: ") + tools.join(", ") + "." };
  if (status === "running") return { kind: "empty", tone: "warn", lead: "Running, no tools.", text: "Recheck once it lists tools." };
  const kind = probeKind(app ? app.detail : "");
  const words = probeWords(app ? app.detail : "", url) || ("The server is " + status + ".");
  if (kind === "refused") return { kind, tone: "danger", lead: "Refused.", text: words + " Check the value with whoever issued it, set it again, then Recheck." };
  if (kind === "unreachable") return { kind, tone: "danger", lead: "Unreachable.", text: words + " Fix the address or start the server, then Recheck." };
  return { kind: "other", tone: "warn", lead: status.charAt(0).toUpperCase() + status.slice(1) + ".", text: words + " Recheck." };
}

// holders words a holder count, for the palette's role rows and the Roles
// area alike.
export const holders = (n: number) => (n === 1 ? "1 holder" : n + " holders");

// POLICY_HELP is the sentence behind the Policy header of every tools
// table, on the server page and in the Roles area alike.
export const POLICY_HELP = "What live policies do when this role calls the tool. A tool no rule names is allowed.";

// runWord is the one plain word per reaching role in a tools table's
// Policy column, with the set named when a policy gates the tool: allowed,
// needs approval, denied.
const RUN_WORD: Record<string, string> = { visible: OUTCOME_WORD.allowed, approve_gated: OUTCOME_WORD.approval, hidden_policy: OUTCOME_WORD.denied, not_running: "not running", matcher_miss: OUTCOME_WORD.none, no_binding: OUTCOME_WORD.none };
export function runWord(e: { status?: string; setName?: string } | undefined): string {
  if (!e) return "";
  const named = e.setName && (e.status === "approve_gated" || e.status === "hidden_policy") ? " (" + e.setName + ")" : "";
  return (RUN_WORD[e.status || ""] || "unknown") + named;
}

// ---- the log panel ----

const MARKER = "status → ";

// classify names a line's severity from the words runtimes actually print.
export function classify(line: string): "err" | "wrn" | "dbg" | "inf" {
  if (/^(error|fatal|panic)\b/i.test(line) || /\b(ERR|ERROR|FATAL|PANIC)\b/.test(line)) return "err";
  if (/^warn(ing)?\b/i.test(line) || /\b(WRN|WARN|WARNING)\b/.test(line)) return "wrn";
  if (/^(debug|trace)\b/i.test(line) || /\b(DBG|DEBUG|TRACE)\b/.test(line)) return "dbg";
  return "inf";
}

export type LogRow =
  | { marker: true; check?: boolean; t: string; text: string }
  | { marker?: false; t: string; last: string; text: string; n: number; k: "err" | "wrn" | "dbg" | "inf" };

// foldRuns turns entries into rows: a status marker the manager wrote
// becomes a marker row, and a run of identical consecutive lines becomes one
// row carrying the first time and the count.
export function foldRuns(entries: { t: string; line: string }[]): LogRow[] {
  const rows: LogRow[] = [];
  for (const e of entries || []) {
    if (e.line.startsWith(MARKER)) { rows.push({ marker: true, t: e.t, text: e.line.slice(MARKER.length).trim() }); continue; }
    const last = rows[rows.length - 1];
    if (last && !last.marker && last.text === e.line) { last.n += 1; last.last = e.t; continue; }
    rows.push({ t: e.t, last: e.t, text: e.line, n: 1, k: classify(e.line) });
  }
  return rows;
}

// forWord is "12 min" or "3 h" or "2 d" between an ISO stamp and now.
export function forWord(iso: string): string {
  const s = Math.max(0, Math.floor((Date.now() - Date.parse(iso)) / 1000));
  return s < 60 ? s + " s" : s < 3600 ? Math.floor(s / 60) + " min" : s < 86400 ? Math.floor(s / 3600) + " h" : Math.floor(s / 86400) + " d";
}

// ---- the events list ----

export type EventRow = { seq: string; t: string; tone: "accent" | "warn" | "danger"; src: string; who: string; text: string };

const list = (xs: unknown) => (Array.isArray(xs) ? xs.map((x) => String(x)).join(", ") : "");

// aboutServer says whether a gateway record is about the server called
// name, and gives the tool name without the server's prefix. A call to a
// name no role reaches carries no app, only the namespaced name the caller
// sent, so there the prefix is the one place the server appears.
export function aboutServer(d: Record<string, unknown>, name: string): { mine: boolean; tool: string } {
  const raw = String(d.toolName || d.tool || "");
  const prefix = name + "__";
  const tool = raw.startsWith(prefix) ? raw.slice(prefix.length) : raw;
  if (d.app === name) return { mine: true, tool };
  return { mine: !d.app && raw.startsWith(prefix), tool };
}

// removedAt is the time of the newest removal of a server called name in
// the audit rows, in epoch milliseconds, or 0. A server installed under a
// removed server's name starts after that moment, so every record at or
// before it belongs to the server that was removed.
export function removedAt(rows: { ce: string }[], name: string): number {
  let at = 0;
  for (const r of rows) {
    let ce: { type?: string; time?: string; data?: Record<string, unknown> } | null = null;
    try { ce = JSON.parse(r.ce); } catch { continue; }
    if (ce && ce.type === "straza.audit.admin" && ce.data && ce.data.action === "apps.remove" && ce.data.app === name) {
      at = Math.max(at, Date.parse(ce.time || "") || 0);
    }
  }
  return at;
}

// CHANGED maps a changed manifest field path to the words the Activity tab
// reads it as, a path under a key taking that key's words. Each key sits
// before its parent, so a block that appeared whole reads as its nearest
// meaningful word. An install record names fields, never their values.
const CHANGED: [string, string][] = [
  ["straza.runtime.kind", "transport"],
  ["straza.runtime.remote.url", "address"],
  ["straza.runtime.remote.auth", "upstream auth"],
  ["straza.runtime.remote", "address"],
  ["straza.runtime.command.exec", "executable"],
  ["straza.runtime.command.args", "arguments"],
  ["straza.runtime.command.workdir", "working directory"],
  ["straza.runtime.command.env", "environment"],
  ["straza.runtime.command", "command"],
  ["straza.runtime.oci.image", "image"],
  ["straza.runtime.oci.sandbox", "sandbox"],
  ["straza.runtime.oci.env", "environment"],
  ["straza.runtime.oci", "image"],
  ["straza.runtime", "transport"],
  ["straza.credential.kind", "credential type"],
  ["straza.credential.inject", "the way the credential is sent"],
  ["straza.credential.agents", "what agents with nothing of their own do"],
  ["straza.credential.oauth.provider", "identity provider"],
  ["straza.credential.oauth.scopes", "scopes"],
  ["straza.credential.oauth", "identity provider"],
  ["straza.credential", "credential"],
  ["straza.exposure", "tools exposed"],
  ["straza.limits.rps", "rate limit"],
  ["straza.limits.timeoutSeconds", "per-call timeout"],
  ["straza.limits.cpu", "resource limits"],
  ["straza.limits.mem", "resource limits"],
  ["straza.limits", "limits"],
  ["metadata.description", "description"],
  ["server", "registry record"],
];

// installWords says what an install record did: the first install with its
// runtime, or an update with the changed fields in words, deduplicated. A
// list of plain nouns shares one "the". Once a phrase that brings its own
// article joins, every item carries its own. An update with no changed
// list at all is one strazad could not compare, which is not the same as
// one that changed nothing.
function installWords(d: Record<string, unknown>): string {
  if (d.update !== true) return "published it (" + runtimeWords(String(d.runtime || "")) + ").";
  if (!Array.isArray(d.changed)) return "published it again. Changed fields not recorded.";
  const paths = d.changed.map(String);
  if (paths.length === 0) return "published the same manifest again.";
  const words = [...new Set(paths.map((p) => (CHANGED.find(([k]) => p === k || p.startsWith(k + ".")) || ["", "settings"])[1]))];
  const own = (w: string) => w.startsWith("the ") || w.startsWith("what ");
  const phrases = words.some(own);
  const items = phrases ? words.map((w) => (own(w) ? w : "the " + w)) : words;
  const joined = items.length < 2 ? items.join("") : items.slice(0, -1).join(", ") + " and " + items[items.length - 1];
  return "changed " + (phrases ? "" : "the ") + joined + ".";
}

// eventRow turns one audit record about this server into a sentence with a
// tone and a source word, or null when the record is not about it. who is
// the actor and text the rest of the sentence, so a screen can set the
// actor in bold. The record's data.user is an id; the list row carries the
// username the server resolved, so that is the name shown. The audit chain
// carries admin actions and gateway decisions; tool list drift rides the
// change feed and is added by driftRow.
export function eventRow(row: { seq: number | string; ce: string; username?: string }, name: string): EventRow | null {
  let ce: { type?: string; time?: string; data?: Record<string, unknown> } | null = null;
  try { ce = JSON.parse(row.ce); } catch { return null; }
  const d = (ce && ce.data) || {};
  const type = (ce && ce.type) || "";
  const base = { seq: String(row.seq), t: (ce && ce.time) || "" };
  if (type === "straza.audit.admin") {
    // A role record names its owning server in server, where a server
    // record names the server in app.
    if (d.app !== name && d.server !== name) return null;
    const who = row.username || (d.actor as string) || "an admin";
    const a = String(d.action || "");
    const role = String(d.role || "");
    const s = a === "apps.install" ? installWords(d)
      : a === "apps.remove" ? "removed it."
        : a === "apps.recheck" ? "ran Recheck: " + d.status + (d.detail ? ", " + probeWords(String(d.detail)) : "") + "."
          : a === "apps.enable" ? "enabled it."
            : a === "apps.disable" ? "paused it. Its tools left every session's list."
              : a === "apps.binding.create" ? "gave " + role + " access to " + (list(d.tools) || "its tools") + "."
                : a === "apps.binding.delete" ? "removed " + role + "'s access."
                  : a === "apps.secret.set" ? "set " + (role ? "the secret for " + role : "the shared secret") + "."
                    : a === "apps.secret.remove" ? "removed " + (role ? "the secret for " + role : "the shared secret") + "."
                      : a === "roles.create" ? "created the role " + role + "."
                        : a === "roles.delete" ? "deleted the role " + role + (d.reason ? ", " + d.reason : "") + "."
                          : null;
    return s ? { ...base, tone: "accent", src: "admin action", who, text: s } : null;
  }
  if (type === "straza.audit.mcp" && d.effect && d.effect !== "allow") {
    const about = aboutServer(d, name);
    if (!about.mine) return null;
    const held = d.effect === "approve" || d.effect === "confirm";
    const tool = about.tool;
    return {
      ...base,
      tone: held ? "warn" : "danger",
      src: held ? "needed approval" : "denied call",
      who: row.username || String(d.user || "") || "a session",
      text: (d.harness ? "'s " + d.harness + " session" : "") + " called " + tool + ". " + (held ? "Needs approval" : "Denied") + (d.reason ? ": " + d.reason : "."),
    };
  }
  return null;
}

// driftRow turns one change-feed row into the drift sentence, or null when
// the row is not a tool list change of this server. The feed names the
// server by id and carries no tool names.
export function driftRow(c: { type: string; id: string; cursor: string; at: string }, appId: string): EventRow | null {
  if (c.type !== "tool" || c.id !== appId) return null;
  return { seq: "c" + c.cursor, t: c.at, tone: "warn", src: "tool list changed", who: "Health check", text: " found the tool list changed. A new tool reaches no role until an access row names it." };
}

// ---- the credential tab ----

// kindLine words the credential kind for the Type row of the Credential
// card.
export function kindLine(cred: Credential | null | undefined): string {
  const kind = (cred && cred.kind) || "none";
  if (kind === "static") return "One shared secret for this server. The server sees one identity. Straza's audit keeps the person or agent.";
  if (kind === "token") return "Each person's own token. People paste theirs on the Credentials tab of their self-service page.";
  if (kind === "oauth") return "Each person's own sign-in through " + ((cred && cred.oauth && cred.oauth.provider) || "the provider") + ".";
  return "None. No credential is sent.";
}

// agentsLine says what an agent with no row of its own runs on, for a
// caller kind, or "" for the other kinds.
export function agentsLine(cred: Credential | null | undefined): string {
  const kind = (cred && cred.kind) || "none";
  if (kind !== "token" && kind !== "oauth") return "";
  const what = kind === "token" ? "token" : "sign-in";
  const agents = (cred && cred.agents) || "own";
  if (agents === "sponsor") return "Run on their sponsor's " + what + " once the sponsor allowed it.";
  if (agents === "shared") return "Use this server's shared secret.";
  return kind === "token" ? "Refused until a sponsor pastes a token for them." : "Refused, because an agent cannot sign in through a browser.";
}

// noToolWords is the tools table's empty line: the status and, when the
// probe said why, the reason with its own period dropped, then the next step.
export function noToolWords(status: string, detail?: string, url?: string): string {
  const why = detail ? ", " + probeWords(detail, url).replace(/\.$/, "") : "";
  return "No tool is known yet, because the server is " + status + why + ". Recheck once it answers.";
}

// probeSay is the result of a recheck in one sentence with the toast tone
// it lands in: ok when running, warn when degraded, failed otherwise, with
// the probe's reason in words when it is not running.
export function probeSay(view: { name: string; status: string; detail?: string; url?: string }): { tone: "ok" | "warn" | "failed"; text: string } {
  const text = view.name + " is " + view.status + "." + (view.status === "running" ? "" : " " + probeWords(view.detail, view.url));
  return { tone: view.status === "running" ? "ok" : view.status === "degraded" ? "warn" : "failed", text };
}
