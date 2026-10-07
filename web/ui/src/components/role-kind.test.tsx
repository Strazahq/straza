import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { BriefcaseIcon, IdCardIcon, ServerCogIcon, ServerIcon, ShieldCogIcon, UserCheckIcon } from "lucide-react";
import { KindGlyph, MARKS, type Mark, RoleKindBadge, markOf } from "./role-kind";

// One glyph and one tone per kind, and the reading of the two MCP admin
// kinds off the servers list rather than the name.

describe("the kind marks", () => {
  it.each<[Mark, unknown, string]>([
    ["application", IdCardIcon, "accent"],
    ["business", BriefcaseIcon, "teal"],
    ["approver", UserCheckIcon, "plain"],
    ["straza", ShieldCogIcon, "plain"],
    ["server-admin", ServerIcon, "plain"],
    ["global-admin", ServerCogIcon, "plain"],
  ])("%s wears its decided glyph and tone", (mark, icon, tone) => {
    expect(MARKS[mark].icon).toBe(icon);
    expect(MARKS[mark].tone).toBe(tone);
  });

  it.each<[string, { kind: string; name?: string }, string[] | null | undefined, Mark]>([
    ["an application role", { kind: "application", name: "dev-tools" }, [], "application"],
    ["a row with no kind, the store's default", { kind: "", name: "old" }, undefined, "business"],
    ["a business role", { kind: "business", name: "dev" }, [], "business"],
    ["an approver role", { kind: "approver", name: "sec-approvers" }, [], "approver"],
    ["a straza role with areas", { kind: "straza", name: "auditor" }, [], "straza"],
    ["the product's global MCP admin role, whatever the servers say", { kind: "straza", name: "straza-global-mcp-admin" }, [], "global-admin"],
    ["a straza role a server names as its admin role", { kind: "straza", name: "mcp-admin-demo-tools" }, ["demo-tools"], "server-admin"],
    ["a role named like a minted one that no server names", { kind: "straza", name: "mcp-admin-x" }, [], "straza"],
    ["a role named like a minted one when the servers could not be read", { kind: "straza", name: "mcp-admin-demo-tools" }, null, "straza"],
    ["a straza role known by kind alone", { kind: "straza" }, undefined, "straza"],
  ])("%s", (_, role, administers, want) => {
    expect(markOf(role, administers)).toBe(want);
  });

  it.each<[Mark, string]>([
    ["application", "text-link"],
    ["business", "text-teal"],
    ["approver", "text-muted-foreground"],
    ["straza", "text-muted-foreground"],
    ["server-admin", "text-muted-foreground"],
    ["global-admin", "text-muted-foreground"],
  ])("draws the %s glyph hidden from assistive technology, in its tone", (mark, tone) => {
    render(<KindGlyph mark={mark} />);
    const svg = document.querySelector('[data-kind-glyph="' + mark + '"]') as SVGElement;
    expect(svg.tagName.toLowerCase()).toBe("svg");
    expect(svg.getAttribute("aria-hidden")).toBe("true");
    expect(svg.getAttribute("class")).toContain(tone);
  });

  it("puts the glyph inside the kind badge and keeps the kind a word", () => {
    render(<RoleKindBadge role={{ kind: "straza", name: "mcp-admin-demo-tools" }} administers={["demo-tools"]} />);
    const badge = document.querySelector("[data-kind]") as HTMLElement;
    expect(badge.getAttribute("data-kind")).toBe("Straza role");
    expect(badge.textContent).toBe("Straza role");
    expect(badge.querySelector('[data-kind-glyph="server-admin"]')).toBeTruthy();
  });

  it("paints the application badge in the accent and the business badge in teal", () => {
    render(<><RoleKindBadge role={{ kind: "application" }} /><RoleKindBadge role={{ kind: "business" }} /></>);
    const [app, biz] = Array.from(document.querySelectorAll("[data-kind]"));
    expect(app.getAttribute("class")).toContain("text-link");
    expect(app.querySelector('[data-kind-glyph="application"]')).toBeTruthy();
    expect(biz.getAttribute("class")).toContain("text-teal");
    expect(biz.querySelector('[data-kind-glyph="business"]')).toBeTruthy();
  });
});
