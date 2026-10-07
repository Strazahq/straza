import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RuleEditor } from "./policy-rule-editor";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { RoleRow } from "@/lib/api";
import { type Doc, type RuleView, docText, openDoc, rulesOf } from "@/lib/policy-model";
import {
  ADVANCED, BUCKET_WORD, CANCEL, HOW_HOLD, HOW_TICKET, HOW_TICKET_END, NO_APPROVER_ROLE, REASON, REASON_STALE, REASON_UNUSED, REMOVE,
  REMOVE_RULE, UNSET_SPAN, WHO_BOTH, WHO_SPONSOR, WHO_TEAM, allowRemoves, appliesToAll, consequenceWords, moveOut,
} from "@/lib/policy-words";

const repo = (rel: string) => fileURLToPath(new URL("../../../../" + rel, import.meta.url));
const seed = readFileSync(repo("web/ui/src/test/testdata/dev-guardrails.yaml"), "utf8");

const commentLines = (text: string) => text.split("\n").map((l) => l.trim()).filter((l) => l.startsWith("#"));

const roles: RoleRow[] = [
  { id: "r1", name: "dev-tools", kind: "application", holder_count: 2 },
  { id: "r2", name: "sec-approvers", kind: "approver", holder_count: 2 },
  { id: "r3", name: "ops-approvers", kind: "approver", holder_count: 5 },
  { id: "r4", name: "straza-admin", kind: "straza", holder_count: 1 },
];

// The harness is the page in miniature: one stored text, an edit that
// applies a model function to it, and the fresh rule read back from the
// text the edit produced.
const state = { text: seed, touched: [] as string[] };

function Harness({ id, held, onRemove, onSplit }: { id: string; held: RoleRow[]; onRemove: (id: string) => void; onSplit?: (name: string) => void }) {
  const [text, setText] = React.useState(seed);
  const doc = openDoc(text);
  const rule = rulesOf(doc).find((r) => r.id === id) as RuleView;
  const onEdit = (touched: string, fn: (d: Doc) => void) => {
    const next = openDoc(text);
    fn(next);
    state.text = docText(next);
    state.touched.push(touched);
    setText(state.text);
  };
  return <RuleEditor doc={doc} rule={rule} events={null} roles={held} onEdit={onEdit} onRemove={onRemove} onSplit={onSplit} />;
}

const mount = (id: string, held: RoleRow[] = roles, onSplit?: (name: string) => void) => {
  const onRemove = vi.fn();
  render(<TooltipProvider><Harness id={id} held={held} onRemove={onRemove} onSplit={onSplit} /></TooltipProvider>);
  return onRemove;
};

// now reads the rule back out of the text the edits produced.
const now = (id: string) => rulesOf(openDoc(state.text)).find((r) => r.id === id) as RuleView;

const seg = (word: string) => screen.getByRole("button", { name: word });
const num = (label: string) => screen.getByRole("spinbutton", { name: label }) as HTMLInputElement;
const unit = (label: string) => screen.getByRole("combobox", { name: label + " unit" });

beforeEach(() => {
  state.text = seed;
  state.touched = [];
});

