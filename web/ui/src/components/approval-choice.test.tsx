import * as React from "react";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApprovalChoice, spanOf } from "./approval-choice";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type Shape, defaultShape } from "@/lib/access-plan";
import type { RoleRow } from "@/lib/api";
import { HOW_HOLD, HOW_TICKET, HOW_TICKET_END, NO_APPROVER_ROLE, W1, WHO_SPONSOR, WHO_TEAM } from "@/lib/policy-words";

const approvers: RoleRow[] = [
  { id: "r-9", name: "sec-approvers", kind: "approver", holder_count: 2 },
  { id: "r-8", name: "ops-approvers", kind: "approver", holder_count: 9 },
];

// latest is the setting the harness holds after the last change.
let latest: Shape = defaultShape();

function Harness({ seed, roles = approvers }: { seed?: Partial<Shape>; roles?: RoleRow[] }) {
  const [shape, setShape] = React.useState<Shape>(() => ({ ...defaultShape(), ...seed }));
  latest = shape;
  return (
    <TooltipProvider>
      <ApprovalChoice shape={shape} approvers={roles} onShape={setShape} />
    </TooltipProvider>
  );
}

const radio = (name: string) => screen.getByRole("radio", { name });
const box = (name: string) => screen.getByRole("spinbutton", { name }) as HTMLInputElement;
const unit = (name: string) => screen.getByRole("combobox", { name: name + " unit" }).textContent;
const w1 = () => document.querySelector("[data-w1]")?.textContent || null;

describe("the approval choice", () => {
  it("reads a window in the largest unit that divides it", () => {
    const cases: [number, ReturnType<typeof spanOf>][] = [
      [90, { n: 90, unit: "seconds" }],
      [120, { n: 2, unit: "minutes" }],
      [7200, { n: 2, unit: "hours" }],
      [86400, { n: 1, unit: "days" }],
      [90000, { n: 25, unit: "hours" }],
    ];
    for (const [seconds, span] of cases) expect(spanOf(seconds)).toEqual(span);
  });

  it("opens on the person behind the agent and a two minute hold, with a role already in the picker", () => {
    render(<Harness />);
    expect(radio(WHO_SPONSOR).getAttribute("aria-checked")).toBe("true");
    expect(radio(HOW_HOLD).getAttribute("aria-checked")).toBe("true");
    expect(box(HOW_HOLD).value).toBe("2");
    expect(unit(HOW_HOLD)).toBe("minutes");
    expect(box(HOW_TICKET).value).toBe("1");
    expect(unit(HOW_TICKET)).toBe("day");
    expect(box(HOW_TICKET_END).value).toBe("1");
    expect(unit(HOW_TICKET_END)).toBe("hour");
    expect(screen.getByRole("combobox", { name: WHO_TEAM }).textContent).toBe("sec-approvers");
    expect(screen.getByText("2 holders")).toBeTruthy();
  });

  it("routes to the first approver role, or to the role picked from the list", async () => {
    render(<Harness />);
    await userEvent.click(radio(WHO_TEAM));
    expect(latest.pool).toBe("sec-approvers");
    await userEvent.click(radio(WHO_SPONSOR));
    expect(latest.pool).toBe("sponsor");
    await userEvent.click(screen.getByRole("combobox", { name: WHO_TEAM }));
    await userEvent.click(await screen.findByRole("option", { name: "ops-approvers" }));
    expect(latest.pool).toBe("ops-approvers");
    expect(radio(WHO_TEAM).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByText("9 holders")).toBeTruthy();
  });

  it("keeps a stored role that is not in the list, and says when no approver role exists", async () => {
    const { unmount } = render(<Harness seed={{ pool: "straza-admin" }} />);
    expect(screen.getByRole("combobox", { name: WHO_TEAM }).textContent).toBe("straza-admin");
    unmount();
    render(<Harness roles={[]} />);
    expect(screen.queryByRole("combobox", { name: WHO_TEAM })).toBeNull();
    expect(screen.getByText(NO_APPROVER_ROLE)).toBeTruthy();
    await userEvent.click(radio(WHO_TEAM));
    expect(latest.pool).toBe("sponsor");
  });

  it("stores windows in seconds and picks the branch whose window is typed", async () => {
    render(<Harness />);
    await userEvent.clear(box(HOW_TICKET));
    await userEvent.type(box(HOW_TICKET), "3");
    expect(latest).toMatchObject({ how: "ticket", ticket: 3 * 86400, hold: 120 });
    await userEvent.click(radio(HOW_HOLD));
    await userEvent.clear(box(HOW_HOLD));
    await userEvent.type(box(HOW_HOLD), "90");
    await userEvent.click(screen.getByRole("combobox", { name: HOW_HOLD + " unit" }));
    await userEvent.click(await screen.findByRole("option", { name: "seconds" }));
    expect(latest).toMatchObject({ how: "hold", hold: 90 });
    expect(box(HOW_HOLD).value).toBe("90");
    expect(unit(HOW_HOLD)).toBe("seconds");
  });

  it("warns when a hold goes to an approver role with two holders or fewer, or for under two minutes", async () => {
    const cases: [Partial<Shape>, string | null][] = [
      [{ pool: "sec-approvers" }, W1(2, 120)],
      [{ pool: "sec-approvers", hold: 600 }, W1(2, 600)],
      [{ pool: "sec-approvers", how: "ticket" }, null],
      [{ pool: "ops-approvers" }, null],
      [{ pool: "ops-approvers", hold: 90 }, W1(9, 90)],
      [{ pool: "sponsor" }, null],
    ];
    for (const [seed, want] of cases) {
      const { unmount } = render(<Harness seed={seed} />);
      expect(w1()).toBe(want);
      unmount();
    }
  });
});
