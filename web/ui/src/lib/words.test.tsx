import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setTimeZone } from "./timezone";
import { agoLong, agoWord, dayWord, matcherWords, matchesTool, probeWords } from "./words";

describe("a past stamp", () => {
  it("reads a past stamp in days once it is older than a day, so a column holds it", () => {
    const day = 86400000;
    expect(agoWord(new Date(Date.now() - 23 * day).toISOString())).toBe("23 d ago");
    expect(agoWord(new Date(Date.now() - 78 * day).toISOString())).toBe("78 d ago");
    expect(agoWord(new Date(Date.now() - 2 * 3600000).toISOString())).toBe("2 h ago");
    expect(agoWord(undefined)).toBe("none");
  });
});

describe("a past stamp in a sentence, with the clock at 14 Nov 2026, 10:00 UTC", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-14T10:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it.each([
    ["2026-11-14T09:59:30Z", "just now"],
    ["2026-11-14T09:59:00Z", "1 minute ago"],
    ["2026-11-14T09:55:59Z", "4 minutes ago"],
    ["2026-11-14T09:00:00Z", "1 hour ago"],
    ["2026-11-14T04:30:00Z", "5 hours ago"],
    ["2026-11-13T10:00:00Z", "1 day ago"],
    ["2026-11-09T09:00:00Z", "5 days ago"],
    ["2026-11-08T14:48:00Z", "6 days ago"],
  ])("reads %s as %s", (iso, word) => {
    expect(agoLong(iso)).toBe(word);
  });

  it("counts the days of one stamp as agoWord does", () => {
    // 5.8 days old: both round to 6, where rounding down would say 5.
    const iso = "2026-11-08T14:48:00Z";
    expect(agoLong(iso)).toBe("6 days ago");
    expect(agoWord(iso)).toBe("6 d ago");
  });

  it("answers none for a missing stamp, like its siblings", () => {
    expect(agoLong(undefined)).toBe("none");
  });
});

describe("the day of a stamp, with the clock at 14 Nov 2026, 10:00 UTC", () => {
  beforeEach(() => {
    setTimeZone("utc");
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-14T10:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it.each([
    ["2026-11-08T09:00:00Z", "8 Nov 2026"],
    ["2026-09-01T00:00:00Z", "1 Sep 2026"],
    ["2026-01-31T23:59:00Z", "31 Jan 2026"],
  ])("reads the day of %s as %s", (iso, word) => {
    expect(dayWord(iso)).toBe(word);
  });
});

describe("an access row's tool patterns, read as the server reads them", () => {
  // The first block is the server's own table for its glob,
  // internal/drafts/match_test.go TestMatchGlobAgreesWithTheManager. The
  // second is the patterns a stored row carries on eval and their kin.
  it.each<[string, string, boolean]>([
    ["*", "anything", true],
    ["*", "", true],
    ["get_me", "get_me", true],
    ["get_me", "get_me2", false],
    ["get_*", "get_repo", true],
    ["get_*", "list_repo", false],
    ["*_repo", "delete_repo", true],
    ["a*b*c", "a1b2c", true],
    ["a*b*c", "a1c2b", false],
    ["a.b", "axb", false],
    ["a.b*", "a.bc", true],
    ["(x)*", "(x)y", true],
    ["re:.*", "re:.*", true],

    ["search_*", "search_users", true],
    ["search_*", "research_users", false],
    ["get_*", "get_", true],
    ["get_*", "get-repo", false],
    ["list_*", "list_requestable_roles", true],
    ["list_*", "whoami", false],
    ["whoami", "whoami", true],
    ["whoami", "whoami_now", false],
    ["x.*", "x.create", true],
    ["x.*", "xcreate", false],
    ["*_role", "request_role", true],
    ["*_role", "request_roles", false],
    ["a*b*c", "abc", true],
    ["a*b*c", "ab", false],
  ])("%s admits %s: %s", (pattern, name, want) => {
    expect(matchesTool([pattern], name)).toBe(want);
  });

  it("admits a tool any pattern of the row admits, and every tool for a row with none", () => {
    const row = ["whoami", "ping", "list_requestable_roles", "request_role", "decide_work_item", "search_*", "get_*", "list_*"];
    expect(["whoami", "search_users", "get_user", "list_orgs", "request_role"].every((t) => matchesTool(row, t))).toBe(true);
    expect(matchesTool(row, "delete_user")).toBe(false);
    expect(matchesTool([], "anything")).toBe(true);
    expect(matchesTool(undefined, "anything")).toBe(true);
  });

  it("says a pattern in words, and a bare name as itself", () => {
    expect(matcherWords(["*"])).toBe("every tool");
    expect(matcherWords(undefined)).toBe("every tool");
    expect(matcherWords(["whoami", "search_*", "x.*"])).toBe("whoami, tools matching search_*, tools matching x.*");
  });
});

describe("a server's health reason", () => {
  it("rewrites a probe failure as one sentence", () => {
    expect(probeWords("dial tcp 10.0.0.5:443: i/o timeout", "https://mcp.example")).toBe("Straza could not reach https://mcp.example (no answer in time).");
  });
  it.each([
    "inventory drift: the server changed set_timeout (readOnlyHint is now true)",
    "inventory drift: the server changed list_unauthorized_logins (description)",
    "inventory drift: +[forbidden_words] -[], changed check (input schema)",
  ])("keeps a drift reason whose tool names spell a failure word: %s", (reason) => {
    expect(probeWords(reason, "https://mcp.example")).toBe(reason);
  });
});
