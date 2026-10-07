// The Web Push wire vocabulary, shared by the page and the service worker.
// It imports nothing, on purpose: the worker is a separate bundle with no
// DOM and no page modules, so what both halves need is importable by both
// and free of window assumptions. Two encodings meet here and neither is
// the browser's default: PushManager.subscribe wants the VAPID key as bytes
// while the server advertises it as unpadded base64url, and getKey answers
// ArrayBuffers while PUT /v1/approver/push wants unpadded base64url, which
// internal/approval/webpush.go decodes strictly. Both directions carry
// known-answer tests, because a wrong encoding fails far from here.

// b64urlToBytes decodes unpadded, or padded, base64url into bytes. It
// refuses anything outside the URL-safe alphabet rather than decoding a
// standard-alphabet string into the wrong bytes, which would make a key the
// push service accepts and the server's signature then fails to match.
export function b64urlToBytes(value: string | null | undefined): Uint8Array {
  const s = String(value == null ? "" : value).replace(/=+$/, "");
  if (!/^[A-Za-z0-9_-]*$/.test(s)) {
    throw new Error("value is not unpadded base64url (the URL-safe alphabet, no padding)");
  }
  const rem = s.length % 4;
  if (rem === 1) throw new Error("value is not valid base64url: its last group is a single character");
  const padded = s.replace(/-/g, "+").replace(/_/g, "/") + (rem ? "=".repeat(4 - rem) : "");
  const bin = atob(padded);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// bytesToB64url encodes bytes as unpadded base64url, the only form the
// server's strict decoder takes.
export function bytesToB64url(buf: ArrayBuffer | Uint8Array): string {
  const bytes = buf instanceof Uint8Array ? buf : new Uint8Array(buf);
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode.apply(null, Array.from(bytes.subarray(i, i + 0x8000)));
  }
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// subscribeOptions builds the one options object both halves subscribe
// with. userVisibleOnly is true because it is the only value Chromium
// accepts and because every push this page receives shows a notification.
export function subscribeOptions(vapidPublicKey: string | undefined): { userVisibleOnly: true; applicationServerKey: Uint8Array } {
  const key = String(vapidPublicKey || "");
  if (!key) throw new Error("this deployment did not advertise a VAPID key, so this browser cannot subscribe");
  return { userVisibleOnly: true, applicationServerKey: b64urlToBytes(key) };
}

export type Registration = { kind: "webpush"; token_or_endpoint: string; p256dh: string; auth: string };

// SubscriptionLike is the part of a PushSubscription the body reads.
export type SubscriptionLike = { endpoint?: string; getKey?: (name: "p256dh" | "auth") => ArrayBuffer | null };

// registrationBody is the exact body of PUT and DELETE /v1/approver/push
// for a browser subscription, pinned by api_approver.go. The server requires
// p256dh and auth for the webpush kind, so a subscription that will not
// hand over its keys is refused here rather than registered as a route
// nothing can be encrypted to.
export function registrationBody(sub: SubscriptionLike | null | undefined): Registration {
  const endpoint = sub && sub.endpoint ? String(sub.endpoint) : "";
  if (!endpoint) throw new Error("this browser's push subscription has no endpoint");
  const p256dh = subscriptionKey(sub, "p256dh");
  const auth = subscriptionKey(sub, "auth");
  if (!p256dh || !auth) {
    throw new Error("this browser's push subscription did not expose its encryption keys, so nothing could be sent to it");
  }
  return { kind: "webpush", token_or_endpoint: endpoint, p256dh, auth };
}

function subscriptionKey(sub: SubscriptionLike | null | undefined, name: "p256dh" | "auth"): string {
  if (!sub || typeof sub.getKey !== "function") return "";
  let raw: ArrayBuffer | null;
  try {
    raw = sub.getKey(name);
  } catch {
    return "";
  }
  if (!raw) return "";
  return bytesToB64url(raw);
}

// sameRegistration compares two registration bodies field by field, which
// is how the page notices a subscription the browser rotated behind its back.
export function sameRegistration(a: Registration | null | undefined, b: Registration | null | undefined): boolean {
  return !!a && !!b && a.token_or_endpoint === b.token_or_endpoint && a.p256dh === b.p256dh && a.auth === b.auth;
}
