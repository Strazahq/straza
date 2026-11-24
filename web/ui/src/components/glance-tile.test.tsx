import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { GlanceTile } from "./glance-tile";

describe("the glance tile", () => {
  it.each([
    [undefined, "text-foreground"],
    ["warn", "text-warn"],
    ["danger", "text-danger"],
  ] as const)("draws the number over its words, in the tone %s", async (tone, colour) => {
    const onOpen = vi.fn();
    render(<GlanceTile name="denied" n={2} words="denied" tone={tone} onOpen={onOpen} />);
    const tile = screen.getByRole("button", { name: "2 denied" });
    expect(tile.getAttribute("data-glance")).toBe("denied");
    expect((tile.firstElementChild as HTMLElement).className).toContain(colour);
    await userEvent.click(tile);
    expect(onOpen).toHaveBeenCalledTimes(1);
  });
});
