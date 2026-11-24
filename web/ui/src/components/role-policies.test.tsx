import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RolePolicies } from "./role-policies";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { PolicySetRow, RoleRow } from "@/lib/api";
import { navigate } from "@/lib/router";
import { DECIDER_LINE, DRAFT, LIVE, OPEN_POLICY, POLICIES_HELP, TAB, noDecider, noPolicy, postureWords } from "@/lib/role-words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const devTools: RoleRow = { id: "r-dev-tools", name: "dev-tools", kind: "application" };
const sec: RoleRow = { id: "r-sec", name: "sec-approvers", kind: "approver", decider_in: ["dev-guardrails", "prod-guardrails"] };

const sets: PolicySetRow[] = [
  { name: "dev-guardrails", status: "active", summary: { rules: 11, postures: { allow: 4, hold: 2, ticket: 1, deny: 2, serverCheck: 1, classify: 1 } } },
  { name: "dev-draft", status: "draft", summary: { rules: 2, postures: { allow: 2 } } },
];

const mount = (role: RoleRow, rows: PolicySetRow[], problem: string | null = null) =>
  render(<TooltipProvider><RolePolicies role={role} sets={rows} problem={problem} lastRead={new Date()} /></TooltipProvider>);

describe("the Policies tab", () => {
  it("lists the sets naming the role with their status and what their rules do", () => {
    mount(devTools, sets);
    expect(screen.getByText("dev-guardrails")).toBeTruthy();
    expect(screen.getByText("11 policy rules: 4 allowed, 3 needs approval, 2 denied, 1 server check, 1 classifier")).toBeTruthy();
    expect(screen.getByText("2 policy rules: 2 allowed")).toBeTruthy();
    const words = Array.from(document.querySelectorAll("[data-policy-status]")).map((b) => b.textContent);
    expect(words).toEqual([LIVE, DRAFT]);
    expect(document.querySelectorAll('[data-list-row] [data-kind-icon="policy-set"]')).toHaveLength(2);
  });

  it("says what it lists behind the heading's help icon, with no line at the foot", async () => {
    mount(devTools, sets);
    const help = screen.getByRole("button", { name: "Help: " + TAB.policies });
    expect(help.closest("[data-section]")?.querySelector("h3")?.textContent).toBe(TAB.policies);
    await userEvent.hover(help);
    expect((await screen.findAllByText(POLICIES_HELP)).length).toBeGreaterThan(0);
  });

  it("words a policy's summary, each outcome left out at zero", () => {
    expect(postureWords(1, { deny: 1 })).toBe("1 policy rule: 1 denied");
    expect(postureWords(3, { confirm: 3 })).toBe("3 policy rules: 3 needs approval");
    expect(postureWords(0, {})).toBe("0 policy rules");
    expect(postureWords(undefined, { allow: 1 })).toBe("");
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
