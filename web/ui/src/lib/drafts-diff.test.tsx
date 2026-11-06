import { describe, expect, it } from "vitest";
import { type Part, wordDiff } from "./drafts-diff";

// The word diff of an edited rule: each case is the old and the new
// sentence and the runs each side shows, a changed run marked with a star.

const show = (parts: Part[]) => parts.map((p) => (p.changed ? "*" + p.text + "*" : p.text)).join(" ");

describe("the word diff of an edited rule", () => {
  const cases: [string, string, string, string, string][] = [
    ["the same sentence", "get-sum on demo-tools is denied.", "get-sum on demo-tools is denied.", "get-sum on demo-tools is denied.", "get-sum on demo-tools is denied."],
    [
      "a dropped tool, a verb and a time",
      "pr_merge and git_push on ghapp need approval by the person behind the agent, within 2 minutes.",
      "pr_merge on ghapp needs approval by the person behind the agent, within 10 minutes.",
      "pr_merge *and git_push* on ghapp *need* approval by the person behind the agent, within *2* minutes.",
      "pr_merge on ghapp *needs* approval by the person behind the agent, within *10* minutes.",
    ],
    ["a word added at the end", "get-sum needs approval.", "get-sum needs approval by sec-approvers.", "get-sum needs *approval.*", "get-sum needs *approval by sec-approvers.*"],
    ["nothing in common", "a b", "c d", "*a b*", "*c d*"],
    ["an empty old side", "", "get-sum is denied.", "", "*get-sum is denied.*"],
    ["runs of spaces read as one", "get-sum  is   denied.", "get-sum is denied.", "get-sum is denied.", "get-sum is denied."],
  ];
  it.each(cases)("marks %s", (_name, was, now, wantWas, wantNow) => {
    const d = wordDiff(was, now);
    expect(show(d.was)).toBe(wantWas);
    expect(show(d.now)).toBe(wantNow);
  });
});
