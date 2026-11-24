import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { UserAccessTab } from "./user-access";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { AssignmentRow, BindingRow, Page, PreviewAnswer, PreviewEntry, RoleRow, UserDetail, UserRow } from "@/lib/api";
import type { AccessRow } from "@/lib/user-access";
import type { Read } from "@/lib/user-read";
import { DECIDES_NOTHING, EVERY_CALL, IDM_OWNS, READING_ACCESS, reachesNothing } from "@/lib/user-words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const counts = { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 };
const altair: UserDetail = {
  id: "u-altair", username: "altair", status: "active", origin: "local", kind: "nhi", user_type: "agent", created_at: "", updated_at: "",
  effective_roles: ["github-readers"], locks: [], sponsored_count: 0, counts,
};
const alice: UserDetail = { ...altair, id: "u-alice", username: "alice", kind: "human", user_type: undefined, origin: "scim", sponsored_count: 1, effective_roles: ["straza-admin", "sec-approvers", "platform-engineer", "github-readers"] };
const roles: RoleRow[] = [
  { id: "r-gh", name: "github-readers", kind: "application" },
  { id: "r-pe", name: "platform-engineer", kind: "business", implies: ["github-readers"] },
  { id: "r-sec", name: "sec-approvers", kind: "approver" },
  { id: "r-admin", name: "straza-admin", kind: "straza", areas: ["full"] },
];
const ready = <T,>(data: T): Read<T> => ({ kind: "ready", data });
const off: Read<never> = { kind: "off" };
const assign = (id: string, role: string, extra: Partial<AssignmentRow> = {}): AssignmentRow => ({ id, subject_kind: "user", subject_id: "u", role_id: role, origin: "admin", ...extra });
const access: AccessRow[] = [{ server: "github", running: true, total: 4, allowed: ["list_issues"], approval: [], denied: [], through: ["github-readers"] }];
const sam: UserRow = { ...altair, id: "u-sam", username: "sam-sre-agent" };
const entry: PreviewEntry = { app: "github", tool: "list_issues", status: "visible", reason: "" };

type Over = Partial<{ user: UserDetail; servers: boolean; preview: Read<PreviewAnswer>; bindings: Read<BindingRow[]>; assignments: Read<AssignmentRow[]>; agents: Read<Page<UserRow>>; access: AccessRow[] | null }>;

function draw(over: Over = {}) {
  render(
    <TooltipProvider>
      <UserAccessTab
        user={over.user || altair}
        servers={over.servers ?? true}
        audit
        policy
        preview={over.preview || ready({ entries: [] })}
        bindings={over.bindings || ready([{ id: "b1", app: "github", role: "github-readers" }])}
        roles={ready(roles)}
        assignments={over.assignments || ready([assign("a1", "r-gh")])}
        agents={over.agents || off}
        access={over.access === undefined ? access : over.access}
        onAssign={vi.fn()}
        onRemove={vi.fn()}
      />
    </TooltipProvider>,
  );
}

const fact = (label: string) => (document.querySelector("[data-fact='" + label + "']") as HTMLElement).querySelector("dd") as HTMLElement;

