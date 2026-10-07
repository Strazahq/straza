// The REST layer of the app: same-origin relative paths, bearer header,
// a hard timeout, JSON
// in and out. The bearer comes from the session module, which owns sign-in,
// resume and refresh; a 401 on an authed call hands the session back to it.
import { lose, token } from "./session";

const TIMEOUT_MS = 10000;

export class ApiError extends Error {
  status: number;
  unreachable: boolean;
  // retryAfter is the server's Retry-After in seconds on a 429, else 0.
  retryAfter?: number;
  // body is the parsed JSON of an error answer, so a screen reads a 422 or
  // a 409 that carries detail, such as DraftRefused or DraftConflicts.
  body?: unknown;
  constructor(message: string, status: number, unreachable = false, retryAfter = 0, body?: unknown) {
    super(message);
    this.status = status;
    this.unreachable = unreachable;
    this.retryAfter = retryAfter;
    this.body = body;
  }
}

// RequestOptions widen a call the way the device flow and the app install
// need: form sends the body URL-encoded, yaml sends it as application/yaml
// text, authed false sends no bearer, and oauth returns a 4xx that carries
// an RFC 6749 error body as the answer instead of throwing. timeoutMs
// replaces the hard timeout for a call the server itself bounds longer.
export type RequestOptions = { form?: boolean; yaml?: boolean; authed?: boolean; oauth?: boolean; text?: boolean; timeoutMs?: number };

// retryAfterOf reads a Retry-After header in seconds, or 0.
function retryAfterOf(resp: Response): number {
  const h = resp.headers;
  if (!h || typeof h.get !== "function") return 0;
  return Number(h.get("Retry-After")) || 0;
}

// errorOf reads the server's error sentence from a JSON body, or "".
function errorOf(data: unknown): string {
  if (data && typeof data === "object" && "error" in data && typeof (data as { error: unknown }).error === "string") {
    return (data as { error: string }).error;
  }
  return "";
}

// request wraps fetch: JSON in and out, bearer auth, a hard timeout so a hung
// server renders as unreachable instead of a spinner forever, and a 401 on
// an authed call loses the session before the error reaches the caller.
// text answers the body as text, for a document the server serves as a
// file, such as a role's export.
export async function request<T>(method: string, path: string, body?: unknown, { form = false, yaml = false, authed = true, oauth = false, text = false, timeoutMs = TIMEOUT_MS }: RequestOptions = {}): Promise<T> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  const headers: Record<string, string> = {};
  let payload: string | undefined;
  if (form) {
    headers["Content-Type"] = "application/x-www-form-urlencoded";
    payload = new URLSearchParams(body as Record<string, string>).toString();
  } else if (yaml) {
    headers["Content-Type"] = "application/yaml";
    payload = String(body);
  } else if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  const bearer = token();
  if (authed && bearer) headers["Authorization"] = "Bearer " + bearer;
  let resp: Response;
  try {
    resp = await fetch(path, { method, headers, body: payload, signal: ctrl.signal });
  } catch {
    throw new ApiError("unreachable", 0, true);
  } finally {
    clearTimeout(timer);
  }
  let data: unknown = null;
  if (text && resp.ok) {
    data = await resp.text();
    return data as T;
  }
  try {
    data = await resp.json();
  } catch {
    // A non-JSON body is rare; the status carries the outcome.
  }
  const error = errorOf(data);
  if (oauth && resp.status >= 400 && resp.status < 500 && error) return data as T;
  const message = error || "HTTP " + resp.status;
  if (resp.status === 401 && authed) {
    lose(message);
    throw new ApiError(message, 401);
  }
  if (!resp.ok) throw new ApiError(message, resp.status, false, retryAfterOf(resp), data ?? undefined);
  return data as T;
}

export const get = <T,>(path: string) => request<T>("GET", path);
export const post = <T,>(path: string, body?: unknown) => request<T>("POST", path, body);
export const del = <T,>(path: string) => request<T>("DELETE", path);

const enc = encodeURIComponent;

// The admin API rows the MCP servers area reads and writes. Only the fields
// the screens render are typed; the manifest is the stored app.yaml object
// as the server keeps it (spec/app-manifest v1beta1).
export type Credential = {
  kind?: string;
  agents?: string;
  inject?: { as?: string; name?: string; template?: string };
  oauth?: { provider?: string; scopes?: string[] };
};

export type ManifestDoc = {
  apiVersion?: string;
  kind?: string;
  metadata?: { name?: string; namespace?: string; description?: string };
  server?: Record<string, unknown>;
  straza?: {
    runtime?: {
      kind?: string;
      remote?: { url?: string; auth?: string };
      command?: { exec?: string; args?: string[]; env?: unknown[]; workdir?: string };
      oci?: { image?: string; sandbox?: string; env?: unknown[] };
    };
    credential?: Credential;
    exposure?: { tools?: string[] };
    limits?: { cpu?: string; mem?: string; rps?: number; timeoutSeconds?: number };
  };
};

export type AppRow = {
  id: string;
  name: string;
  version?: string;
  runtime: string;
  status: string;
  detail?: string;
  source?: string;
  tools?: string[];
  reached_by: string[];
  last_probe_at?: string;
  last_healthy_at?: string;
  status_since?: string;
  paused?: boolean;
  url?: string;
  manifest?: ManifestDoc;
  // file is the watched apps directory file that names this server, absent
  // when no present file does. file_differs says live differs from it.
  file?: string;
  file_differs?: boolean;
  // offered is every tool the server itself lists, before the manifest's
  // exposure list, so the exposure picker can offer a tool that is left
  // out today. Absent when the server holds no live instance.
  offered?: string[];
  // 0.104.0: the role that administers this server, and on list rows whether the caller may change it.
  admin_role?: string;
  admin_role_id?: string;
  may_change?: boolean;
};

