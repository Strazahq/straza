import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { EntraLink } from "./entra-link";
import type { UserRow } from "@/lib/api";
import { entraBlueprints } from "@/lib/keys-api";
import { ENTRA_SECTION, LINKED_BY_ADMIN, LINKED_BY_IDM, LINK_BY_IDM, NOT_LINKED, NO_TYPE, OWNER_IDM, OWNER_STRAZA, linkByAdmin } from "@/lib/keys-words";
import { adminAreas } from "@/lib/session";

vi.mock("@/lib/keys-api", async (orig) => ({ ...(await orig<typeof import("@/lib/keys-api")>()), entraBlueprints: vi.fn() }));
vi.mock("@/lib/session", async (orig) => ({ ...(await orig<typeof import("@/lib/session")>()), adminAreas: vi.fn() }));

const agent = (over: Partial<UserRow>): UserRow => ({
  id: "u1", username: "sam-sre-agent", status: "active", origin: "admin", kind: "nhi", user_type: "agent",
  created_at: "2026-10-01T00:00:00Z", updated_at: "2026-10-01T00:00:00Z", effective_roles: [], locks: [], sponsored_count: 0, ...over,
});

const owner = () => document.querySelector("[data-entra-owner]")?.textContent;
const link = () => document.querySelector("[data-entra-link]")?.getAttribute("data-entra-link");

describe("an agent's Entra link", () => {
  beforeEach(() => {
    vi.mocked(entraBlueprints).mockReset().mockResolvedValue(["76568ffa-de8d-4d35-986d-4efe2a9f8787"]);
    vi.mocked(adminAreas).mockReset().mockReturnValue(null);
  });

  it.each([
    { name: "a Straza agent that is not linked", user: agent({}), owner: OWNER_STRAZA, link: "none", how: linkByAdmin("sam-sre-agent") },
    { name: "a linked Straza agent", user: agent({ entra_agent_id: "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a" }), owner: OWNER_STRAZA, link: "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a", how: LINKED_BY_ADMIN },
    { name: "an identity manager's agent that is not linked", user: agent({ origin: "scim" }), owner: OWNER_IDM, link: "none", how: LINK_BY_IDM },
    { name: "an identity manager's linked agent", user: agent({ origin: "scim", entra_agent_id: "8c2f4e61-0b3d-4a7e-9f15-d6a2c8b04e93" }), owner: OWNER_IDM, link: "8c2f4e61-0b3d-4a7e-9f15-d6a2c8b04e93", how: LINKED_BY_IDM },
  ])("shows $name with its owner and how the link is written", async ({ user, owner: want, link: id, how }) => {
    render(<EntraLink user={user} />);
    await screen.findByText(how);
    expect(owner()).toBe(want);
    expect(link()).toBe(id);
    expect(screen.getByText(how)).toBeTruthy();
    if (id === "none") expect(screen.getByText(NOT_LINKED)).toBeTruthy();
  });

  it("offers no link on a record that has no type agent, because the server refuses one", async () => {
    render(<EntraLink user={agent({ user_type: undefined })} />);
    await screen.findByText(NO_TYPE);
    expect(link()).toBe("no-type");
    expect(screen.getByText(NO_TYPE)).toBeTruthy();
  });

  it("writes nothing: the section has no input and no button but Copy", () => {
    render(<EntraLink user={agent({ entra_agent_id: "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a" })} />);
    expect(document.querySelector("input, textarea, select")).toBeNull();
    expect(screen.getAllByRole("button").map((b) => b.textContent)).toEqual(["Copy"]);
  });

  it("draws nothing for an agent without a link when no provider uses Entra", async () => {
    vi.mocked(entraBlueprints).mockResolvedValue([]);
    render(<EntraLink user={agent({})} />);
    await waitFor(() => expect(entraBlueprints).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(screen.queryByText(ENTRA_SECTION)).toBeNull();
  });

  it("still shows a link an agent holds when no provider uses Entra", () => {
    vi.mocked(entraBlueprints).mockResolvedValue([]);
    render(<EntraLink user={agent({ entra_agent_id: "9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a" })} />);
    expect(link()).toBe("9d8c7b6a-5f4e-4d3c-8b2a-1f0e9d8c7b6a");
  });

  it("does not read the providers without the apps area, and shows the section as before", async () => {
    vi.mocked(adminAreas).mockReturnValue({ identity: true });
    render(<EntraLink user={agent({})} />);
    await screen.findByText(NOT_LINKED);
    expect(entraBlueprints).not.toHaveBeenCalled();
  });
});