describe("the Access tab", () => {
  it("heads the table with what the user can do and its help", () => {
    draw();
    expect(screen.getByRole("heading", { level: 2, name: /What altair can do/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Help: What altair can do" })).toBeTruthy();
    expect(document.querySelector("[data-server=github]")).not.toBeNull();
  });

  it("stands one line for the table without the MCP servers area, and keeps the facts and the roles", () => {
    draw({ servers: false, preview: off, bindings: off, access: null });
    expect((document.querySelector("[data-needs-area]") as HTMLElement).textContent).toBe("The servers and tools are not shown, because your role does not include MCP servers.");
    expect(screen.queryByRole("button", { name: "Help: What altair can do" })).toBeNull();
    expect(document.querySelector("[data-fact=Console]")).not.toBeNull();
    expect((document.querySelector("[data-role-row=github-readers]") as HTMLElement).textContent).toBe("github-readersapplication roleassigned hereRemove");
  });

  it.each<[string, Partial<UserDetail>, PreviewAnswer, string]>([
    ["a disabled user whose roles still give tools", { status: "disabled" }, { entries: [entry] }, "disabled"],
    ["an active user the preview calls revoked", {}, { entries: [entry], refused: "revoked" }, "locked"],
    ["a locked user", { locks: [{ origin: "admin", reason: "incident", created_at: "2026-11-17T09:00:00Z" }] }, { entries: [entry] }, "locked"],
    ["an active user the preview calls disabled", {}, { entries: [entry], refused: "disabled" }, "disabled"],
  ])("draws one denied chip with its cause and no tool for %s", (_name, extra, answer, cause) => {
    draw({ user: { ...altair, ...extra }, preview: ready(answer) });
    const line = document.querySelector("[data-refused]") as HTMLElement;
    expect([line.getAttribute("data-refused"), line.textContent]).toEqual([cause, "denied" + EVERY_CALL + cause]);
    expect(line.querySelector("[data-outcome=denied]")).not.toBeNull();
    expect(document.querySelector("[data-access-table]")).toBeNull();
    expect(screen.queryAllByTitle("list_issues: allowed")).toEqual([]);
  });

  it("keeps the one line about the MCP servers area for a disabled user", () => {
    draw({ user: { ...altair, status: "disabled" }, servers: false, preview: off, bindings: off, access: null });
    expect(document.querySelector("[data-needs-area]")).not.toBeNull();
    expect(document.querySelector("[data-refused]")).toBeNull();
  });

  it("draws the table and no denied chip for an active user the preview does not refuse", () => {
    draw({ preview: ready({ entries: [entry] }) });
    expect(document.querySelector("[data-refused]")).toBeNull();
    expect(screen.getAllByTitle("list_issues: allowed")[0].getAttribute("data-outcome")).toBe("allowed");
  });

  it("says it reads the table while the preview is out", () => {
    draw({ preview: { kind: "loading" }, access: null });
    expect(screen.getByText(READING_ACCESS)).toBeTruthy();
  });

  it("says a user who reaches nothing reaches no MCP server", () => {
    draw({ access: [] });
    expect(screen.getByText(reachesNothing("altair"))).toBeTruthy();
    expect(document.querySelector("[data-access-table]")).toBeNull();
  });

  it("reads the Console fact as no access without a Straza role, and the Approvals fact as deciding nothing", () => {
    draw();
    expect(fact("Console").textContent).toBe("no access");
    expect(fact("Approvals").textContent).toBe(DECIDES_NOTHING);
  });

  it("reads the Console fact through the Straza role, and the Approvals fact with the roles and the sponsored agents", () => {
    draw({ user: alice, agents: ready({ items: [sam], next_cursor: "" }), assignments: ready([assign("a1", "r-admin", { origin: "scim" }), assign("a2", "r-sec", { origin: "scim" }), assign("a3", "r-pe", { origin: "scim" })]) });
    expect(fact("Console").textContent).toBe("every areathroughstraza-admin");
    expect(fact("Approvals").textContent).toBe("decides forsec-approverssponsorssam-sre-agent");
    expect(screen.getByRole("link", { name: "sam-sre-agent" }).getAttribute("href")).toContain("users/u-sam");
    expect(document.querySelector("[data-agent-tag=sam-sre-agent]")?.className).toContain("bg-teal-bg");
  });

  it("reads the Approvals fact with the sponsored agents alone for a person who decides nothing else", () => {
    draw({ user: { ...alice, effective_roles: [] }, agents: ready({ items: [sam], next_cursor: "" }), assignments: ready([]) });
    expect(fact("Approvals").textContent).toBe("sponsorssam-sre-agent");
  });

  it("lists a direct, a composed and an out-of-window role, with the identity manager's line for a SCIM user", () => {
    const user = { ...alice, effective_roles: ["straza-admin", "platform-engineer", "github-readers"] };
    draw({ user, assignments: ready([assign("a1", "r-admin", { origin: "scim" }), assign("a3", "r-pe", { origin: "scim" }), assign("a4", "r-sec", { valid_to: "2026-01-01T00:00:00Z" })]) });
    expect(screen.getByText(IDM_OWNS)).toBeTruthy();
    const rows = [...document.querySelectorAll("[data-role-row]")].map((r) => r.textContent);
    expect(rows).toEqual([
      "straza-adminStraza rolefrom your identity managerRemove",
      "sec-approversapprover roleassigned hereexpiredRemove",
      "platform-engineerbusiness rolefrom your identity managerRemove",
      "github-readersapplication role on githubthrough platform-engineer",
    ]);
  });
});
