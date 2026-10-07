import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { RoleAreas } from "./role-areas";
import type { RoleRow } from "@/lib/api";
import { AREAS, AREAS_LINE, EVERY_AREA, LEVEL_WORD, NO_AREAS } from "@/lib/role-words";

const role = (areas?: string[]): RoleRow => ({ id: "r1", name: "auditor", kind: "straza", areas });

// levels reads the rendered grid: one word per area, in the grid's order.
const levels = () => AREAS.map(([name]) => (document.querySelector('[data-area="' + name + '"] [data-level], [data-area="' + name + '"] [data-level="none"]') as HTMLElement).textContent);

describe("the Areas tab", () => {
  it("reads every area as read and write for the root role", () => {
    render(<RoleAreas role={role(["full"])} />);
    expect(screen.getByText(EVERY_AREA)).toBeTruthy();
    expect(new Set(levels())).toEqual(new Set([LEVEL_WORD.write]));
  });

  it("reads a mapped role at the level its config gives each area", () => {
    render(<RoleAreas role={role(["audit:read", "sessions:read", "policy:write"])} />);
    expect(document.querySelector('[data-area="audit"] [data-level]')?.textContent).toBe(LEVEL_WORD.read);
    expect(document.querySelector('[data-area="policy"] [data-level]')?.textContent).toBe(LEVEL_WORD.write);
    expect(document.querySelector('[data-area="tokens"] [data-level]')?.textContent).toBe(LEVEL_WORD.none);
    expect(screen.queryByText(NO_AREAS)).toBeNull();
  });

  it("lists the drafts area among the twelve, at the level its config gives", () => {
    render(<RoleAreas role={role(["drafts:read"])} />);
    expect(AREAS.map(([name]) => name)).toContain("drafts");
    expect(AREAS).toHaveLength(12);
    expect(document.querySelector('[data-area="drafts"] [data-level]')?.textContent).toBe(LEVEL_WORD.read);
  });

  it("says no area is mapped when the config names none, and where the map lives", () => {
    render(<RoleAreas role={role([])} />);
    expect(screen.getByText(NO_AREAS)).toBeTruthy();
    expect(new Set(levels())).toEqual(new Set([LEVEL_WORD.none]));
    expect(screen.getByText(AREAS_LINE)).toBeTruthy();
  });

  it("says the same for a role whose row carries no area list at all", () => {
    render(<RoleAreas role={role()} />);
    expect(screen.getByText(NO_AREAS)).toBeTruthy();
  });
});
