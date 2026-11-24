import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChangeChip, Chip, OutcomeChip, Pill, StatusChip, Tag, ToolChip, type Tone } from "./chip";
import type { Change, Outcome, Status } from "@/lib/chip-words";

// One chip for every outcome and state: each tone paints its border, fill
// and text, and each word carries the data attribute the screens read.

describe("Chip", () => {
  it.each<[Tone, string[]]>([
    ["ok", ["border-ok", "from-ok-bg", "to-ok-bg", "bg-background", "text-ok"]],
    ["warn", ["border-warn", "from-warn-bg", "to-warn-bg", "bg-background", "text-warn"]],
    ["danger", ["border-danger", "from-danger-bg", "to-danger-bg", "bg-background", "text-danger"]],
    ["accent", ["border-link", "from-accent-bg", "to-accent-bg", "bg-background", "text-link"]],
    ["teal", ["border-teal", "from-teal-bg", "to-teal-bg", "bg-background", "text-teal"]],
    ["unknown", ["border-unknown", "from-unknown-bg", "to-unknown-bg", "bg-background", "text-unknown"]],
    ["plain", ["border-input", "bg-secondary", "text-foreground"]],
  ])("paints the %s tone", (tone, classes) => {
    render(<Chip tone={tone}>word</Chip>);
    const chip = screen.getByText("word");
    for (const c of classes) expect(chip.classList).toContain(c);
    expect(chip.classList).toContain("text-caption");
  });

  it("hands a data attribute and a title to its element", () => {
    render(<Chip tone="plain" title="why" data-origin="idm">idm</Chip>);
    const chip = screen.getByTitle("why");
    expect(chip.getAttribute("data-origin")).toBe("idm");
    expect(chip.textContent).toBe("idm");
  });
});

describe("OutcomeChip", () => {
  it.each<[Outcome, string, string]>([
    ["allowed", "allowed", "ok"],
    ["approval", "needs approval", "warn"],
    ["denied", "denied", "danger"],
    ["none", "no access", "plain"],
  ])("draws %s as its word in its tone", (outcome, word, tone) => {
    render(<OutcomeChip outcome={outcome} />);
    const chip = screen.getByText(word);
    expect(chip.getAttribute("data-outcome")).toBe(outcome);
    expect(chip.getAttribute("data-tone")).toBe(tone);
  });

  it("draws a count before the word", () => {
    render(<OutcomeChip outcome="denied" count={1200} />);
    expect(screen.getByText("1,200 denied").getAttribute("data-outcome")).toBe("denied");
  });
});

describe("StatusChip", () => {
  it.each<[Status, string]>([
    ["disabled", "plain"],
    ["locked", "danger"],
    ["revoked", "danger"],
    ["closed", "plain"],
  ])("draws %s as a chip", (status, tone) => {
    render(<StatusChip status={status} />);
    const chip = screen.getByText(status);
    expect(chip.getAttribute("data-status-word")).toBe(status);
    expect(chip.getAttribute("data-tone")).toBe(tone);
  });

  it("draws active as a dot and a word with no border", () => {
    render(<StatusChip status="active" data-user-status="active" />);
    const word = screen.getByText("active");
    expect(word.getAttribute("data-status-word")).toBe("active");
    expect(word.getAttribute("data-user-status")).toBe("active");
    expect(Array.from(word.classList).some((c) => c.startsWith("border"))).toBe(false);
  });
});

describe("ChangeChip", () => {
  it.each<[Change, string, string]>([
    ["new", "new", "accent"],
    ["changed", "changed", "plain"],
    ["removed", "removed", "danger"],
    ["off", "turned off", "plain"],
    ["on", "turned on", "accent"],
  ])("draws %s as its word in its tone", (change, word, tone) => {
    render(<ChangeChip change={change} />);
    const chip = screen.getByText(word);
    expect(chip.getAttribute("data-change")).toBe(change);
    expect(chip.getAttribute("data-tone")).toBe(tone);
  });
});

describe("Tag, Pill and ToolChip", () => {
  it("draws a tag on the card fill, and an agent's tag in teal", () => {
    render(<><Tag title="a role">dev</Tag><Tag agent>bot-1</Tag></>);
    expect(screen.getByTitle("a role").classList).toContain("bg-card");
    expect(screen.getByText("bot-1").classList).toContain("bg-teal-bg");
  });

  it("draws a pill round on the pill fill", () => {
    render(<Pill title="three lines">3 lines</Pill>);
    const pill = screen.getByTitle("three lines");
    expect(pill.classList).toContain("rounded-pill");
    expect(pill.classList).toContain("bg-pill");
  });

  it("marks a tool chip with its outcome, and none keeps it plain", () => {
    render(<><ToolChip outcome="denied" title="Removes a file.">rm</ToolChip><ToolChip outcome="none">ls</ToolChip></>);
    // The name sits in a span of its own inside the chip, so a long one is
    // cut there and the icon stays.
    const rm = screen.getByText("rm").closest("[data-outcome]") as HTMLElement;
    expect(screen.getByText("rm").getAttribute("title")).toBe("Removes a file.");
    expect(rm.getAttribute("title")).toBe("Removes a file.");
    expect(rm.getAttribute("data-outcome")).toBe("denied");
    expect(rm.classList).toContain("bg-danger-bg");
    expect(screen.getByText("rm").classList).toContain("truncate");
    expect(screen.getByText("ls").hasAttribute("data-outcome")).toBe(false);
    expect(screen.getByText("ls").classList).toContain("bg-pill");
    // A chip with no title of its own says its whole name on hover.
    expect(screen.getByText("ls").getAttribute("title")).toBe("ls");
  });
});
