import { describe, expect, it } from "vitest";
import type { UserKind } from "./user-look";
import { countLine, emptyKind } from "./user-list-words";

describe("countLine", () => {
  it.each<[number, number | undefined, boolean, string]>([
    [11, 11, false, "11 users"],
    [1, 1, false, "1 user"],
    [100, 240, true, "100 of 240 users"],
    [37, 37, false, "37 users"],
    [0, 1, false, "0 of 1 user"],
    [100, undefined, true, "100 users shown"],
    [4, undefined, false, "4 users"],
    [1, undefined, false, "1 user"],
  ])("words %i loaded of a total of %s, more %s", (loaded, total, more, want) => {
    expect(countLine(loaded, total, more)).toBe(want);
  });
});

describe("emptyKind", () => {
  it.each<[UserKind, string]>([
    ["person", "No people."],
    ["agent", "No AI agents."],
    ["service", "No services."],
  ])("words an empty %s tab", (kind, want) => {
    expect(emptyKind(kind)).toBe(want);
  });
});
