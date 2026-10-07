import { describe, expect, it } from "vitest";
import { eventRow } from "./words";

// A role a server owns is recorded against that server, so its creation
// and its deletion read on the server's Activity tab beside the installs
// and the refused calls.

const rec = (data: Record<string, unknown>, seq = 7) => ({
  seq,
  ce: JSON.stringify({ type: "straza.audit.admin", time: "2026-09-18T07:58:04Z", data }),
  username: "carol",
});

describe("the Activity line of a server-owned role", () => {
  const cases: [string, Record<string, unknown>, string | null][] = [
    ["created", { action: "roles.create", target: "r-1", role: "demo-tools-readers", server: "demo-tools" }, "created the role demo-tools-readers."],
    ["deleted", { action: "roles.delete", target: "r-1", role: "demo-tools-readers", server: "demo-tools" }, "deleted the role demo-tools-readers."],
    ["deleted with the server", { action: "roles.delete", target: "r-1", role: "demo-tools-readers", server: "demo-tools", reason: "removed with the server demo-tools" }, "deleted the role demo-tools-readers, removed with the server demo-tools."],
    ["of another server", { action: "roles.create", target: "r-2", role: "midpoint-readers", server: "midpoint" }, null],
    ["of no server", { action: "roles.create", target: "r-3", role: "analysts" }, null],
  ];
  for (const [what, data, want] of cases) {
    it("reads a role " + what, () => {
      const got = eventRow(rec(data), "demo-tools");
      expect(got ? got.text : null).toBe(want);
      if (got) {
        expect(got.who).toBe("carol");
        expect(got.src).toBe("admin action");
        expect(got.tone).toBe("accent");
      }
    });
  }

  it("keeps the access row the same create writes on its own line", () => {
    const got = eventRow(rec({ action: "apps.binding.create", app: "demo-tools", role: "demo-tools-readers", tools: ["echo", "add"] }), "demo-tools");
    expect(got ? got.text : null).toBe("gave demo-tools-readers access to echo, add.");
  });
});
