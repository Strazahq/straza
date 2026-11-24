import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sentence, UserHead } from "./user-head";
import type { UserDetail } from "@/lib/api";
import { opensAccount } from "@/lib/entra-words";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

const minutesAgo = (n: number) => new Date(Date.now() - n * 60000).toISOString();
const counts = { sessions: 0, active_sessions: 0, devices: 0, approver_devices: 0, approvals: 0 };
const base: UserDetail = {
  id: "u1", username: "altair", status: "active", origin: "local", kind: "nhi", user_type: "agent", agency_mode: "autonomous", sponsor: "alice",
  created_at: "2026-11-02T10:00:00Z", updated_at: "2026-11-02T10:00:00Z", effective_roles: [], locks: [], last_seen: minutesAgo(4), sponsored_count: 0, counts,
};
const user = (extra: Partial<UserDetail>): UserDetail => ({ ...base, ...extra });
const AGENT_ID = "d3d3d3d3-0000-4000-8000-000000000003";
const AGENT_URL = "https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/" + AGENT_ID;

describe("the sentence under a user's title", () => {
  it.each<[string, UserDetail, string]>([
    ["an AI agent with a sponsor", base, "Autonomous agent, made in Straza. Sponsored by alice. Last seen 4 minutes ago."],
    ["an AI agent with no sponsor, never seen", user({ sponsor: undefined, last_seen: undefined, agency_mode: undefined, origin: "scim" }), "From your identity manager. No sponsor. Not seen yet."],
    ["a person who sponsors agents", user({ kind: "human", user_type: undefined, origin: "scim", sponsor: undefined, sponsored_count: 2, last_seen: minutesAgo(1) }), "From your identity manager. Sponsors 2 agents. Last seen 1 minute ago."],
    ["a person who sponsors nobody", user({ kind: "human", user_type: undefined, sponsor: undefined }), "Made in Straza. Last seen 4 minutes ago."],
    ["a service with a sponsor", user({ user_type: "service" }), "Made in Straza. Sponsored by alice. Last seen 4 minutes ago."],
    ["a service with no sponsor", user({ user_type: "service", sponsor: undefined }), "Made in Straza. Last seen 4 minutes ago."],
    ["a person from Entra", user({ kind: "human", user_type: undefined, sponsor: undefined, origin: "entra", external_id: "1b4e7c90" }), "Entra Open in Entra Last seen 4 minutes ago."],
    ["a person from Entra with no object ID", user({ kind: "human", user_type: undefined, sponsor: undefined, origin: "entra" }), "Entra Last seen 4 minutes ago."],
    ["an AI agent of origin entra", user({ origin: "entra" }), "Entra Autonomous agent. Sponsored by alice. Last seen 4 minutes ago."],
    ["an AI agent from Entra with no agency and no sponsor", user({ origin: "entra", entra_agent_id: AGENT_ID, agency_mode: undefined, sponsor: undefined }), "Entra Open in Entra AI agent. No sponsor. Last seen 4 minutes ago."],
    ["an AI agent from Entra with no agency and a sponsor", user({ origin: "entra", entra_agent_id: AGENT_ID, agency_mode: undefined }), "Entra Open in Entra AI agent. Sponsored by alice. Last seen 4 minutes ago."],
  ])("reads %s", (_name, u, want) => {
    render(<Sentence user={u} sponsorID={null} />);
    expect((document.querySelector("[data-user-sentence]") as HTMLElement).textContent).toBe(want);
  });

  it.each<[string, UserDetail, string[]]>([
    ["an AI agent with a sponsor", base, ["Autonomous agent, made in Straza.", "Sponsored by alice.", "Last seen 4 minutes ago."]],
    ["an AI agent from Entra with no sponsor", user({ origin: "entra", entra_agent_id: AGENT_ID, agency_mode: undefined, sponsor: undefined }), ["Entra", "Open in Entra", "AI agent.", "No sponsor.", "Last seen 4 minutes ago."]],
  ])("keeps each sentence of %s whole as its own unit, so the line breaks between them", (_name, u, want) => {
    render(<Sentence user={u} sponsorID="u-alice" />);
    const units = [...(document.querySelector("[data-user-sentence]") as HTMLElement).children];
    expect(units.map((e) => e.textContent)).toEqual(want);
    expect(units.every((e) => e.classList.contains("inline-block"))).toBe(true);
  });

  it("names a person from Entra with the tag Entra and links to the account there", () => {
    render(<Sentence user={user({ kind: "human", user_type: undefined, username: "ada", origin: "entra", external_id: "1b4e7c90" })} sponsorID={null} />);
    const sentence = document.querySelector("[data-user-sentence]") as HTMLElement;
    expect(sentence.querySelector("[data-entra-tag]")?.textContent).toBe("Entra");
    const link = screen.getByRole("link", { name: /Open in Entra/ });
    expect(link.getAttribute("href")).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/1b4e7c90");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("title")).toBe(opensAccount("ada"));
  });

  it("links an agent from Entra to its agent identity there", () => {
    render(<Sentence user={user({ username: "lab-entra-born-agent", origin: "entra", entra_agent_id: AGENT_ID })} sponsorID={null} />);
    const link = screen.getByRole("link", { name: /Open in Entra/ });
    expect(link.getAttribute("href")).toBe(AGENT_URL);
    expect(link.getAttribute("title")).toBe(opensAccount("lab-entra-born-agent"));
  });

  it("links the sponsor once its id is known, behind a small person tile", () => {
    render(<Sentence user={base} sponsorID="u-alice" />);
    expect(screen.getByRole("link", { name: "alice" }).getAttribute("href")).toContain("users/u-alice");
    expect(document.querySelector("[data-sponsor=alice] [data-kind-icon=person]")).not.toBeNull();
    const unit = document.querySelector("[data-sponsor=alice]")?.parentElement as HTMLElement;
    expect([unit.textContent, unit.classList.contains("inline-block")]).toEqual(["alice.", true]);
  });
});

