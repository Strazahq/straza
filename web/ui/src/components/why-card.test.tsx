import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Outcome, WhyCard, decidersLine, wireLine } from "./why-card";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { Decision } from "@/lib/api";
import { ENGINE_FIELDS } from "@/lib/policy-words";

// The four readings a decision can have, in the shapes the simulate route
// answers them: a refusal by a rule, a call a rule allows, a call no rule
// matched, and a call that waits for a person.
const SNAP = "7c1e9a2b4f0d1e6a";

const card = (d: Decision, profile = "enterprise", which: "live" | "draft" = "live") =>
  render(
    <TooltipProvider>
      <WhyCard
        d={d}
        profile={profile}
        mark={which}
        head={<Outcome d={d} />}
        context={decidersLine(d)}
        wire={wireLine(d, SNAP, which)}
      />
    </TooltipProvider>,
  );

const box = () => screen.getByRole("status");
const word = () => document.querySelector("[data-outcome]") as HTMLElement;

// openFields opens the Engine fields fold, which is closed on every open
// and keeps the wire line out of the document until it is.
const openFields = () => userEvent.click(screen.getByRole("button", { name: ENGINE_FIELDS }));

describe("the why card", () => {
  it("reads a refusal by a rule, with the rule and the policy in the sentence", async () => {
    card({ effect: "deny", ruleId: "no-env", setName: "dev-guardrails", reason: "Straza: reading the environment is refused." });
    expect(word().textContent).toBe("Denied");
    expect(word().className).toContain("text-danger");
    expect(box().textContent).toContain("Decided by rule no-env in policy dev-guardrails: Straza: reading the environment is refused.");
    await openFields();
    expect(box().textContent).toContain("effect=deny · ruleId=no-env · setName=dev-guardrails · snapshot=7c1e9a2b (live)");
  });

  it("reads a call a rule allows", () => {
    card({ effect: "allow", ruleId: "dev-mcp-reads", setName: "dev-guardrails" });
    expect(word().textContent).toBe("Allowed");
    expect(word().className).toContain("text-ok");
    expect(box().textContent).toContain("Decided by rule dev-mcp-reads in policy dev-guardrails.");
  });

  it("reads a call no rule matched, allowed by role access", async () => {
    card({ effect: "allow", reason: "allowed by role access" });
    expect(word().textContent).toBe("Allowed");
    expect(box().textContent).toContain("No policy matched this call. Allowed by role access. No policy rule gates this tool.");
    await openFields();
    expect(box().textContent).toContain("effect=allow · ruleId=none · setName=none · snapshot=7c1e9a2b (live)");
  });

  it("reads a call no rule matched, refused by the enterprise default", () => {
    card({ effect: "deny" });
    expect(word().textContent).toBe("Denied");
    expect(box().textContent).toContain("No policy matched this call. Denied by the enterprise profile default: a call nothing allows is denied.");
  });

  it("keeps the generic default sentence when the profile was not read", () => {
    card({ effect: "deny" }, "");
    expect(box().textContent).toContain("No policy matched this call. Denied by the profile default: no policy allowed this call.");
  });

  it("reads a call that waits for a person, and names who decides", async () => {
    card({ effect: "allow", ruleId: "dev-mcp-env-ticket", setName: "dev-guardrails", approve: { class: "ticket", deciders: ["sponsor"] } });
    expect(word().textContent).toBe("Needs approval");
    expect(word().className).toContain("text-foreground");
    expect(box().textContent).toContain("The person behind the agent decides.");
    await openFields();
    expect(box().textContent).toContain("effect=allow · mode=approve · class=ticket · ruleId=dev-mcp-env-ticket · setName=dev-guardrails · snapshot=7c1e9a2b (live)");
  });

  it("names the approver roles, and the sponsor beside them", () => {
    expect(decidersLine({ effect: "allow", approve: { roles: ["sec-approvers"] } })).toBe("The approver role sec-approvers decides.");
    expect(decidersLine({ effect: "allow", approve: { roles: ["sec-approvers", "ops-approvers"] } })).toBe("The approver roles sec-approvers or ops-approvers decide.");
    expect(decidersLine({ effect: "allow", approve: { roles: ["sec-approvers", "straza-admin"], deciders: ["sponsor"] } }))
      .toBe("The person behind the agent or the approver roles sec-approvers or straza-admin decide.");
    expect(decidersLine({ effect: "allow", ruleId: "r" })).toBe("");
  });

  it("reads a call the requester confirms", async () => {
    const d = { effect: "allow", ruleId: "dev-confirm", setName: "dev-guardrails", approve: {}, confirm: true } as Decision;
    card(d);
    expect(word().textContent).toBe("Needs approval");
    expect(box().textContent).toContain("The requester confirms on their own device.");
    await openFields();
    expect(box().textContent).toContain("mode=confirm");
  });

  it("says the draft answered, not the live snapshot", async () => {
    card({ effect: "allow", ruleId: "dev-mcp-env-ticket", setName: "dev-guardrails" }, "enterprise", "draft");
    expect(box().getAttribute("data-why-card")).toBe("draft");
    await openFields();
    expect(box().textContent).toContain("snapshot=(draft)");
  });

  it("keeps the engine's field names behind a fold that opens on the trigger", async () => {
    card({ effect: "deny", ruleId: "no-env", setName: "dev-guardrails" });
    const fold = document.querySelector("[data-engine-fields]") as HTMLElement;
    expect(fold).toBeTruthy();
    expect(fold.textContent).toBe(ENGINE_FIELDS);
    expect(box().textContent).not.toContain("effect=deny");
    expect(screen.getByRole("button", { name: "Help: " + ENGINE_FIELDS })).toBeTruthy();
    await openFields();
    expect(fold.textContent).toContain("effect=deny · ruleId=no-env · setName=dev-guardrails");
  });

  it("carries the caption and the tone of the card it is given, and folds nothing without a wire line", () => {
    render(<TooltipProvider><WhyCard d={{ effect: "deny" }} profile="enterprise" mark="live" caption="Live now" tone="danger" head={<Outcome d={{ effect: "deny" }} />} /></TooltipProvider>);
    expect(screen.getByText("Live now")).toBeTruthy();
    expect(box().className).toContain("border-danger/40");
    expect(document.querySelector("[data-engine-fields]")).toBeNull();
  });
});
