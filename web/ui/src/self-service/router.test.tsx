import { describe, expect, it } from "vitest";
import { BASE, pathFor, resolve } from "./router";

describe("the self-service router", () => {
  it("reads the tab from the address and falls back to Requests", () => {
    expect(resolve("/self-service/")).toBe("requests");
    expect(resolve("/self-service")).toBe("requests");
    expect(resolve("/self-service/credentials")).toBe("credentials");
    expect(resolve("/self-service/browser/")).toBe("browser");
    expect(resolve("/self-service/nope")).toBe("requests");
    expect(resolve("/console/approvals")).toBe("requests");
  });
  it("addresses every tab under the base", () => {
    expect(BASE).toBe("/self-service/");
    expect(pathFor("credentials")).toBe("/self-service/credentials");
  });
});
