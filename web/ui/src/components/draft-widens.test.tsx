import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { DraftChecks } from "./draft-checks";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { DraftDetail, DraftFinding } from "@/lib/api";
import { snapshot } from "@/lib/session";
import { DRAFT_86, DRAFT_87, DRAFT_91, GUARD_91, NAMES_86, oldServer } from "@/test/drafts-fixture";

// Widens access as the Checks section draws it: one row per tool and change
// with the roles folded into it, the gate's facts as pills, the warning
// that repeats a row folded into it, the policy rules a changed policy
// loosens, the confirm words once, and the fingerprints under Details.
// A server that sends none of the new fields, and a reader the gains were
// cut for, get the server's sentences instead.

vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), snapshot: vi.fn() }));

const mount = (d: DraftDetail) => render(<TooltipProvider><DraftChecks detail={d} /></TooltipProvider>);
const q = (sel: string) => document.querySelector(sel) as HTMLElement | null;
const cells = (row: Element) => [...row.children].map((c) => c.textContent);
const rows = (kind: string) => [...document.querySelectorAll('[data-widens-table="' + kind + '"] > .contents')];
const acks = () => [...document.querySelectorAll("[data-confirm] [data-ack-words]")].map((s) => s.textContent);
const versions = () => [...document.querySelectorAll("[data-version]")].map((r) => [...r.children].map((c) => c.textContent));
const lines = () => [...document.querySelectorAll('[data-group="risk"] [data-finding]')].map((l) => l.getAttribute("data-finding"));

