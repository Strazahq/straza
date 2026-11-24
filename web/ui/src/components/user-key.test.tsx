import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UserKey, shortPrint } from "./user-key";
import { type KeyPosture, setKey } from "@/lib/api";
import { notify } from "@/lib/notify";
import { KEY_HINT, KEY_LABEL, KEY_MISSING, NO_KEY_LINE, keySetToast } from "@/lib/user-words";
import { absTime } from "@/lib/words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), setKey: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const FP = "sha256:34a6617d0000000000000000000000000000000000004174837";
const registered: KeyPosture = { user_id: "u", registered: true, fingerprint: FP, created: "2026-11-08T09:00:00Z" };
const none: KeyPosture = { user_id: "u", registered: false };
const KEY = "bo9pMuhh0gS0qLSCTMmpEJMzHbG3uJ6rXR2n5WZP0aM=";

function draw(posture: KeyPosture) {
  const props = { onRevoke: vi.fn(), onChanged: vi.fn() };
  render(<UserKey id="u-nina" name="nina" posture={posture} {...props} />);
  return props;
}

describe("the assertion key", () => {
  beforeEach(() => vi.clearAllMocks());

  it("cuts a fingerprint to its head, an ellipsis and its tail, and keeps a short one whole", () => {
    expect(shortPrint(FP)).toBe("sha256:34a6617d…4174837");
    expect(shortPrint("SHA256:9f2c")).toBe("SHA256:9f2c");
  });

  it("shows a registered key with its day, its fingerprint, Rotate key and Revoke key", async () => {
    const { onRevoke } = draw(registered);
    const fact = document.querySelector("[data-key-fact]") as HTMLElement;
    expect(document.querySelector("[data-key=registered]")?.getAttribute("data-tone")).toBe("plain");
    expect(fact.textContent).toBe("registeredsince 8 Nov 2026sha256:34a6617d…4174837Rotate keyRevoke key");
    expect(screen.getByText("since 8 Nov 2026").getAttribute("title")).toBe(absTime(registered.created));
    expect(screen.getByText("sha256:34a6617d…4174837").getAttribute("title")).toBe(FP);
    await userEvent.click(screen.getByRole("button", { name: "Revoke key" }));
    expect(onRevoke).toHaveBeenCalled();
  });

  it("registers a key for an agent that has none, asking for the key at the field", async () => {
    vi.mocked(setKey).mockResolvedValue(registered);
    const { onChanged } = draw(none);
    expect(document.querySelector("[data-key='no key']")?.getAttribute("data-tone")).toBe("warn");
    expect(screen.getByText(NO_KEY_LINE)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "Register key" }));
    expect(screen.getByText(KEY_HINT)).toBeTruthy();
    (document.activeElement as HTMLElement).blur();
    await userEvent.click(screen.getByRole("button", { name: "Register key" }));
    expect(screen.getByText(KEY_MISSING)).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText(KEY_LABEL));
    await userEvent.type(screen.getByLabelText(KEY_LABEL), KEY);
    await userEvent.click(screen.getByRole("button", { name: "Register key" }));
    await waitFor(() => expect(setKey).toHaveBeenCalledWith("u-nina", KEY));
    expect(notify.ok).toHaveBeenCalledWith(keySetToast("nina"));
    expect(onChanged).toHaveBeenCalled();
  });

  it("rotates a registered key, and shows a refusal in the form", async () => {
    vi.mocked(setKey).mockRejectedValue(Object.assign(new Error("public_key must be 32 bytes"), { status: 400, unreachable: false }));
    draw(registered);
    await userEvent.click(screen.getByRole("button", { name: "Rotate key" }));
    expect(document.querySelector("[data-key-form=rotate]")).not.toBeNull();
    await userEvent.type(screen.getByLabelText(KEY_LABEL), "short");
    await userEvent.click(screen.getAllByRole("button", { name: "Rotate key" }).at(-1) as HTMLElement);
    expect((await screen.findByRole("alert")).textContent).toBe("Rotate key refused: public_key must be 32 bytes. Fix what it names, then try again.");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(document.querySelector("[data-key-form]")).toBeNull();
  });

  it.each<[string, KeyPosture, string]>([
    ["Register key", none, "Register key"],
    ["Rotate key", registered, "Rotate key"],
  ])("puts the focus on %s after Cancel hides its form", async (_name, posture, verb) => {
    draw(posture);
    await userEvent.click(screen.getByRole("button", { name: verb }));
    expect(document.activeElement).toBe(screen.getByLabelText(KEY_LABEL));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(document.querySelector("[data-key-form]")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: verb }));
  });
});
