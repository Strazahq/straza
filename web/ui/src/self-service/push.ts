// The page half of the browser push lane. A browser lets a site ask for
// notification
// permission once in any meaningful sense: a declined prompt is remembered,
// often forever, and no code can re-open it. So the consent is asked in the
// page first, where there is room to say what arrives and what does not, and
// only then is the browser prompted. Every failure leaves nothing dangling: a
// declined prompt registers nothing, a refused registration unsubscribes the
// subscription it just created, and a device this browser no longer trusts
// unsubscribes before its record is destroyed. A push route the server keeps
// but the browser cannot receive is worse than none, because it looks like it
// works.
import { pushRegister, pushRemove } from "./approver-api";
import { type Registration, registrationBody, sameRegistration, subscribeOptions } from "./pushwire";
import { type Enrollment, type StorageOutlook, clearPush, savePush } from "./store";
import * as W from "./words";

// READY_MS bounds the wait for the worker to activate, so a browser that
// never activates it gives an honest error instead of a button that spins.
const READY_MS = 10000;

// SW_URL is relative, so the worker is /self-service/sw.js and its scope is
// the page's own directory. OLD_SCOPE is the address the replaced approvals
// page registered a worker at.
const SW_URL = "sw.js";
const OLD_SCOPE = "/approvals/";

// Support says whether this browser can be told about approvals at all, and
// when it cannot, why, in one line the person can act on.
export type Support = { available: boolean; why: string };

// support asks the four questions in the order that matters: the
// deployment's answer first, since no VAPID advert means the server cannot
// send at all, then the platform, then this window, then the browser's own
// permission state.
export function support(rec: Enrollment | null, outlook: StorageOutlook): Support {
  if (!rec || !rec.webpush || !rec.webpush.vapid_public_key) return { available: false, why: W.PUSH_OFF_DEPLOYMENT };
  if (typeof navigator === "undefined" || !navigator.serviceWorker || typeof PushManager === "undefined" || typeof Notification === "undefined") {
    return { available: false, why: W.PUSH_OFF_PLATFORM };
  }
  if (outlook === "ephemeral") return { available: false, why: W.PUSH_OFF_PRIVATE };
  if (Notification.permission === "denied") return { available: false, why: W.PUSH_OFF_BLOCKED };
  return { available: true, why: "" };
}

// enable runs everything after the in-page consent: the browser's prompt,
// the worker, the subscription, the registration. A prompt the person
// declined answers null, because saying no is an outcome and not a failure;
// a server that refuses the registration throws, with the subscription it
// would have left behind already gone.
export async function enable(rec: Enrollment): Promise<Registration | null> {
  const permission = await Notification.requestPermission();
  if (permission !== "granted") return null;

  await navigator.serviceWorker.register(SW_URL);
  const reg = await bounded(navigator.serviceWorker.ready, READY_MS, W.WORKER_SLOW);

  const sub = await reuseOrSubscribe(reg, rec.webpush ? rec.webpush.vapid_public_key : "");
  const body = registrationBody(sub);
  try {
    await pushRegister(body);
  } catch (e) {
    await unsubscribeQuietly(sub);
    throw e;
  }
  rec.push = body;
  await savePush(body);
  return body;
}

// disable removes the server route first, the thing that actually causes a
// notification, then unsubscribes locally, so a person who turns this off
// while the server is unreachable still stops receiving: an unsubscribed
// endpoint answers the sender 404 or 410 and the server prunes the row on
// that. The worker registration stays. It holds no credential, it cannot
// receive a push without a subscription, and keeping it makes turning
// notifications back on instant. The answer is the server's own sentence
// when it could not be told, since "off here, maybe not yet off there" is a
// different sentence from "off".
export async function disable(rec: Enrollment): Promise<string> {
  let problem = "";
  const stored = rec.push;
  if (stored) {
    try {
      await pushRemove(stored);
    } catch (e) {
      problem = messageOf(e);
    }
  }
  await unsubscribeQuietly(await currentSubscription());
  rec.push = null;
  await clearPush().catch(() => null);
  return problem;
}

// teardown is the wipe path: a device the server revoked, or the person's
// own revoke of this browser. It makes no server call on purpose. A revoked
// device token cannot authenticate one, and a revoked browser is destroying
// the credential that would. What matters is that the browser stops holding
// a subscription for a device that no longer exists.
export async function teardown(): Promise<void> {
  await unsubscribeQuietly(await currentSubscription());
}

