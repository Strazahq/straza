// The pure readings of an approval record: what the stored
// summary names, whose decision it is, which phase a record is in and the
// clocks the rows show. Every function is a function of the record, the
// seat and the time alone, so the suites pin them without a screen.
import type { ApproverDeviceRow, ApprovalRow } from "./api";

export type Kind = "hold" | "ticket";

// kindOf reads the record's class: a call held in place, or a standing
// approval the text stores as class ticket.
export const kindOf = (r: Pick<ApprovalRow, "class">): Kind => (r.class === "ticket" ? "ticket" : "hold");

export type Call = { where: string; call: string; whereKind: "server" | "shell" | "other" };

// callOf reads the stored summary the way the two lanes write it:
// "mcp.call <app>:<tool>" from the gateway and "shell.exec: <command>" from
// the hook. Anything else is shown as stored, with no where.
export function callOf(summary: string): Call {
  const mcp = /^mcp\.call\s+([^:\s]+):(\S.*)$/.exec(summary || "");
  if (mcp) return { where: mcp[1], call: mcp[2].trim(), whereKind: "server" };
  const shell = /^shell\.exec:?\s+(\S.*)$/.exec(summary || "");
  if (shell) return { where: "shell", call: shell[1].trim(), whereKind: "shell" };
  // Any other hook tool is "<tool>: <paths>" or the bare tool name; the
  // word before the dot is where the call goes.
  const other = /^([a-z]+)\.([a-z_]+)(?::\s+(\S.*))?$/.exec(summary || "");
  if (other) return { where: OTHER_WHERE[other[1]] || other[1], call: other[3] ? other[3].trim() : other[2], whereKind: "other" };
  return { where: "", call: summary || "", whereKind: "other" };
}

const OTHER_WHERE: Record<string, string> = { file: "files", net: "network" };

// Seat is the signed-in person as the check-in reported them: the username
// and the roles they hold.
export type Seat = { user: string; roles: string[] };

export const ADMIN_ROLE = "straza-admin";

// Deciders is the record's decide pool as the rule named it: the users by
// username (today the requester's sponsor), the approver roles, or the
// requester alone on a confirm record. admins is the empty pool, which the
// server lets straza-admin decide.
export type Deciders = { users: string[]; roles: string[]; kind: "sponsor" | "role" | "both" | "requester" | "admins" };

export function decidersOf(r: Pick<ApprovalRow, "mode" | "approverUsers" | "approverRoles" | "username" | "user">): Deciders {
  if (r.mode === "confirm") return { users: [r.username || r.user], roles: [], kind: "requester" };
  const users = r.approverUsers || [];
  const roles = r.approverRoles || [];
  if (users.length && roles.length) return { users, roles, kind: "both" };
  if (users.length) return { users, roles: [], kind: "sponsor" };
  if (roles.length) return { users: [], roles, kind: "role" };
  return { users: [], roles: [ADMIN_ROLE], kind: "admins" };
}

// mine says whether the seat may decide the record, the rule the server
// applies on every decision (internal/approval/decide.go): a confirm record
// is the requester's alone; otherwise the requester may not decide their
// own request unless the rule allows it, the user pool is matched by
// username, then the roles, and a record naming nobody falls to
// straza-admin. Display only: the server answers 403 when the two disagree.
export function mine(r: ApprovalRow, seat: Seat): boolean {
  if (!seat.user) return false;
  if (r.mode === "confirm") return seat.user === r.username;
  if (seat.user === r.username && !r.selfApproval) return false;
  const d = decidersOf(r);
  if (d.users.includes(seat.user)) return true;
  if (d.kind === "sponsor") return false;
  return d.roles.some((x) => seat.roles.includes(x));
}

// own says whether the record is the seat's own request. The server takes
// such a decision only from a device that signs unless
// approval.unsignedOwnDecisions is on (internal/approval/decide.go).
export const own = (r: Pick<ApprovalRow, "username">, seat: Seat): boolean => !!seat.user && seat.user === r.username;

