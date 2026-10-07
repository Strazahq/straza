// The words of the Overview and the Configuration tab: one row per
// configuration key, read by both pages so a setting has one name, the
// posture that Overview badges, the attention sentences, the tile help and
// the setup list of an empty deployment. Every surface sentence is short;
// the sentence that explains a thing sits behind its help icon.
import { configFragment, type ConfigScalar } from "./config-export";
import type { ApprovalRow, ConfigAnswer, OverviewAnswer, SinkRow } from "./api";

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

// hoursWords reads a retention in hours with the days beside it.
export function hoursWords(h: number | undefined): string {
  const n = h || 0;
  if (n === 0) return "0";
  if (n < 48) return n + " h";
  return n + " h (" + Math.round(n / 24) + " d)";
}

export const nfmt = (n: number | undefined) => (n || 0).toLocaleString("en-US");
export const plural = (n: number, one: string, many: string) => n + " " + (n === 1 ? one : many);

// ---- the configuration rows ----

export type Section = "Deployment" | "Sign-in" | "Governance" | "Approvals" | "MCP servers" | "Recording";
export const SECTIONS: Section[] = ["Deployment", "Sign-in", "Governance", "Approvals", "MCP servers", "Recording"];

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
  const rows: ConfigRow[] = [
    { id: "profile", section: "Deployment", name: "Profile", key: "profile", env: "STRAZA_PROFILE", value: c.profile || "unknown", posture: "plain",
      help: "standalone bundles the identity issuer and the store into this one binary. enterprise expects your own identity provider, Postgres and NATS. Values: standalone, enterprise." },
    { id: "public_url", section: "Deployment", name: "Public URL", key: "server.publicUrl", env: "STRAZA_PUBLIC_URL", value: c.public_url || "not set", posture: "plain",
      help: "The address clients, phones and QR codes are told to come back to. Wrong here and enrolled agents cannot reach the server." },
    { id: "tls", section: "Deployment", name: "TLS terminated by strazad", short: "Transport", key: "server.tls.certFile, server.tls.keyFile", env: "STRAZA_TLS_CERT_FILE, STRAZA_TLS_KEY_FILE", value: c.tls ? "on" : "off", postureValue: c.tls ? "TLS" : plainHTTP ? "plain http" : "TLS at your ingress",
      posture: c.tls ? "strict" : plainHTTP ? "relaxed" : "note",
      help: "on when strazad itself serves HTTPS. off is right behind a TLS ingress, and counts as relaxed only when the public URL is plain http: then nothing terminates TLS in front of this server.",
      cost: plainHTTP ? "Session tokens and approvals cross the network in the clear." : "TLS ends at your ingress, so the hop from it to strazad is only as private as your network",
      strictWord: "strazad terminates TLS" },
    { id: "store", section: "Deployment", name: "Store", key: "store.driver", env: "STRAZA_STORE_DRIVER", value: c.store_driver || "unknown", posture: "plain",
      help: "sqlite is single node. postgres is the replicated shape, where every pod shares one database. Values: sqlite, postgres." },
    { id: "events", section: "Deployment", name: "Event broker", key: "events.embedded", env: "", value: c.events && c.events.embedded === false ? "external" : "embedded", posture: c.events && c.events.embedded === false ? "plain" : "note",
      help: "embedded means this pod runs its own broker, so revocations and approvals never leave it. Right at one replica, split brain above one: a second replica needs an external NATS, named in STRAZA_EVENTS_URL.",
      cost: "the event broker is embedded, so a second replica needs an external one first" },
    { id: "issuer", section: "Sign-in", name: "Identity provider", key: "oidc.issuer", env: "STRAZA_OIDC_ISSUER", value: (c.oidc && c.oidc.external_issuer) || "built into this server", posture: "plain",
      help: "The provider that signs people in. Empty means the issuer built into this server." },
    { id: "jit", section: "Sign-in", name: "Sign-in creates users", key: "oidc.jitProvision", env: "STRAZA_OIDC_JIT", value: c.oidc && c.oidc.jit_provision ? "true" : "false", posture: c.oidc && c.oidc.jit_provision ? "note" : "strict",
      help: "true creates a Straza user on the first successful sign-in. false means only provisioned identities can check in. Values: true, false.",
      cost: "anyone your identity provider signs in becomes a Straza user", strictWord: "sign-in creates no users" },
    { id: "floor", section: "Governance", name: "Client attestation floor", key: "governance.minAttestation", env: "STRAZA_MIN_ATTESTATION", value: floor, posture: floor === "managed" ? "strict" : "relaxed",
      help: "The weakest client posture allowed to check in. Values: none, advisory, managed. Raise it to managed only after every client box ran sudo straza install --managed: a box that did not is refused at its next check-in. The admin console and strazactl stay exempt.",
      cost: floor === "none" ? "Check-in does not require verified installation hashes." : "Reported measurements are required, but a managed installation is not.",
      strictWord: "the attestation floor is managed" },
    { id: "grace", section: "Governance", name: "Offline grace", key: "governance.offlineGraceTTL", env: "", value: secondsWords(g.offline_grace_ttl_seconds), posture: g.offline_grace_ttl_seconds ? "note" : "strict",
      help: "How long a client keeps deciding from its cached snapshot while the server is unreachable. 0 denies the moment the server goes away.",
      cost: "a client cut off from the server keeps deciding for " + secondsWords(g.offline_grace_ttl_seconds), strictWord: "an unreachable server denies at once" },
    { id: "local", section: "Governance", name: "Local tool default", key: "governance.localToolDefault", env: "", value: g.local_tool_default || "deny", posture: g.local_tool_default === "allow" ? "relaxed" : "strict",
      help: "The verdict for a local tool no rule matched. allow is permission by omission. Values: allow, deny.",
      cost: "A local tool no rule names runs.", strictWord: "local tools deny by default" },
    { id: "bp", section: "Governance", name: "Audit backpressure", key: "governance.auditBackpressure", env: "", value: g.audit_backpressure || "block", posture: g.audit_backpressure && g.audit_backpressure !== "block" ? "relaxed" : "strict",
      help: "What happens when audit ingest saturates. block holds the caller so nothing goes unrecorded. drop-with-counter keeps traffic moving and loses evidence. Values: block, drop-with-counter.",
      cost: "Under load, calls proceed unrecorded and a counter is all that remains.", strictWord: "audit blocks under load" },
    { id: "hold", section: "Approvals", name: "Gateway hold", key: "approval.gatewayHoldSeconds", env: "STRAZA_APPROVAL_GATEWAY_HOLD_SECONDS", value: secondsWords(c.approval && c.approval.gateway_hold_seconds), posture: "plain",
      help: "How long a call that needs approval keeps its socket open before the caller gets the structured pending answer. The rule's decision window is always the ceiling; lower it for clients that time out sooner." },
    { id: "ownDecisions", section: "Approvals", name: "Own requests without a signed device", key: "approval.unsignedOwnDecisions", env: "", value: c.approval && c.approval.unsigned_own_decisions ? "true" : "false", posture: c.approval && c.approval.unsigned_own_decisions ? "relaxed" : "strict",
      help: "false means a person confirms their own agent's call only on an enrolled phone or browser, which sign the decision with a key the agent cannot reach. true also accepts the console, strazactl and Slack. Values: true, false.",
      cost: "An agent that runs on a person's machine can approve that person's held calls, its own included.", strictWord: "own requests need a signed device" },
    { id: "watch", section: "MCP servers", name: "Apps directory watcher", key: "apps.dir", env: "STRAZA_APPS_DIR", value: c.apps && c.apps.gitops_dir_enabled ? "on" : "off", posture: c.apps && c.apps.gitops_dir_enabled ? "note" : "plain",
      help: "This pod watches an apps directory: a file dropped or changed there proposes a draft that adds or changes an MCP server, and a file removed there proposes the server's removal. Nothing from a file is live until a person publishes its draft. Read from the running watcher, not from a default.",
      cost: "the apps directory watcher is on, so whoever writes that directory proposes server changes, which a person may publish without reading them" },
    { id: "upstream", section: "MCP servers", name: "Upstream call timeout", key: "apps.upstreamTimeout", env: "", value: secondsWords(c.apps && c.apps.upstream_timeout_seconds), posture: "plain",
      help: "Ceiling on one gateway call to an MCP server before it fails closed." },
    { id: "sets", section: "Recording", name: "Policy sets recording", key: "", env: "", value: String((c.capture && c.capture.policy_sets) || 0), posture: "plain",
      help: "Live policy sets that record sessions, set in each set's capture block. Zero means nothing is recorded, whatever the retention says." },
    { id: "mode", section: "Recording", name: "Mode", key: "", env: "", value: (c.capture && c.capture.mode) || "none", posture: "plain",
      help: "verbatim keeps the turns word for word. redact masks secrets first. mixed means the recording sets disagree. none means no set records." },
    { id: "ret", section: "Recording", name: "Retention", key: "governance.captureRetention", env: "STRAZA_CAPTURE_RETENTION", value: hoursWords(c.capture && c.capture.retention_hours), posture: "plain",
      help: "How long recorded turns are kept before the sweep deletes them." },
    { id: "bodies", section: "Recording", name: "Bodies stored", key: "capture.bodyStore.type", env: "", value: (c.capture && c.capture.body_store) || "inline", posture: "plain",
      help: "inline keeps turn bodies in the database. s3 keeps them in a bucket keyed by content hash, with the database holding the pointer. Values: inline, s3." },
  ];
  const raw: Record<string, ConfigScalar | undefined> = {
    profile: c.profile, public_url: c.public_url, store: c.store_driver,
    events: c.events?.embedded, issuer: c.oidc?.external_issuer, jit: c.oidc?.jit_provision,
    floor: g.min_attestation, grace: g.offline_grace_ttl_seconds === undefined ? undefined : g.offline_grace_ttl_seconds + "s",
    local: g.local_tool_default, bp: g.audit_backpressure, hold: c.approval?.gateway_hold_seconds,
    ownDecisions: c.approval?.unsigned_own_decisions,
    upstream: c.apps?.upstream_timeout_seconds === undefined ? undefined : c.apps.upstream_timeout_seconds + "s",
    ret: c.capture?.retention_hours === undefined ? undefined : c.capture.retention_hours + "h",
    bodies: c.capture?.body_store,
  };
  return rows.map((row) => {
    const present = row.id === "tls" ? c.tls !== undefined : row.id === "watch" ? c.apps?.gitops_dir_enabled !== undefined : row.id === "sets" ? c.capture?.policy_sets !== undefined : row.id === "mode" ? c.capture?.mode !== undefined || c.capture?.policy_sets === 0 : raw[row.id] !== undefined;
    return present ? { ...row, raw: raw[row.id] } : { ...row, raw: undefined, value: VALUE_UNAVAILABLE, help: row.help + " " + VALUE_UNREPORTED, posture: "plain", cost: undefined, strictWord: undefined };
  });
}

