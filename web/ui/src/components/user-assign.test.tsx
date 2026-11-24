import type * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserAssign } from "./user-assign";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AssignmentRow, type BindingRow, type PreviewAnswer, type PreviewEntry, type RoleRow, type ToolRow, catalogPreview, grantRole } from "@/lib/api";
import { grantBody } from "@/lib/role-words";
import { DECIDES_REQUESTS, GRANT_MISSING, IDM_OWNS, NOT_FOR_AGENT, NO_ROLE_MATCHES } from "@/lib/user-words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), grantRole: vi.fn(), catalogPreview: vi.fn() }));

const roles: RoleRow[] = [
  { id: "r-gh", name: "github-readers", kind: "application" },
  { id: "r-demo", name: "demo-tools-sandbox", kind: "application" },
  { id: "r-pe", name: "platform-engineer", kind: "business", implies: ["github-readers", "demo-tools-sandbox"] },
  { id: "r-sec", name: "sec-approvers", kind: "approver" },
  { id: "r-admin", name: "straza-admin", kind: "straza", description: "Straza administration" },
];
const bindings: BindingRow[] = [{ id: "b1", app: "demo-tools", role: "demo-tools-sandbox", tools: ["echo"] }];
const tools: ToolRow[] = [{ id: "t1", app: "demo-tools", app_id: "a", name: "echo" }, { id: "t2", app: "demo-tools", app_id: "a", name: "now" }];
const held: AssignmentRow[] = [{ id: "a1", subject_kind: "user", subject_id: "u", role_id: "r-gh" }];
const refusal = (message: string) => Object.assign(new Error(message), { status: 409, unreachable: false });

function draw(extra: Partial<React.ComponentProps<typeof UserAssign>> = {}) {
  const props = { onOpenChange: vi.fn(), onAssigned: vi.fn() };
  render(
    <TooltipProvider>
      <UserAssign open userID="u-altair" name="altair" machine scim={false} servers roles={roles} assignments={held} bindings={bindings} tools={tools} {...props} {...extra} />
    </TooltipProvider>,
  );
  return props;
}

const dialog = () => screen.getByRole("dialog");
const option = (name: string) => document.querySelector("[data-role-option='" + name + "']") as HTMLElement;

