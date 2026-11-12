import type * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RecordSheet } from "./record-sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { COPIED, SENTINEL, type Row, ceRow } from "@/lib/audit-words";
import { take } from "@/lib/handoff";
import { WRITE_RULE_FOR_CALL } from "@/lib/harness-words";
import { notify } from "@/lib/notify";
import { ENGINE_FIELDS } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { snapshot } from "@/lib/session";

vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
// The session answers no grant unless a suite names one, so the sheet
// offers no rule to write.
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));
// The Test a call sheet is its own suite; here only the call it is handed
// matters, so the seam stands in for it.
vi.mock("@/components/policy-test", () => ({
  PolicyTest: ({ open, prefill }: { open: boolean; prefill?: unknown }) => (open ? <div data-policy-test>{JSON.stringify(prefill)}</div> : null),
}));

const SESSION = "0199cf12-4b1e-7a3c-9d21-0b6e4f2a8c10";
const USER_ID = "0198b2c1-77aa-7f00-8e12-3c4d5e6f7a80";
const HASH = "c41b9e2f0a7d3b8e51c6d4f2a9e0b7c3d5f1a8e6b2c4d0f9a7e3b1c5d8f2a6e0";

// row reads one wire record the way the screen does, so the sheet is
// tested on the same reading the table renders.
const row = (data: Record<string, unknown>, type = "straza.audit.tool", username = "joe-java-developer-agent"): Row =>
  ceRow({ seq: 4210, ce: JSON.stringify({ specversion: "1.0", type, source: "straza", time: "2026-09-12T10:45:01Z", data }), hash: HASH, prevHash: "ab12", username });

const DENY = { session: SESSION, user: USER_ID, tool: "shell.exec", command: "rm -rf ./build", effect: "deny", ruleId: "no-destructive-delete", setName: "dev-guardrails", reason: "Straza: destructive delete is refused for role dev", snapshot: "sha256:4b1e" };
const MCP = { session: SESSION, user: USER_ID, app: "midpoint", toolName: "assign_role", effect: "approve", ruleId: "iga-writes", setName: "iga" };
const SENTINEL_ROW = row({ session: SESSION, detector: "burst-deny", severity: "critical", evidence: [1, 2, 3], reason: "9 refusals in 60 s in one session" }, "straza.audit.sentinel", "");

// blocks names the sheet's blocks in the order they are rendered.
const blocks = () => [...document.querySelectorAll("[data-record-sheet] [data-why], [data-record-sheet] [data-sentinel-strip], [data-record-sheet] [data-section]")]
  .map((e) => e.getAttribute("data-section") || (e.hasAttribute("data-why") ? "why" : "sentinel"));

const show = (r: Row, props: Partial<React.ComponentProps<typeof RecordSheet>> = {}) =>
  render(<TooltipProvider><RecordSheet row={r} open={true} onOpenChange={() => {}} revoked={false} verified={true} {...props} /></TooltipProvider>);

