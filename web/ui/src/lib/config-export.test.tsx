import { describe, expect, it } from "vitest";
import { parse } from "yaml";
import { configRows, fileLine } from "./config-words";
import { configFragment, draftScalar, draftValueError } from "./config-export";

describe("configuration exports", () => {
  it("exports real booleans, integer seconds and duration strings, never display labels", () => {
    const rows = configRows({ events: { embedded: false }, oidc: { external_issuer: "", jit_provision: false }, approval: { gateway_hold_seconds: 120 }, governance: { offline_grace_ttl_seconds: 300 }, apps: { upstream_timeout_seconds: 30 }, capture: { retention_hours: 720 } });
    const read = (id: string) => parse(fileLine(rows.find((r) => r.id === id)!));
    expect(read("events")).toEqual({ events: { embedded: false } });
    expect(read("issuer")).toEqual({ oidc: { issuer: "" } });
    expect(read("hold")).toEqual({ approval: { gatewayHoldSeconds: 120 } });
    expect(read("grace")).toEqual({ governance: { offlineGraceTTL: "300s" } });
    expect(read("upstream")).toEqual({ apps: { upstreamTimeout: "30s" } });
    expect(read("ret")).toEqual({ governance: { captureRetention: "720h" } });
  });

  it("does not export redacted, derived or unavailable values", () => {
    const rows = configRows({ tls: false, apps: { gitops_dir_enabled: true }, capture: { policy_sets: 3, mode: "mixed" } });
    for (const row of rows) expect(fileLine(row)).toBe("");
    expect(rows.find((r) => r.id === "jit")?.value).toBe("Unavailable");
    expect(rows.find((r) => r.id === "jit")?.posture).toBe("plain");
  });

  it("preserves strings containing YAML syntax without injecting settings", () => {
    const value = "https://issuer/yes#fragment\nserver:\n  publicUrl: false";
    expect(parse(configFragment([{ key: "oidc.issuer", value }]))).toEqual({ oidc: { issuer: value } });
    expect(parse(configFragment([{ key: "server.tls.keyFile", value: "false" }]))).toEqual({ server: { tls: { keyFile: "false" } } });
  });

  it("validates numeric and duration drafts before copying", () => {
    expect(draftScalar("hold", "120")).toBe(120);
    expect(draftScalar("events", "false")).toBe(false);
    for (const value of ["2 min", "-1", "NaN", "1.5"]) expect(draftValueError("hold", value)).not.toBe("");
    expect(draftValueError("grace", "2 min")).not.toBe("");
    expect(draftValueError("grace", "1h30m")).toBe("");
  });
});
