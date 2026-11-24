import type * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EntraGroup, look } from "./entra-group";
import { ApiError, type AssignmentRow, type EntraStatus, type RoleEntraGroup, type RoleRow, type UserRow, listUsers, recreateEntraGroup, releaseEntraRemovals } from "@/lib/api";
import {
  APPLY_REMOVALS, ENTRA_GROUP_URL, KEEP_LABEL, KEPT, RECREATE, RECREATE_TITLE, appliedToast, applyTitle, deletedLine, loseLabel, membersPill, notAppliedPill, openGroupTitle, ownerLine,
  recreateBody, recreatedToast, readPill, lastGoodPill,
} from "@/lib/entra-words";
import { notify } from "@/lib/notify";
import { DELETE_ROLE } from "@/lib/role-words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), releaseEntraRemovals: vi.fn(), recreateEntraGroup: vi.fn(), listUsers: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const GID = "7d1e4b2a-93c6-4f05-a8e2-5b0c9d317f64";
const READ = new Date(Date.now() - 40_000).toISOString();
const role: RoleRow = { id: "r-fin", name: "finance-readers", kind: "application" };
const linked: RoleEntraGroup = {
  group_id: GID, state: "linked", read_at: READ,
  not_applied: [{ id: "m1", kind: "user", reason: "no-user" }, { id: "m2", kind: "agentUser", reason: "agent" }],
};
const config = { group_name_prefix: "Straza: ", client_id: "5b7d9f1a-3c5e-4a7b-8d9f-1a2b3c4d5e6f" };
const ok: EntraStatus = { enabled: true, held: 0, last_good: READ };
const asg = (id: string, user: string, roleID: string, origin: string): AssignmentRow => ({ id, subject_kind: "user", subject_id: user, role_id: roleID, origin });
// ada holds the role from Entra and by hand, ben from Entra alone.
const assignments = [asg("a1", "u-ada", "r-fin", "entra"), asg("a2", "u-ada", "r-fin", "admin"), asg("a3", "u-ben", "r-fin", "entra"), asg("a4", "u-cleo", "r-fin", "scim")];
const user = (id: string, username: string): UserRow => ({ id, username, status: "active", origin: "local", kind: "human", created_at: "", updated_at: "", effective_roles: [], locks: [], sponsored_count: 0 });

const props = (extra: Partial<React.ComponentProps<typeof EntraGroup>> = {}) => ({
  role, group: linked, roles: [role], status: ok, config, assignments, product: false, canDelete: true,
  onDelete: vi.fn(), onNotApplied: vi.fn(), onChanged: vi.fn(), ...extra,
});
const draw = (extra: Partial<React.ComponentProps<typeof EntraGroup>> = {}) => {
  const p = props(extra);
  render(<EntraGroup {...p} />);
  return p;
};
const chip = () => document.querySelector("[data-entra-state]") as HTMLElement;
const said = () => (document.querySelector("[data-entra-said]") as HTMLElement | null)?.textContent || "";

