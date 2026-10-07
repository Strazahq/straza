import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PoliciesByRole } from "./policies-by-role";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { BindingRow, PolicySetRow, RoleRow } from "@/lib/api";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { CATEGORY, DOOR_APPROVE, DOOR_APPROVE_EVERYONE_TITLE, DOOR_DENY, EVERYONE, NO_ALLOW_EVERYONE, NO_ALLOW_ROLE, NO_RECORDING, NO_ROLE_POLICY, REC, ROLE_FOOT, doorDenyTitle } from "@/lib/policy-words";

// The six fixture policies, as the list envelope answers them.
const sets: PolicySetRow[] = [
  {
    name: "org-baseline", status: "active",
    summary: { rules: 3, postures: { deny: 3 }, matchRoles: [], lanes: { shell: { deny: 1 }, files: { deny: 1 }, net: { deny: 1 } } },
  },
  {
    name: "dev-guardrails", status: "active",
    summary: { rules: 11, postures: { deny: 2, hold: 3, ticket: 2, allow: 4 }, matchRoles: ["dev-tools"], capture: "verbatim", lanes: { mcp: { hold: 3, ticket: 1, allow: 4 }, shell: { deny: 2, ticket: 1 } } },
  },
  {
    name: "dev-tools-access", status: "active",
    summary: { rules: 2, postures: { deny: 1, hold: 1 }, matchRoles: ["dev-tools"], lanes: { mcp: { deny: 1, hold: 1 } } },
  },
  {
    name: "sre-tools-access", status: "active",
    summary: { rules: 2, postures: { deny: 1, hold: 1 }, matchRoles: ["sre-tools"], lanes: { mcp: { deny: 1, hold: 1 } } },
  },
  {
    name: "finance-agents", status: "active", drift: true,
    summary: { rules: 4, postures: { allow: 1, hold: 1, ticket: 1, deny: 1 }, matchRoles: ["finance-tools"], capture: "redact", lanes: { mcp: { allow: 1, hold: 1, ticket: 1 }, shell: { deny: 1 } } },
  },
  {
    name: "release-window", status: "draft",
    summary: { rules: 1, postures: { ticket: 1 }, matchRoles: ["dev-tools", "sre-tools"], lanes: { shell: { ticket: 1 } } },
  },
];

const outsideSet: PolicySetRow = {
  name: "contractor-lockdown", status: "active",
  summary: { rules: 2, postures: { deny: 2 }, matchRoles: [], matchOther: true, lanes: { shell: { deny: 2 } } },
};

const roles: RoleRow[] = [
  { id: "r-dev-tools", name: "dev-tools", kind: "application", description: "Tools for the developer seat", holder_count: 2 },
  { id: "r-sre-tools", name: "sre-tools", kind: "application", description: "Operations tools for the on-call", holder_count: 3 },
  { id: "r-finance-tools", name: "finance-tools", kind: "application", description: "Tools for the ledger agents", holder_count: 1 },
  { id: "r-auditor-tools", name: "auditor-tools", kind: "application", description: "Read lane for the auditors", holder_count: 0 },
];

const bindings: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b2", app: "midpoint", role: "dev-tools", tools: ["get_user"] },
  { id: "b3", app: "ops-mcp", role: "sre-tools", tools: ["*"] },
  { id: "b4", app: "ledger-mcp", role: "finance-tools", tools: ["*"] },
  { id: "b5", app: "siem-mcp", role: "auditor-tools", tools: ["*"] },
];

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const onOpenPolicy = vi.fn();

type Over = Partial<Parameters<typeof PoliciesByRole>[0]>;

const mount = (over: Over = {}) =>
  render(
    <TooltipProvider>
      <PoliciesByRole rows={sets} roles={roles} bindings={bindings} lane="" onOpenPolicy={onOpenPolicy} {...over} />
    </TooltipProvider>,
  );

const line = (name: string) => document.querySelector('[data-role="' + name + '"]') as HTMLElement;
const chips = (name: string) => Array.from(line(name).querySelectorAll("[data-set]")).map((c) => c.getAttribute("data-set"));
const count = (name: string, bucket: string) => line(name).querySelector('[data-count="' + bucket + '"]') as HTMLElement;

