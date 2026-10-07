import * as React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyMatching } from "./policy-matching";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { EventSupport } from "@/lib/api";
import { type Doc, type RuleView, docText, openDoc, rulesOf } from "@/lib/policy-model";
import {
  ADD_PATTERN, ADD_TOOL, ADVANCED, CHOOSE_EVENTS, EVERY_WORD, MATCH, PATTERNS_HINT, REQUIRE_NONE, WHERE_WORD, eventsCoverage,
  eventsNeverFire, eventsWords, foldSummary, moveOut, removePick,
} from "@/lib/policy-words";

// One set with the shapes the fold has to read: an MCP rule whose tools
// are chips, a shell rule whose patterns are chips, a rule that fires only
// on an event some harnesses never send, and a rule carrying keys the
// cards cannot show. The comment lines prove an edit keeps them.
const text = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: shapes
spec:
  rules:
    # The ledger closes by hand, so two tools are refused outright.
    - id: ledger-writes
      tools: [mcp.call]
      apps: [ledger-mcp]
      toolNames:
        deny: ["post_journal", "close_period"]
      effect: deny
      reason: "Straza: the ledger closes by hand"
    # The shell floor.
    - id: no-force-push
      events: [tool.pre]
      tools: [shell.exec]
      command:
        denyPatterns: ["git push --force*"]
      effect: deny
      reason: "Straza: no force push on this seat"
    # Subagents are refused where the harness says it spawned one.
    - id: no-subagents
      events: [subagent.pre]
      tools: [task.spawn]
      effect: deny
      reason: "Straza: subagents are refused on this seat"
    # Secret writes need an attested session and a person who is not the
    # requester.
    - id: attested-writes
      tools: [file.write, file.edit]
      paths:
        deny: ["**/.env*"]
      require:
        attestation: managed
        deviceCert: true
        harness: [claude-code]
      effect: allow
      mode: approve
      approve:
        deciders: [sponsor]
        timeoutSeconds: 120
        selfApproval: false
      reason: "Straza: secret writes need an attested session"
