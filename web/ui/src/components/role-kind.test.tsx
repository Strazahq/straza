import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { BriefcaseBusinessIcon, IdCardIcon, ServerCogIcon, ShieldIcon, UserCheckIcon } from "lucide-react";
import { KIND_ICON } from "./kind-icon";
import { KindGlyph, MARKS, type Mark, RoleKindBadge, RoleKindTag, markOf } from "./role-kind";

// One glyph per kind, every role kind plain, and the reading of the two
// MCP admin kinds off the servers list rather than the name.

describe("the kind marks", () => {
  it.each<[Mark, unknown]>([
    ["application", IdCardIcon],
    ["business", BriefcaseBusinessIcon],
    ["approver", UserCheckIcon],
    ["straza", ShieldIcon],
    ["server-admin", ServerCogIcon],
    ["global-admin", ServerCogIcon],
  ])("%s wears its decided glyph", (mark, icon) => {
    expect(KIND_ICON[MARKS[mark]]).toBe(icon);
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

  it.each<Mark>(["application", "business", "approver", "straza", "server-admin", "global-admin"])("draws the %s glyph hidden from assistive technology, in the muted colour", (mark) => {
    render(<KindGlyph mark={mark} />);
    const svg = document.querySelector('[data-kind-glyph="' + mark + '"]') as SVGElement;
    expect(svg.tagName.toLowerCase()).toBe("svg");
    expect(svg.getAttribute("aria-hidden")).toBe("true");
    expect(svg.getAttribute("class")).toContain("text-muted-foreground");
  });

  it("puts the glyph inside the kind badge and keeps the kind a word", () => {
    render(<RoleKindBadge role={{ kind: "straza", name: "mcp-admin-demo-tools" }} administers={["demo-tools"]} />);
    const badge = document.querySelector("[data-kind]") as HTMLElement;
    expect(badge.getAttribute("data-kind")).toBe("Straza role");
    expect(badge.textContent).toBe("Straza role");
    expect(badge.querySelector('[data-kind-glyph="server-admin"]')).toBeTruthy();
  });

  it("draws the kind as a round pill for a fact, with no tone, since teal marks an AI agent", () => {
    render(<><RoleKindBadge role={{ kind: "application" }} /><RoleKindBadge role={{ kind: "business" }} /></>);
    const [app, biz] = Array.from(document.querySelectorAll("[data-kind]"));
    expect(app.getAttribute("data-tone")).toBeNull();
    expect(app.className).toContain("rounded-pill");
    expect(app.querySelector('[data-kind-glyph="application"]')).toBeTruthy();
    expect(biz.getAttribute("data-tone")).toBeNull();
    expect(biz.querySelector('[data-kind-glyph="business"]')).toBeTruthy();
  });

  it("names a role in a tag with its kind's icon, and plain when its kind is not known", () => {
    render(<><RoleKindTag name="dev-tools" role={{ kind: "application", name: "dev-tools" }} /><RoleKindTag name="mcp-admin-demo-tools" role={{ kind: "straza", name: "mcp-admin-demo-tools" }} administers={["demo-tools"]} /><RoleKindTag name="gone" /></>);
    const [app, admin, gone] = Array.from(document.querySelectorAll("[data-role-kind-tag]")) as HTMLElement[];
    expect(app.textContent).toBe("dev-tools");
    expect(app.querySelector("svg")?.getAttribute("class")).toContain("lucide-id-card");
    expect(admin.querySelector("svg")?.getAttribute("class")).toContain("lucide-server-cog");
    expect(gone.querySelector("svg")).toBeNull();
    expect(gone.getAttribute("title")).toBe("gone");
  });
});
