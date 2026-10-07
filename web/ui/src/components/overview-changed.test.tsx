import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Changed } from "./overview-changed";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { AuditRow } from "@/lib/api";
import { CHANGED_NONE, CHANGED_TITLE, CHANGES_UNREAD, seatLine } from "@/lib/config-words";
import { NONE } from "@/lib/words";

const record = (seq: number, type: string, data: Record<string, unknown>, username?: string): AuditRow =>
  ({ seq, ce: JSON.stringify({ type, time: "2026-09-13T09:00:00Z", data }), username });

const mount = (rows: AuditRow[] | null, denied = false) =>
  render(<TooltipProvider><Changed rows={rows} denied={denied} /></TooltipProvider>);

describe("what changed", () => {
  it("reads each record as who did it and what happened", () => {
    mount([record(9, "straza.audit.admin", { action: "published dev-guardrails v3" }, "alice")]);
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByText("published dev-guardrails v3")).toBeTruthy();
  });

  it("names the person who acted, never the person the record is about", () => {
    mount([record(11, "straza.audit.admin", { action: "roles.unassign", actor: "alice", actorVia: "login", user: "u-joe" }, "joe")]);
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.queryByText("joe")).toBeNull();
  });

  it("names the SCIM lane's admin API token on a record the identity manager pushed", () => {
    mount([record(12, "straza.audit.admin", { action: "roles.assign", actor: "midpoint-scim", actorId: "tok-1", actorVia: "api-token", user: "u-joe", origin: "scim" }, "joe")]);
    expect(screen.getByText("midpoint-scim")).toBeTruthy();
    expect(screen.queryByText("joe")).toBeNull();
  });

  it("names nobody on a record of strazad's own background work, which carries no actor", () => {
    mount([record(13, "straza.audit.admin", { action: "draft.expire", draft: "12", revision: 2 })]);
    expect((document.querySelector('[data-changed="13"] b') as HTMLElement).textContent).toBe(NONE);
  });

  it("falls back to the record's own type, in words, when it carries no action", () => {
    mount([record(10, "straza.identity.suspended", {}), record(9, "straza.revocation.user", { user: "u-joe", origin: "admin" })]);
    expect(screen.getAllByText(NONE)).toHaveLength(2);
    expect(screen.getByText("User: suspended")).toBeTruthy();
    expect(screen.getByText("Revoked a user's access")).toBeTruthy();
  });

  it("says nothing changed yet when the chain holds no control-plane record", () => {
    mount([]);
    expect(screen.getByText(CHANGED_NONE)).toBeTruthy();
  });

  it("says the recent changes did not load when the read did not answer", () => {
    mount(null);
    expect(screen.getByText(CHANGES_UNREAD)).toBeTruthy();
  });

  it("says the seat cannot read the chain instead of failing the panel", () => {
    mount(null, true);
    expect(screen.getByText(seatLine(CHANGED_TITLE, "audit:read"))).toBeTruthy();
  });
});
