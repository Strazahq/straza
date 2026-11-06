import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WhatRan } from "./what-ran";
import { TooltipProvider } from "@/components/ui/tooltip";
import { COPIED } from "@/lib/audit-words";
import { notify } from "@/lib/notify";
import { ARGS_NONE, FILE_NOTE } from "@/lib/what-ran-words";

vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const HEREDOC = "python - <<'EOF'\nprint('a')\nprint('b')\nEOF\ngrep -n x src/app.ts | head -4";
const show = (data: Record<string, unknown>) => render(<TooltipProvider><WhatRan data={data} /></TooltipProvider>);
const steps = () => [...document.querySelectorAll("[data-step-line]")].map((e) => e.textContent);
const connectors = () => [...document.querySelectorAll("[data-connector]")].map((e) => e.textContent);
const rawLines = () => [...document.querySelectorAll("[data-raw] [data-line]")].map((e) => (e.lastElementChild as HTMLElement).textContent);

describe("What ran", () => {
  beforeEach(() => {
    window.localStorage.clear();
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(() => Promise.resolve()) } });
  });

  it("opens Readable on a shell command: its commands, how each leads on, and its here-document", () => {
    show({ tool: "shell.exec", command: HEREDOC, workspace: "/work/game" });
    expect((document.querySelector("[data-what-ran-meta]") as HTMLElement).textContent).toBe("shell.exec in /work/game");
    expect(document.querySelector("[data-summary]")?.textContent).toBe("3 commands · 1 inline script");
    expect(steps()).toEqual(["python - <<'EOF'", "grep -n x src/app.ts", "head -4"]);
    expect(connectors()).toEqual(["↵then", "|its output goes into"]);
    const doc = document.querySelector("[data-heredoc]") as HTMLElement;
    expect(doc.getAttribute("data-heredoc")).toBe("python");
    expect(doc.textContent).toContain("Given to python as its input");
    expect(doc.textContent).toContain("2 lines");
    expect(doc.textContent).toContain("ends at EOF");
    expect([...doc.querySelectorAll("[data-line]")].map((e) => (e.lastElementChild as HTMLElement).textContent)).toEqual(["print('a')", "print('b')"]);
    expect([...document.querySelectorAll("[data-paths-named] span.font-mono")].map((e) => e.textContent)).toEqual(["src/app.ts"]);
  });

  it("says redirections in words beside them", () => {
    show({ tool: "shell.exec", command: "npx vitest run 2>&1 | tail -30" });
    expect(document.querySelector("[data-redir=\"2>&\"]")?.textContent).toBe("2>&1errors into output");
  });

  it("shows Raw as the command exactly as recorded, line by line, and remembers the pick", async () => {
    const first = show({ tool: "shell.exec", command: HEREDOC });
    await userEvent.click(screen.getByRole("tab", { name: "Raw" }));
    expect(rawLines()).toEqual(HEREDOC.split("\n"));
    expect(document.querySelector("[data-raw]")?.textContent).toContain("5 lines, " + HEREDOC.length + " characters, exactly as recorded");
    await userEvent.click(screen.getByRole("switch", { name: "Wrap lines" }));
    expect(document.querySelector("[data-raw] [data-code]")?.getAttribute("data-wrap")).toBe("off");
    first.unmount();
    show({ tool: "shell.exec", command: "ls" });
    expect(screen.getByRole("tab", { name: "Raw" }).getAttribute("aria-selected")).toBe("true");
  });

  it("marks hidden characters in both views and names each with its line", async () => {
    show({ tool: "shell.exec", command: "git commit -m \"Bump it​\"" });
    const notice = document.querySelector("[data-notice=\"hidden\"]") as HTMLElement;
    expect(notice.textContent).toBe("This command holds 2 characters that do not show on screen.U+00A0 no-break space on line 1, U+200B zero-width space on line 1. They can make the text read differently from what ran. Both views mark them.");
    expect([...document.querySelectorAll("[data-step-line] [data-hidden-char]")].map((e) => e.textContent)).toEqual(["U+00A0", "U+200B"]);
    await userEvent.click(screen.getByRole("tab", { name: "Raw" }));
    expect([...document.querySelectorAll("[data-raw] [data-hidden-char]")].map((e) => e.textContent)).toEqual(["U+00A0", "U+200B"]);
  });

  it("says how much of a cut command Straza kept, and ends the open here-document where the record does", () => {
    const hash = "bd53865339da0eb15964543359e8be8955d56b4ecffbe4f1945a624d3b29c49b";
    show({ tool: "shell.exec", command: "cat > f.json <<'EOF'\n{\"a\":1,... (921600 bytes, sha256 " + hash + ")" });
    expect(document.querySelector("[data-notice=\"cut\"]")?.textContent).toBe("Straza kept the first 28 characters of this command.The whole command was 921,600 bytes. Its SHA-256 is " + hash + ", so a copy the client kept can be checked against it.");
    const doc = document.querySelector("[data-heredoc]") as HTMLElement;
    expect(doc.getAttribute("data-heredoc")).toBe("json");
    expect(doc.textContent).toContain("cut here by the record bound");
    expect(doc.querySelector("[data-cut-end]")?.textContent).toBe("… the record keeps no more of this command");
  });

  it("reads the client's own cut of a long command from its fields", () => {
    const hash = "a4344ec075b85974ac02a0f87dd0179284ec3074a832468f4559979cf0d8b608";
    show({ tool: "shell.exec", command: "cat > f.json <<'EOF'\n{\"a\":1,", commandTruncated: true, commandHash: "sha256:" + hash });
    expect(document.querySelector("[data-notice=\"cut\"]")?.textContent).toBe("Straza kept the first 28 characters of this command.The whole command's SHA-256 is " + hash + ", so a copy the client kept can be checked against it.");
    expect(document.querySelector("[data-cut-end]")).toBeTruthy();
  });

  it("says when a command does not read, and never guesses at its steps", () => {
    show({ tool: "shell.exec", command: "printf 'Built %s\\n' \"$(git rev-parse HEAD)" });
    expect(document.querySelector("[data-unreadable]")?.textContent).toBe("Straza cannot split this command into steps: a \" quote opens and never closes. It is shown as it ran.");
    expect(document.querySelector("[data-unreadable-text]")?.textContent).toContain("printf 'Built %s\\n' \"$(git rev-parse HEAD)");
    expect(document.querySelector("[data-step-line]")).toBeNull();
  });

  it("lays a gateway call out as its arguments, one row each, and Raw as the JSON", async () => {
    show({ tool: "mcp.call", app: "ghapp", toolName: "pr_create", arguments: JSON.stringify({ repo: "Strazahq/docs", title: "Docs", draft: false, body: "line one\nline two" }) });
    expect((document.querySelector("[data-what-ran-meta]") as HTMLElement).textContent).toBe("ghapp.pr_create");
    const cells = [...document.querySelectorAll("[data-args] tr")].map((tr) => [...tr.querySelectorAll("td")].map((td) => td.textContent));
    expect(cells).toEqual([["repo", "Strazahq/docs"], ["title", "Docs"], ["draft", "false"], ["body", "line one\nline two"]]);
    await userEvent.click(screen.getByRole("tab", { name: "Raw" }));
    expect(rawLines()[0]).toBe("{");
    expect(rawLines()).toContain("  \"repo\": \"Strazahq/docs\",");
  });

  it("says a gateway call holds no arguments when it has none", () => {
    show({ app: "midpoint", toolName: "assign_role" });
    expect(screen.getByText(ARGS_NONE)).toBeTruthy();
  });

  it("names a file tool's paths from its workspace and says the record holds no change", () => {
    show({ tool: "file.write", paths: ["/work/game/src/area.ts"], workspace: "/work/game" });
    expect(document.querySelector("[data-paths]")?.textContent).toBe("Writes src/area.ts" + FILE_NOTE);
  });

  it("renders nothing for a record that holds no call", () => {
    const { container } = show({ effect: "allow" });
    expect(container.textContent).toBe("");
  });

  it("copies the command whole", async () => {
    show({ tool: "shell.exec", command: HEREDOC });
    await userEvent.click(screen.getByRole("button", { name: "Copy command" }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(HEREDOC);
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith(COPIED));
  });
});
