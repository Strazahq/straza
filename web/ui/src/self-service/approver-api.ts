// The device-token lane of the self-service page: every /v1/approver call
// rides the 30-day device token minted at enrolment, never the login
// session, so an enrolled browser decides without signing in. The lane
// applies the doctrine the phone app follows: a token_expired answer is
// refreshed by signing a fresh server challenge with the device key and
// the call retried once, and a device_revoked answer destroys the local
// credential. The console's api.ts is not reused here because its 401
// handling ends the login session, which this lane does not hold.
import { refreshMessage } from "./sign";

const TIMEOUT_MS = 10000;

// ApproverError carries what the page branches on: the status, the
// server's machine code, and the three classes that end a lane.
export class ApproverError extends Error {
  status: number;
  code: string;
  unreachable: boolean;
  revoked: boolean;
  notEnrolled: boolean;
  data: unknown;
  constructor(message: string, fields: Partial<Pick<ApproverError, "status" | "code" | "unreachable" | "revoked" | "notEnrolled" | "data">> = {}) {
    super(message);
    this.status = fields.status ?? 0;
    this.code = fields.code ?? "";
    this.unreachable = fields.unreachable ?? false;
    this.revoked = fields.revoked ?? false;
    this.notEnrolled = fields.notEnrolled ?? false;
    this.data = fields.data;
  }
}

type Answer = { ok: boolean; status: number; data: unknown; retryAfter: number };

// http is the lane's one fetch wrapper: JSON in and out, a hard timeout so
// a hung server reads as unreachable, and no status interpretation.
async function http(method: string, path: string, body?: unknown, bearer?: string): Promise<Answer> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), TIMEOUT_MS);
  const headers: Record<string, string> = {};
  let payload: string | undefined;
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    payload = JSON.stringify(body);
  }
  if (bearer) headers["Authorization"] = "Bearer " + bearer;
  let resp: Response;
  try {
    resp = await fetch(path, { method, headers, body: payload, signal: ctrl.signal });
  } catch {
    throw new ApproverError("the server is unreachable, so the state of your approvals cannot be known", { unreachable: true });
  } finally {
    clearTimeout(timer);
  }
  let data: unknown = null;
  try {
    data = await resp.json();
  } catch {
    // a non-JSON body: the status carries the outcome
  }
  const retryAfter = Number(resp.headers.get("Retry-After")) || 0;
  return { ok: resp.ok, status: resp.status, data, retryAfter };
}

function field(data: unknown, name: string): string {
  if (data && typeof data === "object" && name in data) {
    const v = (data as Record<string, unknown>)[name];
    return typeof v === "string" ? v : "";
  }
  return "";
}

function failure(r: Answer, fallback?: string): ApproverError {
  return new ApproverError(field(r.data, "error") || fallback || "HTTP " + r.status, { status: r.status, code: field(r.data, "code"), data: r.data });
}

// Device is the bound credential: the id, the live token, a signing
// closure over the non-extractable key, and the two callbacks the refresh
// and revoke doctrine needs. This module never sees key material.
export type Device = {
  id: string;
  token: string;
  sign: (message: string) => Promise<string>;
  onToken: (token: string, expiresIn: number) => Promise<void>;
  onRevoked: () => Promise<void>;
};

let device: Device | null = null;

export function bindDevice(d: Device | null): void {
  device = d;
}

export function boundDevice(): Device | null {
  return device;
}

// refreshToken is the key-signed, bearer-less refresh: an expired token
// must not block its own refresh, so the device signs a fresh challenge.
async function refreshToken(d: Device): Promise<string> {
  const c = await http("POST", "/v1/approver/refresh/challenge", { approver_device_id: d.id });
  const challenge = field(c.data, "challenge");
  if (!c.ok || !challenge) {
    if (c.status === 404) {
      await d.onRevoked();
      throw new ApproverError("this browser's approver device is gone from the server. Enable this browser again to enroll", { revoked: true, status: 404 });
    }
    throw failure(c, "could not start a token refresh");
  }
  const signature = await d.sign(refreshMessage(d.id, challenge));
  const r = await http("POST", "/v1/approver/refresh", { approver_device_id: d.id, challenge, signature });
  if (r.status === 401 && field(r.data, "code") === "device_revoked") {
    await d.onRevoked();
    throw new ApproverError("this browser's approver device was revoked", { revoked: true, code: "device_revoked", status: 401 });
  }
  const token = field(r.data, "device_token");
  if (!r.ok || !token) throw failure(r, "token refresh was refused");
  d.token = token;
  const expiresIn = r.data && typeof r.data === "object" ? Number((r.data as Record<string, unknown>).expires_in) || 0 : 0;
  await d.onToken(token, expiresIn);
  return token;
}

