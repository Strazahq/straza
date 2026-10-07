import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Setup } from "./overview-setup";
import { TooltipProvider } from "@/components/ui/tooltip";
import { SETUP, setupProgress } from "@/lib/config-words";
import { navigate } from "@/lib/router";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const mount = (marks: Parameters<typeof Setup>[0]["marks"]) => render(<TooltipProvider><Setup marks={marks} /></TooltipProvider>);

const step = (key: string) => document.querySelector('[data-setup="' + key + '"]') as HTMLElement;

describe("the setup list of an empty deployment", () => {
  it("lists the five steps and counts the done ones", () => {
    mount({ server: true, role: false, policy: true, floor: false });
    expect(Array.from(document.querySelectorAll("[data-setup]")).map((s) => s.getAttribute("data-setup")))
      .toEqual(SETUP.map((s) => s.key));
    expect(screen.getByText(setupProgress(2, 5))).toBeTruthy();
    expect(step("server").getAttribute("data-done")).toBe("true");
    expect(step("role").getAttribute("data-done")).toBeNull();
  });

  it("leaves the approver step unmarked, because the console cannot read that fact here", () => {
    mount({ server: false, role: false, policy: false, floor: false });
    expect(screen.getByText(setupProgress(0, 5))).toBeTruthy();
    expect(step("approver").getAttribute("data-done")).toBeNull();
    expect(step("approver").querySelector("[data-mark]")).toBeNull();
    expect(step("policy").querySelector("[data-mark]")?.getAttribute("data-mark")).toBe("open");
  });

  it("gives every step its door", async () => {
    mount({});
    expect(document.querySelectorAll("[data-door]").length).toBe(SETUP.length);
    await userEvent.click(screen.getByRole("link", { name: /MCP servers/ }));
    expect(vi.mocked(navigate)).toHaveBeenCalledWith("servers", []);
  });
});
