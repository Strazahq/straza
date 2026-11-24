import * as React from "react";

// useReturnFocus gives the focus back to the element that held it when a
// dialog's or a sheet's content was first drawn. A content opened by state
// has no Radix trigger, and the modal content would then focus nothing.
// The answer is the content's onCloseAutoFocus: the caller's own handler
// runs first and wins when it prevents the default. An opener that has left
// the document gets nothing, and Radix's own return applies.
// openerOf answers what takes the focus back for the element that held it:
// the element itself, or for an item of a menu the menu's button, because
// the menu closes and takes its items along when a dialog opens from one.
function openerOf(el: Element | null): Element | null {
  const menu = el ? el.closest('[role="menu"]') : null;
  const button = menu ? document.getElementById(menu.getAttribute("aria-labelledby") || "") : null;
  return button || el;
}

export function useReturnFocus(own?: (e: Event) => void): (e: Event) => void {
  // The state initialiser reads the focus before any effect of the content
  // moves it inside.
  const [opener] = React.useState(() => openerOf(document.activeElement));
  return (e) => {
    own?.(e);
    if (e.defaultPrevented || !(opener instanceof HTMLElement) || !opener.isConnected) return;
    e.preventDefault();
    opener.focus();
  };
}