// VALUE_UNAVAILABLE stands in a row whose value strazad's answer left out,
// and VALUE_UNREPORTED joins the row's help to say why and where to look.
export const VALUE_UNAVAILABLE = "Unavailable";
export const VALUE_UNREPORTED = "strazad's configuration answer leaves this value out, so the console cannot show it. Read it in the config file or the chart values.";

// whereWords is the mono line under a row's name: the key and the variable
// that set it, or the derivation when no key exists.
export function whereWords(r: ConfigRow): string {
  if (!r.key) return "derived from the active policy sets";
  return r.env ? r.key + " · " + r.env : r.key;
}

// setWords ends a row's help: where to set it and how.
export function setWords(r: ConfigRow): string {
  if (!r.key) return "";
  const key = r.key.split(",")[0].trim();
  const env = r.env.split(",")[0].trim();
  return env ? "Set " + key + " in the config file or " + env + " in the environment." : "Set " + key + " in the config file or the chart values.";
}

// fileLine is what the row's copy button yields: the config file line for
// its first key and its value.
export const fileLine = (r: ConfigRow) => r.key && !r.key.includes(",") && r.raw !== undefined ? configFragment([{ key: r.key, value: r.raw }]) : "";

export const CONFIG_LEDE = "Read from the config file, the environment and your chart, never from the database. Nothing here changes in a browser; each row says where to set it. Available values can be copied as configuration. Click a name for what it means.";
export const CONFIG_WHY = "A change made in a browser would drift from the file your deployment ships and revert on the next upgrade, so the console shows and never edits. Connection values such as database addresses and file paths are never sent to this page.";
export const CAPTURE_SUB = "what the Transcripts area records";
export const CONSOLE_ACCESS_TITLE = "Console access";
export const CONSOLE_ACCESS_LINE = "Set in strazad's config (admin.roleAreas). The console reads it and cannot change it.";
export const DRAFT_TITLE = "Write a config change";
export const DRAFT_LEDE = "Pick the rows to change and read what each costs. The fragment is what you paste into the deployment; nothing changes here.";
export const DRAFT_ROWS_HELP = "Add rows with the picker; each keeps its current value on the left and the value you pick on the right, with the consequence sentence from the row's own help.";
export const DRAFT_FRAGMENT_HELP = "The same changes three ways: the config file, the chart values, and the environment where a variable exists. Take the one your deployment uses.";
export const DRAFT_DEPLOY = "Deploy it the way your deployment changes config: a restart, a rollout, or the chart upgrade. Overview shows the new posture at its next read.";
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
export const POSTURE_HELP = "The governance settings that decide how strict this server is, read from its configuration. Relaxed names each setting that loosens the system and what it costs; the strict ones fold into one line. Nothing here can be changed in a browser: each link opens the row in Settings that says where to set it.";
export const strictLine = (words: string[]) => words.length + " hold their strict value: " + words.join(", ") + ".";
export const notesLine = (rows: ConfigRow[]) => "Worth knowing: " + rows.map((r) => r.cost).join("; ") + ".";
export const setLink = (r: ConfigRow) => "Set " + r.key.split(",")[0].trim();
export const ALL_STRICT = "Every governance setting holds its strict value.";