describe("the By role view", () => {
  beforeEach(() => {
    vi.mocked(navigate).mockClear();
    onOpenPolicy.mockClear();
    take("policies-new");
  });

  it("puts Everyone above the roles, with the floor policy and no global allow", () => {
    mount();
    const rows = Array.from(document.querySelectorAll("[data-role]")).map((r) => r.getAttribute("data-role"));
    expect(rows).toEqual([EVERYONE, "dev-tools", "sre-tools", "finance-tools", "auditor-tools"]);
    expect(chips(EVERYONE)).toEqual(["org-baseline"]);
    expect(count(EVERYONE, "deny").textContent).toBe("3");
    expect(count(EVERYONE, "deny").getAttribute("title")).toBe("org-baseline (live): 3 deny");
    expect(within(line(EVERYONE)).getByRole("button", { name: DOOR_APPROVE }).getAttribute("title")).toBe(DOOR_APPROVE_EVERYONE_TITLE);
    expect(count(EVERYONE, "allow").textContent).toBe("0");
    expect(count(EVERYONE, "allow").getAttribute("title")).toBe(NO_ALLOW_EVERYONE);
    expect(within(line(EVERYONE)).getByText(NO_RECORDING)).toBeTruthy();
    expect(screen.getByText(ROLE_FOOT)).toBeTruthy();
  });

  it("sums a role's own live policies, names the set that is off and the floor in the hovers, and says what it records", () => {
    mount();
    expect(chips("dev-tools")).toEqual(["dev-guardrails", "dev-tools-access", "release-window"]);
    expect(count("dev-tools", "deny").textContent).toBe("3");
    expect(count("dev-tools", "hum").textContent).toBe("6");
    expect(count("dev-tools", "allow").textContent).toBe("4");
    expect(count("dev-tools", "deny").getAttribute("title")).toBe("dev-guardrails (live): 2 deny\ndev-tools-access (live): 1 deny\norg-baseline's 3 denials apply to this role too");
    expect(count("dev-tools", "hum").getAttribute("title")).toBe("dev-guardrails (live): 3 holds · 2 day-scale tickets\ndev-tools-access (live): 1 hold\nrelease-window (off)");
    expect(within(line("dev-tools")).getByText(REC)).toBeTruthy();
    expect(within(line("dev-tools")).getByText("word for word")).toBeTruthy();
    expect(within(line("finance-tools")).getByText("masked")).toBeTruthy();
    expect(line("dev-tools").querySelector("td")?.getAttribute("title")).toBe("Reaches demo-tools and midpoint.");
  });

  it("says when no policy names a role, and opens the wizard from the empty cell with the role picked", async () => {
    mount();
    expect(within(line("auditor-tools")).getByText(NO_ROLE_POLICY)).toBeTruthy();
    expect(count("auditor-tools", "allow").getAttribute("title")).toBe(NO_ALLOW_ROLE);
    const deny = within(line("auditor-tools")).getByRole("button", { name: DOOR_DENY });
    expect(deny.getAttribute("title")).toBe(doorDenyTitle("auditor-tools"));
    await userEvent.click(deny);
    expect(take("policies-new")).toEqual({ role: "auditor-tools", intent: "deny", lane: undefined });
    expect(navigate).toHaveBeenCalledWith("policies", ["new"]);
  });

  it("carries the Lane filter into the door and into the counts", async () => {
    mount({ lane: "shell" });
    expect(count("dev-tools", "deny").textContent).toBe("2");
    expect(count("dev-tools", "hum").textContent).toBe("1");
    expect(count("dev-tools", "allow").textContent).toBe("0");
    await userEvent.click(within(line("auditor-tools")).getByRole("button", { name: DOOR_APPROVE }));
    expect(take("policies-new")).toEqual({ role: "auditor-tools", intent: "approve", lane: "shell" });
  });

  it("opens a policy from its chip", async () => {
    mount();
    await userEvent.click(within(line("sre-tools")).getByRole("button", { name: "Open release-window" }));
    expect(onOpenPolicy).toHaveBeenCalledWith("release-window");
  });

  it("leaves the Outside roles row out while no policy is scoped by user or identity", () => {
    mount();
    expect(line(CATEGORY.outside.title)).toBeNull();
  });

  it("adds the Outside roles row, which names no role and so opens no door", () => {
    mount({ rows: [...sets, outsideSet] });
    const row = line(CATEGORY.outside.title);
    expect(row).toBeTruthy();
    expect(within(row).getByText(CATEGORY.outside.line)).toBeTruthy();
    expect(within(row).getByRole("button", { name: "Open contractor-lockdown" })).toBeTruthy();
    expect(within(row).queryByRole("button", { name: DOOR_APPROVE })).toBeNull();
  });
});