export type ToolRow = { id: string; app: string; app_id: string; name: string; description?: string };

export type BindingRow = { id: string; app: string; role: string; tools?: string[] };

// SecretRow is one sealed credential row as the secrets list returns it:
// scope app is the server's shared row, scope role an override for one
// role. The value never travels back.
export type SecretRow = { id: string; app?: string; scope: string; role: string; kind?: string; fingerprint: string; set_at?: string };

// RoleRow is one row of GET /v1/admin/roles (openapi 0.101.0). kind is
// business, application, approver or straza. assigned_count counts the
// assignment rows, windows included; holder_count counts the subjects who
// hold the role at read time through any path. implies names the roles a
// role implies directly, areas a straza role's console grants (or the one
// word full), decider_in the active sets naming an approver role as a
// pool; each is absent where it does not apply.
export type RoleRow = {
  id: string;
  name: string;
  kind: string;
  description?: string;
  assigned_count?: number;
  holder_count?: number;
  implies?: string[];
  areas?: string[];
  decider_in?: string[];
  // 0.106.0: server is the name of the MCP server that owns the role, and
  // tools the explicit matchers it reaches there. Both are absent on a role
  // no server owns.
  server?: string;
  tools?: string[];
};

export type ProviderRow = { name: string; scopes?: string[]; redirect_uri?: string };

// AuditRow is one chain record as GET /v1/admin/audit returns it: ce is the
// CloudEvent as a JSON string, parsed by the reader that needs it, and the
// two usernames are read-time enrichments beside the hashed bytes.
export type AuditRow = { seq: number; ce: string; username?: string; decidedByUsername?: string; hash?: string; prevHash?: string };

export type ChangeRow = { cursor: string; type: string; op: string; id: string; at: string };

export type LogEntry = { t: string; line: string };
export type LogsAnswer = { app: string; lines: string[]; entries?: LogEntry[] };

export type PreviewEntry = { app: string; tool: string; status: string; reason: string; ruleId?: string; setName?: string; default?: boolean; hint?: string };
export type PreviewAnswer = { entries: PreviewEntry[]; notes?: string[] };

export type Decision = { effect: string; ruleId?: string; setName?: string; reason?: string; obligations?: string[]; serverCheck?: boolean; classify?: boolean; approve?: unknown; default?: boolean };
export type SimulateRequest = {
  event: { kind: string; tool: string; app?: string; toolName?: string; command?: string; paths?: string[] };
  subject: { user?: string; roles?: string[]; attestation?: string };
  // draft is PolicySet YAML overlaid in place of its same-named stored set,
  // so a page with unpublished edits can ask what they would decide.
  draft?: string;
};
export type SimulateAnswer = { active: Decision; draft?: Decision; subject: { user?: string; roles?: string[] }; snapshot: string };

export type DryRunAnswer = { name: string; runtime: string; credential: string; tools: string[] };
export type ImportAnswer = { manifest: string; name: string; runtime: string };

export const listApps = () => get<AppRow[]>("/v1/admin/apps");
export const listTools = () => get<ToolRow[]>("/v1/admin/tools");
export const listBindings = () => get<BindingRow[]>("/v1/admin/bindings");
export const recheckApp = (id: string) => post<AppRow>("/v1/admin/apps/" + enc(id) + "/health");
export const enableApp = (id: string) => post<AppRow>("/v1/admin/apps/" + enc(id) + "/enable");
export const disableApp = (id: string) => post<AppRow>("/v1/admin/apps/" + enc(id) + "/disable");
export const removeApp = (id: string) => del<{ id: string; status: string }>("/v1/admin/apps/" + enc(id));
export const appLogs = (id: string) => get<LogsAnswer>("/v1/admin/apps/" + enc(id) + "/logs");

// installApp posts the app.yaml text; dryRunApp stops after validation and
// answers what the manifest declares, the check the wizard runs as you type.
export const installApp = (yaml: string) => request<AppRow>("POST", "/v1/admin/apps", yaml, { yaml: true });
export const dryRunApp = (yaml: string) => request<DryRunAnswer>("POST", "/v1/admin/apps?dryRun=1", yaml, { yaml: true });
export const importServerJSON = (record: unknown) => post<ImportAnswer>("/v1/admin/apps/import", record);

export const listSecrets = (id: string) => get<SecretRow[]>("/v1/admin/apps/" + enc(id) + "/secrets");
// setSecret stores the shared row when role is empty, a per-role override
// otherwise. The value travels once, in the body.
export const setSecret = (id: string, value: string, role = "") => post<SecretRow>("/v1/admin/apps/" + enc(id) + "/secrets", role ? { value, role } : { value });
export const removeSecret = (id: string, role = "") => del<unknown>("/v1/admin/apps/" + enc(id) + "/secrets" + (role ? "/" + enc(role) : ""));

export const listRoles = () => get<RoleRow[]>("/v1/admin/roles");
// createRole makes a role; server and tools together make it a role that
// server owns, created with its access row in one request, and they are
// left out of the body for every other role.
export const createRole = (name: string, kind: string, description: string, server?: string, tools?: string[]) =>
  post<RoleRow>("/v1/admin/roles", server ? { name, kind, description, server, tools: tools || [] } : { name, kind, description });
