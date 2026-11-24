import { afterEach, describe, expect, it } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { BASE, navigate, pathFor, requestOf, resolve, useRequestID, useSelfRoute } from "./router";

function Shown() {
  return <span data-testid="tab">{useSelfRoute()}</span>;
}

function Named() {
  return <span data-testid="request">{useRequestID()}</span>;
}

afterEach(() => window.history.replaceState(null, "", "/"));

describe("the self-service router", () => {
  it.each([
    ["/self-service/", "home"],
    ["/self-service", "home"],
    ["/self-service/requests", "requests"],
    ["/self-service/credentials", "credentials"],
    ["/self-service/devices/", "devices"],
    ["/self-service/browser", "devices"],
    ["/self-service/nope", "home"],
    ["/console/approvals", "home"],
  ])("reads %s as the %s tab", (path, tab) => {
    expect(resolve(path)).toBe(tab);
  });

  it("addresses every tab under the base, Home at the page's own address", () => {
    expect(BASE).toBe("/self-service/");
    expect(pathFor("home")).toBe("/self-service/");
    expect(pathFor("requests")).toBe("/self-service/requests");
    expect(pathFor("credentials")).toBe("/self-service/credentials");
    expect(pathFor("devices")).toBe("/self-service/devices");
  });

  it("shows Devices at the old address and puts the new one in the address bar, query kept", () => {
    window.history.replaceState(null, "", "/self-service/browser?from=signin");
    render(<Shown />);
    expect(screen.getByTestId("tab").textContent).toBe("devices");
    expect(window.location.pathname).toBe("/self-service/devices");
    expect(window.location.search).toBe("?from=signin");
  });

  it("leaves an unknown address as it is and shows Home", () => {
    window.history.replaceState(null, "", "/self-service/nope");
    render(<Shown />);
    expect(screen.getByTestId("tab").textContent).toBe("home");
    expect(window.location.pathname).toBe("/self-service/nope");
  });

  it.each([
    ["/self-service/requests/apr_push", "requests", "apr_push"],
    ["/self-service/requests/apr_push/", "requests", "apr_push"],
    ["/self-service/requests/apr%2Fx", "requests", "apr/x"],
    ["/self-service/requests/%E0%A4%A", "requests", ""],
    ["/self-service/requests", "requests", ""],
    ["/self-service/devices/apr_push", "devices", ""],
    ["/self-service/apr_push", "home", ""],
  ])("reads %s as the %s tab naming the request %j", (path, tab, id) => {
    expect(resolve(path)).toBe(tab);
    expect(requestOf(path)).toBe(id);
  });

  it("addresses one request under the Requests tab and no other", () => {
    expect(pathFor("requests", "apr_push")).toBe("/self-service/requests/apr_push");
    expect(pathFor("requests", "apr/x")).toBe("/self-service/requests/apr%2Fx");
    expect(pathFor("devices", "apr_push")).toBe("/self-service/devices");
    expect(pathFor("home", "apr_push")).toBe("/self-service/");
  });

  it("follows the request in the address through navigate, a reload and Back", () => {
    window.history.replaceState(null, "", "/self-service/requests/apr_push");
    const reload = render(<><Shown /><Named /></>);
    expect(screen.getByTestId("tab").textContent).toBe("requests");
    expect(screen.getByTestId("request").textContent).toBe("apr_push");
    reload.unmount();

    render(<><Shown /><Named /></>);
    act(() => navigate("requests"));
    expect(window.location.pathname).toBe("/self-service/requests");
    expect(screen.getByTestId("request").textContent).toBe("");
    act(() => navigate("requests", false, "apr_sum"));
    expect(window.location.pathname).toBe("/self-service/requests/apr_sum");
    expect(screen.getByTestId("request").textContent).toBe("apr_sum");
    act(() => {
      window.history.replaceState(null, "", "/self-service/requests/apr_env");
      window.dispatchEvent(new PopStateEvent("popstate"));
    });
    expect(screen.getByTestId("request").textContent).toBe("apr_env");
    act(() => navigate("home"));
    expect(screen.getByTestId("tab").textContent).toBe("home");
    expect(screen.getByTestId("request").textContent).toBe("");
  });
});
