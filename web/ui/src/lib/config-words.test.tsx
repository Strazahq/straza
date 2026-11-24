import { describe, expect, it } from "vitest";
import type { ConfigAnswer } from "./api";
import {
  CHAIN_SEAT, CHANGED_TITLE, CONFIG_DENIED, DECIDED_PANEL, HOOK_WORD, LIVE_TITLE, POSTURE_TITLE, RELAXED_SENTENCE, SECTIONS, SIGNED_IN_TITLE, STALE_WHAT, STOPPED_TITLE, TILE, VALUE_UNAVAILABLE, VALUE_UNREPORTED,
  configRows, consequenceOf, fileLine, fleetLine, isPlainHTTP, lacksGrant, notesLine, postureOf, relaxedLine, retentionWords, seatLine, secondsWords, staleLine, summaryUnread, valuesOf,
} from "./config-words";

const eval09: ConfigAnswer = {
  profile: "enterprise",
  public_url: "http://localhost:8420",
  tls: false,
  store_driver: "postgres",
  events: { embedded: true },
  oidc: { external_issuer: "http://localhost:8080/realms/straza", jit_provision: false },
  governance: { min_attestation: "none", offline_grace_ttl_seconds: 300, local_tool_default: "deny", audit_backpressure: "block" },
  approval: { gateway_hold_seconds: 120, unsigned_own_decisions: false },
  admin: { second_person: false },
  apps: { gitops_dir_enabled: true, upstream_timeout_seconds: 30 },
  capture: { policy_sets: 1, mode: "verbatim", retention_hours: 720, body_store: "inline" },
};

const valueOf = (patch: Partial<ConfigAnswer>, id: string) => configRows({ ...eval09, ...patch }).find((r) => r.id === id)?.value;

describe("configRows", () => {
  it("names every row once, in section order, with the key that sets it", () => {
    const rows = configRows(eval09);
    expect(rows.map((r) => r.id)).toEqual(["profile", "public_url", "tls", "store", "events", "issuer", "jit", "floor", "grace", "local", "bp", "hold", "ownDecisions", "secondPerson", "watch", "upstream", "sets", "mode", "ret", "bodies"]);
    const order = rows.map((r) => SECTIONS.indexOf(r.section));
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    expect(rows[7].key + " " + rows[7].env).toBe("governance.minAttestation STRAZA_MIN_ATTESTATION");
    expect(rows[13]).toMatchObject({ name: "Second person for publishing", section: "Approvals and publishing", key: "admin.secondPerson", env: "", posture: "plain" });
    expect(rows[16].name).toBe("Policies that record");
    expect(fileLine(rows[2])).toBe("");
    expect(fileLine(rows[13])).toBe("admin:\n  secondPerson: false");
    expect(valuesOf(rows[13])).toEqual(["true", "false"]);
    expect(valuesOf(rows[7])).toEqual(["none", "advisory", "managed"]);
  });

  it.each<[string, Partial<ConfigAnswer>, string, string]>([
    ["sign-in creating users, false", { oidc: { jit_provision: false } }, "jit", "no"],
    ["sign-in creating users, true", { oidc: { jit_provision: true } }, "jit", "yes"],
    ["own requests without a signed device, false", { approval: { unsigned_own_decisions: false } }, "ownDecisions", "no"],
    ["own requests without a signed device, true", { approval: { unsigned_own_decisions: true } }, "ownDecisions", "yes"],
    ["second person for publishing, false", { admin: { second_person: false } }, "secondPerson", "off"],
    ["second person for publishing, true", { admin: { second_person: true } }, "secondPerson", "on"],
    ["offline grace, 0", { governance: { offline_grace_ttl_seconds: 0 } }, "grace", "none"],
    ["offline grace, 300", { governance: { offline_grace_ttl_seconds: 300 } }, "grace", "5 min"],
    ["mode, verbatim", { capture: { policy_sets: 1, mode: "verbatim" } }, "mode", "word for word"],
    ["mode, redact", { capture: { policy_sets: 1, mode: "redact" } }, "mode", "masked"],
    ["mode, mixed", { capture: { policy_sets: 2, mode: "mixed" } }, "mode", "mixed"],
    ["retention, 720 hours", { capture: { retention_hours: 720 } }, "ret", "30 days"],
    ["retention, 24 hours", { capture: { retention_hours: 24 } }, "ret", "1 day"],
    ["retention, 36 hours", { capture: { retention_hours: 36 } }, "ret", "36 hours"],
    ["retention, 1 hour", { capture: { retention_hours: 1 } }, "ret", "1 hour"],
    ["bodies, inline", { capture: { body_store: "inline" } }, "bodies", "in the database"],
    ["bodies, s3", { capture: { body_store: "s3" } }, "bodies", "in an object store"],
    ["second person for publishing, missing", { admin: undefined }, "secondPerson", VALUE_UNAVAILABLE],
  ])("reads the value of %s as a person says it", (_name, patch, id, word) => {
    expect(valueOf(patch, id)).toBe(word);
  });

  it("keeps the raw value for the lines a person copies", () => {
    const rows = configRows({ ...eval09, oidc: { jit_provision: true }, capture: { retention_hours: 720, body_store: "s3" } });
    const raw = (id: string) => rows.find((r) => r.id === id)?.raw;
    expect(raw("jit")).toBe(true);
    expect(raw("ret")).toBe("720h");
    expect(raw("bodies")).toBe("s3");
    expect(raw("secondPerson")).toBe(false);
  });

  it.each<[string, Partial<ConfigAnswer>, string, string]>([
    ["TLS off on a plain http URL", {}, "tls", RELAXED_SENTENCE.tls],
    ["the floor at none", {}, "floor", RELAXED_SENTENCE.floorNone],
    ["the floor at advisory", { governance: { min_attestation: "advisory" } }, "floor", RELAXED_SENTENCE.floorAdvisory],
    ["local tools allowed by default", { governance: { local_tool_default: "allow" } }, "local", RELAXED_SENTENCE.local],
    ["audit that drops under load", { governance: { audit_backpressure: "drop-with-counter" } }, "bp", RELAXED_SENTENCE.bp],
    ["own requests without a signed device", { approval: { unsigned_own_decisions: true } }, "ownDecisions", RELAXED_SENTENCE.ownDecisions],
  ])("says what %s means", (_name, patch, id, sentence) => {
    const row = configRows({ ...eval09, ...patch }).find((r) => r.id === id);
    expect(row?.posture).toBe("relaxed");
    expect(row?.cost).toBe(sentence);
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
    expect(strict.strictWords).toEqual(["Straza terminates TLS", "sign-in creates no users", "the attestation floor is managed", "a client cut off from Straza denies at once", "local tools deny by default", "audit blocks under load", "own requests need a signed device"]);
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
    expect(retentionWords(720)).toBe("30 days");
    expect(retentionWords(48)).toBe("2 days");
    expect(retentionWords(25)).toBe("25 hours");
  });
});

