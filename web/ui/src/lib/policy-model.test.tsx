import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { parse } from "yaml";
import { describe, expect, it } from "vitest";
import { addRule, buildRule, docProblems, docText, moveRule, newPolicyDoc, openDoc, posturesOf, readSet, removeRule, ruleView, rulesOf, setApprove, setCapture, setClass, setMatchRoles, setNames, setPosture, setReason, slug, uniqueId } from "./policy-model";
import { countTitle, durationWords, foldSummary, outcomeWords, sentence, subjectWords, whatChanges } from "./policy-words";

const repo = (rel: string) => fileURLToPath(new URL("../../../../" + rel, import.meta.url));
const seed = readFileSync(repo("web/ui/src/test/testdata/dev-guardrails.yaml"), "utf8");
const examplesDir = repo("spec/policyset/examples");

const commentLines = (text: string) => text.split("\n").map((l) => l.trim()).filter((l) => l.startsWith("#"));

describe("a hold that stores no window", () => {
  const view = (rule: Record<string, unknown>) => ruleView({ id: "r", tools: ["mcp.call"], ...rule }, 0);

  it("reads the engine's 90 seconds, and says so in the rule's sentence", () => {
    const hold = view({ effect: "allow", mode: "approve", approve: { roles: ["sec-approvers"] } });
    expect([hold.posture, hold.timeoutSeconds]).toEqual(["hold", 90]);
    expect(sentence(hold)).toContain("within 90 seconds");
  });

  it("reads any other rule's window as the 2 minutes a switch to hold writes", () => {
    const ticket = view({ effect: "allow", mode: "approve", approve: { class: "ticket" } });
    const deny = view({ effect: "deny" });
    expect([ticket.posture, ticket.timeoutSeconds, deny.posture, deny.timeoutSeconds]).toEqual(["ticket", 120, "deny", 120]);
  });
});

