// The theme choice: system, light or dark. The choice lives in
// localStorage as a display preference, the CSS reads data-theme on the
// root element, and system means no attribute so the OS preference
// decides. Every storage access is wrapped: a private window has none.
import * as React from "react";

export type ThemeChoice = "system" | "light" | "dark";
export type Resolved = "dark" | "light";

const KEY = "straza.console.theme";
const subs = new Set<() => void>();
let choice: ThemeChoice = readChoice();

function readChoice(): ThemeChoice {
  try {
    const v = window.localStorage.getItem(KEY);
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

function apply(c: ThemeChoice) {
  const root = document.documentElement;
  if (c === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", c);
}

// themeChoice is the stored choice.
export function themeChoice(): ThemeChoice {
  return choice;
}

// setTheme stores the choice, applies it to the root element and tells
// every subscriber; a storage failure keeps the choice for this page.
export function setTheme(c: ThemeChoice) {
  choice = c;
  try {
    window.localStorage.setItem(KEY, c);
  } catch {
    // Private mode: the choice lasts this page only.
  }
  apply(c);
  for (const f of subs) f();
}

// resolveTheme is the theme the CSS is rendering: an explicit data-theme
// wins, else the system preference.
export function resolveTheme(): Resolved {
  const forced = document.documentElement.getAttribute("data-theme");
  if (forced === "light" || forced === "dark") return forced;
  return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

// useTheme returns the stored choice and the resolved theme, and re-renders
// the caller when either changes.
export function useTheme(): { choice: ThemeChoice; resolved: Resolved } {
  const [state, setState] = React.useState(() => ({ choice, resolved: resolveTheme() }));
  React.useEffect(() => {
    const on = () => setState({ choice, resolved: resolveTheme() });
    subs.add(on);
    const mq = window.matchMedia("(prefers-color-scheme: light)");
    mq.addEventListener("change", on);
    on();
    return () => {
      subs.delete(on);
      mq.removeEventListener("change", on);
    };
  }, []);
  return state;
}

apply(choice);