`;

const matrix: EventSupport = {
  harnesses: ["claude-code", "codex", "gemini"],
  events: [
    { kind: "tool.pre", blocking: true, harnesses: ["claude-code", "codex", "gemini"] },
    { kind: "subagent.pre", blocking: true, harnesses: ["claude-code"] },
    { kind: "compact.pre", harnesses: ["claude-code", "codex"] },
  ],
};

const commentLines = (t: string) => t.split("\n").map((l) => l.trim()).filter((l) => l.startsWith("#"));

const state = { text };

function Harness({ id, events, onSplit }: { id: string; events: EventSupport | null; onSplit?: (name: string) => void }) {
  const [stored, setStored] = React.useState(text);
  const rule = rulesOf(openDoc(stored)).find((r) => r.id === id) as RuleView;
  const onEdit = (_touched: string, fn: (d: Doc) => void) => {
    const next = openDoc(stored);
    fn(next);
    state.text = docText(next);
    setStored(state.text);
  };
  return <PolicyMatching rule={rule} events={events} onEdit={onEdit} onSplit={onSplit} />;
}

const rules = rulesOf(openDoc(text));
const ruleOf = (id: string) => rules.find((r) => r.id === id) as RuleView;
const now = (id: string) => rulesOf(openDoc(state.text)).find((r) => r.id === id) as RuleView;

const mount = (id: string, events: EventSupport | null = matrix, onSplit?: (name: string) => void) =>
  render(<TooltipProvider><Harness id={id} events={events} onSplit={onSplit} /></TooltipProvider>);
const fold = () => screen.getByRole("button", { name: new RegExp("^" + ADVANCED) });
const cell = (slot: string) => document.querySelector('[data-match="' + slot + '"]') as HTMLElement;
const open = async (id: string, events: EventSupport | null = matrix, onSplit?: (name: string) => void) => {
  mount(id, events, onSplit);
  await userEvent.click(fold());
};

beforeEach(() => { state.text = text; });

describe("the fold, closed", () => {
  it("reads the rule in one line and shows nothing else", () => {
    mount("ledger-writes");
    expect(fold().textContent).toBe(ADVANCED + foldSummary(ruleOf("ledger-writes")));
    expect(cell("lane")).toBeNull();
  });
});

describe("what the rule matches", () => {
  it("says the lane and the server, and carries the YAML key on each label", async () => {
    await open("ledger-writes");
    expect(cell("lane").textContent).toBe(WHERE_WORD.mcp);
    expect(screen.getByTitle("tools").textContent).toBe(MATCH.lane);
    expect(cell("server").textContent).toBe("ledger-mcp");
    expect(screen.getByTitle("apps").textContent).toBe(MATCH.server);
    expect(screen.getByTitle("toolNames.deny").textContent).toBe(MATCH.tools);
  });

  it("adds a tool to an MCP rule and widens it again when the last chip goes", async () => {
    await open("ledger-writes");
    await userEvent.type(within(cell("tools")).getByRole("textbox", { name: MATCH.tools }), "reopen_period");
    await userEvent.click(screen.getByRole("button", { name: ADD_TOOL }));
    expect(now("ledger-writes").names).toEqual(["post_journal", "close_period", "reopen_period"]);
    for (const name of ["post_journal", "close_period", "reopen_period"]) {
      await userEvent.click(screen.getByRole("button", { name: removePick(name) }));
    }
    expect(now("ledger-writes").names).toBeNull();
    expect(cell("tools").textContent).toContain(EVERY_WORD.mcp);
    expect(state.text).not.toContain("toolNames");
  });

  it("adds a command pattern and keeps every comment line", async () => {
    await open("no-force-push");
    expect(cell("patterns").textContent).toContain(PATTERNS_HINT);
    await userEvent.type(within(cell("patterns")).getByRole("textbox", { name: MATCH.patterns }), "git reset --hard*");
    await userEvent.click(screen.getByRole("button", { name: ADD_PATTERN }));
    expect(now("no-force-push").names).toEqual(["git push --force*", "git reset --hard*"]);
    expect(commentLines(state.text)).toEqual(commentLines(text));
  });
});

describe("moving one call out of a rule", () => {
  it("offers the split of every name, and hands the name back", async () => {
    const onSplit = vi.fn();
    await open("ledger-writes", matrix, onSplit);
    expect(Array.from(document.querySelectorAll("[data-move-out]")).map((e) => e.getAttribute("data-move-out"))).toEqual(["post_journal", "close_period"]);
    await userEvent.click(screen.getByRole("button", { name: moveOut("close_period") }));
    expect(onSplit).toHaveBeenCalledWith("close_period");
  });

  it("offers no split where the rule names one call or the page passes none", async () => {
    await open("ledger-writes");
    expect(document.querySelector("[data-move-out]")).toBeNull();
    cleanup();
    await open("no-force-push", matrix, vi.fn());
    expect(document.querySelector("[data-move-out]")).toBeNull();
  });
});

describe("the events a rule fires on", () => {
  it("says when an MCP rule fires, and offers no picker", async () => {
    await open("ledger-writes");
    expect(cell("events").textContent).toBe(eventsWords([], "mcp"));
    expect(screen.queryByRole("button", { name: CHOOSE_EVENTS })).toBeNull();
  });

  it("reads tool.pre as the default and offers the matrix", async () => {
    await open("no-force-push");
    expect(cell("events").textContent).toContain(eventsWords(["tool.pre"], "shell"));
    expect(document.querySelector("[data-never-fires]")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: CHOOSE_EVENTS }));
    const item = await screen.findByRole("menuitemcheckbox", { name: new RegExp("^subagent.pre") });
    expect(item.textContent).toContain(eventsCoverage("subagent.pre", ["codex", "gemini"]));
    await userEvent.click(item);
    expect(now("no-force-push").events).toEqual(["tool.pre", "subagent.pre"]);
  });

  it("warns where every event the rule picked is missing on a harness", async () => {
    await open("no-subagents");
    expect((document.querySelector("[data-never-fires]") as HTMLElement).textContent).toBe(eventsNeverFire(["codex", "gemini"]));
    await userEvent.click(screen.getByRole("button", { name: CHOOSE_EVENTS }));
    await userEvent.click(await screen.findByRole("menuitemcheckbox", { name: new RegExp("^compact.pre") }));
    expect(now("no-subagents").events).toEqual(["subagent.pre", "compact.pre"]);
    expect((document.querySelector("[data-never-fires]") as HTMLElement).textContent).toBe(eventsNeverFire(["gemini"]));
  });

  it("says nothing about coverage while the matrix is unread", async () => {
    await open("no-subagents", null);
    expect(document.querySelector("[data-never-fires]")).toBeNull();
    expect(screen.queryByRole("button", { name: CHOOSE_EVENTS })).toBeNull();
  });
});

describe("what a rule requires, and what the cards cannot show", () => {
  it("says nothing is required where no rule asks for it, and says what a condition is", async () => {
    await open("no-force-push");
    expect(cell("require").textContent).toBe(REQUIRE_NONE);
    expect(document.querySelector('[data-help="' + MATCH.require + '"]')).toBeTruthy();
    expect(cell("written")).toBeNull();
  });

  it("reads the require block as written and keeps the rest as YAML", async () => {
    await open("attested-writes");
    expect(cell("require").textContent).toBe("attestation: manageddeviceCert: trueharness: claude-code");
    expect((document.querySelector("[data-written]") as HTMLElement).textContent).toBe("approve:\n  selfApproval: false\n");
  });
});
