import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => cleanup());

// jsdom lacks the layout and media APIs that Radix, cmdk and the theme hook
// read; each stub answers the way a page with no layout would.
if (!window.matchMedia) {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }) as MediaQueryList;
}
if (!window.ResizeObserver) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  window.ResizeObserver = ResizeObserverStub as unknown as typeof ResizeObserver;
}
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}
// Radix Select reads the pointer capture API, which jsdom does not have.
for (const name of ["hasPointerCapture", "setPointerCapture", "releasePointerCapture"]) {
  if (!(name in Element.prototype)) Object.defineProperty(Element.prototype, name, { value: () => false, configurable: true, writable: true });
}
