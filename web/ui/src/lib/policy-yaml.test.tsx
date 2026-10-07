import { describe, expect, it } from "vitest";
import { YamlError, asList, asMap, emitPolicy, parsePolicy } from "./policy-yaml";

const ACCESS = [
  "apiVersion: straza.dev/v1beta1",
  "kind: PolicySet",
  "metadata:",
  "  name: dev-tools-access",
  "  description: \"Gates for the role dev-tools, written from the role page\"",
  "spec:",
  "  priority: 100",
  "  match:",
  "    roles: [dev-tools]",
  "  rules:",
  "    - id: demo-tools-get-sum-approve",
  "      tools: [mcp.call]",
  "      apps: [demo-tools]",
  "      toolNames:",
  "        allow: [get-sum]",
  "      mode: approve",
  "      approve:",
  "        deciders: [sponsor]",
  "        timeoutSeconds: 90",
  "      effect: allow",
  "      reason: \"get-sum needs approval, set on the role page\"",
  "",
].join("\n");

describe("policy-yaml", () => {
  it("parses a stored access set into values and emits it back byte for byte", () => {
    const doc = parsePolicy(ACCESS);
    const spec = asMap(asMap(doc).spec);
    expect(asMap(asMap(doc).metadata).name).toBe("dev-tools-access");
    expect(asMap(spec.match).roles).toEqual(["dev-tools"]);
    const rules = asList(spec.rules);
    expect(rules).toHaveLength(1);
    const rule = asMap(rules[0]);
    expect(rule.id).toBe("demo-tools-get-sum-approve");
    expect(asMap(rule.approve).timeoutSeconds).toBe(90);
    expect(emitPolicy(doc)).toBe(ACCESS);
  });

  it("reads comments, quotes, flow maps that wrap, booleans and nulls", () => {
    const text = [
      "---",
      "# the set",
      "metadata: { name: \"a b\", description: 'it''s' } # trailing",
      "spec:",
      "  rules:",
      "    - id: one",
      "      toolNames: { allow: [x,",
      "        y] }",
      "      require: { attestation: true }",
      "      obligations:",
      "      escape: ~",
      "",
    ].join("\n");
    const doc = asMap(parsePolicy(text));
    expect(asMap(doc.metadata)).toEqual({ name: "a b", description: "it's" });
    const rule = asMap(asList(asMap(doc.spec).rules)[0]);
    expect(asMap(rule.toolNames).allow).toEqual(["x", "y"]);
    expect(asMap(rule.require).attestation).toBe(true);
    expect(rule.obligations).toBeNull();
    expect(rule.escape).toBeNull();
  });

  it("orders keys the spec way, quotes what needs quoting, and drops empty collections", () => {
    const out = emitPolicy({ spec: { rules: [{ reason: "a: b", effect: "deny", id: "x", apps: [] }], match: { roles: ["r"] } }, kind: "PolicySet", metadata: { name: "n" } });
    expect(out).toBe(["kind: PolicySet", "metadata:", "  name: n", "spec:", "  match:", "    roles: [r]", "  rules:", "    - id: x", "      effect: deny", "      reason: \"a: b\"", ""].join("\n"));
  });

  it("refuses tabs, block scalars, duplicate keys and a second document with the line", () => {
    expect(() => parsePolicy("a:\n\tb: 1")).toThrow(YamlError);
    expect(() => parsePolicy("a: |\n  text")).toThrow(/block scalars.*line 1/);
    expect(() => parsePolicy("a: 1\na: 2")).toThrow(/duplicate key a \(line 2\)/);
    expect(() => parsePolicy("a: 1\n---\nb: 2")).toThrow(/multiple YAML documents/);
    expect(() => parsePolicy("")).toThrow(/empty document/);
  });
});