// stuck says whether nobody at all can decide a waiting record: it names
// no user, and every role it names has no holder. holders maps a role name
// to its holder count as the roles list answers it.
export function stuck(r: ApprovalRow, holders: Record<string, number>): boolean {
  if (r.state !== "pending") return false;
  const d = decidersOf(r);
  if (d.kind !== "role") return false;
  return d.roles.every((x) => (holders[x] || 0) === 0);
}

export type Phase = "waiting" | "approved" | "granted" | "used" | "lapsed" | "denied" | "expired";

// phaseOf reads the record's phase. A standing approval that was approved
// is granted while its window is open, used once a call consumed it, and
// lapsed when the window closed unused; the state field never says so.
export function phaseOf(r: ApprovalRow, now = Date.now()): Phase {
  if (r.state === "pending") return "waiting";
  if (r.state === "denied") return "denied";
  if (r.state !== "approved") return "expired";
  if (kindOf(r) === "hold") return "approved";
  if (r.consumedAt) return "used";
  const t = Date.parse(r.grantExpiresAt || "");
  if (!Number.isNaN(t) && t > now) return "granted";
  return "lapsed";
}

const pad = (n: number) => String(n).padStart(2, "0");

// secondsLeft is the whole seconds until iso, 0 once it has passed or when
// there is no stamp.
export function secondsLeft(iso: string | null | undefined, now = Date.now()): number {
  const t = Date.parse(iso || "");
  return Number.isNaN(t) ? 0 : Math.max(0, Math.floor((t - now) / 1000));
}

// holdLeft is the clock of a call held in place: seconds, then minutes and
// seconds, then the word expired.
export function holdLeft(iso: string | null | undefined, now = Date.now()): string {
  const s = secondsLeft(iso, now);
  if (s <= 0) return "expired";
  if (s < 60) return s + " s left";
  return Math.floor(s / 60) + " m " + pad(s % 60) + " s left";
}

// dayLeft is the clock of a standing approval: days and hours, hours and
// minutes, minutes, then the word expired.
export function dayLeft(iso: string | null | undefined, now = Date.now()): string {
  let s = secondsLeft(iso, now);
  if (s <= 0) return "expired";
  const d = Math.floor(s / 86400);
  s -= d * 86400;
  const h = Math.floor(s / 3600);
  s -= h * 3600;
  const m = Math.floor(s / 60);
  if (d > 0) return d + " d " + h + " h left";
  if (h > 0) return h + " h " + pad(m) + " min left";
  if (m > 0) return m + " min left";
  return s + " s left";
}

// timeLeft picks the clock by the record's kind.
export const timeLeft = (r: ApprovalRow, now = Date.now()) => (kindOf(r) === "ticket" ? dayLeft(r.expiresAt, now) : holdLeft(r.expiresAt, now));

// holdShare is the part of a hold's window still to run, in whole percent,
// read from the request and expiry stamps the row carries. It is 0 once the
// window has passed or when a stamp is missing.
export function holdShare(r: Pick<ApprovalRow, "createdAt" | "expiresAt">, now: number): number {
  const end = Date.parse(r.expiresAt);
  const span = end - Date.parse(r.createdAt);
  return span > 0 ? Math.round(100 * Math.min(1, Math.max(0, (end - now) / span))) : 0;
}

// withinWords words the open window of a granted standing approval.
export function withinWords(iso: string | null | undefined, now = Date.now()): string {
  const s = secondsLeft(iso, now);
  if (s <= 0) return "the window closed";
  if (s >= 3600) return "the same call within " + Math.floor(s / 3600) + " h " + pad(Math.floor((s % 3600) / 60)) + " min runs";
  return "the same call within " + Math.max(1, Math.ceil(s / 60)) + " min runs";
}

