import { describe, expect, it } from "vitest";
import { readImport } from "@/components/wizard/import-reader";
import type { ManifestDoc } from "./api";
import { toYAML } from "./manifest";
import {
  type Connection, type CredentialEdit, type Settings, changeRows, coveredBy, editConnection, editCredential, editSettings, injectMoves, joinArgs,
  readConnection, readCredential, readSettings, splitArgs,
} from "./manifest-edit";

// The fixtures are stored manifests as GET /v1/admin/apps answers them:
// the JSON of the parsed app.yaml with the server's defaults applied.
const MIDPOINT: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "midpoint", description: "midPoint IGA operations, per-user identity." },
  server: { name: "io.github.evolveum/midpoint-mcp", version: "0.3.1-dev", description: "midPoint over MCP", remotes: [{ type: "streamable-http", url: "http://mcp-midpoint-http:3001/mcp" }] },
  straza: {
    runtime: { kind: "remote", remote: { url: "http://mcp-midpoint-http:3001/mcp", auth: "inject" } },
    credential: { kind: "oauth", agents: "own", oauth: { provider: "keycloak", scopes: ["openid"] }, inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } },
    exposure: { tools: ["*"] },
    limits: { rps: 10 },
  },
};
const SCOUT: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "scout-tools", description: "Search and read the team's scouting notes." },
  server: { name: "scout-tools", version: "0" },
  straza: {
    runtime: { kind: "remote", remote: { url: "http://scout-tools.internal:8080/mcp", auth: "inject" } },
    credential: { kind: "static", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } },
    exposure: { tools: ["*"] },
  },
};
const ECHO: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "u3-echo" },
  server: { name: "io.github.straza/u3-echo", version: "0.1.0" },
  straza: {
    runtime: { kind: "command", command: { exec: "/var/lib/straza/u3-echo", args: ["--stdio"], env: [{ name: "LOG_LEVEL", value: "info" }], workdir: "/var/lib/straza" } },
    credential: { kind: "static", inject: { as: "env", name: "ECHO_TOKEN", template: "{{secret}}" } },
    exposure: { tools: ["*"] },
  },
};
const CUSTOM: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "legacy-api" },
  server: { name: "legacy-api", version: "2" },
  straza: {
    runtime: { kind: "remote", remote: { url: "https://legacy.internal/mcp", auth: "inject" } },
    credential: { kind: "static", inject: { as: "header", name: "Authorization", template: "Token {{secret}}" } },
    exposure: { tools: ["*"] },
  },
};
const PATTERNS: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "notes", namespace: "team-a" },
  server: { name: "notes", version: "1.4.0" },
  straza: {
    runtime: { kind: "remote", remote: { url: "http://notes.internal/mcp", auth: "passthrough" } },
    exposure: { tools: ["read_*", "search_notes", "gone_tool"] },
    limits: { cpu: "500m", mem: "256Mi", rps: 5 },
  },
};
const FIXTURES: [string, ManifestDoc][] = [["midpoint", MIDPOINT], ["scout-tools", SCOUT], ["u3-echo", ECHO], ["legacy-api", CUSTOM], ["notes", PATTERNS]];
const TOOLS = ["read_note", "search_notes", "list_tags", "create_note", "delete_note"];

const copy = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
// without drops the dotted paths from a copy, so what remains is what an
// edit must leave exactly as installed.
function without(doc: ManifestDoc, paths: string[]): unknown {
  const out = copy(doc) as Record<string, unknown>;
  for (const p of paths) {
    const keys = p.split(".");
    let at: Record<string, unknown> | undefined = out;
    for (const k of keys.slice(0, -1)) at = at && (at[k] as Record<string, unknown> | undefined);
    if (at) delete at[keys[keys.length - 1]];
  }
  return out;
}

const conn = (doc: ManifestDoc, p: Partial<Connection>) => editConnection(doc, { ...readConnection(doc), ...p });
const cred = (doc: ManifestDoc, p: Partial<CredentialEdit>) => editCredential(doc, { ...readCredential(doc), ...p });
const sets = (doc: ManifestDoc, p: Partial<Settings>) => editSettings(doc, { ...readSettings(doc, TOOLS), ...p }, TOOLS);

