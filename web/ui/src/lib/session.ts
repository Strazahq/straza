// The session half of the REST layer. The session
// token lives in this module's memory and is mirrored to per-tab
// sessionStorage, so a page refresh resumes through checkin instead of
// demanding a new device flow. Deliberately not localStorage: sessionStorage
// dies with the tab, and the token it holds is the short-lived session
// token, checked against the denylist at every resume, never a long-lived
// credential.
import { ApiError, post, request } from "./api";

// CheckinResponse is what POST /v1/checkin answers on every lane: a fresh
// id token, a stored session token on resume, or the live one on refresh.
export type CheckinResponse = {
  session_token: string;
  user: string;
  roles?: string[];
  admin_grants?: string;
  admin_servers?: number;
  expires_in?: number;
  session_id?: string;
};

// DeviceGrant is the RFC 8628 device authorization response.
export type DeviceGrant = {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  interval?: number;
  expires_in?: number;
};

// TokenAnswer is the token endpoint's answer while polling: an id token
// once authorized, else an RFC 6749 error such as authorization_pending.
export type TokenAnswer = { id_token?: string; error?: string };

// SessionView is the signed-in session as the app may show it: never the
// token itself.
export type SessionView = { user: string; roles: string[]; grants: string; expiresIn: number; sessionID: string | null };

// Stored is the per-tab mirror's shape. admin_grants is "" when the user has
// none, and absent means "". admin_servers is absent when the session
// administers no MCP server, the way the check-in answer omits it.
type Stored = { token?: string; user?: string; admin_grants?: string; admin_servers?: number };

// HARNESS names the surface a check-in comes from, so the sessions list
// tells a console sign-in from a self-service one; the self-service entry
// sets its own name before its first call.
let HARNESS = { name: "console", version: "1" };
const ATTESTATION = { managed: false, hashes: {} };

// setHarness names this entry page's surface for every check-in it makes.
export function setHarness(name: string) {
  HARNESS = { name, version: "1" };
}
// The per-tab key is shared with the self-service page: one JSON object
// {token, user, admin_grants, admin_servers} written and read by both entry
// pages, so a sign-in on one resumes on the other.
const STORE_KEY = "straza.session";

const auth = {
  token: null as string | null,
  user: null as string | null,
  roles: [] as string[],
  grants: "", // admin_grants from checkin: "full" or "area:verb,..."; "" is no admin standing
  servers: 0, // admin_servers from checkin: how many MCP servers this session administers
  expiresIn: 300,
  sessionID: null as string | null,
};

let onLost: ((reason: string) => void) | null = null;

// Storage failures (privacy modes, quota) degrade to the old behaviour, a
// refresh means signing in again, never to a broken console.
function saveStored() {
  try {
    // Field order token, user, admin_grants, admin_servers is part of the
    // cross-bundle pin, and the count is left out when it is zero, the way
    // the check-in answer leaves it out.
    // Roles are not persisted: resume re-learns them from the checkin response.
    window.sessionStorage.setItem(STORE_KEY, JSON.stringify({ token: auth.token, user: auth.user, admin_grants: auth.grants || "", admin_servers: auth.servers || undefined }));
  } catch {
    // Resume is unavailable in this tab.
  }
}

function clearStored() {
  try {
    window.sessionStorage.removeItem(STORE_KEY);
  } catch {
    // Nothing to clear.
  }
}

function loadStored(): Stored | null {
  try {
    const raw = window.sessionStorage.getItem(STORE_KEY);
    return raw ? (JSON.parse(raw) as Stored) : null;
  } catch {
    return null;
  }
}

// remember records a checkin response as the live session and mirrors it.
function remember(resp: CheckinResponse) {
  auth.token = resp.session_token;
  auth.user = resp.user;
  auth.roles = resp.roles || [];
  auth.grants = resp.admin_grants || "";
  auth.servers = resp.admin_servers || 0;
  auth.expiresIn = resp.expires_in || 300;
  auth.sessionID = resp.session_id || null;
  saveStored();
}