// ---- needs attention ----

export type AttentionTone = "warn" | "danger";
export type Attention = { tone: AttentionTone; text: string; to: "audit" | "servers" | "approvals" | "sessions" | "settings" | "sink"; sink?: string };

export const ATTENTION_TITLE = "Needs attention";
export const ATTENTION_HELP = "Only what a person should act on: waiting approvals, drafts that wait for review, servers that failed or degraded, a broken audit chain, a push lane down, sinks holding parked events, relaxed governance settings. Each line opens the area where the action is.";
export const NOTHING_TO_DO = "No issues found in the available checks.";
export const chainBrokenLine = (seq: number) => "The audit chain is broken at record " + nfmt(seq) + ": records after it may have been altered. Verify the whole chain with strazactl audit verify.";
export const failedLine = (n: number, names: string[]) => plural(n, "MCP server", "MCP servers") + " failed: " + names.join(", ") + (n === 1 ? " has" : " have") + " not answered a probe.";
export const degradedLine = (n: number, names: string[]) => plural(n, "MCP server is", "MCP servers are") + " degraded: " + names.join(", ") + ".";
export const waitingLine = (n: number, oldest: ApprovalRow | undefined, oldestAgo: string) => plural(n, "call waits", "calls wait") + " for approval" + (oldest ? ": " + (oldest.username || oldest.user) + " asked for " + oldest.summary + " " + oldestAgo + "." : ".");
// draftsLine is the drafts that wait, counted from one page of the list, and
// the oldest one's proposer.
export const draftsLine = (n: number, more: boolean, who: string, ago: string) =>
  (more ? n + " or more drafts wait" : plural(n, "draft waits", "drafts wait")) + " for review" + (who ? ", the oldest from " + who + ", drafted " + ago + "." : ".");
