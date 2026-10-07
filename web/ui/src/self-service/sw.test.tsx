import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// The worker registers its handlers on import against the global scope, so
// each case builds a scope, imports the module fresh, and drives the
// handlers it captured. The record comes through the idb module, mocked.

type Handler = (e: unknown) => void;

const record: { value: Record<string, unknown> | undefined } = { value: undefined };
const patched: Array<Record<string, unknown>> = [];
vi.mock("./idb", () => ({
  readRecord: vi.fn(async () => record.value),
  patchRecord: vi.fn(async (p: Record<string, unknown>) => { patched.push(p); return p; }),
}));

function scope() {
  const handlers: Record<string, Handler> = {};
  const shown: Array<{ title: string; opts: { body: string; tag: string } }> = [];
  const messages: unknown[] = [];
  const opened: string[] = [];
  const focused: string[] = [];
  const clients: Array<{ url: string; focus: () => Promise<void>; postMessage: (m: unknown) => void }> = [{ url: "http://localhost:8420/console/servers", focus: async () => { focused.push("console"); }, postMessage: (m: unknown) => { messages.push(m); } }];
  const sw = {
    addEventListener: (type: string, fn: Handler) => { handlers[type] = fn; },
    skipWaiting: async () => undefined,
    registration: {
      scope: "http://localhost:8420/self-service/",
      showNotification: async (title: string, opts: { body: string; tag: string }) => { shown.push({ title, opts }); },
      pushManager: { subscribe: vi.fn(async () => fakeSub("https://push.example/new")) },
    },
    clients: { matchAll: async () => clients, openWindow: async (url: string) => { opened.push(url); }, claim: async () => undefined },
  };
  vi.stubGlobal("self", sw);
  return { sw, handlers, shown, messages, opened, focused, clients };
}

function fakeSub(endpoint: string) {
  return { endpoint, getKey: (n: string) => new Uint8Array(n === "auth" ? 16 : 65).fill(3).buffer };
}

const wait = (p: Promise<unknown>) => p;
const push = (data: unknown) => ({ data: data === null ? null : { json: () => data }, waitUntil: wait });

async function load() {
  vi.resetModules();
  await import("./sw");
}

beforeEach(() => { record.value = undefined; patched.length = 0; });
afterEach(() => vi.unstubAllGlobals());

describe("the self-service worker", () => {
  it("shows the call and who asked for a decide push, and pings every open page", async () => {
    const s = scope();
    record.value = { deviceToken: "tok" };
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify([{ id: "apr_1", summary: { tool: "mcp.call", app: "demo-tools", tool_name: "get_sum" }, requester: { username: "joe" } }]), { status: 200 })));
    await load();
    await s.handlers.push(push({ v: 1, ref: "apr_1", kind: "decide" }));
    expect(s.shown).toEqual([{ title: "Approval needed", opts: { body: "get sum in demo-tools, asked by joe", tag: "apr_1", data: { ref: "apr_1" } } }]);
    expect(s.messages).toEqual([{ type: "straza-approvals", kind: "decide" }]);
    const call = (fetch as unknown as ReturnType<typeof vi.fn>).mock.calls[0] as [string, RequestInit];
    expect(call[0]).toBe("/v1/approver/pending?scope=decidable");
    expect((call[1].headers as Record<string, string>).Authorization).toBe("Bearer tok");
  });

  it("still notifies on an undecodable payload, in its own words, and fetches nothing", async () => {
    const s = scope();
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await load();
    await s.handlers.push(push({ command: "rm -rf /" }));
    await s.handlers.push(push(null));
    expect(s.shown).toHaveLength(2);
    expect(s.shown[0].opts.body).toBe("Something happened in your approvals. Open the self-service page to see it.");
    expect(s.shown[0].opts.body).not.toContain("rm");
    expect(fetchMock).not.toHaveBeenCalled();
    expect(s.messages).toEqual([{ type: "straza-approvals", kind: "" }, { type: "straza-approvals", kind: "" }]);
  });

  it("words a status push from the requester's own records, generically when the read fails", async () => {
    const s = scope();
    record.value = { deviceToken: "tok" };
    vi.stubGlobal("fetch", vi.fn(async () => new Response("no", { status: 500 })));
    await load();
    await s.handlers.push(push({ v: 1, ref: "apr_2", kind: "status" }));
    expect(s.shown[0].title).toBe("Your request was decided");
    expect(s.shown[0].opts.body).toBe("Open the self-service page to see the outcome.");
  });

  it("focuses an open page on either address and opens the new one otherwise", async () => {
    const s = scope();
    await load();
    const click = { notification: { close: vi.fn() }, waitUntil: wait };
    await s.handlers.notificationclick(click);
    expect(s.opened).toEqual(["/self-service/"]);
    s.clients.push({ url: "http://localhost:8420/approvals/", focus: async () => { s.focused.push("old"); }, postMessage: () => undefined });
    await s.handlers.notificationclick(click);
    expect(s.focused).toEqual(["old"]);
    expect(s.opened).toHaveLength(1);
  });

  it("re-registers a rotated subscription once and drops the old row, and never without a stored one", async () => {
    const s = scope();
    const calls: Array<[string, string]> = [];
    vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => { calls.push([init.method || "GET", String(init.body)]); return new Response("{}", { status: 200 }); }));
    await load();
    const change = { newSubscription: fakeSub("https://push.example/new"), waitUntil: wait };
    record.value = { deviceToken: "tok", webpush: { vapid_public_key: "AQID" } };
    await s.handlers.pushsubscriptionchange(change);
    expect(calls).toHaveLength(0);
    record.value = { deviceToken: "tok", webpush: { vapid_public_key: "AQID" }, push: { kind: "webpush", token_or_endpoint: "https://push.example/old", p256dh: "x", auth: "y" } };
    await s.handlers.pushsubscriptionchange(change);
    expect(calls.map((c) => c[0])).toEqual(["PUT", "DELETE"]);
    expect(calls[0][1]).toContain("https://push.example/new");
    expect(calls[1][1]).toContain("https://push.example/old");
    expect(patched).toHaveLength(1);
    expect((patched[0].push as { token_or_endpoint: string }).token_or_endpoint).toBe("https://push.example/new");
  });
});