describe("the record sheet", () => {
  beforeEach(() => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(() => Promise.resolve()) } });
  });

  it("names the record, its subject and its session, then leads with the why box", async () => {
    show(row(DENY));
    expect(screen.getByRole("heading", { name: "Record 4210 tool" })).toBeTruthy();
    expect(screen.getByText("joe-java-developer-agent, 2026-09-12 10:45:01 UTC, session", { exact: false }).textContent)
      .toBe("joe-java-developer-agent, 2026-09-12 10:45:01 UTC, session 0199cf12-4b1e…");
    expect(blocks()).toEqual(["why", "What ran", "Chain", "The record as stored"]);
    const why = document.querySelector("[data-why]") as HTMLElement;
    expect(why.textContent).toContain("Denied.");
    expect(why.textContent).toContain("Decided by rule no-destructive-delete in policy dev-guardrails: Straza: destructive delete is refused for role dev");
    expect(why.textContent).toContain("Recorded at decision time.");
    expect(why.textContent).not.toContain("effect=deny");
    await userEvent.click(screen.getByRole("button", { name: ENGINE_FIELDS }));
    expect(why.textContent).toContain("effect=deny · ruleId=no-destructive-delete · setName=dev-guardrails · snapshot=sha256:4b1e");
  });

  it.each([
    { name: "a refusal", data: DENY, type: "straza.audit.tool", tone: "danger", word: "Denied.", meta: "shell.exec" },
    { name: "a held call", data: MCP, type: "straza.audit.mcp", tone: "warn", word: "Needs approval.", meta: "midpoint.assign_role" },
    { name: "an allowance", data: { ...DENY, effect: "allow" }, type: "straza.audit.tool", tone: "ok", word: "Allowed.", meta: "shell.exec" },
  ])("sets the verdict of $name on a neutral card, its word and dot in its tone, and the call under What ran", ({ data, type, tone, word, meta }) => {
    show(row(data, type));
    const why = document.querySelector("[data-why]") as HTMLElement;
    expect(why.className).toContain("bg-card");
    expect(why.className).not.toContain("-bg");
    expect(why.getAttribute("data-tone")).toBe(tone);
    const verdict = why.querySelector("[data-outcome]") as HTMLElement;
    expect(verdict.textContent).toBe(word);
    expect(verdict.className).toContain("text-" + tone);
    expect(document.querySelector("[data-call]")).toBeNull();
    const ran = document.querySelector("[data-section=\"What ran\"]") as HTMLElement;
    expect((ran.querySelector("[data-what-ran-meta]") as HTMLElement).textContent).toBe(meta);
  });

  it("lays the command out as its steps under What ran", () => {
    show(row(DENY));
    const ran = document.querySelector("[data-section=\"What ran\"]") as HTMLElement;
    expect([...ran.querySelectorAll("[data-step-line]")].map((e) => e.textContent)).toEqual(["rm -rf ./build"]);
  });

  it("leaves What ran out when the record names no call", () => {
    show(row({ session: SESSION, user: USER_ID, effect: "allow", ruleId: "dev-reads", setName: "dev-guardrails" }));
    expect((document.querySelector("[data-why]") as HTMLElement).textContent).toContain("Allowed.");
    expect(document.querySelector("[data-section=\"What ran\"]")).toBeNull();
  });

  it("says no policy matched when the record carries no rule", () => {
    show(row({ ...DENY, ruleId: "", setName: "", reason: "no rule matched this call" }));
    expect((document.querySelector("[data-why]") as HTMLElement).textContent)
      .toContain("No policy rule was recorded for this call. no rule matched this call");
  });

  it("says the policy name was not recorded on a record written before the field existed", () => {
    const { setName, ...old } = DENY;
    expect(setName).toBe("dev-guardrails");
    show(row(old));
    expect((document.querySelector("[data-why]") as HTMLElement).textContent)
      .toContain("Decided by rule no-destructive-delete. The policy name was not recorded on this record.");
  });

  it("folds the record as it is stored, and shows it indented once opened", async () => {
    show(row(DENY));
    expect(document.querySelector("[data-section=\"The record as stored\"] pre")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "The record as stored" }));
    const stored = document.querySelector("[data-section=\"The record as stored\"] pre") as HTMLElement;
    expect(stored.textContent).toContain("\n  \"type\": \"straza.audit.tool\",");
    expect(stored.textContent).toContain("\n    \"effect\": \"deny\",");
  });

  it("names the hash it is signed by on one line, and copies it whole", async () => {
    show(row(DENY));
    const hash = document.querySelector("[data-hash]") as HTMLElement;
    expect(hash.textContent).toBe(HASH.slice(0, 16) + "…");
    expect(hash.getAttribute("title")).toBe(HASH);
    await userEvent.click(screen.getByRole("button", { name: "Copy hash" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(HASH);
  });

  it("tells a verified chain check by its mark and its words, with no fill", () => {
    show(row(DENY));
    const chain = document.querySelector("[data-section=\"Chain\"]") as HTMLElement;
    expect(chain.className).not.toContain("-bg");
    const check = chain.querySelector("[data-chain-check]") as HTMLElement;
    expect(check.getAttribute("data-chain-check")).toBe("ok");
    expect(check.textContent).toBe("Hash matches the loaded chain.");
    expect(check.querySelector("svg")?.getAttribute("class")).toContain("text-ok");
  });

  it("says a search result was not re-hashed, and never in the verified tone", () => {
    show(row(DENY), { verified: false });
    expect(screen.getByText("Not verified in this browser. See the audit chain status for details.")).toBeTruthy();
    expect(screen.queryByText("Hash matches the loaded chain.")).toBeNull();
    const chain = document.querySelector("[data-section=\"Chain\"]") as HTMLElement;
    expect(chain.querySelector("[data-chain-check]")?.getAttribute("data-chain-check")).toBe("unknown");
    expect(chain.outerHTML).not.toContain("-ok");
    expect(chain.querySelector("svg")).toBeNull();
  });

  it("leads a sentinel verdict with its strip and hands the revoke back to the screen", async () => {
    const onRevoke = vi.fn();
    show(SENTINEL_ROW, { onRevoke });
    expect(blocks()).toEqual(["sentinel", "Chain", "The record as stored"]);
    const strip = document.querySelector("[data-sentinel-strip]") as HTMLElement;
    expect(strip.textContent).toBe("criticalburst-deny0199cf12-4b1e…" + SENTINEL + "Revoke session");
    expect(document.querySelector("[data-why]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Revoke session" }));
    expect(onRevoke).toHaveBeenCalledWith(SENTINEL_ROW);
  });

  it("replaces the revoke with the badge once the session is revoked", () => {
    show(SENTINEL_ROW, { onRevoke: vi.fn(), revoked: true });
    expect(screen.queryByRole("button", { name: "Revoke session" })).toBeNull();
    expect(screen.getByText("session revoked")).toBeTruthy();
  });

  it("copies the record as JSON and says it was copied", async () => {
    const r = row(DENY);
    show(r);
    await userEvent.click(screen.getByRole("button", { name: "Copy as JSON" }));
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith(COPIED));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(r.raw);
  });

  it("opens Test a call with the call the record decided", async () => {
    show(row(DENY));
    const foot = document.querySelector("[data-slot=\"sheet-footer\"]") as HTMLElement;
    expect(within(foot).getAllByRole("button").map((b) => b.textContent)).toEqual(["Test this call", "Copy as JSON", "Close"]);
    await userEvent.click(within(foot).getByRole("button", { name: "Test this call" }));
    expect(JSON.parse((document.querySelector("[data-policy-test]") as HTMLElement).textContent || "{}"))
      .toEqual({ user: "joe-java-developer-agent", lane: "shell", app: "", tool: "", command: "rm -rf ./build", path: "" });
  });

  it("opens a gateway record on the MCP lane, with its server and tool", async () => {
    show(row(MCP, "straza.audit.mcp"));
    await userEvent.click(screen.getByRole("button", { name: "Test this call" }));
    expect(JSON.parse((document.querySelector("[data-policy-test]") as HTMLElement).textContent || "{}"))
      .toEqual({ user: "joe-java-developer-agent", lane: "mcp", app: "midpoint", tool: "assign_role", command: "", path: "" });
  });

  it.each([
    ["the repo it named", { type: "repo", reach: "argument", value: "Strazahq/pay-api", id: "9012332" }, { value: "Strazahq/pay-api", id: "9012332" }],
    ["no repo when the record cut the value", { type: "repo", reach: "argument", value: "x".repeat(512), valueTruncated: true, valueSha256: "0f1e" }, undefined],
  ])("opens Test a call with %s", async (_, object, want) => {
    show(row({ ...MCP, app: "ghapp", toolName: "pr_create", object }, "straza.audit.mcp"));
    await userEvent.click(screen.getByRole("button", { name: "Test this call" }));
    const prefill = JSON.parse((document.querySelector("[data-policy-test]") as HTMLElement).textContent || "{}");
    expect(prefill.object).toEqual(want);
    expect(prefill.tool).toBe("pr_create");
  });

  it("offers no test on a record that decided no call", () => {
    show(SENTINEL_ROW);
    expect(screen.queryByRole("button", { name: "Test this call" })).toBeNull();
    expect(document.querySelector("[data-policy-test]")).toBeNull();
  });

  it.each([
    { name: "a listed repo", object: { type: "repo", reach: "argument", value: "Strazahq/pay-api", id: "9012332" }, fact: "Strazahq/pay-api id 9012332" },
    { name: "a repo the list does not hold", object: { type: "repo", reach: "argument", value: "Strazahq/old" }, fact: "Strazahq/old not in the repo list" },
    { name: "a cut value", object: { type: "repo", reach: "argument", value: "x".repeat(512), valueTruncated: true, valueSha256: "0f1e2d3c4b5a69788796a5b4c3d2e1f0" }, fact: "x".repeat(512) + " not in the repo list, cut at 512 bytes, SHA-256 0f1e2d3c4b5a…" },
  ])("names $name on a fact row under the call, labelled by the server's type", ({ object, fact }) => {
    show(row({ ...MCP, app: "ghapp", toolName: "pr_create", effect: "allow", object }, "straza.audit.mcp"));
    const why = document.querySelector("[data-why]") as HTMLElement;
    const label = within(why).getByText("Repo");
    expect(label.tagName).toBe("DT");
    expect((label.nextElementSibling as HTMLElement).textContent).toBe(fact);
  });

  it("shows no fact row for a call that names no repo", () => {
    show(row({ ...MCP, object: { type: "repo", reach: "credential" } }, "straza.audit.mcp"));
    expect(document.querySelector("[data-why] [data-facts]")).toBeNull();
  });

  it("closes on the footer's Close", async () => {
    const onOpenChange = vi.fn();
    show(row(DENY), { onOpenChange });
    const foot = document.querySelector("[data-slot=\"sheet-footer\"]") as HTMLElement;
    await userEvent.click(within(foot).getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});

describe("a call no rule decided", () => {
  // The hook's records of a call only a rule can let run: a tool of an MCP
  // server the harness runs itself, and a harness tool Straza does not
  // classify. Neither names a rule.
  const MACHINE = { session: SESSION, user: USER_ID, tool: "mcp.call", app: "claude-in-chrome", toolName: "navigate", effect: "deny", ruleId: "", reason: "no rule matched this call" };
  const HARNESS = { session: SESSION, user: USER_ID, tool: "other", toolName: "LSP", effect: "deny", ruleId: "", reason: "no rule matched this call" };
  const writeRule = () => document.querySelector("[data-write-rule]") as HTMLElement | null;

  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "policy:read,policy:write", expiresIn: 300, sessionID: "s1" });
    vi.mocked(navigate).mockClear();
    take("policies-new");
  });

  it.each([
    ["a tool of a server on the person's machine", MACHINE, { lane: "mcp", app: "claude-in-chrome", tools: ["navigate"] }],
    ["a harness tool", HARNESS, { lane: "harness", tools: ["LSP"] }],
  ])("opens the New policy wizard on %s", async (_, data, door) => {
    show(row(data));
    expect(writeRule()?.textContent).toBe(WRITE_RULE_FOR_CALL);
    await userEvent.click(screen.getByRole("button", { name: WRITE_RULE_FOR_CALL }));
    expect(take("policies-new")).toEqual(door);
    expect(navigate).toHaveBeenCalledWith("policies", ["new"]);
  });

  it.each([
    ["a denial a rule decided", { ...MACHINE, ruleId: "deny-chrome", setName: "browser-guard" }, "straza.audit.tool"],
    ["a gateway record", MACHINE, "straza.audit.mcp"],
    ["a harness tool with no name", { ...HARNESS, toolName: "" }, "straza.audit.tool"],
    ["an allowance", { ...HARNESS, effect: "allow" }, "straza.audit.tool"],
  ])("offers no rule on %s", (_, data, type) => {
    show(row(data, type));
    expect(writeRule()).toBeNull();
  });

  it("offers no rule to a session that may not write policy", () => {
    vi.mocked(snapshot).mockReturnValue({ user: "joe", roles: [], grants: "policy:read", expiresIn: 300, sessionID: "s2" });
    show(row(MACHINE));
    expect(writeRule()).toBeNull();
  });
});
