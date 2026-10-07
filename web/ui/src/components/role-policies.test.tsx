import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RolePolicies } from "./role-policies";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { PolicySetRow, RoleRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { DECIDER_LINE, DRAFT, LIVE, OPEN_POLICY, noDecider, noPolicy, policiesLine, postureWords } from "@/lib/role-words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const devTools: RoleRow = { id: "r-dev-tools", name: "dev-tools", kind: "application" };
const sec: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver", decider_in: ["dev-guardrails", "prod-guardrails"] };

const sets: PolicySetRow[] = [
  { name: "dev-guardrails", status: "active", summary: { rules: 11, postures: { allow: 4, hold: 3, deny: 2 } } },
  { name: "dev-draft", status: "draft", summary: { rules: 2, postures: { allow: 2 } } },
];

const mount = (role: RoleRow, rows: PolicySetRow[], problem: string | null = null) =>
  render(<TooltipProvider><RolePolicies role={role} sets={rows} problem={problem} lastRead={new Date()} /></TooltipProvider>);

describe("the Policies tab", () => {
  it("lists the sets naming the role with their status and what their rules do", () => {
    mount(devTools, sets);
    expect(screen.getByText("dev-guardrails")).toBeTruthy();
    expect(screen.getByText(postureWords(11, { allow: 4, hold: 3, deny: 2 }))).toBeTruthy();
    const words = Array.from(document.querySelectorAll("[data-policy-status]")).map((b) => b.textContent);
    expect(words).toEqual([LIVE, DRAFT]);
    expect(screen.getByText(policiesLine("dev-tools"))).toBeTruthy();
  });

  it("says no policy names the role when none does", () => {
    mount(devTools, []);
    expect(screen.getByText(noPolicy("dev-tools"))).toBeTruthy();
  });

  it("makes no such claim while the read is failing", () => {
    mount(devTools, [], "The policies could not be read: HTTP 503. Reload to try again.");
    expect(screen.queryByText(noPolicy("dev-tools"))).toBeNull();
    expect((document.querySelector("[data-fetch-error]") as HTMLElement).textContent).toContain("HTTP 503");
  });

  it("lists the pools an approver role decides for instead", () => {
    mount(sec, []);
    expect(screen.getByText("dev-guardrails")).toBeTruthy();
    expect(screen.getByText("prod-guardrails")).toBeTruthy();
    expect(screen.getAllByText(DECIDER_LINE).length).toBe(2);
    expect(screen.queryByText(policiesLine("sec-approvers"))).toBeNull();
  });

  it("says when no live policy uses the approver role as its pool", () => {
    mount({ ...sec, decider_in: [] }, []);
    expect(screen.getByText(noDecider("sec-approvers"))).toBeTruthy();
  });

  it("opens the policy's page", async () => {
    mount(devTools, sets);
    await userEvent.click(screen.getAllByRole("button", { name: OPEN_POLICY })[0]);
    expect(navigate).toHaveBeenCalledWith("policies", [sets[0].name]);
  });
});
