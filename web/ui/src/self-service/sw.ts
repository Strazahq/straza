// The self-service service worker: the only code of the page that runs
// while the page is closed. It receives Web Push messages, shows the
// notification, and re-registers a subscription the browser rotates. It is
// built on its own as one classic script (vite.sw.config.ts) and served at
// /self-service/sw.js, so its scope is the page's directory and never the
// origin root. What it shares with the page is source the build inlines:
// the IndexedDB record, the push wire shapes and the call summary. The
// server's push envelope is content-free, an opaque reference and a kind,
// so the worker asks the authenticated approver API for the redacted row
// the reference names, best effort and hard-bounded; a push always shows a
// notification, enriched or not, and never renders what arrived.
import { patchRecord, readRecord } from "./idb";
import { registrationBody, sameRegistration, subscribeOptions } from "./pushwire";
import { type SummaryView, summaryText } from "./summary";

type Client = { url?: string; focus?: () => Promise<unknown>; postMessage: (m: unknown) => void };
type Scope = {
  addEventListener: (type: string, fn: (e: never) => void) => void;
  skipWaiting: () => Promise<void>;
  registration: {
    scope?: string;
    showNotification: (title: string, opts: { body: string; tag: string; data: { ref: string } }) => Promise<void>;
    pushManager: { subscribe: (o: unknown) => Promise<PushSubscription> };
  };
  clients: { matchAll: (o: unknown) => Promise<Client[]>; openWindow: (url: string) => Promise<unknown>; claim: () => Promise<void> };
};
type PushEvent = { data?: { json: () => unknown } | null; waitUntil: (p: Promise<unknown>) => void };
type ClickEvent = { notification: { close: () => void }; waitUntil: (p: Promise<unknown>) => void };
type ChangeEvent = { newSubscription?: PushSubscription | null; waitUntil: (p: Promise<unknown>) => void };
type Msg = { ref: string; kind: "decide" | "status" };
type Row = { id?: string; summary?: SummaryView; state?: string; requester?: { username?: string } };

const sw = self as unknown as Scope;

// LOOKUP_MS bounds the enrichment fetch: a browser that waits too long
// shows its own "site updated in the background" instead.
const LOOKUP_MS = 3000;
const PAGE = "/self-service/";
const OLD_PAGE = "/approvals/";

// A fresh worker takes over at once instead of waiting for every tab to
// close; it owns no cache, so nothing breaks in the takeover.
sw.addEventListener("install", () => sw.skipWaiting());
sw.addEventListener("activate", (event: { waitUntil: (p: Promise<unknown>) => void }) => event.waitUntil(sw.clients.claim()));

sw.addEventListener("push", (event: PushEvent) => event.waitUntil(notify(event)));

async function notify(event: PushEvent): Promise<void> {
  const msg = decode(event);
  await pingClients(msg);
  if (!msg) {
    return show("Approval activity", "Something happened in your approvals. Open the self-service page to see it.", "");
  }
  const row = await lookup(msg);
  if (msg.kind === "status") {
    return show("Your request was decided",
      row ? summaryText(row.summary) + ": " + (row.state || "decided") : "Open the self-service page to see the outcome.",
      msg.ref);
  }
  return show("Approval needed",
    row ? summaryText(row.summary) + ", asked by " + requesterOf(row) : "Something needs your decision. Open the self-service page to approve or deny it.",
    msg.ref);
}

// pingClients tells every open page that a push arrived, so an open page
// refetches its lists instead of sitting stale behind the notification.
async function pingClients(msg: Msg | null): Promise<void> {
  try {
    const wins = await sw.clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const w of wins) w.postMessage({ type: "straza-approvals", kind: msg ? msg.kind : "" });
  } catch {
    // the notification below is the fallback
  }
}

// decode reads the only payload the server sends: a reference and a kind.
// Anything else answers null, the generic notification.
function decode(event: PushEvent): Msg | null {
  let raw: unknown = null;
  try {
    raw = event && event.data ? event.data.json() : null;
  } catch {
    return null;
  }
  if (!raw || typeof raw !== "object") return null;
  const o = raw as { ref?: unknown; kind?: unknown };
  const ref = typeof o.ref === "string" ? o.ref : "";
  const kind = o.kind === "decide" || o.kind === "status" ? o.kind : "";
  if (!ref || !kind) return null;
  return { ref, kind };
}

