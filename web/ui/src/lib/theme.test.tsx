import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { setTheme, themeChoice, useTheme } from "./theme";

function Probe() {
  const { choice, resolved } = useTheme();
  return <span data-probe>{choice + " " + resolved}</span>;
}

describe("the theme choice", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    setTheme("system");
  });

  it.each([
    ["dark", "dark"],
    ["light", "light"],
    ["system", null],
  ] as const)("%s leaves data-theme at %s", (choice, attr) => {
    setTheme("dark");
    setTheme(choice);
    expect(document.documentElement.getAttribute("data-theme")).toBe(attr);
    expect(localStorage.getItem("straza.console.theme")).toBe(choice);
  });

  it("re-renders the hook with the choice and the resolved theme", () => {
    render(<Probe />);
    act(() => setTheme("light"));
    expect(screen.getByText("light light")).toBeTruthy();
    act(() => setTheme("dark"));
    expect(screen.getByText("dark dark")).toBeTruthy();
    act(() => setTheme("system"));
    expect(screen.getByText("system dark")).toBeTruthy();
  });

  it("keeps the choice for the page when storage refuses the write", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("QuotaExceededError"); });
    expect(() => setTheme("dark")).not.toThrow();
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(themeChoice()).toBe("dark");
  });
});