// updateRole changes the description alone: kind is fixed at create.
export const updateRole = (id: string, description: string) => request<RoleRow>("PATCH", "/v1/admin/roles/" + enc(id), { description });
// deleteRole answers sets_off, the live policy sets the delete turned off
// because their match named only this role, to a caller who reads policy
// sets.
export const deleteRole = (id: string) => del<{ status: string; sets_off?: string[] }>("/v1/admin/roles/" + enc(id));
// exportRole answers the server's own role document as text, the file
// strazactl roles export writes.
export const exportRole = (id: string) => request<string>("GET", "/v1/admin/roles/" + enc(id) + "/export", undefined, { text: true });
export const listProviders = () => get<ProviderRow[]>("/v1/admin/oauth/providers");
// catalogPreview asks what a session holding the role gets on one app.
// assume merges hypothetical matchers into the role's grant on that app, so
// a grant that does not exist yet reads truthfully; "*" spells every tool.
export const catalogPreview = (role: string, app?: string, assume?: string[]) => get<PreviewAnswer>("/v1/admin/catalog/preview?role=" + enc(role) + (app ? "&app=" + enc(app) : "") + (app && assume && assume.length ? "&assume=" + enc(assume.join(",")) : ""));
// listAudit takes the query string as the caller builds it (q, type, order,
// after, limit), since the readers differ in what they filter on.
export const listAudit = (query: string) => get<AuditRow[]>("/v1/admin/audit?" + query);
export const listChanges = (types: string, limit: number) => get<{ changes: ChangeRow[] }>("/v1/admin/changes?types=" + enc(types) + "&limit=" + limit);
export const simulate = (body: SimulateRequest) => post<SimulateAnswer>("/v1/admin/policies/simulate", body);
// DecisionBucket is one hour of the decisions block: how many calls the
// engine allowed, held for a person or denied in that hour. catalog is the
// hour's catalog reads, the answers to tools/list and resources/list, which
// allowed leaves out since openapi 0.139.0. An older server sends no
// catalog and counts those reads in allowed.
export type DecisionBucket = { start: string; allowed: number; approval: number; denied: number; catalog?: number };
// StoppedRow is one of the tools stopped most in the window, with the
// reason that fired most often for it. app is "" for a hook-lane tool.
export type StoppedRow = { app: string; tool: string; outcome: "denied" | "approval"; count: number; reason: string };
// DecisionsBlock is the server's count of the last 24 hour-aligned hours
// (openapi 0.102.0): since is the start of the oldest bucket, buckets are
// oldest first and always 24, stopped is at most five rows by count.
// minutes is the last hour, 60 buckets of one minute ending with the current
// one, sent only on a read that asks for the hour window (openapi 0.140.0).
export type DecisionsBlock = { since: string; buckets: DecisionBucket[]; stopped: StoppedRow[]; minutes?: DecisionBucket[] };
export type CountPair = { total?: number; active?: number };
export type AppCounts = { total?: number; running?: number; degraded?: number; pending?: number; stopped?: number; failed?: number };
// OverviewAnswer is GET /v1/admin/overview: the counts, the chain head,
// the push lane and, since 0.102.0, the decisions block. Every field is
// optional because a partial answer must never blank a tile.
export type OverviewAnswer = {
  profile?: string;
  snapshot_id?: string;
  users?: CountPair;
  sessions?: CountPair;
  apps?: AppCounts;
  policies?: CountPair;
  audit?: { head_seq?: number; head_hash?: string };
  denylist?: { entries?: number };
  push?: { lane_up?: boolean; connected?: number };
  decisions?: DecisionsBlock;
};
// overview reads the last 24 hours. window "hour" also asks for the
// decisions block's minutes.
export const overview = (window?: "hour") => get<OverviewAnswer>("/v1/admin/overview" + (window ? "?window=" + window : ""));
// ConfigAnswer is GET /v1/admin/config, the redacted effective
// configuration: values and presence booleans, never a connection string.
export type ConfigAnswer = {
  profile?: string;
  public_url?: string;
  tls?: boolean;
  store_driver?: string;
  events?: { embedded?: boolean };
  oidc?: { external_issuer?: string; jit_provision?: boolean };
  governance?: { min_attestation?: string; offline_grace_ttl_seconds?: number; local_tool_default?: string; audit_backpressure?: string };
  approval?: { gateway_hold_seconds?: number; unsigned_own_decisions?: boolean };
  apps?: { gitops_dir_enabled?: boolean; upstream_timeout_seconds?: number };
  capture?: CaptureConfig;
};
export const getConfig = () => get<ConfigAnswer>("/v1/admin/config");

// ---- the Users, Sessions, Audit and Transcripts areas ----

// query joins parameters into a query string, leaving out the empty ones,
// so a caller names only the filters it sets.
export function query(params: Record<string, string | number | undefined>): string {
  return Object.entries(params)
    .filter(([, v]) => v !== undefined && v !== "")
    .map(([k, v]) => enc(k) + "=" + enc(String(v)))
    .join("&");
}

// Page is the paged lane's envelope (openapi 0.100.0): the rows and the
// cursor of the row the page ends on, "" on the last page. A cursor is
// opaque and continues only the sort and order it was minted under; any
// other pairing answers 400 with the sentence to re-fetch from the start.
export type Page<T> = { items: T[]; next_cursor: string };

