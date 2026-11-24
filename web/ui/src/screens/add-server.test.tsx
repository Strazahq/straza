import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AddServer } from "./add-server";
import { Landed } from "@/components/wizard/parts";
import { ApiError, type DraftVerdict, dryRunApp, getConfig, importServerJSON, listApps, listProviders, recheckApp, setSecret } from "@/lib/api";
import { checkDraft, createDraft, publishDraft } from "@/lib/drafts-api";
import { take } from "@/lib/handoff";
import { NAME_RULE } from "@/lib/manifest";
import { navigate } from "@/lib/router";
import { notify } from "@/lib/notify";
import { version } from "@/lib/public";
import { SECRET_WAITS } from "@/lib/save-words";
import { ADDRESS_HINT, AGENT_ROWS, CALLER_NOT_HERE, COMMAND_OFF, CREDENTIAL_CHOICES, ENV_WARNING_SHORT, IMPORT_RECORD, LEDE, OCI_OFF, OTHER_WAYS, RUNTIMES, SERVER_NAME_HINT } from "@/lib/words";
import { VERDICT } from "@/test/drafts-fixture";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApps: vi.fn(), listProviders: vi.fn(), dryRunApp: vi.fn(), setSecret: vi.fn(), recheckApp: vi.fn(), importServerJSON: vi.fn(), getConfig: vi.fn(),
}));
// The wizard publishes through the shared saves, so the drafts calls are
// faked here and the whole chain runs: check, draft, publish, then the
// secret and the probe on the server the publish made.
vi.mock("@/lib/drafts-api", async (orig) => ({ ...(await orig<typeof import("@/lib/drafts-api")>()), checkDraft: vi.fn(), createDraft: vi.fn(), publishDraft: vi.fn() }));
vi.mock("@/lib/public", () => ({ version: vi.fn() }));
// The session's standing: root by default, and a server admin in the case
// that proves the wizard is locked for an account that may not register.
const sess = vi.hoisted(() => ({ areas: null as Record<string, true> | null, servers: 0 }));
vi.mock("@/lib/session", () => ({ adminAreas: () => sess.areas, adminServers: () => sess.servers }));
vi.mock("@/lib/router", async (orig) => ({ ...(await orig<typeof import("@/lib/router")>()), navigate: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn(), undo: vi.fn() } }));
// The sheet has its own suite; here it only has to open with the server.
vi.mock("@/components/test-a-call", () => ({
  TestACall: ({ open, app, tools }: { open: boolean; app: { name: string }; tools: string[] }) => (open ? <div data-test-a-call>{app.name + ": " + tools.join(", ")}</div> : null),
}));

const URL_ = "http://demo-tools:3001/mcp";
const TOOLS = ["echo", "get-sum", "get-env"];
const failure = (message: string, status: number, unreachable = false) => Object.assign(new Error(message), { status, unreachable });
const scout = (over: Record<string, unknown> = {}) => ({ id: "app-9", name: "scout-tools", version: "0", runtime: "remote", status: "running", detail: "", tools: TOOLS, reached_by: [], ...over });
const DEMO = { id: "a1", name: "demo-tools", runtime: "remote", status: "running", tools: ["echo"], reached_by: ["dev-tools"] };
const CLEAN: DraftVerdict = { ...VERDICT, refused: [], risks: [], warnings: [], unchecked: [], passed: [], info: [], gains: [], needs: [], risk_digest: "" };
const DRAFT60 = { id: "60", revision: 1, state: "open" as const, door: "console" as const, authors: [], items: [], title: "Add server scout-tools", created_at: "", updated_at: "" };
// The dry run answers what the manifest declares, the way the server reads it.
const dryAnswer = (yaml: string) => Promise.resolve({
  name: "scout-tools", tools: ["*"],
  runtime: (/runtime:\n\s+kind: (\w+)/.exec(yaml) || ["", "remote"])[1],
  credential: (/credential:\n\s+kind: (\w+)/.exec(yaml) || ["", "none"])[1],
});

const box = (name: string) => screen.getByRole("textbox", { name });
const click = async (role: string, name: string) => userEvent.click(screen.getByRole(role, { name }));
const radio = (name: string) => screen.getByRole("radio", { name });
// The cards are found by their own names, the names a person reads, never
// by the kind the manifest writes.
const runtimeCard = (kind: string) => radio((RUNTIMES.find((r) => r.key === kind) as { name: string }).name);
const credentialCard = (kind: string) => radio((CREDENTIAL_CHOICES.find((c) => c.kind === kind) as { label: string }).label);
const agentsRow = (value: string, kind = "token") => radio((AGENT_ROWS[kind].find((r) => r.value === value) as { name: string }).name);
const heading = (name: string) => screen.findByRole("heading", { level: 2, name });
// LOADED is the budget of a case that ran out of vitest's 5 s, or of a wait
// that ran out of testing-library's 1 s, under make ui-test's full load,
// and that passes alone well inside both.
const LOADED = 30000;
const foot = () => within(document.querySelector("[data-wizard-foot]") as HTMLElement);
const footButtons = () => foot().getAllByRole("button").map((b) => b.textContent);
const textOf = (selector: string) => (document.querySelector(selector) as HTMLElement | null)?.textContent || "";
// fold reads the manifest under its closed View as YAML line, which keeps
// the document in the page.
const fold = async () => {
  expect((document.querySelector("[data-yaml-fold]") as HTMLDetailsElement).open).toBe(false);
  expect(document.querySelector("[data-yaml-fold] summary")?.textContent).toBe("View as YAMLapp.yaml, as it will be sent");
  return (document.querySelector("[data-manifest]") as HTMLElement).textContent || "";
};
const choose = async (combo: string, option: string) => {
  await click("combobox", combo);
  await userEvent.click(await screen.findByRole("option", { name: option }));
};

