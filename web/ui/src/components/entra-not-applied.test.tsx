import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EntraNotApplied } from "./entra-not-applied";
import type { RoleEntraGroup } from "@/lib/api";
import { COPY_ID, ENTRA_GROUP_URL, EVERY_MEMBER_APPLIED, NOT_APPLIED_TITLE, OPEN_IN_ENTRA, REMOVE_IN_ENTRA, opensAccount, opensGroup } from "@/lib/entra-words";

const group: RoleEntraGroup = {
  group_id: "g1", state: "linked",
  not_applied: [
    { id: "6c1f0d52", kind: "user", reason: "name-taken" },
    { id: "2e7c4a19", kind: "servicePrincipal", reason: "not-agent" },
    { id: "7a1f9e3c", kind: "agentIdentity", reason: "name-taken" },
    { id: "9b2e7f40", kind: "user", reason: "not-read" },
    { id: "d25c81e6", kind: "other", reason: "unread" },
    { id: "a41b7e09", kind: "device", reason: "disabled" },
  ],
};

const draw = (g: RoleEntraGroup, open = true) => {
  const onOpenChange = vi.fn();
  render(<EntraNotApplied group={g} name="Straza: lab-echo-callers" open={open} onOpenChange={onOpenChange} reveal={false} onRevealed={() => {}} />);
  return onOpenChange;
};
const reason = (r: string) => document.querySelector("[data-entra-reason='" + r + "']") as HTMLElement;
const kind = (id: string) => (document.querySelector("[data-entra-member='" + id + "'] [data-entra-kind]") as HTMLElement).textContent;

describe("the members not applied", () => {
  it("groups the members by reason, each reason with its chip and what to do", () => {
    draw(group);
    const head = document.querySelector("summary") as HTMLElement;
    expect(head.textContent).toBe(NOT_APPLIED_TITLE + "6");
    expect(Array.from(document.querySelectorAll("[data-entra-reason]")).map((e) => e.getAttribute("data-entra-reason"))).toEqual(["name-taken", "not-agent", "not-read", "unread", "disabled"]);
    expect(reason("name-taken").querySelector("[data-tone]")?.textContent).toBe("name taken");
    expect(reason("name-taken").querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("warn");
    expect(reason("not-agent").querySelector("[data-tone]")?.textContent).toBe("not an agent");
    expect(reason("not-agent").querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("plain");
    expect(reason("not-read").textContent).toContain("kind not read" + "identity.entra.read leaves out people. Add people there, or remove the member in Entra.");
    expect(reason("unread").querySelector("[data-tone]")?.getAttribute("data-tone")).toBe("warn");
  });

  it("shows a reason and a type the console does not know as the server sent them, with no fix line", () => {
    draw(group);
    expect(reason("disabled").textContent).toBe("disableddevicea41b7e09");
    expect(kind("a41b7e09")).toBe("device");
    expect(kind("d25c81e6")).toBe("other");
  });

  it("words each type in Entra's words", () => {
    draw(group);
    expect(kind("6c1f0d52")).toBe("user");
    expect(kind("2e7c4a19")).toBe("application");
    expect(kind("7a1f9e3c")).toBe("agent identity");
  });

  it("is closed until opened, and says so to the page", async () => {
    const onOpenChange = draw(group, false);
    const fold = document.querySelector("details") as HTMLDetailsElement;
    expect(fold.open).toBe(false);
    await userEvent.click(screen.getByText(NOT_APPLIED_TITLE));
    expect(onOpenChange).toHaveBeenCalledWith(true);
  });

  it("offers each member's Entra object ID to copy", () => {
    draw(group);
    expect(screen.getAllByRole("button", { name: COPY_ID })).toHaveLength(6);
  });

  it("says every member was applied when none was left out, and nothing while the group is being created", () => {
    draw({ ...group, not_applied: [] });
    expect(screen.getByText(EVERY_MEMBER_APPLIED)).toBeTruthy();
    document.body.innerHTML = "";
    draw({ group_id: "", state: "creating" });
    expect(document.body.textContent).toBe("");
  });

  it("links each member whose sign-in name is taken to its account in Entra, a person or an agent, and no other member", () => {
    draw(group);
    const links = screen.getAllByRole("link", { name: new RegExp(OPEN_IN_ENTRA) });
    expect(links.map((l) => l.closest("[data-entra-member]")?.getAttribute("data-entra-member"))).toEqual(["6c1f0d52", "7a1f9e3c"]);
    expect(links[0].getAttribute("href")).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/6c1f0d52");
    expect(links[1].getAttribute("href")).toBe("https://entra.microsoft.com/#view/Microsoft_AAD_IAM/ManagedAppMenuBlade/~/Overview/objectId/7a1f9e3c");
    expect(links[0].getAttribute("target")).toBe("_blank");
    expect(links[0].getAttribute("title")).toBe(opensAccount("6c1f0d52"));
    expect(reason("name-taken").textContent).toBe("name taken" + "user" + "6c1f0d52" + OPEN_IN_ENTRA + "agent identity" + "7a1f9e3c" + OPEN_IN_ENTRA);
  });

  it("sends an application, which is not an agent, to the group in Entra to remove it, with no fix line", () => {
    draw(group);
    expect(reason("not-agent").textContent).toBe("not an agent" + "application" + "2e7c4a19" + REMOVE_IN_ENTRA);
    const link = screen.getByRole("link", { name: new RegExp(REMOVE_IN_ENTRA) });
    expect(link.closest("[data-entra-member]")?.getAttribute("data-entra-member")).toBe("2e7c4a19");
    expect(link.getAttribute("href")).toBe(ENTRA_GROUP_URL("g1"));
    expect(link.getAttribute("title")).toBe(opensGroup("Straza: lab-echo-callers"));
  });
});