export type SortOrder = "asc" | "desc";

export type LockRow = { origin: string; reason: string; created_at: string };

// UserRow is one paged users row (GET /v1/admin/users?limit=): the user
// with the roles the resolver computed, the lock rows the kill-switch lanes
// wrote, the newest session's stamp, and how many agents name the user as
// their sponsor. Sort keys: created, name, status, last_seen. Filters: q,
// status, role, sponsor.
export type UserRow = {
  id: string;
  username: string;
  email?: string;
  display?: string;
  title?: string;
  status: string;
  origin: string;
  kind: string;
  external_id?: string;
  user_type?: string;
  agency_mode?: string;
  sponsor?: string;
  swarm_id?: string;
  ephemeral?: boolean;
  created_at: string;
  updated_at: string;
  effective_roles: string[];
  locks: LockRow[];
  last_seen?: string;
  sponsored_count: number;
};

// UserDetail is GET /v1/admin/users/{id}: the row plus the counts and, on
// an agent, whether an assertion key is registered.
export type UserDetail = UserRow & {
  counts: { sessions: number; active_sessions: number; devices: number; approver_devices: number; approvals: number };
  nhi_key_registered?: boolean;
};

export type DeviceRow = { id: string; user_id: string; name: string; fingerprint: string; platform: string; status: string; enrolled_at: string };

// AssignmentRow is one direct grant. valid_from and valid_to bound it; a
// row outside its window is one the resolver already ignores.
export type AssignmentRow = { id: string; subject_kind: string; subject_id: string; role_id: string; valid_from?: string; valid_to?: string; origin?: string };

export type KeyPosture = { user_id: string; registered: boolean; fingerprint?: string; created?: string };

// SessionRow is one session as GET /v1/admin/sessions answers it. Sort
// keys: started, last_seen, user, status, attestation. Filters: status,
// user (an id).
export type SessionRow = {
  id: string;
  user_id: string;
  username?: string;
  harness: string;
  client_version?: string;
  attestation: string;
  status: string;
  started_at: string;
  last_seen: string;
  wiring_status?: string;
  wiring_hash?: string;
};

export type ConversationRow = { session_id: string; user_id: string; username?: string; turns: number; first_at: string; last_at: string; preview: string };

export type TurnRow = {
  at: string;
  kind: string;
  mode: string;
  content: string;
  truncated?: boolean;
  content_hash: string;
  agent_type?: string;
  body_missing?: boolean;
  session_id?: string;
  user_id?: string;
  username?: string;
};

export type Transcript = { session_id: string; username?: string; turns: TurnRow[] };

export type CaptureConfig = { policy_sets?: number; mode?: string; retention_hours?: number; body_store?: string };

export const listUsers = (q: string) => get<Page<UserRow>>("/v1/admin/users?" + q);
export const getUser = (id: string) => get<UserDetail>("/v1/admin/users/" + enc(id));
export const setUserStatus = (id: string, status: "active" | "disabled") => request<UserRow>("PATCH", "/v1/admin/users/" + enc(id), { status });
export const lockUser = (id: string, reason: string) => post<{ id: string; locked: boolean }>("/v1/admin/users/" + enc(id) + "/lock", { reason });
export const unlockUser = (id: string) => post<{ id: string; locked: boolean }>("/v1/admin/users/" + enc(id) + "/unlock");
export const listDevices = (id: string) => get<DeviceRow[]>("/v1/admin/users/" + enc(id) + "/devices");
export const revokeDevice = (id: string, deviceID: string) => del<{ status: string }>("/v1/admin/users/" + enc(id) + "/devices/" + enc(deviceID));
export const keyPosture = (id: string) => get<KeyPosture>("/v1/admin/users/" + enc(id) + "/nhi-key");
export const setKey = (id: string, publicKey: string) => request<KeyPosture>("PUT", "/v1/admin/users/" + enc(id) + "/nhi-key", { public_key: publicKey });
export const removeKey = (id: string) => del<unknown>("/v1/admin/users/" + enc(id) + "/nhi-key");
// listAssignments reads one user's direct grants, never the whole table.
export const listAssignments = (userID: string) => get<AssignmentRow[]>("/v1/admin/assignments?subject_kind=user&subject_id=" + enc(userID));
export const grantRole = (userID: string, roleID: string) => post<AssignmentRow>("/v1/admin/assignments", { subject_kind: "user", subject_id: userID, role_id: roleID });
export const revokeAssignment = (id: string) => del<unknown>("/v1/admin/assignments/" + enc(id));

export const listSessions = (q: string) => get<Page<SessionRow>>("/v1/admin/sessions?" + q);
export const revokeSession = (id: string) => post<{ status: string }>("/v1/admin/sessions/" + enc(id) + "/revoke");
// revokeSessions stands a named set down in one call and one control event.
export const revokeSessions = (ids: string[]) => post<{ revoked: number }>("/v1/admin/sessions/revoke", { sessions: ids });
export const sessionTranscript = (id: string) => get<Transcript>("/v1/admin/sessions/" + enc(id) + "/transcript");

export const listConversations = (limit: number) => get<ConversationRow[]>("/v1/admin/transcripts?limit=" + limit);
// searchTurns takes q (substring) or hash (sha256:<hex> of the exact
// content), plus user and limit, as the caller builds them.
export const searchTurns = (q: string) => get<TurnRow[]>("/v1/admin/transcripts/search?" + q);
export const captureConfig = () => get<{ capture?: CaptureConfig }>("/v1/admin/config");

