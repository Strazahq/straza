// The words of the Overview and the Configuration tab: one row per
// configuration key, read by both pages so a setting has one name, the
// posture that Overview badges, the attention sentences, the tile help and
// the setup list of an empty deployment. Every surface sentence is short;
// the sentence that explains a thing sits behind its help icon.
import { configFragment, type ConfigScalar } from "./config-export";
import type { ApprovalRow, ConfigAnswer, OverviewAnswer, SinkRow } from "./api";
import { OUTCOME_LABEL, OUTCOME_WORD } from "./chip-words";
import { AREA_WORD, needsArea } from "./say";

// ---- durations and counts ----

// secondsWords reads a seconds count the way a person says it: 0, 45 s,
// 5 min, 2 h, 3 d.
export function secondsWords(s: number | undefined): string {
  const n = s || 0;
  if (n === 0) return "0";
  if (n < 60) return n + " s";
  if (n < 3600) return Math.round(n / 60) + " min";
  if (n < 86400) return Math.round(n / 3600) + " h";
  return Math.round(n / 86400) + " d";
}

// retentionWords reads a retention in hours as whole days where it is
// one, else as hours.
export function retentionWords(h: number | undefined): string {
  const n = h || 0;
  if (n === 0) return "0";
  if (n % 24 === 0) return n / 24 + (n === 24 ? " day" : " days");
  return n + (n === 1 ? " hour" : " hours");
}

export const nfmt = (n: number | undefined) => (n || 0).toLocaleString("en-US");
export const plural = (n: number, one: string, many: string) => n + " " + (n === 1 ? one : many);

// ---- the configuration rows ----

export type Section = "Deployment" | "Sign-in" | "Entra" | "Governance" | "Approvals and publishing" | "MCP servers" | "Recording";
export const SECTIONS: Section[] = ["Deployment", "Sign-in", "Entra", "Governance", "Approvals and publishing", "MCP servers", "Recording"];

// VALUE_WORD is what a person reads for a raw value: the booleans of the
// two yes or no rows and the on or off row, a zero grace, the recording
// mode and where bodies are stored. Every other value reads as sent.
export const VALUE_WORD = { yes: "yes", no: "no", on: "on", off: "off", none: "none" };
export const MODE_WORD: Record<string, string> = { verbatim: "word for word", redact: "masked", mixed: "mixed" };
export const BODIES_WORD: Record<string, string> = { inline: "in the database", s3: "in an object store" };

// RELAXED_SENTENCE is what a relaxed value means, the cost Overview and the
// relaxed card print beside it.
export const RELAXED_SENTENCE = {
  tls: "Session tokens and approvals cross the network in the clear.",
  floorNone: "A client that proves nothing about its install still gets a session.",
  floorAdvisory: "Reported measurements are required, but a managed installation is not.",
  local: "A local tool no policy rule names is allowed.",
  bp: "Under load, calls proceed unrecorded and a counter is all that remains.",
  ownDecisions: "An agent that runs on a person's machine can approve that person's held calls, its own included.",
};

// Posture is what a row's value says about how strict the server is:
// relaxed loosens it and is badged, strict is the tight value, note is a
// choice worth knowing without a badge, plain carries no posture.
export type Posture = "plain" | "strict" | "relaxed" | "note";

// ConfigRow is one line of the Configuration tab. key and env say where
// the value is set (env is "" when only the file or the chart set it);
// help ends with the allowed values where a closed set exists; cost is the
// clause Overview prints beside a relaxed or noted value; strictWord is the
// clause that folds into the strict line when the value is the tight one.
export type ConfigRow = {
  id: string;
  section: Section;
  name: string;
  short?: string;
  key: string;
  env: string;
  value: string;
  raw?: ConfigScalar;
  postureValue?: string;
  posture: Posture;
  help: string;
  cost?: string;
  strictWord?: string;
};

// isPlainHTTP says whether the public URL is served without TLS anywhere in
// front: the case where TLS off is a relaxed posture, not an ingress choice.
export const isPlainHTTP = (c: ConfigAnswer) => !c.tls && (c.public_url || "").toLowerCase().startsWith("http://");

