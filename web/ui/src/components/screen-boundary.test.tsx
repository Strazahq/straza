import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ScreenBoundary } from "./screen-boundary";

let explode = true;

function Screen() {
  if (explode) throw new Error("config.oidc is undefined");
  return <p>The screen rendered.</p>;
}

describe("the screen boundary", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    explode = true;
  });

  it("keeps the sidebar when a screen throws and offers try again", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    render(
      <div>
        <nav aria-label="Areas"><a href="/console/servers">MCP servers</a></nav>
        <main>
          <ScreenBoundary>
            <Screen />
          </ScreenBoundary>
        </main>
      </div>,
    );
    expect(screen.getByRole("navigation", { name: "Areas" })).toBeTruthy();
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("This screen hit an error.");
    expect(alert.textContent).toContain("config.oidc is undefined");
    expect(alert.textContent).toContain("The rest of the console is unaffected: switch screens, or try again.");
    explode = false;
    await userEvent.click(screen.getByRole("button", { name: "try again" }));
    expect(screen.getByText("The screen rendered.")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