// ---- the Roles area ----

// ImplicationRow is one edge a role holds: id is the implied role's id,
// the value the delete path takes.
export type ImplicationRow = { id: string; implies_id: string; implies_name: string };

export type PackBindingRow = { id: string; role_id: string; role_name?: string };
export type PackRow = { id: string; name: string; version?: string; content?: string; bindings?: PackBindingRow[] };

// PolicySummary is the server's decomposition of a stored set, from the
// same parse validate uses; absent when the stored text no longer parses.
// postures counts rules by what they do (deny, allow, confirm, hold,
// ticket, serverCheck, classify); lanes counts them per governed surface
// (mcp, shell, files, net, other), a lane present only when a rule governs
// it; capture is the recording mode when the set records.
export type PolicySummary = {
  name?: string;
  description?: string;
  priority?: number;
  rules?: number;
  postures?: Record<string, number>;
  matchRoles?: string[];
  matchOther?: boolean;
  capture?: string;
  lanes?: Record<string, Record<string, number>>;
};
// PolicySetRow is one row of the policies list envelope: summary only, no
// yaml. drift is present and true only on an active set whose stored text
// differs from what runs: saved edits are not live until the next publish.
export type PolicySetRow = {
  id?: string;
  name: string;
  status: string;
  priority?: number;
  updated_at?: string;
  drift?: boolean;
  summary?: PolicySummary;
};
// PoliciesAnswer is the list envelope; roles is the per-role facet over
// every parseable match, present when the list is asked with limit=0.
export type PoliciesAnswer = { items: PolicySetRow[]; total?: number; roles?: { role: string; sets: number; postures?: Record<string, number> }[] };

// PolicyDoc is one stored set with its text, from GET by name and the
// apply answer.
export type PolicyDoc = PolicySetRow & { yaml?: string };

// ValidateAnswer is what validate says about a document that parses:
// advisories are the typed twin of warnings, keyed by code (for example
// events-never-fire) with the rule they concern. A document that does not
// parse answers 400 with the server's sentence.
export type ValidateAnswer = {
  ok?: boolean;
  name?: string;
  rules?: number;
  priority?: number;
  matchRoles?: string[];
  capture?: string;
  warnings?: string[];
  advisories?: { code: string; severity?: string; rule?: string; text: string }[];
};

// EventSupport is the harness matrix for policy events: which mapped
// harness emits which canonical event, static per build.
export type EventSupport = { events: { kind: string; blocking?: boolean; harnesses?: string[] }[]; harnesses?: string[] };

export const listImplications = (roleID: string) => get<ImplicationRow[]>("/v1/admin/roles/" + enc(roleID) + "/implications");
export const addImplication = (roleID: string, impliesRoleID: string) => post<{ role_id: string; implies_role_id: string }>("/v1/admin/roles/" + enc(roleID) + "/implications", { implies_role_id: impliesRoleID });
export const removeImplication = (roleID: string, impliesRoleID: string) => del<{ status: string }>("/v1/admin/roles/" + enc(roleID) + "/implications/" + enc(impliesRoleID));

// createBinding gives a role access to an app: tools are the matchers the
// row stores, an empty list meaning every tool, which the server spells *.
export const createBinding = (appID: string, role: string, tools: string[]) => post<BindingRow>("/v1/admin/apps/" + enc(appID) + "/bindings", { role, tools });
export const removeBinding = (id: string) => del<{ status: string }>("/v1/admin/bindings/" + enc(id));

// listPolicies takes the query as the caller builds it (role, status,
// limit); limit=0 answers every match.
export const listPolicies = (q: string) => get<PoliciesAnswer>("/v1/admin/policies?" + q);
export const getPolicy = (name: string) => get<PolicyDoc>("/v1/admin/policies/" + enc(name));
// applyPolicy stores a set from its yaml; it stays a draft until activated,
// or replaces the live version once activated again.
export const applyPolicy = (yaml: string) => request<PolicyDoc>("PUT", "/v1/admin/policies", yaml, { yaml: true });
export const activatePolicy = (name: string) => post<unknown>("/v1/admin/policies/" + enc(name) + "/activate", { status: "active" });
// deactivatePolicy turns a live set into a draft: it stops governing at the
// next snapshot and stays stored.
export const deactivatePolicy = (name: string) => post<unknown>("/v1/admin/policies/" + enc(name) + "/activate", { status: "draft" });
// deletePolicy removes a stored set; the server refuses a live one.
export const deletePolicy = (name: string) => del<{ status: string }>("/v1/admin/policies/" + enc(name));
// validatePolicy runs the server's parser and gates over the text without
// storing anything; the same check activation runs.
export const validatePolicy = (yaml: string) => request<ValidateAnswer>("POST", "/v1/admin/policies/validate", yaml, { yaml: true });
export const eventSupport = () => get<EventSupport>("/v1/admin/policies/event-support");

// ---- the Overview and Settings areas ----