// configRows turns the redacted answer into the rows, in section order.
export function configRows(c: ConfigAnswer): ConfigRow[] {
  const g = c.governance || {};
  const plainHTTP = isPlainHTTP(c);
  const floor = g.min_attestation || "none";
  const mode = (c.capture && c.capture.mode) || "none";
  const bodies = (c.capture && c.capture.body_store) || "inline";
  const rows: ConfigRow[] = [
    { id: "profile", section: "Deployment", name: "Profile", key: "profile", env: "STRAZA_PROFILE", value: c.profile || "unknown", posture: "plain",
      help: "standalone bundles the identity issuer and the store into this one binary. enterprise expects your own identity provider, Postgres and NATS. Values: standalone, enterprise." },
    { id: "public_url", section: "Deployment", name: "Public URL", key: "server.publicUrl", env: "STRAZA_PUBLIC_URL", value: c.public_url || "not set", posture: "plain",
      help: "The address clients and phones use to reach Straza. If it is wrong, enrolled agents cannot connect." },
    { id: "tls", section: "Deployment", name: "TLS terminated by strazad", short: "Transport", key: "server.tls.certFile, server.tls.keyFile", env: "STRAZA_TLS_CERT_FILE, STRAZA_TLS_KEY_FILE", value: c.tls ? "on" : "off", postureValue: c.tls ? "TLS" : plainHTTP ? "plain http" : "TLS at your ingress",
      posture: c.tls ? "strict" : plainHTTP ? "relaxed" : "note",
      help: "on when Straza itself serves HTTPS. off is right behind a TLS ingress. off counts as relaxed only when the public URL is http.",
      cost: plainHTTP ? RELAXED_SENTENCE.tls : "The hop from your ingress to Straza is plain HTTP.",
      strictWord: "Straza terminates TLS" },
    { id: "store", section: "Deployment", name: "Store", key: "store.driver", env: "STRAZA_STORE_DRIVER", value: c.store_driver || "unknown", posture: "plain",
      help: "sqlite is single node. postgres is the replicated shape, where every pod shares one database. Values: sqlite, postgres." },
    { id: "events", section: "Deployment", name: "Event broker", key: "events.embedded", env: "", value: c.events && c.events.embedded === false ? "external" : "embedded", posture: c.events && c.events.embedded === false ? "plain" : "note",
      help: "embedded runs the broker inside this pod. That is right for one replica. A second replica needs an external NATS, set in STRAZA_EVENTS_URL.",
      cost: "A second replica needs an external NATS first." },
    { id: "issuer", section: "Sign-in", name: "Identity provider", key: "oidc.issuer", env: "STRAZA_OIDC_ISSUER", value: (c.oidc && c.oidc.external_issuer) || "built into Straza", posture: "plain",
      help: "The provider that signs people in. Empty means the issuer built into Straza." },
    { id: "jit", section: "Sign-in", name: "Sign-in creates users", key: "oidc.jitProvision", env: "STRAZA_OIDC_JIT", value: c.oidc && c.oidc.jit_provision ? VALUE_WORD.yes : VALUE_WORD.no, posture: c.oidc && c.oidc.jit_provision ? "note" : "strict",
      help: "true creates a Straza user on the first successful sign-in. false means only provisioned identities can check in. Values: true, false.",
      cost: "anyone your identity provider signs in becomes a Straza user", strictWord: "sign-in creates no users" },
    { id: "floor", section: "Governance", name: "Client attestation floor", key: "governance.minAttestation", env: "STRAZA_MIN_ATTESTATION", value: floor, posture: floor === "managed" ? "strict" : "relaxed",
      help: "The lowest proof of its install a client must give to get a session. none lets every client in. advisory asks for hashes and lets a mismatch through. managed refuses a client whose hashes are not in the registry.",
      cost: floor === "managed" ? undefined : floor === "none" ? RELAXED_SENTENCE.floorNone : RELAXED_SENTENCE.floorAdvisory,
      strictWord: "the attestation floor is managed" },
    { id: "grace", section: "Governance", name: "Offline grace", key: "governance.offlineGraceTTL", env: "", value: g.offline_grace_ttl_seconds ? secondsWords(g.offline_grace_ttl_seconds) : VALUE_WORD.none, posture: g.offline_grace_ttl_seconds ? "note" : "strict",
      help: "How long a client keeps deciding from its cached snapshot while Straza is unreachable. 0 denies the moment Straza goes away.",
      cost: "a client cut off from Straza keeps deciding for " + secondsWords(g.offline_grace_ttl_seconds), strictWord: "a client cut off from Straza denies at once" },
    { id: "local", section: "Governance", name: "Local tool default", key: "governance.localToolDefault", env: "", value: g.local_tool_default || "deny", posture: g.local_tool_default === "allow" ? "relaxed" : "strict",
      help: "The verdict for a local tool no rule matched. allow is permission by omission. Values: allow, deny.",
      cost: g.local_tool_default === "allow" ? RELAXED_SENTENCE.local : undefined, strictWord: "local tools deny by default" },
    { id: "bp", section: "Governance", name: "Audit backpressure", key: "governance.auditBackpressure", env: "", value: g.audit_backpressure || "block", posture: g.audit_backpressure && g.audit_backpressure !== "block" ? "relaxed" : "strict",
      help: "What happens when audit ingest saturates. block holds the caller so nothing goes unrecorded. drop-with-counter keeps traffic moving and loses evidence. Values: block, drop-with-counter.",
      cost: g.audit_backpressure && g.audit_backpressure !== "block" ? RELAXED_SENTENCE.bp : undefined, strictWord: "audit blocks under load" },
    { id: "hold", section: "Approvals and publishing", name: "Gateway hold", key: "approval.gatewayHoldSeconds", env: "STRAZA_APPROVAL_GATEWAY_HOLD_SECONDS", value: secondsWords(c.approval && c.approval.gateway_hold_seconds), posture: "plain",
      help: "How long a call that needs approval keeps its socket open before the caller gets the structured pending answer. The rule's decision window is the ceiling. Lower it for clients that time out sooner." },
    { id: "ownDecisions", section: "Approvals and publishing", name: "Own requests without a signed device", key: "approval.unsignedOwnDecisions", env: "", value: c.approval && c.approval.unsigned_own_decisions ? VALUE_WORD.yes : VALUE_WORD.no, posture: c.approval && c.approval.unsigned_own_decisions ? "relaxed" : "strict",
      help: "false means a person confirms their own agent's call only on an enrolled phone or browser, which sign the decision with a key the agent cannot reach. true also accepts the console, strazactl and Slack. Values: true, false.",
      cost: c.approval && c.approval.unsigned_own_decisions ? RELAXED_SENTENCE.ownDecisions : undefined, strictWord: "own requests need a signed device" },
    { id: "secondPerson", section: "Approvals and publishing", name: "Second person for publishing", key: "admin.secondPerson", env: "", value: c.admin && c.admin.second_person ? VALUE_WORD.on : VALUE_WORD.off, posture: "plain",
      help: "When it is on, a change that widens access cannot be published by the person who wrote it, by the sponsor of the agent that wrote it, or by whoever minted the API token that wrote it. Another administrator publishes it." },
    { id: "watch", section: "MCP servers", name: "Apps directory watcher", key: "apps.dir", env: "STRAZA_APPS_DIR", value: c.apps && c.apps.gitops_dir_enabled ? "on" : "off", posture: c.apps && c.apps.gitops_dir_enabled ? "note" : "plain",
      help: "This pod watches an apps directory: a file dropped or changed there proposes a draft that adds or changes an MCP server, and a file removed there proposes the server's removal. Nothing from a file is live until a person publishes its draft.",
      cost: "Whoever writes the apps directory proposes server changes." },
    { id: "upstream", section: "MCP servers", name: "Upstream call timeout", key: "apps.upstreamTimeout", env: "", value: secondsWords(c.apps && c.apps.upstream_timeout_seconds), posture: "plain",
      help: "Ceiling on one gateway call to an MCP server before it fails closed." },
    { id: "sets", section: "Recording", name: "Policies that record", key: "", env: "", value: String((c.capture && c.capture.policy_sets) || 0), posture: "plain",
      help: "Live policies that record sessions, set in each policy's capture block. Zero means nothing is recorded, whatever the retention says." },
    { id: "mode", section: "Recording", name: "Mode", key: "", env: "", value: MODE_WORD[mode] || mode, posture: "plain",
      help: "verbatim keeps the turns word for word. redact masks secrets first. mixed means the recording policies disagree. none means no policy records." },
    { id: "ret", section: "Recording", name: "Retention", key: "governance.captureRetention", env: "STRAZA_CAPTURE_RETENTION", value: retentionWords(c.capture && c.capture.retention_hours), posture: "plain",
      help: "How long recorded turns are kept before the sweep deletes them." },
    { id: "bodies", section: "Recording", name: "Bodies stored", key: "capture.bodyStore.type", env: "", value: BODIES_WORD[bodies] || bodies, posture: "plain",
      help: "inline keeps turn bodies in the database. s3 keeps them in a bucket keyed by content hash, with the database holding the pointer. Values: inline, s3." },
    ...entraRows(c.entra),
  ];
  const raw: Record<string, ConfigScalar | undefined> = {
    profile: c.profile, public_url: c.public_url, store: c.store_driver,
    events: c.events?.embedded, issuer: c.oidc?.external_issuer, jit: c.oidc?.jit_provision,
    floor: g.min_attestation, grace: g.offline_grace_ttl_seconds === undefined ? undefined : g.offline_grace_ttl_seconds + "s",
    local: g.local_tool_default, bp: g.audit_backpressure, hold: c.approval?.gateway_hold_seconds,
    ownDecisions: c.approval?.unsigned_own_decisions, secondPerson: c.admin?.second_person,
    upstream: c.apps?.upstream_timeout_seconds === undefined ? undefined : c.apps.upstream_timeout_seconds + "s",
    ret: c.capture?.retention_hours === undefined ? undefined : c.capture.retention_hours + "h",
    bodies: c.capture?.body_store,
    // TLS off means neither file key is set: the config refuses one without the other.
    tls: c.tls === false ? "" : undefined,
  };
  return rows.map((row) => {
    if (row.section === "Entra") return row;
    const present = row.id === "tls" ? c.tls !== undefined : row.id === "watch" ? c.apps?.gitops_dir_enabled !== undefined : row.id === "sets" ? c.capture?.policy_sets !== undefined : row.id === "mode" ? c.capture?.mode !== undefined || c.capture?.policy_sets === 0 : raw[row.id] !== undefined;
    return present ? { ...row, raw: raw[row.id] } : { ...row, raw: undefined, value: VALUE_UNAVAILABLE, help: row.help + " " + VALUE_UNREPORTED, posture: "plain", cost: undefined, strictWord: undefined };
  });
}

