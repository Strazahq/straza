import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PolicyFacts } from "./policy-facts";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { RoleRow } from "@/lib/api";
import { type Doc, docText, openDoc, readSet } from "@/lib/policy-model";
import { APPLY_TO_PAGE, EVERYONE, EVERYONE_MEANS, FACT, NOTHING_TICKED, PRIORITY_LINE, RECORDING_CHOICE, TOUCHED } from "@/lib/policy-words";

const text = `apiVersion: straza.dev/v1beta1
kind: PolicySet
metadata:
  name: dev-guardrails
spec:
  priority: 150
  match:
    roles: [dev-tools]
  capture:
    conversations: true
    mode: verbatim
  rules: []
`;

const roles: RoleRow[] = [
  { id: "r1", name: "dev-tools", kind: "application", holder_count: 2 },
  { id: "r2", name: "sre-tools", kind: "application", holder_count: 3 },
  { id: "r3", name: "sec-approvers", kind: "approver", holder_count: 2 },
];

// mount renders the facts over a real document, so an edit is asserted on
// the text that would be published rather than on the call.
const mount = () => {
  const edits: { touched: string; text: string }[] = [];
  const onEdit = vi.fn((touched: string, fn: (doc: Doc) => void) => {
    const doc = openDoc(text);
    fn(doc);
    edits.push({ touched, text: docText(doc) });
  });
  render(<TooltipProvider><PolicyFacts view={readSet(openDoc(text))} roles={roles} onEdit={onEdit} /></TooltipProvider>);
  return edits;
};

const fact = (key: string) => document.querySelector('[data-fact="' + key + '"]') as HTMLElement;
const state = (sheet: HTMLElement) => (sheet.querySelector("[data-applies-state]") as HTMLElement).textContent;
const openSheet = async (key: string) => {
  await userEvent.click(within(fact(key)).getByRole("button", { name: FACT.change }));
  return screen.findByRole("dialog");
};

describe("the three facts of a policy", () => {
  it("shows who it applies to, whether it records, and its priority with the rule that reads it", () => {
    mount();
    expect(fact("applies").textContent).toContain("dev-tools");
    expect(fact("recording").textContent).toContain("word for word");
    expect(fact("priority").textContent).toContain("150");
    expect(fact("priority").textContent).toContain(PRIORITY_LINE);
  });

  it("leads with Everyone, then every application role with its holders", async () => {
    mount();
    const sheet = await openSheet("applies");
    const boxes = within(sheet).getAllByRole("checkbox");
    expect(boxes.map((b) => b.getAttribute("aria-label"))).toEqual([EVERYONE, "dev-tools", "sre-tools"]);
    expect(sheet.textContent).toContain("2 holders");
    expect((boxes[0] as HTMLInputElement).checked).toBe(false);
    expect((boxes[1] as HTMLInputElement).checked).toBe(true);
    expect(state(sheet)).toBe("");
  });

  it("clears the roles when Everyone is ticked, and writes a set that names none", async () => {
    const edits = mount();
    const sheet = await openSheet("applies");
    await userEvent.click(within(sheet).getByRole("checkbox", { name: EVERYONE }));
    expect((within(sheet).getByRole("checkbox", { name: "dev-tools" }) as HTMLInputElement).checked).toBe(false);
    expect(state(sheet)).toContain(EVERYONE_MEANS);
    await userEvent.click(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }));
    await waitFor(() => expect(edits.length).toBe(1));
    expect(edits[0].touched).toBe(TOUCHED.applies);
    expect(edits[0].text).not.toContain("roles:");
  });

  it("clears Everyone when a role is ticked", async () => {
    const edits = mount();
    const sheet = await openSheet("applies");
    await userEvent.click(within(sheet).getByRole("checkbox", { name: EVERYONE }));
    await userEvent.click(within(sheet).getByRole("checkbox", { name: "sre-tools" }));
    expect((within(sheet).getByRole("checkbox", { name: EVERYONE }) as HTMLInputElement).checked).toBe(false);
    expect(state(sheet)).toBe("");
    await userEvent.click(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }));
    await waitFor(() => expect(edits.length).toBe(1));
    expect(edits[0].text).toContain("roles: [sre-tools]");
  });

  it("refuses to apply while nothing is ticked, and says what to pick", async () => {
    const edits = mount();
    const sheet = await openSheet("applies");
    await userEvent.click(within(sheet).getByRole("checkbox", { name: "dev-tools" }));
    const apply = within(sheet).getByRole("button", { name: APPLY_TO_PAGE });
    expect(apply.getAttribute("aria-disabled")).toBe("true");
    expect((sheet.querySelector("[data-nothing-ticked]") as HTMLElement).textContent).toBe(NOTHING_TICKED);
    await userEvent.click(apply);
    expect(edits.length).toBe(0);
    await userEvent.click(within(sheet).getByRole("checkbox", { name: EVERYONE }));
    expect(sheet.querySelector("[data-nothing-ticked]")).toBeNull();
    expect(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }).hasAttribute("aria-disabled")).toBe(false);
  });

  it("writes the roles the sheet ticked", async () => {
    const edits = mount();
    const sheet = await openSheet("applies");
    await userEvent.click(within(sheet).getByRole("checkbox", { name: "sre-tools" }));
    await userEvent.click(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }));
    await waitFor(() => expect(edits.length).toBe(1));
    expect(edits[0].touched).toBe(TOUCHED.applies);
    expect(edits[0].text).toContain("roles: [dev-tools, sre-tools]");
  });

  it("turns recording off, and on with secrets masked", async () => {
    const edits = mount();
    let sheet = await openSheet("recording");
    await userEvent.click(within(sheet).getByRole("radio", { name: RECORDING_CHOICE.off }));
    await userEvent.click(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }));
    await waitFor(() => expect(edits.length).toBe(1));
    expect(edits[0].touched).toBe(TOUCHED.recording);
    expect(edits[0].text).not.toContain("conversations");

    sheet = await openSheet("recording");
    await userEvent.click(within(sheet).getByRole("radio", { name: RECORDING_CHOICE.masked }));
    await userEvent.click(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }));
    await waitFor(() => expect(edits.length).toBe(2));
    expect(edits[1].text).toContain("mode: redact");
  });

  it("writes a new priority", async () => {
    const edits = mount();
    const sheet = await openSheet("priority");
    const input = within(sheet).getByLabelText(FACT.priority);
    await userEvent.clear(input);
    await userEvent.type(input, "300");
    await userEvent.click(within(sheet).getByRole("button", { name: APPLY_TO_PAGE }));
    await waitFor(() => expect(edits.length).toBe(1));
    expect(edits[0].touched).toBe(TOUCHED.priority);
    expect(edits[0].text).toContain("priority: 300");
  });

  it("leaves the document alone when the sheet is cancelled", async () => {
    const edits = mount();
    const sheet = await openSheet("priority");
    await userEvent.click(within(sheet).getByRole("button", { name: "Cancel" }));
    expect(edits.length).toBe(0);
  });
});