// token is the bearer for the REST layer, or null when the tab has no session.
export function token(): string | null {
  return auth.token;
}

// snapshot is the signed-in session as the app may show it, or null.
export function snapshot(): SessionView | null {
  if (auth.token == null) return null;
  return { user: auth.user || "", roles: auth.roles, grants: auth.grants, expiresIn: auth.expiresIn, sessionID: auth.sessionID };
}

// adminAreas answers which admin areas this session may see: null means all
// (a full grant), else an object keyed by area name derived from the grant
// list. Display only: the server re-checks every request; this only keeps
// the app from showing screens that would 403.
export function adminAreas(): Record<string, true> | null {
  if (auth.grants === "full") return null;
  const areas: Record<string, true> = {};
  for (const g of auth.grants.split(",")) {
    const a = g.split(":")[0];
    if (a) areas[a] = true;
  }
  return areas;
}

// adminServers counts the MCP servers whose admin role this session holds,
// as the check-in answer reported it. Display only, like adminAreas: the
// server answers the MCP servers list with the servers this session may see
// and refuses the rest, whatever the console shows.
export function adminServers(): number {
  return auth.servers;
}

// onAuthLost registers the one subscriber told when the session is lost,
// with the server's sentence, and returns the unsubscribe.
export function onAuthLost(cb: (reason: string) => void): () => void {
  onLost = cb;
  return () => {
    if (onLost === cb) onLost = null;
  };
}

// lose drops the session, token first, then tells the subscriber why when
// there was a session to lose.
export function lose(reason: string) {
  const had = auth.token != null;
  auth.token = null;
  auth.user = null;
  auth.roles = [];
  auth.grants = "";
  auth.servers = 0;
  auth.sessionID = null;
  clearStored();
  if (had && onLost) onLost(reason);
}

// logout drops the session in this tab without touching the server.
export function logout() {
  lose("signed out");
}

// revokeSelf ends the session the current token names: POST /v1/session/revoke
// is idempotent and echoes the revoked session id. It never clears local
// state; callers pair it with logout and word the notice by what happened.
export const revokeSelf = () => post<{ session_id?: string }>("/v1/session/revoke");

// Login discovery asks strazad which issuer to sign in at, then resolves that
// issuer's device-flow endpoints through OIDC discovery. Standalone servers,
// and older ones that 404 the document, keep the same-origin /oidc paths.
// Enterprise answers with the external IdP and a server-dictated client id,
// and the IdP's CORS must then allow this origin. An empty client_id means
// the built-in issuer with our own id. Cached per page load; a failed
// discovery is retried on the next sign-in click. The emergency sign-in
// skips discovery altogether: the break-glass admin signs in at the
// server's own page, which must work when the IdP cannot sign anyone in.
type Flow = { deviceAuthUrl: string; tokenUrl: string; clientId: string };
const BUILTIN_FLOW: Flow = { deviceAuthUrl: "/oidc/device_authorization", tokenUrl: "/oidc/token", clientId: "console" };
let flowPromise: Promise<Flow> | null = null;

function loginFlow(): Promise<Flow> {
  if (!flowPromise) {
    flowPromise = discoverFlow().catch((e) => {
      flowPromise = null;
      throw e;
    });
  }
  return flowPromise;
}

// IdpUnreachableError is the sign-in refusal when this browser cannot fetch
// the identity provider's discovery document: the name does not resolve
// here, or nothing answers at that address. strazad itself answered.
export class IdpUnreachableError extends Error {
  constructor(issuer: string) {
    super("This browser cannot reach your identity provider at " + issuer + ". Open that address in this browser to check that it answers, then press Start again.");
  }
}