describe("reading the seed", () => {
  const doc = openDoc(seed);
  const set = readSet(doc);
  const byId = Object.fromEntries(set.rules.map((r) => [r.id, r]));

  it("parses clean and reads the set's facts", () => {
    expect(docProblems(doc)).toEqual([]);
    expect(set.name).toBe("dev-guardrails");
    expect(set.priority).toBe(150);
    expect(set.roles).toEqual(["dev-demo-tools", "dev-midpoint"]);
    expect(set.matchOther).toBe(false);
    expect(set.capture).toBe("verbatim");
    expect(set.escape).toBe(false);
    expect(set.rules.map((r) => r.id)).toEqual([
      "dev-mcp-sum-approval-showcase", "dev-mcp-env-ticket", "dev-mcp-demo-tools", "dev-straza-approval-tools", "dev-mcp-midpoint-read",
      "dev-mcp-midpoint-workitem-selfapprove", "dev-mcp-midpoint-write-approve", "dev-deploy-ticket", "dev-local-tools", "no-rm-rf", "protect-secrets",
    ]);
  });

  it("reads each rule's lane, posture, subject and pool", () => {
    const hold = byId["dev-mcp-sum-approval-showcase"];
    expect([hold.lane, hold.posture, hold.bucket, hold.app, hold.names, hold.namesKey]).toEqual(["mcp", "hold", "hum", "demo-tools", ["get-sum"], "toolNames.allow"]);
    expect(hold.who).toEqual({ sponsor: true, roles: [] });
    expect(hold.timeoutSeconds).toBe(120);
    expect(hold.events).toEqual(["tool.pre"]);
    expect(hold.extra).toEqual([]);

    const ticket = byId["dev-mcp-env-ticket"];
    expect([ticket.posture, ticket.ticketTTLSeconds, ticket.grantTTLSeconds]).toEqual(["ticket", 86400, 3600]);

    const every = byId["dev-mcp-demo-tools"];
    expect([every.posture, every.app, every.names]).toEqual(["allow", "demo-tools", null]);

    const team = byId["dev-mcp-midpoint-workitem-selfapprove"];
    expect(team.who).toEqual({ sponsor: false, roles: ["sec-approvers"] });
    expect(team.notify).toEqual(["push", "console"]);
    expect(team.timeoutSeconds).toBe(600);

    const both = byId["dev-deploy-ticket"];
    expect([both.lane, both.posture, both.names, both.namesKey]).toEqual(["shell", "ticket", ["./deploy*", "make deploy*", "terraform apply*"], "command.allowPatterns"]);
    expect(both.who).toEqual({ sponsor: true, roles: ["sec-approvers"] });

    const local = byId["dev-local-tools"];
    expect([local.lane, local.posture, local.names]).toEqual(["tools", "allow", ["shell", "file read", "file write", "file edit", "network fetch", "subagents"]]);

    const deny = byId["no-rm-rf"];
    expect([deny.lane, deny.posture, deny.names, deny.namesKey]).toEqual(["shell", "deny", ["rm -rf *", "git push --force*", "curl * | *sh*"], "command.denyPatterns"]);
    expect(deny.reason).toBe("Straza: destructive command denied for role dev");

    const paths = byId["protect-secrets"];
    expect([paths.lane, paths.posture, paths.names, paths.namesKey]).toEqual(["files", "deny", ["**/.env*", "**/id_rsa*", "**/credentials.json"], "paths.deny"]);
  });

  it("words each rule the way the cards and the review say it", () => {
    expect(outcomeWords(byId["dev-mcp-sum-approval-showcase"])).toBe("needs approval by the person behind the agent within 2 minutes.");
    expect(sentence(byId["dev-mcp-sum-approval-showcase"])).toBe("get-sum on demo-tools needs approval by the person behind the agent within 2 minutes.");
    expect(outcomeWords(byId["dev-mcp-env-ticket"])).toBe("needs a ticket the person behind the agent grants within a day; the same call within an hour runs.");
    expect(outcomeWords(byId["dev-mcp-demo-tools"])).toBe("allowed.");
    expect(sentence(byId["dev-mcp-demo-tools"])).toBe("Every tool on demo-tools is allowed.");
    expect(sentence({ ...byId["no-rm-rf"], names: null })).toBe("Every shell command is denied.");
    expect(outcomeWords({ ...byId["dev-mcp-sum-approval-showcase"], names: null })).toBe("needs approval by the person behind the agent within 2 minutes.");
    expect(outcomeWords(byId["dev-mcp-midpoint-workitem-selfapprove"])).toBe("need approval by the approver role sec-approvers within 10 minutes, told by phone and console.");
    expect(outcomeWords(byId["dev-deploy-ticket"])).toBe("need a ticket the person behind the agent or the approver role sec-approvers grant within a day; the same call within an hour runs.");
    expect(subjectWords(byId["dev-deploy-ticket"])).toBe("shell commands matching ./deploy*, make deploy* or terraform apply*");
    expect(sentence(byId["no-rm-rf"])).toBe("Shell commands matching rm -rf *, git push --force* or curl * | *sh* are denied.");
    expect(sentence(byId["protect-secrets"])).toBe("Files under **/.env*, **/id_rsa* or **/credentials.json are denied.");
    expect(outcomeWords(byId["dev-local-tools"])).toBe("allowed.");
    expect(foldSummary(byId["dev-mcp-env-ticket"])).toBe("MCP calls to get-env on demo-tools · tool.pre: the gateway checks once, before the call");
    expect(foldSummary(byId["no-rm-rf"])).toBe("shell commands matching 3 patterns · events: tool.pre");
  });

  it("counts the buckets from the server's summary", () => {
    const p = posturesOf({ deny: 2, hold: 3, ticket: 2, allow: 4 });
    expect([p.deny, p.hum, p.allow, p.hold, p.ticket, p.check]).toEqual([2, 5, 4, 3, 2, 0]);
    expect(countTitle("hum", p, { mcp: { hold: 3, ticket: 1, allow: 4 }, shell: { ticket: 1, deny: 1 } })).toBe("3 holds · 2 day-scale tickets · lanes: 4 mcp · 1 shell");
    expect(countTitle("deny", p, undefined)).toBe("2 rules deny");
    expect(countTitle("allow", posturesOf({}), undefined)).toMatch(/^Nothing is allowed/);
  });
});

