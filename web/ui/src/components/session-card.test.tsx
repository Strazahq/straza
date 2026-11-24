import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SessionCard } from "./session-card";

describe("the session card", () => {
  it("heads its body with the title and what follows it", () => {
    render(<SessionCard title="Health" extra={<span>newest first</span>} data-health-card><p>body</p></SessionCard>);
    const card = document.querySelector("[data-health-card]") as HTMLElement;
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Health");
    expect(card.textContent).toBe("Healthnewest firstbody");
  });
});
