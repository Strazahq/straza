import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionGlance } from "./session-glance";

const draw = (allowed: number | null, waiting: number, denied: number | null, turns: number | null) => {
  const props = { onFilter: vi.fn(), onTurns: vi.fn() };
  render(<SessionGlance allowed={allowed} waiting={waiting} denied={denied} turns={turns} {...props} />);
  return props;
};
const tiles = () => [...document.querySelectorAll("[data-glance]")].map((b) => b.getAttribute("data-glance") + ": " + b.textContent);

describe("the session's glance tiles", () => {
  it("draws each tile as a number over its words, the waiting and denied numbers in their tones", () => {
    draw(41, 1, 2, 18);
    expect(tiles()).toEqual(["allowed: 41 calls allowed", "waiting: 1 waiting for approval", "denied: 2 denied", "turns: 18 turns recorded"]);
    expect((screen.getByRole("button", { name: "1 waiting for approval" }).firstElementChild as HTMLElement).className).toContain("text-warn");
    expect((screen.getByRole("button", { name: "2 denied" }).firstElementChild as HTMLElement).className).toContain("text-danger");
  });

  it("words one call and one turn", () => {
    draw(1, 0, 0, 1);
    expect(tiles()).toEqual(["allowed: 1 call allowed", "turns: 1 turn recorded"]);
  });

  it("leaves out a tile at zero and one whose count is not there", () => {
    draw(null, 0, null, null);
    expect(tiles()).toEqual([]);
    expect(document.querySelector("[data-glance-row]")).toBeNull();
  });

  it("sets the activity's filter from the counts and opens the transcript from the turns", async () => {
    const { onFilter, onTurns } = draw(41, 1, 2, 18);
    await userEvent.click(screen.getByRole("button", { name: "41 calls allowed" }));
    expect(onFilter).toHaveBeenLastCalledWith("all");
    await userEvent.click(screen.getByRole("button", { name: "1 waiting for approval" }));
    expect(onFilter).toHaveBeenLastCalledWith("waiting");
    await userEvent.click(screen.getByRole("button", { name: "2 denied" }));
    expect(onFilter).toHaveBeenLastCalledWith("denied");
    await userEvent.click(screen.getByRole("button", { name: "18 turns recorded" }));
    expect(onTurns).toHaveBeenCalledTimes(1);
  });
});
