import { describe, expect, it } from "vitest";
import { deletedToast } from "./role-words";

describe("deletedToast", () => {
  it.each([
    [[], "gated is deleted."],
    [["gated-access"], "gated is deleted, and its policy set gated-access is turned off."],
    [["gated-access", "gated-holds", "gated-audit"], "gated is deleted, and its policy sets gated-access, gated-holds and gated-audit are turned off."],
  ])("names the policy sets the delete turned off: %j", (setsOff, want) => {
    expect(deletedToast("gated", setsOff)).toBe(want);
  });
});
