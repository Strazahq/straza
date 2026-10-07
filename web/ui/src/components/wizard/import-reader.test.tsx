import { describe, expect, it } from "vitest";
import { credentialFields, importedDoc, readImport } from "./import-reader";
import { EMPTY_FORM, buildManifest, manifestYAML } from "@/lib/manifest";
import envDemo from "./testdata/import-env-demo.yaml?raw";
import envDemoRecord from "./testdata/import-env-demo.server.json";
import obsidian from "./testdata/import-obsidian.yaml?raw";

// The fixtures are the import endpoint's own answers on eval, so the reader
// is pinned to what gopkg.in/yaml.v3 writes: four-space indent, sequences
// under their key, single-quoted scalars, and the verbatim record under
// server: with keys that repeat the runtime's.
describe("the import card's reader", () => {
  it("reads a command runtime with its env entries and the credential the secret variable became", () => {
    const read = readImport(envDemo);
    expect(read).toEqual({
      kind: "command", url: "", exec: "npx", args: ["-y", "env-demo@1.0.0"], image: "",
      env: [{ name: "LOG_LEVEL", value: "info" }, { name: "MODE", value: "read only: yes" }],
      workdir: "", namespace: "io.github.example",
      credential: { kind: "static", as: "env", name: "API_KEY", template: "{{secret}}" },
    });
  });

  it("reads a remote runtime and a header credential, and never takes the record's url for the runtime's", () => {
    const read = readImport(obsidian);
    expect(read.kind).toBe("remote");
    expect(read.url).toBe("https://server.smithery.ai/@Hint-Services/obsidian-github-mcp/mcp");
    expect(read.namespace).toBe("ai.smithery");
    expect(read.env).toEqual([]);
    expect(read.credential).toEqual({ kind: "static", as: "header", name: "Authorization", template: "Bearer {{secret}}" });
  });

  it("maps the credentials the cards write, and answers null for one they cannot", () => {
    expect(credentialFields(readImport(envDemo).credential)).toEqual({ cred: "static", sentAs: "env", envName: "API_KEY" });
    expect(credentialFields(readImport(obsidian).credential)).toEqual({ cred: "static", sentAs: "bearer" });
    expect(credentialFields({ kind: "static", as: "header", name: "X-Api-Key", template: "{{secret}}" })).toEqual({ cred: "static", sentAs: "header", headerName: "X-Api-Key" });
    expect(credentialFields({ kind: "static", as: "header", name: "Authorization", template: "Token {{secret}}" })).toBeNull();
    expect(credentialFields(null)).toEqual({});
  });

  it("keeps the env entries, the namespace and the verbatim record when the form builds the manifest", () => {
    const read = readImport(envDemo);
    const doc = importedDoc(read, envDemoRecord, "env-demo", "command");
    const f = { ...EMPTY_FORM, name: "env-demo", mode: "command", runtime: "command", exec: "npx", args: "-y env-demo@1.0.0", ...credentialFields(read.credential), secret: "s", imported: { doc, text: envDemo } };
    const yaml = manifestYAML(buildManifest(f));
    for (const want of ["namespace: io.github.example", "  - name: LOG_LEVEL", '    value: "read only: yes"', "name: API_KEY", "as: env", '"$schema"'.slice(1, -1) + ": https://static.modelcontextprotocol.io"]) {
      expect(yaml).toContain(want);
    }
    expect(yaml).toContain('description: "A record with a fixed environment entry and one secret.\\nSecond line."');
  });

  it("leaves what it cannot read empty", () => {
    expect(readImport("straza:\n  runtime:\n    kind: oci\n    oci:\n      image: |\n        ghcr.io/x\n")).toMatchObject({ kind: "oci", image: "", credential: null });
  });
});
