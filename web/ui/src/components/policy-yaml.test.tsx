import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyYaml } from "./policy-yaml";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError, validatePolicy } from "@/lib/api";
import { notify } from "@/lib/notify";
import { COPIED, COPY, COPY_REFUSED, EDITOR_LABEL, PARSE_FAILED, SHOW_DIFF, VALIDATE } from "@/lib/policy-words";

const stored = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-guardrails
spec:
  priority: 150
`;

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), validatePolicy: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const mount = (text = stored, onChange = vi.fn()) => {
  const view = render(<TooltipProvider><PolicyYaml text={text} stored={stored} onChange={onChange} /></TooltipProvider>);
  return { ...view, onChange };
};

const editor = () => screen.getByRole("textbox", { name: EDITOR_LABEL }) as HTMLTextAreaElement;

beforeEach(() => {
  vi.mocked(validatePolicy).mockReset();
  vi.mocked(notify.ok).mockReset();
  vi.mocked(notify.failed).mockReset();
});

describe("the YAML tab", () => {
  it("shows the text with a line number for every line", () => {
    mount();
    expect(editor().value).toBe(stored);
    const gutter = document.querySelector('[aria-label="Line numbers"]') as HTMLElement;
    expect(Array.from(gutter.children).map((c) => c.textContent)).toEqual(["1", "2", "3", "4", "5", "6", "7"]);
  });

  it("hands the text to the page when the editor loses focus", async () => {
    const { onChange } = mount();
    fireEvent.change(editor(), { target: { value: stored + "  escape: []\n" } });
    fireEvent.blur(editor());
    expect(onChange).toHaveBeenCalledWith(stored + "  escape: []\n");
  });

  it("prints the server's reading of a text that is valid, with its advisories", async () => {
    vi.mocked(validatePolicy).mockResolvedValue({
      ok: true,
      rules: 11,
      matchRoles: ["dev-tools"],
      capture: "verbatim",
      advisories: [{ code: "events-never-fire", rule: "no-rm-rf", text: "subagent.pre never fires on Gemini." }],
    });
    mount();
    await userEvent.click(screen.getByRole("button", { name: VALIDATE }));
    const ok = await waitFor(() => {
      const el = document.querySelector('[data-verdict="ok"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(ok.textContent).toBe("Valid, checked by the server just now: 11 rules, matches dev-tools, records word for word.");
    expect((document.querySelector('[data-advisory="events-never-fire"]') as HTMLElement).textContent).toBe("no-rm-rf: subagent.pre never fires on Gemini.");
  });

  it("prints the server's own sentence and marks the line it names", async () => {
    vi.mocked(validatePolicy).mockRejectedValue(new ApiError("bad indentation at line 4, column 3", 400));
    mount();
    await userEvent.click(screen.getByRole("button", { name: VALIDATE }));
    const bad = await waitFor(() => {
      const el = document.querySelector('[data-verdict="bad"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(bad.textContent).toBe(PARSE_FAILED + ": bad indentation at line 4, column 3");
    const gutter = document.querySelector('[aria-label="Line numbers"]') as HTMLElement;
    expect(gutter.children[3].className).toContain("text-danger");
  });

  it("offers the diff only once the text differs, and paints what moved", async () => {
    mount();
    expect(screen.queryByLabelText(SHOW_DIFF)).toBeNull();
    fireEvent.change(editor(), { target: { value: stored.replace("priority: 150", "priority: 999") } });
    fireEvent.blur(editor());
    const toggle = await screen.findByLabelText(SHOW_DIFF);
    await userEvent.click(toggle);
    const removed = document.querySelectorAll('[data-diff="remove"]');
    const added = document.querySelectorAll('[data-diff="add"]');
    expect(Array.from(removed).map((e) => e.textContent)).toEqual(["  priority: 150"]);
    expect(Array.from(added).map((e) => e.textContent)).toEqual(["  priority: 999"]);
    expect(screen.queryByRole("textbox", { name: EDITOR_LABEL })).toBeNull();
  });

  it("says when the browser refused the copy", async () => {
    const writeText = vi.fn(() => Promise.reject(new Error("refused")));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mount();
    await userEvent.click(screen.getByRole("button", { name: COPY }));
    expect(writeText).toHaveBeenCalledWith(stored);
    await waitFor(() => expect(vi.mocked(notify.failed)).toHaveBeenCalledWith(COPY_REFUSED));
  });

  it("says when the text is on the clipboard", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    mount();
    await userEvent.click(screen.getByRole("button", { name: COPY }));
    await waitFor(() => expect(vi.mocked(notify.ok)).toHaveBeenCalledWith(COPIED));
  });
});
