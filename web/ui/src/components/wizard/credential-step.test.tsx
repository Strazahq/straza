import * as React from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CredentialStep } from "./credential-step";
import { useWizardForm } from "./use-wizard-form";
import type { ProviderRow } from "@/lib/api";
import { EMPTY_FORM, type Form, buildManifest, manifestYAML } from "@/lib/manifest";
import {
  AGENTS_SECRET_HINT, AGENT_ROWS, BASIC_NOTE, CALLER_NOT_HERE, CHANGE_ON_PAGE, CREDENTIAL_CHOICES, ENV_WARNING_SHORT, HOW_THIS_WORKS, NOT_AVAILABLE, NO_PROVIDER_YET,
  PROVIDERS_UNREAD, SECRET_HINT, STORE_AGAIN_LINE, credentialSay,
} from "@/lib/words";

const URL_ = "http://demo-tools:3001/mcp";
const KEYCLOAK: ProviderRow[] = [{ name: "keycloak", scopes: ["openid", "profile"], redirect_uri: "http://localhost:8420/v1/connect/callback" }];
const LABEL: Record<string, string> = Object.fromEntries(CREDENTIAL_CHOICES.map((c) => [c.kind, c.label]));

// Step draws the Credential step over the wizard's own form, started on
// the Server step's answers, with the manifest the fold shows.
function Step({ start, providers = KEYCLOAK, installed = "" }: { start: Partial<Form>; providers?: ProviderRow[] | null; installed?: string }) {
  const { form, f, field, set } = useWizardForm();
  React.useEffect(() => { form.reset({ ...EMPTY_FORM, name: "scout-tools", ...start }); }, [form, start]);
  const yaml = manifestYAML(buildManifest(f));
  return <CredentialStep f={f} errors={form.formState.errors} control={form.control} field={field} set={set} providers={providers} installed={installed} dry={{ state: "idle" }} yaml={yaml} />;
}

const REMOTE: Partial<Form> = { url: URL_ };
const COMMAND: Partial<Form> = { mode: "command", runtime: "command", exec: "/usr/local/bin/mcp-server" };
const OCI: Partial<Form> = { mode: "oci", runtime: "oci", image: "ghcr.io/org/server:1.2" };
const radio = (name: string) => screen.getByRole("radio", { name });
const box = (name: string) => screen.getByRole("textbox", { name });
const choose = async (combo: string, option: string) => {
  await userEvent.click(screen.getByRole("combobox", { name: combo }));
  await userEvent.click(await screen.findByRole("option", { name: option }));
};
const manifest = () => (document.querySelector("[data-manifest]") as HTMLElement).textContent || "";
const row = (kind: string) => document.querySelector('[data-row="' + kind + '"]') as HTMLElement;
// rowFields maps each row that has its fields open to their text.
const rowFields = () => Object.fromEntries([...document.querySelectorAll("[data-row-fields]")].map((b) => [(b.parentElement as HTMLElement).dataset.row, b.textContent]));
// labels lists the field labels inside el, in the order they are drawn.
const labels = (el: HTMLElement) => [...el.querySelectorAll("label, span.font-semibold")].filter((l) => !l.closest('[role="radio"]')).map((l) => l.textContent);
// described is the line a radio row is described by, or "" without one.
const described = (name: string) => {
  const id = radio(name).getAttribute("aria-describedby");
  return id ? (document.getElementById(id) as HTMLElement).textContent : "";
};
const sends = () => (document.querySelector("[data-sends]") as HTMLElement).textContent;

// head and tail are the parts of every expected document around the
// credential block, as buildManifest writes them for each runtime.
const head = (runtime: string) =>
  "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: scout-tools\nserver:\n  name: scout-tools\n  version: \"0\"\nstraza:\n  runtime:\n" + runtime;
