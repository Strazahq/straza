import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { NotBuilt } from "./not-built";
import { routeByKey } from "@/lib/routes";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

describe("the locked panel of an area", () => {
  it("names the grants the account holds when it has some", () => {
    render(<NotBuilt route={routeByKey("roles")} reachable={false} grants="audit:read" first={routeByKey("audit")} />);
    expect(screen.getByText("Roles is not available to this account.")).toBeTruthy();
    expect(screen.getByText("This account's admin scopes cover audit:read, which does not reach Roles. Ask for a wider role, or use strazactl.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open Audit" })).toBeTruthy();
  });

  it("says what a server admin does reach when the account holds no area grant", () => {
    render(<NotBuilt route={routeByKey("roles")} reachable={false} grants="" first={routeByKey("servers")} administers={1} />);
    expect(screen.getByText("This account administers 1 MCP server and holds no area scope, which does not reach Roles. Ask for a wider role, or use strazactl.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open MCP servers" })).toBeTruthy();
  });

  it("keeps the nothing wording for an account with neither", () => {
    render(<NotBuilt route={routeByKey("roles")} reachable={false} grants="" first={null} />);
    expect(screen.getByText("This account's admin scopes cover nothing, which does not reach Roles. Ask for a wider role, or use strazactl.")).toBeTruthy();
  });
});