export const LANE_DOWN = "The push lane is down, so every daemon polls for revocations and approvals every 30 s instead of hearing them at once.";
export const sinkLine = (s: SinkRow, since: string) => "Sink " + s.name + " holds " + nfmt(s.parked) + " parked events" + (since ? " since " + since : "") + (lastError(s) ? ": " + lastError(s) : "") + ". Fix the target, then replay.";
export const lastError = (s: SinkRow) => (s.streams || []).map((x) => x.last_error).find((e) => e) || "";
export const replayWord = (name: string) => "Replay " + name;
export const relaxedLine = (rows: ConfigRow[]) => rows.length + " governance settings are relaxed: " + rows.map((r) => (r.short || r.name).toLowerCase()).join(", ") + ".";
export const NOT_READ = "not read";
export const seatLine = (what: string, grant: string) => what + " " + NOT_READ + ": this session lacks " + grant + ".";

// ---- the tiles ----

export const TILE = {
  sessions: { label: "Sessions", help: "Sessions active now, of every session this server has issued. On the push lane means the daemon hears revocations and approvals at once; the rest poll every 30 s." },
  users: { label: "Users", help: "People and agents Straza knows. Active means not disabled, not locked, not deleted." },
  servers: { label: "MCP servers", help: "Servers answering their last probe, of every server installed." },
  policies: { label: "Policies", help: "Policy sets that are live now, of every set stored." },
  approvals: { label: "Approvals", help: "Calls that need approval, counted from the pending list." },
  audit: { label: "Audit chain", help: "The newest record's number. On every load the browser re-hashes the newest 25 records and their links, so the word intact is proven here, not assumed. strazactl audit verify re-hashes the whole chain." },
};
export const pushDetail = (laneUp: boolean, connected: number, revoked: number) => (laneUp ? connected + " on the push lane" : "push lane down") + (revoked ? ", " + nfmt(revoked) + " revoked" : "");
export const usersDetail = (total: number, active: number) => total === 0 ? "No users" : total === active ? "All users active" : Math.max(0, total - active) + " inactive";
export const serversDetail = (a: OverviewAnswer["apps"]) => {
  if (!a) return TILE_UNREAD;
  const x = a || {};
  const parts = [x.failed ? x.failed + " failed" : "", x.degraded ? x.degraded + " degraded" : "", x.pending ? x.pending + " starting" : ""].filter(Boolean);
  return parts.length ? parts.join(", ") : x.total === 0 ? "No servers registered" : x.total !== undefined && x.running === x.total ? "All servers responding" : "Server health partially available";
};
export const offDetail = (total: number, active: number) => Math.max(0, total - active) + " off";
export const approvalsDetail = (n: number, oldestAgo: string) => (n ? "Oldest request: " + oldestAgo : "No pending requests");
export const CHAIN_INTACT = "Latest records verified (up to 25)";
export const chainBrokenDetail = (seq: number) => "broken at " + nfmt(seq);
export const CHAIN_SEAT = "not checked: this session lacks audit:read";
export const CHAIN_UNVERIFIED = "not re-hashed: needs TLS or localhost";
export const readLine = (ago: string) => "Updated " + ago + " · refreshes every 30 s";

