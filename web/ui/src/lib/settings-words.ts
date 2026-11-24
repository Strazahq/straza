// The words of the Attestation registry and the API tokens tabs: captions,
// the job cards, the areas, the sensitive grants, the mint sheet, the
// revoke sentence. Every surface sentence is short; the sentence that
// explains a thing sits behind its help icon.
import type { ApiTokenRow, AttestationHashRow } from "./api";
import { AREAS } from "./role-words";
import { AREA_WORD } from "./say";
import { timeZone } from "./timezone";
import { dayWord, relTimeText } from "./words";

export { AREAS };

// ---- the page ----

// area "full" is a tab only a full administrator sees, because the server
// takes its writes from one only. KEYS_TAB is its label, which the
// Overview's door names too.
export const KEYS_TAB = "Agent signing keys";
export type SettingsTab = "configuration" | "registry" | "tokens" | "keys";
export const TABS: { key: SettingsTab; label: string; area: "config" | "tokens" | "full" }[] = [
  { key: "tokens", label: "API tokens", area: "tokens" },
  { key: "keys", label: KEYS_TAB, area: "full" },
  { key: "registry", label: "Attestation registry", area: "config" },
  { key: "configuration", label: "Configuration", area: "config" },
];
// tabLabel is a tab's label by its key, for the help and the doors that
// name a tab.
export const tabLabel = (key: SettingsTab) => (TABS.find((t) => t.key === key) as (typeof TABS)[number]).label;
export const READ_ONLY = "read-only";
export const hiddenTabsLine = (labels: string[]) => labels.join(" and ") + (labels.length === 1 ? " needs" : " need") + " a role that includes " + AREA_WORD.config + ".";

// ---- the registry ----

export const REGISTRY_NONE_STRIP = "Managed installation is not required.";
export const REGISTRY_NONE_LINE = "Prepare clients with sudo straza install --managed, then set the attestation floor to managed.";
export const REGISTRY_ADVISORY_LINE = "The attestation floor is advisory. Clients must report measurements, but do not need a managed installation. Set governance.minAttestation to managed to require hashes accepted by this registry.";
export const REGISTRY_LEDE_NONE = "Approved hook configurations by harness file. Managed clients report these hashes at check-in.";
export const REGISTRY_LEDE_MANAGED = "Managed clients must report approved hook configuration hashes to check in.";
export const REGISTRY_VERIFY = "Use sha256sum to compare the managed file SHA-256 hash with its registry entry. Use strazactl attestation list to inspect entries and strazactl attestation add to register a custom configuration.";
export const REGISTRY_MEASURED = "A managed install measures itself, its config and its hook wiring at every check-in. Only the hook wiring is registered here: the other two are drift signals on the session, since a binary reporting its own hash proves nothing. The MCP registration file is served and deliberately unmeasured.";
export const HASH_HELP = "The file's SHA-256, as sha256sum prints it.";
export const STATUS_HELP = "current: the hook configuration generated for this harness and platform. allowed: another approved configuration, such as an earlier version or custom installation.";
export const CURRENT = "current";
export const ALLOWED = "allowed";
export const ALLOWED_TIP = "Earlier configuration, still accepted.";
export const DIRTY = "dirty build";
export const DIRTY_TIP = "Registered by a build with uncommitted changes: it matches no commit and should never appear in production. Rebuild clean and re-register.";
export const RETIRE = "Retire this hash";
export const retireTitle = (short: string) => "Retire " + short + "?";
export const retireBody = (n: number) => "A managed client with this hash is refused at its next check-in" + (n > 1 ? ", on all " + n + " platforms" : "") + ".";
export const RETIRE_VERB = "Retire hash";
export const REGISTRY_EMPTY_TITLE = "No hook configurations registered";
export const REGISTRY_EMPTY_BODY = "Straza registers its generated configurations at startup.";
export const rendersWord = (renders: number, platforms: number) => renders + (renders === 1 ? " configuration" : " configurations") + " · " + platforms + (platforms === 1 ? " platform" : " platforms");
export const filePathLine = (path: string) => "Linux file: " + path;

// MANAGED_PATH is the managed file per harness on a Linux box, the path the
// verify caption names beside each band (internal/agentguard/install.go).
export const MANAGED_PATH: Record<string, string> = {
  "hooks.claude-code": "/etc/claude-code/managed-settings.json",
  "hooks.codex": "/etc/codex/requirements.toml",
  "hooks.gemini": "/etc/gemini-cli/settings.json",
};

export const shortHash = (h: string) => (h.length > 20 ? h.slice(0, 12) + "…" + h.slice(-4) : h);