// sync reconciles what this browser holds with what the server was told, on
// every load of an enrolled page that claims notifications are on. Browsers
// rotate push subscriptions on their own schedule and the
// pushsubscriptionchange event needs the worker to be alive to hear it, so
// the page re-checks rather than assuming. on=false means the control reads
// as off with the reason, never as on while nothing can arrive.
export async function sync(rec: Enrollment, outlook: StorageOutlook): Promise<{ on: boolean; why: string }> {
  const stored = rec.push;
  if (!stored) return { on: false, why: "" };

  const can = support(rec, outlook);
  if (!can.available) {
    // The ground moved under a stored subscription: notifications blocked in
    // site settings, or the platform gone. The local claim goes and so does
    // the server's route, because "this browser will not show notifications
    // any more" and "keep sending to it" cannot both be true.
    rec.push = null;
    await clearPush().catch(() => null);
    await pushRemove(stored).catch(() => null);
    return { on: false, why: can.why };
  }

  try {
    const reg = (await navigator.serviceWorker.getRegistration(SW_URL)) || (await navigator.serviceWorker.register(SW_URL));
    let sub = await reg.pushManager.getSubscription();
    if (!sub) sub = await reuseOrSubscribe(reg, rec.webpush ? rec.webpush.vapid_public_key : "");
    const body = registrationBody(sub);
    if (sameRegistration(body, stored)) return { on: true, why: "" };

    // Rotated: register the new route before dropping the old row, so there
    // is never a window with no route at all.
    await pushRegister(body);
    rec.push = body;
    await savePush(body);
    if (stored.token_or_endpoint !== body.token_or_endpoint) await pushRemove(stored).catch(() => null);
    return { on: true, why: "" };
  } catch (e) {
    rec.push = null;
    await clearPush().catch(() => null);
    return { on: false, why: W.pushLost(messageOf(e)) };
  }
}

// retireOldWorker unregisters the worker the replaced approvals page left
// behind. Its scope is the old address, so a notification it shows opens a
// page that is no longer served.
export async function retireOldWorker(): Promise<void> {
  if (typeof navigator === "undefined" || !navigator.serviceWorker || !navigator.serviceWorker.getRegistrations) return;
  try {
    for (const reg of await navigator.serviceWorker.getRegistrations()) {
      if (reg.scope && reg.scope.endsWith(OLD_SCOPE)) await reg.unregister();
    }
  } catch {
    // Nothing to retire, or the browser refuses to say: either way the new
    // worker owns this page's scope.
  }
}

// reuseOrSubscribe prefers the subscription this browser already holds. A
// second subscribe with a different applicationServerKey throws
// InvalidStateError and browsers keep a subscription across reloads, so
// blindly subscribing is the common way this lane breaks. The one case where
// the old one is useless is a deployment that rotated its VAPID key, since
// the push service would refuse the new sender's signature.
async function reuseOrSubscribe(reg: ServiceWorkerRegistration, vapidPublicKey: string | undefined): Promise<PushSubscription> {
  const opts = subscribeOptions(vapidPublicKey);
  const existing = await reg.pushManager.getSubscription();
  if (existing) {
    if (sameApplicationServerKey(existing, opts.applicationServerKey)) return existing;
    await unsubscribeQuietly(existing);
  }
  // The DOM types want a view over a plain ArrayBuffer, which the shared
  // wire module's bytes do not promise, so the key is copied into one.
  return reg.pushManager.subscribe({ userVisibleOnly: opts.userVisibleOnly, applicationServerKey: bytes(opts.applicationServerKey) });
}

// bytes copies a wire key into a buffer the Push API's own types accept.
function bytes(key: Uint8Array): ArrayBuffer {
  const out = new ArrayBuffer(key.byteLength);
  new Uint8Array(out).set(key);
  return out;
}

function sameApplicationServerKey(sub: PushSubscription, want: Uint8Array): boolean {
  const got = sub.options && sub.options.applicationServerKey;
  if (!got) return true; // the browser will not say: keep what exists rather than churn
  const have = new Uint8Array(got);
  if (have.length !== want.length) return false;
  return have.every((b, i) => b === want[i]);
}

async function currentSubscription(): Promise<PushSubscription | null> {
  if (typeof navigator === "undefined" || !navigator.serviceWorker) return null;
  try {
    const reg = await navigator.serviceWorker.getRegistration(SW_URL);
    return reg ? await reg.pushManager.getSubscription() : null;
  } catch {
    return null;
  }
}

async function unsubscribeQuietly(sub: PushSubscription | null): Promise<void> {
  if (!sub || typeof sub.unsubscribe !== "function") return;
  try {
    await sub.unsubscribe();
  } catch {
    // already gone, or the browser refuses: nothing left to do
  }
}

// messageOf reads a thrown value's sentence, whatever it is.
function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

function bounded<T>(p: Promise<T>, ms: number, message: string): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = setTimeout(() => reject(new Error(message)), ms);
    p.then((v) => { clearTimeout(t); resolve(v); }, (e) => { clearTimeout(t); reject(e); });
  });
}