// ---- decisions ----

export const DECIDED_TITLE = "Tool decisions · last 24 hours";
export const DECIDED_HELP = "Tool decisions recorded by the gateway and hooks in the last 24 hours. A held call counts once as required approval. A catalog read lists the tools a caller may use and runs none, so it is counted apart. Hover or focus an hour for counts. Use arrow keys to move between hours, or select one for a breakdown.";
export const DECIDED_NONE = "No tool decisions recorded in the last 24 hours.";
export const bucketTitle = (from: string, to: string, allowed: number, approval: number, denied: number, running = false) => from + " to " + to + " UTC" + (running ? SO_FAR : "") + ": " + allowed + " allowed, " + approval + " needed approval, " + denied + " denied";
export const STOPPED_TITLE = "Denied and held calls";
export const STOPPED_HELP = "Up to five tool and outcome pairs with the most denials or approval requirements in the last 24 hours. View reason shows the most frequent recorded reason and a link to Audit filtered to the tool.";
export const STOPPED_NONE = "No denied or approval-required calls recorded in the last 24 hours.";
export const outcomeWord = (o: string) => (o === "approval" ? "needs approval" : "denied");
export const toolWords = (app: string, tool: string) => (app ? app + " / " + tool : tool);

// ---- what changed, who is signed in, live ----