// Render groups the registry rows that share an artifact and a hash: one
// line with every platform the hash covers.
export type Render = { artifact: string; hash: string; harness: string; rows: AttestationHashRow[]; platforms: string[]; current: boolean; created_at?: string; note?: string; dirty: boolean };

// rendersOf folds the flat rows into renders, banded by artifact in first
// seen order, current renders first inside a band.
export function rendersOf(rows: AttestationHashRow[]): Render[] {
  const out: Render[] = [];
  for (const r of rows) {
    const k = out.find((x) => x.artifact === r.artifact && x.hash === r.hash);
    if (k) {
      k.rows.push(r);
      if (r.platform && !k.platforms.includes(r.platform)) k.platforms.push(r.platform);
      k.current = k.current || !!r.current;
      continue;
    }
    out.push({ artifact: r.artifact, hash: r.hash, harness: r.harness || "", rows: [r], platforms: r.platform ? [r.platform] : [], current: !!r.current, created_at: r.created_at, note: r.note, dirty: !!(r.note && r.note.includes("-dirty")) });
  }
  const order = [...new Set(out.map((x) => x.artifact))];
  return out.sort((a, b) => order.indexOf(a.artifact) - order.indexOf(b.artifact) || Number(b.current) - Number(a.current));
}

// ---- tokens ----

export const TOKENS_LEDE = "A token lets a script or your identity manager call Straza's admin API.";
export const GRANTS_HELP = "read covers every GET under the area, write everything else. Amber marks a scope that reaches sensitive content or mints credentials.";
export const LAST_USED_HELP = "Updated at most once an hour.";
export const NEVER_TIP = "No expiry: this credential lives until revoked.";
export const NEVER = "never";
export const byWord = (who: string | undefined) => (who ? "by " + who : "");
export const TOKENS_EMPTY_TITLE = "No API tokens yet";
export const TOKENS_EMPTY_BODY = "Mint one for your identity manager, a pipeline or a SIEM check.";
export const NEW_TOKEN = "Mint a token";

// SENSITIVE names the scopes the table and the sheet mark in amber, with
// the reason on hover.
export const SENSITIVE: Record<string, string> = {
  "transcripts:read": "Sensitive: reads conversation content.",
  "scim:write": "Sensitive: creates and disables users and writes role membership.",
  "tokens:write": "Root-equivalent: mints further admin tokens.",
};
export const FULL_BADGE = "full: every admin route";
export const FULL_TIP = "A root credential. It can mint more tokens.";

export const grantsOf = (scope: string) => (scope === "full" ? [] : scope.split(",").map((g) => g.trim()).filter(Boolean));
export const areaHint = (area: string) => (AREAS.find((a) => a[0] === area) || ["", ""])[1];

// ---- the mint sheet ----

export const MINT_TITLE = "Mint a token";
export const NAME_HINT = "Name who will hold it. It shows in audit records.";
export const nameTaken = (name: string) => "A token named " + name + " exists. Pick another name, or revoke it first.";
export const NAME_MISSING = "Name the token first.";
export const GRANTS_MISSING = "Pick a job, or build the scopes under Custom.";
export const LIFETIME_HINT = "For one call from your terminal, pick 1 day.";
export const NEVER_WARN = "No expiry: this credential lives until revoked. Prefer 90 days and rotate.";
export const LIFETIMES: { seconds: number; label: string }[] = [
  { seconds: 86400, label: "1 day" },
  { seconds: 604800, label: "7 days" },
  { seconds: 2592000, label: "30 days" },
  { seconds: 7776000, label: "90 days" },
  { seconds: 31536000, label: "1 year" },
  { seconds: 0, label: "Never" },
];
export const DEFAULT_LIFETIME = 7776000;
export const JOB_TITLE = "The job";

