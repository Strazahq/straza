import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import { checkFailed, refused } from "./say";

describe("write failure wording", () => {
  it("does not claim a write was rolled back when its response is lost", () => {
    const message = refused(new ApiError("request timed out", 0, true));
    expect(message).toContain("may have been saved");
    expect(message).toContain("Reload to check before trying again");
    expect(message).not.toContain("nothing changed");
  });

  it("retains the server's reason when the server explicitly rejects a write", () => {
    const message = refused(new ApiError("role is assigned and cannot be deleted.", 409));
    expect(message).toContain("role is assigned and cannot be deleted.");
    expect(message).not.toContain("may have been saved");
  });
});

describe("check failure wording", () => {
  it("says an unanswered check stored nothing and keeps the draft", () => {
    const message = checkFailed(new ApiError("request timed out", 0, true));
    expect(message).toContain("Nothing was stored");
    expect(message).not.toContain("may have been saved");
    expect(message).not.toContain("Reload");
  });

  it("retains the server's reason when the server rejects a check", () => {
    expect(checkFailed(new ApiError("unknown field rules", 400))).toBe(refused(new ApiError("unknown field rules", 400)));
  });
});