export const CHANGED_TITLE = "Recent changes";
export const CHANGED_HELP = "The newest control plane changes from the audit chain's admin, identity and policy records: policies published, access given, roles and membership written, servers installed, tokens minted. Who did it, in words. Audit holds every one with its record.";
export const CHANGED_NONE = "No recent administrative changes found.";
export const SIGNED_IN_TITLE = "Active sessions";
export const SIGNED_IN_HELP = "The active sessions, newest first, up to ten; Sessions holds the rest. Hook configuration compares the managed installation with the registry: current, allowed, mismatch or unmeasured. Client identifies the installed Straza version.";
export const NOT_MEASURED_ADMIN = "not measured";
export const ADMIN_CLIENT = "admin";
export const CLIENT_VERSION_DIFFERS = "version differs";
export const NO_GOVERNED = "No governed sessions in this list.";
export const fleetLine = (governed: number, counts: Record<string, number>, different: number) => plural(governed, "governed session", "governed sessions") + ": " + Object.keys(counts).map((k) => counts[k] + " " + k).join(", ") + (different ? " · " + plural(different, "client version differs", "client versions differ") + " from the server" : "");
export const LIVE_TITLE = "Recent audit events";
export const LIVE_HELP = "The newest audit records, refreshed with the page while the switch is on. Off by default and remembered per browser. Audit keeps the full record; this is the window.";
export const LIVE_OFF = "Turn on Follow to refresh recent audit events every 30 seconds.";
export const FOLLOW = "Follow";
export const newSince = (n: number) => n + " new since you looked";

// ---- day zero ----

export type SetupStep = { key: "server" | "role" | "policy" | "approver" | "floor"; title: string; line: string; to: "servers" | "roles" | "policies" | "approvals" | "settings" };
export const SETUP_TITLE = "Deployment setup";
export const SETUP_HELP = "These links open the existing setup screens. Marks indicate configuration found, not verified access or successful live calls.";
export const setupProgress = (done: number, total: number) => done + " of " + total + " configured";
export const SETUP: SetupStep[] = [
  { key: "server", title: "Add an MCP server", line: "Register the server, choose its transport and configure authentication.", to: "servers" },
  { key: "role", title: "Create a role and give it access", line: "Give roles access to server tools and assign membership at the correct identity authority.", to: "roles" },
  { key: "policy", title: "Publish a policy", line: "Review current rules, configure approval or denial conditions, and test a call.", to: "policies" },
  { key: "approver", title: "Configure approvers and delivery", line: "Choose who can decide and how to notify them. Enroll a signing device where the approval mode requires one.", to: "approvals" },
  { key: "floor", title: "Review client verification", line: "Check the current minimum attestation level. Prepare managed installations before requiring managed verification.", to: "settings" },
];

// ---- the Configuration tab and the draft sheet ----
// The tab reads the configuration under SUBJECT_CONFIG, declared with the
// other read subjects further down.

export const READING_CONFIG = "Reading the configuration.";
export const RELAXED_BADGE = "relaxed";
export const copyLabel = (line: string) => "Copy the line for the config file: " + line;
export const LINE_COPIED = "Configuration copied. Merge this fragment into the deployment configuration.";
export const COPY_REFUSED = "The browser refused the clipboard, so nothing was copied. Select the text and copy it by hand.";

