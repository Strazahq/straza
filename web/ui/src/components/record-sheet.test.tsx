import type * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RecordSheet } from "./record-sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import { COPIED, SENTINEL, type Row, ceRow } from "@/lib/audit-words";
import { notify } from "@/lib/notify";
import { ENGINE_FIELDS } from "@/lib/policy-words";

vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
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
    expect(blocks()).toEqual(["why", "Chain", "The record as stored"]);
    const why = document.querySelector("[data-why]") as HTMLElement;
    expect(why.textContent).toContain("Denied.");
    expect(why.textContent).toContain("Decided by rule no-destructive-delete in policy dev-guardrails: Straza: destructive delete is refused for role dev");
    expect(why.textContent).toContain("Recorded at decision time.");
    expect(why.textContent).not.toContain("effect=deny");
    await userEvent.click(screen.getByRole("button", { name: ENGINE_FIELDS }));
    expect(why.textContent).toContain("effect=deny · ruleId=no-destructive-delete · setName=dev-guardrails · snapshot=sha256:4b1e");
  });

  it.each([
    { name: "a refusal", data: DENY, type: "straza.audit.tool", tone: "danger", word: "Denied.", call: "shell.exec rm -rf ./build" },
    { name: "a held call", data: MCP, type: "straza.audit.mcp", tone: "warn", word: "Needs approval.", call: "midpoint.assign_role" },
    { name: "an allowance", data: { ...DENY, effect: "allow" }, type: "straza.audit.tool", tone: "ok", word: "Allowed.", call: "shell.exec rm -rf ./build" },
  ])("sets the verdict of $name at title size in its tone, with the call it decided as a mono line", ({ data, type, tone, word, call }) => {
    show(row(data, type));
    const why = document.querySelector("[data-why]") as HTMLElement;
    expect(why.className).toContain("bg-" + tone + "-bg");
    const verdict = why.firstElementChild as HTMLElement;
    expect(verdict.textContent).toBe(word);
    expect(verdict.className).toContain("text-xl");
    expect(verdict.className).toContain("text-" + tone);
    const line = verdict.nextElementSibling as HTMLElement;
    expect(line.hasAttribute("data-call")).toBe(true);
    expect(line.textContent).toBe(call);
    expect(line.className).toContain("font-mono");
  });

  it("leaves the call line out when the record names no call", () => {
    show(row({ session: SESSION, user: USER_ID, effect: "allow", ruleId: "dev-reads", setName: "dev-guardrails" }));
    expect((document.querySelector("[data-why]") as HTMLElement).textContent).toContain("Allowed.");
    expect(document.querySelector("[data-call]")).toBeNull();
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

  it("shows the record as it is stored, indented, and the hash it is signed by", () => {
    show(row(DENY));
    const stored = document.querySelector("[data-section=\"The record as stored\"] pre") as HTMLElement;
    expect(stored.textContent).toContain("\n  \"type\": \"straza.audit.tool\",");
    expect(stored.textContent).toContain("\n    \"effect\": \"deny\",");
    expect(screen.getByText(HASH)).toBeTruthy();
    expect(screen.getByText("Hash matches the loaded chain.")).toBeTruthy();
  });

  it("tells a verified chain check by its fill, its check mark and its words", () => {
    show(row(DENY));
    const chain = document.querySelector("[data-section=\"Chain\"]") as HTMLElement;
    expect(chain.className).toContain("bg-ok-bg");
    const check = chain.querySelector("[data-chain-check]") as HTMLElement;
    expect(check.getAttribute("data-chain-check")).toBe("ok");
    expect(check.textContent).toBe("Hash matches the loaded chain.");
    expect(check.className).toContain("text-ok");
    expect(check.querySelector("svg")).toBeTruthy();
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

  it("offers no test on a record that decided no call", () => {
    show(SENTINEL_ROW);
    expect(screen.queryByRole("button", { name: "Test this call" })).toBeNull();
    expect(document.querySelector("[data-policy-test]")).toBeNull();
  });

  it("closes on the footer's Close", async () => {
    const onOpenChange = vi.fn();
    show(row(DENY), { onOpenChange });
    const foot = document.querySelector("[data-slot=\"sheet-footer\"]") as HTMLElement;
    await userEvent.click(within(foot).getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