// toCredential names the server, picks its runtime, and moves past Next.
async function toCredential(name = "scout-tools", runtime: "remote" | "command" = "remote") {
  render(<AddServer />);
  await userEvent.type(box("server name"), name);
  if (runtime === "command") {
    await userEvent.click(runtimeCard("command"));
    await userEvent.type(box("executable"), "/usr/local/bin/mcp-server");
  } else await userEvent.type(box("server address"), URL_);
  await click("button", "Next: Credential");
  await heading("Credential");
}
// toReview picks a static secret sent in the named header X-Demo-Key.
async function toReview() {
  await toCredential();
  await userEvent.click(credentialCard("static"));
  await userEvent.type(screen.getByLabelText("secret"), "hunter2");
  await choose("sent as", "A named header");
  await userEvent.type(box("header name"), "X-Demo-Key");
  await click("button", "Next: Review");
  await heading("Review");
}
async function toCheck() {
  await toReview();
  await click("button", "Save and publish");
  await heading("Check");
}

describe("the Add MCP server wizard", () => {
  beforeEach(() => {
    vi.mocked(listApps).mockReset().mockResolvedValue([DEMO]);
    vi.mocked(listProviders).mockReset().mockResolvedValue([{ name: "keycloak", scopes: ["openid", "profile"], redirect_uri: "http://localhost:8420/v1/connect/callback" }]);
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "standalone", runtimes: ["remote", "command"] });
    vi.mocked(dryRunApp).mockReset().mockImplementation(dryAnswer);
    vi.mocked(checkDraft).mockReset().mockResolvedValue({ items: [], verdict: CLEAN });
    vi.mocked(createDraft).mockReset().mockResolvedValue({ draft: DRAFT60, verdict: CLEAN });
    // A publish makes the server, so the list the wizard reads next names it.
    vi.mocked(publishDraft).mockReset().mockImplementation(async () => {
      vi.mocked(listApps).mockResolvedValue([DEMO, scout({ status: "starting", tools: [] })]);
      return { draft: { ...DRAFT60, state: "published" }, snapshot: "3be0a1", servers: [{ name: "scout-tools", change: "created", status: "starting" }], next: [] };
    });
    vi.mocked(setSecret).mockReset().mockResolvedValue({ id: "c1", scope: "app", role: "", fingerprint: "a1c4" });
    vi.mocked(recheckApp).mockReset().mockResolvedValue(scout());
    vi.mocked(importServerJSON).mockReset();
    vi.mocked(getConfig).mockReset().mockResolvedValue({ apps: { upstream_timeout_seconds: 30 } } as never);
    vi.mocked(navigate).mockReset();
    vi.mocked(notify.ok).mockReset();
    vi.mocked(notify.warn).mockReset();
    vi.mocked(notify.failed).mockReset();
  });

  it("opens on the Server step under the step strip, with no lede, the HTTP row and the address focused", async () => {
    render(<AddServer />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Add MCP server");
    expect(document.querySelector("[data-page-head] p")).toBeNull();
    const steps = within(screen.getByRole("list", { name: "wizard steps" })).getAllByRole("listitem");
    expect(steps.map((s) => s.textContent)).toEqual(["1Server", "2Credential", "3Review", "4Check"]);
    expect(steps[0].getAttribute("aria-current")).toBe("step");
    expect((await heading("Server")).textContent).toBe("Server");
    expect((await heading("Server")).parentElement?.parentElement?.querySelector("p")).toBeNull();
    // The standalone stand-in runs commands and has no docker, so the
    // choice is HTTP or a command, each a row with its short line.
    await waitFor(() => expect(screen.getAllByRole("radio").map((r) => r.getAttribute("aria-label"))).toEqual([RUNTIMES[0].name, RUNTIMES[1].name]));
    expect(runtimeCard("remote").getAttribute("aria-checked")).toBe("true");
    for (const r of RUNTIMES.slice(0, 2)) {
      expect(runtimeCard(r.key).textContent).toBe(r.name + r.line);
      // A row names its choice in words and draws no raw kind beside it.
      expect(runtimeCard(r.key).querySelector(".font-mono")).toBeNull();
    }
    expect(document.activeElement).toBe(box("server address"));
    expect(screen.getByText(ADDRESS_HINT)).toBeTruthy();
    expect(screen.getByText(SERVER_NAME_HINT)).toBeTruthy();
    // Before the dry run has run, the line under the fields stays empty.
    expect(screen.queryByRole("status")).toBeNull();
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(crumbs.textContent).toBe("Straza/MCP servers/Add");
    await userEvent.click(within(crumbs).getByRole("link", { name: "MCP servers" }));
    expect(navigate).toHaveBeenCalledWith("servers", undefined);
  });

  it("confirms a free name and applies the manifest rule as you type", async () => {
    render(<AddServer />);
    await userEvent.type(box("server name"), "scout-tools");
    expect(await screen.findByText("scout-tools is free")).toBeTruthy();
    await userEvent.clear(box("server name"));
    await userEvent.type(box("server name"), "Bad Name");
    expect(screen.getByText(NAME_RULE)).toBeTruthy();
  });

  it("draws the address first, then the name, then the description", async () => {
    render(<AddServer />);
    await heading("Server");
    const order = [box("server address"), box("server name"), box("server description")];
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });

  // Each case is a typed address and the name it suggests: the host's first
  // label after a leading mcp, api or www, with no port, folded to the name
  // rule, and nothing for an IP address.
  const suggested: [string, string][] = [
    ["https://mcp.github.com/mcp", "github"],
    ["http://objects-demo:3002/mcp", "objects-demo"],
    ["https://api.www.Linear.app/mcp", "linear"],
    ["https://user@mcp.Example_Corp.io:8443/v1", "example-corp"],
    ["http://10.0.0.7:3001/mcp", ""],
    ["http://[::1]:3001/mcp", ""],
  ];
  it.each(suggested)("names the server from the address %s while the name is not typed", async (address, want) => {
    render(<AddServer />);
    await userEvent.click(box("server address"));
    await userEvent.paste(address);
    expect((box("server name") as HTMLInputElement).value).toBe(want);
  });

  it("stops the name following the address once a name is typed, even after the name is cleared", async () => {
    render(<AddServer />);
    await userEvent.type(box("server address"), "https://mcp.github.com/mcp");
    expect((box("server name") as HTMLInputElement).value).toBe("github");
    await userEvent.type(box("server name"), "-team");
    expect((box("server name") as HTMLInputElement).value).toBe("github-team");
    await userEvent.clear(box("server address"));
    await userEvent.type(box("server address"), URL_);
    expect((box("server name") as HTMLInputElement).value).toBe("github-team");
    await userEvent.clear(box("server name"));
    await userEvent.type(box("server address"), "x");
    expect((box("server name") as HTMLInputElement).value).toBe("");
  });

  it("stops at a twin name and offers Open it", async () => {
    render(<AddServer />);
    await userEvent.type(box("server name"), "demo-tools");
    await userEvent.type(box("server address"), URL_);
    expect((await screen.findByText(/A server named demo-tools already exists\./)).textContent).toContain("Open it");
    await click("button", "Next: Credential");
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Server");
    expect(document.activeElement).toBe(box("server name"));
    await click("link", "Open it");
    expect(navigate).toHaveBeenCalledWith("servers", ["a1"]);
  });

  // An App put of a live server's name changes that server, and the check
  // refuses nothing for it, so a name that went live after the list was
  // read, or that no list could check, is refused by the check's existed
  // answer in the Server step's words.
  const LIVE = { items: [{ kind: "App" as const, name: "scout-tools", op: "put" as const, doc: "", existed: true }], verdict: CLEAN };
  const TAKEN = "Nothing was saved.A server named scout-tools already exists. Go back to Server and pick another name.";
  const saveNote = () => document.querySelector('[data-save-note="refused"]') as HTMLElement | null;
  it.each(["Save draft", "Save and publish"])("stops %s when the check answers the name as a live server, and stores nothing", async (button) => {
    vi.mocked(checkDraft).mockResolvedValue(LIVE);
    await toReview();
    await click("button", button);
    await waitFor(() => expect(saveNote()).not.toBeNull());
    expect(saveNote()!.textContent).toBe(TAKEN);
    for (const fn of [createDraft, publishDraft, setSecret]) expect(fn).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { level: 2, name: "Review" })).toBeTruthy();
  }, LOADED);

  it("says a name is not checked when the servers list could not be read, and the save still refuses a live one", async () => {
    vi.mocked(listApps).mockRejectedValue(failure("strazad did not answer", 0, true));
    vi.mocked(checkDraft).mockResolvedValue(LIVE);
    render(<AddServer />);
    await userEvent.type(box("server name"), "scout-tools");
    const wait = { timeout: LOADED };
    expect(await screen.findByText("The servers list could not be read, so this name is not checked here. Saving still refuses a name that a server already has.", undefined, wait)).toBeTruthy();
    await userEvent.type(box("server address"), URL_);
    await click("button", "Next: Credential");
    await screen.findByRole("heading", { level: 2, name: "Credential" }, wait);
    await click("button", "Next: Review");
    await screen.findByRole("heading", { level: 2, name: "Review" }, wait);
    await click("button", "Save and publish");
    await waitFor(() => expect(saveNote()).not.toBeNull(), wait);
    expect(saveNote()!.textContent).toBe(TAKEN);
    for (const fn of [createDraft, publishDraft]) expect(fn).not.toHaveBeenCalled();
  }, LOADED);

  it("keeps Next clickable, focuses the first missing field and says what is missing", async () => {
    render(<AddServer />);
    const next = foot().getByRole("button", { name: "Next: Credential" });
    expect(next.hasAttribute("disabled") || next.getAttribute("aria-disabled") === "true").toBe(false);
    await userEvent.click(next);
    expect(await screen.findByText("Name the server. " + NAME_RULE)).toBeTruthy();
    expect(screen.getByText("Say where Straza reaches it: an http or https address.")).toBeTruthy();
    expect(document.activeElement).toBe(box("server address"));
    await userEvent.type(box("server name"), "scout-tools");
    await waitFor(() => expect(screen.queryByText("Name the server. " + NAME_RULE)).toBeNull());
    await userEvent.type(box("server address"), "ftp://demo-tools");
    await userEvent.click(next);
    expect(await screen.findByText("The address must start with http:// or https://.")).toBeTruthy();
    expect(document.activeElement).toBe(box("server address"));
  });

  // otherWays opens the closed fold under the fields and reads each way it
  // lists with the sentence that says why this server cannot use it.
  const otherWays = async () => {
    expect(document.querySelector("[data-other-ways]")).toBeNull();
    await click("button", OTHER_WAYS);
    return [...document.querySelectorAll("[data-way-off]")].map((li) => li.textContent);
  };

  it("moves the container behind the fold with the docker sentence, and offers it when /version lists no runtimes", async () => {
    const first = render(<AddServer />);
    await waitFor(() => expect(screen.queryByRole("radio", { name: RUNTIMES[2].name })).toBeNull());
    expect(await otherWays()).toEqual([RUNTIMES[2].name + OCI_OFF]);
    first.unmount();
    vi.mocked(version).mockReset().mockResolvedValue(null);
    render(<AddServer />);
    await waitFor(() => expect(version).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: OTHER_WAYS })).toBeNull();
    await userEvent.click(runtimeCard("oci"));
    expect(runtimeCard("oci").getAttribute("aria-disabled")).toBeNull();
    expect(box("image")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "server address" })).toBeNull();
  });

  it("offers no choice when HTTP is the only way, and folds the command and the container with why", async () => {
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "enterprise", runtimes: ["remote"] });
    render(<AddServer />);
    await waitFor(() => expect(screen.queryByRole("radiogroup")).toBeNull());
    expect(screen.queryAllByRole("radio")).toEqual([]);
    expect(box("server address")).toBeTruthy();
    expect(screen.queryByRole("textbox", { name: "executable" })).toBeNull();
    expect(await otherWays()).toEqual([RUNTIMES[1].name + COMMAND_OFF, RUNTIMES[2].name + OCI_OFF]);
  });

  it("opens the paste box from Import a registry record beside the heading, and closes it again", async () => {
    render(<AddServer />);
    const door = screen.getByRole("button", { name: IMPORT_RECORD });
    expect(door.getAttribute("aria-expanded")).toBe("false");
    await userEvent.click(door);
    expect(door.getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(box("server.json"));
    expect(screen.queryByRole("textbox", { name: "server address" })).toBeNull();
    expect(screen.queryAllByRole("radio")).toEqual([]);
    await userEvent.click(door);
    expect(door.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("textbox", { name: "server.json" })).toBeNull();
    expect(box("server address")).toBeTruthy();
  });

  it("checks the manifest with the server as you type, debounced", async () => {
    render(<AddServer />);
    await userEvent.type(box("server name"), "scout-tools");
    await userEvent.type(box("server address"), URL_);
    expect((await screen.findByText("Straza accepts this server.")).className).toContain("text-ok");
    expect(screen.getByRole("status").textContent).toBe("Straza accepts this server.");
    const calls = vi.mocked(dryRunApp).mock.calls;
    expect(calls.length).toBeLessThan(4);
    expect(calls[calls.length - 1][0]).toContain("url: " + URL_);
    vi.mocked(dryRunApp).mockRejectedValue(failure('manifest: invalid App "scout-tools": remote url must be absolute', 422));
    await userEvent.type(box("server address"), "x");
    expect((await screen.findByText(/remote url must be absolute/)).className).toContain("text-danger");
    vi.mocked(dryRunApp).mockRejectedValue(failure("unreachable", 0, true));
    await userEvent.type(box("server address"), "y");
    expect(await screen.findByText("Straza is unreachable, so the manifest is not checked here. Save and publish will say.")).toBeTruthy();
  });

  it("shows each credential row's fact and draws a remote static header server in the fold", async () => {
    await toCredential();
    expect(screen.getByText(LEDE.credential as string)).toBeTruthy();
    expect(screen.getByText(LEDE.credential as string).textContent).toBe("Agents never see the credential.");
    for (const c of CREDENTIAL_CHOICES) expect(credentialCard(c.kind).textContent).toBe(c.label + c.fact);
    for (const c of CREDENTIAL_CHOICES) expect(credentialCard(c.kind).querySelector(".font-mono")).toBeNull();
    expect(screen.getByText("Straza's audit still names who called.")).toBeTruthy();
    expect(screen.queryByText("Nothing to set for this choice.")).toBeNull();
    await userEvent.click(credentialCard("static"));
    expect(screen.getByLabelText("secret").getAttribute("type")).toBe("password");
    expect(screen.getByText("Stored encrypted. Never shown again.")).toBeTruthy();
    await choose("sent as", "A named header");
    await userEvent.type(screen.getByLabelText("secret"), "hunter2");
    await userEvent.type(box("header name"), "X-Demo-Key");
    expect(screen.getByText("X-Demo-Key: <secret>")).toBeTruthy();
    const yaml = await fold();
    for (const want of ["name: scout-tools", "url: " + URL_, "kind: static", "as: header", "name: X-Demo-Key", 'template: "{{secret}}"', '- "*"']) expect(yaml).toContain(want);
    expect(yaml).not.toContain("hunter2");
    expect(yaml).not.toContain("agents:");
  });

  it("greys the caller cards on a command runtime, and a sign-in pick falls back to none", async () => {
    await toCredential("cmd-tools");
    await userEvent.click(credentialCard("oauth"));
    expect(await fold()).toContain("kind: oauth");
    await click("button", "Back");
    await userEvent.click(runtimeCard("command"));
    await userEvent.type(box("executable"), "/usr/local/bin/mcp-server");
    expect(await fold()).not.toContain("credential:");
    await click("button", "Next: Credential");
    await heading("Credential");
    expect(credentialCard("none").getAttribute("aria-checked")).toBe("true");
    for (const kind of ["token", "oauth"]) {
      expect(credentialCard(kind).getAttribute("aria-disabled")).toBe("true");
      expect(document.getElementById(credentialCard(kind).getAttribute("aria-describedby") || "")?.textContent).toBe(CALLER_NOT_HERE);
    }
  });

  it("presets bearer on the token card, writes the agents rows and asks the shared secret", async () => {
    await toCredential();
    await userEvent.click(credentialCard("token"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A bearer token in Authorization");
    expect([...(document.querySelector("[data-credential-box]") as HTMLElement).querySelectorAll(".rounded-pill")].map((p) => p.textContent)).toEqual(["nothing yet", "each person pastes their own"]);
    expect(screen.queryByLabelText("secret")).toBeNull();
    expect(screen.queryByRole("textbox", { name: "header name" })).toBeNull();
    let yaml = await fold();
    for (const want of ["kind: token", "agents: own", "name: Authorization", 'template: "Bearer {{secret}}"']) expect(yaml).toContain(want);
    for (const r of AGENT_ROWS.token) expect(agentsRow(r.value).querySelector(".font-mono")).toBeNull();
    await userEvent.click(agentsRow("sponsor"));
    yaml = (document.querySelector("[data-manifest]") as HTMLElement).textContent || "";
    expect(yaml).toContain("agents: sponsor");
    expect(screen.queryByLabelText("secret")).toBeNull();
    await userEvent.click(agentsRow("shared"));
    expect(screen.getByText("Stored encrypted after you publish. Only agents use it.")).toBeTruthy();
    await click("button", "Next: Review");
    expect(await screen.findByText("Type the secret the server expects. Straza stores it encrypted after you publish.")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText("secret"));
    await choose("sent as", "A named header");
    await userEvent.type(screen.getByLabelText("secret"), "shared-1");
    await click("button", "Next: Review");
    expect(await screen.findByText("Name the header the token travels in.")).toBeTruthy();
    expect(document.activeElement).toBe(box("header name"));
  }, LOADED);

  it("blocks a named header without a name on the shared-secret card", async () => {
    await toCredential();
    await userEvent.click(credentialCard("static"));
    await choose("sent as", "A named header");
    await userEvent.type(screen.getByLabelText("secret"), "hunter2");
    await click("button", "Next: Review");
    expect(await screen.findByText("Name the header the secret travels in.")).toBeTruthy();
    expect(document.activeElement).toBe(box("header name"));
    expect(screen.getByRole("heading", { level: 2 }).textContent).toBe("Credential");
  });

  // The server refuses an inject with no name, and so does this stand-in,
  // so a half-filled credential sent to the live check shows as a refusal.
  const NO_NAME = 'manifest: invalid App "scout-tools": credential.inject.name is empty, so Straza does not know which header or environment variable carries the secret. Set it, for example Authorization for a header or API_TOKEN for an environment variable';
  const strict = (yaml: string) => (yaml.includes('name: ""') ? Promise.reject(failure(NO_NAME, 422)) : dryAnswer(yaml));
  const asked = () => vi.mocked(dryRunApp).mock.calls.map((c) => c[0]);
  const askedStatic = () => asked().filter((y) => y.includes("kind: static"));

  it("presets bearer on the shared-secret card of an HTTP server, and the live check accepts it", async () => {
    vi.mocked(dryRunApp).mockImplementation(strict);
    await toCredential();
    await userEvent.click(credentialCard("static"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A bearer token in Authorization");
    await waitFor(() => expect(askedStatic().length).toBeGreaterThan(0));
    for (const y of askedStatic()) for (const want of ["as: header", "name: Authorization", 'template: "Bearer {{secret}}"']) expect(y).toContain(want);
    await act(async () => { await vi.mocked(dryRunApp).mock.results.at(-1)?.value; });
    expect(document.querySelector('[data-dry-run="refused"]')).toBeNull();
  });

  it("asks the live check nothing while a named header has no name, and asks once it has one", async () => {
    vi.mocked(dryRunApp).mockImplementation(strict);
    await toCredential();
    await userEvent.click(credentialCard("static"));
    await choose("sent as", "A bearer token in Authorization");
    await waitFor(() => expect(asked().at(-1)).toContain("name: Authorization"));
    vi.mocked(dryRunApp).mockClear();
    await choose("sent as", "A named header");
    await act(() => new Promise((r) => setTimeout(r, 600)));
    expect(dryRunApp).not.toHaveBeenCalled();
    await userEvent.type(box("header name"), "X-Demo-Key");
    await waitFor(() => expect(asked().at(-1)).toContain("name: X-Demo-Key"));
    expect(asked().filter((y) => y.includes('name: ""'))).toEqual([]);
    expect(document.querySelector('[data-dry-run="refused"]')).toBeNull();
  }, LOADED);

  it("keeps a typed header name when the shared-secret card is picked after the token card", async () => {
    await toCredential();
    await userEvent.click(credentialCard("token"));
    await choose("sent as", "A named header");
    await userEvent.type(box("header name"), "X-Demo-Key");
    await userEvent.click(credentialCard("static"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A named header");
    expect((box("header name") as HTMLInputElement).value).toBe("X-Demo-Key");
    expect(await fold()).toContain("name: X-Demo-Key");
  });

  it("writes the provider, its scopes, the bearer inject and agents sponsor on the sign-in card", async () => {
    await toCredential();
    await userEvent.click(credentialCard("oauth"));
    expect(screen.getByRole("combobox", { name: "provider" }).textContent).toBe("keycloak");
    expect((box("scopes") as HTMLInputElement).value).toBe("openid profile");
    expect(document.querySelector("[data-credential-box]")?.textContent).toContain("Register http://localhost:8420/v1/connect/callback at keycloak.");
    expect(screen.queryByLabelText("secret")).toBeNull();
    const yaml = await fold();
    // An agent cannot sign in through a browser, so the card starts on the
    // value that gives an agent a way: its sponsor's sign-in.
    expect(agentsRow("sponsor", "oauth").getAttribute("aria-checked")).toBe("true");
    for (const want of ["kind: oauth", "agents: sponsor", "provider: keycloak", "- openid", "- profile", "as: header", "name: Authorization", 'template: "Bearer {{secret}}"']) expect(yaml).toContain(want);
  });

  it("writes a command runtime's shared secret as an environment variable", async () => {
    await toCredential("cmd-tools", "command");
    await userEvent.click(credentialCard("static"));
    expect(screen.queryByRole("combobox", { name: "sent as" })).toBeNull();
    expect(screen.getByText("an environment variable").className).toContain("rounded-pill");
    expect(screen.getByText(ENV_WARNING_SHORT)).toBeTruthy();
    await userEvent.type(screen.getByLabelText("secret"), "env-1");
    await click("button", "Next: Review");
    expect(await screen.findByText("Name the environment variable the process reads the secret from.")).toBeTruthy();
    await userEvent.type(box("variable name"), "API_TOKEN");
    const yaml = await fold();
    for (const want of ["kind: command", "exec: /usr/local/bin/mcp-server", "as: env", "name: API_TOKEN"]) expect(yaml).toContain(want);
    expect(yaml).not.toContain("as: header");
  });

  it("reviews the server's facts, the server's verdict, the rows, and the manifest under its closed fold", async () => {
    await toReview();
    const facts = await waitFor(() => {
      const el = document.querySelector("[data-server-card] [data-review-facts]") as HTMLElement;
      expect(el.textContent).toContain("30 s per call");
      return el;
    });
    expect([...facts.querySelectorAll("dt")].map((d) => d.textContent)).toEqual(["Name", "Runs", "Credential", "Tools", "Limits"]);
    expect(facts.querySelector('[data-fact="Runs"]')?.textContent).toBe("Over HTTP at " + URL_);
    expect(facts.querySelector('[data-fact="Credential"]')?.textContent).toMatch(/^One shared secret, sent in the X-Demo-Key header/);
    expect(screen.getByText("Stored encrypted after publishing. Never shown again.")).toBeTruthy();
    const verdict = await waitFor(() => {
      const el = document.querySelector('[data-verdict="ok"]') as HTMLElement;
      expect(el).not.toBeNull();
      return el;
    });
    expect(verdict.textContent).toBe("accepted");
    const rows = [...(document.querySelector("[data-landed]") as HTMLElement).querySelectorAll("li")].map((li) => li.textContent);
    expect(rows).toEqual(["1Publish scout-tools", "2Store its secret", "3Check scout-tools"]);
    expect(textOf("[data-nobody-until-role]")).toBe("Nobody can call it until a role gives access to its tools.");
    expect(screen.getByRole("heading", { level: 3, name: "What will happen" })).toBeTruthy();
    expect(await fold()).toContain("name: X-Demo-Key");
    expect(footButtons()).toEqual(["Back", "Save draft", "Save and publish"]);
    expect(screen.getByText(SECRET_WAITS)).toBeTruthy();
  }, LOADED);

  // Each case is what the dry run answers on Review, the state of the
  // banner that says so, and the tone of its fill.
  const verdicts: [string, Error | null, string, string][] = [
    ["accepts the manifest", null, "ok", "bg-ok-bg"],
    ["refuses it", failure('manifest: invalid App "scout-tools": remote url must be absolute', 422), "refused", "bg-danger-bg"],
    ["is not reached", failure("unreachable", 0, true), "unreachable", "bg-unknown-bg"],
  ];
  it.each(verdicts)("sets the verdict as one banner when the server %s", async (_case, answer, state, fill) => {
    await toCredential();
    if (answer) vi.mocked(dryRunApp).mockRejectedValue(answer);
    await click("button", "Next: Review");
    await heading("Review");
    const strip = await waitFor(() => {
      const el = document.querySelector('[data-verdict="' + state + '"]') as HTMLElement;
      expect(el).not.toBeNull();
      return el;
    });
    expect(strip.className.split(" ")).toContain(fill);
  });

  // Each case is the list before and after the steps run: what leads a row
  // and the width of the column it sits in.
  const leads: [string, boolean, string[], string][] = [
    ["its number in a narrow column before the steps run", true, ["1", "2"], "w-4"],
    ["its state in the state column once they ran", false, ["done", "failed"], "w-16"],
  ];
  it.each(leads)("leads each row of What will happen with %s", (_case, numbered, marks, width) => {
    render(<Landed rows={[{ key: "install", label: "Publish scout-tools", state: "done" }, { key: "check", label: "Check scout-tools", state: "failed" }]} numbered={numbered} />);
    const lead = [...document.querySelectorAll("[data-landed] li > span:first-child")];
    expect(lead.map((s) => s.textContent)).toEqual(marks);
    expect(lead.every((s) => s.className.split(" ").includes(width))).toBe(true);
  });

  it("keeps both saves aria-disabled with the server's sentence when the dry run refused", async () => {
    await toReview();
    vi.mocked(dryRunApp).mockRejectedValue(failure('manifest: invalid App "scout-tools": credential.inject.name is required', 422));
    await click("button", "Back");
    await click("button", "Next: Review");
    await screen.findByText("refused");
    for (const name of ["Save draft", "Save and publish"]) {
      const button = foot().getByRole("button", { name });
      expect(button.getAttribute("aria-disabled")).toBe("true");
      expect(button.getAttribute("title")).toContain("credential.inject.name is required");
      await userEvent.click(button);
    }
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("publishes the YAML as one App put, then stores the secret app-scoped, probes, and ends on What next for a running server", async () => {
    await toReview();
    const sent = (document.querySelector("[data-manifest]") as HTMLElement).textContent || "";
    await click("button", "Save and publish");
    await heading("Check");
    const items = [{ kind: "App", name: "scout-tools", op: "put", doc: sent }];
    expect(checkDraft).toHaveBeenCalledWith({ items });
    expect(createDraft).toHaveBeenCalledWith({ items });
    expect(publishDraft).toHaveBeenCalledWith("60", { revision: 1, risk_digest: "", ticked: [], typed: {} });
    expect(setSecret).toHaveBeenCalledWith("app-9", "hunter2");
    expect(recheckApp).toHaveBeenCalledWith("app-9");
    expect(vi.mocked(publishDraft).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(setSecret).mock.invocationCallOrder[0]);
    expect(vi.mocked(setSecret).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(recheckApp).mock.invocationCallOrder[0]);
    expect(vi.mocked(notify.undo).mock.calls[0][0]).toBe("scout-tools is live.");
    expect(vi.mocked(notify.undo).mock.calls[0][1]).toBe("Undo");
    const next = document.querySelector("[data-what-next]") as HTMLElement;
    expect(textOf("[data-live]")).toBe("scout-tools is live. Straza reached it and read 3 tools.");
    // What next ends the wizard, so no step reads as the current one.
    expect(document.querySelector('[aria-current="step"]')).toBeNull();
    expect(within(next).getByRole("heading", { level: 3, name: "What next" })).toBeTruthy();
    expect(document.querySelector("[data-landed]")).toBeNull();
    expect(document.querySelector("[data-nobody-reaches]")).toBeNull();
    expect([...next.querySelectorAll("[data-next-tile]")].map((t) => t.textContent)).toEqual([
      "Give access to itMake an application role that reaches some or all of its 3 tools.",
      "Write a policy for itMake calls to its tools need approval, deny them or record them.",
      "Open scout-tools",
    ]);
    await userEvent.click(within(next).getByRole("button", { name: "As a strazactl command" }));
    expect(within(next).getByText(/strazactl roles create/).textContent).toBe("strazactl roles create scout-tools-<word> --app scout-tools --tools <tool>,<tool>");
    await userEvent.click(within(next).getByRole("button", { name: "Call it from an agent's machine, once a role reaches it" }));
    const snippet = [...document.querySelectorAll("pre")].map((p) => p.textContent || "").find((t) => t.includes("tools/call")) || "";
    expect(snippet).toContain("scout-tools__echo");
    expect(snippet).toContain("straza mcp --harness claude-code");
    expect(footButtons()).toEqual(["Download app.yaml", "Add another server", "Test a call"]);
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
    await click("button", "Test a call");
    expect(document.querySelector("[data-test-a-call]")?.textContent).toBe("scout-tools: echo, get-sum, get-env");
    await userEvent.click(next.querySelector('[data-next-tile="servers"]') as HTMLElement);
    expect(navigate).toHaveBeenCalledWith("servers", ["app-9"]);
  }, LOADED);

  it("opens New role on the server it just added, and New policy on its calls, through the hand-offs", async () => {
    await toCheck();
    await userEvent.click(screen.getByRole("button", { name: /^Give access to it/ }));
    expect(navigate).toHaveBeenCalledWith("roles", ["new"]);
    expect(take("roles-new")).toEqual({ server: "scout-tools" });
    expect(take("roles-new")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /^Write a policy for it/ }));
    expect(navigate).toHaveBeenCalledWith("policies", ["new"]);
    expect(take("policies-new")).toEqual({ lane: "mcp", app: "scout-tools" });
  }, LOADED);

  it("tells a refused check from an unreachable one, each with its doors", async () => {
    vi.mocked(recheckApp).mockResolvedValue(scout({ status: "degraded", detail: "ping: upstream answered 401 Unauthorized", tools: [] }));
    await toCheck();
    expect(document.querySelector("[data-health]")?.textContent).toBe("Refused. " + URL_ + " answered 401 to the secret you stored. Check the value with whoever issued it, set it again, then Recheck.");
    expect(document.querySelector("[data-landed]")?.textContent).toContain("refusedCheck scout-tools");
    expect(footButtons()).toEqual(["Set it again", "Recheck", "Open scout-tools"]);
    vi.mocked(recheckApp).mockResolvedValue(scout({ status: "degraded", detail: "connect failed: dial tcp 172.20.0.3:3001: connect: connection refused", tools: [] }));
    await click("button", "Recheck");
    await waitFor(() => expect(document.querySelector("[data-health]")?.getAttribute("data-health")).toBe("unreachable"));
    expect(document.querySelector("[data-health]")?.textContent).toContain("Unreachable. Straza could not reach " + URL_ + " (connection refused). Fix the address or start the server, then Recheck.");
    expect(footButtons()).toEqual(["Recheck", "Open scout-tools"]);
    expect(notify.warn).toHaveBeenCalledWith("scout-tools is degraded. Straza could not reach " + URL_ + " (connection refused).");
    await click("button", "Open scout-tools");
    expect(navigate).toHaveBeenCalledWith("servers", ["app-9"]);
    expect(publishDraft).toHaveBeenCalledTimes(1);
  });

  // The whole install walk plus typed input takes about half of vitest's 5 s
  // default alone, so it carries its own budget for a loaded full suite.
  it("says the server exists on Cancel after the publish, and Set it again never publishes twice", async () => {
    vi.mocked(recheckApp).mockResolvedValue(scout({ status: "degraded", detail: "ping: upstream answered 401 Unauthorized", tools: [] }));
    await toCheck();
    await click("button", "Set it again");
    await heading("Credential");
    expect(screen.getByText("Storing replaces the stored secret, then runs the check.")).toBeTruthy();
    // The installed choice stands as facts, and only the secret is asked.
    expect(textOf("[data-installed-choice]")).toBe("One shared secretone identityheader X-Demo-Key");
    expect(screen.getByText("To change the choice, open the server's page.")).toBeTruthy();
    expect(screen.queryAllByRole("radio")).toEqual([]);
    expect(footButtons()).toEqual(["Store and check"]);
    await click("button", "Cancel");
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Leave the wizard?");
    expect(dialog.textContent).toContain("scout-tools is published and stays degraded.");
    expect(dialog.textContent).not.toContain("Nothing has been created yet");
    await click("button", "Keep going");
    await click("button", "Store and check");
    expect(await screen.findByText("Type the secret the server expects. Straza stores it encrypted after you publish.")).toBeTruthy();
    vi.mocked(recheckApp).mockResolvedValue(scout());
    await userEvent.type(screen.getByLabelText("secret"), "correct-horse");
    await click("button", "Store and check");
    await heading("Check");
    expect(publishDraft).toHaveBeenCalledTimes(1);
    expect(setSecret).toHaveBeenLastCalledWith("app-9", "correct-horse");
    expect(textOf("[data-live]")).toBe("scout-tools is live. Straza reached it and read 3 tools.");
  }, 15000);

  it("keeps a manifest the server refuses on Review with nothing stored, the secret scan included", async () => {
    await toReview();
    const scan = { code: "secret.token", class: "refused" as const, object: "App/scout-tools", key: "k-s", sentence: "In the server scout-tools, metadata.description holds what looks like a GitHub token.", fix: "Store the secret with strazactl apps secret set scout-tools." };
    vi.mocked(checkDraft).mockResolvedValueOnce({ items: [], verdict: { ...CLEAN, refused: [scan] } });
    await click("button", "Save and publish");
    const note = await waitFor(() => { const el = document.querySelector("[data-save-note]") as HTMLElement; expect(el).not.toBeNull(); return el; });
    expect(note.textContent).toBe("Nothing was saved." + scan.sentence + " " + scan.fix);
    expect(createDraft).not.toHaveBeenCalled();
    expect(setSecret).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { level: 2, name: "Review" })).toBeTruthy();
    vi.mocked(publishDraft).mockRejectedValueOnce(new ApiError("unreachable", 0, true));
    await click("button", "Save and publish");
    await waitFor(() => expect(document.querySelector("[data-save-note]")?.getAttribute("data-save-note")).toBe("unreachable"));
    expect(document.querySelector("[data-save-note]")?.textContent).toBe("Straza did not answer. The change may have been saved. Reload to check before trying again.");
    expect(setSecret).not.toHaveBeenCalled();
  });

  it("saves the manifest to the working draft and keeps the secret out of it", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: { ...DRAFT60, id: "39" }, verdict: CLEAN });
    await toReview();
    const sent = (document.querySelector("[data-manifest]") as HTMLElement).textContent || "";
    await click("button", "Save draft");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("drafts", ["39"]));
    expect(createDraft).toHaveBeenCalledWith({ items: [{ kind: "App", name: "scout-tools", op: "put", doc: sent }], working: true });
    expect(sent).not.toContain("hunter2");
    expect(setSecret).not.toHaveBeenCalled();
    expect(publishDraft).not.toHaveBeenCalled();
  });

  it("says a published server the list does not name yet, and runs no secret or check", async () => {
    vi.mocked(publishDraft).mockResolvedValue({ draft: { ...DRAFT60, state: "published" }, snapshot: "3be0a1", servers: [], next: [] });
    await toReview();
    await click("button", "Save and publish");
    expect((await screen.findByText(/is published but not listed yet/)).closest("[data-fetch-error]")?.textContent).toBe("Check did not run. scout-tools is published but not listed yet. Open MCP servers, then set the secret on its page.");
    expect(setSecret).not.toHaveBeenCalled();
    expect(recheckApp).not.toHaveBeenCalled();
  });

  it("asks before discarding the answers and leaves a clean form at once", async () => {
    render(<AddServer />);
    await click("button", "Cancel");
    expect(navigate).toHaveBeenCalledWith("servers");
    vi.mocked(navigate).mockReset();
    await userEvent.type(box("server name"), "scout-tools");
    await click("button", "Cancel");
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Discard these answers?");
    expect(dialog.textContent).toContain("Nothing has been created yet. Discarding loses the name scout-tools and the answers so far.");
    expect(within(dialog).getByRole("button", { name: "Discard the answers" }).className).toContain("bg-danger");
    await click("button", "Keep editing");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("converts a pasted server.json on the server and opens the way it read", async () => {
    render(<AddServer />);
    await click("button", IMPORT_RECORD);
    await click("button", "Next: Credential");
    expect(await screen.findByText("Paste the registry's server.json and click Convert. To type the address yourself, click Import a registry record again.")).toBeTruthy();
    expect(document.activeElement).toBe(box("server.json"));
    await userEvent.click(box("server.json"));
    await userEvent.paste("not json");
    await click("button", "Convert");
    expect(screen.getByText("This is not JSON. Paste the server.json record from the registry as it is.")).toBeTruthy();
    const record = { name: "io.github.example/weather", version: "1.0.2", remotes: [{ type: "streamable-http", url: "https://registry.example/other" }] };
    vi.mocked(importServerJSON).mockResolvedValue({ name: "weather", runtime: "remote", manifest: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n    name: weather\nserver:\n    remotes:\n        - type: streamable-http\n          url: https://registry.example/other\nstraza:\n    runtime:\n        kind: remote\n        remote:\n            url: https://weather.example/mcp\n            auth: inject\n" });
    await userEvent.clear(box("server.json"));
    await userEvent.paste(JSON.stringify(record));
    await click("button", "Convert");
    expect(await screen.findByText("Converted weather and filled in the fields.")).toBeTruthy();
    expect(importServerJSON).toHaveBeenCalledWith(record);
    expect(runtimeCard("remote").getAttribute("aria-checked")).toBe("true");
    expect((box("server address") as HTMLInputElement).value).toBe("https://weather.example/mcp");
    expect((box("server name") as HTMLInputElement).value).toBe("weather");
    const yaml = await fold();
    expect(yaml).toContain("name: io.github.example/weather");
    expect(yaml).toContain("url: https://weather.example/mcp");
  });
  it("is locked for a session that administers servers and may not register, and asks for none of the lists", async () => {
    sess.areas = {};
    sess.servers = 2;
    render(<AddServer />);
    expect(await screen.findByText("Add MCP server is not available to this account.")).toBeTruthy();
    expect(screen.getByText("Adding a server needs straza-global-mcp-admin.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open MCP servers" })).toBeTruthy();
    expect(listApps).not.toHaveBeenCalled();
    sess.areas = null;
    sess.servers = 0;
  });

  describe("a door out of the wizard", () => {
    // The breadcrumb, the rail, the header and the palette call the
    // router's own navigate, which asks the guard the wizard registers.
    let router: typeof import("@/lib/router");
    beforeEach(async () => {
      router = await vi.importActual<typeof import("@/lib/router")>("@/lib/router");
      router.replaceAddress(router.pathFor("servers", ["new"]));
    });

    it("passes while nothing is typed", async () => {
      render(<AddServer />);
      act(() => router.navigate("users"));
      expect(window.location.pathname).toBe(router.pathFor("users"));
      expect(screen.queryByRole("alertdialog")).toBeNull();
    });

    it("asks Cancel's question once an answer is typed, and goes on its confirm without asking again", async () => {
      render(<AddServer />);
      await userEvent.type(box("server name"), "scout-tools");
      act(() => router.navigate("users"));
      const dialog = await screen.findByRole("alertdialog");
      expect(dialog.textContent).toContain("Discard these answers?");
      expect(window.location.pathname).toBe(router.pathFor("servers", ["new"]));
      await userEvent.click(within(dialog).getByRole("button", { name: "Discard the answers" }));
      expect(window.location.pathname).toBe(router.pathFor("users"));
      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
      expect(navigate).not.toHaveBeenCalled();
    });

    it("lets a Save draft that landed open its draft without asking", async () => {
      vi.mocked(createDraft).mockResolvedValue({ draft: { ...DRAFT60, id: "39" }, verdict: CLEAN });
      vi.mocked(navigate).mockImplementation(router.navigate);
      await toReview();
      await click("button", "Save draft");
      await waitFor(() => expect(window.location.pathname).toBe(router.pathFor("drafts", ["39"])));
      expect(screen.queryByRole("alertdialog")).toBeNull();
    }, LOADED);
  });
});