describe("the Assign a role dialog", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(catalogPreview).mockResolvedValue({ entries: [] });
  });

  it("groups the roles not held by kind, application first, each with what it gives", () => {
    draw();
    expect(dialog().getAttribute("data-assign-dialog")).not.toBeNull();
    expect(within(dialog()).getByRole("heading", { name: "Assign a role to altair" })).toBeTruthy();
    expect([...document.querySelectorAll("[data-role-group]")].map((g) => g.getAttribute("data-role-group"))).toEqual(["application", "business", "approver", "straza"]);
    expect(option("github-readers")).toBeNull();
    expect(option("demo-tools-sandbox").textContent).toBe("demo-tools-sandbox1 of 2 tools on demo-tools.");
    expect(option("platform-engineer").textContent).toBe("platform-engineerIncludes github-readers and demo-tools-sandbox.");
    expect(option("straza-admin").textContent).toBe("straza-adminStraza administration");
  });

  it("disables an approver role for an agent and offers it to a person", () => {
    draw();
    expect(option("sec-approvers").textContent).toBe("sec-approvers" + DECIDES_REQUESTS + " " + NOT_FOR_AGENT);
    expect((within(option("sec-approvers")).getByRole("radio") as HTMLButtonElement).disabled).toBe(true);
  });

  it("draws the approver row an agent cannot take at full opacity, its name muted, and keeps it out of reach", async () => {
    draw();
    const row = option("sec-approvers");
    expect(row.className).not.toContain("opacity");
    const name = within(row).getByText("sec-approvers");
    expect(name.className).toContain("text-muted-foreground");
    expect(name.className).not.toContain("text-foreground");
    expect(within(row).getByText(DECIDES_REQUESTS + " " + NOT_FOR_AGENT).className).toContain("text-text-2");
    const radio = within(row).getByRole("radio") as HTMLButtonElement;
    expect(radio.disabled).toBe(true);
    await userEvent.click(row);
    expect(radio.getAttribute("aria-checked")).toBe("false");
    expect(document.querySelector("[data-gains]")).toBeNull();
  });

  it("says what a role gives and not when it last changed", () => {
    const changed = new Date(Date.now() - 3 * 86400000 - 60000).toISOString();
    draw({ roles: [{ ...roles[1], updated_at: changed }, { id: "r-ops", name: "straza-operators", kind: "straza", updated_at: changed }, roles[2]] });
    expect(option("demo-tools-sandbox").textContent).toBe("demo-tools-sandbox1 of 2 tools on demo-tools.");
    // A role with no line of its own shows its name alone.
    expect(option("straza-operators").textContent).toBe("straza-operators");
    expect(option("platform-engineer").textContent).toBe("platform-engineerIncludes github-readers and demo-tools-sandbox.");
  });

  it("offers an approver role to a person", () => {
    draw({ machine: false });
    expect(option("sec-approvers").textContent).toBe("sec-approvers" + DECIDES_REQUESTS);
    expect((within(option("sec-approvers")).getByRole("radio") as HTMLButtonElement).disabled).toBe(false);
  });

  it("narrows the list by the search, and says when nothing matches", async () => {
    draw();
    await userEvent.type(screen.getByRole("textbox", { name: "Search roles" }), "administration");
    expect([...document.querySelectorAll("[data-role-option]")].map((o) => o.getAttribute("data-role-option"))).toEqual(["straza-admin"]);
    await userEvent.type(screen.getByRole("textbox", { name: "Search roles" }), " nothing");
    expect(screen.getByText(NO_ROLE_MATCHES)).toBeTruthy();
  });

  it.each<[string, RoleRow[] | null, string]>([
    ["every role is held", [roles[0]], "Every role is already assigned."],
    ["the catalog is empty", [], "No role exists yet to assign. Create one under Roles."],
    ["the catalog could not be read", null, "The roles could not be read. Close this and reload the page."],
  ])("says there is nothing to offer when %s", (_name, list, want) => {
    draw({ roles: list });
    expect((document.querySelector("[data-nothing-to-offer]") as HTMLElement).textContent).toBe(want);
  });

  it("asks for the pick at the list when nothing is picked", async () => {
    draw();
    await userEvent.click(within(dialog()).getByRole("button", { name: "Assign a role" }));
    expect(screen.getByText(GRANT_MISSING)).toBeTruthy();
    expect(document.activeElement?.getAttribute("role")).toBe("radio");
    expect(grantRole).not.toHaveBeenCalled();
  });

  it("says what the user gains, assigns, and hands the role back", async () => {
    vi.mocked(grantRole).mockResolvedValue(held[0]);
    const { onAssigned } = draw({ scim: true });
    await userEvent.click(within(option("platform-engineer")).getByRole("radio"));
    expect((document.querySelector("[data-gains]") as HTMLElement).textContent).toBe(grantBody("business", "altair", "platform-engineer"));
    expect(screen.getByText(IDM_OWNS)).toBeTruthy();
    await userEvent.click(within(dialog()).getByRole("button", { name: "Assign platform-engineer" }));
    await waitFor(() => expect(grantRole).toHaveBeenCalledWith("u-altair", "r-pe"));
    expect(onAssigned).toHaveBeenCalledWith("platform-engineer");
  });

  it("says under the pick what the role gives on each server on the person's machine, in its three forms", async () => {
    const named = (app: string, tool: string): PreviewEntry => ({ app, tool, status: "visible", reason: "", setName: "audio-access" });
    vi.mocked(catalogPreview).mockResolvedValue({
      entries: [],
      machine: [
        { app: "elevenlabs", open: true, tools: [named("elevenlabs", "text_to_speech"), named("elevenlabs", "clone_voice")] },
        { app: "filesystem", open: true, tools: [] },
        { app: "notes", tools: [named("notes", "read_note")] },
      ],
    });
    draw({ scim: true });
    await userEvent.click(within(option("platform-engineer")).getByRole("radio"));
    await waitFor(() => expect(document.querySelectorAll("[data-machine-gives]").length).toBe(3));
    expect(catalogPreview).toHaveBeenCalledTimes(1);
    expect(catalogPreview).toHaveBeenCalledWith("platform-engineer");
    const lines = [...document.querySelectorAll("[data-machine-gives]")];
    expect(lines.map((l) => l.textContent)).toEqual([
      "2 tools of elevenlabs on the person's machine, and more by a pattern.",
      "Tools of filesystem on the person's machine, named by a pattern.",
      "1 tool of notes on the person's machine.",
    ]);
    expect(lines[0].className).toContain("text-muted-foreground");
    // The lines stand under the sentence for the pick, above the identity
    // manager's line.
    const order = [...(document.querySelector("[data-gains]")?.parentElement?.children || [])].map((c) => c.getAttribute("data-machine-gives") ?? (c.hasAttribute("data-gains") ? "gains" : c.textContent));
    expect(order).toEqual(["gains", "elevenlabs", "filesystem", "notes", IDM_OWNS]);
  });

  it("reads once per pick, and never shows the answer of an earlier pick for the next", async () => {
    let answer: (v: PreviewAnswer) => void = () => {};
    vi.mocked(catalogPreview)
      .mockResolvedValueOnce({ entries: [], machine: [{ app: "elevenlabs", open: true, tools: [] }] })
      .mockReturnValueOnce(new Promise((resolve) => { answer = resolve; }));
    draw();
    await userEvent.click(within(option("platform-engineer")).getByRole("radio"));
    expect(await screen.findByText("Tools of elevenlabs on the person's machine, named by a pattern.")).toBeTruthy();
    await userEvent.click(within(option("straza-admin")).getByRole("radio"));
    expect(catalogPreview).toHaveBeenCalledTimes(2);
    expect(catalogPreview).toHaveBeenLastCalledWith("straza-admin");
    // While the read for the second pick runs, nothing stands under it.
    expect(document.querySelector("[data-gains]")).not.toBeNull();
    expect(document.querySelector("[data-machine-gives]")).toBeNull();
    answer({ entries: [] });
    await waitFor(() => expect(catalogPreview).toHaveBeenCalledTimes(2));
    expect(document.querySelector("[data-machine-gives]")).toBeNull();
  });

  it.each<[string, () => void]>([
    ["the read fails", () => vi.mocked(catalogPreview).mockRejectedValue(refusal("down"))],
    ["the answer has no machine", () => vi.mocked(catalogPreview).mockResolvedValue({ entries: [] })],
    ["the read is still out", () => vi.mocked(catalogPreview).mockReturnValue(new Promise(() => {}))],
  ])("shows no line under the pick when %s", async (_name, arrange) => {
    arrange();
    draw();
    await userEvent.click(within(option("platform-engineer")).getByRole("radio"));
    await waitFor(() => expect(catalogPreview).toHaveBeenCalledWith("platform-engineer"));
    await new Promise((r) => setTimeout(r, 0));
    expect((document.querySelector("[data-gains]") as HTMLElement).textContent).toBe(grantBody("business", "altair", "platform-engineer"));
    expect(document.querySelector("[data-machine-gives]")).toBeNull();
  });

  it("reads no preview without the MCP servers area", async () => {
    draw({ servers: false });
    expect(catalogPreview).not.toHaveBeenCalled();
    await userEvent.click(within(option("platform-engineer")).getByRole("radio"));
    expect(document.querySelector("[data-gains]")).not.toBeNull();
    expect(catalogPreview).not.toHaveBeenCalled();
    expect(document.querySelector("[data-machine-gives]")).toBeNull();
  });

  it("reads no preview for a role that is not picked", () => {
    draw();
    expect(catalogPreview).not.toHaveBeenCalled();
  });

  it("shows a refusal inside the dialog", async () => {
    vi.mocked(grantRole).mockRejectedValue(refusal("role is already assigned"));
    const { onAssigned } = draw();
    await userEvent.click(within(option("straza-admin")).getByRole("radio"));
    await userEvent.click(within(dialog()).getByRole("button", { name: "Assign straza-admin" }));
    expect((await within(dialog()).findByRole("alert")).textContent).toBe("Assign straza-admin refused: role is already assigned. Fix what it names, then try again.");
    expect(onAssigned).not.toHaveBeenCalled();
  });
});
