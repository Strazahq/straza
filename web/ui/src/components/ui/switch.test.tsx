import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Switch } from "./switch";

describe("the Switch primitive", () => {
  it("is a switch a person can read and toggle by keyboard", async () => {
    const onChange = vi.fn();
    render(<Switch aria-label="Follow" checked={false} onCheckedChange={onChange} />);
    const box = screen.getByRole("switch", { name: "Follow" });
    expect(box.getAttribute("aria-checked")).toBe("false");
    box.focus();
    await userEvent.keyboard("{Enter}");
    expect(onChange).toHaveBeenCalledWith(true);
  });

  it("shows the checked state the caller holds", () => {
    render(<Switch aria-label="Follow" checked onCheckedChange={() => {}} />);
    expect(screen.getByRole("switch", { name: "Follow" }).getAttribute("aria-checked")).toBe("true");
  });
});