// READ_WORD is a kind identity.entra.read lists, in words. A kind this
// console does not know shows as the config holds it.
const READ_WORD: Record<string, string> = { people: "people", agentUsers: "agent users", agentIdentities: "agent identities" };

// everyWords reads an interval in seconds as how often: every minute,
// every 2 hours, every 30 seconds.
export function everyWords(s: number): string {
  const [n, unit] = s % 3600 === 0 ? [s / 3600, "hour"] : s % 60 === 0 ? [s / 60, "minute"] : [s, "second"];
  return n === 1 ? "every " + unit : "every " + n + " " + unit + "s";
}

// durationRaw is an interval the way the config file writes it: 1m, 1h.
const durationRaw = (s: number) => (s % 3600 === 0 ? s / 3600 + "h" : s % 60 === 0 ? s / 60 + "m" : s + "s");

// guardShare reads the removal guard's share: more than half.
const guardShare = (percent: number) => (percent === 50 ? "more than half" : "more than " + percent + "%");

// entraRows are the rows of the identity.entra section, none while it is
// off. A list or a pair of numbers has no one line to copy, so those rows
// copy their keys alone.
function entraRows(e: ConfigAnswer["entra"]): ConfigRow[] {
  if (!e) return [];
  const read = e.read || [];
  const prefix = e.group_name_prefix || "";
  const delta = e.delta_interval_seconds || 0;
  const full = e.reconcile_interval_seconds || 0;
  const share = guardShare(e.removal_guard_percent || 0);
  const least = e.removal_guard_min_members || 0;
  const row = (r: Omit<ConfigRow, "section" | "env" | "posture">): ConfigRow => ({ ...r, section: "Entra", env: "", posture: "plain" });
  return [
    row({ id: "entraTenant", name: "Tenant", key: "identity.entra.tenant", value: e.tenant || VALUE_WORD.none, raw: e.tenant, help: "The Entra tenant whose groups Straza writes and reads." }),
    row({ id: "entraClient", name: "Service app", key: "identity.entra.clientId", value: e.client_id || VALUE_WORD.none, raw: e.client_id,
      help: "The app Straza signs in as to Microsoft Graph. It holds the certificate of the client assertion key and the Graph permissions." }),
    row({ id: "entraRead", name: "Kinds read", key: "identity.entra.read", value: read.length ? read.map((k) => READ_WORD[k] || k).join(", ") : VALUE_WORD.none,
      help: "The kinds Entra owns. A member of a kind not listed is not applied, and the role's page lists it. Values: people, agentUsers, agentIdentities." }),
    row({ id: "entraGroups", name: "Role groups", key: "identity.entra.roleGroups", value: e.role_groups ? VALUE_WORD.on : VALUE_WORD.off, raw: e.role_groups,
      help: "On, Straza makes one security group in Entra for each role and gives the role to the group's members. Off, Straza writes nothing into Entra. Values: true, false." }),
    row({ id: "entraPrefix", name: "Group name prefix", key: "identity.entra.groupNamePrefix", value: JSON.stringify(prefix), raw: prefix,
      help: "Starts the name of every group in Entra, so the group of finance-readers reads " + prefix + "finance-readers." }),
    row({ id: "entraDelta", name: "Read changes", key: "identity.entra.deltaInterval", value: everyWords(delta), raw: durationRaw(delta),
      help: "How often Straza reads what changed in the role groups. Entra itself takes 6 to 16 seconds to show a change." }),
    row({ id: "entraFull", name: "Read all members", key: "identity.entra.reconcileInterval", value: everyWords(full), raw: durationRaw(full),
      help: "How often Straza reads every member of every role group and repairs what drifted, such as a renamed group." }),
    row({ id: "entraGuard", name: "Removal guard", key: "identity.entra.removalGuard.percent, identity.entra.removalGuard.minMembers", value: share + ", at least " + least,
      help: "A read that would remove " + share + " of a role's holders from Entra, and at least " + least + ", waits for an administrator on the role's page." }),
  ];
}

