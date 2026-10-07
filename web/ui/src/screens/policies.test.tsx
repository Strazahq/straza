import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Policies } from "./policies";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type BindingRow, type PoliciesAnswer, type PolicySetRow, type RoleRow, listBindings, listPolicies, listRoles } from "@/lib/api";
import { take } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { CATEGORY, DRAFT, EDITED, EVERYONE, FILTER, LANE_FILTER, LIST_FOOT, LIVE, NOT_PARSED, NOT_PARSED_TITLE, NO_MATCH, OUTSIDE_FOOT, REC, REC_VERBATIM, SEARCH, SUBJECT_ROLES, VIEW, countWords } from "@/lib/policy-words";
import { readFailed } from "@/lib/say";

// The six fixture policies, in the shapes the list envelope answers,
// plus one whose stored text no longer parses and so carries no summary.
const sets: PolicySetRow[] = [
  {
    name: "org-baseline", status: "active", updated_at: "2026-08-25T10:02:00Z",
    summary: { description: "The floor for every session: destructive commands, secret files and exfil hosts.", rules: 3, postures: { deny: 3 }, matchRoles: [], lanes: { shell: { deny: 1 }, files: { deny: 1 }, net: { deny: 1 } } },
  },
  {
    name: "dev-guardrails", status: "active", updated_at: "2026-09-07T09:14:00Z",
    summary: { description: "Mode B sandbox governance for the developer seat.", rules: 11, postures: { deny: 2, hold: 3, ticket: 2, allow: 4 }, matchRoles: ["dev-tools"], capture: "verbatim", lanes: { mcp: { hold: 3, ticket: 1, allow: 4 }, shell: { deny: 2, ticket: 1 } } },
  },
  {
    name: "dev-tools-access", status: "active", updated_at: "2026-09-13T08:11:00Z",
    summary: { description: "Gates for the role dev-tools, written from the role page", rules: 2, postures: { deny: 1, hold: 1 }, matchRoles: ["dev-tools"], lanes: { mcp: { deny: 1, hold: 1 } } },
  },
  {
    name: "sre-tools-access", status: "active", updated_at: "2026-09-11T15:40:00Z",
    summary: { description: "Gates for the role sre-tools, written from the role page", rules: 2, postures: { deny: 1, hold: 1 }, matchRoles: ["sre-tools"], lanes: { mcp: { deny: 1, hold: 1 } } },
  },
  {
    name: "finance-agents", status: "active", drift: true, updated_at: "2026-09-13T10:52:00Z",
    summary: { description: "The ledger agents: reads run, postings wait for a person.", rules: 4, postures: { allow: 1, hold: 1, ticket: 1, deny: 1 }, matchRoles: ["finance-tools"], capture: "redact", lanes: { mcp: { allow: 1, hold: 1, ticket: 1 }, shell: { deny: 1 } } },
  },
  {
    name: "release-window", status: "draft", updated_at: "2026-09-12T17:25:00Z",
    summary: { description: "Deploys during the release window need a security ticket.", rules: 1, postures: { ticket: 1 }, matchRoles: ["dev-tools", "sre-tools"], lanes: { shell: { ticket: 1 } } },
  },
  { name: "broken-policy", status: "active", updated_at: "2026-09-10T07:00:00Z" },
];

const answer: PoliciesAnswer = { items: sets, total: sets.length };

const roles: RoleRow[] = [
  { id: "r-dev-tools", name: "dev-tools", kind: "application", description: "Tools for the developer seat", holder_count: 2 },
  { id: "r-sre-tools", name: "sre-tools", kind: "application", description: "Operations tools for the on-call", holder_count: 3 },
  { id: "r-finance-tools", name: "finance-tools", kind: "application", description: "Tools for the ledger agents", holder_count: 1 },
  { id: "r-auditor-tools", name: "auditor-tools", kind: "application", description: "Read lane for the auditors", holder_count: 0 },
  { id: "r-dev", name: "dev", kind: "business", description: "The developer seat", implies: ["dev-tools"] },
];