// approver calls one /v1/approver route with the device token: an expired
// token, or one the server cannot verify, is proved again with the device
// key once and the call retried; device_revoked wipes and throws; everything
// else reaches the caller with its status and code. A token the server
// cannot verify is what a browser holds after the server was reset or its
// signing key retired, and the key-signed refresh either mints a fresh token
// or learns that the server no longer knows the device, which wipes it.
export async function approver<T>(method: string, path: string, body?: unknown): Promise<T> {
  const d = device;
  if (!d) throw new ApproverError("this browser is not enrolled to decide", { notEnrolled: true });
  let r = await http(method, path, body, d.token);
  const code = r.status === 401 ? field(r.data, "code") : "";
  if (code === "token_expired" || code === "token_invalid") {
    await refreshToken(d);
    r = await http(method, path, body, d.token);
  }
  if (r.status === 401 && field(r.data, "code") === "device_revoked") {
    await d.onRevoked();
    throw new ApproverError("this browser's approver device was revoked. Enable this browser again to enroll", { revoked: true, code: "device_revoked", status: 401 });
  }
  if (!r.ok) throw failure(r);
  return r.data as T;
}

// EnrollAnswer is the 201 of POST /v1/approver/enroll.
export type EnrollAnswer = {
  approver_device_id: string;
  device_token: string;
  expires_in: number;
  project?: { id?: string; name?: string } | null;
  user?: { id?: string; username?: string } | null;
  webpush?: { vapid_public_key?: string } | null;
};

// enrollDevice registers the generated key. Unauthenticated by contract:
// the one-time enrol token is the credential.
export async function enrollDevice(body: unknown): Promise<EnrollAnswer> {
  const r = await http("POST", "/v1/approver/enroll", body);
  if (!r.ok) throw failure(r, "enrollment was refused");
  return r.data as EnrollAnswer;
}

// selfUnenroll retires this device's own server row, best effort: the
// person's revoke must never be blocked by the network or a dead token, so
// every failure resolves false and the local teardown proceeds. An expired
// token gets the key-signed refresh and one retry, which is why the caller
// runs this before destroying the key. True means the row is gone.
export async function selfUnenroll(): Promise<boolean> {
  const d = device;
  if (!d) return false;
  try {
    let r = await http("DELETE", "/v1/approver/enrollment", undefined, d.token);
    if (r.status === 401 && field(r.data, "code") === "token_expired") {
      await refreshToken(d);
      r = await http("DELETE", "/v1/approver/enrollment", undefined, d.token);
    }
    if (r.status === 401 && field(r.data, "code") === "device_revoked") return true;
    return r.status === 204;
  } catch {
    return false;
  }
}

export type PendingScope = "decidable" | "mine";

export const pending = <T,>(scope: PendingScope) => approver<T[]>("GET", "/v1/approver/pending?scope=" + encodeURIComponent(scope));
export const history = <T,>(cursor: string) =>
  approver<{ items: T[]; next_cursor: string }>("GET", "/v1/approver/history" + (cursor ? "?cursor=" + encodeURIComponent(cursor) : ""));

// DecideBody is the signed decision: the id, the verdict, the challenge the
// row carried, the signature over the canonical message and its clock.
export type DecideBody = { request_id: string; verdict: "approve" | "deny"; challenge: string; signature: string; ts: number; reason?: string };
export const decide = <T,>(body: DecideBody) => approver<T>("POST", "/v1/approver/decide", body);

// The push route is one row per device, kind and endpoint: registering is a
// PUT and removing takes the same body back.
export const pushRegister = (body: unknown) => approver<unknown>("PUT", "/v1/approver/push", body);
export const pushRemove = (body: unknown) => approver<unknown>("DELETE", "/v1/approver/push", body);
