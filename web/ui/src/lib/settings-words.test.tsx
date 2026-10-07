import { describe, expect, it } from "vitest";
import type { AttestationHashRow } from "./api";
import { agoWord, canonicalScope, grantsOf, hiddenTabsLine, rendersOf, revokeBody, shortHash } from "./settings-words";

const row = (artifact: string, platform: string, hash: string, current: boolean, note?: string): AttestationHashRow => ({ id: artifact + "/" + platform, artifact, harness: artifact.replace("hooks.", ""), platform, hash, current, note, created_at: "2026-09-07T09:12:00Z" });

describe("settings words", () => {
  it("folds eighteen registrations into one row per render, current first", () => {
    const rows: AttestationHashRow[] = [];
    for (const art of ["hooks.claude-code", "hooks.codex", "hooks.gemini"]) {
      for (const p of ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"]) rows.push(row(art, p, art + "-unix", true));
      for (const p of ["windows/amd64", "windows/arm64"]) rows.push(row(art, p, art + "-win", true));
    }
    for (const p of ["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"]) rows.push(row("hooks.codex", p, "hooks.codex-old", false, "harness-config render (strazad v1.0.0-1051)"));
    const renders = rendersOf(rows);
    expect(renders).toHaveLength(7);
    expect(renders.map((r) => r.artifact + ":" + r.current)).toEqual(["hooks.claude-code:true", "hooks.claude-code:true", "hooks.codex:true", "hooks.codex:true", "hooks.codex:false", "hooks.gemini:true", "hooks.gemini:true"]);
    expect(renders[0].platforms).toEqual(["linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"]);
    expect(renders[4].rows).toHaveLength(4);
    expect(renders[4].dirty).toBe(false);
    expect(rendersOf([row("hooks.codex", "linux/amd64", "h", true, "harness-config render (strazad v1.0.0-1051-dirty)")])[0].dirty).toBe(true);
  });

  it("shortens a hash for the cell and keeps a short one whole", () => {
    expect(shortHash("5b1e0c7d9a44f2e6b3d18c0a7f5e2d9c4b6a1e8f0d3c5b7a9e1f2d4c6b8a0e3f")).toBe("5b1e0c7d9a44…0e3f");
    expect(shortHash("abc")).toBe("abc");
  });

  it.each<[string, string]>([
    ["scim:read,scim:write,identity:read,apps:read,changes:read,config:read", "Whatever signs in as midpoint loses the SCIM plane, its reads of users and roles, its reads of servers, the change feed and the configuration read-out within seconds."],
    ["audit:read,sessions:read", "Whatever signs in as midpoint loses the ledger and its view of sessions within seconds."],
    ["policy:write", "Whatever signs in as midpoint loses its policy access within seconds."],
    ["full", "Whatever signs in as midpoint loses every admin route within seconds."],
  ])("builds the revoke sentence from the grants %s", (scope, body) => {
    expect(revokeBody({ name: "midpoint", scope })).toBe(body);
  });

  it("reads a past stamp in days once it is older than a day, so a column holds it", () => {
    const day = 86400000;
    expect(agoWord(new Date(Date.now() - 23 * day).toISOString())).toBe("23 d ago");
    expect(agoWord(new Date(Date.now() - 78 * day).toISOString())).toBe("78 d ago");
    expect(agoWord(new Date(Date.now() - 2 * 3600000).toISOString())).toBe("2 h ago");
    expect(agoWord(undefined)).toBe("none");
  });

  it("renders the canonical scope and reads grants back", () => {
    expect(canonicalScope(false, ["scim:write", "scim:read", "scim:write"])).toBe("scim:read,scim:write");
    expect(canonicalScope(true, ["audit:read"])).toBe("full");
    expect(grantsOf("full")).toEqual([]);
    expect(grantsOf(" audit:read, sessions:read ")).toEqual(["audit:read", "sessions:read"]);
    expect(hiddenTabsLine(["Configuration", "Attestation registry"])).toBe("Configuration and Attestation registry need the config area, which this session does not hold.");
    expect(hiddenTabsLine(["Configuration"])).toBe("Configuration needs the config area, which this session does not hold.");
  });
});