// VALUE_UNAVAILABLE stands in a row whose value strazad's answer left out,
// and VALUE_UNREPORTED joins the row's help to say why and where to look.
export const VALUE_UNAVAILABLE = "Unavailable";
export const VALUE_UNREPORTED = "Straza does not report this value. Read it in the config file or the chart values.";

// fileLine is what the row's copy button yields: the config file line for
// its first key and its value.
export const fileLine = (r: ConfigRow) => r.key && !r.key.includes(",") && r.raw !== undefined ? configFragment([{ key: r.key, value: r.raw }]) : "";

export const CONFIG_WHY = "A browser edit would drift from your deployment's file and revert on upgrade. Connection values such as database addresses and file paths are never sent to this page.";
export const CONSOLE_ACCESS_TITLE = "Console access";
export const CONSOLE_ACCESS_LINE = "Set in admin.roleAreas.";
export const CONFIG_BANNER = "Set in the config file, the chart or the environment.";
export const DRAFT_TITLE = "Prepare a change";
export const DRAFT_LEDE = "Pick a setting and its new value to get the lines for your deployment.";
export const DRAFT_DEPLOY = "Takes effect when Straza restarts.";
export const NO_ENV_LINE = (key: string) => "# " + key + " has no environment variable: set it in the file or the chart";

// ---- posture, the panel on Overview ----

export type PostureRead = { relaxed: ConfigRow[]; notes: ConfigRow[]; strictWords: string[] };

// postureOf splits the rows into what Overview shows: the relaxed rows with
// their cost, the notes worth knowing, and the strict clauses that fold
// into one line.
export function postureOf(rows: ConfigRow[]): PostureRead {
  return {
    relaxed: rows.filter((r) => r.posture === "relaxed"),
    notes: rows.filter((r) => r.posture === "note"),
    strictWords: rows.filter((r) => r.posture === "strict" && r.strictWord).map((r) => r.strictWord as string),
  };
}

export const POSTURE_TITLE = "Security configuration";
export const postureBadge = (n: number) => (n ? n + " relaxed" : "strict");
export const POSTURE_HELP = "Settings that decide how strict Straza is. Change them in the deployment config.";
export const strictLine = (words: string[]) => "Strict: " + words.join(", ") + ".";
// asSentence starts a cost with a capital and ends it with a full stop, so
// each cost reads as a sentence of its own.
const asSentence = (cost: string) => {
  const text = cost.charAt(0).toUpperCase() + cost.slice(1);
  return text.endsWith(".") ? text : text + ".";
};
export const notesLine = (rows: ConfigRow[]) => rows.map((r) => asSentence(r.cost || "")).join(" ");
export const setLink = (r: ConfigRow) => "Set " + r.key.split(",")[0].trim();
export const ALL_STRICT = "Every governance setting holds its strict value.";

