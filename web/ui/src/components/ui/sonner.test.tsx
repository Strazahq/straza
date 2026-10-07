import { describe, expect, it } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { Toaster } from "./sonner";

// Every sheet of the console opens on the right at full height, with its X
// at the top right and its buttons at the bottom right, so a toast sits at
// the bottom left, clear of both.

describe("the toaster", () => {
  it("shows its toasts at the bottom left", async () => {
    render(<Toaster />);
    act(() => { toast("Saved."); });
    await waitFor(() => expect(document.querySelector("[data-sonner-toaster]")).not.toBeNull());
    const list = document.querySelector("[data-sonner-toaster]") as HTMLElement;
    expect([list.getAttribute("data-y-position"), list.getAttribute("data-x-position")]).toEqual(["bottom", "left"]);
  });
});