// Job is one card of the mint sheet: its grants with the reason each one
// is there, or full, or the custom table.
export type Job = { key: string; title: string; blurb: string; why?: Record<string, string>; full?: boolean; custom?: boolean };
export const JOBS: Job[] = [
  { key: "iga", title: "IGA connector", blurb: "midPoint or SailPoint: writes users and role membership over SCIM, reads servers, roles and the change feed.",
    why: { "scim:read": "reads users and membership over SCIM", "scim:write": "provisions users and membership over SCIM", "identity:read": "reads users and roles", "apps:read": "reads the server catalog", "changes:read": "polls the change feed", "config:read": "the connection test reads Overview" } },
  { key: "audit", title: "Audit automation", blurb: "Scripts and SIEM-side checks that read audit records and session state.",
    why: { "audit:read": "reads the audit records", "sessions:read": "correlates live sessions" } },
  { key: "ci", title: "Policy CI pipeline", blurb: "A pipeline that applies and activates policies from a repository.",
    why: { "policy:read": "diffs the stored policies", "policy:write": "applies and activates" } },
  { key: "reviewer", title: "Read-only reviewer", blurb: "An auditor's evidence pass. Includes transcript content.",
    why: { "audit:read": "the audit records", "transcripts:read": "conversation content, sensitive", "sessions:read": "session context" } },
  { key: "root", title: "Full root", blurb: "Every admin route, including minting more tokens.", full: true },
  { key: "custom", title: "Custom", blurb: "Build the scopes by hand from the area table.", custom: true },
];
export const AREAS_TITLE = "Areas";
export const LEVEL_NONE = "none";
export const LEVEL_READ = "read";
export const LEVEL_RW = "read and write";
export const LEVEL_WRITE = "write only";
export const levelHint = (area: string, level: string) => {
  if (level === "read") return "read: every GET under " + area + ".";
  if (level === "rw") return "read and write: GET plus every change under " + area + ".";
  if (level === "write") return "write only: every change under " + area + " and no read. A preset left it behind.";
  return "none: no access to " + area + ".";
};
export const SCOPE_TITLE = "What will be sent";
export const SCOPE_LINE = "The same string strazactl api-token create --scope takes.";
export const ROOT_WARN = "This token can mint further tokens: a root credential. Prefer narrow scopes for connectors.";
export const MINT_VERB = "Mint token";
export const MINTING = "Minting…";

// canonicalScope renders the grants the way the server stores them: sorted
// and deduplicated, or full.
export const canonicalScope = (full: boolean, grants: string[]) => (full ? "full" : [...new Set(grants)].sort().join(","));

// ---- minted, refused, the sheet, revoke ----

export const mintedTitle = (name: string) => name + " is minted";
export const MINTED_LEDE = "Only its hash is stored.";
export const SHOWN_ONCE = "shown once";
export const COPY_NOW = "Copy it now. A lost token cannot be recovered.";
export const COPY = "Copy";
export const COPIED = "Copied";
export const USE_TITLE = "How a caller uses it";
export const useLine = (hasScim: boolean) => "Send it as a bearer token." + (hasScim ? " It also works on SCIM." : "");
export const headerLine = (token: string) => "Authorization: Bearer " + token;
export const curlLine = (origin: string) => 'curl -H "Authorization: Bearer $STRAZA_API_TOKEN" ' + origin + "/v1/admin/users";
export const FROM_SHELL = "From a shell:";
export const HOLDS_TITLE = "What it holds";
export const expiresLine = (rel: string, abs: string) => "Expires " + rel + ", on " + abs + ".";
export const DONE = "Done";
export const GRANTS_TITLE = "Scopes";
export const REVOKE = "Revoke";
export const REVOKE_VERB = "Revoke token";
export const revokeTitle = (name: string) => "Revoke " + name + "?";
export const REVOKE_HELP = "A call in flight finishes. The next one is refused. Mint a replacement first if the caller still needs access.";

// AREA_LOSS names what a caller loses per area, for the revoke sentence.
const AREA_LOSS: Record<string, string> = {
  scim: "the SCIM plane",
  identity: "its reads of users and roles",
  sessions: "its view of sessions",
  policy: "its policy access",
  apps: "its reads of servers",
  audit: "the audit records",
  transcripts: "the transcripts",
  approvals: "its approvals access",
  config: "the configuration read-out",
  tokens: "the power to mint tokens",
  changes: "the change feed",
};

// revokeBody builds the consequence from the row's own grants, never a
// guess about who holds the token.
export function revokeBody(t: Pick<ApiTokenRow, "name" | "scope">): string {
  if (t.scope === "full") return t.name + " loses every admin route within seconds.";
  const areas = [...new Set(grantsOf(t.scope).map((g) => g.split(":")[0]))];
  const words = areas.map((a) => AREA_LOSS[a] || a).filter(Boolean);
  if (!words.length) return t.name + " loses its access within seconds.";
  const list = words.length === 1 ? words[0] : words.slice(0, -1).join(", ") + " and " + words[words.length - 1];
  return t.name + " loses " + list + " within seconds.";
}

// ---- the Settings page and the registry table ----