// ---- needs attention ----

export type AttentionTone = "warn" | "danger";
export type Attention = { tone: AttentionTone; text: string; to: "audit" | "servers" | "approvals" | "sessions" | "settings" | "sink"; sink?: string };

export const ATTENTION_TITLE = "Needs attention";
export const ATTENTION_HELP = "What a person should act on now. Each line opens where the action is.";
export const NOTHING_TO_DO = "Nothing needs attention.";
export const chainBrokenLine = (seq: number) => "The audit chain is broken at record " + nfmt(seq) + ": records after it may have been altered. Verify the whole chain with strazactl audit verify.";
export const failedLine = (n: number, names: string[]) => plural(n, "MCP server", "MCP servers") + " failed: " + names.join(", ") + ".";
export const degradedLine = (n: number, names: string[]) => plural(n, "MCP server is", "MCP servers are") + " degraded: " + names.join(", ") + ".";
export const waitingLine = (n: number, oldest: ApprovalRow | undefined, oldestAgo: string) => plural(n, "call waits", "calls wait") + " for approval" + (oldest ? ": " + (oldest.username || oldest.user) + " asked for " + oldest.summary + " " + oldestAgo + "." : ".");
// draftsLine is the drafts that wait, counted from one page of the list, and
// the oldest one's proposer.
export const draftsLine = (n: number, more: boolean, who: string, ago: string) =>
  (more ? n + " or more drafts wait" : plural(n, "draft waits", "drafts wait")) + " for review." + (who ? " Oldest: " + who + ", " + ago + "." : "");
export const LANE_DOWN = "Live updates are off. Clients check for revocations and approvals every 30 s. Check that clients can reach the live update address.";
export const sinkLine = (s: SinkRow, since: string) => "Audit sink " + s.name + " holds " + nfmt(s.parked) + (s.parked === 1 ? " undelivered event" : " undelivered events") + (since ? " since " + since : "") + (lastError(s) ? ": " + lastError(s) : "") + ". Fix the target, then replay.";
export const lastError = (s: SinkRow) => (s.streams || []).map((x) => x.last_error).find((e) => e) || "";
// REPLAY is the sink row's button. replayWord names it for a screen
// reader, since the line beside it carries the sink's name.
export const REPLAY = "Replay";
export const replayWord = (name: string) => REPLAY + " " + name;
export const relaxedLine = (rows: ConfigRow[]) => rows.length + " governance settings are relaxed: " + rows.map((r) => (r.short || r.name).toLowerCase()).join(", ") + ".";
export const NOT_READ = "not read";
// seatLine stands in a panel the reader's role cannot read, by the area its
// read needs. Security configuration is the one panel title in the singular.
export const seatLine = (what: string, area: string) => needsArea(what + (what === POSTURE_TITLE ? " is" : " are"), area);

// ---- the tiles ----

export const TILE = {
  sessions: { label: "Sessions", help: "Active sessions, of every session issued." },
  users: { label: "Users", help: "People and agents Straza knows. Active means not disabled, not locked, not deleted." },
  servers: { label: "MCP servers", help: "Servers answering their last probe, of every server registered." },
  policies: { label: "Policies", help: "Policies that are live now, of every policy stored." },
  approvals: { label: "Approvals", help: "Calls that wait for a decision." },
  audit: { label: "Audit chain", help: "The browser re-hashes the newest 25 records. strazactl audit verify checks the whole chain." },
};
export const pushDetail = (laneUp: boolean, connected: number, revoked: number) => (laneUp ? connected + " with live updates" : "live updates off") + (revoked ? ", " + nfmt(revoked) + " revoked" : "");
export const usersDetail = (total: number, active: number) => total === 0 ? "No users" : total === active ? "All users active" : Math.max(0, total - active) + " inactive";
export const serversDetail = (a: OverviewAnswer["apps"]) => {
  if (!a) return TILE_UNREAD;
  const x = a || {};
  const parts = [x.failed ? x.failed + " failed" : "", x.degraded ? x.degraded + " degraded" : "", x.pending ? x.pending + " starting" : ""].filter(Boolean);
  return parts.length ? parts.join(", ") : x.total === 0 ? "No servers registered" : x.total !== undefined && x.running === x.total ? "All servers responding" : "Server health partially available";
};
export const offDetail = (total: number, active: number) => Math.max(0, total - active) + " off";
export const approvalsDetail = (n: number, oldestAgo: string) => (n ? "Oldest request: " + oldestAgo : "No waiting requests");
// CHAIN_INTACT is the audit line under a verified chain, and CHAIN_SCOPE
// the Audit tile's detail, where the tile's value already says Verified.
export const CHAIN_INTACT = "Newest records verified, up to 25";
export const CHAIN_SCOPE = "Newest records, up to 25";
export const chainBrokenDetail = (seq: number) => "broken at " + nfmt(seq);
export const CHAIN_SEAT = "not checked, because your role does not include Audit";
export const CHAIN_UNVERIFIED = "Not checked: needs HTTPS or localhost";
export const readLine = (ago: string) => "Updated " + ago + " · refreshes every 30 s";

// ---- decisions ----

