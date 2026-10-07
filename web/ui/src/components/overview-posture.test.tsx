import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Posture } from "./overview-posture";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { ConfigAnswer } from "@/lib/api";
import { POSTURE_TITLE, configRows, postureOf, seatLine, setLink, strictLine } from "@/lib/config-words";
import { pathFor } from "@/lib/router";

const RELAXED: ConfigAnswer = {
  public_url: "http://localhost:8420",
  tls: false,
  governance: { min_attestation: "none", local_tool_default: "allow", audit_backpressure: "block", offline_grace_ttl_seconds: 300 },
};

const STRICT: ConfigAnswer = {
  public_url: "https://straza.example.com",
  tls: true,
  governance: { min_attestation: "managed", local_tool_default: "deny", audit_backpressure: "block", offline_grace_ttl_seconds: 0 },
};

const mount = (c: ConfigAnswer | null, denied = false) =>
  render(<TooltipProvider><Posture rows={c ? configRows(c) : null} denied={denied} /></TooltipProvider>);

describe("the Posture panel", () => {
  it("names each relaxed setting with its value, its cost and the row that sets it", async () => {
    mount(RELAXED);
    expect(screen.getByText(/3 relaxed/)).toBeTruthy();
    expect(Array.from(document.querySelectorAll("[data-relaxed]")).map((r) => r.getAttribute("data-relaxed"))).toEqual(["tls", "floor", "local"]);
    const rows = configRows(RELAXED);
    const floor = rows.find((r) => r.id === "floor");
    await userEvent.click(screen.getByRole("button", { name: "Show security configuration" }));
    const link = screen.getByRole("link", { name: setLink(floor!) });
    expect(link.getAttribute("href")).toBe(pathFor("settings", ["configuration"]) + "#floor");
    expect(screen.getByText(new RegExp(floor!.cost!.slice(0, 20)))).toBeTruthy();
  });

  it("folds the strict settings into one line and badges the whole posture as strict", () => {
    mount(STRICT);
    expect(screen.getByText("Partially available")).toBeTruthy();
    expect(document.querySelectorAll("[data-relaxed]").length).toBe(0);
    expect((document.querySelector("[data-strict-line]") as HTMLElement).textContent).toBe(strictLine(postureOf(configRows(STRICT)).strictWords));
  });

  it("says the seat cannot read the configuration instead of failing the panel", () => {
    mount(null, true);
    expect(screen.getByText(seatLine(POSTURE_TITLE, "config:read"))).toBeTruthy();
  });
});