// valuesOf reads the closed set a row's help names ("Values: none,
// advisory, managed."), or an empty list when the row takes free text.
export function valuesOf(r: ConfigRow): string[] {
  if (["events", "jit", "ownDecisions"].includes(r.id)) return ["true", "false"];
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
export const DRAFT_EMPTY = "No row is picked yet. Add the setting you want to change and its fragment appears here.";
export const ADD_ROW = "Add a row";
export const ADD_ROW_HINT = "Pick a setting to change";
export const CHANGE_TO = "to";
export const VALUE_PLACEHOLDER = "the new value";
export const MISSING_VALUE = "Type the new value before copying the fragment.";
export const includeLabel = (name: string) => "Include " + name + " in the fragment";
export const valueLabel = (name: string) => "New value for " + name;
export const FRAGMENT_TITLE = "The fragment";
export const FILE_LABEL = "straza.yaml";
export const VALUES_LABEL = "values.yaml";
export const ENV_LABEL = "environment";
export const COPY_FRAGMENT = "Copy the fragment";
export const FRAGMENT_COPIED = "The config file fragment is copied.";
export const CLOSE = "Close";

// ---- the Overview screen: the words the six panels needed beyond the
// block above ----

// The subject of each read, for the sentence a failed read prints.
export const SUBJECT_OVERVIEW = "The deployment counts";
export const SUBJECT_CONFIG = "The configuration";
export const SUBJECT_APPROVALS = "The waiting approvals";
export const SUBJECT_CHAIN = "The audit chain";
export const SUBJECT_SINKS = "The sinks";
export const SUBJECT_SESSIONS = "The sessions";
export const SUBJECT_SERVERS = "The MCP servers";
export const SUBJECT_CHANGES = "The change list";
export const SUBJECT_DRAFTS = "The waiting drafts";
export const SUBJECT_ROLES = "The roles";
export const SUBJECT_LIVE = "The live records";

// lacksGrant is the clause a tile prints under the not-read word, where
// seatLine would repeat the word above it.
export const lacksGrant = (grant: string) => "this session lacks " + grant;

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
export const DECIDED_APPROVAL = "needed approval";
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

// The answer to a replay of a sink's parked events.
export const replayedWords = (name: string, replayed: number, remaining: number) =>
  "Sink " + name + " replayed " + nfmt(replayed) + (replayed === 1 ? " event" : " events") + ". " + nfmt(remaining) + " still parked.";

// ---- the Overview's panels: their labels, and the states a read left empty ----

// RETRY is the next step of a panel whose read did not answer: the page
// reads again on its own, and names a read that failed at its top.
const RETRY = "The page reads again every 30 s, and a read that failed is named at the top of the page.";
export const CONFIG_UNREAD = "The configuration did not load, so its posture is unknown. " + RETRY;
export const CONFIG_PARTIAL = "Some values are missing from strazad's configuration answer, so this panel cannot judge them. Settings, Configuration marks each one Unavailable and says where it is set.";
export const CONFIG_DENIED = "This session lacks config:read, so the configuration cannot be read here. Ask for a role that grants it.";
export const DECIDED_UNREAD = "Decision counts did not load. " + RETRY;
export const STOPPED_UNREAD = "The denied and held calls did not load. " + RETRY;
export const SESSIONS_UNREAD = "The active sessions did not load. " + RETRY;
export const CHANGES_UNREAD = "The recent changes did not load. " + RETRY;
export const TILE_UNREAD = "Not read: the deployment counts did not load. The page reads them again every 30 s.";
export const CHAIN_UNREAD = "Not checked: the newest audit records did not load. The page reads them again every 30 s.";
export const PARTIAL_CHECKS = "No issues found in the checks that ran. Some checks could not run, and the panels on System details say why.";
export const summaryUnread = (denied: boolean) => denied ? "Summary not read: this session lacks config:read." : "Summary not read: the deployment counts did not load. The page reads them again every 30 s.";

export const OVERVIEW_TABS = { label: "Overview views", summary: "Summary", activity: "Activity", system: "System details" };
export const summaryLastRead = (ago: string) => "Summary last read " + ago;
export const refreshFailed = (n: number) => n + (n === 1 ? " section could" : " sections could") + " not be refreshed. Some data may be outdated.";
export const postureIncomplete = (relaxed: number) => (relaxed ? postureBadge(relaxed) + " · Some values unavailable" : "Partially available");
export const COUNT_WORD = { partial: "partial", none: "none" };
export const VIEW_CHECKS = "View checks";
export const WIRING_NOT_REPORTED = "not reported";
export const PERMISSION_REQUIRED = "Permission required";
export const LOADING = "Loading…";
export const lastReadDot = (ago: string) => "Last read " + ago + " · ";
export const lastCheckedDot = (ago: string) => "Last checked " + ago + " · ";
export const CHAIN_VALUE = { intact: "Verified", broken: "Integrity check failed", seat: PERMISSION_REQUIRED, other: "Not verified" };
export const latestRecord = (seq: number) => "Latest record #" + nfmt(seq);

// The inventory strip, the configuration note, the audit line and the
// access guide of the Summary tab.
export const INVENTORY = { servers: "MCP servers running", serversShort: "MCP servers", sessions: "active sessions", users: "active users", policies: "active policies" };
export const SETTING_LABEL = { tls: "Public URL uses HTTP", floorNone: "Client verification is not required", floorAdvisory: "Managed client installation is not required" };
export const CONFIG_NOTE = { denied: "Configuration requires permission", incomplete: "Some configuration values are unavailable", none: "No configuration exceptions reported" };
export const toReview = (n: number) => n + (n === 1 ? " setting to review" : " settings to review");
export const valuesLastRead = (ago: string) => "Last read " + ago + ". Values may be outdated.";
export const REVIEW_SETTINGS = "Review settings";
export const NO_RECORDS_TO_VERIFY = "No audit records to verify";
export const lastChecked = (ago: string) => "Last checked " + ago + ". ";
export const GUIDE = {
  open: "Access setup guide",
  title: "Configure and verify access",
  lede: "Follow the access path from the server connection to the recorded decision.",
  steps: [
    { line: "Inspect the server connection and its credential ownership.", to: "servers" },
    { line: "Give roles access to the intended tools, including tools the server adds later.", to: "roles" },
    { line: "Check assigned and included roles. Change membership in the identity system that owns it.", to: "users" },
    { line: "Review policy conditions and simulate a call from the server page.", to: "policies" },
    { line: "For calls requiring approval, configure approver membership and devices.", to: "approvals" },
    { line: "Make a real call through your agent's harness, then inspect the recorded decision. Simulation alone does not verify a live connection.", to: "audit" },
  ],
} as const;

// The decisions panel, its hourly chart and the sheet of one hour.
export const DECIDED_PANEL = "Tool decisions";
export const LAST_24 = "Last 24 hours";
export const TOTAL_LABEL = { allowed: "Tool calls allowed", denied: "Denied", approval: "Required approval" };
export const CATALOG_APART = "Catalog reads, counted apart";
export const OPEN_AUDIT = "Open audit";
export const UNKNOWN_HOUR = "Unknown hour";
export const HOUR_TITLE = "Hourly tool decisions";
export const hourSpan = (from: string, to: string) => from + " to " + to + " UTC";

// The last hour view of the same panel: the switch, the chart by the minute,
// the sheet of one minute and the sentences that stand in for the chart.
// SO_FAR marks the minute that is still running.
export const WINDOW_LABEL = "Window";
export const LAST_HOUR = "Last hour";
export const LAST_HOUR_HELP = "Tool decisions recorded by the gateway and hooks in the last hour, one bar per minute, counted again every 5 s. A held call counts once as required approval. A catalog read lists the tools a caller may use and runs none, so it is counted apart. Hover or focus a minute for counts. Use arrow keys to move between minutes, or select one for a breakdown.";
export const LAST_HOUR_NONE = "No tool decisions recorded in the last hour.";
export const LAST_HOUR_READING = "Reading the last hour.";
export const LAST_HOUR_UNREAD = "The last hour did not load. The page reads it again every 5 s, and a read that failed is named at the top of the page.";
export const LAST_HOUR_UNSUPPORTED = "This server does not count the last hour by the minute. Update strazad to see the last hour, or go back to Last 24 hours.";
export const SUBJECT_LAST_HOUR = "The last hour of decisions";
export const PER_BUCKET = { hour: "Decisions per hour", minute: "Decisions per minute" };
export const MINUTES_ALT = "Calls decided per minute over the last hour";
export const MINUTE_TITLE = "Tool decisions in one minute";
export const SO_FAR = ", so far";