export const DECIDED_TITLE = "Tool decisions · last 24 hours";
export const DECIDED_HELP = "Tool decisions recorded by the gateway and hooks in the last 24 hours. A call that waited for a person counts once, as needs approval. A read of the tool list runs no tool, so it is counted apart.";
export const DECIDED_NONE = "No tool decisions recorded in the last 24 hours.";
export const bucketTitle = (from: string, to: string, allowed: number, approval: number, denied: number, running = false) => from + " to " + to + " UTC" + (running ? SO_FAR : "") + ": " + allowed + " allowed, " + approval + " needs approval, " + denied + " denied";
export const STOPPED_TITLE = "Calls denied or needing approval";
export const STOPPED_HELP = "The tools with the most calls denied or needing approval in the last 24 hours.";
export const STOPPED_NONE = "No call was denied or needed approval in the last 24 hours.";
export const outcomeWord = (o: string) => (o === "approval" ? OUTCOME_WORD.approval : OUTCOME_WORD.denied);
export const toolWords = (app: string, tool: string) => (app ? app + " / " + tool : tool);

// ---- what changed, who is signed in, live ----

export const CHANGED_TITLE = "Recent changes";
export const CHANGED_HELP = "The newest changes to servers, roles, access, users and policies.";
export const CHANGED_NONE = "No recent changes.";
export const SIGNED_IN_TITLE = "Active sessions";
export const SIGNED_IN_HELP = "Active sessions, newest first, up to ten.";
export const NOT_MEASURED_ADMIN = "not measured";
export const ADMIN_CLIENT = "admin";
export const CLIENT_BUILD_DIFFERS = "differs from Straza";
export const buildsTitle = (client: string, server: string) => "This session started on " + client + ". Straza runs " + server + ".";
export const NO_GOVERNED = "No governed sessions.";
// HOOK_WORD says a session's hook configuration state in words.
export const HOOK_WORD: Record<string, string> = { current: "current", allowed: "not current", mismatch: "not registered", unmeasured: "not measured" };
// fleetLine counts the governed sessions by their hook configuration state.
// The counts are keyed by the state the server sends, and the line says
// each one through HOOK_WORD.
export const fleetLine = (governed: number, counts: Record<string, number>, different: number) => plural(governed, "governed session", "governed sessions") + ": " + Object.keys(counts).map((k) => counts[k] + " " + (HOOK_WORD[k] || k)).join(", ") + (different ? " · " + plural(different, "client build differs", "client builds differ") + " from Straza" : "");
export const LIVE_TITLE = "Recent audit events";
export const LIVE_HELP = "The newest audit records while Follow is on.";
export const LIVE_OFF = "Follow is off.";
export const FOLLOW = "Follow";
export const newSince = (n: number) => n + " new since Follow";

// ---- day zero ----

export type SetupStep = { key: "server" | "role" | "policy" | "approver" | "floor"; title: string; line: string; to: "servers" | "roles" | "policies" | "approvals" | "settings"; rest?: string[] };
export const SETUP_TITLE = "Deployment setup";
export const SETUP_HELP = "A mark means the object exists, not that a call works.";
export const setupProgress = (done: number, total: number) => done + " of " + total + " configured";
export const SETUP: SetupStep[] = [
  { key: "server", title: "Add an MCP server", line: "Register the server, choose its transport and configure authentication.", to: "servers" },
  { key: "role", title: "Create a role and give it access", line: "Give roles access to server tools and assign membership at the correct identity authority.", to: "roles" },
  { key: "policy", title: "Publish a policy", line: "Review the policy rules, set what needs approval or is denied, and test a call.", to: "policies" },
  { key: "approver", title: "Configure approvers and delivery", line: "Choose who can decide and how to notify them. Enroll a signing device where the approval mode requires one.", to: "approvals" },
  { key: "floor", title: "Review client verification", line: "Check the lowest level of client verification a session must reach. Prepare managed installations before you require them.", to: "settings", rest: ["configuration", "floor"] },
];

// ---- the Configuration tab and the draft sheet ----
// The tab reads the configuration under SUBJECT_CONFIG, declared with the
// other read subjects further down.

export const READING_CONFIG = "Reading the configuration.";
export const RELAXED_BADGE = "relaxed";
export const copyLabel = (line: string) => "Copy the line for the config file: " + line;
export const LINE_COPIED = "Line copied.";
export const COPY_REFUSED = "The browser refused the clipboard, so nothing was copied. Select the text and copy it by hand.";

// The facts of an opened row: where its value is set and when a change
// takes effect.
export const FACT = { file: "Set in the config file", env: "Environment", effect: "Takes effect", from: "Comes from" };
export const ON_RESTART = "When Straza restarts";
export const FROM_SETS = "The policies that are live";
export const ON_PUBLISH = "When a policy is published";
export const NO_ENV = "No environment variable for this setting.";
export const NOT_SET = "not set";
export const envCopyLabel = (variable: string) => "Copy the environment line: " + variable;
export const OPEN_REGISTRY = "Open the attestation registry";

// valuesOf reads the closed set a row's help names ("Values: allow,
// deny."), or an empty list when the row takes free text.
export function valuesOf(r: ConfigRow): string[] {
  if (["events", "jit", "ownDecisions", "secondPerson"].includes(r.id)) return ["true", "false"];
  if (r.id === "floor") return ["none", "advisory", "managed"];
  const at = r.help.lastIndexOf("Values:");
  if (at < 0) return [];
  return r.help.slice(at + "Values:".length).split(".")[0].split(",").map((v) => v.trim()).filter(Boolean);
}

