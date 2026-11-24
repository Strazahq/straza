import * as React from "react";
import { describe, expect, it } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AlertDialog, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";

// The three content wrappers give the focus back to what opened them, as
// useReturnFocus says, when a screen opens them by state with no trigger.

type Own = (e: Event) => void;

// Each wrapper, drawn the way the screens draw it: open by state, closed
// by its own Escape.
const WRAPPERS: { name: string; role: string; draw: (open: boolean, close: () => void, own?: Own) => React.ReactNode }[] = [
  {
    name: "DialogContent", role: "dialog",
    draw: (open, close, own) => (
      <Dialog open={open} onOpenChange={(o) => { if (!o) close(); }}>
        <DialogContent onCloseAutoFocus={own}><DialogTitle>Probe</DialogTitle><DialogDescription>Probe</DialogDescription></DialogContent>
      </Dialog>
    ),
  },
  {
    name: "SheetContent", role: "dialog",
    draw: (open, close, own) => (
      <Sheet open={open} onOpenChange={(o) => { if (!o) close(); }}>
        <SheetContent onCloseAutoFocus={own}><SheetTitle>Probe</SheetTitle><SheetDescription>Probe</SheetDescription></SheetContent>
      </Sheet>
    ),
  },
  {
    name: "AlertDialogContent", role: "alertdialog",
    draw: (open, close, own) => (
      <AlertDialog open={open} onOpenChange={(o) => { if (!o) close(); }}>
        <AlertDialogContent onCloseAutoFocus={own}><AlertDialogTitle>Probe</AlertDialogTitle><AlertDialogDescription>Probe</AlertDialogDescription><AlertDialogCancel>Cancel</AlertDialogCancel></AlertDialogContent>
      </AlertDialog>
    ),
  },
];

// Screen draws the wrapper closed from the start, the way a page holds a
// dialog it opens later, with the button that opens it and a second button
// a caller's own handler may pick. gone takes the opener out of the
// document while the content is open.
function Screen({ draw, own, gone }: { draw: (typeof WRAPPERS)[number]["draw"]; own?: Own; gone?: boolean }) {
  const [open, setOpen] = React.useState(false);
  return (
    <>
      {!(gone && open) && <button type="button" onClick={() => setOpen(true)}>Open</button>}
      <button type="button">Elsewhere</button>
      {draw(open, () => setOpen(false), own)}
    </>
  );
}

describe.each(WRAPPERS)("$name", ({ role, draw }) => {
  it("gives the focus back to the button that opened it, after Escape", async () => {
    render(<Screen draw={draw} />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    const content = await screen.findByRole(role);
    await waitFor(() => expect(content.contains(document.activeElement)).toBe(true));
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open" })));
  });

  it("lets a caller's own handler that prevents the default place the focus", async () => {
    const own: Own = (e) => { e.preventDefault(); screen.getByRole("button", { name: "Elsewhere" }).focus(); };
    render(<Screen draw={draw} own={own} />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole(role);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Elsewhere" })));
  });

  it("focuses nothing it remembered when the opener left the document", async () => {
    render(<Screen draw={draw} gone />);
    const opener = screen.getByRole("button", { name: "Open" });
    await userEvent.click(opener);
    await screen.findByRole(role);
    expect(opener.isConnected).toBe(false);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole(role)).toBeNull());
    expect(document.activeElement).toBe(document.body);
  });
});

// FromMenu opens a dialog from an item of a menu, the way a page's More
// menu opens a confirm.
function FromMenu() {
  const [open, setOpen] = React.useState(false);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger>More</DropdownMenuTrigger>
        <DropdownMenuContent><DropdownMenuItem onSelect={() => setOpen(true)}>Turn off</DropdownMenuItem></DropdownMenuContent>
      </DropdownMenu>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent><DialogTitle>Probe</DialogTitle><DialogDescription>Probe</DialogDescription></DialogContent>
      </Dialog>
    </>
  );
}

describe("a dialog opened from a menu item", () => {
  it("gives the focus back to the menu's button, since the item left with the menu", async () => {
    const user = userEvent.setup();
    render(<FromMenu />);
    await user.click(screen.getByRole("button", { name: "More" }));
    await user.click(await screen.findByRole("menuitem", { name: "Turn off" }));
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "More" })));
  });
});