describe("the Change sheets' edit model", () => {
  it.each(FIXTURES)("gives back %s unchanged from a read then a write with no change, and never touches the input", (_, doc) => {
    const before = copy(doc);
    expect(editConnection(doc, readConnection(doc))).toEqual(before);
    expect(editCredential(doc, readCredential(doc))).toEqual(before);
    expect(editSettings(doc, readSettings(doc, TOOLS), TOOLS)).toEqual(before);
    expect(doc).toEqual(before);
  });

  const OWN = { connection: ["straza.runtime"], credential: ["straza.credential"], settings: ["metadata.description", "straza.exposure", "straza.limits"] };
  const EDITS: [string, keyof typeof OWN, ManifestDoc, () => ManifestDoc][] = [
    ["a new address on midpoint", "connection", MIDPOINT, () => conn(MIDPOINT, { url: "https://midpoint.internal/mcp" })],
    ["a new argument on u3-echo", "connection", ECHO, () => conn(ECHO, { args: "--stdio --verbose" })],
    ["a new variable on u3-echo", "connection", ECHO, () => conn(ECHO, { env: [{ name: "LOG_LEVEL", value: "debug" }, { name: "TZ", value: "UTC" }] })],
    ["each caller's own token on scout-tools", "credential", SCOUT, () => cred(SCOUT, { kind: "token", agents: "shared" })],
    ["new scopes on midpoint", "credential", MIDPOINT, () => cred(MIDPOINT, { scopes: "openid profile" })],
    ["a new header shape on legacy-api", "credential", CUSTOM, () => cred(CUSTOM, { sent: "header", headerName: "X-Legacy-Key" })],
    ["picked tools and a timeout on midpoint", "settings", MIDPOINT, () => sets(MIDPOINT, { expose: "only", picked: ["read_note"], timeout: "45", desc: "" })],
    ["no rate limit on notes", "settings", PATTERNS, () => sets(PATTERNS, { rps: "" })],
  ];
  it.each(EDITS)("changes only its own block for %s", (_, card, doc, edit) => {
    const after = edit();
    expect(without(after, OWN[card])).toEqual(without(doc, OWN[card]));
    expect(after).not.toEqual(doc);
  });

  it("keeps every key of the installed block that the card does not show", () => {
    const mp = conn(MIDPOINT, { url: "https://midpoint.internal/mcp" });
    expect(mp.straza?.runtime).toEqual({ kind: "remote", remote: { url: "https://midpoint.internal/mcp", auth: "inject" } });
    const echo = conn(ECHO, { args: "--stdio --verbose" });
    expect(echo.straza?.runtime?.command).toEqual({ exec: "/var/lib/straza/u3-echo", args: ["--stdio", "--verbose"], env: [{ name: "LOG_LEVEL", value: "info" }], workdir: "/var/lib/straza" });
    const scopes = cred(MIDPOINT, { scopes: "openid profile" }).straza?.credential;
    expect(scopes).toEqual({ kind: "oauth", agents: "own", oauth: { provider: "keycloak", scopes: ["openid", "profile"] }, inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } });
  });

  it("drops the old runtime block when the kind changes and writes the new one", () => {
    const oci = conn(ECHO, { kind: "oci", image: "ghcr.io/straza/u3-echo:0.1.0" });
    expect(oci.straza?.runtime).toEqual({ kind: "oci", oci: { image: "ghcr.io/straza/u3-echo:0.1.0" } });
    expect(oci.straza?.credential).toEqual(ECHO.straza?.credential);
    expect(conn(ECHO, { kind: "oci", image: "x" }).straza?.runtime?.command).toBeUndefined();
    expect(conn(conn(ECHO, { kind: "remote" }), {}).straza?.runtime?.kind).toBe("remote");
    expect(editConnection(ECHO, { ...readConnection(ECHO), kind: "oci", image: "x", exec: "changed" })).toEqual(conn(ECHO, { kind: "oci", image: "x" }));
    expect(editConnection(ECHO, { ...readConnection(ECHO), kind: "command" })).toEqual(ECHO);
  });

  const MOVES: [string, ManifestDoc, Partial<Connection>, NonNullable<ManifestDoc["straza"]>["credential"]][] = [
    ["from HTTP to a command, into the variable the sheet asks for", SCOUT, { kind: "command", exec: "/usr/local/bin/scout", envName: " SCOUT_TOKEN " },
      { kind: "static", inject: { as: "env", name: "SCOUT_TOKEN", template: "{{secret}}" } }],
    ["from a command to HTTP, into the named header the sheet asks for", ECHO, { kind: "remote", url: "https://echo.internal/mcp", sent: "header", headerName: "X-Echo-Key" },
      { kind: "static", inject: { as: "header", name: "X-Echo-Key", template: "{{secret}}" } }],
    ["from a command to HTTP, as a bearer token", ECHO, { kind: "remote", url: "https://echo.internal/mcp", sent: "bearer" },
      { kind: "static", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } }],
    ["from a command to a container, where the variable still fits", ECHO, { kind: "oci", image: "ghcr.io/straza/u3-echo:0.1.0" }, ECHO.straza?.credential],
  ];
  it.each(MOVES)("rewrites how the shared secret travels %s", (_, doc, p, want) => {
    const after = conn(doc, p);
    expect(after.straza?.credential).toEqual(want);
    expect(injectMoves(doc, p.kind || "")).toBe(JSON.stringify(want) !== JSON.stringify(doc.straza?.credential));
  });

  it("keeps a template the cards cannot write unless the sent-as choice or the kind changes", () => {
    const read = readCredential(CUSTOM);
    expect(read.sent).toBe("keep");
    expect(cred(CUSTOM, {}).straza?.credential).toEqual(CUSTOM.straza?.credential);
    expect(conn(CUSTOM, { url: "https://legacy.example/mcp" }).straza?.credential).toEqual(CUSTOM.straza?.credential);
    expect(cred(CUSTOM, { sent: "bearer" }).straza?.credential?.inject).toEqual({ as: "header", name: "Authorization", template: "Bearer {{secret}}" });
    expect(cred(CUSTOM, { kind: "token", sent: "bearer" }).straza?.credential).toEqual({ kind: "token", agents: "own", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } });
  });

  const KINDS: [string, Partial<CredentialEdit>, NonNullable<ManifestDoc["straza"]>["credential"] | undefined][] = [
    ["none drops the block", { kind: "none" }, undefined],
    ["a token server takes agents and the sent-as shape", { kind: "token", agents: "sponsor", sent: "header", headerName: "X-Scout-Token" },
      { kind: "token", agents: "sponsor", inject: { as: "header", name: "X-Scout-Token", template: "{{secret}}" } }],
    ["a sign-in server always sends a bearer header", { kind: "oauth", agents: "own", provider: "keycloak", scopes: "openid, profile" },
      { kind: "oauth", agents: "own", oauth: { provider: "keycloak", scopes: ["openid", "profile"] }, inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } }],
  ];
  it.each(KINDS)("writes a new credential kind in the wizard's shape: %s", (_, p, want) => {
    expect(cred(SCOUT, p).straza?.credential).toEqual(want);
  });

  it("keeps patterns and names the checkboxes cannot show, and cpu and mem with them", () => {
    const read = readSettings(PATTERNS, TOOLS);
    expect(read).toEqual({ desc: "", expose: "only", picked: ["search_notes"], kept: ["read_*", "gone_tool"], rps: "5", timeout: "" });
    const more = sets(PATTERNS, { picked: ["read_note", "search_notes"] });
    expect(more.straza?.exposure?.tools).toEqual(["read_*", "search_notes", "gone_tool", "read_note"]);
    expect(more.straza?.limits).toEqual({ cpu: "500m", mem: "256Mi", rps: 5 });
    expect(sets(PATTERNS, { rps: "", timeout: "45" }).straza?.limits).toEqual({ cpu: "500m", mem: "256Mi", timeoutSeconds: 45 });
    expect(sets(PATTERNS, { rps: "" }).straza?.limits).toEqual({ cpu: "500m", mem: "256Mi" });
    expect(sets(PATTERNS, { expose: "all" }).straza?.exposure).toEqual({ tools: ["*"] });
    expect(sets(MIDPOINT, { rps: "" }).straza?.limits).toBeUndefined();
    expect(sets(SCOUT, { rps: "2.5" }).straza?.limits).toEqual({ rps: 2.5 });
    expect(sets(MIDPOINT, { desc: "  Reads allowed.  " }).metadata).toEqual({ name: "midpoint", description: "Reads allowed." });
    expect(sets(MIDPOINT, { desc: "" }).metadata).toEqual({ name: "midpoint" });
  });

  it("writes YAML that reads back in the shape the server expects", () => {
    const moved = readImport(toYAML(conn(SCOUT, { kind: "command", exec: "/usr/local/bin/scout", args: '--stdio --title "Scout notes"', envName: "SCOUT_TOKEN", env: [{ name: "TZ", value: "UTC" }] })));
    expect(moved).toMatchObject({
      kind: "command", exec: "/usr/local/bin/scout", args: ["--stdio", "--title", "Scout notes"], env: [{ name: "TZ", value: "UTC" }],
      credential: { kind: "static", as: "env", name: "SCOUT_TOKEN", template: "{{secret}}" },
    });
    expect(toYAML(conn(ECHO, { args: "--stdio --verbose" }))).toBe([
      "apiVersion: straza.dev/v1beta1",
      "kind: App",
      "metadata:",
      "  name: u3-echo",
      "server:",
      "  name: io.github.straza/u3-echo",
      "  version: 0.1.0",
      "straza:",
      "  runtime:",
      "    kind: command",
      "    command:",
      "      exec: /var/lib/straza/u3-echo",
      "      args:",
      '        - "--stdio"',
      '        - "--verbose"',
      "      env:",
      "        - name: LOG_LEVEL",
      "          value: info",
      "      workdir: /var/lib/straza",
      "  credential:",
      "    kind: static",
      "    inject:",
      "      as: env",
      "      name: ECHO_TOKEN",
      '      template: "{{secret}}"',
      "  exposure:",
      "    tools:",
      '      - "*"',
      "",
    ].join("\n"));
    const limits = toYAML(sets(PATTERNS, { rps: "10", timeout: "45" }));
    expect(limits).toContain("  limits:\n    cpu: 500m\n    mem: 256Mi\n    rps: 10\n    timeoutSeconds: 45\n");
  });

  // A value a YAML decoder resolves to another type has to travel quoted.
  // The registry record is stored verbatim, so rewriting one of its values
  // would change what the record says, and .inf or .nan would be refused.
  const QUOTED = [
    ["a date", "2025-09-09"], ["a timestamp", "2025-09-09T10:30:00Z"], ["hexadecimal", "0x1F"], ["octal", "0o17"], ["binary", "0b1010"],
    ["an exponent", "1e3"], ["a leading dot float", ".5"], ["a trailing dot float", "1."], ["an underscored number", "1_000"],
    ["a signed number", "-12"], ["infinity", ".inf"], ["negative infinity", "-.Inf"], ["not a number", ".NaN"],
    ["a boolean word", "yes"], ["a boolean word in caps", "OFF"], ["the short boolean", "n"], ["true", "True"],
    ["null", "Null"], ["a tilde", "~"], ["a plain integer", "42"], ["the secret placeholder", "Bearer {{secret}}"],
  ];
  it.each(QUOTED)("quotes a value a decoder would read as something other than a string: %s", (_, value) => {
    expect(toYAML({ server: { field: value } })).toBe('server:\n  field: ' + JSON.stringify(value) + "\n");
  });

  const PLAIN = [
    ["a URL", "http://scout-tools.internal:8080/mcp"], ["an executable path", "/usr/local/bin/mcp-server"], ["a name with a dash", "scout-tools"],
    ["a version with three parts", "0.3.1-dev"], ["an image reference", "ghcr.io/org/server:1.2"], ["an api version", "straza.dev/v1beta1"],
    ["a tool name", "read_note"], ["a sentence", "Search and read the team's scouting notes."], ["a variable name", "SCOUT_TOKEN"],
  ];
  it.each(PLAIN)("keeps a plain string plain: %s", (_, value) => {
    expect(toYAML({ field: value })).toBe("field: " + value + "\n");
  });

  it("carries a registry record's own values through an edit unchanged", () => {
    const dated: ManifestDoc = { ...SCOUT, server: { name: "scout-tools", version: "1.4", published: "2025-09-09", weight: "0x1F" } };
    const yaml = toYAML(conn(dated, { url: "https://scout-tools.internal/mcp" }));
    expect(yaml).toContain('  published: "2025-09-09"\n');
    expect(yaml).toContain('  weight: "0x1F"\n');
    expect(yaml).toContain('  version: "1.4"\n');
  });

  it("reads an exposure pattern the way the manager's glob does", () => {
    expect(coveredBy(["read_*"], "read_note")).toBe("read_*");
    expect(coveredBy(["read_*"], "search_notes")).toBe("");
    expect(coveredBy(["*_note", "gone_tool"], "read_note")).toBe("*_note");
    expect(coveredBy(["get.*"], "get.one")).toBe("get.*");
    expect(coveredBy(["get.*"], "getxone")).toBe("");
    expect(coveredBy(["gone_tool"], "gone_tool")).toBe("");
  });

  it("names each changed field with its words now and after saving", () => {
    expect(changeRows(SCOUT, conn(SCOUT, { url: "https://scout-tools.internal/mcp" }))).toEqual([["Address", "http://scout-tools.internal:8080/mcp", "https://scout-tools.internal/mcp"]]);
    expect(changeRows(SCOUT, conn(SCOUT, { kind: "command", exec: "/usr/local/bin/scout", envName: "SCOUT_TOKEN" })).map((r) => r[0])).toEqual(["Transport", "Executable", "Sent as"]);
    expect(changeRows(ECHO, conn(ECHO, { kind: "oci", image: "ghcr.io/straza/u3-echo:0.1.0" }))).toEqual([
      ["Transport", "stdio, a process Straza starts on the strazad host", "A container Straza runs with docker on the strazad host"],
      ["Environment", "LOG_LEVEL=info", "none"],
      ["Image", "none", "ghcr.io/straza/u3-echo:0.1.0"],
    ]);
    expect(changeRows(SCOUT, cred(SCOUT, { kind: "token", agents: "shared" }))).toEqual([
      ["Type", "One shared secret", "Each caller's own token"],
      ["Sent as", "The Authorization header, as Bearer and the secret.", "The Authorization header, as Bearer and the person's token."],
      ["Agents with nothing of their own", "not asked", "Use this server's shared secret (agents shared)."],
    ]);
    expect(changeRows(MIDPOINT, sets(MIDPOINT, { rps: "", timeout: "45", expose: "only", picked: ["read_note", "search_notes"] }), 30)).toEqual([
      ["Tools exposed", "All tools, including ones it adds later.", "Only read_note, search_notes."],
      ["Rate limit", "10 calls per second, per session.", "Not limited."],
      ["Per-call timeout", "30 s per call, the server-wide default.", "45 s per call."],
    ]);
    expect(changeRows(MIDPOINT, MIDPOINT)).toEqual([]);
  });

  it("writes arguments as words and reads the same arguments back", () => {
    const args = ["--title", "My Server", "", 'say "hi"', "C:\\tools"];
    expect(joinArgs(args)).toBe('--title "My Server" "" "say \\"hi\\"" C:\\tools');
    expect(splitArgs(joinArgs(args))).toEqual(args);
    expect(splitArgs("  --stdio   --port 8080 ")).toEqual(["--stdio", "--port", "8080"]);
  });
});