// ApprovalRow is one held request as GET /v1/admin/approvals answers it
// (openapi Approval). Overview reads only the pending ones for the
// attention list and the tile; the Approvals area reads every field. class
// is absent on a call held in place and "ticket" on a standing approval;
// the grant and consumed fields ride a standing approval alone; the five
// preview fields travel together; decidedReason and decidedDeviceId are the
// decider's own words and the device that signed, when there was one.
export type ApprovalRow = {
  id: string;
  state: string;
  createdAt: string;
  expiresAt: string;
  decidedAt?: string | null;
  user: string;
  username: string;
  session: string;
  rule: string;
  set: string;
  lane: string;
  summary: string;
  justification: string;
  approverRoles: string[];
  approverUsers?: string[];
  selfApproval: boolean;
  mode: string;
  decidedBy: string;
  decidedByName: string;
  channel?: string;
  decidedReason?: string;
  decidedDeviceId?: string;
  class?: string;
  grantExpiresAt?: string | null;
  consumedAt?: string | null;
  consumedBy?: string;
  argsPreview?: string;
  argsTruncated?: boolean;
  argsBytes?: number;
  argvHashPrefix?: string;
  bindingScope?: string;
};
// listApprovals answers the unpaged list of one state, pending by default.
export const listApprovals = (state = "pending") => get<{ approvals: ApprovalRow[] }>("/v1/admin/approvals?state=" + enc(state));

// ---- the Approvals area ----

// pageApprovals reads one page of the approvals list; q carries state, limit
// and cursor. The list has one order, newest first, and takes no sort.
export const pageApprovals = (q: string) => get<Page<ApprovalRow>>("/v1/admin/approvals?" + q);
export const getApproval = (id: string) => get<{ approval: ApprovalRow }>("/v1/admin/approvals/" + enc(id));
// decideApproval approves or denies one record; the reason travels only
// when the person typed one, so the body stays absent as it always was.
export const decideApproval = (id: string, verdict: "approve" | "deny", reason: string) =>
  post<{ approval: ApprovalRow }>("/v1/admin/approvals/" + enc(id) + "/" + verdict, reason ? { reason } : undefined);

// ApproverDeviceRow is one enrolled approver device, a phone or a browser,
// as GET /v1/admin/approvers answers it. push_routes 0 means no push
// notification reaches it.
export type ApproverDeviceRow = {
  id: string;
  user_id: string;
  username?: string;
  name: string;
  platform: string;
  key_security_level: string;
  attestation: string;
  enrolled_at: string;
  last_seen?: string | null;
  push_routes: number;
};
export const listApproverDevices = () => get<ApproverDeviceRow[]>("/v1/admin/approvers");
export const revokeApproverDevice = (id: string) => del<{ status: string }>("/v1/admin/approvers/" + enc(id));

// EnrollTokenAnswer is the mint's envelope (openapi ApproverEnrollTokenResponse):
// the one-time code, its life, the servers the phone tries, the pin when
// strazad terminates the phone's connection itself, and qr_payload, the
// exact string the QR encodes.
export type EnrollTokenAnswer = {
  enroll_token: string;
  expires_in: number;
  user: { id: string; username: string };
  servers: string[];
  tls_spki_pin?: string;
  qr_payload?: string;
};
export const mintEnrollToken = (userID: string) => post<EnrollTokenAnswer>("/v1/admin/approvers/enroll-token", { user_id: userID });

// ChannelRow is one notification channel's status (openapi
// ApprovalChannelStatus); devices and registrations ride the push row.
export type ChannelRow = {
  name: string;
  configured: boolean;
  detail?: string;
  devices?: number;
  registrations?: number;
  last_delivery?: { at: string; ok: boolean; note?: string };
};
export const listChannels = () => get<{ channels: ChannelRow[] }>("/v1/admin/approvals/channels");
export type ChannelTestReport = { channel: string; targets: { target: string; ok: boolean; error?: string }[] };
export const testChannel = (name: string) => post<ChannelTestReport>("/v1/admin/approvals/channels/" + enc(name) + "/test", {});

// SinkRow is one configured sink with its delivery counters; parked is the
// number of events the sink refused and holds for a replay.
export type SinkStream = { stream: string; pending: number; inflight: number; delivered: number; duplicates: number; parked: number; last_error?: string; last_error_at?: string };
export type SinkRow = { name: string; type: string; target: string; batch: number; subjects: string[]; parked: number; streams: SinkStream[] };
export const listSinks = () => get<SinkRow[]>("/v1/admin/sinks");
export const replaySink = (name: string) => post<unknown>("/v1/admin/sinks/" + enc(name) + "/replay");

// AttestationHashRow is one registry row. current (0.102.0) is true when
// the hash is the render this server publishes now for the artifact.
export type AttestationHashRow = { id: string; artifact: string; harness?: string; platform?: string; hash: string; note?: string; created_at?: string; current?: boolean };
export const listAttestationHashes = () => get<AttestationHashRow[]>("/v1/admin/attestation-hashes");
export const deleteAttestationHash = (id: string) => del<unknown>("/v1/admin/attestation-hashes/" + enc(id));

// ApiTokenRow is one admin API token's metadata; the value itself exists
// only in the mint answer. created_by (0.102.0) names who minted it and is
// absent on rows minted before it was recorded.
export type ApiTokenRow = { id: string; name: string; scope: string; created: string; expires?: string; lastUsed?: string; created_by?: string };
export type MintedToken = { id: string; name: string; scope: string; token: string; expires?: string };
export const listApiTokens = () => get<ApiTokenRow[]>("/v1/admin/api-tokens");
// createApiToken mints one; expiresIn is seconds and 0 means never, which
// the request spells by leaving the field out.
export const createApiToken = (name: string, scope: string, expiresIn: number) => post<MintedToken>("/v1/admin/api-tokens", expiresIn > 0 ? { name, scope, expires_in: expiresIn } : { name, scope });
export const revokeApiToken = (id: string) => del<unknown>("/v1/admin/api-tokens/" + enc(id));

