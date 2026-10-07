import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyRules } from "./policy-rules";
import { openDoc, rulesOf } from "@/lib/policy-model";
import { BUCKET_WORD, EVERY_WORD, MARK, NO_REASON_YET, NO_RULE_MATCH, RESTATES_ACCESS, RULES_SEARCH, WHERE_WORD, howLine, openRule, rulesMatch } from "@/lib/policy-words";

// One set with the rule shapes the table has to read: a whole server, one
// named tool that waits for a person, a shell denial with its reason, a
// file denial without one, and a check the cards do not edit.
const text = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: shapes
spec:
  rules:
    - id: whole-server
      tools: [mcp.call]
      apps: [ledger-mcp]
      effect: allow
    - id: env-approval
      tools: [mcp.call]
      apps: [demo-tools]
      toolNames:
        allow: [get-env]
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
        timeoutSeconds: 120
      reason: "Straza: reading the environment needs approval"
    - id: no-rm-rf
      tools: [shell.exec]
      command:
        denyPatterns: ["rm -rf *"]
      effect: deny
      reason: "Straza: destructive command blocked"
    - id: secrets-denied
      tools: [file.write, file.edit]
      paths:
        deny: ["**/.env*"]
      effect: deny
    - id: checked-first
      tools: [mcp.call]
      apps: [ledger-mcp]
      toolNames:
        allow: [post_journal]
      effect: allow
      mode: serverCheck
`;

const rules = rulesOf(openDoc(text));
const ruleOf = (id: string) => rules.filter((r) => r.id === id)[0];

const mount = (props: Partial<React.ComponentProps<typeof PolicyRules>> = {}) => {
  const onOpen = vi.fn();
  render(<PolicyRules rules={rules} changed={[]} added={[]} openId={null} onOpen={onOpen} {...props} />);
  return onOpen;
};

const row = (id: string) => document.querySelector('[data-rule="' + id + '"]') as HTMLElement;
const cells = (id: string) => Array.from(row(id).querySelectorAll("td")).map((c) => c.textContent || "");
const ids = () => Array.from(document.querySelectorAll("[data-rule]")).map((e) => e.getAttribute("data-rule"));
const count = () => (document.querySelector("[data-rules]") as HTMLElement).getAttribute("data-rules");

describe("the rules table", () => {
  it("reads one row per rule: where, which calls, what happens and why", () => {
    mount();
    expect(count()).toBe("5");
    const c = cells("env-approval");
    expect(c[0]).toBe("demo-tools");
    expect(c[1]).toBe("get-env");
    expect(c[2]).toContain(BUCKET_WORD.hum);
    expect(c[2]).toContain(howLine(ruleOf("env-approval")));
    expect(c[3]).toContain("Straza: reading the environment needs approval");
    expect(c[4]).toContain("env-approval");
  });

  it("says the lane where the rule is not an MCP one", () => {
    mount();
    expect(cells("no-rm-rf")[0]).toBe(WHERE_WORD.shell);
    expect(cells("secrets-denied")[0]).toBe(WHERE_WORD.files);
  });

  it("says every tool where an MCP rule names none", () => {
    mount();
    expect(cells("whole-server")[1]).toBe(EVERY_WORD.mcp);
  });

  it("says an MCP allow only restates role access, and marks a missing reason", () => {
    mount();
    expect(cells("whole-server")[3]).toBe(RESTATES_ACCESS);
    expect(cells("secrets-denied")[3]).toBe(NO_REASON_YET);
    expect(cells("checked-first")[3]).toBe(NO_REASON_YET);
  });

  it("orders the rows by where the call goes, servers first and alphabetical", () => {
    mount();
    expect(ids()).toEqual(["env-approval", "checked-first", "whole-server", "no-rm-rf", "secrets-denied"]);
  });

  it("puts a rule this page added at the top and marks what is unpublished", () => {
    mount({ added: ["secrets-denied"], changed: ["no-rm-rf"] });
    expect(ids()[0]).toBe("secrets-denied");
    expect(row("secrets-denied").getAttribute("data-mark")).toBe("new");
    expect(cells("secrets-denied")[4]).toContain(MARK.new);
    expect(row("no-rm-rf").getAttribute("data-mark")).toBe("edited");
    expect(row("whole-server").getAttribute("data-mark")).toBeNull();
  });

  it("narrows the table to one outcome", async () => {
    mount();
    await userEvent.click(screen.getByRole("button", { name: BUCKET_WORD.deny }));
    expect(ids()).toEqual(["no-rm-rf", "secrets-denied"]);
    expect(count()).toBe("2");
    expect(document.querySelector("[data-rules-filter]")?.getAttribute("data-rules-filter")).toBe("deny");
    expect(screen.getByRole("button", { name: BUCKET_WORD.deny }).getAttribute("aria-pressed")).toBe("true");
  });

  it("searches the ids, the reasons, the calls and the servers", async () => {
    mount();
    const box = screen.getByRole("textbox", { name: RULES_SEARCH });
    await userEvent.type(box, "get-env");
    expect(ids()).toEqual(["env-approval"]);
    expect(screen.getByText(rulesMatch(1, 5))).toBeTruthy();
    await userEvent.clear(box);
    await userEvent.type(box, "destructive");
    expect(ids()).toEqual(["no-rm-rf"]);
    await userEvent.clear(box);
    await userEvent.type(box, "ledger");
    expect(ids()).toEqual(["checked-first", "whole-server"]);
  });

  it("says so when nothing matches the search", async () => {
    mount();
    await userEvent.type(screen.getByRole("textbox", { name: RULES_SEARCH }), "nothing-here");
    expect(count()).toBe("0");
    expect(screen.getByText(NO_RULE_MATCH)).toBeTruthy();
  });

  it("tells the page which rule to open, on a click and on Enter", async () => {
    const onOpen = mount();
    await userEvent.click(screen.getByRole("button", { name: openRule("no-rm-rf") }));
    expect(onOpen).toHaveBeenCalledWith("no-rm-rf");
    fireEvent.keyDown(row("whole-server"), { key: "Enter" });
    expect(onOpen).toHaveBeenCalledWith("whole-server");
  });

  it("marks the row whose sheet is open", () => {
    mount({ openId: "no-rm-rf" });
    expect(row("no-rm-rf").getAttribute("data-rule-open")).toBe("no-rm-rf");
    expect(row("whole-server").getAttribute("data-rule-open")).toBeNull();
  });
});