describe("Overview for a reader whose role lacks an area", () => {
  it.each([
    [DECIDED_PANEL, "config", "Tool decisions are not shown, because your role does not include Overview and Settings."],
    [STOPPED_TITLE, "config", "Calls denied or needing approval are not shown, because your role does not include Overview and Settings."],
    [POSTURE_TITLE, "config", "Security configuration is not shown, because your role does not include Overview and Settings."],
    [CHANGED_TITLE, "audit", "Recent changes are not shown, because your role does not include Audit."],
    [LIVE_TITLE, "audit", "Recent audit events are not shown, because your role does not include Audit."],
    [SIGNED_IN_TITLE, "sessions", "Active sessions are not shown, because your role does not include Sessions."],
  ])("says %s is not shown by the area %s", (title, area, want) => {
    expect(seatLine(title, area)).toBe(want);
  });

  it("names the area, never a grant, on a tile, the chain line, the configuration note and the read line", () => {
    expect(lacksGrant("approvals")).toBe("your role does not include Approvals");
    expect(CHAIN_SEAT).toBe("not checked, because your role does not include Audit");
    expect(CONFIG_DENIED).toBe("Your role cannot read the configuration. Ask an administrator for a role that can.");
    expect(summaryUnread(true)).toBe("The summary is not shown, because your role cannot read the configuration.");
    for (const s of [lacksGrant("approvals"), CHAIN_SEAT, CONFIG_DENIED, summaryUnread(true)]) expect(s).not.toMatch(/[a-z]+:(read|write)/);
  });
});

describe("Overview's other words", () => {
  it("says a session's hook configuration state in words and counts the fleet by them", () => {
    expect(HOOK_WORD).toEqual({ current: "current", allowed: "not current", mismatch: "not registered", unmeasured: "not measured" });
    expect(fleetLine(4, { current: 1, allowed: 1, mismatch: 1, unmeasured: 1 }, 0)).toBe("4 governed sessions: 1 current, 1 not current, 1 not registered, 1 not measured");
    expect(fleetLine(1, { "not reported": 1 }, 0)).toBe("1 governed session: 1 not reported");
  });

  it("says each cost worth knowing as a sentence of its own", () => {
    const notes = postureOf(configRows(eval09)).notes;
    expect(notes.length).toBeGreaterThan(1);
    const line = notesLine(notes);
    expect(line.startsWith("Worth knowing")).toBe(false);
    expect(line).not.toContain(";");
    for (const r of notes) expect(line).toContain((r.cost || "").charAt(0).toUpperCase() + (r.cost || "").slice(1).replace(/\.$/, "") + ".");
  });

  it("says what the Policies and Audit chain tiles count", () => {
    expect(TILE.policies.help).toBe("Policies that are live now, of every policy stored.");
    expect(TILE.audit.help).toBe("The browser re-hashes the newest 25 records. strazactl audit verify checks the whole chain.");
  });

  it("says a stale panel's subject and when it was read", () => {
    expect(staleLine(STALE_WHAT.config, "2 m ago")).toBe("Configuration last read 2 m ago.");
    expect(Object.values(STALE_WHAT)).toEqual(["Configuration", "Sessions", "Decision counts", "Recent changes"]);
  });
});
