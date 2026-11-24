import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { ReviewStep } from "./review-step";
import type { Row } from "./parts";
import type { Dry } from "./use-dry-run";
import { ApiError, getConfig } from "@/lib/api";
import { EMPTY_FORM, type Form, buildManifest, manifestYAML } from "@/lib/manifest";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), getConfig: vi.fn() }));

const URL_ = "http://demo-tools:3001/mcp";
const OK: Dry = { state: "ok", runtime: "remote", credential: "none" };
const ROWS: Row[] = [
  { key: "install", label: "Publish scout-tools", state: "pending" },
  { key: "check", label: "Check scout-tools", state: "pending" },
];

const form = (over: Partial<Form>): Form => ({ ...EMPTY_FORM, name: "scout-tools", url: URL_, ...over });
const mount = (f: Form, dry: Dry = OK) => render(<ReviewStep f={f} yaml={manifestYAML(buildManifest(f))} dry={dry} rows={ROWS} ran={false} problem={null} />);
const fact = (label: string) => document.querySelector('[data-fact="' + label + '"]') as HTMLElement | null;
const labels = () => [...document.querySelectorAll("[data-review-facts] dt")].map((d) => d.textContent);
const lead = (label: string) => {
  const el = fact(label) as HTMLElement;
  const fate = el.querySelector("[data-secret-fate]");
  return (el.textContent || "").slice(0, (el.textContent || "").length - (fate ? (fate.textContent || "").length : 0));
};

