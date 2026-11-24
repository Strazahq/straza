import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Tabs, TabsList, TabsTrigger } from "./tabs";

// layout stands in for what jsdom does not do: the list shows 200 px
// from its own left edge, and each tab is 150 px wide in list order.
function layout(list: HTMLElement) {
  list.getBoundingClientRect = () => ({ left: 0, right: 200 }) as DOMRect;
  Array.from(list.querySelectorAll<HTMLElement>("[role=tab]")).forEach((tab, i) => {
    tab.getBoundingClientRect = () => ({ left: i * 150 - list.scrollLeft, right: (i + 1) * 150 - list.scrollLeft }) as DOMRect;
  });
}

describe("the tab list", () => {
  it("brings the selected tab into view when the list is wider than its place", async () => {
    render(
      <Tabs defaultValue="one">
        <TabsList><TabsTrigger value="one">One</TabsTrigger><TabsTrigger value="two">Two</TabsTrigger><TabsTrigger value="three">Three</TabsTrigger></TabsList>
      </Tabs>,
    );
    const list = screen.getByRole("tablist");
    layout(list);
    await userEvent.click(screen.getByRole("tab", { name: "Three" }));
    expect(list.scrollLeft).toBe(250);
    await userEvent.click(screen.getByRole("tab", { name: "One" }));
    expect(list.scrollLeft).toBe(0);
  });

  it("brings the selected tab into view again when a count that loads late widens a tab", async () => {
    const Strip = ({ count }: { count: string }) => (
      <Tabs value="three">
        <TabsList><TabsTrigger value="one">One {count}</TabsTrigger><TabsTrigger value="two">Two</TabsTrigger><TabsTrigger value="three">Three</TabsTrigger></TabsList>
      </Tabs>
    );
    const { rerender } = render(<Strip count="" />);
    const list = screen.getByRole("tablist");
    layout(list);
    // The first tab grows by 60 px once its count is read.
    const tabs = Array.from(list.querySelectorAll<HTMLElement>("[role=tab]"));
    tabs.forEach((tab, i) => {
      tab.getBoundingClientRect = () => ({ left: i * 150 + (i ? 60 : 0) - list.scrollLeft, right: (i + 1) * 150 + 60 - list.scrollLeft }) as DOMRect;
    });
    rerender(<Strip count="9" />);
    await waitFor(() => expect(list.scrollLeft).toBe(310));
  });

  it("draws a tab that is not selected in the muted text colour, which passes the contrast bar in both themes", () => {
    render(
      <Tabs defaultValue="one">
        <TabsList><TabsTrigger value="one">One</TabsTrigger><TabsTrigger value="two">Two</TabsTrigger></TabsList>
      </Tabs>,
    );
    const classes = screen.getByRole("tab", { name: "Two" }).className.split(" ");
    expect(classes).toContain("text-muted-foreground");
    expect(classes.some((c) => c.startsWith("text-foreground/"))).toBe(false);
  });

  it("leaves the list where it is while the selected tab shows", async () => {
    render(
      <Tabs defaultValue="one">
        <TabsList><TabsTrigger value="one">One</TabsTrigger><TabsTrigger value="two">Two</TabsTrigger></TabsList>
      </Tabs>,
    );
    const list = screen.getByRole("tablist");
    list.getBoundingClientRect = () => ({ left: 0, right: 400 }) as DOMRect;
    Array.from(list.querySelectorAll<HTMLElement>("[role=tab]")).forEach((tab, i) => {
      tab.getBoundingClientRect = () => ({ left: i * 150, right: (i + 1) * 150 }) as DOMRect;
    });
    await userEvent.click(screen.getByRole("tab", { name: "Two" }));
    expect(list.scrollLeft).toBe(0);
  });
});
