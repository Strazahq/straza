import { describe, expect, it } from "vitest";
import type { Transcript } from "./api";
import { captureLine, clockTime, conversationJSONL, conversationText, turnsLine } from "./transcript-words";
import { dayOf } from "./words";

const SESSION = "0199cf12-4b1e-7a3d-9f21-8c0b1e2d3a44";
const JOE = "joe-java-developer-agent";

describe("recording configuration", () => {
  it("distinguishes a confirmed disabled state from missing settings", () => {
    expect(captureLine({ policy_sets: 0 }, false).word).toBe("off");
    expect(captureLine({}, false).word).toBe("unknown");
    const partial = captureLine({ policy_sets: 1 }, false);
    expect(partial.text).toContain("recording mode unavailable");
    expect(partial.text).toContain("Retention is unavailable");
    expect(partial.text).toContain("message storage is unavailable");
    expect(partial.text).not.toContain("after 0 h");
  });

  it("explains that mixed recording modes depend on the policy", () => {
    const line = captureLine({ policy_sets: 2, mode: "mixed", retention_hours: 720, body_store: "s3" }, false);
    expect(line.text).toContain("word for word or with secrets masked, depending on the policy");
    expect(line.text).toContain("S3 and follow its lifecycle rules");
  });
});

// One conversation across midnight in UTC, the default zone: a prompt, a
// reply the store lost, and a truncated reply on the next day.
const across: Transcript = {
  session_id: SESSION,
  username: JOE,
  turns: [
    { at: "2026-09-11T23:50:01Z", kind: "prompt", mode: "redact", content: "Deploy the billing service.", content_hash: "sha256:aa01" },
    { at: "2026-09-11T23:50:09Z", kind: "reply", mode: "redact", content: "", body_missing: true, content_hash: "sha256:aa02", agent_type: "subagent" },
    { at: "2026-09-12T00:03:00Z", kind: "reply", mode: "redact", content: "The first part of a long answer.", truncated: true, content_hash: "sha256:aa03" },
  ],
};

describe("the day of a stamp", () => {
  it.each([
    ["2026-09-12T10:20:04Z", "2026-09-12", "10:20:04"],
    ["2026-09-11T23:59:59Z", "2026-09-11", "23:59:59"],
  ])("reads %s as day %s and clock %s", (iso, day, clock) => {
    expect(dayOf(iso)).toBe(day);
    expect(clockTime(iso)).toBe(clock);
  });

  it("reads a missing stamp as none", () => {
    expect(dayOf(undefined)).toBe("none");
  });
});

describe("the span sentence of a conversation", () => {
  it.each([
    ["one day names the day once", JOE, 6, "2026-09-12T10:20:04Z", "2026-09-12T10:45:00Z", JOE + ". 6 turns on 2026-09-12 from 10:20:04 to 10:45:00."],
    ["one turn is singular", JOE, 1, "2026-09-12T10:20:04Z", "2026-09-12T10:20:04Z", JOE + ". 1 turn on 2026-09-12 from 10:20:04 to 10:20:04."],
    ["across midnight names both stamps", JOE, 3, "2026-09-11T23:50:01Z", "2026-09-12T00:03:00Z", JOE + ". 3 turns from 2026-09-11 23:50:01 to 2026-09-12 00:03:00."],
    ["an unnamed user says so", undefined, 2, "2026-09-12T10:20:04Z", "2026-09-12T10:45:00Z", "The user is not named. 2 turns on 2026-09-12 from 10:20:04 to 10:45:00."],
  ])("%s", (_name, username, n, from, to, want) => {
    expect(turnsLine(username, n, from, to)).toBe(want);
  });
});

describe("the plain text export", () => {
  it("leads with the session, the user and the capture mode, then one stamped block per turn", () => {
    const lines = conversationText(across).split("\n");
    expect(lines.slice(0, 4)).toEqual([
      "Session " + SESSION,
      "User: " + JOE,
      "Recorded with secrets masked. 3 turns from 2026-09-11 23:50:01 UTC to 2026-09-12 00:03:00 UTC.",
      "",
    ]);
    expect(lines.slice(4, 7)).toEqual(["[2026-09-11 23:50:01 UTC] " + JOE + ":", "Deploy the billing service.", ""]);
    expect(lines[7]).toBe("[2026-09-11 23:50:09 UTC] assistant · subagent:");
    expect(lines[8]).toBe("(body unavailable) The body of this turn should be in the body store and was not found. This is an integrity finding; the hash witness on the audit chain still stands. Hash sha256:aa02");
    expect(lines.slice(10, 13)).toEqual(["[2026-09-12 00:03:00 UTC] assistant:", "The first part of a long answer.", "Truncated. Full content hash sha256:aa03"]);
    expect(conversationText(across).endsWith("\n")).toBe(true);
  });

  it("says a verbatim conversation is verbatim and an empty one is empty", () => {
    const verbatim = { ...across, turns: across.turns.map((t) => ({ ...t, mode: "verbatim" })) };
    expect(conversationText(verbatim).split("\n")[2]).toBe("Recorded word for word. 3 turns from 2026-09-11 23:50:01 UTC to 2026-09-12 00:03:00 UTC.");
    const empty = conversationText({ session_id: SESSION, turns: [] }).split("\n");
    expect(empty.slice(0, 3)).toEqual(["Session " + SESSION, "User: not named", "No recorded turns: recording is off for this session's policy, or nothing was said."]);
  });
});

describe("the JSONL export", () => {
  it("writes one self-describing line per turn, the session and the user on each", () => {
    const lines = conversationJSONL(across).split("\n");
    expect(lines).toHaveLength(4);
    expect(lines[3]).toBe("");
    const rows = lines.slice(0, 3).map((l) => JSON.parse(l));
    expect(rows.map((r) => [r.session_id, r.username, r.at, r.kind])).toEqual([
      [SESSION, JOE, "2026-09-11T23:50:01Z", "prompt"],
      [SESSION, JOE, "2026-09-11T23:50:09Z", "reply"],
      [SESSION, JOE, "2026-09-12T00:03:00Z", "reply"],
    ]);
    expect(rows[1].body_missing).toBe(true);
    expect(rows[2].truncated).toBe(true);
    expect(rows[2].content_hash).toBe("sha256:aa03");
  });

  it("writes nothing but a newline for an empty conversation", () => {
    expect(conversationJSONL({ session_id: SESSION, turns: [] })).toBe("\n");
  });
});
