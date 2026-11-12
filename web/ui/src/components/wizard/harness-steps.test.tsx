import * as React from "react";
import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MachineTools, NewRoleName, newRoleProblem } from "./harness-steps";
import type { DeniedTool } from "@/lib/api";
import {
  ADD_BY_NAME, HARNESS_COLUMN, HARNESS_NAME_HINT, MCP_NAME_HINT, NEW_ROLE_HINT, NEW_ROLE_TAKEN, NO_PATTERN, NO_PREFIX, NO_TOOL_SEEN, NOTHING_SEEN,
  TODAY_DENIED, harnessWords,
} from "@/lib/harness-words";
import { ADD_TOOL, NAME_BAD, removePick } from "@/lib/policy-words";

// The tool picker for a server on a person's machine and for harness
// tools, and the name of the role a policy makes. The wizard's walk is in
// the screen's own suite; this one holds the parts.

const denied = (name: string, count: number, harnesses: string[]): DeniedTool => ({ name, harnesses, count, lastSeq: 9000 + count, lastTime: "2026-11-11T09:00:00Z" });
const browser: DeniedTool[] = [denied("navigate", 4, ["claude-code"]), denied("form_input", 1, ["claude-code"])];
const harness: DeniedTool[] = [denied("LSP", 2, ["claude-code"]), denied("NotebookEdit", 1, ["claude-code", "codex"])];

// Picker holds the picked names the way the wizard's draft does, and shows
// them so a test reads what the picker handed back.
function Picker({ lane, tools, start = [], whole }: { lane: "mcp" | "harness"; tools: DeniedTool[]; start?: string[]; whole?: boolean }) {
  const [picked, setPicked] = React.useState<string[]>(start);
  return (
    <>
      <MachineTools lane={lane} denied={tools} picked={picked} whole={whole} onPick={setPicked} />
      <output data-picked>{picked.join(",")}</output>
    </>
  );
}

const picked = () => (document.querySelector("[data-picked]") as HTMLElement).textContent;
const box = (name: string) => screen.getByRole("checkbox", { name }) as HTMLInputElement;
const typeName = async (name: string) => {
  await userEvent.type(screen.getByRole("textbox", { name: ADD_BY_NAME }), name);
  await userEvent.click(screen.getByRole("button", { name: ADD_TOOL }));
};
const problem = () => document.querySelector("[data-name-problem]")?.textContent || "";

