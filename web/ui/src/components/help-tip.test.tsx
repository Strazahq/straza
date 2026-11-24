import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { HelpTip } from "./help-tip";
import { TooltipProvider } from "@/components/ui/tooltip";

// Help opens on a click or a tap as well as on hover, so a phone can read
// it; a second click, Escape and a click outside close it again.

function mount() {
  const row = vi.fn();
  render(
    <TooltipProvider>
      <div onClick={row}>
        <HelpTip label="Owner" text="Who answers for this role." />
      </div>
      <p>elsewhere</p>
    </TooltipProvider>,
  );
  return { icon: screen.getByRole("button", { name: "Help: Owner" }), row };
}

describe("HelpTip", () => {
  it("opens on a click without a hover, and the click stays off the row", async () => {
    const { icon, row } = mount();
    await userEvent.pointer({ keys: "[MouseLeft]", target: icon });
    expect(screen.getByRole("tooltip").textContent).toBe("Who answers for this role.");
    expect(icon.getAttribute("aria-expanded")).toBe("true");
    expect(row).not.toHaveBeenCalled();
  });

  it("closes on a second click", async () => {
    const { icon } = mount();
    await userEvent.pointer({ keys: "[MouseLeft]", target: icon });
    expect(screen.getByRole("tooltip")).toBeTruthy();
    await userEvent.pointer({ keys: "[MouseLeft]", target: icon });
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(icon.getAttribute("aria-expanded")).toBe("false");
  });

  it("closes on Escape", async () => {
    const { icon } = mount();
    await userEvent.pointer({ keys: "[MouseLeft]", target: icon });
    expect(screen.getByRole("tooltip")).toBeTruthy();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("closes on a click outside", async () => {
    const { icon } = mount();
    await userEvent.pointer({ keys: "[MouseLeft]", target: icon });
    expect(screen.getByRole("tooltip")).toBeTruthy();
    await userEvent.pointer({ keys: "[MouseLeft]", target: screen.getByText("elsewhere") });
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
