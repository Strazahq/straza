import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TokensTab } from "./settings-tokens";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApiTokenRow, listApiTokens, revokeApiToken } from "@/lib/api";
import { notify } from "@/lib/notify";
import {
  NEVER,
  NEVER_TIP,
  SENSITIVE,
  SUBJECT_TOKENS,
  TOKENS_EMPTY_BODY,
  TOKENS_EMPTY_TITLE,
  revokeBody,
  revokeTitle,
  revokedToast,
} from "@/lib/settings-words";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApiTokens: vi.fn(),
  createApiToken: vi.fn(),
  revokeApiToken: vi.fn(),
}));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const DAY = 86400000;
const at = (ms: number) => new Date(Date.now() + ms).toISOString();

const midpoint: ApiTokenRow = {
  id: "tok-mid", name: "midpoint", created_by: "alice",
  scope: "scim:read,scim:write,identity:read,apps:read,changes:read,config:read",
  created: at(-6 * DAY), expires: at(84 * DAY), lastUsed: at(-3 * 60000),
};
const siem: ApiTokenRow = {
  id: "tok-siem", name: "siem-reader", created_by: "alice",
  scope: "audit:read,sessions:read", created: at(-12 * DAY), lastUsed: at(-3600000),
};
const ci: ApiTokenRow = {
  id: "tok-ci", name: "policy-ci", created_by: "break-glass",
  scope: "policy:read,policy:write", created: at(-78 * DAY), expires: at(-2 * DAY),
};
const rows = [midpoint, siem, ci];

const mount = (mintRequest = 0) => render(<TooltipProvider><TokensTab mintRequest={mintRequest} /></TooltipProvider>);
const row = (name: string) => screen.getByRole("button", { name: "Open " + name });
const ask = () => screen.getByRole("alertdialog");

describe("the API tokens tab", () => {
  beforeEach(() => {
    vi.mocked(listApiTokens).mockResolvedValue(rows);
    vi.mocked(revokeApiToken).mockResolvedValue({});
    vi.mocked(notify.ok).mockClear();
  });

  it("lists every token with who minted it and its grants as chips", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(within(row("midpoint")).getByText("by alice")).toBeTruthy();
    expect(within(row("policy-ci")).getByText("by break-glass")).toBeTruthy();
    const chips = [...row("siem-reader").querySelectorAll("[data-grant]")].map((el) => el.textContent);
    expect(chips).toEqual(["audit:read", "sessions:read"]);
    // A jammed comma run of grants never reaches a cell.
    expect(container.textContent).not.toContain("audit:read,sessions:read");
  });

  it("marks a sensitive grant in amber and carries its sentence", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const chip = within(row("midpoint")).getByTitle(SENSITIVE["scim:write"]);
    expect(chip.textContent).toBe("scim:write");
    expect(chip.className).toContain("text-warn");
    expect(within(row("midpoint")).getByText("scim:read").getAttribute("title")).toBe(null);
  });

  it("reads every expiry in the future tense, and names the two that are not dates", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(within(row("midpoint")).getByText("in 84 d")).toBeTruthy();
    const never = within(row("siem-reader")).getByText(NEVER);
    expect(never.getAttribute("title")).toBe(NEVER_TIP);
    expect(never.className).toContain("text-warn");
    const expired = within(row("policy-ci")).getByText("expired");
    expect(expired.className).toContain("text-danger");
    // A token never used says the word, in the muted colour, not a dash.
    expect(within(row("policy-ci")).getByText(NEVER).getAttribute("data-last-used")).toBe(NEVER);
  });

  it("says what would be here when no token is minted", async () => {
    vi.mocked(listApiTokens).mockResolvedValue([]);
    mount();
    expect(await screen.findByText(TOKENS_EMPTY_TITLE)).toBeTruthy();
    expect(screen.getByText(TOKENS_EMPTY_BODY)).toBeTruthy();
  });

  it("says what could not be read when the list never arrives", async () => {
    vi.mocked(listApiTokens).mockRejectedValue(new ApiError("unreachable", 0, true));
    mount();
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain(SUBJECT_TOKENS + ": unreachable, state unknown.");
    expect(block.textContent).toContain("Check that it is running, then reload.");
  });

  it("opens the token's own sheet from a row", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open midpoint" }));
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-token-sheet")).toBe("midpoint");
  });

  it("opens the mint sheet when the head's counter moves, and not before", async () => {
    const view = mount(0);
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(screen.queryByRole("dialog")).toBe(null);
    view.rerender(<TooltipProvider><TokensTab mintRequest={1} /></TooltipProvider>);
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-mint-sheet")).toBe("form");
  });

  const consequences: [ApiTokenRow, string][] = [
    [midpoint, "Whatever signs in as midpoint loses the SCIM plane, its reads of users and roles, its reads of servers, the change feed and the configuration read-out within seconds."],
    [siem, "Whatever signs in as siem-reader loses the ledger and its view of sessions within seconds."],
  ];
  it.each(consequences)("builds the revoke sentence from the row's own grants", async (token, sentence) => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke " + token.name }));
    expect(within(ask()).getByText(revokeTitle(token.name))).toBeTruthy();
    expect(ask().textContent).toContain(sentence);
    expect(revokeBody(token)).toBe(sentence);
  });

  it("revokes through the dialog, says so, and reads the list again", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke midpoint" }));
    vi.mocked(listApiTokens).mockResolvedValue([siem, ci]);
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke token" }));
    await waitFor(() => expect(revokeApiToken).toHaveBeenCalledWith("tok-mid"));
    expect(notify.ok).toHaveBeenCalledWith(revokedToast("midpoint"));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBe(null));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Open midpoint" })).toBe(null));
  });

  it("keeps the dialog open with the server's own sentence when the revoke is refused", async () => {
    vi.mocked(revokeApiToken).mockRejectedValue(new ApiError("the token is already revoked", 409));
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke midpoint" }));
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke token" }));
    const alert = await within(ask()).findByRole("alert");
    expect(alert.textContent).toContain("Revoke token refused.");
    expect(alert.textContent).toContain("the token is already revoked");
    expect(alert.textContent).toContain("Fix what it names, then try again.");
    expect(alert.textContent).not.toContain("retrying");
    expect(alert.textContent).not.toContain("state unknown");
    expect(screen.getByRole("button", { name: "Open midpoint", hidden: true })).toBeTruthy();
  });

  it("keeps the last table on screen when a later read fails", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Revoke midpoint" }));
    vi.mocked(listApiTokens).mockRejectedValue(new ApiError("the database is unreachable", 500));
    await userEvent.click(within(ask()).getByRole("button", { name: "Revoke token" }));
    const block = await screen.findByRole("status");
    expect(block.textContent).toContain("The API tokens could not be read: the database is unreachable.");
    expect(screen.getByRole("button", { name: "Open midpoint" })).toBeTruthy();
  });
});
