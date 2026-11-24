import type * as React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type Ask, UserConfirm } from "./user-confirm";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { AssignmentRow, DeviceRow } from "@/lib/api";
import { OWN_SESSION_WARNING } from "@/lib/session-words";
import {
  IDM_OWNS, REASON_LABEL, REASON_MISSING, deadRowBody, deviceRevokeBody, disableBody, keyRevokeBody, lockBody, revokeAllBody, revokeRoleBody, unlockBody,
} from "@/lib/user-words";

const row: AssignmentRow = { id: "as1", subject_kind: "user", subject_id: "u", role_id: "r-gh", origin: "admin" };
const dead: AssignmentRow = { ...row, valid_to: "2026-01-01T00:00:00Z" };
const later: AssignmentRow = { ...row, valid_from: "2999-01-01T00:00:00Z" };
const laptop: DeviceRow = { id: "d1", user_id: "u", name: "mara-laptop", fingerprint: "f", platform: "macos", status: "active", enrolled_at: "" };

function draw(ask: Ask, extra: Partial<React.ComponentProps<typeof UserConfirm>> = {}) {
  const props = { onGo: vi.fn(), onClose: vi.fn() };
  render(
    <TooltipProvider>
      <UserConfirm ask={ask} name="mara" scim={false} busy={false} problem={null} sessions={0} own={false} {...props} {...extra} />
    </TooltipProvider>,
  );
  return props;
}

const dialog = () => screen.getByRole("alertdialog");
const primary = () => within(dialog()).getAllByRole("button").at(-1) as HTMLButtonElement;

describe("the confirms of a user's page", () => {
  it.each<[string, Ask, boolean, string, string, string, boolean]>([
    ["unlock", { kind: "unlock" }, false, "Unlock mara?", unlockBody("mara"), "Unlock", false],
    ["disable", { kind: "disable" }, false, "Disable mara?", disableBody("mara"), "Disable", true],
    ["remove a role", { kind: "removeRole", row, role: "github-readers" }, false, "Remove github-readers from mara?", revokeRoleBody("mara", "github-readers"), "Remove role", true],
    ["remove a role of a SCIM user", { kind: "removeRole", row, role: "github-readers" }, true, "Remove github-readers from mara?", revokeRoleBody("mara", "github-readers") + " " + IDM_OWNS, "Remove role", true],
    ["remove the assignment made here of a role also held from Entra", { kind: "removeRole", row, role: "github-readers", fromEntra: true }, false, "Remove github-readers from mara?", "Only the assignment made here goes. mara keeps github-readers from Entra.", "Remove role", true],
    ["remove the identity manager's assignment of a role also held from Entra", { kind: "removeRole", row: { ...row, origin: "scim" }, role: "github-readers", fromEntra: true }, true, "Remove github-readers from mara?", "Only the assignment from your identity manager goes. mara keeps github-readers from Entra. " + IDM_OWNS, "Remove role", true],
    ["remove an expired assignment", { kind: "removeRole", row: dead, role: "github-readers" }, false, "Remove github-readers from mara?", deadRowBody("mara", "github-readers", "expired"), "Remove role", true],
    ["remove an assignment not yet valid", { kind: "removeRole", row: later, role: "github-readers" }, false, "Remove github-readers from mara?", "mara does not get github-readers when the assignment starts.", "Remove role", true],
    ["revoke a device", { kind: "device", device: laptop, label: "mara-laptop" }, false, "Revoke mara-laptop?", deviceRevokeBody("mara", "mara-laptop"), "Revoke device", true],
    ["revoke the key", { kind: "revokeKey" }, false, "Revoke the assertion key of mara?", keyRevokeBody("mara"), "Revoke key", true],
  ])("asks to %s with its title, body and button", async (_name, ask, scim, title, body, button, red) => {
    const { onGo } = draw(ask, { scim });
    expect(within(dialog()).getByRole("heading").textContent).toBe(title);
    expect(dialog().textContent).toContain(body);
    expect(primary().textContent).toBe(button);
    expect(primary().className.includes("bg-danger")).toBe(red);
    await userEvent.click(primary());
    expect(onGo).toHaveBeenCalledWith("");
  });

  it("locks only with a reason, answered at the field", async () => {
    const { onGo } = draw({ kind: "lock" });
    expect(within(dialog()).getByRole("heading").textContent).toBe("Lock mara?");
    expect(dialog().textContent).toContain(lockBody("mara"));
    (document.activeElement as HTMLElement).blur();
    await userEvent.click(primary());
    expect(screen.getByText(REASON_MISSING)).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText(REASON_LABEL));
    expect(onGo).not.toHaveBeenCalled();
    await userEvent.type(screen.getByLabelText(REASON_LABEL), "  incident 42 ");
    await userEvent.click(primary());
    expect(onGo).toHaveBeenCalledWith("incident 42");
  });

  it("counts the active sessions it ends, with the reader's own named", () => {
    draw({ kind: "sessions" }, { sessions: 2, own: true });
    expect(within(dialog()).getByRole("heading").textContent).toBe("Revoke every active session of mara?");
    expect(dialog().textContent).toContain(revokeAllBody(2, "mara") + " " + OWN_SESSION_WARNING);
    expect(primary().textContent).toBe("Revoke 2 sessions");
    expect(primary().disabled).toBe(false);
  });

  it("names no session of the reader on another user's page", () => {
    draw({ kind: "sessions" }, { sessions: 1, own: false });
    expect(dialog().textContent).toContain(revokeAllBody(1, "mara"));
    expect(dialog().textContent).not.toContain(OWN_SESSION_WARNING);
    expect(primary().textContent).toBe("Revoke session");
  });

  it("shows a refusal inside the dialog, such as the server's word on a break-glass account", () => {
    draw({ kind: "disable" }, { problem: { subject: "Disable", text: "Straza refused it: break-glass accounts cannot be disabled. Fix what it names, then try again." } });
    expect(within(dialog()).getByRole("alert").textContent).toBe("Disable refused: break-glass accounts cannot be disabled. Fix what it names, then try again.");
  });
});
