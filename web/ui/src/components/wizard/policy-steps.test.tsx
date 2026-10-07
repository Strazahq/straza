import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type Draft, EMPTY_DRAFT, HowStep, nameProblem, reasonDefault, ruleIdOf, ruleOf, secondsOf, todayWords } from "./policy-steps";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { PreviewEntry, RoleRow } from "@/lib/api";
import { DEPENDS_ON_ROLE, HOW_TICKET, NOT_REACHED_TODAY, RUNS_TODAY, WHO_TEAM, alreadyToday, reasonApprove, reasonDeny } from "@/lib/policy-words";

// The answers of the wizard as the document reads them. The step bodies
// are walked in the screen's own suite; this one holds the writing.

const draft = (patch: Partial<Draft>): Draft => ({ ...EMPTY_DRAFT, ...patch });

const mcp = draft({ role: "dev-tools", app: "demo-tools", tools: ["write-file", "delete-file"], reason: "Straza: it needs approval" });
const shell = draft({ intent: "deny", lane: "shell", patterns: ["rm -rf *"] });
const files = draft({ intent: "allow", lane: "files", paths: ["**/build/*"] });

describe("the wizard's answers as a rule", () => {
  it("reads a window as the seconds the rule stores", () => {
    const cases: [Parameters<typeof secondsOf>[0], number][] = [
      [{ n: 90, unit: "seconds" }, 90],
      [{ n: 2, unit: "minutes" }, 120],
      [{ n: 1, unit: "hours" }, 3600],
      [{ n: 1, unit: "days" }, 86400],
      [{ n: 0, unit: "minutes" }, 60],
    ];
    for (const [span, seconds] of cases) expect(secondsOf(span)).toBe(seconds);
  });

  it("names the rule after what it does", () => {
    expect(ruleIdOf(mcp)).toBe("approve-demo-tools-write-file-delete-file");
    expect(ruleIdOf(shell)).toBe("deny-shell-rm-rf");
    expect(ruleIdOf(draft({ app: "demo-tools", wholeServer: true }))).toBe("approve-demo-tools-every");
  });

  it("writes an MCP rule that waits for the sponsor", () => {
    expect(ruleOf(mcp, "r")).toEqual({
      id: "r",
      tools: ["mcp.call"],
      apps: ["demo-tools"],
      toolNames: { allow: ["write-file", "delete-file"] },
      effect: "allow",
      mode: "approve",
      approve: { deciders: ["sponsor"], timeoutSeconds: 120 },
      reason: "Straza: it needs approval",
    });
  });

  it("writes a ticket with both of its windows", () => {
    const rule = ruleOf(draft({ ...mcp, how: "ticket", who: { sponsor: false, roles: ["sec-approvers"] } }), "r");
    expect(rule.approve).toEqual({ roles: ["sec-approvers"], class: "ticket", ticketTTLSeconds: 86400, grantTTLSeconds: 3600 });
  });

  it("writes the patterns of a denied shell command and the paths of an allowed lane", () => {
    expect(ruleOf(shell, "r")).toEqual({ id: "r", tools: ["shell.exec"], command: { denyPatterns: ["rm -rf *"] }, effect: "deny" });
    expect(ruleOf(files, "r")).toEqual({ id: "r", tools: ["file.write", "file.edit"], paths: { allow: ["**/build/*"] }, effect: "allow" });
  });

  it("offers a reason that says what the rule does", () => {
    expect(reasonDefault(mcp)).toBe(reasonApprove("write-file and delete-file on demo-tools", false));
    expect(reasonDefault(shell)).toBe(reasonDeny("shell commands matching rm -rf *", true));
  });

  it("says why a name cannot be stored", () => {
    const taken = ["dev-guardrails", "org-baseline"];
    const cases: [string, string][] = [
      ["dev-tools-approvals", ""],
      ["dev-guardrails", "taken"],
      ["new", "taken"],
      ["by-role", "taken"],
      ["Dev Tools", "bad"],
      ["-leading", "bad"],
      ["", "bad"],
    ];
    for (const [name, problem] of cases) expect(nameProblem(name, taken)).toBe(problem);
  });
});

describe("what a tool does today", () => {
  const entry = (status: string): PreviewEntry => ({ app: "demo-tools", tool: "write-file", status, reason: "", setName: "dev-guardrails" });

  it("reads the role's own preview", () => {
    expect(todayWords("dev-tools", true, entry("visible"))).toEqual({ text: RUNS_TODAY, muted: false });
    expect(todayWords("dev-tools", true, undefined)).toEqual({ text: RUNS_TODAY, muted: false });
    expect(todayWords("dev-tools", true, entry("approve_gated"))).toEqual({ text: alreadyToday("needs approval", "dev-guardrails"), muted: false });
    expect(todayWords("dev-tools", true, entry("hidden_policy"))).toEqual({ text: alreadyToday("denied", "dev-guardrails"), muted: false });
    expect(todayWords("dev-tools", false, undefined)).toEqual({ text: NOT_REACHED_TODAY, muted: true });
  });

  it("says the answer depends on the role when the policy applies to everyone", () => {
    expect(todayWords(null, true, entry("visible"))).toEqual({ text: RUNS_TODAY, muted: false });
    expect(todayWords(null, true, undefined)).toEqual({ text: DEPENDS_ON_ROLE, muted: true });
    expect(todayWords(null, true, entry("approve_gated"))).toEqual({ text: DEPENDS_ON_ROLE, muted: true });
  });
});

describe("the How step", () => {
  const approvers: RoleRow[] = [{ id: "r-9", name: "sec-approvers", kind: "approver", holder_count: 2 }];

  it("hands back only the answer that changed, in the draft's own shape", async () => {
    const onChange = vi.fn();
    render(<TooltipProvider><HowStep draft={mcp} approvers={approvers} onChange={onChange} /></TooltipProvider>);
    await userEvent.click(screen.getByRole("radio", { name: WHO_TEAM }));
    expect(onChange).toHaveBeenLastCalledWith({ who: { sponsor: false, roles: ["sec-approvers"] } });
    const ticket = screen.getByRole("spinbutton", { name: HOW_TICKET });
    await userEvent.clear(ticket);
    await userEvent.type(ticket, "3");
    expect(onChange).toHaveBeenLastCalledWith({ how: "ticket", ticket: { n: 3, unit: "days" } });
    expect(onChange).toHaveBeenCalledTimes(2);
  });
});
