// The session half of the REST layer. The session
// token lives in this module's memory and is mirrored to per-tab
// sessionStorage, so a page refresh resumes through checkin instead of
// demanding a new sign-in. Deliberately not localStorage: sessionStorage
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

// TokenAnswer is the token endpoint's answer to a code redemption: an id
// token, else an RFC 6749 error such as invalid_grant.
type TokenAnswer = { id_token?: string; error?: string; error_description?: string };

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
// issuer's authorization and token endpoints through OIDC discovery.
// Standalone servers, and older ones that 404 the document, keep the
// same-origin /oidc paths. Enterprise answers with the external IdP and a
// server-dictated client id, and the IdP's CORS must then allow this origin
// at its token endpoint. An empty client_id means the built-in issuer with
// our own id. Cached per page load; a failed discovery is retried on the
// next sign-in click. The emergency sign-in skips discovery altogether: the
// break-glass admin signs in at the server's own page, which must work when
// the IdP cannot sign anyone in.
type Flow = { authUrl: string; tokenUrl: string; clientId: string; issuer: string };
const BUILTIN_FLOW: Flow = { authUrl: "/oidc/authorize", tokenUrl: "/oidc/token", clientId: "console", issuer: "" };
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

// signInHost names where Sign in sends this page: the external identity
// provider's host, or "" for the server's own sign-in page.
export async function signInHost(): Promise<string> {
  const flow = await loginFlow();
  return flow.issuer ? new URL(flow.issuer).host : "";
}

// IdpUnreachableError is the sign-in refusal when this browser cannot fetch
// the identity provider's discovery document: the name does not resolve
// here, or nothing answers at that address. strazad itself answered.
export class IdpUnreachableError extends Error {
  constructor(issuer: string) {
    super("This browser cannot reach your identity provider at " + issuer + ". Open that address in this browser to check that it answers, then press Sign in again.");
  }
}

async function discoverFlow(): Promise<Flow> {
  let resp: Response;
  try {
    resp = await fetch("/.well-known/straza/idp.json");
  } catch {
    throw new ApiError("unreachable", 0, true);
  }
  if (resp.status === 404) return BUILTIN_FLOW;
  const doc = (await resp.json().catch(() => null)) as { error?: string; issuer?: string; client_id?: string } | null;
  if (!resp.ok) throw new Error((doc && doc.error) || "the server cannot say where to log in (HTTP " + resp.status + ")");
  if (!doc || !doc.issuer || !doc.client_id) return BUILTIN_FLOW; // built-in issuer: same origin, own client id
  const issuer = doc.issuer.replace(/\/+$/, "");
  let disc: { authorization_endpoint?: string; token_endpoint?: string } | null;
  try {
    disc = (await fetch(issuer + "/.well-known/openid-configuration").then((r) => r.json())) as typeof disc;
  } catch {
    throw new IdpUnreachableError(issuer);
  }
  if (!disc || !disc.authorization_endpoint || !disc.token_endpoint) {
    throw new Error("Your identity provider at " + issuer + " does not offer a browser sign-in, because its discovery document names no authorization endpoint. Ask your administrator to check Straza's identity provider settings.");
  }
  return { authUrl: disc.authorization_endpoint, tokenUrl: disc.token_endpoint, clientId: doc.client_id, issuer };
}

// SIGNIN_PATH is the one address the identity provider sends a sign-in back
// to, for the console and the self-service page alike, so an administrator
// registers a single redirect per server.
export const SIGNIN_PATH = "/console/signin";

// A sign-in answer older than this is not used: the person had ten minutes
// at the identity provider.
const PENDING_MS = 10 * 60 * 1000;
const PENDING_KEY = "straza.signin";
const PROBLEM_KEY = "straza.signin.problem";
const RESUME_KEY = "straza.signin.resume";

// Pending is the sign-in this tab started: the PKCE verifier, the state and
// the nonce the answer must match, where to redeem it, where the person
// started, and the browser name an Enable this browser sheet carries over.
type Pending = { state: string; verifier: string; nonce: string; tokenUrl: string; clientId: string; back: string; started: number; sheet?: string };