describe("the tool picker for tools Straza cannot list", () => {
  it("ticks and unticks a denied tool, and says how often it was denied", async () => {
    render(<Picker lane="mcp" tools={browser} />);
    expect((document.querySelector('[data-today="navigate"]') as HTMLElement).textContent).toBe(TODAY_DENIED(4));
    await userEvent.click(box("navigate"));
    expect(box("navigate").checked).toBe(true);
    expect(picked()).toBe("navigate");
    await userEvent.click(box("form_input"));
    await userEvent.click(box("navigate"));
    expect(box("navigate").checked).toBe(false);
    expect(picked()).toBe("form_input");
  });

  it.each([
    ["mcp" as const, NOTHING_SEEN, MCP_NAME_HINT],
    ["harness" as const, NO_TOOL_SEEN, HARNESS_NAME_HINT],
  ])("says on the %s lane that nothing was denied, and how to name a tool", (lane, empty, hint) => {
    render(<Picker lane={lane} tools={[]} />);
    expect(screen.getByText(empty)).toBeTruthy();
    expect(screen.getByText(hint)).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("adds a typed name as a chip and removes it again", async () => {
    render(<Picker lane="harness" tools={harness} />);
    await typeName("WebSearch");
    const chips = document.querySelector("[data-typed-tools]") as HTMLElement;
    expect(chips.textContent).toBe("WebSearch");
    expect(picked()).toBe("WebSearch");
    expect((screen.getByRole("textbox", { name: ADD_BY_NAME }) as HTMLInputElement).value).toBe("");
    await userEvent.click(within(chips).getByRole("button", { name: removePick("WebSearch") }));
    expect(document.querySelector("[data-typed-tools]")).toBeNull();
    expect(picked()).toBe("");
  });

  it.each([
    ["a pattern on the harness lane", "harness" as const, "Notebook*", NO_PATTERN, ""],
    ["a regular expression on the harness lane", "harness" as const, "re:^Notebook", NO_PATTERN, ""],
    ["a prefixed name on the mcp lane", "mcp" as const, "mcp__claude-in-chrome__navigate", NO_PREFIX, ""],
    ["a glob on the mcp lane", "mcp" as const, "read_*", "", "read_*"],
  ])("answers %s", async (_, lane, name, said, kept) => {
    render(<Picker lane={lane} tools={[]} />);
    await typeName(name);
    expect(problem()).toBe(said);
    expect(picked()).toBe(kept);
  });

  it("clears the problem once an accepted name is added", async () => {
    render(<Picker lane="harness" tools={[]} />);
    await typeName("LSP*");
    expect(problem()).toBe(NO_PATTERN);
    await userEvent.clear(screen.getByRole("textbox", { name: ADD_BY_NAME }));
    await typeName("LSP");
    expect(document.querySelector("[data-name-problem]")).toBeNull();
    expect(picked()).toBe("LSP");
  });

  it("ticks every tool and takes no name while the rule covers the whole server", () => {
    render(<Picker lane="mcp" tools={browser} whole />);
    for (const t of browser) {
      expect(box(t.name).checked).toBe(true);
      expect(box(t.name).disabled).toBe(true);
    }
    expect(screen.queryByRole("textbox", { name: ADD_BY_NAME })).toBeNull();
    expect(screen.queryByText(MCP_NAME_HINT)).toBeNull();
  });

  it.each([
    ["harness" as const, harness, true],
    ["mcp" as const, browser, false],
  ])("shows the Harness column on the harness lane alone, here the %s lane", (lane, tools, shown) => {
    render(<Picker lane={lane} tools={tools} />);
    expect(!!screen.queryByRole("columnheader", { name: HARNESS_COLUMN })).toBe(shown);
    const row = document.querySelector('[data-tool="' + tools[tools.length - 1].name + '"]') as HTMLElement;
    expect(row.textContent?.includes(harnessWords(tools[tools.length - 1].harnesses))).toBe(shown);
  });
});

describe("the role a policy makes", () => {
  it.each([
    ["claude-in-chrome-users", [], ""],
    ["claude-in-chrome-users", ["dev-tools"], ""],
    ["", [], "bad"],
    ["Chrome Users", [], "bad"],
    ["-users", [], "bad"],
    ["dev-tools", ["dev-tools"], "taken"],
  ])("answers %s against %j with %j", (name, taken, said) => {
    expect(newRoleProblem(name, taken)).toBe(said);
  });

  it.each([
    ["a name a role carries", "dev-tools", NEW_ROLE_TAKEN, "taken"],
    ["a name outside the grammar", "Chrome Users", NAME_BAD, "bad"],
  ])("says why it cannot make %s", (_, name, said, key) => {
    render(<NewRoleName name={name} taken={["dev-tools"]} onName={() => undefined} />);
    const line = document.querySelector("[data-role-problem]") as HTMLElement;
    expect(line.textContent).toBe(said);
    expect(line.getAttribute("data-role-problem")).toBe(key);
    expect(screen.getByText(NEW_ROLE_HINT)).toBeTruthy();
  });

  it("says nothing about a free name or an empty one", () => {
    const { rerender } = render(<NewRoleName name="claude-in-chrome-users" taken={["dev-tools"]} onName={() => undefined} />);
    expect(document.querySelector("[data-role-problem]")).toBeNull();
    rerender(<NewRoleName name="" taken={["dev-tools"]} onName={() => undefined} />);
    expect(document.querySelector("[data-role-problem]")).toBeNull();
  });
});