describe("editing in place", () => {
  it("turns a ticket into an allow and keeps the rule's comments", () => {
    const doc = openDoc(seed);
    setPosture(doc, "dev-mcp-env-ticket", "allow");
    const text = docText(doc);
    const rule = rulesOf(doc).find((r) => r.id === "dev-mcp-env-ticket");
    expect(rule && rule.posture).toBe("allow");
    expect(text).toContain("# The DAY-SCALE MCP TICKET: reading the MCP server's");
    expect(commentLines(text)).toEqual(commentLines(seed));
    const block = text.slice(text.indexOf("- id: dev-mcp-env-ticket"), text.indexOf("- id: dev-mcp-demo-tools"));
    expect(block).not.toContain("mode: approve");
    expect(block).not.toContain("ticketTTLSeconds");
    expect(block).toContain("effect: allow");
    // The other ten rules are untouched.
    const before = parse(seed).spec.rules.filter((r: { id: string }) => r.id !== "dev-mcp-env-ticket");
    const after = parse(text).spec.rules.filter((r: { id: string }) => r.id !== "dev-mcp-env-ticket");
    expect(after).toEqual(before);
  });

  it("turns a denial into a hold with the sponsor and a window, keys in a hand-written order, and moves the patterns to the allow side", () => {
    const doc = openDoc(seed);
    setPosture(doc, "no-rm-rf", "hold", { who: { sponsor: true, roles: [] }, timeoutSeconds: 120 });
    const text = docText(doc);
    const block = text.slice(text.indexOf("- id: no-rm-rf"), text.indexOf("- id: protect-secrets"));
    expect(block).toContain("effect: allow\n      mode: approve\n      approve:\n        timeoutSeconds: 120\n        deciders: [sponsor]\n      reason:");
    expect(block).toContain("# The hard floor for role dev");
    expect(block).toContain('allowPatterns: ["rm -rf *", "git push --force*", "curl * | *sh*"]');
    expect(block).not.toContain("denyPatterns");
    const rule = rulesOf(doc).find((r) => r.id === "no-rm-rf");
    expect(rule && [rule.posture, rule.who, rule.timeoutSeconds, rule.namesKey, rule.names]).toEqual(["hold", { sponsor: true, roles: [] }, 120, "command.allowPatterns", ["rm -rf *", "git push --force*", "curl * | *sh*"]]);
  });

  it("moves the matchers to the deny side when an allow or a hold becomes a denial, and leaves a map with both sides alone", () => {
    const doc = openDoc(seed);
    setPosture(doc, "dev-mcp-sum-approval-showcase", "deny");
    setPosture(doc, "protect-secrets", "allow");
    const text = docText(doc);
    expect(text).toContain("toolNames:\n        deny: [\"get-sum\"]");
    expect(text).toContain('paths:\n        allow: ["**/.env*", "**/id_rsa*", "**/credentials.json"]');
    const both = openDoc(seed.replace("        deny: [\"**/.env*\", \"**/id_rsa*\", \"**/credentials.json\"]", "        allow: [\"src/**\"]\n        deny: [\"**/.env*\"]"));
    setPosture(both, "protect-secrets", "allow");
    expect(docText(both)).toContain('allow: ["src/**"]\n        deny: ["**/.env*"]');
    expect(commentLines(text)).toEqual(commentLines(seed));
  });

  it("turns a hold into a ticket for a team and drops the blocking knobs", () => {
    const doc = openDoc(seed);
    setPosture(doc, "dev-mcp-sum-approval-showcase", "ticket", { who: { sponsor: false, roles: ["sec-approvers"] }, ticketTTLSeconds: 172800 });
    const rule = rulesOf(doc).find((r) => r.id === "dev-mcp-sum-approval-showcase");
    expect(rule && [rule.posture, rule.who, rule.ticketTTLSeconds, rule.grantTTLSeconds]).toEqual(["ticket", { sponsor: false, roles: ["sec-approvers"] }, 172800, 3600]);
    const block = docText(doc).slice(0, docText(doc).indexOf("- id: dev-mcp-env-ticket"));
    expect(block).not.toContain("timeoutSeconds");
    expect(block).not.toContain("retryTTLSeconds");
    expect(block).toContain("binding: call");
    expect(block).toContain("class: ticket");
  });

  it("changes the pool and the window of a rule that already holds", () => {
    const doc = openDoc(seed);
    setApprove(doc, "dev-mcp-midpoint-write-approve", { who: { sponsor: true, roles: ["sec-approvers"] }, timeoutSeconds: 300 });
    const rule = rulesOf(doc).find((r) => r.id === "dev-mcp-midpoint-write-approve");
    expect(rule && [rule.who, rule.timeoutSeconds]).toEqual([{ sponsor: true, roles: ["sec-approvers"] }, 300]);
  });

  it("writes the reason, the names, the roles and the recording", () => {
    const doc = openDoc(seed);
    setReason(doc, "no-rm-rf", "Straza: no destructive commands");
    setNames(doc, "no-rm-rf", ["rm -rf *", "mkfs*"]);
    setNames(doc, "dev-mcp-sum-approval-showcase", ["get-sum", "get-env"]);
    setMatchRoles(doc, ["dev-tools", "sre-tools"]);
    setCapture(doc, "redact");
    const set = readSet(doc);
    const byId = Object.fromEntries(set.rules.map((r) => [r.id, r]));
    expect(byId["no-rm-rf"].reason).toBe("Straza: no destructive commands");
    expect(byId["no-rm-rf"].names).toEqual(["rm -rf *", "mkfs*"]);
    expect(byId["dev-mcp-sum-approval-showcase"].names).toEqual(["get-sum", "get-env"]);
    expect(set.roles).toEqual(["dev-tools", "sre-tools"]);
    expect(set.capture).toBe("redact");
    expect(docText(doc)).toContain("roles: [dev-tools, sre-tools]");
    setReason(doc, "no-rm-rf", "");
    setCapture(doc, null);
    expect(readSet(doc).rules.find((r) => r.id === "no-rm-rf")?.reason).toBe("");
    expect(readSet(doc).capture).toBeNull();
    expect(docText(doc)).not.toContain("capture:");
  });

  it("switches a rule's class in place, dropping only the other class's knobs", () => {
    const cases: [string, "hold" | "ticket", string[], string[]][] = [
      ["dev-mcp-sum-approval-showcase", "ticket", ["class: ticket", "binding: call", "mode: approve", "# The 2-MINUTE MCP SHOWCASE"], ["timeoutSeconds", "retryTTLSeconds"]],
      ["dev-mcp-env-ticket", "hold", ["mode: approve", "# The DAY-SCALE MCP TICKET"], ["class:", "ticketTTLSeconds", "grantTTLSeconds"]],
    ];
    for (const [id, how, kept, gone] of cases) {
      const doc = openDoc(seed);
      setClass(doc, id, how);
      const text = docText(doc);
      const block = text.slice(text.indexOf("- id: " + id), text.indexOf("- id: ", text.indexOf("- id: " + id) + 1));
      for (const k of kept) expect(block, id).toContain(k);
      for (const g of gone) expect(block, id).not.toContain(g);
      expect(rulesOf(doc).find((r) => r.id === id)?.posture).toBe(how);
      expect(commentLines(text)).toEqual(commentLines(seed));
    }
  });

  it("moves a rule with its comments and leaves the others in order", () => {
    const doc = openDoc(seed);
    const ids = rulesOf(doc).map((r) => r.id);
    moveRule(doc, "protect-secrets", 0);
    moveRule(doc, "dev-mcp-sum-approval-showcase", 99);
    const moved = rulesOf(doc).map((r) => r.id);
    expect(moved).toEqual(["protect-secrets"].concat(ids.filter((id) => id !== "protect-secrets" && id !== "dev-mcp-sum-approval-showcase"), ["dev-mcp-sum-approval-showcase"]));
    const text = docText(doc);
    expect(text.indexOf("# The 2-MINUTE MCP SHOWCASE")).toBeGreaterThan(text.indexOf("- id: no-rm-rf"));
    expect(commentLines(text).sort()).toEqual(commentLines(seed).sort());
    expect(() => moveRule(doc, "no-such-rule", 0)).toThrow(/no rule no-such-rule/);
  });

  it("adds and removes a rule", () => {
    const doc = openDoc(seed);
    addRule(doc, buildRule({ id: "approve-demo-tools-write-file", lane: "mcp", posture: "hold", app: "demo-tools", names: ["write-file"], who: { sponsor: true, roles: [] }, timeoutSeconds: 120, reason: "Straza: write-file needs approval" }));
    expect(rulesOf(doc).map((r) => r.id)).toContain("approve-demo-tools-write-file");
    expect(() => addRule(doc, { id: "no-rm-rf" })).toThrow(/exists already/);
    removeRule(doc, "dev-mcp-demo-tools");
    expect(rulesOf(doc).map((r) => r.id)).not.toContain("dev-mcp-demo-tools");
    expect(docText(doc)).not.toContain("# The sandbox lane: demo-tools is the harmless demo MCP server.");
    expect(docText(doc)).toContain("# The gateway's built-in approval tools");
  });
});

