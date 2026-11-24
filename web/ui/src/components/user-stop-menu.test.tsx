import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StopMenu } from "./user-stop-menu";
import { opensAccount } from "@/lib/entra-words";
import { DISABLE_IN_ENTRA, NO_ACTIVE_SESSION, STOP_DISABLE_LINE, STOP_LOCK_LINE, stopSessionsLine } from "@/lib/user-words";

type Case = [string, { sessions: boolean; active: number; locked: boolean; disabled: boolean; entra?: { url: string } }, string[]];

const ACCOUNT = "https://entra.microsoft.com/#view/Microsoft_AAD_UsersAndTenants/UserProfileMenuBlade/~/overview/userId/1b4e7c90";

const items = () => [...document.querySelectorAll("[data-stop]")].map((e) => e.getAttribute("data-stop") + (e.hasAttribute("data-disabled") ? " off" : ""));

describe("the Stop access menu", () => {
  it.each<Case>([
    ["an active user with a session", { sessions: true, active: 2, locked: false, disabled: false }, ["sessions", "lock", "disable"]],
    ["an active user with no session", { sessions: true, active: 0, locked: false, disabled: false }, ["sessions off", "lock", "disable"]],
    ["a locked user", { sessions: true, active: 0, locked: true, disabled: false }, ["sessions off", "disable"]],
    ["a disabled user, for a role without the sessions area", { sessions: false, active: 1, locked: false, disabled: true }, ["lock"]],
    ["an active user Entra owns", { sessions: true, active: 1, locked: false, disabled: false, entra: { url: ACCOUNT } }, ["sessions", "lock", "entra"]],
    ["a locked user Entra owns", { sessions: false, active: 0, locked: true, disabled: false, entra: { url: ACCOUNT } }, ["entra"]],
    ["a user disabled in Entra", { sessions: true, active: 0, locked: false, disabled: true, entra: { url: ACCOUNT } }, ["sessions off", "lock"]],
    ["a user Entra owns whose account the console cannot place", { sessions: true, active: 1, locked: false, disabled: false, entra: { url: "" } }, ["sessions", "lock"]],
  ])("offers what applies to %s", async (_name, state, want) => {
    render(<StopMenu name="altair" onStop={vi.fn()} entra={null} {...state} />);
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await screen.findByRole("menu");
    expect(items()).toEqual(want);
  });

  it("says what each choice does before it is picked", async () => {
    render(<StopMenu name="altair" sessions active={1} locked={false} disabled={false} entra={null} onStop={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    const menu = await screen.findByRole("menu");
    expect(menu.hasAttribute("data-stop-menu")).toBe(true);
    expect(menu.textContent).toContain(stopSessionsLine("altair"));
    expect(menu.textContent).toContain(STOP_LOCK_LINE);
    expect(menu.textContent).toContain(STOP_DISABLE_LINE);
  });

  it("says no session is active on the disabled sessions choice", async () => {
    render(<StopMenu name="altair" sessions active={0} locked={false} disabled={false} entra={null} onStop={vi.fn()} />);
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    expect((await screen.findByRole("menuitem", { name: /Revoke all sessions/ })).textContent).toContain(NO_ACTIVE_SESSION);
  });

  it("draws the sessions choice that does not apply at full opacity, its name muted, and keeps it out of reach", async () => {
    const onStop = vi.fn();
    render(<StopMenu name="altair" sessions active={0} locked={false} disabled={false} entra={null} onStop={onStop} />);
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    const item = await screen.findByRole("menuitem", { name: /Revoke all sessions/ });
    expect(item.className).not.toContain("opacity-50");
    expect(item.getAttribute("aria-disabled")).toBe("true");
    expect(within(item).getByText("Revoke all sessions").className).toContain("text-muted-foreground");
    expect(within(item).getByText(NO_ACTIVE_SESSION).className).toContain("text-text-2");
    await userEvent.click(item);
    item.focus();
    await userEvent.keyboard("{Enter}");
    expect(onStop).not.toHaveBeenCalled();
    // A choice that applies keeps its name in the body text colour.
    expect(within(screen.getByRole("menuitem", { name: /^Lock/ })).getByText("Lock").className).toContain("text-foreground");
  });

  it("keeps the menu 16 px off each edge of a phone's window", async () => {
    // jsdom lays nothing out, so the window is given a phone's width, and
    // the room the menu may take is read from the width the popper leaves
    // it: the window less 16 px on each side.
    const html = document.documentElement;
    Object.defineProperty(html, "clientWidth", { value: 390, configurable: true });
    Object.defineProperty(html, "clientHeight", { value: 844, configurable: true });
    try {
      render(<StopMenu name="altair" sessions active={1} locked={false} disabled={false} entra={null} onStop={vi.fn()} />);
      await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
      const wrapper = (await screen.findByRole("menu")).parentElement as HTMLElement;
      await waitFor(() => expect(wrapper.style.getPropertyValue("--radix-popper-available-width")).toBe("358px"));
    } finally {
      delete (html as unknown as Record<string, unknown>).clientWidth;
      delete (html as unknown as Record<string, unknown>).clientHeight;
    }
  });

  it("draws no button when nothing is left to stop", () => {
    render(<StopMenu name="altair" sessions={false} active={0} locked disabled entra={null} onStop={vi.fn()} />);
    expect(screen.queryByRole("button", { name: /Stop access/ })).toBeNull();
  });

  it.each([["Revoke all sessions", "sessions"], ["Lock", "lock"], ["Disable", "disable"]])("opens the confirm of %s", async (word, stop) => {
    const onStop = vi.fn();
    render(<StopMenu name="altair" sessions active={1} locked={false} disabled={false} entra={null} onStop={onStop} />);
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: new RegExp("^" + word) }));
    expect(onStop).toHaveBeenCalledWith(stop);
  });

  it("leaves Disable out for a user Entra owns, and links to the account in Entra in its place", async () => {
    const onStop = vi.fn();
    render(<StopMenu name="ada" sessions active={1} locked={false} disabled={false} entra={{ url: ACCOUNT }} onStop={onStop} />);
    await userEvent.click(screen.getByRole("button", { name: /Stop access/ }));
    await screen.findByRole("menu");
    expect(screen.queryByRole("menuitem", { name: /^Disable$/ })).toBeNull();
    const link = screen.getByRole("menuitem", { name: DISABLE_IN_ENTRA });
    expect(link.tagName).toBe("A");
    expect(link.getAttribute("href")).toBe(ACCOUNT);
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("title")).toBe(opensAccount("ada"));
    expect(screen.getByRole("menu").textContent).not.toContain(STOP_DISABLE_LINE);
    expect(onStop).not.toHaveBeenCalled();
  });
});
