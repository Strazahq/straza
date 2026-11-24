import { describe, expect, it } from "vitest";
import { ADMIN_HEALTH_TITLE, ATT_TITLE, HEALTH_WORD, WIRING_TITLE, countWords, groupLine, healthOf, liveHelp, sessionHarness } from "./session-words";

describe("sessionHarness", () => {
  it.each([
    ["claude-code/2.2.0", "Claude Code 2.2.0"],
    ["claude-code", "Claude Code"],
    ["python-sdk/0.4.0", "Python kit 0.4.0"],
    ["exec-wrapper/mcp", "exec wrapper mcp"],
    ["console/1", "Console 1"],
    ["self-service", "Self-service page"],
    ["strazactl/eval-seed", "strazactl eval-seed"],
    ["some-kit/1.0/beta", "some-kit 1.0/beta"],
    ["", ""],
  ])("reads %s as %s", (harness, want) => {
    expect(sessionHarness(harness)).toBe(want);
  });
});

// The title healthOf gives a session that is not an admin harness: the
// attestation's sentence, then the hook configuration's, each left out when
// it has none.
const titled = (att: string, wiring: string) => [ATT_TITLE[att], WIRING_TITLE[wiring]].filter(Boolean).join(" ");

describe("healthOf", () => {
  // Every row of the Health table, in its order, each case a session that
  // the row is the first to fit.
  it.each([
    ["the console", { harness: "console/2.2.0", attestation: "none", wiring_status: "" }, "plain", HEALTH_WORD.admin],
    ["the self-service page", { harness: "self-service", attestation: "managed", wiring_status: undefined }, "plain", HEALTH_WORD.admin],
    ["strazactl", { harness: "strazactl/eval-seed", attestation: "none", wiring_status: "unmeasured" }, "plain", HEALTH_WORD.admin],
    ["attestation none", { harness: "claude-code/2.2.0", attestation: "none", wiring_status: "current" }, "danger", HEALTH_WORD.clientFailed],
    ["an attestation the console does not know", { harness: "claude-code/2.2.0", attestation: "partial", wiring_status: "current" }, "danger", HEALTH_WORD.clientFailed],
    ["hook configuration mismatch", { harness: "claude-code/2.2.0", attestation: "managed", wiring_status: "mismatch" }, "danger", HEALTH_WORD.hookUnknown],
    ["attestation advisory", { harness: "codex/0.9.1", attestation: "advisory", wiring_status: "current" }, "warn", HEALTH_WORD.clientNot],
    ["hook configuration allowed", { harness: "claude-code/2.2.0", attestation: "managed", wiring_status: "allowed" }, "warn", HEALTH_WORD.hookOld],
    ["hook configuration unmeasured", { harness: "claude-code/2.2.0", attestation: "managed", wiring_status: "unmeasured" }, "warn", HEALTH_WORD.hookUnmeasured],
    ["a blank hook configuration", { harness: "claude-code/2.2.0", attestation: "managed", wiring_status: undefined }, "warn", HEALTH_WORD.hookUnmeasured],
    ["managed and current", { harness: "claude-code/2.2.0", attestation: "managed", wiring_status: "current" }, "ok", HEALTH_WORD.verified],
  ])("reads %s", (_case, row, level, word) => {
    const h = healthOf(row);
    expect(h.level).toBe(level);
    expect(h.word).toBe(word);
    expect(h.title).toBe(level === "plain" ? ADMIN_HEALTH_TITLE : titled(row.attestation, row.wiring_status || ""));
  });

  // Every pair of an attestation and a hook configuration status, the
  // first fitting row of the table winning.
  const WANT: Record<string, Record<string, [string, string]>> = {
    managed: { current: ["ok", "verified"], allowed: ["warn", "hookOld"], mismatch: ["danger", "hookUnknown"], unmeasured: ["warn", "hookUnmeasured"], "": ["warn", "hookUnmeasured"] },
    advisory: { current: ["warn", "clientNot"], allowed: ["warn", "clientNot"], mismatch: ["danger", "hookUnknown"], unmeasured: ["warn", "clientNot"], "": ["warn", "clientNot"] },
    none: { current: ["danger", "clientFailed"], allowed: ["danger", "clientFailed"], mismatch: ["danger", "clientFailed"], unmeasured: ["danger", "clientFailed"], "": ["danger", "clientFailed"] },
  };
  const PAIRS = Object.entries(WANT).flatMap(([att, byWiring]) => Object.entries(byWiring).map(([wiring, [level, word]]) => [att, wiring, level, word] as const));
  it.each(PAIRS)("reads attestation %s with hook configuration %j as %s %s", (att, wiring, level, word) => {
    const h = healthOf({ harness: "claude-code/2.2.0", attestation: att, wiring_status: wiring || undefined });
    expect(h).toEqual({ level, word: HEALTH_WORD[word as keyof typeof HEALTH_WORD], title: titled(att, wiring) });
  });

  it("never reads a hook configuration status it does not know as verified", () => {
    expect(healthOf({ harness: "claude-code/2.2.0", attestation: "managed", wiring_status: "retired" }).level).toBe("warn");
  });

  it("leaves out a sentence the attestation does not have", () => {
    expect(healthOf({ harness: "claude-code/2.2.0", attestation: "constructor", wiring_status: "current" }).title).toBe(WIRING_TITLE.current);
  });
});

describe("liveHelp", () => {
  it.each([
    ["a push lane that is down", { lane_up: false, connected: 3 }, 4, "Straza cannot push a change to its clients right now. Each client checks for changes every 30 s."],
    ["a push block without the lane", {}, 4, "Straza cannot push a change to its clients right now. Each client checks for changes every 30 s."],
    ["every active client connected", { lane_up: true, connected: 4 }, 4, "Every active client is connected to Straza and hears of a change, such as a revoke, at once."],
    ["no active session", { lane_up: true }, 0, "Every active client is connected to Straza and hears of a change, such as a revoke, at once."],
    ["some active clients connected", { lane_up: true, connected: 3 }, 7, "3 of 7 active clients are connected to Straza and hear of a change, such as a revoke, at once. The others check every 30 s."],
    ["no client connected", { lane_up: true }, 2, "0 of 2 active clients are connected to Straza and hear of a change, such as a revoke, at once. The others check every 30 s."],
  ])("words %s", (_case, push, active, want) => {
    expect(liveHelp(push, active)).toBe(want);
  });
});

describe("groupLine", () => {
  it.each([
    [1, 1, true, "1 active session"],
    [3, 3, true, "3 active sessions"],
    [1, 0, false, "1 session"],
    [4, 0, false, "4 sessions"],
    [4, 1, false, "4 sessions, 1 active"],
    [2, 2, false, "2 sessions, 2 active"],
  ])("words %i sessions with %i active, active filter %s, as %s", (total, active, onlyActive, want) => {
    expect(groupLine(total, active, onlyActive)).toBe(want);
  });
});

describe("countWords", () => {
  it.each([
    [0, false, "0 sessions"],
    [1, false, "1 session"],
    [4, false, "4 sessions"],
    [1, true, "1 session loaded, more on Straza"],
    [100, true, "100 sessions loaded, more on Straza"],
  ])("words %i loaded, more %s, as %s", (loaded, more, want) => {
    expect(countWords(loaded, more)).toBe(want);
  });
});