// durationWords words a number of seconds the way a rule states it: "2
// minutes", "90 seconds", "1 day", "24 hours".
export function durationWords(s: number): string {
  if (!Number.isFinite(s) || s <= 0) return "its window";
  if (s % 86400 === 0) return plural(s / 86400, "day");
  if (s % 3600 === 0) return plural(s / 3600, "hour");
  if (s % 60 === 0) return plural(s / 60, "minute");
  return plural(s, "second");
}

// windowWords words the record's own window, from the request and expiry
// stamps.
export function windowWords(r: Pick<ApprovalRow, "createdAt" | "expiresAt">): string {
  return durationWords(Math.round((Date.parse(r.expiresAt) - Date.parse(r.createdAt)) / 1000));
}

// Param is one row of the parameters table: the name, with nested names
// joined by a dot, and the value as words.
export type Param = { name: string; value: string };

// paramsOf reads the stored preview as a table when it is a JSON object:
// nested objects flatten into dotted names, lists read as comma lists,
// booleans as yes and no. Anything else, a shell command or a bare value,
// answers null and the caller shows the text as it is.
export function paramsOf(preview: string | undefined): Param[] | null {
  if (!preview) return null;
  let obj: unknown;
  try { obj = JSON.parse(preview); } catch { return null; }
  if (!obj || typeof obj !== "object" || Array.isArray(obj)) return null;
  const out: Param[] = [];
  const walk = (o: Record<string, unknown>, prefix: string) => {
    for (const [k, v] of Object.entries(o)) {
      const name = prefix ? prefix + "." + k : k;
      if (v && typeof v === "object" && !Array.isArray(v)) walk(v as Record<string, unknown>, name);
      else out.push({ name, value: wordsOf(v) });
    }
  };
  walk(obj as Record<string, unknown>, "");
  return out;
}

function wordsOf(v: unknown): string {
  if (v === null || v === undefined) return "none";
  if (typeof v === "boolean") return v ? "yes" : "no";
  if (Array.isArray(v)) return v.map((x) => (typeof x === "object" && x !== null ? JSON.stringify(x) : String(x))).join(", ");
  return String(v);
}

const plural = (n: number, unit: string) => n + " " + unit + (n === 1 ? "" : "s");

// shortID is an identifier's first thirteen characters and an ellipsis, the
// sessions list's form.
export const shortID = (id: string | undefined) => (id ? (id.length > 13 ? id.slice(0, 13) + "…" : id) : "");

// STALE_DAYS is how long a device may go unseen before the list marks it.
export const STALE_DAYS = 30;

// DeviceFilter is one chip of the devices strip.
export type DeviceFilter = "all" | "phones" | "browsers" | "mustcheck" | "softphone" | "stale";
export const DEVICE_FILTERS: DeviceFilter[] = ["all", "phones", "browsers", "mustcheck", "softphone", "stale"];

// deviceSeenAt is the stamp the list sorts and ages by: the last contact,
// or the enrolment when the device never came back.
export const deviceSeenAt = (d: ApproverDeviceRow) => Date.parse(d.last_seen || d.enrolled_at) || 0;

// devicePasses says whether a device belongs under a chip.
export function devicePasses(d: ApproverDeviceRow, f: DeviceFilter, now = Date.now()): boolean {
  const phone = d.platform !== "browser";
  switch (f) {
    case "phones": return phone;
    case "browsers": return !phone;
    case "mustcheck": return d.push_routes === 0;
    case "softphone": return phone && (d.key_security_level === "software" || d.attestation === "none" || !d.attestation);
    case "stale": return now - deviceSeenAt(d) >= STALE_DAYS * 86400000;
    default: return true;
  }
}

// deviceNeedsLook counts a device once under any of the three attention
// chips, for the group header.
export const deviceNeedsLook = (d: ApproverDeviceRow, now = Date.now()) => devicePasses(d, "mustcheck", now) || devicePasses(d, "softphone", now) || devicePasses(d, "stale", now);

// deviceMatches says whether a device or its person matches a search.
export function deviceMatches(d: ApproverDeviceRow, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return (d.username || d.user_id || "").toLowerCase().includes(needle) || (d.name || "").toLowerCase().includes(needle);
}