const bindings: BindingRow[] = [
  { id: "b1", app: "demo-tools", role: "dev-tools", tools: ["*"] },
  { id: "b2", app: "ops-mcp", role: "sre-tools", tools: ["*"] },
  { id: "b3", app: "ledger-mcp", role: "finance-tools", tools: ["*"] },
  { id: "b4", app: "siem-mcp", role: "auditor-tools", tools: ["*"] },
];

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listPolicies: vi.fn(),
  listRoles: vi.fn(),
  listBindings: vi.fn(),
}));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = (view?: "sets" | "roles") => render(<TooltipProvider><Policies view={view} /></TooltipProvider>);

const section = (key: string) => document.querySelector('[data-category="' + key + '"]') as HTMLElement;
const rowsOf = (key: string) => Array.from(section(key).querySelectorAll("[data-policy]")).map((r) => r.getAttribute("data-policy"));
const row = (name: string) => screen.getByRole("button", { name: "Open " + name });
const count = (name: string, bucket: string) => row(name).querySelector('[data-count="' + bucket + '"]') as HTMLElement;
const categoryCount = (key: string) => (document.querySelector('[data-category-count="' + key + '"]') as HTMLElement).textContent;
const shownNames = () => Array.from(document.querySelectorAll("[data-policy]")).map((r) => r.getAttribute("data-policy"));

