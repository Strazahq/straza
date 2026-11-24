import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TokensTab } from "./settings-tokens";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, type ApiTokenRow, listApiTokens, revokeApiToken } from "@/lib/api";
import { notify } from "@/lib/notify";
import {
  MINT_TITLE,
  NEVER,
  NEVER_TIP,
  NEW_TOKEN,
  REVOKE,
  SENSITIVE,
  SUBJECT_TOKENS,
  TOKENS_EMPTY_BODY,
  TOKENS_EMPTY_TITLE,
  TOKENS_LEDE,
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

const mount = () => render(<TooltipProvider><TokensTab /></TooltipProvider>);
const row = (name: string) => screen.getByRole("button", { name: "Open " + name });
const ask = () => screen.getByRole("alertdialog");

describe("the API tokens tab", () => {
  beforeEach(() => {
    vi.mocked(listApiTokens).mockResolvedValue(rows);
    vi.mocked(revokeApiToken).mockResolvedValue({});
    vi.mocked(notify.ok).mockClear();
  });

  it("lists every token with its tile, who minted it and its grants as chips, and no count line", async () => {
    const { container } = mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(within(row("midpoint")).getByText("by alice")).toBeTruthy();
    expect(row("midpoint").querySelector('[data-kind-icon="api-token"]')).toBeTruthy();
    expect(within(row("policy-ci")).getByText("by break-glass")).toBeTruthy();
    const chips = [...row("siem-reader").querySelectorAll("[data-grant]")].map((el) => el.textContent);
    expect(chips).toEqual(["audit:read", "sessions:read"]);
    // A jammed comma run of grants never reaches a cell.
    expect(container.textContent).not.toContain("audit:read,sessions:read");
    // The tab's label carries the count, so the table prints none.
    expect(document.querySelector("[data-row-count]")).toBeNull();
    expect(screen.getByText(TOKENS_LEDE)).toBeTruthy();
  });

  it("marks a sensitive grant in amber and carries its sentence", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    const chip = within(row("midpoint")).getByTitle(SENSITIVE["scim:write"]);
    expect(chip.textContent).toBe("scim:write");
    expect(chip.className).toContain("text-warn");
    expect(within(row("midpoint")).getByText("scim:read").getAttribute("title")).toBe(null);
  });

  describe("with the clock at 14 Nov 2026, 10:00 UTC", () => {
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date("2026-11-14T10:00:00Z"));
    });
    afterEach(() => vi.useRealTimers());

    const dated: ApiTokenRow[] = [
      { id: "d1", name: "midpoint-connector", scope: "scim:write", created: "2026-11-08T09:00:00Z", expires: "2027-02-05T12:00:00Z", lastUsed: "2026-11-14T09:58:00Z" },
      { id: "d2", name: "ci-drafts", scope: "drafts:write", created: "2026-11-02T09:00:00Z", expires: "2026-11-19T08:00:00Z", lastUsed: "2026-11-13T08:00:00Z" },
      { id: "d3", name: "siem-export", scope: "audit:read", created: "2026-10-14T09:00:00Z", lastUsed: "2026-11-14T09:00:00Z" },
      { id: "d4", name: "old-import", scope: "identity:write", created: "2026-09-01T09:00:00Z", expires: "2026-11-11T10:00:00Z", lastUsed: "2026-10-12T10:00:00Z" },
    ];
    const cell = (name: string, text: string) => within(row(name)).getByText(text);

    it("reads a created day, a last use and the four forms of an expiry", async () => {
      vi.mocked(listApiTokens).mockResolvedValue(dated);
      mount();
      await screen.findByRole("button", { name: "Open midpoint-connector" });
      expect(cell("midpoint-connector", "8 Nov 2026")).toBeTruthy();
      expect(cell("midpoint-connector", "2 m ago")).toBeTruthy();
      expect(cell("ci-drafts", "yesterday")).toBeTruthy();
      expect(cell("siem-export", "1 h ago")).toBeTruthy();
      expect(cell("old-import", "12 Oct 2026")).toBeTruthy();

      // Far off is plain text, near is a warn chip, past is a danger chip.
      expect(cell("midpoint-connector", "in 83 days").closest("[data-tone]")).toBeNull();
      expect(cell("ci-drafts", "in 5 days").getAttribute("data-tone")).toBe("warn");
      expect(cell("old-import", "expired 3 days ago").getAttribute("data-tone")).toBe("danger");
      // No expiry reads never in the text colour, with its tip.
      const never = cell("siem-export", NEVER);
      expect(never.getAttribute("title")).toBe(NEVER_TIP);
      expect(never.className).toContain("text-foreground");
    });
  });

  it("says never for a token never used, in the muted colour, not a dash", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(within(row("policy-ci")).getByText(NEVER).getAttribute("data-last-used")).toBe(NEVER);
  });

  it("revokes from a quiet button", async () => {
    mount();
    const revoke = await screen.findByRole("button", { name: "Revoke midpoint" });
    expect(revoke.textContent).toBe(REVOKE);
    expect(revoke.getAttribute("data-variant")).toBe("ghost");
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
    expect(block.textContent).toContain(SUBJECT_TOKENS + " could not be read because Straza did not answer.");
    expect(block.textContent).toContain("Check that it is running, then reload.");
    // The tab's own button stays, so a token can be minted while the list is unread.
    expect(screen.getByRole("button", { name: NEW_TOKEN })).toBeTruthy();
  });

  it("opens the token's own sheet from a row", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Open midpoint" }));
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-token-sheet")).toBe("midpoint");
  });

  it("opens the mint sheet from the tab's own primary button, and not before", async () => {
    mount();
    await screen.findByRole("button", { name: "Open midpoint" });
    expect(screen.queryByRole("dialog")).toBe(null);
    const mint = screen.getByRole("button", { name: NEW_TOKEN });
    expect(mint.getAttribute("data-variant")).toBe("default");
    await userEvent.click(mint);
    const sheet = await screen.findByRole("dialog");
    expect(sheet.getAttribute("data-mint-sheet")).toBe("form");
    expect(within(sheet).getByText(MINT_TITLE)).toBeTruthy();
  });

  const consequences: [ApiTokenRow, string][] = [
    [midpoint, "midpoint loses the SCIM plane, its reads of users and roles, its reads of servers, the change feed and the configuration read-out within seconds."],
    [siem, "siem-reader loses the audit records and its view of sessions within seconds."],
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
    expect(alert.textContent).toContain("Revoke token refused: the token is already revoked.");
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