// lookup turns the reference into the redacted row over the approver API.
// An expired token is not refreshed here: the refresh signs with the device
// key, which is the page's lane.
async function lookup(msg: Msg): Promise<Row | null> {
  const rec = await readRecord().catch(() => undefined);
  if (!rec || !rec.deviceToken) return null;
  const scope = msg.kind === "status" ? "mine" : "decidable";
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), LOOKUP_MS);
  try {
    const resp = await fetch("/v1/approver/pending?scope=" + scope, {
      headers: { Authorization: "Bearer " + rec.deviceToken },
      signal: ctrl.signal,
    });
    if (!resp.ok) return null;
    const rows = (await resp.json()) as unknown;
    if (!Array.isArray(rows)) return null;
    return (rows as Row[]).find((r) => r && r.id === msg.ref) || null;
  } catch {
    return null;
  } finally {
    clearTimeout(timer);
  }
}

function requesterOf(row: Row): string {
  return (row && row.requester && row.requester.username) || "someone";
}

// show renders one notification; the tag is the reference, so a nudge about
// the same request replaces the earlier one.
function show(title: string, body: string, ref: string): Promise<void> {
  return sw.registration.showNotification(title, { body, tag: ref || "straza-approvals", data: { ref: ref || "" } });
}

sw.addEventListener("notificationclick", (event: ClickEvent) => {
  event.notification.close();
  event.waitUntil(focusOrOpen());
});

// focusOrOpen brings the page forward if it is open anywhere, the old
// address included, and opens the page otherwise.
async function focusOrOpen(): Promise<unknown> {
  const open = await sw.clients.matchAll({ type: "window", includeUncontrolled: true });
  for (const client of open) {
    const url = typeof client.url === "string" ? client.url : "";
    if (url.indexOf(PAGE) >= 0 || url.indexOf(OLD_PAGE) >= 0) {
      return client.focus ? client.focus() : null;
    }
  }
  return sw.clients.openWindow(PAGE);
}

// pushsubscriptionchange fires when the browser drops or replaces this
// origin's subscription. Best effort: the page runs the same reconciliation
// on every load, so the worst case is notifications resuming at the next
// visit rather than at once.
sw.addEventListener("pushsubscriptionchange", (event: ChangeEvent) => event.waitUntil(resubscribe(event)));

async function resubscribe(event: ChangeEvent): Promise<void> {
  const rec = await readRecord().catch(() => undefined);
  // rec.push proves this browser subscribed on purpose; without it a
  // rotation must not create a registration nobody asked for.
  if (!rec || !rec.deviceToken || !rec.push || !rec.webpush || !rec.webpush.vapid_public_key) return;
  let sub: PushSubscription | null = (event && event.newSubscription) || null;
  if (!sub) {
    sub = await sw.registration.pushManager.subscribe(subscribeOptions(rec.webpush.vapid_public_key)).catch(() => null);
  }
  if (!sub) return;
  let body;
  try {
    body = registrationBody(sub);
  } catch {
    return;
  }
  if (sameRegistration(body, rec.push)) return;
  if (!(await push("PUT", rec.deviceToken, body))) return;
  await patchRecord({ push: body }).catch(() => null);
  if (rec.push.token_or_endpoint !== body.token_or_endpoint) {
    await push("DELETE", rec.deviceToken, rec.push);
  }
}

// push is the worker's own two-line REST call with the stored bearer; every
// non-2xx is the same answer, give up until the page reconciles.
async function push(method: string, token: string, body: unknown): Promise<boolean> {
  try {
    const resp = await fetch("/v1/approver/push", {
      method,
      headers: { "Content-Type": "application/json", Authorization: "Bearer " + token },
      body: JSON.stringify(body),
    });
    return resp.ok;
  } catch {
    return false;
  }
}
