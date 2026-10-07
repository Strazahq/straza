import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AddServer } from "./add-server";
import { Landed } from "@/components/wizard/parts";
import { ApiError, type DraftVerdict, dryRunApp, importServerJSON, listApps, listProviders, recheckApp, setSecret } from "@/lib/api";
import { checkDraft, createDraft, publishDraft } from "@/lib/drafts-api";
import { take } from "@/lib/handoff";
import { NAME_RULE } from "@/lib/manifest";
import { navigate } from "@/lib/router";
import { notify } from "@/lib/notify";
import { version } from "@/lib/public";
import { SECRET_WAITS } from "@/lib/save-words";
import { CALLER_OFF_SHORT, COMMAND_REFUSED, ENV_WARNING_SHORT, LEDE, OCI_MISSING, OCI_MISSING_REMOTE, credentialCards } from "@/lib/words";
import { VERDICT } from "@/test/drafts-fixture";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  listApps: vi.fn(), listProviders: vi.fn(), dryRunApp: vi.fn(), setSecret: vi.fn(), recheckApp: vi.fn(), importServerJSON: vi.fn(),
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
const heading = (name: string) => screen.findByRole("heading", { level: 2, name });
// LOADED is the budget of a case that ran out of vitest's 5 s, or of a wait
// that ran out of testing-library's 1 s, under make ui-test's full load,
// and that passes alone well inside both.
const LOADED = 30000;
const foot = () => within(document.querySelector("[data-wizard-foot]") as HTMLElement);
const footButtons = () => foot().getAllByRole("button").map((b) => b.textContent);
const fold = async () => {
  await click("button", "The manifest this writes (app.yaml)");
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
    await userEvent.click(radio("runtime command"));
    await userEvent.type(box("executable"), "/usr/local/bin/mcp-server");
  } else await userEvent.type(box("url"), URL_);
  await click("button", "Next");
  await heading("Credential");
}
// toReview picks a static secret sent in the named header X-Demo-Key.
async function toReview() {
  await toCredential();
  await userEvent.click(radio("credential static"));
  await userEvent.type(screen.getByLabelText("secret"), "hunter2");
  await choose("sent as", "A named header");
  await userEvent.type(box("header name"), "X-Demo-Key");
  await click("button", "Next");
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
    vi.mocked(navigate).mockReset();
    vi.mocked(notify.ok).mockReset();
    vi.mocked(notify.warn).mockReset();
    vi.mocked(notify.failed).mockReset();
  });

  it("opens on the Server step under the step strip, with its lede and the HTTP card", async () => {
    render(<AddServer />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("Add MCP server");
    const steps = within(screen.getByRole("list", { name: "wizard steps" })).getAllByRole("listitem");
    expect(steps.map((s) => s.textContent)).toEqual(["1Server", "2Credential", "3Review", "4Check"]);
    expect(steps[0].getAttribute("aria-current")).toBe("step");
    expect((await heading("Server")).textContent).toBe("Server");
    expect(screen.getByText(LEDE.server)).toBeTruthy();
    expect(radio("runtime remote").getAttribute("aria-checked")).toBe("true");
    expect(document.activeElement).toBe(box("server name"));
    expect(screen.getByRole("status").textContent).toBe("The manifest is checked with the server once it has a name and an address.");
  });

  it("confirms a free name and applies the manifest rule as you type", async () => {
    render(<AddServer />);
    await userEvent.type(box("server name"), "scout-tools");
    expect(await screen.findByText("scout-tools is free")).toBeTruthy();
    await userEvent.clear(box("server name"));
    await userEvent.type(box("server name"), "Bad Name");
    expect(screen.getByText(NAME_RULE)).toBeTruthy();
  });

  it("stops at a twin name and offers Open it", async () => {
    render(<AddServer />);
    await userEvent.type(box("server name"), "demo-tools");
    await userEvent.type(box("url"), URL_);
    expect((await screen.findByText(/A server named demo-tools is already installed\./)).textContent).toContain("Open it");
    await click("button", "Next");
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
  const TAKEN = "Nothing was saved.A server named scout-tools is already installed. Go back to Server and pick another name.";
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
    await userEvent.type(box("url"), URL_);
    await click("button", "Next");
    await screen.findByRole("heading", { level: 2, name: "Credential" }, wait);
    await click("button", "Next");
    await screen.findByRole("heading", { level: 2, name: "Review" }, wait);
    await click("button", "Save and publish");
    await waitFor(() => expect(saveNote()).not.toBeNull(), wait);
    expect(saveNote()!.textContent).toBe(TAKEN);
    for (const fn of [createDraft, publishDraft]) expect(fn).not.toHaveBeenCalled();
  }, LOADED);

  it("keeps Next clickable, focuses the first missing field and says what is missing", async () => {
    render(<AddServer />);
    const next = foot().getByRole("button", { name: "Next" });
    expect(next.hasAttribute("disabled") || next.getAttribute("aria-disabled") === "true").toBe(false);
    await userEvent.click(next);
    expect(await screen.findByText("Name the server. " + NAME_RULE)).toBeTruthy();
    expect(screen.getByText("Say where Straza reaches it: an http or https address.")).toBeTruthy();
    expect(document.activeElement).toBe(box("server name"));
    await userEvent.type(box("server name"), "scout-tools");
    await waitFor(() => expect(screen.queryByText("Name the server. " + NAME_RULE)).toBeNull());
    await userEvent.type(box("url"), "ftp://demo-tools");
    await userEvent.click(next);
    expect(await screen.findByText("The address must start with http:// or https://.")).toBeTruthy();
    expect(document.activeElement).toBe(box("url"));
  });

  it("greys the container card with the docker sentence, and opens it when /version lists no runtimes", async () => {
    const first = render(<AddServer />);
    await waitFor(() => expect(radio("runtime oci").getAttribute("aria-disabled")).toBe("true"));
    expect(radio("runtime oci").textContent).toContain(OCI_MISSING);
    await userEvent.click(radio("runtime oci"));
    expect(radio("runtime oci").getAttribute("aria-checked")).toBe("false");
    expect(radio("runtime import").parentElement?.className).toContain("border-dashed");
    first.unmount();
    vi.mocked(version).mockReset().mockResolvedValue(null);
    render(<AddServer />);
    await waitFor(() => expect(version).toHaveBeenCalled());
    await userEvent.click(radio("runtime oci"));
    expect(radio("runtime oci").getAttribute("aria-disabled")).toBeNull();
    expect(box("image")).toBeTruthy();
  });

  it("greys the command card with the enterprise sentence, and the container card names HTTP only, when /version lists no command runtime", async () => {
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "enterprise", runtimes: ["remote"] });
    render(<AddServer />);
    await waitFor(() => expect(radio("runtime command").getAttribute("aria-disabled")).toBe("true"));
    expect(radio("runtime command").textContent).toContain(COMMAND_REFUSED);
    expect(radio("runtime oci").textContent).toContain(OCI_MISSING_REMOTE);
    await userEvent.click(radio("runtime command"));
    expect(radio("runtime command").getAttribute("aria-checked")).toBe("false");
    expect(screen.queryByRole("textbox", { name: "executable" })).toBeNull();
  });

  it("checks the manifest with the server as you type, debounced", async () => {
    render(<AddServer />);
    await userEvent.type(box("server name"), "scout-tools");
    await userEvent.type(box("url"), URL_);
    expect(await screen.findByText("The server accepts this manifest: remote runtime, credential none.")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("Checked with the server as you type, the same code path as strazactl spec validate.");
    const calls = vi.mocked(dryRunApp).mock.calls;
    expect(calls.length).toBeLessThan(4);
    expect(calls[calls.length - 1][0]).toContain("url: " + URL_);
    vi.mocked(dryRunApp).mockRejectedValue(failure('manifest: invalid App "scout-tools": remote url must be absolute', 422));
    await userEvent.type(box("url"), "x");
    expect((await screen.findByText(/remote url must be absolute/)).className).toContain("text-danger");
    vi.mocked(dryRunApp).mockRejectedValue(failure("unreachable", 0, true));
    await userEvent.type(box("url"), "y");
    expect(await screen.findByText("strazad is unreachable, so the manifest is not checked here. Save and publish will say.")).toBeTruthy();
  });

  it("shows each credential card's line and draws a remote static header server in the fold", async () => {
    await toCredential();
    expect(screen.getByText(LEDE.credential)).toBeTruthy();
    for (const c of credentialCards("keycloak")) expect(radio("credential " + c.kind).textContent).toContain(c.line);
    expect(screen.getByText("Nothing to set for this choice.")).toBeTruthy();
    await userEvent.click(radio("credential static"));
    expect(screen.getByLabelText("secret").getAttribute("type")).toBe("password");
    expect(screen.getByText("Stored sealed. Never shown again.")).toBeTruthy();
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
    await userEvent.click(radio("credential oauth"));
    expect(await fold()).toContain("kind: oauth");
    await click("button", "Back");
    await userEvent.click(radio("runtime command"));
    await userEvent.type(box("executable"), "/usr/local/bin/mcp-server");
    expect(await fold()).not.toContain("credential:");
    await click("button", "Next");
    await heading("Credential");
    expect(radio("credential none").getAttribute("aria-checked")).toBe("true");
    for (const kind of ["token", "oauth"]) {
      expect(radio("credential " + kind).getAttribute("aria-disabled")).toBe("true");
      expect(radio("credential " + kind).textContent).toContain(CALLER_OFF_SHORT);
    }
  });

  it("presets bearer on the token card, writes the agents rows and asks the shared secret", async () => {
    await toCredential();
    await userEvent.click(radio("credential token"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A bearer token in Authorization");
    expect(screen.getByText("Nothing yet. People paste their own token on the Credentials tab of their self-service page after install.")).toBeTruthy();
    expect(screen.queryByLabelText("secret")).toBeNull();
    expect(screen.queryByRole("textbox", { name: "header name" })).toBeNull();
    let yaml = await fold();
    for (const want of ["kind: token", "agents: own", "name: Authorization", 'template: "Bearer {{secret}}"']) expect(yaml).toContain(want);
    await userEvent.click(radio("agents sponsor"));
    yaml = (document.querySelector("[data-manifest]") as HTMLElement).textContent || "";
    expect(yaml).toContain("agents: sponsor");
    expect(screen.queryByLabelText("secret")).toBeNull();
    await userEvent.click(radio("agents shared"));
    expect(screen.getByText("Stored sealed after install. Only agents use it.")).toBeTruthy();
    await click("button", "Next");
    expect(await screen.findByText("Type the secret the server expects. It is stored sealed after the install.")).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText("secret"));
    await choose("sent as", "A named header");
    await userEvent.type(screen.getByLabelText("secret"), "shared-1");
    await click("button", "Next");
    expect(await screen.findByText("Name the header the token travels in.")).toBeTruthy();
    expect(document.activeElement).toBe(box("header name"));
  }, LOADED);

  it("blocks a named header without a name on the shared-secret card", async () => {
    await toCredential();
    await userEvent.click(radio("credential static"));
    await choose("sent as", "A named header");
    await userEvent.type(screen.getByLabelText("secret"), "hunter2");
    await click("button", "Next");
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
    await userEvent.click(radio("credential static"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A bearer token in Authorization");
    await waitFor(() => expect(askedStatic().length).toBeGreaterThan(0));
    for (const y of askedStatic()) for (const want of ["as: header", "name: Authorization", 'template: "Bearer {{secret}}"']) expect(y).toContain(want);
    await act(async () => { await vi.mocked(dryRunApp).mock.results.at(-1)?.value; });
    expect(document.querySelector('[data-dry-run="refused"]')).toBeNull();
  });

  it("asks the live check nothing while a named header has no name, and asks once it has one", async () => {
    vi.mocked(dryRunApp).mockImplementation(strict);
    await toCredential();
    await userEvent.click(radio("credential static"));
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
    await userEvent.click(radio("credential token"));
    await choose("sent as", "A named header");
    await userEvent.type(box("header name"), "X-Demo-Key");
    await userEvent.click(radio("credential static"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A named header");
    expect((box("header name") as HTMLInputElement).value).toBe("X-Demo-Key");
    expect(await fold()).toContain("name: X-Demo-Key");
  });

  it("writes the provider, its scopes, the bearer inject and agents sponsor on the sign-in card", async () => {
    await toCredential();
    await userEvent.click(radio("credential oauth"));
    expect(screen.getByRole("combobox", { name: "provider" }).textContent).toBe("keycloak");
    expect((box("scopes") as HTMLInputElement).value).toBe("openid profile");
    expect(document.querySelector("[data-credential-box]")?.textContent).toContain("Register http://localhost:8420/v1/connect/callback at keycloak.");
    expect(screen.queryByLabelText("secret")).toBeNull();
    const yaml = await fold();
    // An agent cannot sign in through a browser, so the card starts on the
    // value that gives an agent a way: its sponsor's sign-in.
    expect(radio("agents sponsor").getAttribute("aria-checked")).toBe("true");
    for (const want of ["kind: oauth", "agents: sponsor", "provider: keycloak", "- openid", "- profile", "as: header", "name: Authorization", 'template: "Bearer {{secret}}"']) expect(yaml).toContain(want);
  });

  it("writes a command runtime's shared secret as an environment variable", async () => {
    await toCredential("cmd-tools", "command");
    await userEvent.click(radio("credential static"));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("An environment variable");
    expect(screen.getByText(ENV_WARNING_SHORT)).toBeTruthy();
    await userEvent.type(screen.getByLabelText("secret"), "env-1");
    await click("button", "Next");
    expect(await screen.findByText("Name the environment variable the process reads the secret from.")).toBeTruthy();
    await userEvent.type(box("variable name"), "API_TOKEN");
    const yaml = await fold();
    for (const want of ["kind: command", "exec: /usr/local/bin/mcp-server", "as: env", "name: API_TOKEN"]) expect(yaml).toContain(want);
    expect(yaml).not.toContain("as: header");
  });

  it("reviews the manifest, the server's verdict, the secret's fate and the rows", async () => {
    await toReview();
    const sent = (document.querySelector("pre") as HTMLElement).textContent || "";
    expect(sent).toContain("name: X-Demo-Key");
    expect((await screen.findByText(/Checked with the dry run a moment ago\./)).textContent).toBe("The server accepts this manifest: remote runtime, credential static. Checked with the dry run a moment ago.");
    expect(screen.getByText("accepted")).toBeTruthy();
    expect(screen.getByText(/The secret you typed is stored sealed once scout-tools is installed/)).toBeTruthy();
    const rows = [...(document.querySelector("[data-landed]") as HTMLElement).querySelectorAll("li")].map((li) => li.textContent);
    expect(rows).toEqual(["1Publish scout-tools", "2Store its secret", "3Check scout-tools: Straza starts it or pings it and reads its tool list"]);
    expect(footButtons()).toEqual(["Back", "Save draft", "Save and publish"]);
    expect(screen.getByText(SECRET_WAITS)).toBeTruthy();
  });

  // Each case is what the dry run answers on Review and the state of the
  // strip that says so.
  const verdicts: [string, Error | null, string][] = [
    ["accepts the manifest", null, "ok"],
    ["refuses it", failure('manifest: invalid App "scout-tools": remote url must be absolute', 422), "refused"],
    ["is not reached", failure("unreachable", 0, true), "unreachable"],
  ];
  it.each(verdicts)("sets the verdict at section size when the server %s", async (_case, answer, state) => {
    await toCredential();
    if (answer) vi.mocked(dryRunApp).mockRejectedValue(answer);
    await click("button", "Next");
    await heading("Review");
    const strip = await waitFor(() => {
      const el = document.querySelector('[data-verdict="' + state + '"]') as HTMLElement;
      expect(el).not.toBeNull();
      return el;
    });
    expect(strip.className.split(" ")).toEqual(expect.arrayContaining(["text-lg", "font-semibold"]));
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
    await click("button", "Next");
    await screen.findByText("refused");
    for (const name of ["Save draft", "Save and publish"]) {
      const button = foot().getByRole("button", { name });
      expect(button.getAttribute("aria-disabled")).toBe("true");
      expect(button.getAttribute("title")).toContain("credential.inject.name is required");
      await userEvent.click(button);
    }
    expect(checkDraft).not.toHaveBeenCalled();
  });

  it("publishes the YAML as one App put, then stores the secret app-scoped, probes, and opens the doors of a running server", async () => {
    await toReview();
    const sent = (document.querySelector("pre") as HTMLElement).textContent || "";
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
    expect(vi.mocked(notify.undo).mock.calls[0][0]).toBe("scout-tools is live with your change.");
    const rows = [...(document.querySelector("[data-landed]") as HTMLElement).querySelectorAll("li")].map((li) => li.textContent);
    expect(rows).toEqual(["donePublish scout-tools", "doneStore its secret", "doneCheck scout-tools"]);
    expect(document.querySelector("[data-health]")?.textContent).toBe("Running. 3 tools found: echo, get-sum, get-env. Checked just now.");
    const card = document.querySelector("[data-nobody-reaches]") as HTMLElement;
    expect(within(card).getByText("Nobody reaches scout-tools yet.")).toBeTruthy();
    expect(document.body.textContent).not.toContain("No role reaches");
    expect(within(card).getByRole("button", { name: "Create a role for scout-tools" })).toBeTruthy();
    expect(within(card).getByText("An application role gives access to its tools and says which calls need a person's approval.")).toBeTruthy();
    await userEvent.click(within(card).getByRole("button", { name: "The same as commands" }));
    expect(within(card).getByText(/strazactl roles create/).textContent).toBe("strazactl roles create scout-tools-<word> --app scout-tools --tools <tool>,<tool>");
    const snippet = [...document.querySelectorAll("pre")].map((p) => p.textContent || "").find((t) => t.includes("tools/call")) || "";
    expect(snippet).toContain("scout-tools__echo");
    expect(snippet).toContain("straza mcp --harness claude-code");
    expect(footButtons()).toEqual(["Download app.yaml", "Add another server", "Test a call", "Open scout-tools"]);
    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
    await click("button", "Test a call");
    expect(document.querySelector("[data-test-a-call]")?.textContent).toBe("scout-tools: echo, get-sum, get-env");
    await click("button", "Open scout-tools");
    expect(navigate).toHaveBeenCalledWith("servers", ["app-9"]);
  });

  it("opens New role on the server it just added, through the hand-off", async () => {
    await toCheck();
    await userEvent.click(within(document.querySelector("[data-nobody-reaches]") as HTMLElement).getByRole("button", { name: "Create a role for scout-tools" }));
    expect(navigate).toHaveBeenCalledWith("roles", ["new"]);
    expect(take("roles-new")).toEqual({ server: "scout-tools" });
    expect(take("roles-new")).toBeNull();
  });

  it("tells a refused check from an unreachable one, each with its doors", async () => {
    vi.mocked(recheckApp).mockResolvedValue(scout({ status: "degraded", detail: "ping: upstream answered 401 Unauthorized", tools: [] }));
    await toCheck();
    expect(document.querySelector("[data-health]")?.textContent).toBe("Refused. " + URL_ + " answered 401 to the secret you stored. Check the value with whoever issued it, set it again, then Recheck. The server exists and keeps this state; you can finish access later from its page.");
    expect(document.querySelector("[data-landed]")?.textContent).toContain("refusedCheck scout-tools");
    expect(footButtons()).toEqual(["Set the secret again", "Recheck", "Finish later, open scout-tools"]);
    vi.mocked(recheckApp).mockResolvedValue(scout({ status: "degraded", detail: "connect failed: dial tcp 172.20.0.3:3001: connect: connection refused", tools: [] }));
    await click("button", "Recheck");
    await waitFor(() => expect(document.querySelector("[data-health]")?.getAttribute("data-health")).toBe("unreachable"));
    expect(document.querySelector("[data-health]")?.textContent).toContain("Unreachable. Straza could not reach " + URL_ + " (connection refused). Fix the address or start the server, then Recheck.");
    expect(footButtons()).toEqual(["Recheck", "Finish later, open scout-tools"]);
    expect(notify.warn).toHaveBeenCalledWith("scout-tools reads degraded after the probe. Straza could not reach " + URL_ + " (connection refused).");
    await click("button", "Finish later, open scout-tools");
    expect(navigate).toHaveBeenCalledWith("servers", ["app-9"]);
    expect(publishDraft).toHaveBeenCalledTimes(1);
  });

  // The whole install walk plus typed input takes about half of vitest's 5 s
  // default alone, so it carries its own budget for a loaded full suite.
  it("says the server exists on Cancel after the publish, and Set the secret again never publishes twice", async () => {
    vi.mocked(recheckApp).mockResolvedValue(scout({ status: "degraded", detail: "ping: upstream answered 401 Unauthorized", tools: [] }));
    await toCheck();
    await click("button", "Set the secret again");
    await heading("Credential");
    expect(screen.getByText("scout-tools is installed. Storing the secret again replaces the sealed value, then the check runs under it.")).toBeTruthy();
    expect(footButtons()).toEqual(["Store the secret and check"]);
    await click("button", "Cancel");
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByRole("heading").textContent).toBe("Leave the wizard?");
    expect(dialog.textContent).toContain("Leave the wizard. scout-tools stays installed and reads degraded on the MCP servers page. You can finish access later from its page.");
    expect(dialog.textContent).not.toContain("Nothing has been created yet");
    await click("button", "Keep going");
    await click("button", "Store the secret and check");
    expect(await screen.findByText("Type the secret the server expects. It is stored sealed after the install.")).toBeTruthy();
    vi.mocked(recheckApp).mockResolvedValue(scout());
    await userEvent.type(screen.getByLabelText("secret"), "correct-horse");
    await click("button", "Store the secret and check");
    await heading("Check");
    expect(publishDraft).toHaveBeenCalledTimes(1);
    expect(setSecret).toHaveBeenLastCalledWith("app-9", "correct-horse");
    expect(document.querySelector("[data-health]")?.textContent).toContain("Running. 3 tools found");
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
    expect(document.querySelector("[data-save-note]")?.textContent).toBe("The server did not respond. The change may have been saved. Reload to check before trying again.");
    expect(setSecret).not.toHaveBeenCalled();
  });

  it("saves the manifest to the working draft and keeps the secret out of it", async () => {
    vi.mocked(createDraft).mockResolvedValue({ draft: { ...DRAFT60, id: "39" }, verdict: CLEAN });
    await toReview();
    const sent = (document.querySelector("pre") as HTMLElement).textContent || "";
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
    expect((await screen.findByRole("alert")).textContent).toBe("Check refused. scout-tools is published, but the server list did not name it yet, so its secret and check did not run. Open MCP servers, then set the secret on its page.");
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

  it("converts a pasted server.json on the server and opens the card it read", async () => {
    render(<AddServer />);
    await userEvent.click(radio("runtime import"));
    await click("button", "Next");
    expect(await screen.findByText("Paste the registry's server.json and click Convert, or pick one of the other three cards.")).toBeTruthy();
    await userEvent.click(box("server.json"));
    await userEvent.paste("not json");
    await click("button", "Convert");
    expect(screen.getByText("This is not JSON. Paste the server.json record from the registry as it is.")).toBeTruthy();
    const record = { name: "io.github.example/weather", version: "1.0.2", remotes: [{ type: "streamable-http", url: "https://registry.example/other" }] };
    vi.mocked(importServerJSON).mockResolvedValue({ name: "weather", runtime: "remote", manifest: "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n    name: weather\nserver:\n    remotes:\n        - type: streamable-http\n          url: https://registry.example/other\nstraza:\n    runtime:\n        kind: remote\n        remote:\n            url: https://weather.example/mcp\n            auth: inject\n" });
    await userEvent.clear(box("server.json"));
    await userEvent.paste(JSON.stringify(record));
    await click("button", "Convert");
    expect(await screen.findByText("Converted: weather, remote runtime. The cards above show what it said; edit them if needed.")).toBeTruthy();
    expect(importServerJSON).toHaveBeenCalledWith(record);
    expect(radio("runtime remote").getAttribute("aria-checked")).toBe("true");
    expect((box("url") as HTMLInputElement).value).toBe("https://weather.example/mcp");
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
    expect(screen.getByText(/needs the role straza-global-mcp-admin\. You administer 2 servers and may change them/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Open MCP servers" })).toBeTruthy();
    expect(listApps).not.toHaveBeenCalled();
    sess.areas = null;
    sess.servers = 0;
  });
});
