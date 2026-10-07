import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TokenSheet } from "./settings-token-sheet";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { ApiTokenRow } from "@/lib/api";
import {
  FULL_BADGE,
  FULL_TIP,
  MINTED_BY,
  NEVER,
  NOT_SET,
  SENSITIVE,
  TOKEN_ELIDED,
  TOKEN_KIND,
  areaHint,
  expiresWord,
  headerLine,
  useLine,
} from "@/lib/settings-words";
import { absTime, relTimeText } from "@/lib/words";

const DAY = 86400000;
const at = (ms: number) => new Date(Date.now() + ms).toISOString();

const midpoint: ApiTokenRow = {
  id: "tok-mid", name: "midpoint", created_by: "alice",
  scope: "scim:read,scim:write,identity:read",
  created: at(-6 * DAY), expires: at(84 * DAY), lastUsed: at(-3 * 60000),
};

const onRevoke = vi.fn();
const onOpenChange = vi.fn();
const mount = (token: ApiTokenRow = midpoint) =>
  render(
    <TooltipProvider>
      <TokenSheet token={token} open={true} onOpenChange={onOpenChange} onRevoke={onRevoke} />
    </TooltipProvider>,
  );
const sheet = () => screen.getByRole("dialog");

describe("a token's sheet", () => {
  it("names the credential and its facts, with the relative reading beside each stamp", () => {
    mount();
    expect(within(sheet()).getByText("midpoint")).toBeTruthy();
    expect(within(sheet()).getByText(TOKEN_KIND)).toBeTruthy();
    const facts = sheet().querySelector("[data-facts]") as HTMLElement;
    expect(facts.textContent).toContain(MINTED_BY);
    expect(facts.textContent).toContain("alice");
    expect(facts.textContent).toContain(absTime(midpoint.created));
    expect(facts.textContent).toContain("(" + relTimeText(midpoint.created) + ")");
    expect(facts.textContent).toContain(absTime(midpoint.expires));
    expect(facts.textContent).toContain("(" + expiresWord(midpoint.expires) + ")");
    expect(facts.textContent).toContain("(" + relTimeText(midpoint.lastUsed) + ")");
  });

  it("reads an absent fact as a word, never a dash", () => {
    mount({ ...midpoint, created_by: undefined, expires: undefined, lastUsed: undefined });
    const facts = sheet().querySelector("[data-facts]") as HTMLElement;
    expect(facts.textContent).toContain(NOT_SET);
    expect(within(facts).getAllByText(NEVER).length).toBe(2);
    expect(facts.textContent).not.toContain("--");
  });

  it("gives every grant the sentence of the area it reaches, and marks the sensitive one", () => {
    mount();
    const line = (grant: string) => sheet().querySelector("[data-grant-line='" + grant + "']") as HTMLElement;
    expect(line("identity:read").textContent).toContain(areaHint("identity") + ".");
    expect(line("scim:read").textContent).toContain(areaHint("scim") + ".");
    expect(line("scim:read").textContent).not.toContain("Sensitive");
    expect(line("scim:write").textContent).toContain(SENSITIVE["scim:write"]);
    expect(within(line("scim:write")).getByText("scim:write").className).toContain("text-warn");
  });

  it("says how a caller signs with it, and names SCIM when the grants reach it", () => {
    mount();
    expect(within(sheet()).getByText(headerLine(TOKEN_ELIDED))).toBeTruthy();
    expect(within(sheet()).getByText(useLine(true))).toBeTruthy();
  });

  it("says a full scope in one badge, and drops SCIM from the use line without it", () => {
    mount({ ...midpoint, scope: "full" });
    expect(within(sheet()).getAllByText(FULL_BADGE).length).toBeGreaterThan(0);
    expect(within(sheet()).getByText(FULL_TIP)).toBeTruthy();
    mount({ ...midpoint, name: "policy-ci", scope: "policy:read,policy:write" });
    expect(screen.getAllByText(useLine(false)).length).toBeGreaterThan(0);
  });

  it("hands the revoke back to the tab and closes on Close", async () => {
    mount();
    await userEvent.click(within(sheet()).getByRole("button", { name: "Revoke token" }));
    expect(onRevoke).toHaveBeenCalledWith(midpoint);
    const footer = sheet().querySelector("[data-slot='sheet-footer']") as HTMLElement;
    await userEvent.click(within(footer).getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