describe("Widens access", () => {
  beforeEach(() => {
    vi.mocked(snapshot).mockReturnValue({ user: "alice", roles: [], grants: "full", expiresIn: 300, sessionID: null });
  });

  it("draws draft 91 as one row per tool, the two roles folded into each, and the gate's facts as pills", () => {
    mount(DRAFT_91);
    expect(q("[data-widens-cause]")?.textContent).toBe("Turns offdemo-tools-readers-accessturned off");
    const [env, sum] = rows("tools");
    expect(rows("tools").length).toBe(2);
    expect(cells(env)).toEqual(["Toolget-envondemo-tools", "Fordemo-tools-readersoperator", "Nowneeds approvalthe agent's sponsorticketa day", "", "After publishingallowed"]);
    expect(cells(sum)).toEqual(["Toolget-sumondemo-tools", "Fordemo-tools-readersoperator", "Nowneeds approvalthe agent's sponsor2 minutes", "", "After publishingallowed"]);
    // The retry window is not the default, so the time's hover says it.
    expect([...sum.querySelectorAll("[title]")].map((e) => e.getAttribute("title"))).toContain("retried within 2 minutes");
    expect(lines()).toEqual([]);
    expect(acks()).toEqual(["Type demo-tools-readers-access when you publish.", "Type 2 when you publish, the number of roles that gain access."]);
    expect(q("[data-confirm] [data-undo]")?.textContent).toBe("Publishing the old version again does not undo it.");
    expect(versions()).toEqual([["demo-tools-get-env-approve", "39dd4a83dffb", "off"], ["demo-tools-get-sum-approve", "47230bc994d2", "off"]]);
    expect(q("[data-check-lines]")?.textContent).toBe("Check lines: policy.guardrail-changed on demo-tools-readers-access, and access.ungated on demo-tools-readers and on operator.");
    expect((q("[data-widens-details]") as HTMLDetailsElement).open).toBe(false);
  });

  it("keeps two rows apart where the roles' gates differ", () => {
    const d = structuredClone(DRAFT_91);
    const g = d.verdict.gains.find((x) => x.role === "operator" && x.tool === "get-env");
    if (g) g.before_gate = { ...g.before_gate, seconds: 3600 };
    mount(d);
    expect(rows("tools").map((r) => [r.getAttribute("data-widens-row"), r.children[1].textContent])).toEqual([
      ["demo-tools/get-env", "Fordemo-tools-readers"],
      ["demo-tools/get-sum", "Fordemo-tools-readersoperator"],
      ["demo-tools/get-env", "Foroperator"],
    ]);
  });

  it("draws draft 86 as one row of 13 tools on a server Straza has not registered, with its warning folded in and no undo line", () => {
    mount(DRAFT_86);
    expect(q("[data-widens-cause]")?.textContent).toBe("Addsux-walk-audio-accessnew");
    const [row] = rows("tools");
    expect(cells(row).slice(1)).toEqual(["Forux-walk-audio", "Nowno access", "", "After publishingallowed", "It matches any MCP server a person's own harness runs under the name elevenlabs. If you meant a registered server, fix the name."]);
    expect(row.children[0].textContent).toBe("Tool13 toolsonelevenlabsnot registeredcheck_subscriptionlist_modelssearch_voicessearch_voice_libraryget_voice+8 more");
    expect(q('[data-group="warning"]')).toBeNull();
    expect(acks()).toEqual(["Tick to confirm."]);
    // The draft only adds objects, so there is no old version to publish again.
    expect(q("[data-confirm] [data-undo]")).toBeNull();
    expect(versions()).toEqual([["elevenlabs-audio-allow", "none", "new"]]);
    expect(q("[data-check-lines]")?.textContent).toBe("Check lines: policy.local-allow on ux-walk-audio-access, with the warning ready.rule-names-nothing folded into it.");
  });

  // The fold reads the rule and server fields alone: a warning with the
  // same words on another rule stays a warning.
  it("folds a warning by its object, rule and server, never by its words", () => {
    const other: DraftFinding = { ...NAMES_86, key: "k86-other", rule: "another-rule" };
    mount({ ...DRAFT_86, verdict: { ...DRAFT_86.verdict, warnings: [NAMES_86, other] } });
    expect([...document.querySelectorAll('[data-group="warning"] [data-finding]')].length).toBe(1);
    expect(q('[data-group="warning"]')?.textContent).toContain(NAMES_86.sentence);
  });

  it("draws draft 87 per policy rule, says a rule that only gains a pattern stays the same, and that the policy applies to you", () => {
    mount(DRAFT_87);
    expect(q("[data-widens-cause]")?.textContent).toBe("Changesagent-guardrailschangedapplies to you");
    const [deploy, rm] = rows("rules");
    expect(cells(deploy)).toEqual(["Policy ruledeploy-ticket+ kubectl apply*", "Callsshell command", "Nowneeds approvalthe agent's sponsorsec-approversticketa day", "", "After publishingneeds approvalsame as now"]);
    expect(cells(rm)).toEqual(["Policy ruleno-rm-rf", "Callsshell command", "Nowdenied", "", "After publishingneeds approvalthe agent's sponsor2 minutes"]);
    expect(acks()).toEqual(["Type agent-guardrails when you publish."]);
    expect(versions()).toEqual([["deploy-ticket", "ccbf1adb51a1", "7463f095f50a"], ["no-rm-rf", "21081ff3e4a2", "8d4f605ef67d"]]);
  });

  it("names the proposer a changed policy applies to for any other reader", () => {
    vi.mocked(snapshot).mockReturnValue({ user: "bob", roles: [], grants: "full", expiresIn: 300, sessionID: null });
    mount(DRAFT_87);
    expect(q("[data-applies]")?.textContent).toBe("applies to alice");
  });

  // An older server sends no rows, gates, rule, server or tools: the page
  // keeps the server's sentences, with Now and After where they have no
  // fingerprint and no Now where a line has none.
  it("keeps the sentences of a server that sends none of the new fields", () => {
    mount(oldServer(DRAFT_91));
    expect(q("[data-widens-table]")).toBeNull();
    expect(lines()).toEqual(["access.ungated", "access.ungated"]);
    expect(q('[data-finding="access.ungated"]')?.textContent).toContain("Now: demo-tools/get-env=");
    expect(q("[data-widens-cause]")?.textContent).toBe("Turns offdemo-tools-readers-accessturned off");
    expect(versions().length).toBe(2);
    document.body.innerHTML = "";
    mount(oldServer(DRAFT_86));
    const local = q('[data-finding="policy.local-allow"]') as HTMLElement;
    expect(local.textContent).toContain("After publishing: elevenlabs-audio-allow: elevenlabs.");
    expect(local.textContent).not.toContain("Now:");
    expect(q('[data-group="warning"]')?.textContent).toContain(NAMES_86.sentence);
    expect(q("[data-applies]")).toBeNull();
  });

  it("keeps the sentence of a line whose gains the server left out for this reader", () => {
    mount({ ...DRAFT_91, verdict: { ...DRAFT_91.verdict, gains: DRAFT_91.verdict.gains.filter((g) => g.role !== "operator") } });
    expect(rows("tools").map((r) => r.children[1].textContent)).toEqual(["Fordemo-tools-readers", "Fordemo-tools-readers"]);
    expect(lines()).toEqual(["access.ungated"]);
  });

  // A whole document's hash and a server's fingerprint go under Details,
  // named by the object, and leave the line with its sentence alone.
  it("moves a line's fingerprints under Details", () => {
    const hash = (c: string) => c.repeat(64);
    const rego: DraftFinding = { ...GUARD_91, code: "policy.rego", key: "k-rego", group: undefined, sentence: "The policy demo-tools-readers-access carries a Rego module.", before: hash("a"), after: hash("b") };
    mount({ ...DRAFT_91, verdict: { ...DRAFT_91.verdict, risks: [rego] } });
    const line = q('[data-finding="policy.rego"]') as HTMLElement;
    expect(line.textContent).toBe(rego.sentence);
    expect(versions()).toEqual([["demo-tools-readers-access", hash("a"), hash("b")]]);
  });
});