// consequenceOf is the sentence under a drafted change: what the value
// costs when the row says it, else the first sentence of the row's help.
export function consequenceOf(r: ConfigRow): string {
  const raw = r.cost || r.help.split(". ")[0];
  const text = raw.charAt(0).toUpperCase() + raw.slice(1);
  return text.endsWith(".") ? text : text + ".";
}

// ---- the Console access section ----

export const EVERY_AREA_WORD = "every area";
export const NO_AREA_WORD = "no area";
export const openRole = (name: string) => "Open " + name;

// consoleAreasWords reads a Straza role's areas the way the
// Console access row says them: every area for the root grant, the area
// names for a narrower one, and the word for a role the config maps to
// nothing.
export function consoleAreasWords(areas: string[] | undefined): string {
  const list = areas || [];
  if (list.includes("full")) return EVERY_AREA_WORD;
  const names = [...new Set(list.map((a) => a.split(":")[0]).filter(Boolean))];
  return names.length ? names.join(", ") : NO_AREA_WORD;
}

// ---- the draft sheet ----

export const DRAFT_CHANGES = "Changes";
export const DRAFT_EMPTY = "No setting picked.";
export const PICK_LABEL = "Setting";
export const ADD_ROW_HINT = "Pick a setting to change";
export const CHANGE_NOW = "now";
export const VALUE_PLACEHOLDER = "the new value";
export const MISSING_VALUE = "Type the new value before copying the fragment.";
export const includeLabel = (name: string) => "Include " + name + " in the fragment";
export const valueLabel = (name: string) => "New value for " + name;
export const FRAGMENT_TITLE = "The fragment";
export const FILE_LABEL = "Config file";
export const VALUES_LABEL = "Chart values";
export const ENV_LABEL = "Environment";
export const COPY_FRAGMENT = "Copy";
export const FRAGMENT_COPIED = "The lines are copied.";
export const CLOSE = "Close";

// ---- the Overview screen: the words the six panels needed beyond the
// block above ----

// The subject of each read, for the sentence a failed read prints.
export const SUBJECT_OVERVIEW = "The deployment counts";
export const SUBJECT_CONFIG = "The configuration";
export const SUBJECT_APPROVALS = "The waiting approvals";
export const SUBJECT_CHAIN = "The audit chain";
export const SUBJECT_SINKS = "The audit sinks";
export const SUBJECT_SESSIONS = "The sessions";
export const SUBJECT_SERVERS = "The MCP servers";
export const SUBJECT_CHANGES = "The change list";
export const SUBJECT_DRAFTS = "The waiting drafts";
export const SUBJECT_ROLES = "The roles";
export const SUBJECT_LIVE = "The live records";

// lacksGrant is the clause a tile prints under the not-read word, where
// seatLine would repeat the word above it. It takes the area the tile's
// read needs.
export const lacksGrant = (area: string) => "your role does not include " + (AREA_WORD[area] || area);

// READING_OVERVIEW holds the page before its first read lands; NO_ANSWER
// stands where a number would be when the read that carries it never
// answered, so a tile never reads zero for a count nobody has.
export const READING_OVERVIEW = "Reading the deployment.";
export const NO_ANSWER = "no answer";

// The small type beside a tile's number.
export const tileOf = (n: number | undefined) => "of " + nfmt(n);
export const TILE_WAITING = "waiting";
export const TILE_RECORDS = "records";

// The decisions strip: the three series, the picture's own name for a
// screen reader, and the three words under the bars.
export const DECIDED_ALLOWED = "allowed";
export const DECIDED_APPROVAL = "needs approval";
export const DECIDED_DENIED = "denied";
export const DECIDED_ALT = "Calls decided per hour over the last 24 hours";
export const DECIDED_AXIS = ["24 h ago", "12 h ago", "now"];

// What was stopped: the columns, and the name of the door on a row.
export const STOPPED_COLUMN = { tool: "Tool", times: "Count", outcome: "Outcome", why: "Why, most often" };
export const stoppedRowLabel = (tool: string) => "View reason for " + tool;

// Who is signed in: the columns, and the harnesses that are a person at a
// keyboard rather than a governed agent.
export const SIGNED_IN_COLUMN = { who: "Who", harness: "Harness", client: "Client", wiring: "Hook configuration", seen: "Last seen" };
export const ADMIN_HARNESSES = ["console", "strazactl", "approvals"];
export const harnessName = (h: string | undefined) => (h || "").split("/")[0];
export const isAdminHarness = (h: string | undefined) => ADMIN_HARNESSES.includes(harnessName(h));

// The answer to a replay of an audit sink's undelivered events.
export const replayedWords = (name: string, replayed: number, remaining: number) =>
  "Audit sink " + name + " replayed " + nfmt(replayed) + (replayed === 1 ? " event" : " events") + ". " + nfmt(remaining) + " still undelivered.";

// ---- the Overview's panels: their labels, and the states a read left empty ----