describe("the round trip", () => {
  const files = readdirSync(examplesDir).filter((f) => f.startsWith("valid-") && f.endsWith(".yaml")).map((f) => examplesDir + "/" + f);
  // The eval seed's sets render verbatim on the policy page, so they round-trip too.
  const seedDir = repo("deploy/compose/eval-stack/seed/policies");
  const seedFiles = readdirSync(seedDir).filter((f) => f.endsWith(".yaml")).map((f) => seedDir + "/" + f);
  it.each(files.concat([repo("web/ui/src/test/testdata/dev-guardrails.yaml")], seedFiles))("keeps the meaning and every comment of %s", (file) => {
    const text = readFileSync(file, "utf8");
    const doc = openDoc(text);
    expect(docProblems(doc)).toEqual([]);
    const out = docText(doc);
    expect(parse(out)).toEqual(parse(text));
    expect(commentLines(out)).toEqual(commentLines(text));
  });

  it("names the line of a document that does not parse", () => {
    const doc = openDoc("spec:\n  rules:\n    - id: a\n   bad: [\n");
    expect(docProblems(doc).length).toBeGreaterThan(0);
  });
});

describe("a new policy", () => {
  it("renders the wizard's answer with its lists on one line", () => {
    const rule = buildRule({ id: "approve-demo-tools-write-file-delete-file", lane: "mcp", posture: "hold", app: "demo-tools", names: ["write-file", "delete-file"], who: { sponsor: true, roles: [] }, timeoutSeconds: 120, reason: "Straza: write-file and delete-file on demo-tools need approval" });
    const doc = newPolicyDoc({ name: "dev-tools-approvals", description: "write-file and delete-file on demo-tools wait for the requester's sponsor", priority: 150, roles: ["dev-tools"], rules: [rule] });
    expect(docText(doc)).toBe([
      "apiVersion: straza.dev/v1beta1",
      "kind: PolicySet",
      "metadata:",
      "  name: dev-tools-approvals",
      "  description: write-file and delete-file on demo-tools wait for the requester's sponsor",
      "spec:",
      "  priority: 150",
      "  match:",
      "    roles: [dev-tools]",
      "  rules:",
      "    - id: approve-demo-tools-write-file-delete-file",
      "      tools: [mcp.call]",
      "      apps: [demo-tools]",
      "      toolNames:",
      "        allow: [write-file, delete-file]",
      "      effect: allow",
      "      mode: approve",
      "      approve:",
      "        deciders: [sponsor]",
      "        timeoutSeconds: 120",
      "      reason: \"Straza: write-file and delete-file on demo-tools need approval\"",
      "",
    ].join("\n"));
    const view = rulesOf(doc)[0];
    expect(sentence(view)).toBe("write-file and delete-file on demo-tools need approval by the person behind the agent within 2 minutes.");
  });

  it("builds a shell denial, a file allow, a team ticket and a whole-lane fetch", () => {
    expect(buildRule({ id: "no-rm-rf", lane: "shell", posture: "deny", names: ["rm -rf *"], reason: "Straza: no" })).toEqual({ id: "no-rm-rf", tools: ["shell.exec"], command: { denyPatterns: ["rm -rf *"] }, effect: "deny", reason: "Straza: no" });
    expect(buildRule({ id: "workspace", lane: "files", posture: "allow", names: ["src/**"] })).toEqual({ id: "workspace", tools: ["file.write", "file.edit"], paths: { allow: ["src/**"] }, effect: "allow" });
    expect(buildRule({ id: "deploy", lane: "shell", posture: "ticket", names: ["./deploy*"], who: { sponsor: false, roles: ["sec-approvers"] }, ticketTTLSeconds: 86400, grantTTLSeconds: 3600 })).toEqual({ id: "deploy", tools: ["shell.exec"], command: { allowPatterns: ["./deploy*"] }, effect: "allow", mode: "approve", approve: { roles: ["sec-approvers"], class: "ticket", ticketTTLSeconds: 86400, grantTTLSeconds: 3600 } });
    expect(buildRule({ id: "fetch", lane: "net", posture: "allow" })).toEqual({ id: "fetch", tools: ["net.fetch"], effect: "allow" });
  });

  it("slugs names and keeps ids unique", () => {
    expect(slug("Approve demo-tools: write_file!")).toBe("approve-demo-tools-write-file");
    expect(uniqueId("a", ["a", "a-2"])).toBe("a-3");
  });
});

describe("words", () => {
  it("says windows in natural units", () => {
    expect([90, 120, 600, 3600, 7200, 86400, 172800].map(durationWords)).toEqual(["90 seconds", "2 minutes", "10 minutes", "an hour", "2 hours", "a day", "2 days"]);
  });
  it("says what a publish changes", () => {
    expect(whatChanges(1, [], [], ["dev-tools"], true, false)).toBe("Adds 1 rule for role dev-tools. A new policy: nothing removed.");
    expect(whatChanges(0, ["no-rm-rf"], ["old"], [], false, true)).toBe("Replaces the running version: changes 1 rule (no-rm-rf) and removes 1 rule (old) for every session.");
  });
});
