import { describe, expect, it } from "vitest";
import { cn } from "./utils";

describe("cn with the six text sizes", () => {
  it("keeps a size and a colour together", () => {
    expect(cn("text-caption", "text-muted-foreground")).toBe("text-caption text-muted-foreground");
  });
  it("lets a later size replace an earlier one", () => {
    expect(cn("text-caption", "text-section")).toBe("text-section");
  });
  it("replaces text-sm with text-dense", () => {
    expect(cn("text-sm", "text-dense")).toBe("text-dense");
  });
});

describe("cn with the round pill", () => {
  it("lets a later radius replace rounded-pill", () => {
    expect(cn("rounded-pill", "rounded-md")).toBe("rounded-md");
  });
});
