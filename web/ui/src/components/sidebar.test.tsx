import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { Sidebar } from "./sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ROUTES, serverBadge, serversBadgeTitle, visibleRoutes } from "@/lib/routes";

// The rail a session sees. The interesting session holds no area
// grant and administers one MCP server, so the count is the only reason the
// MCP servers row is there at all.

// rail renders the sidebar the way the shell does, from the session's areas
// and the servers it administers, and answers the route keys it drew.
function rail(areas: Record<string, boolean> | null, servers: number): string[] {
  const visible = visibleRoutes(areas, servers);
  render(
    <TooltipProvider>
      <Sidebar routes={visible} active="servers" servers={serverBadge(areas, servers)} />
    </TooltipProvider>,
  );
  return visible.map((r) => r.key);
}

const badge = () => document.querySelector("[data-servers-badge]") as HTMLElement | null;
const labels = () => within(screen.getByRole("navigation", { name: "Areas" })).queryAllByRole("link").map((a) => (a.textContent || "").trim());

describe("the sidebar of a session that administers MCP servers", () => {
  it("lists MCP servers and the Drafts a server admin may write, and keeps every other area out", () => {
    expect(rail({}, 1)).toEqual(["servers", "drafts"]);
    expect(labels()).toEqual(["MCP servers1", "Drafts"]);
  });

  it("badges how many servers the session administers, and says so on hover", () => {
    rail({}, 3);
    expect(badge()?.textContent).toBe("3");
    expect(badge()?.getAttribute("title")).toBe("This account administers 3 MCP servers.");
    expect(serversBadgeTitle(1)).toBe("This account administers 1 MCP server.");
  });

  it("adds MCP servers and Drafts to the areas a mixed session does hold", () => {
    expect(rail({ audit: true }, 1)).toEqual(["audit", "servers", "drafts"]);
  });

  it("draws no badge when the session's own apps grant opens every server", () => {
    expect(rail({ apps: true }, 2)).toEqual(["servers", "drafts"]);
    expect(badge()).toBeNull();
  });

  it("leaves the root admin's rail whole and unbadged", () => {
    expect(rail(null, 1)).toEqual(ROUTES.map((r) => r.key));
    expect(badge()).toBeNull();
  });

  it("shows no MCP servers row for a session with neither a grant nor a server", () => {
    expect(rail({}, 0)).toEqual([]);
    expect(labels()).toEqual([]);
  });
});

 it("keeps personal self-service separate from permitted administration areas", () => {
   rail({}, 0);
   expect(screen.getByRole("link", { name: /Self-service/ }).getAttribute("href")).toBe("/self-service/");
   expect(screen.queryByRole("heading", { name: "Access & governance" })).toBeNull();
 });

describe("the Drafts row", () => {
  // Drafts opens to every grant that may draft: the drafts area itself,
  // and the areas whose objects a draft changes.
  const grants: [string, Record<string, boolean>, boolean][] = [
    ["the drafts area", { drafts: true }, true],
    ["the apps area", { apps: true }, true],
    ["the identity area", { identity: true }, true],
    ["the policy area", { policy: true }, true],
    ["the audit area alone", { audit: true }, false],
  ];
  it.each(grants)("is there for %s: %s", (_name, areas, shown) => {
    expect(visibleRoutes(areas, 0).some((r) => r.key === "drafts")).toBe(shown);
  });

  it("sits in Decisions after Approvals", () => {
    render(<TooltipProvider><Sidebar routes={ROUTES} active="drafts" /></TooltipProvider>);
    const decisions = screen.getByRole("region", { name: "Decisions" });
    expect(within(decisions).getAllByRole("link").map((a) => (a.textContent || "").trim())).toEqual(["Approvals", "Drafts"]);
  });

  it("badges the drafts that wait, and says so on hover", () => {
    render(<TooltipProvider><Sidebar routes={ROUTES} active="drafts" drafts="200+" /></TooltipProvider>);
    const b = document.querySelector("[data-drafts-badge]") as HTMLElement;
    expect(b.textContent).toBe("200+");
    expect(b.getAttribute("title")).toBe("200+ drafts wait for review.");
  });

  it("draws no badge when none waits", () => {
    render(<TooltipProvider><Sidebar routes={ROUTES} active="drafts" drafts="" /></TooltipProvider>);
    expect(document.querySelector("[data-drafts-badge]")).toBeNull();
  });
});
