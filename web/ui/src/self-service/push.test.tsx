import { beforeEach, describe, expect, it, vi } from "vitest";
import { pushRegister, pushRemove } from "./approver-api";
import { disable, enable, retireOldWorker, support, sync, teardown } from "./push";
import { clearPush, savePush } from "./store";
import type { Enrollment, PushRegistration } from "./store";
import * as W from "./words";

// The two server calls and the two record writes are the whole seam of this
// module; the wire shapes it builds stay real, because their exact bytes are
// what the server pins.
vi.mock("./approver-api", async (orig) => ({
  ...(await orig<typeof import("./approver-api")>()),
  pushRegister: vi.fn(),
  pushRemove: vi.fn(),
}));
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  savePush: vi.fn(async () => undefined),
  clearPush: vi.fn(async () => undefined),
}));

// The advert and the subscription keys as the wire carries them: unpadded
// base64url of a 65-octet point, a 65-octet point and a 16-octet secret.
const VAPID = "BFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFxcXFw";
const P256DH = "BBERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERERE";
const AUTH = "IiIiIiIiIiIiIiIiIiIiIg";
const VAPID_BYTES = new Uint8Array([4, ...new Array(64).fill(0x5c)]);

const wire = (endpoint: string): PushRegistration => ({ kind: "webpush", token_or_endpoint: endpoint, p256dh: P256DH, auth: AUTH });

// A fake push platform: every call it takes is recorded in order, since the
// order is the contract this lane is built on.
type Sub = {
  endpoint: string;
  options: { applicationServerKey: ArrayBuffer | null };
  getKey: (name: "p256dh" | "auth") => ArrayBuffer;
  unsubscribe: () => Promise<boolean>;
};

const env = {
  order: [] as string[],
  grant: "granted" as NotificationPermission,
  permission: "default" as NotificationPermission,
  sub: null as Sub | null,
  unsubscribed: [] as string[],
  registered: [] as string[],
  lastKey: null as ArrayBuffer | null,
  others: [] as { scope: string; unregister: () => Promise<boolean> }[],
};

function bytesOf(b64: string): ArrayBuffer {
  const bin = atob(b64.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (b64.length % 4)) % 4));
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

function subscription(endpoint: string, key: ArrayBuffer | null): Sub {
  return {
    endpoint,
    options: { applicationServerKey: key },
    getKey: (name) => bytesOf(name === "p256dh" ? P256DH : AUTH),
    unsubscribe: async () => { env.order.push("unsubscribe"); env.unsubscribed.push(endpoint); env.sub = null; return true; },
  };
}

const registration = {
  scope: "https://app.example/self-service/",
  unregister: async () => { env.order.push("unregister"); return true; },
  pushManager: {
    getSubscription: async () => { env.order.push("getSubscription"); return env.sub; },
    subscribe: async (o: { applicationServerKey: ArrayBuffer }) => {
      env.order.push("subscribe");
      env.lastKey = o.applicationServerKey;
      env.sub = subscription("https://push.example/wp/sub-new", o.applicationServerKey);
      return env.sub;
    },
  },
};

function platform(on: boolean) {
  if (!on) {
    Object.defineProperty(navigator, "serviceWorker", { configurable: true, value: undefined });
    Reflect.deleteProperty(globalThis, "PushManager");
    Reflect.deleteProperty(globalThis, "Notification");
    return;
  }
  Object.defineProperty(navigator, "serviceWorker", {
    configurable: true,
    value: {
      register: async (url: string) => { env.order.push("register"); env.registered.push(url); return registration; },
      get ready() { return Promise.resolve(registration); },
      getRegistration: async () => registration,
      getRegistrations: async () => [registration, ...env.others],
    },
  });
  Object.defineProperty(globalThis, "PushManager", { configurable: true, value: function PushManagerStub() {} });
  Object.defineProperty(globalThis, "Notification", {
    configurable: true,
    value: {
      get permission() { return env.permission; },
      requestPermission: async () => { env.order.push("requestPermission"); env.permission = env.grant; return env.grant; },
    },
  });
}

function record(over: Partial<Enrollment> = {}): Enrollment {
  return {
    deviceId: "apd_browser", deviceToken: "dt-live", tokenExpiresAt: Date.now() + 86400000,
    project: null, user: { id: "u-alice", username: "alice" },
    webpush: { vapid_public_key: VAPID }, keys: {} as CryptoKeyPair,
    deviceName: "Firefox on Windows", enrolledAt: new Date().toISOString(),
    ...over,
  };
}