export const listPacks = () => get<PackRow[]>("/v1/admin/packs");
export const bindPack = (packID: string, roleID: string) => post<unknown>("/v1/admin/packs/" + enc(packID) + "/bindings", { role_id: roleID });
export const unbindPack = (packID: string, roleID: string) => del<{ status: string }>("/v1/admin/packs/" + enc(packID) + "/bindings/" + enc(roleID));

// ---- the self-service page: the login-session lane ----

// SelfAnswer is GET /v1/self: who is signed in and what the account may
// enrol, computed by the same server code that gates the mint, plus the
// agents the person sponsors.
export type SelfAnswer = { username: string; user_kind: string; admin_grants?: string; enroll_channels: string[]; sponsored: string[] };
export const readSelf = () => get<SelfAnswer>("/v1/self");

// ServerRow is one row of GET /v1/self/servers: a server the person's roles
// reach, with the state of the credential it uses for them and never a value.
export type ServerRow = {
  app: string;
  runtime: string;
  kind: string;
  provider?: string;
  agents?: string;
  reached: boolean;
  connected?: boolean;
  fingerprint?: string;
  expires_at?: string;
  set_by?: string;
  allow_agents?: boolean;
  updated_at?: string;
  scopes?: string[];
};
const userQuery = (user: string) => (user ? "?user=" + enc(user) : "");
export const selfServers = (user = "") => get<ServerRow[]>("/v1/self/servers" + userQuery(user));

// ConnectAnswer is what a stored token or a switch change answers: the
// fingerprint, the expiry, who set it and the agents opt-in, never the value.
export type ConnectAnswer = { fingerprint?: string; expires_at?: string; set_by?: string; allow_agents?: boolean };
// connectToken stores a pasted token for the person or a sponsored agent;
// the value travels once, in this body, and no answer echoes it.
export function connectToken(app: string, token: string, expiresAt: string, user = "") {
  const body: Record<string, unknown> = { token };
  if (expiresAt) body.expires_at = expiresAt;
  if (user) body.user = user;
  return post<ConnectAnswer>("/v1/connect/" + enc(app), body);
}
// connectStart begins a provider sign-in for the person: the address for a
// new tab and the seconds the sign-in may take.
export const connectStart = (app: string) => post<{ authorize_url: string; expires_in: number }>("/v1/connect/" + enc(app));
// connectFinish hands the provider's code and the state of the sign-in to
// strazad with the person's session, which stores the sign-in only when the
// state names that person.
export const connectFinish = (code: string, state: string) =>
  post<{ app: string; provider?: string; allow_agents?: boolean }>("/v1/connect/callback", { code, state });
// connectAgents records the person's opt-in for their sponsored agents.
export function connectAgents(app: string, allow: boolean, user = "") {
  const body: Record<string, unknown> = { allow_agents: allow };
  if (user) body.user = user;
  return request<ConnectAnswer>("PATCH", "/v1/connect/" + enc(app), body);
}
export const connectDelete = (app: string, user = "") => del<unknown>("/v1/connect/" + enc(app) + userQuery(user));

// SelfEnrollToken is the mint of this person's own one-time enrol token, for
// this browser or for a phone: the console's envelope plus the project; 429
// is the per-user cooldown, carried as retryAfter.
export type SelfEnrollToken = {
  enroll_token: string;
  expires_in: number;
  user?: { id?: string; username?: string };
  servers?: string[];
  tls_spki_pin?: string;
  qr_payload?: string;
  project?: { id?: string; name?: string };
};
export const selfEnrollToken = (channel: "browser" | "mobile" = "browser") => post<SelfEnrollToken>("/v1/approvals/self/enroll-token", { channel });

// ---- config drafts (contract section 15) ----
// The call wrappers live in drafts-api.ts, which only the lazy drafts
// screens import, so the entry page carries the types alone.

// DraftKind, DraftOp, DraftState and DraftDoor spell the drafts wire
// (openapi Draft, DraftItem).
export type DraftKind = "App" | "Role" | "PolicySet";
export type DraftOp = "put" | "off" | "remove";
export type DraftState = "open" | "published" | "discarded" | "expired";
export type DraftDoor = "console" | "strazactl" | "straza-app" | "apps-directory" | "api";

// DraftItem is one object a draft writes: doc is its canonical document and
// base its fingerprint at the draft's last check, empty when it was absent.
// base comes only to a reader who may read the object, existed to every
// reader, and withheld says why doc and base are left out.
export type DraftItem = { kind: DraftKind; name: string; op: DraftOp; doc?: string; base?: string; existed: boolean; withheld?: string };

// DraftPrincipal is who wrote a revision or decided a draft. agent marks a
// user who is not a person; sponsor names an agent author's sponsor.
export type DraftPrincipal = {
  user_id: string;
  username: string;
  agent: boolean;
  via: "login" | "session" | "api-token" | "file" | "upgrade";
  client: string;
  sponsor_id?: string;
  sponsor?: string;
};