describe("the group block", () => {
  beforeEach(() => {
    vi.mocked(notify.ok).mockClear();
    vi.mocked(releaseEntraRemovals).mockReset();
    vi.mocked(recreateEntraGroup).mockReset();
    vi.mocked(listUsers).mockResolvedValue({ items: [user("u-ada", "ada"), { ...user("u-ben", "ben"), kind: "nhi", user_type: "agent" }, user("u-cleo", "cleo")], next_cursor: "" });
  });

  // Each state the server sends, and the two the page reads off other
  // facts, with the chip's word and tone and whether the block says more.
  const states: [string, RoleEntraGroup, EntraStatus | null, string, string, boolean][] = [
    ["being created", { group_id: "", state: "creating" }, ok, "creating", "unknown", false],
    ["linked", linked, ok, "linked", "ok", false],
    ["stale while Entra cannot be read", linked, { enabled: true, held: 0, stale_since: READ, error: "Entra refused the Straza service app (AADSTS700027)." }, "stale", "warn", true],
    ["deleted in Entra", { ...linked, state: "missing", error: "The group of this role was deleted in Entra." }, ok, "group deleted", "danger", true],
    ["holding removals", { ...linked, state: "held", error: "Entra's group lists 14 of this role's 20 holders from Entra no longer." }, ok, "removals held", "warn", true],
    ["no longer owned by Straza", { ...linked, error: "Straza is no longer an owner of this group in Entra." }, ok, "not owner", "warn", true],
    ["released, a state the table does not word", { ...linked, state: "released" }, ok, "released", "plain", false],
    ["in a state this console does not know", { ...linked, state: "frozen", error: "Entra froze the group." }, ok, "frozen", "plain", true],
  ];
  for (const [what, group, status, word, tone, more] of states) {
    it("draws a group " + what + " as the chip " + word, () => {
      draw({ group, status });
      expect(chip().textContent).toBe(word);
      expect(chip().getAttribute("data-tone")).toBe(tone);
      expect(!!document.querySelector("[data-entra-said]")).toBe(more);
    });
  }

  it("names the group with the config's prefix, links it to Entra and counts its members once", async () => {
    const p = draw();
    const link = document.querySelector("[data-entra-open]") as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe(ENTRA_GROUP_URL(GID));
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("title")).toBe(openGroupTitle("Straza: finance-readers"));
    expect(link.textContent).toBe("Straza: finance-readers");
    expect(screen.getByText(readPill(READ))).toBeTruthy();
    // Two applied from Entra, ada and ben, and two not applied.
    expect(screen.getByText(membersPill(4))).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: notAppliedPill(2) }));
    expect(p.onNotApplied).toHaveBeenCalled();
  });

  it("names the group by its ID without the config, and leaves the member count out without the assignments", () => {
    draw({ config: null, assignments: null });
    expect((document.querySelector("[data-entra-open]") as HTMLElement).textContent).toBe(GID);
    expect(document.querySelector("[data-entra-members]")).toBeNull();
    expect(screen.getByText(notAppliedPill(2))).toBeTruthy();
  });

  it("shows no link, no time and no count while the group is being created", () => {
    draw({ group: { group_id: "", state: "creating" } });
    expect(document.querySelector("[data-entra-open]")).toBeNull();
    expect(document.querySelector("[data-entra-read]")).toBeNull();
    expect(document.querySelector("[data-entra-members]")).toBeNull();
    expect(screen.getByText("Straza: finance-readers")).toBeTruthy();
  });

  it("says why reads fail and what holders keep while Entra cannot be read", () => {
    const error = "Entra refused the Straza service app (AADSTS700027). An administrator uploads the certificate.";
    draw({ status: { enabled: true, held: 0, stale_since: READ, error } });
    expect(said()).toBe(error + KEPT);
    expect(screen.getByText(lastGoodPill(READ))).toBeTruthy();
  });

  it("says what is wrong when Straza is no longer an owner, in the page's words with the config and in the server's without", () => {
    const group = { ...linked, error: "Straza is no longer an owner of this group in Entra." };
    draw({ group });
    expect(said()).toBe(ownerLine("Straza: finance-readers", config.client_id));
  });

  it("falls back to the server's sentence for a group no longer owned when the config is not read", () => {
    const group = { ...linked, error: "Straza is no longer an owner of this group in Entra." };
    draw({ group, config: null });
    expect(said()).toBe(group.error);
  });

  it("offers Create the group again and Delete role on a deleted group", async () => {
    const p = draw({ group: { ...linked, state: "missing" } });
    expect(said()).toContain(deletedLine("Straza: finance-readers", false));
    expect(document.querySelector("[data-entra-open]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: DELETE_ROLE }));
    expect(p.onDelete).toHaveBeenCalled();
  });

  it("offers a product role only Create the group again", () => {
    draw({ group: { ...linked, state: "missing" }, product: true, canDelete: false });
    expect(said()).toContain(deletedLine("Straza: finance-readers", true));
    expect(screen.queryByRole("button", { name: DELETE_ROLE })).toBeNull();
    expect(screen.getByRole("button", { name: RECREATE })).toBeTruthy();
  });

  it("applies held removals behind a confirm that quotes the server, then says when they land", async () => {
    const error = "Entra's group lists 14 of this role's 20 holders from Entra no longer.";
    vi.mocked(releaseEntraRemovals).mockResolvedValue({ status: "released" });
    const p = draw({ group: { ...linked, state: "held", error } });
    expect(document.querySelector("[data-entra-members]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: APPLY_REMOVALS }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe(applyTitle("finance-readers"));
    expect(within(dialog).getByText(error)).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: APPLY_REMOVALS }));
    await waitFor(() => expect(releaseEntraRemovals).toHaveBeenCalledWith("r-fin"));
    expect(notify.ok).toHaveBeenCalledWith(appliedToast("finance-readers"));
    expect(p.onChanged).toHaveBeenCalled();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
  });

  it("keeps the apply confirm open on a 409 and shows the server's sentence", async () => {
    const sentence = "nothing changed, because no removals of role finance-readers are on hold. Read the role again to see its group's state";
    vi.mocked(releaseEntraRemovals).mockRejectedValue(new ApiError(sentence, 409));
    const p = draw({ group: { ...linked, state: "held", error: "held" } });
    await userEvent.click(screen.getByRole("button", { name: APPLY_REMOVALS }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: APPLY_REMOVALS }));
    const refusal = await within(dialog).findByRole("alert");
    expect(refusal.textContent).toContain(sentence);
    expect(p.onChanged).not.toHaveBeenCalled();
    expect(notify.ok).not.toHaveBeenCalled();
  });

  it("creates a deleted group again behind a confirm that names who loses the role and who keeps it", async () => {
    vi.mocked(recreateEntraGroup).mockResolvedValue({ status: "creating" });
    const p = draw({ group: { ...linked, state: "missing" } });
    await userEvent.click(screen.getByRole("button", { name: RECREATE }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe(RECREATE_TITLE);
    expect(within(dialog).getByText(recreateBody("Straza: finance-readers"))).toBeTruthy();
    expect(within(dialog).getByText(loseLabel("finance-readers"))).toBeTruthy();
    expect(within(dialog).getByText(KEEP_LABEL)).toBeTruthy();
    await waitFor(() => expect((dialog.querySelector("[data-entra-lose]") as HTMLElement).textContent).toBe(loseLabel("finance-readers") + "ben"));
    expect((dialog.querySelector("[data-entra-keep]") as HTMLElement).textContent).toBe(KEEP_LABEL + "ada");
    // An agent user that loses the role carries the agent's icon.
    expect((dialog.querySelector("[data-entra-holder=ben] svg") as SVGElement).getAttribute("class")).toContain("lucide-bot");
    await userEvent.click(within(dialog).getByRole("button", { name: RECREATE }));
    await waitFor(() => expect(recreateEntraGroup).toHaveBeenCalledWith("r-fin"));
    expect(notify.ok).toHaveBeenCalledWith(recreatedToast("Straza: finance-readers"));
    expect(p.onChanged).toHaveBeenCalled();
  });

  it("keeps the recreate confirm open on a 409 and shows the server's sentence", async () => {
    const sentence = "nothing changed, because the group of role finance-readers is not marked as deleted in Entra. Read the role again to see its group's state";
    vi.mocked(recreateEntraGroup).mockRejectedValue(new ApiError(sentence, 409));
    draw({ group: { ...linked, state: "missing" } });
    await userEvent.click(screen.getByRole("button", { name: RECREATE }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: RECREATE }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain(sentence);
  });
});

describe("look", () => {
  it("reads stale off the state of the read for a group Straza reads, and not owner off a linked group's sentence", () => {
    const stale: EntraStatus = { enabled: true, held: 0, stale_since: READ };
    expect(look(linked, stale)).toEqual({ chip: "stale", stale: true, notOwner: false });
    expect(look({ ...linked, state: "held" }, stale).chip).toBe("stale");
    expect(look({ ...linked, state: "missing" }, stale).chip).toBe("missing");
    expect(look({ ...linked, state: "creating" }, stale).chip).toBe("creating");
    expect(look({ ...linked, error: "x" }, ok)).toEqual({ chip: "not-owner", stale: false, notOwner: true });
    expect(look(linked, null).chip).toBe("linked");
  });
});