const TAIL = "  exposure:\n    tools:\n      - \"*\"\n";
const ON_REMOTE = "    kind: remote\n    remote:\n      url: " + URL_ + "\n";
const ON_COMMAND = "    kind: command\n    command:\n      exec: /usr/local/bin/mcp-server\n";
const ON_OCI = "    kind: oci\n    oci:\n      image: ghcr.io/org/server:1.2\n";
const inject = (as: string, name: string, template: string) => "    inject:\n      as: " + as + "\n      name: " + name + "\n      template: \"" + template + "\"\n";

// Each case is the Server step's answers, what the person does on the
// Credential step, and the whole app.yaml the step sends for them. The
// documents are written out in full, so the same answers always send the
// same manifest.
const cases: [string, Partial<Form>, () => Promise<void>, string][] = [
  ["no credential", REMOTE, async () => {}, head(ON_REMOTE) + TAIL],
  ["one shared secret as a bearer", REMOTE, async () => {
    await userEvent.click(radio(LABEL.static));
    await userEvent.type(screen.getByLabelText("secret"), "hunter2");
  }, head(ON_REMOTE) + "  credential:\n    kind: static\n" + inject("header", "Authorization", "Bearer {{secret}}") + TAIL],
  ["one shared secret in a named header", REMOTE, async () => {
    await userEvent.click(radio(LABEL.static));
    await choose("sent as", "A named header");
    await userEvent.type(box("header name"), "X-Demo-Key");
  }, head(ON_REMOTE) + "  credential:\n    kind: static\n" + inject("header", "X-Demo-Key", "{{secret}}") + TAIL],
  ["one shared secret as basic auth", REMOTE, async () => {
    await userEvent.click(radio(LABEL.static));
    await choose("sent as", "Basic auth in Authorization");
  }, head(ON_REMOTE) + "  credential:\n    kind: static\n" + inject("header", "Authorization", "Basic {{secret}}") + TAIL],
  ["one shared secret a command reads", COMMAND, async () => {
    await userEvent.click(radio(LABEL.static));
    await userEvent.type(box("variable name"), "API_TOKEN");
  }, head(ON_COMMAND) + "  credential:\n    kind: static\n" + inject("env", "API_TOKEN", "{{secret}}") + TAIL],
  ["one shared secret a container reads", OCI, async () => {
    await userEvent.click(radio(LABEL.static));
    await userEvent.type(box("variable name"), "API_TOKEN");
  }, head(ON_OCI) + "  credential:\n    kind: static\n" + inject("env", "API_TOKEN", "{{secret}}") + TAIL],
  ["each person's own token", REMOTE, async () => {
    await userEvent.click(radio(LABEL.token));
  }, head(ON_REMOTE) + "  credential:\n    kind: token\n    agents: own\n" + inject("header", "Authorization", "Bearer {{secret}}") + TAIL],
  ["each person's own token with the agents' shared secret", REMOTE, async () => {
    await userEvent.click(radio(LABEL.token));
    await userEvent.click(radio("Use this server's shared secret"));
    await userEvent.type(screen.getByLabelText("secret"), "shared-1");
  }, head(ON_REMOTE) + "  credential:\n    kind: token\n    agents: shared\n" + inject("header", "Authorization", "Bearer {{secret}}") + TAIL],
  ["each person's own sign-in", REMOTE, async () => {
    await userEvent.click(radio(LABEL.oauth));
  }, head(ON_REMOTE) + "  credential:\n    kind: oauth\n    agents: sponsor\n    oauth:\n      provider: keycloak\n      scopes:\n        - openid\n        - profile\n" + inject("header", "Authorization", "Bearer {{secret}}") + TAIL],
];