// Draft is one draft. title is the server's, never the note; note is the
// proposer's own words and renders only under the unverified note heading.
export type Draft = {
  id: string;
  revision: number;
  state: DraftState;
  door: DraftDoor;
  source?: string;
  note?: string;
  authors: DraftPrincipal[];
  items: DraftItem[];
  reverts?: string;
  title: string;
  working?: boolean;
  policy_edit?: string;
  refusal?: string;
  created_at: string;
  updated_at: string;
  expires_at?: string;
  decided_at?: string;
  decided_by?: DraftPrincipal;
  decided_reason?: string;
  snapshot?: string;
};

// DraftFinding is one line of a verdict. A code the console does not know
// renders by its class, never as nothing. key is the server's; a client
// echoes it in a publish and never computes it.
export type FindingClass = "refused" | "risk" | "warning" | "unchecked" | "passed" | "info";
export type DraftFinding = {
  code: string;
  class: FindingClass;
  ack?: "tick" | "typed";
  object?: string;
  sentence: string;
  fix?: string;
  before?: string;
  after?: string;
  typed?: string;
  key: string;
};

// DraftGain is one row of who gains what. holders comes only to root and to
// a caller holding identity:read; everyone reads holders_count.
export type GainOutcome = "runs" | "needs-approval" | "denied" | "not-reachable" | "unknown";
export type DraftGain = {
  role: string;
  server: string;
  tool: string;
  holders?: string[];
  holders_count: number;
  before: GainOutcome;
  after: GainOutcome;
  before_words?: string;
  after_words?: string;
};

export type DraftNeed = { object: string; standing: string };

// DraftVerdict is the server's reading of one revision, computed on every
// read and again at publish; risk_digest travels back in the publish.
export type DraftVerdict = {
  draft: string;
  revision: number;
  snapshot: string;
  checked_at: string;
  refused: DraftFinding[];
  risks: DraftFinding[];
  warnings: DraftFinding[];
  unchecked: DraftFinding[];
  passed: DraftFinding[];
  info: DraftFinding[];
  gains: DraftGain[];
  needs: DraftNeed[];
  risk_digest: string;
};

// DraftSummary is one row of the drafts queue. checks holds the counts the
// server stored with its last check of an open draft, absent on a decided
// draft and before the first check. A page of them is Page<DraftSummary>.
export type DraftSummary = {
  id: string;
  title: string;
  state: DraftState;
  door: DraftDoor;
  source?: string;
  revision: number;
  proposer: DraftPrincipal;
  items: { kind: DraftKind; name: string; op: DraftOp }[];
  checks?: { refused: number; risks: number; warnings: number; unchecked: number; revision: number; checked_at: string };
  working?: boolean;
  policy_edit?: string;
  created_at: string;
  updated_at: string;
  decided_at?: string;
  decided_by?: DraftPrincipal;
};

export type DraftRevision = { revision: number; author: DraftPrincipal; door: DraftDoor; digest: string; mechanical?: boolean; created_at: string };

// DraftChange is the published before and after of one object; implied
// marks one a removal took along.
export type DraftChange = {
  kind: DraftKind;
  name: string;
  implied: boolean;
  before_op: DraftOp;
  before_doc: string;
  before_fp: string;
  after_op: DraftOp;
  after_doc: string;
  after_fp: string;
};

// DraftDetail is GET /v1/admin/drafts/{id}. live and contacted are keyed
// by Kind/Name; may_publish is display only, publish checks again.
export type DraftDetail = {
  draft: Draft;
  verdict: DraftVerdict;
  revisions: DraftRevision[];
  live: Record<string, { op: DraftOp; doc?: string }>;
  contacted?: Record<string, { at: string; tools: string[] }>;
  changes?: DraftChange[];
  // checks holds, on a draft that is not open, the counts of the check the
  // server stored for its revision, whose lines the verdict does not hold.
  // It is absent on an open draft and when no check of the revision was
  // stored.
  checks?: DraftSummary["checks"];
  may_publish: boolean;
  publish_refusal?: string;
};

export type DraftAnswer = { draft: Draft; verdict: DraftVerdict };
export type DraftCheckAnswer = { items: DraftItem[]; verdict: DraftVerdict };
// DraftItemIn is an item as a body sends it: the server stamps the base and
// reads existed from live state, so a body carries neither.
export type DraftItemIn = Pick<DraftItem, "kind" | "name" | "op" | "doc">;
export type DraftBody = { documents?: string[]; items?: DraftItemIn[]; note?: string; working?: boolean };
export type DraftUpdateBody = { revision: number; documents?: string[]; items?: DraftItemIn[]; note?: string };
export type DraftRebaseBody = { revision: number; picks?: Record<string, "draft" | "live"> };
export type DraftPublishBody = { revision: number; risk_digest: string; ticked: string[]; typed: Record<string, string> };
export type DraftPublished = {
  draft: Draft;
  snapshot: string;
  servers: { name: string; change: "created" | "changed" | "removed"; status: string; detail?: string }[];
  next: string[];
};
export type DraftContacted = {
  object: string;
  host: string;
  contacted_at: string;
  server?: { name?: string; version?: string };
  tools: { name: string; description?: string; read_only?: boolean }[];
};

// DraftRefused is the body of a 422 or 409 that carries detail, and code
// names a refusal a client acts on, such as second_person;
// DraftConflicts the body of a rebase that needs picks.
export type DraftRefused = { error: string; code?: string; findings?: DraftFinding[]; verdict?: DraftVerdict };
export type DraftConflict = { object: string; field: string; base: string; draft: string; live: string };
export type DraftConflicts = { error: string; conflicts?: DraftConflict[] };
