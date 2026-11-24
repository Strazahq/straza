import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { UserDetails } from "./user-details";
import type { UserDetail } from "@/lib/api";
import { FROM_ENTRA, IN_ENTRA_AS, NO_SPONSOR_CHIP, OBJECT_HINT } from "@/lib/entra-words";
import { setTimeZone } from "@/lib/timezone";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const counts = { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 };
const altair: UserDetail = {
  id: "u-altair", username: "altair", status: "active", origin: "local", kind: "nhi", user_type: "agent", agency_mode: "autonomous", sponsor: "alice",
  created_at: "2026-11-02T10:00:00Z", updated_at: "", effective_roles: [], locks: [], last_seen: "2026-11-14T09:56:00Z", sponsored_count: 0, counts,
  swarm_id: "swarm-7", ephemeral: true,
};
const alice: UserDetail = { ...altair, id: "u-alice", username: "alice", kind: "human", user_type: undefined, origin: "scim", email: "alice@corp.example", external_id: "3f1a", title: "Platform lead", agency_mode: undefined, sponsor: undefined, swarm_id: undefined, ephemeral: false, last_seen: undefined };

const facts = () => [...document.querySelectorAll("[data-fact]")].map((f) => f.getAttribute("data-fact") + ": " + f.querySelector("dd")?.textContent);

describe("the Details tab", () => {
  beforeEach(() => {
    setTimeZone("utc");
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-14T10:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("reads an agent's fields, with the sponsor as the head shows it", () => {
    render(<UserDetails user={altair} sponsorID="u-alice" />);
    expect(facts()).toEqual([
      "Sign-in name: altair",
      "Email: none",
      "Source: Straza",
      "Created: 2 Nov 2026",
      "Last seen: 4 m ago",
      "Agency: Autonomous",
      "Sponsor: alice",
      "Swarm: swarm-7",
      "Ephemeral: yes",
    ]);
    expect(screen.getByRole("link", { name: "alice" })).toBeTruthy();
  });

  it("reads a person's fields and leaves out what does not apply", () => {
    render(<UserDetails user={alice} sponsorID={null} />);
    expect(facts()).toEqual([
      "Sign-in name: alice",
      "Email: alice@corp.example",
      "External id: 3f1a",
      "Title: Platform lead",
      "Source: Identity manager",
      "Created: 2 Nov 2026",
      "Last seen: not yet",
    ]);
  });

  it("says an agent's agency and sponsor are not set", () => {
    render(<UserDetails user={{ ...altair, agency_mode: undefined, sponsor: undefined }} sponsorID={null} />);
    expect(facts()).toContain("Agency: not set");
    expect(facts()).toContain("Sponsor: none");
  });

  it.each<[string, string, string | null]>([
    ["Entra", "entra", "from-entra"],
    ["an origin the console does not know", "okta", null],
  ])("names the source %s with its icon, and an unknown one as sent", (_name, origin, icon) => {
    render(<UserDetails user={{ ...alice, origin }} sponsorID={null} />);
    expect(facts()).toContain("Source: " + (origin === "entra" ? "Entra" : origin));
    expect(document.querySelector("[data-fact=Source] [data-kind-icon]")?.getAttribute("data-kind-icon") ?? null).toBe(icon);
  });
});

describe("the Details tab of an agent from Entra", () => {
  const AGENT_ID = "d3d3d3d3-0000-4000-8000-000000000003";
  const born: UserDetail = {
    ...altair, id: "u-born", username: "lab-entra-born-agent", origin: "entra", agency_mode: undefined, sponsor: "ben", swarm_id: undefined, ephemeral: false,
    entra_agent_id: AGENT_ID, entra: { identity: "enabled", agent_user: "enabled" },
  };
  beforeEach(() => {
    setTimeZone("utc");
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-14T10:00:00Z"));
  });
  afterEach(() => vi.useRealTimers());

  it("names the objects Entra has for it and the one sponsor Entra set", () => {
    render(<UserDetails user={born} sponsorID="u-ben" />);
    expect(facts()).toEqual([
      "Sign-in name: lab-entra-born-agent",
      "Email: none",
      "Source: Entra",
      "In Entra as: agent identityagent user",
      "Created: 2 Nov 2026",
      "Last seen: 4 m ago",
      "Agency: not set",
      "Sponsor: benFrom Entra",
    ]);
    expect(screen.getByRole("link", { name: "ben" })).toBeTruthy();
    expect(document.querySelector("[data-sponsor-from-entra]")?.textContent).toBe(FROM_ENTRA);
  });

  it("opens the agent identity in Entra from its chip, and says on hover what each object is", () => {
    render(<UserDetails user={born} sponsorID={null} />);
    const identity = document.querySelector("[data-entra-object-link=agentIdentity]") as HTMLAnchorElement;
    expect(identity.getAttribute("href")).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/" + AGENT_ID);
    expect(identity.getAttribute("target")).toBe("_blank");
    expect(document.querySelector("[data-entra-object=agentIdentity]")?.getAttribute("title")).toBe(OBJECT_HINT.agentIdentity);
    expect(document.querySelector("[data-entra-object=agentUser]")?.getAttribute("title")).toBe(OBJECT_HINT.agentUser);
    // The answer carries no object ID of the agent user, so its chip opens nothing.
    expect(document.querySelector("[data-entra-object-link=agentUser]")).toBeNull();
  });

  it.each<[string, Partial<UserDetail>, string]>([
    ["no sponsor", { sponsor: undefined, entra: { identity: "enabled", sponsor_state: "none" } }, "Sponsor: " + NO_SPONSOR_CHIP],
    ["no sponsor and no state from Entra yet", { sponsor: undefined, entra: undefined }, "Sponsor: " + NO_SPONSOR_CHIP],
    ["a sponsor set before Entra's state was stored", { entra: undefined }, "Sponsor: ben"],
  ])("reads the Sponsor row of an agent with %s", (_name, extra, want) => {
    render(<UserDetails user={{ ...born, ...extra }} sponsorID={null} />);
    expect(facts()).toContain(want);
  });

  it("leaves In Entra as out while Entra lists neither object", () => {
    render(<UserDetails user={{ ...born, entra: {} }} sponsorID={null} />);
    expect(facts().some((f) => f.startsWith(IN_ENTRA_AS))).toBe(false);
  });
});