async function discoverFlow(): Promise<Flow> {
  let resp: Response;
  try {
    resp = await fetch("/.well-known/straza/idp.json");
  } catch {
    return BUILTIN_FLOW; // unreachable: deviceStart surfaces it on the real endpoint
  }
  if (resp.status === 404) return BUILTIN_FLOW;
  const doc = (await resp.json().catch(() => null)) as { error?: string; issuer?: string; client_id?: string } | null;
  if (!resp.ok) throw new Error((doc && doc.error) || "the server cannot say where to log in (HTTP " + resp.status + ")");
  if (!doc || !doc.issuer || !doc.client_id) return BUILTIN_FLOW; // built-in issuer: same origin, own client id
  const issuer = doc.issuer.replace(/\/+$/, "");
  let disc: { device_authorization_endpoint?: string; token_endpoint?: string } | null;
  try {
    disc = (await fetch(issuer + "/.well-known/openid-configuration").then((r) => r.json())) as typeof disc;
  } catch {
    throw new IdpUnreachableError(issuer);
  }
  if (!disc || !disc.device_authorization_endpoint || !disc.token_endpoint) {
    throw new Error("identity provider " + issuer + " does not advertise the device authorization grant");
  }
  return { deviceAuthUrl: disc.device_authorization_endpoint, tokenUrl: disc.token_endpoint, clientId: doc.client_id };
}

// deviceStart requests a device code from the discovered issuer, or from
// the server's own issuer on the emergency sign-in.
export async function deviceStart(emergency = false): Promise<DeviceGrant> {
  const flow = emergency ? BUILTIN_FLOW : await loginFlow();
  return request<DeviceGrant>("POST", flow.deviceAuthUrl, { client_id: flow.clientId, scope: "openid" }, { form: true, authed: false });
}

// devicePoll asks the token endpoint once for the device code's outcome.
export async function devicePoll(deviceCode: string, emergency = false): Promise<TokenAnswer> {
  const flow = emergency ? BUILTIN_FLOW : await loginFlow();
  return request<TokenAnswer>("POST", flow.tokenUrl, {
    grant_type: "urn:ietf:params:oauth:grant-type:device_code",
    device_code: deviceCode,
    client_id: flow.clientId,
  }, { form: true, authed: false, oauth: true });
}

// checkin exchanges the id token for a session token and records the session
// in memory. Callers gate on admin_grants, and on admin_servers, before
// proceeding: either one is admin standing in the console.
export async function checkin(idToken: string): Promise<CheckinResponse> {
  const resp = await request<CheckinResponse>("POST", "/v1/checkin", { id_token: idToken, harness: HARNESS, attestation: ATTESTATION }, { authed: false });
  remember(resp);
  return resp;
}

// resume re-establishes the session from the per-tab stored token after a
// page refresh. It resolves the checkin response, or null when nothing is
// stored or the token is refused. A definitive refusal clears storage, so
// the next render is a clean sign-in card. An unreachable server throws
// instead: the stored token may still be valid, and discarding it would turn
// a server blip into a forced re-login.
export async function resume(): Promise<CheckinResponse | null> {
  const s = loadStored();
  if (!s || !s.token) return null;
  try {
    const resp = await request<CheckinResponse>("POST", "/v1/checkin", { session_token: s.token, harness: HARNESS, attestation: ATTESTATION }, { authed: false });
    remember(resp);
    return resp;
  } catch (e) {
    if (e instanceof ApiError && e.unreachable) throw e;
    clearStored();
    return null;
  }
}

// refresh re-checks in with the current session token. A refusal (401 or
// 403: revoked, expired, user disabled) loses the session; other errors
// throw for the caller to retry.
export async function refresh(): Promise<CheckinResponse> {
  try {
    const resp = await request<CheckinResponse>("POST", "/v1/checkin", { session_token: auth.token, harness: HARNESS, attestation: ATTESTATION }, { authed: false });
    remember(resp);
    return resp;
  } catch (e) {
    if (e instanceof ApiError && (e.status === 401 || e.status === 403)) lose(e.message || "session no longer active");
    throw e;
  }
}
