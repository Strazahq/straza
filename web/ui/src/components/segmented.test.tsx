import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UsersIcon } from "lucide-react";
import { Segmented } from "./segmented";

// The pressed segment shows in both themes: it carries the accent fill and
// no dark-only class that could outrank it.

describe("Segmented", () => {
  const options = [
    { value: "all", label: "All", count: 4 },
    { value: "mine", label: "Mine" },
  ] as const;

  it("names the group and presses one button", () => {
    render(<Segmented label="Whose" value="mine" onChange={() => {}} options={[...options]} />);
    const group = screen.getByRole("group", { name: "Whose" });
    expect(group).toBeTruthy();
    expect(screen.getByRole("button", { name: "All 4" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("button", { name: "Mine" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("leads an option with its icon, hidden beside the label", () => {
    render(<Segmented label="Kind" value="people" onChange={() => {}} options={[{ value: "people", label: "People", icon: UsersIcon, count: 6 }]} />);
    const button = screen.getByRole("button", { name: "People 6" });
    expect(button.querySelector("svg")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("calls onChange with the value of the clicked button", async () => {
    const onChange = vi.fn();
    render(<Segmented label="Whose" value="mine" onChange={onChange} options={[...options]} />);
    await userEvent.click(screen.getByRole("button", { name: "All 4" }));
    expect(onChange).toHaveBeenCalledWith("all");
  });

  it("paints the pressed button with the accent fill and no dark-only class", () => {
    render(<Segmented label="Whose" value="all" onChange={() => {}} options={[...options]} />);
    const pressed = screen.getByRole("button", { name: "All 4" });
    expect(pressed.classList).toContain("bg-accent-bg");
    expect(Array.from(pressed.classList).some((c) => c.startsWith("dark:"))).toBe(false);
  });
});