describe("the head of a user's page", () => {
  const draw = (u: UserDetail, canTest = true) => {
    const onTest = vi.fn();
    render(<UserHead user={u} sponsorID={null} canTest={canTest} sessions entraOwned={u.origin === "entra"} onTest={onTest} onStop={vi.fn()} />);
    return { onTest };
  };

  it("titles an agent by its display name, with the sign-in name, the teal kind chip and the state", async () => {
    const { onTest } = draw(user({ display: "Altair (coding agent)" }));
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Altair (coding agent)");
    expect(document.querySelector("[data-sign-in]")?.textContent).toBe("altair");
    expect(document.querySelector("[data-kind-chip=agent]")?.getAttribute("data-tone")).toBe("teal");
    expect(document.querySelector("[data-kind-chip=agent]")?.textContent).toBe("AI agent");
    expect(document.querySelector("[data-user-status=active]")).not.toBeNull();
    expect(screen.getByRole("navigation", { name: "Breadcrumb" }).textContent).toBe("Straza/Users/altair");
    await userEvent.click(screen.getByRole("button", { name: "Test a call" }));
    expect(onTest).toHaveBeenCalled();
  });

  it("titles a user with no display name by the sign-in name alone", () => {
    draw(user({ display: undefined }));
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("altair");
    expect(document.querySelector("[data-sign-in]")).toBeNull();
  });

  it.each<[string, Partial<UserDetail>, string, string[]]>([
    ["a person", { kind: "human", user_type: undefined }, "Person", ["active"]],
    ["a disabled service", { user_type: "service", status: "disabled" }, "Service", ["disabled"]],
    ["a locked and disabled person", { kind: "human", user_type: undefined, status: "disabled", locks: [{ origin: "admin", reason: "incident", created_at: minutesAgo(9) }] }, "Person", ["disabled", "locked"]],
  ])("reads the kind and state of %s as plain chips", (_name, extra, word, states) => {
    draw(user(extra), false);
    expect(document.querySelector("[data-kind-chip]")?.textContent).toBe(word);
    expect(document.querySelector("[data-kind-chip]")?.getAttribute("data-tone")).toBe("plain");
    expect([...document.querySelectorAll("[data-user-status]")].map((e) => e.getAttribute("data-user-status"))).toEqual(states);
    expect(screen.queryByRole("button", { name: "Test a call" })).toBeNull();
  });

  it.each<[string, Partial<UserDetail>, boolean]>([
    ["an active agent", {}, true],
    ["a disabled agent", { status: "disabled" }, false],
    ["a locked agent", { locks: [{ origin: "admin", reason: "incident", created_at: minutesAgo(9) }] }, false],
  ])("offers Test a call to a reader with the policy area on %s only while its calls reach policy", (_name, extra, shown) => {
    draw(user(extra));
    expect(screen.queryByRole("button", { name: "Test a call" }) !== null).toBe(shown);
  });

  it("offers Disable in Entra in place of Disable for a person Entra owns", async () => {
    draw(user({ kind: "human", user_type: undefined, username: "ada", origin: "entra", external_id: "1b4e7c90" }));
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await screen.findByRole("menu");
    expect([...document.querySelectorAll("[data-stop]")].map((e) => e.getAttribute("data-stop"))).toEqual(["sessions", "lock", "entra"]);
  });

  it("offers an agent Entra owns Revoke all sessions and Lock, and Disable in Entra on its agent identity", async () => {
    draw(user({ username: "lab-entra-born-agent", origin: "entra", entra_agent_id: AGENT_ID, counts: { ...counts, active_sessions: 1 } }));
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    const menu = await screen.findByRole("menu");
    expect([...menu.querySelectorAll("[data-stop]")].map((e) => e.textContent)).toEqual([
      "Revoke all sessionsEvery active session ends now. lab-entra-born-agent can start a new one.",
      "LockSessions end and no new one starts until you unlock.",
      "Disable in Entra",
    ]);
    const link = menu.querySelector("[data-stop=entra]") as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe(AGENT_URL);
    expect(link.getAttribute("title")).toBe(opensAccount("lab-entra-born-agent"));
  });
});
