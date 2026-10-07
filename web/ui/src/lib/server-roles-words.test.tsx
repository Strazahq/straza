import { describe, expect, it } from "vitest";
import type { AppRow } from "./api";
import {
  certifiedHint,
  cliCreateRole,
  countLine,
  emptyBody,
  foldName,
  frozenTools,
  heldDelete,
  heldRule,
  newRoleCommand,
  previewParts,
  roleNameOf,
  rolePrefix,
  serverPrefixCheck,
  serverPrefixRefusal,
  unheldLine,
} from "./server-roles-words";

const app = (name: string): AppRow => ({ id: "app-" + name, name, runtime: "http", status: "running", reached_by: [] });

describe("the fold a server's prefix goes through", () => {
  const cases: [string, string][] = [
    ["demo-tools", "demo-tools"],
    ["Demo Tools", "demo-tools"],
    ["demo_tools", "demo-tools"],
    ["  read ers  ", "read-ers"],
    ["--x--y--", "x-y"],
    ["", ""],
  ];
  for (const [raw, want] of cases) {
    it("folds " + JSON.stringify(raw) + " to " + JSON.stringify(want), () => {
      expect(foldName(raw)).toBe(want);
    });
  }

  it("names a role of the server with the prefix and the folded suffix", () => {
    expect(rolePrefix("demo-tools")).toBe("demo-tools-");
    expect(roleNameOf("demo-tools", "Read Only")).toBe("demo-tools-read-only");
    expect(roleNameOf("demo-tools", "")).toBe("demo-tools-");
  });
});

describe("the sentences of a held role", () => {
  it("counts one holder and many the same way in both refusals", () => {
    expect(frozenTools("demo-tools-readers", 1)).toBe("The role demo-tools-readers has 1 holder. Its tools change only by the global admin or by a new role.");
    expect(heldDelete("demo-tools-readers", 3)).toBe("The role demo-tools-readers has 3 holders. The identity manager removes them first, then delete it.");
    expect(certifiedHint(1)).toBe("1 holder was certified on the current list. Widening it changes what they were certified for.");
    expect(certifiedHint(2)).toBe("2 holders were certified on the current list. Widening it changes what they were certified for.");
  });

  it("says what holding freezes, once per standing, and who assigns a role nobody holds", () => {
    expect(heldRule(false)).toBe("A held role keeps its tools and cannot be deleted until the identity manager removes the holders.");
    expect(heldRule(true)).toBe("A held role keeps its tools for its server admin and cannot be deleted until the identity manager removes the holders.");
    expect(unheldLine("demo-tools-readers")).toBe("Nobody holds it yet. Your identity manager assigns it: in midPoint it is AR:demo-tools-readers within a sync cycle.");
  });

  it("counts the roles of the server and names the prefix in the empty state", () => {
    expect(countLine(1)).toBe("1 role of this server");
    expect(countLine(4)).toBe("4 roles of this server");
    expect(emptyBody("demo-tools")).toContain("is named demo-tools- and a word of yours");
  });
});

describe("the live preview of a new role", () => {
  it("reads as the stored name, the identity manager's name and the same strazactl line", () => {
    expect(previewParts("demo-tools-readers")).toEqual({ lead: "Stored as ", stored: "demo-tools-readers", mid: ", in midPoint as ", ar: "AR:demo-tools-readers", end: "." });
    expect(cliCreateRole("demo-tools-readers", "demo-tools", ["echo", "add"])).toBe("strazactl roles create demo-tools-readers --app demo-tools --tools echo,add");
    expect(cliCreateRole("demo-tools-", "demo-tools", [])).toBe("strazactl roles create demo-tools- --app demo-tools --tools …");
  });

  // Each case is the tools, the description, and the line strazactl takes.
  const lines: [string, string[], string, string][] = [
    ["quotes every tool, and tools added later, so the shell keeps the star", ["*"], "", "strazactl roles create demo-tools-all --app demo-tools --tools '*'"],
    ["carries the description it is given", ["echo"], "Reads echo.", 'strazactl roles create demo-tools-all --app demo-tools --tools echo --description "Reads echo."'],
  ];
  it.each(lines)("%s", (_case, tools, description, want) => {
    expect(cliCreateRole("demo-tools-all", "demo-tools", tools, description)).toBe(want);
  });

  it("names the blanks of the line at the end of Add MCP server", () => {
    expect(newRoleCommand("scout-tools")).toBe("strazactl roles create scout-tools-<word> --app scout-tools --tools <tool>,<tool>");
  });
});

describe("the check that keeps a server's prefix for that server", () => {
  const apps = [app("demo"), app("demo-tools"), app("Files Server")];
  const cases: [string, string | null][] = [
    ["demo-tools-x", "demo-tools"],
    ["demo-readers", "demo"],
    ["demo-tools", "demo"],
    ["demo-tools-", "demo"],
    ["analysts", null],
    ["files-server-readers", "Files Server"],
    ["", null],
  ];
  for (const [typed, want] of cases) {
    it(JSON.stringify(typed) + (want ? " belongs to " + want : " belongs to no server"), () => {
      const hit = serverPrefixCheck(typed, apps);
      expect(hit ? hit.name : null).toBe(want);
    });
  }

  it("answers nothing when the server list was not read", () => {
    expect(serverPrefixCheck("demo-tools-x", null)).toBeNull();
  });

  it("says where the role belongs and what to do", () => {
    expect(serverPrefixRefusal("demo-tools")).toBe("Role names beginning with demo-tools- belong to the server demo-tools. Create it on that server's page so it becomes server-owned.");
  });
});
