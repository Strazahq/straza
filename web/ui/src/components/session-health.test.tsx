import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SessionHealth } from "./session-health";
import type { SessionRow } from "@/lib/api";
import { CLIENT_ROW, HOOK_ROW, NOT_MEASURED_ROW } from "@/lib/session-words";
import { absTime } from "@/lib/words";

const base: SessionRow = {
  id: "01a1da3d-934c-7be8-bd1a-92e51c045044", user_id: "u-altair", harness: "claude-code/2.2.0", client_version: "2.2.0", attestation: "managed", status: "active",
  started_at: "2026-11-14T14:27:10Z", last_seen: "2026-11-14T14:30:20Z", wiring_status: "current", wiring_hash: "sha256:236f8d3dd754",
};

const rowsOf = () => [...document.querySelectorAll("[data-health-row]")].map((r) => {
  const svg = r.querySelector("svg");
  return [r.textContent, svg ? (svg.getAttribute("class") || "").match(/text-(ok|warn|danger)/)?.[1] : null];
});

describe("the Health card", () => {
  it.each([
    ["managed", "current", "ok", "ok"],
    ["advisory", "allowed", "warn", "warn"],
    ["none", "mismatch", "danger", "danger"],
    ["managed", "unmeasured", "ok", "warn"],
  ])("draws a client %s and a hook configuration %s", (att, wiring, clientTone, hookTone) => {
    render(<SessionHealth row={{ ...base, attestation: att, wiring_status: wiring }} />);
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Health");
    expect(rowsOf()).toEqual([[CLIENT_ROW[att].join(""), clientTone], [HOOK_ROW[wiring].join(""), hookTone]]);
  });

  it("falls back to the failed client and the unmeasured hook configuration for a value it does not know", () => {
    render(<SessionHealth row={{ ...base, attestation: "partial", wiring_status: "retired" }} />);
    expect(rowsOf()).toEqual([[CLIENT_ROW.none.join(""), "danger"], [HOOK_ROW.unmeasured.join(""), "warn"]]);
  });

  it("falls back to the unmeasured hook configuration when the server sends none", () => {
    render(<SessionHealth row={{ ...base, wiring_status: undefined }} />);
    expect(rowsOf()[1]).toEqual([HOOK_ROW.unmeasured.join(""), "warn"]);
  });

  it("draws one row with no icon for a session of an admin harness", () => {
    render(<SessionHealth row={{ ...base, harness: "console/2.2.0", attestation: "none", wiring_status: undefined }} />);
    expect(rowsOf()).toEqual([[NOT_MEASURED_ROW.join(""), null]]);
  });

  it("keeps the ids and hashes in a closed fold", async () => {
    render(<SessionHealth row={base} />);
    const fold = document.querySelector("[data-ids-fold]") as HTMLDetailsElement;
    expect(fold.open).toBe(false);
    expect(fold.querySelector("summary")?.textContent).toBe("Ids and hashes");
    await userEvent.click(screen.getByText("Ids and hashes"));
    expect(fold.open).toBe(true);
    const facts = [...fold.querySelectorAll("[data-fact]")].map((f) => [f.getAttribute("data-fact"), f.querySelector("dd")?.textContent]);
    expect(facts).toEqual([
      ["Session id", base.id],
      ["Client build", "2.2.0"],
      ["Hook configuration", "sha256:236f8d3dd754"],
      ["Started", absTime(base.started_at)],
      ["Last seen", absTime(base.last_seen)],
    ]);
    expect(fold.querySelector('[data-fact="Session id"] dd span')?.className).toContain("font-mono");
  });

  it("leaves out the client build and the hook configuration the session does not have", () => {
    render(<SessionHealth row={{ ...base, client_version: undefined, wiring_hash: undefined }} />);
    expect([...document.querySelectorAll("[data-fact]")].map((f) => f.getAttribute("data-fact"))).toEqual(["Session id", "Started", "Last seen"]);
  });
});
