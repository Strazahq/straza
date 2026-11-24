import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AttestationHashRow } from "./api";
import { canonicalScope, expiryOf, grantsOf, hiddenTabsLine, rendersOf, revokeBody, shortHash, usedWord } from "./settings-words";
import { setTimeZone } from "./timezone";

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
    ["scim:read,scim:write,identity:read,apps:read,changes:read,config:read", "midpoint loses the SCIM plane, its reads of users and roles, its reads of servers, the change feed and the configuration read-out within seconds."],
    ["audit:read,sessions:read", "midpoint loses the audit records and its view of sessions within seconds."],
    ["policy:write", "midpoint loses its policy access within seconds."],
    ["full", "midpoint loses every admin route within seconds."],
  ])("builds the revoke sentence from the grants %s", (scope, body) => {
    expect(revokeBody({ name: "midpoint", scope })).toBe(body);
  });

  it("renders the canonical scope and reads grants back", () => {
    expect(canonicalScope(false, ["scim:write", "scim:read", "scim:write"])).toBe("scim:read,scim:write");
    expect(canonicalScope(true, ["audit:read"])).toBe("full");
    expect(grantsOf("full")).toEqual([]);
    expect(grantsOf(" audit:read, sessions:read ")).toEqual(["audit:read", "sessions:read"]);
    expect(hiddenTabsLine(["Configuration", "Attestation registry"])).toBe("Configuration and Attestation registry need a role that includes Overview and Settings.");
    expect(hiddenTabsLine(["Configuration"])).toBe("Configuration needs a role that includes Overview and Settings.");
  });
});

describe("the dates of the API tokens table, with the clock at 14 Nov 2026, 10:00 UTC", () => {
  beforeEach(() => {
    setTimeZone("utc");
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-14T10:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it.each([
    ["2026-11-14T09:58:00Z", "2 m ago"],
    ["2026-11-13T23:00:00Z", "11 h ago"],
    ["2026-11-13T08:00:00Z", "yesterday"],
    ["2026-11-12T23:00:00Z", "12 Nov 2026"],
    ["2026-10-12T10:00:00Z", "12 Oct 2026"],
  ])("reads a last use at %s as %s", (iso, word) => {
    expect(usedWord(iso)).toBe(word);
  });

  it.each([
    ["2027-02-05T12:00:00Z", "in 83 days", "plain"],
    ["2026-11-29T10:00:00Z", "in 15 days", "plain"],
    ["2026-11-28T10:00:00Z", "in 14 days", "warn"],
    ["2026-11-15T09:00:00Z", "in 1 day", "warn"],
    ["2026-11-14T20:00:00Z", "today", "warn"],
    ["2026-11-14T08:00:00Z", "expired today", "danger"],
    ["2026-11-13T12:00:00Z", "expired yesterday", "danger"],
    ["2026-11-11T10:00:00Z", "expired 3 days ago", "danger"],
  ])("reads an expiry at %s as %s in the %s tone", (iso, word, tone) => {
    expect(expiryOf(iso)).toEqual({ word, tone });
  });
});
