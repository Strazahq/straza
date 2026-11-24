import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DraftYaml } from "./draft-yaml";
import type { Draft } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import { FILE_MASKED, YAML_COPIED } from "@/lib/drafts-words";
import { DETAIL, GITHUB_APP, WRITERS_SET } from "@/test/drafts-fixture";

// The YAML sheet: one tab per item with a file, opened on the card's own,
// the document as the draft holds it, which Copy copies, and a sub line
// that says which state of the document it is.

vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn() }));

const mount = (draft: Draft = DETAIL.draft, shown = "PolicySet/github-writers-access", onShow = vi.fn(), onClose = vi.fn()) => {
  render(<DraftYaml draft={draft} open shown={shown} onShow={onShow} onClose={onClose} onClosed={() => {}} />);
  return { onShow, onClose };
};

describe("the YAML sheet", () => {
  beforeEach(() => vi.resetAllMocks());

  it("titles itself with the file it shows, and holds one tab per item with a file", () => {
    mount();
    const sheet = screen.getByRole("dialog");
    expect(within(sheet).getByRole("heading").textContent).toBe("github-writers-access.yaml");
    expect(within(sheet).getAllByRole("tab").map((t) => t.textContent)).toEqual(["github.yaml", "demo-tools.yaml", "github-readers.yaml", "demo-tools-readers.yaml", "github-writers-access.yaml", "demo-tools-readers-access.yaml"]);
    expect(within(sheet).getByRole("tab", { selected: true }).textContent).toBe("github-writers-access.yaml");
    expect(sheet.querySelector('[data-yaml="PolicySet/github-writers-access"]')?.textContent).toBe(WRITERS_SET);
    expect(sheet.textContent).not.toContain(FILE_MASKED);
  });

  it("says a server's file masks the values the server keeps secret", () => {
    mount(DETAIL.draft, "App/github");
    const sheet = screen.getByRole("dialog");
    expect(sheet.querySelector('[data-yaml="App/github"]')?.textContent).toBe(GITHUB_APP);
    expect(sheet.textContent).toContain(FILE_MASKED);
  });

  it("moves to another tab, copies the file that shows, and closes", async () => {
    const { onShow, onClose } = mount();
    await userEvent.click(screen.getByRole("tab", { name: "github.yaml" }));
    expect(onShow).toHaveBeenCalledWith("App/github");
    await userEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(copyText).toHaveBeenCalledWith(WRITERS_SET, YAML_COPIED);
    await userEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  it.each([
    ["open", "As it will be published."],
    ["published", "As it was published."],
    ["discarded", "As it was drafted."],
    ["expired", "As it was drafted."],
  ] as const)("says which state of the document a draft now %s shows", (state, sub) => {
    mount({ ...DETAIL.draft, state });
    expect(within(screen.getByRole("dialog")).getByText(sub)).toBeTruthy();
  });

  it("keeps a server and a role of one name apart", () => {
    const draft = { ...DETAIL.draft, items: [DETAIL.draft.items[0], { ...DETAIL.draft.items[2], name: "github" }] };
    mount(draft, "Role/github");
    expect(screen.getAllByRole("tab").map((t) => t.getAttribute("aria-selected"))).toEqual(["false", "true"]);
  });
});