// choose opens one filter and takes an option by its word.
async function choose(label: string, option: string) {
  await userEvent.click(screen.getByRole("combobox", { name: label }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

describe("the Policies list", () => {
  beforeEach(() => {
    vi.mocked(listPolicies).mockResolvedValue(answer);
    vi.mocked(listRoles).mockResolvedValue(roles);
    vi.mocked(listBindings).mockResolvedValue(bindings);
    vi.mocked(navigate).mockClear();
    take("policies-new");
  });

  it("splits the policies into the two categories, each with its title, count and line", async () => {
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    const titles = Array.from(document.querySelectorAll("[data-category] h2")).map((h) => h.textContent);
    expect(titles).toEqual([CATEGORY.everyone.title, CATEGORY.roles.title]);
    expect(categoryCount("everyone")).toBe("2");
    expect(categoryCount("roles")).toBe("5");
    expect(rowsOf("everyone")).toEqual(["broken-policy", "org-baseline"]);
    expect(rowsOf("roles")).toEqual(["dev-guardrails", "dev-tools-access", "finance-agents", "release-window", "sre-tools-access"]);
    expect(within(section("everyone")).getByText(CATEGORY.everyone.line)).toBeTruthy();
    expect((document.querySelector("[data-row-count]") as HTMLElement).textContent).toBe(countWords(7, 7));
    expect(screen.getByText(LIST_FOOT + " " + OUTSIDE_FOOT)).toBeTruthy();
  });

  it("reads one policy row across its columns", async () => {
    mount();
    const dev = await screen.findByRole("button", { name: "Open dev-guardrails" });
    expect(within(dev).getByText("dev-guardrails")).toBeTruthy();
    expect(within(dev).getByText(REC).getAttribute("title")).toBe(REC_VERBATIM);
    expect(within(dev).getByText("Mode B sandbox governance for the developer seat.")).toBeTruthy();
    expect(within(dev).getByText(LIVE)).toBeTruthy();
    expect(within(dev).getByText("dev-tools")).toBeTruthy();
    expect(count("dev-guardrails", "deny").textContent).toBe("2");
    expect(count("dev-guardrails", "hum").textContent).toBe("5");
    expect(count("dev-guardrails", "allow").textContent).toBe("4");
    expect(within(row("release-window")).getByText(DRAFT)).toBeTruthy();
    expect(within(row("finance-agents")).getByText(EDITED)).toBeTruthy();
    expect(within(row("org-baseline")).getByText(EVERYONE)).toBeTruthy();
  });

  it("splits the human bucket and names the lanes in a count hover", async () => {
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    expect(count("dev-guardrails", "deny").getAttribute("title")).toBe("2 rules deny · lanes: 2 shell");
    expect(count("dev-guardrails", "hum").getAttribute("title")).toBe("3 holds · 2 day-scale tickets · lanes: 4 mcp · 1 shell");
    expect(count("dev-guardrails", "allow").getAttribute("title")).toBe("4 rules allow · lanes: 4 mcp");
    expect(count("org-baseline", "allow").getAttribute("title")).toBe("Nothing is allowed by this policy's own rules. MCP tools run by role access; local tools follow the profile default, denied under enterprise.");
  });

  it("says so where the stored text no longer parses, instead of reading as zero", async () => {
    mount();
    const broken = await screen.findByRole("button", { name: "Open broken-policy" });
    const said = within(broken).getAllByText(NOT_PARSED);
    expect(said.length).toBe(4);
    expect(said[0].getAttribute("title")).toBe(NOT_PARSED_TITLE);
  });

  it("opens the policy page from a row", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open dev-guardrails" }));
    expect(navigate).toHaveBeenCalledWith("policies", ["dev-guardrails"]);
  });

  it("narrows every table at once on the search, over the name and the description", async () => {
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    await userEvent.type(screen.getByRole("textbox", { name: SEARCH }), "release window");
    expect(shownNames()).toEqual(["release-window"]);
    expect((document.querySelector("[data-row-count]") as HTMLElement).textContent).toBe(countWords(1, 7));
    expect(within(section("everyone")).getByText(NO_MATCH)).toBeTruthy();
  });

  it("narrows on Status, on Applies to and on Lane", async () => {
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    await choose(FILTER.status, DRAFT);
    expect(shownNames()).toEqual(["release-window"]);
    await choose(FILTER.status, EDITED);
    expect(shownNames()).toEqual(["finance-agents"]);
    await choose(FILTER.status, FILTER.any);
    await choose(FILTER.applies, EVERYONE);
    expect(shownNames()).toEqual(["broken-policy", "org-baseline"]);
    await choose(FILTER.applies, "sre-tools");
    expect(shownNames()).toEqual(["release-window", "sre-tools-access"]);
    await choose(FILTER.applies, FILTER.anyRole);
    await choose(FILTER.lane, LANE_FILTER.files);
    expect(shownNames()).toEqual(["org-baseline"]);
  });

  it("keeps the heading of an empty category and opens the wizard from its door", async () => {
    vi.mocked(listPolicies).mockResolvedValue({ items: sets.filter((s) => s.summary && (s.summary.matchRoles || []).length > 0) });
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    expect(within(section("everyone")).getByText(CATEGORY.everyone.none, { exact: false })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: CATEGORY.everyone.door }));
    expect(take("policies-new")).toEqual({ role: null });
    expect(navigate).toHaveBeenCalledWith("policies", ["new"]);
  });

  it("navigates to the other view from the view control", async () => {
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    await userEvent.click(screen.getByRole("button", { name: VIEW.roles }));
    expect(navigate).toHaveBeenCalledWith("policies", ["by-role"]);
  });

  it("drops the filters By role has no use for and counts the roles instead", async () => {
    mount("roles");
    await waitFor(() => expect(document.querySelector('[data-role="dev-tools"]')).toBeTruthy());
    expect(screen.queryByRole("combobox", { name: FILTER.status })).toBeNull();
    expect(screen.queryByRole("combobox", { name: FILTER.applies })).toBeNull();
    expect(screen.getByRole("combobox", { name: FILTER.lane })).toBeTruthy();
    expect((document.querySelector("[data-row-count]") as HTMLElement).textContent).toBe("4 roles and Everyone");
    expect(document.querySelector('[data-role="dev"]')).toBeNull();
  });

  it("keeps the policies on screen when a read beside them fails, and says what failed", async () => {
    const err = new ApiError("roles: service unavailable", 503);
    vi.mocked(listRoles).mockRejectedValue(err);
    mount();
    await screen.findByRole("button", { name: "Open dev-guardrails" });
    expect(screen.getByText(readFailed(SUBJECT_ROLES, err), { exact: false })).toBeTruthy();
    expect(shownNames().length).toBe(7);
  });

  it("says what failed and offers the read again when the list itself cannot be read", async () => {
    vi.mocked(listPolicies).mockRejectedValue(new ApiError("strazad did not answer", 0, true));
    mount();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("The policy list could not be read");
    expect(within(alert).getByRole("button")).toBeTruthy();
  });
});