export const PLAIN_HTTP = "This page is served over plain HTTP from a network address, so the browser will not run the sign-in. Serve Straza over HTTPS, or open it at 127.0.0.1 on the server.";
export const NO_STORAGE = "This browser does not let Straza keep the sign-in in this tab. Allow site data for this address, then press Sign in.";
export const NO_ANSWER = "The sign-in did not come back from your identity provider. If it showed an error about a redirect address, your administrator must add ";
const STALE = "This sign-in answer was not started in this tab, or it is more than 10 minutes old, so Straza did not use it. Press Sign in to start again.";
const USED = "Your identity provider says the sign-in answer expired or was already used. Press Sign in to start again.";
const OUTAGE = "Straza is temporarily unavailable, try again in a moment (the sign-in itself succeeded, nothing on your side needs fixing).";
const DOWN = "strazad is unreachable, state unknown. Check the server, then press Sign in again.";

function redirectURI(): string {
  return window.location.origin + SIGNIN_PATH;
}

// noAnswer is the sentence for a sign-in that left and never came back.
function noAnswer(): string {
  return NO_ANSWER + redirectURI() + " to Straza's registration there. Press Sign in to try again.";
}

function b64url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function randomPart(): string {
  return b64url(crypto.getRandomValues(new Uint8Array(32)));
}

function readJSON<T>(key: string): T | null {
  try {
    const raw = window.sessionStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function writeJSON(key: string, value: unknown | null) {
  try {
    if (value == null) window.sessionStorage.removeItem(key);
    else window.sessionStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Storage is unavailable; the sign-in then cannot finish in this tab.
  }
}

// safeBack keeps a return address on this server's two pages, so a stored
// value can never send the tab elsewhere.
function safeBack(back: string | undefined): string {
  return back && /^\/(console|self-service)\//.test(back) && !back.startsWith(SIGNIN_PATH) ? back : "/console/";
}

// beginSignIn sends this tab to the identity provider with a PKCE code
// request, or to the server's own page on the emergency sign-in. back is
// where the person returns, and sheet is the browser name an Enable this
// browser sheet resumes with. It throws a sentence when the tab cannot
// leave: no Web Crypto on a plain HTTP page, or an unreachable issuer.
export async function beginSignIn({ emergency = false, back = window.location.pathname, sheet }: { emergency?: boolean; back?: string; sheet?: string } = {}): Promise<void> {
  if (!globalThis.crypto || !globalThis.crypto.subtle) throw new Error(PLAIN_HTTP);
  const flow = emergency ? BUILTIN_FLOW : await loginFlow();
  const verifier = randomPart();
  const challenge = b64url(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier))));
  const pending: Pending = { state: randomPart(), verifier, nonce: randomPart(), tokenUrl: flow.tokenUrl, clientId: flow.clientId, back: safeBack(back), started: Date.now(), sheet };
  writeJSON(PENDING_KEY, pending);
  // Without the record the answer cannot be used, so the tab does not leave.
  if (readJSON<Pending>(PENDING_KEY)?.state !== pending.state) throw new Error(NO_STORAGE);
  const q = new URLSearchParams({
    response_type: "code", client_id: flow.clientId, redirect_uri: redirectURI(), scope: "openid profile",
    state: pending.state, nonce: pending.nonce, code_challenge: challenge, code_challenge_method: "S256",
  });
  window.location.assign(flow.authUrl + "?" + q.toString());
}

// Finished is a sign-in's outcome at SIGNIN_PATH: the check-in answer, or
// the sentence that says what failed. back is where the person started.
export type Finished = { resp: CheckinResponse; back: string } | { problem: string; back: string };

let finishRun: { search: string; p: Promise<Finished> } | null = null;

// finishSignIn completes the sign-in whose answer is in search: it takes the
// pending record out first and cleans the address, so a reload cannot use
// the answer twice, then checks the state and the age, redeems the code,
// checks the nonce and checks in. A second call with the same answer, or
// with the empty address the first call left behind, gets the first call's
// outcome, as a development build's second effect run does.
export function finishSignIn(search: string): Promise<Finished> {
  if (!finishRun || (finishRun.search !== search && search !== "")) finishRun = { search, p: runFinish(search) };
  return finishRun.p;
}