describe("the browser push lane", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    env.order = [];
    env.grant = "granted";
    env.permission = "default";
    env.sub = null;
    env.unsubscribed = [];
    env.registered = [];
    env.lastKey = null;
    env.others = [];
    platform(true);
    vi.mocked(pushRegister).mockResolvedValue(undefined);
    vi.mocked(pushRemove).mockResolvedValue(undefined);
  });

  it("says in one line why it cannot be offered, deployment first", () => {
    expect(support(record({ webpush: null }), "durable")).toEqual({ available: false, why: W.PUSH_OFF_DEPLOYMENT });
    expect(support(record(), "ephemeral")).toEqual({ available: false, why: W.PUSH_OFF_PRIVATE });
    env.permission = "denied";
    expect(support(record(), "durable")).toEqual({ available: false, why: W.PUSH_OFF_BLOCKED });
    env.permission = "default";
    expect(support(record(), "durable")).toEqual({ available: true, why: "" });
    platform(false);
    expect(support(record(), "durable")).toEqual({ available: false, why: W.PUSH_OFF_PLATFORM });
  });

  it("prompts, registers the worker, subscribes, then registers the route once", async () => {
    const rec = record();
    const body = await enable(rec);

    expect(env.order).toEqual(["requestPermission", "register", "getSubscription", "subscribe"]);
    expect(env.registered).toEqual(["sw.js"]);
    expect(new Uint8Array(env.lastKey as ArrayBuffer)).toEqual(VAPID_BYTES);
    expect(vi.mocked(pushRegister).mock.calls).toEqual([[wire("https://push.example/wp/sub-new")]]);
    expect(body).toEqual(wire("https://push.example/wp/sub-new"));
    expect(rec.push).toEqual(wire("https://push.example/wp/sub-new"));
    expect(vi.mocked(savePush)).toHaveBeenCalledWith(wire("https://push.example/wp/sub-new"));
  });

  it("registers nothing when the browser prompt is declined", async () => {
    env.grant = "denied";
    const rec = record();

    expect(await enable(rec)).toBe(null);
    expect(env.order).toEqual(["requestPermission"]);
    expect(vi.mocked(pushRegister)).not.toHaveBeenCalled();
    expect(env.sub).toBe(null);
    expect(rec.push).toBe(undefined);
  });

  it("leaves no subscription behind when the server refuses the registration", async () => {
    vi.mocked(pushRegister).mockRejectedValue(new Error("push endpoint host push.example is not on approval.push.allowedPushHosts"));
    const rec = record();

    await expect(enable(rec)).rejects.toThrow("allowedPushHosts");
    expect(env.unsubscribed).toEqual(["https://push.example/wp/sub-new"]);
    expect(env.sub).toBe(null);
    expect(vi.mocked(savePush)).not.toHaveBeenCalled();
  });

  it("removes the route, unsubscribes and keeps the worker when turned off", async () => {
    const endpoint = "https://push.example/wp/sub-live";
    env.sub = subscription(endpoint, bytesOf(VAPID));
    const rec = record({ push: wire(endpoint) });

    expect(await disable(rec)).toBe("");
    expect(vi.mocked(pushRemove).mock.calls).toEqual([[wire(endpoint)]]);
    expect(env.unsubscribed).toEqual([endpoint]);
    expect(env.order).not.toContain("unregister");
    expect(rec.push).toBe(null);
    expect(vi.mocked(clearPush)).toHaveBeenCalled();
  });

  it("still stops receiving when the server cannot be told, and says so", async () => {
    const endpoint = "https://push.example/wp/sub-live";
    env.sub = subscription(endpoint, bytesOf(VAPID));
    vi.mocked(pushRemove).mockRejectedValue(new Error("strazad is unreachable"));

    expect(await disable(record({ push: wire(endpoint) }))).toBe("strazad is unreachable");
    expect(env.unsubscribed).toEqual([endpoint]);
  });

  it("re-registers a rotated subscription exactly once and drops the old row", async () => {
    const rotated = "https://push.example/wp/sub-rotated";
    env.sub = subscription(rotated, bytesOf(VAPID));
    const rec = record({ push: wire("https://push.example/wp/sub-old") });

    expect(await sync(rec, "durable")).toEqual({ on: true, why: "" });
    expect(vi.mocked(pushRegister).mock.calls).toEqual([[wire(rotated)]]);
    expect(vi.mocked(pushRemove).mock.calls).toEqual([[wire("https://push.example/wp/sub-old")]]);
    expect(rec.push).toEqual(wire(rotated));
  });

  it("re-registers nothing when the subscription still matches what the server was told", async () => {
    const endpoint = "https://push.example/wp/sub-live";
    env.sub = subscription(endpoint, bytesOf(VAPID));

    expect(await sync(record({ push: wire(endpoint) }), "durable")).toEqual({ on: true, why: "" });
    expect(vi.mocked(pushRegister)).not.toHaveBeenCalled();
    expect(vi.mocked(pushRemove)).not.toHaveBeenCalled();
  });

  it("drops the local claim and tells the server when the browser blocked notifications", async () => {
    const endpoint = "https://push.example/wp/sub-live";
    env.sub = subscription(endpoint, bytesOf(VAPID));
    env.permission = "denied";
    const rec = record({ push: wire(endpoint) });

    expect(await sync(rec, "durable")).toEqual({ on: false, why: W.PUSH_OFF_BLOCKED });
    expect(vi.mocked(pushRemove).mock.calls).toEqual([[wire(endpoint)]]);
    expect(rec.push).toBe(null);
    expect(vi.mocked(clearPush)).toHaveBeenCalled();
  });

  it("unsubscribes without a server call when the device is gone", async () => {
    const endpoint = "https://push.example/wp/sub-doomed";
    env.sub = subscription(endpoint, bytesOf(VAPID));

    await teardown();
    expect(env.unsubscribed).toEqual([endpoint]);
    expect(vi.mocked(pushRemove)).not.toHaveBeenCalled();
  });

  it("unregisters the worker of the replaced approvals page and no other", async () => {
    const gone: string[] = [];
    env.others = [
      { scope: "https://app.example/approvals/", unregister: async () => { gone.push("approvals"); return true; } },
      { scope: "https://app.example/other/", unregister: async () => { gone.push("other"); return true; } },
    ];

    await retireOldWorker();
    expect(gone).toEqual(["approvals"]);
    expect(env.order).not.toContain("unregister");
  });
});
