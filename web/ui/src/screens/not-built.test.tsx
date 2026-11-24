import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { NotBuilt } from "./not-built";
import { type RouteKey, routeByKey } from "@/lib/routes";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

// The locked panel says the role lacks the area and how to get it, the same
// for every seat. It never offers strazactl, which the same check refuses.
describe("the locked panel of an area", () => {
  it.each([
    { name: "an account whose first area is Audit", first: "audit", button: "Open Audit" },
    { name: "a server admin with no area grant", first: "servers", button: "Open MCP servers" },
    { name: "an account with no area", first: null, button: null },
  ])("says what the role lacks for $name", ({ first, button }) => {
    render(<NotBuilt route={routeByKey("roles")} reachable={false} first={first ? routeByKey(first as RouteKey) : null} />);
    expect(screen.getByText("Roles is not available to this account.")).toBeTruthy();
    expect(screen.getByText("Your role does not include Roles. Ask an administrator for a role that does.")).toBeTruthy();
    expect(screen.queryByText(/strazactl/)).toBeNull();
    if (button) expect(screen.getByRole("button", { name: button })).toBeTruthy();
    else expect(screen.queryByRole("button")).toBeNull();
  });
});