// A panel whose read did not answer says only that it did not load: the
// page reads again on its own and names a read that failed at its top.
export const CONFIG_UNREAD = "The configuration did not load.";
export const CONFIG_PARTIAL = "Some values are missing from Straza's answer. Settings › Configuration marks them Unavailable.";
export const CONFIG_DENIED = "Your role cannot read the configuration. Ask an administrator for a role that can.";
export const DECIDED_UNREAD = "Decision counts did not load.";
export const STOPPED_UNREAD = "The calls denied or needing approval did not load.";
export const SESSIONS_UNREAD = "The active sessions did not load.";
export const CHANGES_UNREAD = "The recent changes did not load.";
export const TILE_UNREAD = "The deployment counts did not load.";
export const CHAIN_UNREAD = "The newest audit records did not load.";
export const PARTIAL_CHECKS = "No issues in the checks that ran. Some could not run.";
export const summaryUnread = (denied: boolean) => denied ? "The summary is not shown, because your role cannot read the configuration." : "The deployment counts did not load.";

export const OVERVIEW_TABS = { label: "Overview views", summary: "Summary", activity: "Activity", system: "System details" };
export const summaryLastRead = (ago: string) => "Summary last read " + ago;
export const refreshFailed = (n: number) => n + (n === 1 ? " section could" : " sections could") + " not be refreshed.";
export const postureIncomplete = (relaxed: number) => (relaxed ? postureBadge(relaxed) + " · some values unavailable" : "Partially available");
export const COUNT_WORD = { partial: "partial", none: "none" };
export const VIEW_CHECKS = "View checks";
export const WIRING_NOT_REPORTED = "not reported";
export const PERMISSION_REQUIRED = "Permission required";
export const LOADING = "Loading…";
export const lastReadDot = (ago: string) => "Last read " + ago + " · ";
export const lastCheckedDot = (ago: string) => "Last checked " + ago + " · ";
export const CHAIN_VALUE = { intact: "Verified", broken: "Broken", seat: PERMISSION_REQUIRED, other: "Not verified" };
export const latestRecord = (seq: number) => "Latest record #" + nfmt(seq);

// The inventory strip, the configuration note, the audit line and the
// access guide of the Summary tab.
export const INVENTORY = { servers: "MCP servers running", serversShort: "MCP servers", sessions: "active sessions", users: "active users", policies: "live policies" };
export const SETTING_LABEL = { tls: "Public URL uses HTTP", floorNone: "Client verification is not required", floorAdvisory: "Managed client installation is not required" };
export const CONFIG_NOTE = { denied: "Configuration not shown", incomplete: "Some configuration values are unavailable", none: "No configuration exceptions reported" };
export const valuesLastRead = (ago: string) => "Last read " + ago + ".";
// staleLine stands over a panel whose last read failed: what the panel
// shows and when it was last read.
export const STALE_WHAT = { config: "Configuration", sessions: "Sessions", decisions: "Decision counts", changes: "Recent changes" };
export const staleLine = (what: string, ago: string) => what + " last read " + ago + ".";
export const NO_RECORDS_TO_VERIFY = "No audit records to verify";
export const lastChecked = (ago: string) => "Last checked " + ago + ". ";
export const GUIDE = {
  open: "Access setup guide",
  title: "Configure and verify access",
  lede: "Follow the access path from the server connection to the recorded decision.",
  steps: [
    { line: "Check the server's connection and whose credential it uses.", to: "servers" },
    { line: "Give roles access to the intended tools, including tools the server adds later.", to: "roles" },
    { line: "Check assigned and included roles. Change membership in the identity system that owns it.", to: "users" },
    { line: "Review policy conditions and simulate a call from the server page.", to: "policies" },
    { line: "For calls that need approval, set who approves and from which devices.", to: "approvals" },
    { line: "Make a real call through your agent's harness, then inspect the recorded decision.", to: "audit" },
  ],
} as const;

// The decisions panel, its hourly chart and the sheet of one hour.
export const DECIDED_PANEL = "Tool decisions";
export const LAST_24 = "Last 24 hours";
export const TOTAL_LABEL = { allowed: OUTCOME_LABEL.allowed, denied: OUTCOME_LABEL.denied, approval: OUTCOME_LABEL.approval };
export const CATALOG_APART = "Tool list reads";
export const OPEN_AUDIT = "Open Audit";
export const UNKNOWN_HOUR = "Unknown hour";
export const HOUR_TITLE = "Hourly tool decisions";
export const hourSpan = (from: string, to: string) => from + " to " + to + " UTC";

// The last hour view of the same panel: the switch, the chart by the minute,
// the sheet of one minute and the sentences that stand in for the chart.
// SO_FAR marks the minute that is still running.
export const WINDOW_LABEL = "Window";
export const LAST_HOUR = "Last hour";
export const LAST_HOUR_HELP = "Tool decisions recorded by the gateway and hooks in the last hour, one bar per minute, counted again every 5 s. A call that waited for a person counts once, as needs approval. A read of the tool list runs no tool, so it is counted apart.";
export const LAST_HOUR_NONE = "No tool decisions recorded in the last hour.";
export const LAST_HOUR_READING = "Reading the last hour.";
export const LAST_HOUR_UNREAD = "The last hour did not load.";
export const LAST_HOUR_UNSUPPORTED = "This version of Straza does not count the last hour by the minute. Update Straza to see the last hour, or go back to Last 24 hours.";
export const SUBJECT_LAST_HOUR = "The last hour of decisions";
export const PER_BUCKET = { hour: "Decisions per hour", minute: "Decisions per minute" };
export const MINUTES_ALT = "Calls decided per minute over the last hour";
export const MINUTE_TITLE = "Tool decisions in one minute";
export const SO_FAR = ", so far";