// hiddenTokensTabLine is the sibling of hiddenTabsLine for the other area:
// the line a session holding config alone reads under the tab strip.
export const hiddenTokensTabLine = (label: string) => label + " needs a role that includes " + AREA_WORD.tokens + ".";
export const SUBJECT_REGISTRY = "The attestation registry";
export const READING_REGISTRY = "Reading the attestation registry.";
export const REGISTRY_COLUMN = { hash: "Hash", platforms: "Platforms", status: "Status", registered: "Registered" };
export const COPY_HASH = "Copy the full hash";
export const HASH_COPIED = "The full hash is copied.";
export const retiredToast = (short: string) => short + " is retired.";

// ---- the API tokens table, a token's sheet, the mint sheet ----

export const SUBJECT_TOKENS = "The API tokens";
export const READING_TOKENS = "Reading the API tokens.";
export const TOKEN_COLUMN = { name: "Token", grants: "Scopes", created: "Created", expires: "Expires", used: "Last used" };
export const revokeName = (name: string) => "Revoke " + name;
export const revokedToast = (name: string) => name + " is revoked.";
export const MINTED_BY = "Minted by";
export const EXPIRED = "expired";

// expiresWord reads an expiry in the future tense, since an expiry that
// has not happened yet must never render as a past event: "in 84 d" for a
// stamp days away, the relative reading under a day, and "expired" once
// the instant has passed. The absolute stamp rides beside it on hover.
export function expiresWord(iso: string | undefined): string {
  if (!iso) return NEVER;
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const left = t - Date.now();
  if (left <= 0) return EXPIRED;
  if (left < 86400000) return relTimeText(iso);
  return "in " + Math.round(left / 86400000) + " d";
}

// ---- the dates of the API tokens table ----

const DAY_MS = 86400000;
// NEAR_EXPIRY_DAYS is how close an expiry turns into a warn chip.
export const NEAR_EXPIRY_DAYS = 14;
export const YESTERDAY = "yesterday";

// dayNumber counts calendar days in the zone the console shows, so two
// stamps on the same day in that zone share a number.
function dayNumber(t: number): number {
  const shift = timeZone() === "utc" ? 0 : new Date(t).getTimezoneOffset() * 60000;
  return Math.floor((t - shift) / DAY_MS);
}

// usedWord reads a last use: relative under a day, yesterday for the
// calendar day before today, and the day past that.
export function usedWord(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const now = Date.now();
  if (now - t < DAY_MS) return relTimeText(iso);
  return dayNumber(t) === dayNumber(now) - 1 ? YESTERDAY : dayWord(iso);
}

// expiryOf reads an expiry in calendar days with the tone it wears: plain
// text while it is far, a warn chip from NEAR_EXPIRY_DAYS on, and a danger
// chip once it has passed.
export function expiryOf(iso: string): { word: string; tone: "plain" | "warn" | "danger" } {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return { word: iso, tone: "plain" };
  const now = Date.now();
  const days = dayNumber(t) - dayNumber(now);
  if (t <= now) return { word: days === 0 ? "expired today" : days === -1 ? "expired " + YESTERDAY : "expired " + -days + " days ago", tone: "danger" };
  const word = days === 0 ? "today" : days === 1 ? "in 1 day" : "in " + days + " days";
  return { word, tone: days > NEAR_EXPIRY_DAYS ? "plain" : "warn" };
}

export const NAME_LABEL = "Name";
export const LIFETIME_LABEL = "Lifetime";
export const AREA_COLUMN = { area: "Area and what it covers", access: "Access" };
export const areaLevelLabel = (area: string) => area + " access";
export const removeGrant = (grant: string) => "Remove " + grant;
export const scopeLine = (scope: string) => "scope: " + scope;
// TOKEN_ELIDED stands in for the value on a token's own sheet, where the
// secret itself is gone: only its hash is stored.
export const TOKEN_ELIDED = "wat_…";

// levelOf reads the level a held grant set gives one area. write is the
// state a preset leaves behind when its read grant is removed.
export const levelOf = (grants: string[], area: string): "none" | "read" | "rw" | "write" => {
  const read = grants.includes(area + ":read");
  const write = grants.includes(area + ":write");
  if (read && write) return "rw";
  if (read) return "read";
  if (write) return "write";
  return "none";
};

// grantsAtLevel rewrites one area's grants to the level picked, leaving
// every other area untouched.
export function grantsAtLevel(grants: string[], area: string, level: string): string[] {
  const kept = grants.filter((g) => g.split(":")[0] !== area);
  if (level === "read") kept.push(area + ":read");
  if (level === "rw") kept.push(area + ":read", area + ":write");
  if (level === "write") kept.push(area + ":write");
  return kept;
}

// NOT_SET is the word a fact reads when the server never recorded it, on
// a token minted before created_by existed.
export const NOT_SET = "not set";
