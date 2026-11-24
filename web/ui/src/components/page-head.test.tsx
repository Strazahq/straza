import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PageHead } from "./page-head";
import { navigate } from "@/lib/router";

vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));

// The page head's breadcrumb names the pages above this one as links, a
// back link above it leads to the last of them, and a tile leads the title
// of a page that is about one object.

describe("PageHead", () => {
  beforeEach(() => vi.clearAllMocks());

  it("draws Straza and the page when it has no crumb, and no back link", () => {
    render(<PageHead label="Users" description="People and agents." />);
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(crumbs).getAllByRole("link").map((a) => a.textContent)).toEqual(["Straza"]);
    expect(within(crumbs).getByText("Users").getAttribute("aria-current")).toBe("page");
    expect(document.querySelector("[data-back-link]")).toBeNull();
    expect(screen.queryByRole("link", { name: /^Back to/ })).toBeNull();
  });

  it("leads the head with a back link to the last crumb, above the breadcrumb", () => {
    render(<PageHead label="alice" crumbs={[{ label: "Users", to: "users" }]} description="" />);
    const back = screen.getByRole("link", { name: "Back to Users" });
    expect(back.hasAttribute("data-back-link")).toBe(true);
    expect(back.getAttribute("href")).toMatch(/\/users$/);
    expect(back.textContent).toBe("Users");
    const head = document.querySelector("[data-page-head]") as HTMLElement;
    expect(head.firstElementChild).toBe(back);
    expect(back.nextElementSibling).toBe(screen.getByRole("navigation", { name: "Breadcrumb" }));
  });

  it("opens the list on a plain click of the back link and leaves a modifier click to the browser", async () => {
    render(<PageHead label="dev-tools" crumbs={[{ label: "Roles", to: "roles" }]} description="" />);
    const back = screen.getByRole("link", { name: "Back to Roles" });
    // The browser opens a modifier click in a new tab, which jsdom cannot,
    // so the test keeps it from trying once the head has had its say.
    const hold = (e: Event) => e.preventDefault();
    document.addEventListener("click", hold);
    fireEvent.click(back, { metaKey: true });
    fireEvent.click(back, { ctrlKey: true });
    document.removeEventListener("click", hold);
    expect(navigate).not.toHaveBeenCalled();
    await userEvent.click(back);
    expect(navigate).toHaveBeenCalledWith("roles", undefined);
  });

  it("links each page above this one and opens it on a click", async () => {
    render(<PageHead label="alice" crumbs={[{ label: "Users", to: "users" }]} description="" />);
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    const link = within(crumbs).getByRole("link", { name: "Users" });
    expect(link.getAttribute("href")).toMatch(/\/users$/);
    await userEvent.click(link);
    expect(navigate).toHaveBeenCalledWith("users", undefined);
    expect(within(crumbs).getByText("alice").getAttribute("aria-current")).toBe("page");
  });

  it("leads the title with the tile it is given", () => {
    render(<PageHead label="alice" tile={<span data-testid="tile" />} description="" />);
    const tile = screen.getByTestId("tile");
    expect(tile.nextElementSibling?.querySelector("h1")?.textContent).toBe("alice");
  });
});
