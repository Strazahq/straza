import { describe, expect, it } from "vitest";
import type { ConfigAnswer } from "./api";
import { SECTIONS, VALUE_UNAVAILABLE, VALUE_UNREPORTED, configRows, consequenceOf, fileLine, hoursWords, isPlainHTTP, postureOf, relaxedLine, secondsWords, setWords, valuesOf, whereWords } from "./config-words";

const eval09: ConfigAnswer = {
  profile: "enterprise",
  public_url: "http://localhost:8420",
  tls: false,
  store_driver: "postgres",
  events: { embedded: true },
  oidc: { external_issuer: "http://localhost:8080/realms/straza", jit_provision: false },
  governance: { min_attestation: "none", offline_grace_ttl_seconds: 300, local_tool_default: "deny", audit_backpressure: "block" },
  approval: { gateway_hold_seconds: 120, unsigned_own_decisions: false },
  apps: { gitops_dir_enabled: true, upstream_timeout_seconds: 30 },
  capture: { policy_sets: 1, mode: "verbatim", retention_hours: 720, body_store: "inline" },
};

describe("configRows", () => {
  it("names every row once, in section order, with the key that sets it", () => {
    const rows = configRows(eval09);
    expect(rows.map((r) => r.id)).toEqual(["profile", "public_url", "tls", "store", "events", "issuer", "jit", "floor", "grace", "local", "bp", "hold", "ownDecisions", "watch", "upstream", "sets", "mode", "ret", "bodies"]);
    const order = rows.map((r) => SECTIONS.indexOf(r.section));
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    expect(whereWords(rows[7])).toBe("governance.minAttestation · STRAZA_MIN_ATTESTATION");
    expect(whereWords(rows[8])).toBe("governance.offlineGraceTTL");
    expect(whereWords(rows[15])).toBe("derived from the active policy sets");
    expect(setWords(rows[7])).toBe("Set governance.minAttestation in the config file or STRAZA_MIN_ATTESTATION in the environment.");
    expect(setWords(rows[9])).toBe("Set governance.localToolDefault in the config file or the chart values.");
    expect(fileLine(rows[2])).toBe("");
  });

  it.each<[string, Partial<ConfigAnswer>, string[], string[]]>([
    ["the eval stack: floor none and plain http", {}, ["tls", "floor"], ["events", "grace", "watch"]],
    ["own requests accepted without a signed device is relaxed", { approval: { gateway_hold_seconds: 120, unsigned_own_decisions: true } }, ["tls", "floor", "ownDecisions"], ["events", "grace", "watch"]],
    ["TLS at an https ingress is a note, not relaxed", { public_url: "https://straza.example" }, ["floor"], ["tls", "events", "grace", "watch"]],
    ["strazad terminating TLS is strict", { tls: true, public_url: "https://straza.example" }, ["floor"], ["events", "grace", "watch"]],
    ["advisory still counts as relaxed", { governance: { min_attestation: "advisory", offline_grace_ttl_seconds: 0, local_tool_default: "deny", audit_backpressure: "block" } }, ["tls", "floor"], ["events", "watch"]],
    ["allow by default and dropped audit are relaxed, sign-in creating users is a note", { governance: { min_attestation: "managed", offline_grace_ttl_seconds: 0, local_tool_default: "allow", audit_backpressure: "drop-with-counter" }, oidc: { external_issuer: "", jit_provision: true } }, ["tls", "local", "bp"], ["events", "jit", "watch"]],
  ])("reads the posture: %s", (_name, patch, relaxed, notes) => {
    const p = postureOf(configRows({ ...eval09, ...patch }));
    expect(p.relaxed.map((r) => r.id)).toEqual(relaxed);
    expect(p.notes.map((r) => r.id)).toEqual(notes);
  });

  it("folds the strict values into words and names the relaxed ones", () => {
    const strict = postureOf(configRows({ ...eval09, tls: true, public_url: "https://x", governance: { min_attestation: "managed", offline_grace_ttl_seconds: 0, local_tool_default: "deny", audit_backpressure: "block" } }));
    expect(strict.relaxed).toEqual([]);
    expect(strict.strictWords).toEqual(["strazad terminates TLS", "sign-in creates no users", "the attestation floor is managed", "an unreachable server denies at once", "local tools deny by default", "audit blocks under load", "own requests need a signed device"]);
    expect(relaxedLine(postureOf(configRows(eval09)).relaxed)).toBe("2 governance settings are relaxed: transport, client attestation floor.");
    expect(isPlainHTTP(eval09)).toBe(true);
  });

  it("reads the mode the server omits when no policy records sessions as none", () => {
    const rows = configRows({ ...eval09, capture: { policy_sets: 0, retention_hours: 720, body_store: "inline" } });
    expect(rows.find((r) => r.id === "mode")?.value).toBe("none");
    expect(rows.filter((r) => r.value === "Unavailable")).toEqual([]);
    expect(configRows({ ...eval09, capture: undefined }).find((r) => r.id === "mode")?.value).toBe("Unavailable");
  });

  it("says in a row's help why its value is missing and where to read it, keeping the row's own sentences", () => {
    const whole = configRows(eval09).find((r) => r.id === "floor");
    const floor = configRows({ ...eval09, governance: undefined }).find((r) => r.id === "floor");
    expect(floor?.value).toBe(VALUE_UNAVAILABLE);
    expect(floor?.help).toBe(whole?.help + " " + VALUE_UNREPORTED);
    expect(floor && valuesOf(floor)).toEqual(whole && valuesOf(whole));
    expect(floor && consequenceOf(floor)).toBe(whole && consequenceOf({ ...whole, cost: undefined }));
    expect(whole?.help).not.toContain(VALUE_UNREPORTED);
  });

  it("reads durations the way a person says them", () => {
    expect(secondsWords(0)).toBe("0");
    expect(secondsWords(45)).toBe("45 s");
    expect(secondsWords(300)).toBe("5 min");
    expect(secondsWords(7200)).toBe("2 h");
    expect(secondsWords(259200)).toBe("3 d");
    expect(hoursWords(720)).toBe("720 h (30 d)");
    expect(hoursWords(24)).toBe("24 h");
  });
});
