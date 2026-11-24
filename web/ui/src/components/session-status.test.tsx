import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { SessionStatus } from "./session-status";

describe("the session status", () => {
  it.each([
    ["active", "active", null],
    ["revoked", "revoked", "danger"],
    ["closed", "closed", "plain"],
    ["suspended", "suspended", "plain"],
  ])("draws %s", (status, word, tone) => {
    const { container } = render(<SessionStatus status={status} />);
    const chip = container.firstElementChild as HTMLElement;
    expect(chip.textContent).toBe(word);
    expect(chip.getAttribute("data-tone")).toBe(tone);
  });
});