async function runFinish(search: string): Promise<Finished> {
  const q = new URLSearchParams(search);
  const pending = readJSON<Pending>(PENDING_KEY);
  writeJSON(PENDING_KEY, null);
  window.history.replaceState(null, "", SIGNIN_PATH);
  const back = safeBack(pending ? pending.back : undefined);
  if (!pending || pending.state !== q.get("state") || Date.now() - pending.started > PENDING_MS) return { problem: STALE, back };
  const said = q.get("error");
  if (said) {
    const desc = q.get("error_description");
    return { problem: "Your identity provider did not sign you in." + (desc ? " It said: " + desc.replace(/\.?$/, ".") : "") + " Press Sign in to try again, or ask your administrator whether your account may use Straza.", back };
  }
  const code = q.get("code");
  if (!code) return { problem: STALE, back };

  let answer: TokenAnswer;
  try {
    answer = await request<TokenAnswer>("POST", pending.tokenUrl, {
      grant_type: "authorization_code", code, redirect_uri: redirectURI(), client_id: pending.clientId, code_verifier: pending.verifier,
    }, { form: true, authed: false, oauth: true });
  } catch (e) {
    const external = /^https?:/.test(pending.tokenUrl);
    if (external && e instanceof ApiError && e.unreachable) {
      return { problem: "Your identity provider at " + new URL(pending.tokenUrl).host + " did not let this page finish the sign-in. Straza's registration there must allow sign-ins from " + window.location.origin + ": a single-page application address on Entra, a web origin on Keycloak, a trusted origin on Okta. Ask your administrator, then press Sign in.", back };
    }
    return { problem: e instanceof ApiError && e.unreachable ? DOWN : "Your identity provider refused to finish the sign-in: " + (e instanceof Error ? e.message : String(e)) + ". Press Sign in to try again.", back };
  }
  if (!answer.id_token) {
    if (answer.error === "invalid_grant") return { problem: USED, back };
    return { problem: "Your identity provider refused to finish the sign-in: " + (answer.error_description || answer.error || "no ID token in the answer") + ". Press Sign in to try again.", back };
  }
  if (nonceOf(answer.id_token) !== pending.nonce) return { problem: STALE, back };

  try {
    const resp = await checkin(answer.id_token);
    if (pending.sheet) writeJSON(RESUME_KEY, pending.sheet);
    return { resp, back };
  } catch (e) {
    const api = e instanceof ApiError ? e : null;
    if (api && (api.unreachable || api.status >= 500)) return { problem: OUTAGE, back };
    const why = (e instanceof Error ? e.message : String(e)).replace(/\.?$/, ".");
    return { problem: "You signed in at your identity provider, but Straza refused the session: " + why, back };
  }
}

// nonceOf reads the nonce claim of an ID token without verifying it: strazad
// verifies the token at check-in, and this only ties the answer to this tab.
function nonceOf(idToken: string): string {
  try {
    const part = idToken.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
    return (JSON.parse(atob(part)) as { nonce?: string }).nonce || "";
  } catch {
    return "";
  }
}

// stashProblem keeps a sign-in failure for the page the person started on,
// which shows it after the hand-on.
export function stashProblem(problem: string) {
  writeJSON(PROBLEM_KEY, problem);
}

// takeSignInProblem returns, once, why the last sign-in of this tab did not
// finish: a failure stashed for this page, or a sign-in that left for the
// identity provider and came back without an answer.
export function takeSignInProblem(): string | null {
  const stashed = readJSON<string>(PROBLEM_KEY);
  if (stashed) {
    writeJSON(PROBLEM_KEY, null);
    return stashed;
  }
  const pending = readJSON<Pending>(PENDING_KEY);
  if (!pending || window.location.pathname === SIGNIN_PATH) return null;
  writeJSON(PENDING_KEY, null);
  return Date.now() - pending.started <= PENDING_MS ? noAnswer() : null;
}

// hasResume reports whether a finished sign-in left an Enable this browser
// sheet to resume, and takeResume returns its browser name, once.
export function hasResume(): boolean {
  return readJSON<string>(RESUME_KEY) != null;
}

export function takeResume(): string | null {
  const name = readJSON<string>(RESUME_KEY);
  writeJSON(RESUME_KEY, null);
  return name;
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
