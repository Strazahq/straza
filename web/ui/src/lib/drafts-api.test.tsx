import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import {
  checkDraft,
  conflictsOf,
  contactDraftServer,
  createDraft,
  discardDraft,
  getDraft,
  listDrafts,
  publishDraft,
  rebaseDraft,
  refusalOf,
  revertDraft,
  updateDraft,
  waitingLabel,
} from "./drafts-api";

// The drafts wrappers speak the drafts routes byte for byte: the
// method, the path and the body each call sends, and the error bodies a
// screen reads back.

vi.mock("./session", () => ({ token: () => "tok", lose: vi.fn() }));

type Sent = { method: string; path: string; body: unknown };
let sent: Sent[] = [];
let answer: { status: number; body: unknown } = { status: 200, body: {} };

beforeEach(() => {
  sent = [];
  answer = { status: 200, body: {} };
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    sent.push({ method: init?.method || "GET", path: String(input), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    return new Response(JSON.stringify(answer.body), { status: answer.status, headers: { "Content-Type": "application/json" } });
  });
});
afterEach(() => vi.unstubAllGlobals());

describe("the drafts wrappers", () => {
  const publish = { revision: 2, risk_digest: "d".repeat(64), ticked: ["k-host", "k-reach"], typed: { "k-host": "api.githubcopilot.com" } };
  const cases: [string, () => Promise<unknown>, Sent][] = [
    ["lists with the query as given", () => listDrafts("state=open&mine=true"), { method: "GET", path: "/v1/admin/drafts?state=open&mine=true", body: undefined }],
    ["reads one draft", () => getDraft("41"), { method: "GET", path: "/v1/admin/drafts/41", body: undefined }],
    ["creates a draft", () => createDraft({ documents: ["kind: App"], note: "n" }), { method: "POST", path: "/v1/admin/drafts", body: { documents: ["kind: App"], note: "n" } }],
    ["checks without storing", () => checkDraft({ documents: ["kind: App"] }), { method: "POST", path: "/v1/admin/drafts/check", body: { documents: ["kind: App"] } }],
    ["updates at a revision", () => updateDraft("41", { revision: 2, documents: [] }), { method: "PUT", path: "/v1/admin/drafts/41", body: { revision: 2, documents: [] } }],
    ["checks again with picks", () => rebaseDraft("43", { revision: 1, picks: { "App/demo-tools straza.limits.rps": "draft" } }), { method: "POST", path: "/v1/admin/drafts/43/rebase", body: { revision: 1, picks: { "App/demo-tools straza.limits.rps": "draft" } } }],
    ["discards at a revision with a reason", () => discardDraft("41", "not needed", 2), { method: "POST", path: "/v1/admin/drafts/41/discard", body: { revision: 2, reason: "not needed" } }],
    ["discards with no revision", () => discardDraft("41", ""), { method: "POST", path: "/v1/admin/drafts/41/discard", body: { reason: "" } }],
    ["undoes a published draft with a note", () => revertDraft("41", "roll back"), { method: "POST", path: "/v1/admin/drafts/41/revert", body: { note: "roll back" } }],
    ["contacts one server of a draft", () => contactDraftServer("41", "App/github"), { method: "POST", path: "/v1/admin/drafts/41/contact", body: { object: "App/github" } }],
    ["publishes with the body as given", () => publishDraft("41", publish), { method: "POST", path: "/v1/admin/drafts/41/publish", body: publish }],
  ];
  it.each(cases)("%s", async (_name, call, want) => {
    await call();
    expect(sent).toEqual([want]);
  });

  it("escapes an id it is handed", async () => {
    await getDraft("4/1");
    expect(sent[0].path).toBe("/v1/admin/drafts/4%2F1");
  });
});

describe("the error bodies a drafts screen reads", () => {
  it("keeps a 409's parsed body on the error", async () => {
    const body = { error: "Publish refused: something. Acknowledge it and publish again.", verdict: { risks: [] } };
    answer = { status: 409, body };
    const err = (await publishDraft("41", { revision: 1, risk_digest: "", ticked: [], typed: {} }).catch((e) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.message).toBe(body.error);
    expect(err.body).toEqual(body);
    expect(refusalOf(err)).toEqual(body);
    expect(conflictsOf(err)).toBe(null);
  });

  it("reads the conflicts of a rebase", async () => {
    const body = { error: "Draft 43 and live state both changed App/demo-tools straza.limits.rps since the draft was checked. Pick which value to keep, then check again.", conflicts: [{ object: "App/demo-tools", field: "straza.limits.rps", base: "5", draft: "20", live: "10" }] };
    answer = { status: 409, body };
    const err = (await rebaseDraft("43", { revision: 1 }).catch((e) => e)) as ApiError;
    expect(conflictsOf(err)).toEqual(body.conflicts);
  });

  const bare: [string, unknown][] = [
    ["an error with no body", new ApiError("unreachable", 0, true)],
    ["an error whose body is not an object", new ApiError("HTTP 500", 500, false, 0, "oops")],
    ["something that is not an ApiError", new Error("boom")],
  ];
  it.each(bare)("answers null for %s", (_name, err) => {
    expect(refusalOf(err)).toBe(null);
    expect(conflictsOf(err)).toBe(null);
  });
});

describe("how long a call waits", () => {
  // A contact's dial is bounded at 10 s on the server, so the console waits
  // longer and shows the server's own 502, not an unanswered write.
  const hang = () => vi.stubGlobal("fetch", (_input: RequestInfo | URL, init?: RequestInit) => new Promise<Response>((_resolve, reject) => {
    init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
  }));

  it("waits 15 s for a contact", async () => {
    vi.useFakeTimers();
    try {
      hang();
      let settled: ApiError | null = null;
      void contactDraftServer("41", "App/github").catch((e) => { settled = e as ApiError; });
      await vi.advanceTimersByTimeAsync(10500);
      expect(settled).toBe(null);
      await vi.advanceTimersByTimeAsync(5000);
      expect(settled).not.toBe(null);
      expect((settled as unknown as ApiError).unreachable).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the 10 s bound on every other call", async () => {
    vi.useFakeTimers();
    try {
      hang();
      let settled: ApiError | null = null;
      void getDraft("41").catch((e) => { settled = e as ApiError; });
      await vi.advanceTimersByTimeAsync(10500);
      expect(settled).not.toBe(null);
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("the waiting count the sidebar shows", () => {
  const cases: [string, unknown, string][] = [
    ["no open draft reads as no number", { items: [], next_cursor: "" }, ""],
    ["a page of open drafts reads as its length", { items: [{ id: "1" }, { id: "2" }], next_cursor: "" }, "2"],
    ["a full page with more reads as a floor", { items: new Array(200).fill({ id: "x" }), next_cursor: "c" }, "200+"],
  ];
  it.each(cases)("%s", async (_name, page, want) => {
    answer = { status: 200, body: page };
    expect(await waitingLabel()).toBe(want);
    expect(sent[0].path).toBe("/v1/admin/drafts?state=open&limit=200");
  });
});