describe("the Review of Add MCP server", () => {
  beforeEach(() => {
    vi.mocked(getConfig).mockReset().mockResolvedValue({ apps: { upstream_timeout_seconds: 30, default_remote_rps: 20 } } as never);
  });

  // Each case is a runtime, the answers that go with it, and the Runs fact
  // the server card says for it.
  const runs: [string, Partial<Form>, string][] = [
    ["remote", {}, "Over HTTP at " + URL_],
    ["command", { mode: "command", runtime: "command", url: "", exec: "/usr/local/bin/mcp-server", args: "--stdio --verbose" }, "On the strazad host, as a process Straza starts: /usr/local/bin/mcp-server --stdio --verbose"],
    ["oci", { mode: "oci", runtime: "oci", url: "", image: "ghcr.io/org/server:1.2" }, "On the strazad host, as a container Straza runs from ghcr.io/org/server:1.2"],
  ];
  it.each(runs)("says where a %s server runs", (_kind, over, want) => {
    mount(form(over));
    expect(fact("Runs")?.textContent).toBe(want);
  });

  // Each case is a credential, the answers that go with it, the lead of the
  // Credential fact, and the sentence about the secret under it.
  const credentials: [string, Partial<Form>, string, string | undefined][] = [
    // A server that takes no credential says nothing more about a secret.
    ["none", {}, "None.", undefined],
    ["one shared secret in a named header", { cred: "static", sentAs: "header", headerName: "X-Demo-Key" }, "One shared secret, sent in the X-Demo-Key header",
      "Stored encrypted after publishing. Never shown again."],
    ["one shared secret as a bearer", { cred: "static", sentAs: "bearer" }, "One shared secret, sent in the Authorization header",
      "Stored encrypted after publishing. Never shown again."],
    ["one shared secret the process reads", { mode: "command", runtime: "command", url: "", exec: "/usr/local/bin/mcp-server", cred: "static", envName: "API_TOKEN" }, "One shared secret, which the process reads from API_TOKEN",
      "Stored encrypted after publishing. Never shown again."],
    ["each person's own token", { cred: "token", sentAs: "bearer" }, "Each person's own token, sent in the Authorization header",
      "Nothing is stored now. Each person pastes their own token on the Credentials tab of their self-service page after publishing."],
    ["each person's own token with the agents' shared secret", { cred: "token", agents: "shared", sentAs: "bearer" }, "Each person's own token, sent in the Authorization header",
      "The shared secret for agents is stored encrypted after publishing. Each person pastes their own token on the Credentials tab of their self-service page."],
    ["each person's own sign-in", { cred: "oauth", agents: "sponsor", provider: "keycloak", scopes: "openid" }, "Each person's own sign-in through keycloak",
      "Nothing is stored now. Each person signs in through keycloak on the Credentials tab of their self-service page."],
  ];
  it.each(credentials)("says the credential of %s, with what happens to the secret", (_case, over, want, fate) => {
    mount(form(over));
    expect(lead("Credential")).toBe(want);
    expect(fact("Credential")?.querySelector("[data-secret-fate]")?.textContent).toBe(fate);
  });

  // Each case is a caller kind, an agents choice, and the row's name as
  // the card says it.
  const agents: [string, string, string][] = [
    ["token", "own", "Refused"],
    ["token", "sponsor", "Run on their sponsor's token"],
    ["token", "shared", "Use this server's shared secret"],
    ["oauth", "own", "Refused"],
    ["oauth", "sponsor", "Run on their sponsor's sign-in"],
    ["oauth", "shared", "Use a shared account"],
  ];
  it.each(agents)("names the %s server's agents choice %s as its row says it", (cred, value, want) => {
    mount(form({ cred, agents: value, sentAs: "bearer", provider: "keycloak" }));
    expect(fact("Agents")?.textContent).toBe(want);
  });

  it("leaves Agents out for a credential that has no caller of its own, and Description out when it is empty", () => {
    mount(form({ cred: "static", sentAs: "bearer" }));
    expect(labels()).toEqual(["Name", "Runs", "Credential", "Tools", "Limits"]);
    expect(fact("Name")?.textContent).toBe("scout-tools");
    expect(fact("Name")?.querySelector(".font-mono")).toBeTruthy();
  });

  it("says the description when there is one", () => {
    mount(form({ desc: "Search the scout notes" }));
    expect(labels()).toEqual(["Name", "Description", "Runs", "Credential", "Tools", "Limits"]);
    expect(fact("Description")?.textContent).toBe("Search the scout notes");
  });

  it("names every tool, the server-wide time per call and the remote rate it read once from the config", async () => {
    mount(form({}));
    expect(fact("Tools")?.textContent).toBe("Every tool the server lists");
    await waitFor(() => expect(fact("Limits")?.textContent).toBe("30 s per call. 20 calls per second, per session, the default."));
    expect(getConfig).toHaveBeenCalledTimes(1);
  });

  it("says a command server with no rate is not limited", async () => {
    mount(form({ mode: "command", runtime: "command", url: "", exec: "/usr/local/bin/mcp-server" }));
    await waitFor(() => expect(fact("Limits")?.textContent).toBe("30 s per call. Not limited."));
  });

  it("says the remote default beside a timeout of the record's own", async () => {
    const doc = { apiVersion: "straza.dev/v1beta1", kind: "App", metadata: { name: "weather" }, server: { name: "io.github.example/weather" }, straza: { runtime: { kind: "remote", remote: { url: URL_ } }, limits: { timeoutSeconds: 20 } } };
    mount(form({ imported: { doc, text: "" } }));
    await waitFor(() => expect(fact("Limits")?.textContent).toBe("20 s per call. 20 calls per second, per session, the default."));
  });

  // Each case is a config read this session cannot use.
  const unread: [string, () => Promise<never>][] = [
    ["may not read it", () => Promise.reject(new ApiError("forbidden", 403))],
    ["does not reach strazad", () => Promise.reject(new ApiError("unreachable", 0, true))],
    ["reads no time in it", () => Promise.resolve({} as never)],
  ];
  it.each(unread)("leaves the time per call unnamed when the session %s", async (_case, answer) => {
    vi.mocked(getConfig).mockImplementation(answer);
    mount(form({}));
    await waitFor(() => expect(getConfig).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 0));
    expect(fact("Limits")?.textContent).toBe("The server-wide time per call. The default for a remote server.");
  });

  it("says an imported record's own tools and limits", async () => {
    const doc = { apiVersion: "straza.dev/v1beta1", kind: "App", metadata: { name: "weather" }, server: { name: "io.github.example/weather" }, straza: { runtime: { kind: "remote", remote: { url: URL_ } }, exposure: { tools: ["echo", "get-sum"] }, limits: { rps: 5, timeoutSeconds: 20 } } };
    mount(form({ imported: { doc, text: "" } }));
    expect(fact("Tools")?.textContent).toBe("Only echo, get-sum.");
    await waitFor(() => expect(getConfig).toHaveBeenCalled());
    expect(fact("Limits")?.textContent).toBe("20 s per call. 5 calls per second, per session.");
  });

  // Each case is what the dry run answers, the banner's state and its words.
  const verdicts: [string, Dry, string, string][] = [
    ["accepts it", OK, "ok", "accepted"],
    ["refuses it", { state: "refused", text: "remote url must be absolute" }, "refused", "refusedremote url must be absolute"],
    ["is not reached", { state: "unreachable" }, "unreachable", "Straza is unreachable, so the manifest is not checked here. Save and publish will say."],
    ["has not answered", { state: "pending" }, "pending", "Checking the manifest with Straza."],
  ];
  it.each(verdicts)("sets the verdict as one banner when the server %s", (_case, dry, state, want) => {
    mount(form({}), dry);
    expect(document.querySelector('[data-verdict="' + state + '"]')?.textContent).toBe(want);
  });

  it("numbers the rows under What will happen, says nobody can call it yet, and folds the manifest closed", () => {
    mount(form({}));
    expect(screen.getByRole("heading", { level: 3, name: "What will happen" })).toBeTruthy();
    expect([...document.querySelectorAll("[data-landed] li")].map((li) => li.textContent)).toEqual(["1Publish scout-tools", "2Check scout-tools"]);
    expect(document.querySelector("[data-nobody-until-role]")?.textContent).toBe("Nobody can call it until a role gives access to its tools.");
    const fold = document.querySelector("[data-yaml-fold]") as HTMLDetailsElement;
    expect(fold.open).toBe(false);
    expect(fold.querySelector("summary")?.textContent).toBe("View as YAMLapp.yaml, as it will be sent");
    expect(document.querySelector("[data-manifest]")?.textContent).toContain("url: " + URL_);
  });
});