describe("the Credential step of Add MCP server", () => {
  beforeEach(() => { document.body.innerHTML = ""; });

  it.each(cases)("sends the same manifest as before for %s", async (_case, start, act, want) => {
    render(<Step start={start} />);
    await act();
    expect(manifest()).toBe(want);
    expect(manifest()).not.toMatch(/hunter2|shared-1/);
  });

  it("draws one row per choice with its fact as a pill, and opens the fields of the picked row under it", async () => {
    render(<Step start={REMOTE} />);
    expect(screen.getByRole("radiogroup", { name: "credential" })).toBeTruthy();
    for (const c of CREDENTIAL_CHOICES) {
      expect(radio(c.label).textContent).toBe(c.label + c.fact);
      expect(radio(c.label).querySelector(".font-mono")).toBeNull();
      expect(radio(c.label).getAttribute("aria-disabled")).toBeNull();
    }
    // The picked None says one sentence and asks nothing.
    expect(rowFields()).toEqual({ none: credentialSay("none", "") });
    expect(screen.queryByText("Nothing to set for this choice.")).toBeNull();
    expect(screen.getByText(HOW_THIS_WORKS)).toBeTruthy();

    await userEvent.click(radio(LABEL.static));
    expect(Object.keys(rowFields())).toEqual(["static"]);
    expect(rowFields().static).toContain(credentialSay("static", ""));
    expect(within(row("static")).getByLabelText("secret").getAttribute("type")).toBe("password");
    expect(within(row("static")).getByText(SECRET_HINT)).toBeTruthy();
    expect(labels(row("static"))).toEqual(["Secret", "Sent as"]);
    expect(sends()).toBe("SendsAuthorization: Bearer <secret>");

    await userEvent.click(radio(LABEL.token));
    expect(Object.keys(rowFields())).toEqual(["token"]);
    expect(rowFields().token).toContain(credentialSay("token", ""));
    expect(labels(row("token"))).toEqual(["Sent as", "Stored now", "Agents with no token of their own"]);
    expect([...row("token").querySelectorAll("[data-row-fields] .rounded-pill")].map((p) => p.textContent)).toEqual(["nothing yet", "each person pastes their own"]);
    expect(sends()).toBe("SendsAuthorization: Bearer <the person's token>");
    // Only the picked agents row says its line.
    expect(described(LABEL.token)).toBe("");
    expect(described(AGENT_ROWS.token[0].name)).toBe(AGENT_ROWS.token[0].line);
    for (const r of AGENT_ROWS.token.slice(1)) expect(described(r.name)).toBe("");
    await userEvent.click(radio(AGENT_ROWS.token[2].name));
    expect(described(AGENT_ROWS.token[2].name)).toBe(AGENT_ROWS.token[2].line);
    expect(described(AGENT_ROWS.token[0].name)).toBe("");
    expect(labels(row("token"))).toEqual(["Sent as", "Stored now", "Agents with no token of their own", "Secret for agents"]);
    expect(within(row("token")).getByText(AGENTS_SECRET_HINT)).toBeTruthy();

    await userEvent.click(radio(LABEL.oauth));
    expect(Object.keys(rowFields())).toEqual(["oauth"]);
    expect(rowFields().oauth).toContain(credentialSay("oauth", "keycloak"));
    expect(rowFields().oauth).toContain("Register http://localhost:8420/v1/connect/callback at keycloak.");
    expect(labels(row("oauth"))).toEqual(["Provider", "Scopes", "Agents with no sign-in of their own"]);
    // The agents' shared choice carries over from the token row.
    expect(described(AGENT_ROWS.oauth[2].name)).toBe(AGENT_ROWS.oauth[2].line);
  });

  it("previews what a named header or basic auth sends as it is typed", async () => {
    render(<Step start={REMOTE} />);
    await userEvent.click(radio(LABEL.static));
    await choose("sent as", "A named header");
    expect(sends()).toBe("Sends<name>: <secret>");
    expect(labels(row("static"))).toEqual(["Secret", "Sent as", "Header name"]);
    await userEvent.type(box("header name"), "X-Demo-Key");
    expect(sends()).toBe("SendsX-Demo-Key: <secret>");
    await choose("sent as", "Basic auth in Authorization");
    expect(sends()).toBe("SendsAuthorization: Basic <secret>" + BASIC_NOTE);
  });

  it("shows the one way a container reads the secret as a fact, not a picker", async () => {
    render(<Step start={OCI} />);
    await userEvent.click(radio(LABEL.static));
    expect(screen.queryByRole("combobox", { name: "sent as" })).toBeNull();
    expect(within(row("static")).getByText("an environment variable").className).toContain("rounded-pill");
    expect(labels(row("static"))).toEqual(["Secret", "Sent as", "Variable name"]);
    expect(sends()).toBe("Sends<name>=<secret>");
    await userEvent.type(box("variable name"), "API_TOKEN");
    expect(sends()).toBe("SendsAPI_TOKEN=<secret>");
    expect(within(row("static")).getByText(ENV_WARNING_SHORT)).toBeTruthy();
  });

  // Each case is the Server step's answers, the identity providers as read,
  // a choice that is not available there, and the reason its row gives.
  const off: [string, Partial<Form>, ProviderRow[] | null, string, string][] = [
    ["a token on a command server", COMMAND, KEYCLOAK, "token", CALLER_NOT_HERE],
    ["a sign-in on a command server", COMMAND, KEYCLOAK, "oauth", CALLER_NOT_HERE],
    ["a token on a container server", OCI, KEYCLOAK, "token", CALLER_NOT_HERE],
    ["a sign-in on a container server", OCI, KEYCLOAK, "oauth", CALLER_NOT_HERE],
    ["a sign-in with no identity provider set up", REMOTE, [], "oauth", NO_PROVIDER_YET],
    ["a sign-in when the identity providers could not be read", REMOTE, null, "oauth", PROVIDERS_UNREAD],
  ];
  it.each(off)("keeps %s as a row that is not available and says why", async (_case, start, providers, kind, why) => {
    render(<Step start={start} providers={providers} />);
    const label = LABEL[kind];
    expect(radio(label).getAttribute("aria-disabled")).toBe("true");
    expect(radio(label).textContent).toBe(label + NOT_AVAILABLE);
    expect(described(label)).toBe(why);
    await userEvent.click(radio(label));
    expect(radio(LABEL.none).getAttribute("aria-checked")).toBe("true");
    expect(Object.keys(rowFields())).toEqual(["none"]);
    expect(manifest()).not.toContain("credential:");
  });

  it("keeps the sign-in open while the identity providers are still being read", () => {
    render(<Step start={REMOTE} providers={undefined} />);
    expect(radio(LABEL.oauth).getAttribute("aria-disabled")).toBeNull();
    expect(described(LABEL.oauth)).toBe("");
  });

  // Each case is the installed choice, the facts it stands as, and the
  // label and hint of the one field the step asks for again.
  const installed: [string, Partial<Form>, string, string, string][] = [
    ["one shared secret", { ...REMOTE, cred: "static", sentAs: "bearer" }, "One shared secretone identityheader Authorization", "Secret", SECRET_HINT],
    ["a token with the agents' shared secret", { ...REMOTE, cred: "token", agents: "shared", sentAs: "header", headerName: "X-Api-Key" }, "Each person's own tokeneach personheader X-Api-Key", "Secret for agents", AGENTS_SECRET_HINT],
    ["one shared secret a command reads", { ...COMMAND, cred: "static", envName: "API_TOKEN" }, "One shared secretone identityvariable API_TOKEN", "Secret", SECRET_HINT],
  ];
  it.each(installed)("asks only for the secret when it comes back from Check on %s", async (_case, start, facts, label, hint) => {
    render(<Step start={start} installed="scout-tools" />);
    expect(await screen.findByText(STORE_AGAIN_LINE)).toBeTruthy();
    expect(screen.getByText(STORE_AGAIN_LINE).textContent).toBe("Storing replaces the stored secret, then runs the check.");
    expect((document.querySelector("[data-installed-choice]") as HTMLElement).textContent).toBe(facts);
    expect(screen.getByText(CHANGE_ON_PAGE)).toBeTruthy();
    expect(screen.queryAllByRole("radio")).toEqual([]);
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(labels(document.querySelector("[data-credential-box]") as HTMLElement)).toEqual([label]);
    expect(screen.getByText(hint)).toBeTruthy();
    expect(screen.queryByText(HOW_THIS_WORKS)).toBeNull();
  });
});