describe("an open rule card, as it reads", () => {
  it("marks what happens, says the consequence and names the pool and the windows", () => {
    mount("dev-mcp-env-ticket");
    expect(seg(BUCKET_WORD.hum).getAttribute("aria-pressed")).toBe("true");
    expect(seg(BUCKET_WORD.deny).getAttribute("aria-pressed")).toBe("false");
    expect(seg(BUCKET_WORD.allow).getAttribute("aria-pressed")).toBe("false");
    expect((document.querySelector("[data-consequence]") as HTMLElement).textContent).toBe(consequenceWords("ticket"));
    expect(screen.getByRole("radio", { name: WHO_SPONSOR }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("radio", { name: HOW_TICKET }).getAttribute("aria-checked")).toBe("true");
    expect([num(HOW_TICKET).value, unit(HOW_TICKET).textContent]).toEqual(["1", "day"]);
    expect([num(HOW_TICKET_END).value, unit(HOW_TICKET_END).textContent]).toEqual(["1", "hour"]);
  });

  it("marks the third pool where the rule already names the sponsor and a team", () => {
    mount("dev-deploy-ticket");
    expect(screen.getByRole("radio", { name: WHO_BOTH }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("combobox", { name: WHO_TEAM }).textContent).toBe("sec-approvers");
  });

  it("offers the third pool to a rule that names one, and writes the sponsor with the team", async () => {
    mount("dev-mcp-sum-approval-showcase");
    const both = screen.getByRole("radio", { name: WHO_BOTH });
    expect(both.getAttribute("aria-checked")).toBe("false");
    await userEvent.click(both);
    expect(now("dev-mcp-sum-approval-showcase").who).toEqual({ sponsor: true, roles: ["sec-approvers"] });
  });

  it("says the outcome covers every call a rule of several names", () => {
    mount("no-rm-rf");
    expect((document.querySelector("[data-applies-all]") as HTMLElement).textContent).toBe(appliesToAll(3));
  });

  it("says nothing about every call where the rule names one", () => {
    mount("dev-mcp-env-ticket");
    expect(document.querySelector("[data-applies-all]")).toBeNull();
  });

  it("leaves the window of the branch the rule does not use empty", () => {
    mount("dev-mcp-env-ticket");
    expect(num(HOW_HOLD).value).toBe("");
    expect(num(HOW_HOLD).disabled).toBe(true);
    expect(unit(HOW_HOLD).textContent).toBe(UNSET_SPAN);
  });

  it("says what is missing where the deployment holds no approver role", async () => {
    mount("dev-mcp-sum-approval-showcase", [{ id: "r1", name: "dev-tools", kind: "application" }]);
    expect(screen.queryByRole("combobox", { name: WHO_TEAM })).toBeNull();
    expect(screen.getByText(NO_APPROVER_ROLE)).toBeTruthy();
    await userEvent.click(screen.getByRole("radio", { name: WHO_TEAM }));
    expect(state.text).toBe(seed);
    expect(screen.getByRole("radio", { name: WHO_SPONSOR }).getAttribute("aria-checked")).toBe("true");
  });
});

describe("changing what a rule does", () => {
  it("drops the approval block on Allowed and puts the reason out of reach", async () => {
    mount("dev-mcp-env-ticket");
    await userEvent.click(seg(BUCKET_WORD.allow));
    const rule = now("dev-mcp-env-ticket");
    expect(rule.posture).toBe("allow");
    expect(rule.raw.mode).toBeUndefined();
    expect(rule.raw.approve).toBeUndefined();
    expect((screen.getByRole("textbox", { name: REASON }) as HTMLInputElement).disabled).toBe(true);
    expect((document.querySelector("[data-reason-unused]") as HTMLElement).textContent).toBe(REASON_UNUSED);
    // The reason stays in the document, untouched, for a rule that denies
    // again later.
    expect(String(rule.raw.reason)).toContain("day-scale approval ticket");
    expect(commentLines(state.text).length).toBe(commentLines(seed).length);
  });

  it("routes a denial to the sponsor for two minutes on Needs approval", async () => {
    mount("no-rm-rf");
    await userEvent.click(seg(BUCKET_WORD.hum));
    const rule = now("no-rm-rf");
    expect(rule.posture).toBe("hold");
    expect(rule.who).toEqual({ sponsor: true, roles: [] });
    expect(rule.timeoutSeconds).toBe(120);
    expect([num(HOW_HOLD).value, unit(HOW_HOLD).textContent]).toEqual(["2", "minutes"]);
    expect(commentLines(state.text).length).toBe(commentLines(seed).length);
  });

  it("names the approver team the radio picks", async () => {
    mount("dev-mcp-sum-approval-showcase");
    await userEvent.click(screen.getByRole("radio", { name: WHO_TEAM }));
    expect(now("dev-mcp-sum-approval-showcase").who).toEqual({ sponsor: false, roles: ["sec-approvers"] });
    await userEvent.click(screen.getByRole("combobox", { name: WHO_TEAM }));
    await userEvent.click(screen.getByRole("option", { name: "ops-approvers" }));
    expect(now("dev-mcp-sum-approval-showcase").who).toEqual({ sponsor: false, roles: ["ops-approvers"] });
  });

  it("writes a window typed in minutes as its seconds", async () => {
    mount("dev-mcp-sum-approval-showcase");
    await userEvent.clear(num(HOW_HOLD));
    await userEvent.type(num(HOW_HOLD), "5");
    await waitFor(() => expect(now("dev-mcp-sum-approval-showcase").timeoutSeconds).toBe(300));
    expect(commentLines(state.text).length).toBe(commentLines(seed).length);
  });

  it("turns a hold into a ticket and keeps the pool", async () => {
    mount("dev-mcp-sum-approval-showcase");
    await userEvent.click(screen.getByRole("radio", { name: HOW_TICKET }));
    const rule = now("dev-mcp-sum-approval-showcase");
    expect(rule.posture).toBe("ticket");
    expect((rule.raw.approve as Record<string, unknown>).class).toBe("ticket");
    expect((rule.raw.approve as Record<string, unknown>).timeoutSeconds).toBeUndefined();
    expect(rule.who).toEqual({ sponsor: true, roles: [] });
  });

  it("warns when a team is paged for a window too short to answer", async () => {
    mount("dev-mcp-midpoint-workitem-selfapprove");
    expect(document.querySelector("[data-w1]")).toBeNull();
    await userEvent.click(unit(HOW_HOLD));
    await userEvent.click(screen.getByRole("option", { name: "seconds" }));
    await userEvent.clear(num(HOW_HOLD));
    await userEvent.type(num(HOW_HOLD), "90");
    await waitFor(() => expect(now("dev-mcp-midpoint-workitem-selfapprove").timeoutSeconds).toBe(90));
    const line = document.querySelector("[data-w1]") as HTMLElement;
    expect(line.textContent).toContain("2 people paged for a 90 s window");
  });

  it("flags a reason that still refuses once the rule waits for a person", async () => {
    mount("no-rm-rf");
    expect(document.querySelector("[data-reason-stale]")).toBeNull();
    await userEvent.click(seg(BUCKET_WORD.hum));
    expect((document.querySelector("[data-reason-stale]") as HTMLElement).textContent).toBe(REASON_STALE);
    const box = screen.getByRole("textbox", { name: REASON });
    await userEvent.clear(box);
    await userEvent.type(box, "Straza: a person decides destructive commands");
    await userEvent.tab();
    expect(document.querySelector("[data-reason-stale]")).toBeNull();
  });

  it("says what a switch from a denial to Allowed takes away", async () => {
    mount("no-rm-rf");
    expect(document.querySelector("[data-allow-removes]")).toBeNull();
    await userEvent.click(seg(BUCKET_WORD.allow));
    expect((document.querySelector("[data-allow-removes]") as HTMLElement).textContent).toBe(allowRemoves(3));
  });

  it("hands the matching fold the split of one call", async () => {
    const onSplit = vi.fn();
    mount("no-rm-rf", roles, onSplit);
    await userEvent.click(screen.getByRole("button", { name: new RegExp("^" + ADVANCED) }));
    await userEvent.click(screen.getByRole("button", { name: moveOut("rm -rf *") }));
    expect(onSplit).toHaveBeenCalledWith("rm -rf *");
  });

  it("writes the reason on blur", async () => {
    mount("no-rm-rf");
    const box = screen.getByRole("textbox", { name: REASON });
    await userEvent.clear(box);
    await userEvent.type(box, "Straza: destructive commands are refused for role dev");
    await userEvent.tab();
    expect(now("no-rm-rf").reason).toBe("Straza: destructive commands are refused for role dev");
    expect(state.touched).toContain("no-rm-rf");
    expect(commentLines(state.text).length).toBe(commentLines(seed).length);
  });
});

describe("removing a rule", () => {
  it("names the rule in the confirm and hands the page the removal", async () => {
    const onRemove = mount("no-rm-rf");
    await userEvent.click(screen.getByRole("button", { name: REMOVE_RULE }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Remove rule no-rm-rf?");
    await userEvent.click(screen.getByRole("button", { name: CANCEL }));
    expect(onRemove).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: REMOVE_RULE }));
    await screen.findByRole("alertdialog");
    await userEvent.click(screen.getByRole("button", { name: REMOVE }));
    expect(onRemove).toHaveBeenCalledWith("no-rm-rf");
  });
});
