import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChangeSheet, type ChangeWhich } from "./server-change-sheet";
import { MISSING } from "@/components/wizard/form-schema";
import type { DraftSaveOptions, SaveNote } from "@/components/use-draft-save";
import { type AppRow, type DraftPublished, type ManifestDoc, dryRunApp, listApps, listProviders } from "@/lib/api";
import { NAME_LOCKED, NOTHING_TO_SAVE, NOTHING_YET, OFFERED_SHORT, READ_UNREACHABLE, RPS_MISS, coveredWords, gone, keptWords, readRefused, reading, remoteRpsMiss } from "@/lib/change-words";
import type { SaveItem } from "@/lib/draft-save";
import { version } from "@/lib/public";
import { workingHolds } from "@/lib/save-words";
import {
  AGENT_ROWS, CALLER_NOT_HERE, CALLER_OFF_SHORT, COMMAND_REFUSED, CREDENTIAL_CHOICES, HOW_THIS_WORKS, NOT_AVAILABLE, NO_PROVIDER_YET, OCI_MISSING, OCI_MISSING_REMOTE, PROVIDERS_UNREAD, RUNTIMES,
  credentialSay,
} from "@/lib/words";

// The cards are found by their own names, the names a person reads, never
// by the kind the manifest writes.
const runtimeCard = (kind: string) => screen.getByRole("radio", { name: (RUNTIMES.find((r) => r.key === kind) as { name: string }).name });
const credentialCard = (kind: string) => screen.getByRole("radio", { name: (CREDENTIAL_CHOICES.find((c) => c.kind === kind) as { label: string }).label });
const agentsRow = (value: string) => screen.getByRole("radio", { name: (AGENT_ROWS.token.find((r) => r.value === value) as { name: string }).name });

vi.mock("@/lib/api", () => ({ dryRunApp: vi.fn(), listApps: vi.fn(), listProviders: vi.fn() }));
// The two saves are the shared hook's, with its own suite; here it stands
// in so this suite pins what the sheet hands it: one App put built from the
// live manifest, the button that sends it, and what a publish does next.
const save = vi.hoisted(() => ({
  opts: null as DraftSaveOptions | null, note: null as SaveNote | null, held: null as string | null, asked: "",
  draft: vi.fn<(items: SaveItem[]) => Promise<void>>(), publish: vi.fn<(items: SaveItem[]) => Promise<void>>(),
}));
vi.mock("@/components/use-draft-save", async (orig) => ({
  ...(await orig<typeof import("@/components/use-draft-save")>()),
  useDraftSave: (o: DraftSaveOptions) => { save.opts = o; return { busy: null, note: save.note, saveDraft: save.draft, saveAndPublish: save.publish, dialog: null }; },
  useWorkingDraft: (object: string) => { save.asked = object; return save.held; },
}));
vi.mock("@/lib/public", () => ({ version: vi.fn() }));
const sess = vi.hoisted(() => ({ areas: null as Record<string, true> | null }));
vi.mock("@/lib/session", () => ({ adminAreas: () => sess.areas, adminServers: () => 0 }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));

const SCOUT: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "scout-tools", description: "Search and read the team's scouting notes." },
  server: { name: "scout-tools", version: "0" },
  straza: {
    runtime: { kind: "remote", remote: { url: "http://scout-tools.internal:8080/mcp", auth: "inject" } },
    credential: { kind: "static", inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } },
    exposure: { tools: ["*"] },
  },
};
const ECHO: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "u3-echo" },
  server: { name: "u3-echo", version: "0.1.0" },
  straza: {
    runtime: { kind: "command", command: { exec: "/var/lib/straza/u3-echo", args: ["--stdio"], env: [{ name: "LOG_LEVEL", value: "info" }] } },
    credential: { kind: "static", inject: { as: "env", name: "ECHO_TOKEN", template: "{{secret}}" } },
    exposure: { tools: ["*"] },
  },
};
const MIDPOINT: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "midpoint" },
  server: { name: "midpoint", version: "0.3.1" },
  straza: {
    runtime: { kind: "remote", remote: { url: "http://mcp-midpoint-http:3001/mcp", auth: "inject" } },
    credential: { kind: "oauth", agents: "own", oauth: { provider: "keycloak", scopes: ["openid"] }, inject: { as: "header", name: "Authorization", template: "Bearer {{secret}}" } },
  },
};
const LEGACY: ManifestDoc = {
  apiVersion: "straza.dev/v1beta1", kind: "App",
  metadata: { name: "legacy-api" },
  server: { name: "legacy-api", version: "2" },
  straza: {
    runtime: { kind: "remote", remote: { url: "https://legacy.internal/mcp" } },
    credential: { kind: "static", inject: { as: "header", name: "Authorization", template: "Token {{secret}}" } },
  },
};

// PATTERNS exposes a glob and a name the server no longer offers, the two
// shapes the checkboxes cannot write. ONLY_ONE exposes a single tool, so a
// tool the server offers is one the manifest leaves out today.
const PATTERNS: ManifestDoc = { ...SCOUT, straza: { ...SCOUT.straza, exposure: { tools: ["read_*", "gone_tool"] } } };
const ONLY_ONE: ManifestDoc = { ...SCOUT, straza: { ...SCOUT.straza, exposure: { tools: ["search_notes"] } } };

const TOOLS = ["search_notes", "read_note", "list_tags", "create_note", "delete_note"];
const NEW_URL = "https://scout-tools.internal/mcp";
const row = (name: string, manifest: ManifestDoc, extra: Partial<AppRow> = {}): AppRow =>
  ({ id: "a-" + name, name, runtime: manifest.straza?.runtime?.kind || "remote", status: "running", reached_by: ["dev-tools"], manifest, ...extra });
const failure = (message: string, status: number, unreachable = false) => Object.assign(new Error(message), { status, unreachable });

// open renders the sheet the way the page does and waits for the read of
// the server's own row, which is where the fields come from. fresh is the
// row the server answers with when it differs from the one the page holds.
async function open(app: AppRow, which: ChangeWhich, fresh?: AppRow) {
  vi.mocked(listApps).mockResolvedValue([fresh || app]);
  const onClose = vi.fn();
  const onSaved = vi.fn();
  render(<ChangeSheet app={app} which={which} tools={TOOLS} upstreamTimeout={30} remoteRps={20} onClose={onClose} onSaved={onSaved} />);
  await waitFor(() => expect(document.querySelector("[data-change-say]")).toBeNull());
  return { onClose, onSaved };
}
const text = (sel: string) => (document.querySelector(sel) as HTMLElement | null)?.textContent || "";
// described is the line a radio row is described by, or "" without one.
const described = (el: HTMLElement) => {
  const id = el.getAttribute("aria-describedby");
  return id ? (document.getElementById(id) as HTMLElement).textContent : "";
};
const paragraphs = () => [...document.querySelectorAll("[data-when-save] p")].map((p) => p.textContent);
const address = () => screen.getByRole("textbox", { name: "Address" });
const primary = (name = "Save and publish") => screen.getByRole("button", { name });
const PUBLISHED = { draft: { id: "50" }, snapshot: "3be0a1", servers: [], next: [] } as unknown as DraftPublished;
const sent = (fn: typeof save.publish) => fn.mock.calls[0][0][0];
async function retype(box: HTMLElement, value: string) {
  await userEvent.clear(box);
  await userEvent.type(box, value);
}

describe("the Change sheets of a server's Overview", () => {
  beforeEach(() => {
    save.draft.mockReset().mockResolvedValue(undefined);
    save.publish.mockReset().mockResolvedValue(undefined);
    save.note = null;
    save.held = null;
    save.opts = null;
    vi.mocked(dryRunApp).mockReset().mockImplementation((yaml: string) => Promise.resolve({
      name: "x", tools: ["*"],
      runtime: (/runtime:\n\s+kind: (\w+)/.exec(yaml) || ["", "remote"])[1],
      credential: (/credential:\n\s+kind: (\w+)/.exec(yaml) || ["", "none"])[1],
    }));
    vi.mocked(listProviders).mockReset().mockResolvedValue([{ name: "keycloak", scopes: ["openid", "profile"], redirect_uri: "http://localhost:8420/v1/connect/callback" }]);
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "standalone", runtimes: ["remote", "command"] });
    vi.mocked(listApps).mockReset();
  });

  it("opens on the address with the name locked, and follows typing with the rows, the server's check and what publishing does", async () => {
    await open(row("scout-tools", SCOUT), "connection");
    expect(screen.getByRole("dialog", { name: "Change how Straza reaches scout-tools" })).toBeTruthy();
    expect(screen.getByText(NAME_LOCKED)).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(address()));
    expect(text("[data-change-rows]")).toBe(NOTHING_YET);
    // When you publish waits for the first edit, so the empty state is said once.
    expect(screen.queryByText("When you publish")).toBeNull();
    await retype(address(), NEW_URL);
    const rows = within(document.querySelector("[data-change-rows]") as HTMLElement).getAllByRole("row").map((r) => r.textContent);
    expect(rows).toEqual(["FieldNowAfter publishing", "Addresshttp://scout-tools.internal:8080/mcp" + NEW_URL]);
    expect((document.querySelector("[data-change-rows] table") as HTMLElement).className).toContain("min-w-[480px]");
    expect(screen.getByText("When you publish")).toBeTruthy();
    expect((await screen.findByText("Straza accepts this change.")).className).toContain("text-ok");
    expect(text("[data-dry-run]")).toBe("Straza accepts this change.");
    expect(vi.mocked(dryRunApp).mock.calls.at(-1)?.[0]).toContain("url: " + NEW_URL + "\n      auth: inject");
    expect(paragraphs()).toEqual([
      "Straza closes its connections to the old address, connects to the new one and reads the tools again.",
      "A call in flight gets an error and can be retried.",
      "Access rows and stored secrets stay as they are.",
    ]);
  });

  it("says there is nothing to save when no field changed", async () => {
    await open(row("scout-tools", SCOUT), "connection");
    await userEvent.click(primary());
    expect(text("[data-nothing-to-save]")).toBe(NOTHING_TO_SAVE);
    await userEvent.click(primary("Save draft"));
    expect(save.publish).not.toHaveBeenCalled();
    expect(save.draft).not.toHaveBeenCalled();
  });

  it("says what is missing under the field, holds the server's check, and focuses the field on save", async () => {
    await open(row("scout-tools", SCOUT), "connection");
    await userEvent.clear(address());
    expect(screen.getByText(MISSING.url)).toBeTruthy();
    await userEvent.type(address(), "ftp://scout");
    expect(screen.getByText(MISSING.scheme)).toBeTruthy();
    expect(address().getAttribute("aria-invalid")).toBe("true");
    await userEvent.click(primary());
    expect(document.activeElement).toBe(address());
    expect(save.publish).not.toHaveBeenCalled();
    expect(dryRunApp).not.toHaveBeenCalled();
  });

  it("publishes the edited manifest as one App put, then hands back and closes", async () => {
    const { onClose, onSaved } = await open(row("scout-tools", SCOUT), "connection");
    expect(save.opts?.name).toBe("scout-tools");
    await retype(address(), NEW_URL);
    await screen.findByText(/Straza accepts this change/);
    await userEvent.click(primary());
    const item = sent(save.publish);
    expect([item.kind, item.name, item.op]).toEqual(["App", "scout-tools", "put"]);
    expect(item.doc).toContain("  name: scout-tools\n  description: Search and read the team's scouting notes.\n");
    expect(item.doc).toContain("url: " + NEW_URL);
    expect(item.doc).toContain('template: "Bearer {{secret}}"');
    const fold = document.querySelector("[data-yaml-fold]") as HTMLDetailsElement;
    expect(fold.open).toBe(false);
    expect(fold.querySelector("summary")?.textContent).toBe("View as YAMLapp.yaml, as it will be sent");
    expect(document.querySelector("[data-manifest]")?.textContent).toBe(item.doc);
    expect(save.draft).not.toHaveBeenCalled();
    expect(onSaved).not.toHaveBeenCalled();
    expect(save.opts?.toast).toBeUndefined();
    save.opts?.onPublished(PUBLISHED);
    expect(onSaved).toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it("saves the same App put to the working draft with Save draft, and closes nothing itself", async () => {
    const { onClose, onSaved } = await open(row("scout-tools", SCOUT), "connection");
    await retype(address(), NEW_URL);
    await screen.findByText(/Straza accepts this change/);
    await userEvent.click(primary("Save draft"));
    expect(sent(save.draft).doc).toContain("url: " + NEW_URL);
    expect(save.publish).not.toHaveBeenCalled();
    expect(onSaved).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it("shows the save's note beside the buttons and keeps the sheet open", async () => {
    save.note = { tone: "refused", lines: ["Nothing was saved.", "In the server scout-tools, metadata.description holds what looks like a GitHub token."] };
    const { onClose } = await open(row("scout-tools", SCOUT), "connection");
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toBe("Nothing was saved.In the server scout-tools, metadata.description holds what looks like a GitHub token.");
    expect(alert.getAttribute("data-save-note")).toBe("refused");
    expect(alert.className).toContain("border-danger");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("opens on live and says so when the working draft already changes the server", async () => {
    save.held = "39";
    await open(row("scout-tools", SCOUT), "connection");
    expect(save.asked).toBe("App/scout-tools");
    expect(text("[data-working-holds]")).toBe(workingHolds("39", "scout-tools"));
    expect((address() as HTMLInputElement).value).toBe("http://scout-tools.internal:8080/mcp");
  });

  it("scrolls to the server's refusal instead of saving a manifest it refused", async () => {
    vi.mocked(dryRunApp).mockRejectedValue(failure("manifest: invalid App: runtime.remote.url must be http(s)", 400));
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    await open(row("scout-tools", SCOUT), "connection");
    await retype(address(), NEW_URL);
    expect((await screen.findByText("manifest: invalid App: runtime.remote.url must be http(s)")).getAttribute("data-dry-run")).toBe("refused");
    await userEvent.click(primary());
    expect(scroll).toHaveBeenCalled();
    expect(save.publish).not.toHaveBeenCalled();
    scroll.mockRestore();
  });

  it("changes a paused server and says it stays paused, in the sheet and in the toast", async () => {
    await open(row("u3-echo", ECHO, { status: "stopped", paused: true }), "connection");
    expect(screen.getByText("ECHO_TOKEN").parentElement?.textContent).toBe("ECHO_TOKEN comes from the stored secret, encrypted. Change it on the Server authentication card.");
    await retype(screen.getByRole("textbox", { name: "Arguments" }), "--stdio --verbose");
    expect(paragraphs()).toEqual([
      "u3-echo stays paused. Enable starts it with this change.",
      "Access rows and stored secrets stay as they are.",
    ]);
    await screen.findByText("Straza accepts this change.");
    await userEvent.click(primary());
    expect(save.publish).toHaveBeenCalled();
    expect(save.opts?.toast).toBe("u3-echo keeps the new settings and stays paused. Enable starts it with them.");
  });

  it("keeps the command and container cards off for a server that takes each caller's own credential", async () => {
    await open(row("midpoint", MIDPOINT), "connection");
    for (const kind of ["command", "oci"]) {
      const card = runtimeCard(kind);
      expect(card.getAttribute("aria-disabled")).toBe("true");
      expect(card.textContent).toContain(CALLER_OFF_SHORT);
    }
    // A card names its choice in words and draws no raw kind beside it.
    for (const kind of ["remote", "command", "oci"]) expect(runtimeCard(kind).querySelector(".font-mono")).toBeNull();
    await userEvent.click(runtimeCard("command"));
    expect(screen.queryByRole("textbox", { name: "Executable" })).toBeNull();
    expect(text("[data-change-rows]")).toBe(NOTHING_YET);
  });

  it("keeps the command card off when strazad refuses command servers, and the container card names HTTP only", async () => {
    vi.mocked(version).mockReset().mockResolvedValue({ version: "t", commit: "t", go: "g", profile: "enterprise", runtimes: ["remote"] });
    await open(row("scout-tools", SCOUT), "connection");
    const card = () => runtimeCard("command");
    await waitFor(() => expect(card().textContent).toContain(COMMAND_REFUSED));
    expect(card().getAttribute("aria-disabled")).toBe("true");
    expect(runtimeCard("oci").textContent).toContain(OCI_MISSING_REMOTE);
    await userEvent.click(card());
    expect(screen.queryByRole("textbox", { name: "Executable" })).toBeNull();
    expect(text("[data-change-rows]")).toBe(NOTHING_YET);
  });

  it("keeps the container card off when strazad has no docker, and asks how the secret travels once the transport moves", async () => {
    await open(row("scout-tools", SCOUT), "connection");
    await waitFor(() => expect(runtimeCard("oci").textContent).toContain(OCI_MISSING));
    await userEvent.click(runtimeCard("command"));
    await userEvent.type(screen.getByRole("textbox", { name: "Executable" }), "/usr/local/bin/scout");
    expect(screen.getByText(MISSING.envName)).toBeTruthy();
    await userEvent.type(screen.getByRole("textbox", { name: "Shared secret's variable" }), "SCOUT_TOKEN");
    const labels = [...document.querySelectorAll("[data-change-rows] tbody tr td:first-child")].map((c) => c.textContent);
    expect(labels).toEqual(["Transport", "Executable", "Sent as", "Rate limit"]);
    expect(text("[data-change-rows]")).toContain("Variable SCOUT_TOKEN");
    expect(text("[data-change-rows]")).toContain("Rate limit20 calls per second, per session, the default.Not limited.");
    expect(paragraphs()[0]).toBe("Straza stops the old runtime, starts the new one and reads the tools again.");
  });

  it("gives each person their own token and says what happens to the shared secret", async () => {
    await open(row("scout-tools", SCOUT), "credential");
    expect(screen.getByRole("dialog", { name: "Change server authentication of scout-tools" })).toBeTruthy();
    await userEvent.click(credentialCard("token"));
    expect(described(agentsRow("own"))).toBe(AGENT_ROWS.token[0].line);
    for (const kind of ["none", "static", "token", "oauth"]) expect(credentialCard(kind).querySelector(".font-mono")).toBeNull();
    for (const value of ["own", "sponsor", "shared"]) expect(agentsRow(value).querySelector(".font-mono")).toBeNull();
    expect(paragraphs()).toEqual([
      "Each person's calls carry their own token from now on, and a person without one is refused until they paste it on the Credentials tab of their self-service page.",
      "The stored shared secret stays, unused. Remove it on the Server authentication card.",
    ]);
    await userEvent.click(agentsRow("shared"));
    expect(described(agentsRow("shared"))).toBe("Only agents fall back to it.");
    expect(described(agentsRow("own"))).toBe("");
    expect(paragraphs()[1]).toBe("Agents with no token of their own run on this server's shared secret. People never fall back to it.");
    expect(text("[data-change-rows]")).toContain("Use this server's shared secret.");
  });

  it("offers a template the cards cannot write as it stands, first and chosen", async () => {
    await open(row("legacy-api", LEGACY), "credential");
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("Keep as written: Authorization with Token {{secret}}");
    expect(screen.getByText("The header Authorization, written as Token <the secret>.")).toBeTruthy();
    expect(text("[data-change-rows]")).toBe(NOTHING_YET);
  });

  it("keeps the caller cards off for a command server", async () => {
    await open(row("u3-echo", ECHO), "credential");
    for (const kind of ["token", "oauth"]) expect(credentialCard(kind).getAttribute("aria-disabled")).toBe("true");
  });

  it("draws the credential as the Add wizard's rows, with the fields of the picked row under it", async () => {
    await open(row("scout-tools", SCOUT), "credential");
    for (const c of CREDENTIAL_CHOICES) expect(credentialCard(c.kind).textContent).toBe(c.label + c.fact);
    const fields = () => [...document.querySelectorAll("[data-row-fields]")].map((b) => [(b.parentElement as HTMLElement).dataset.row, b.textContent]);
    expect(fields().map(([kind]) => kind)).toEqual(["static"]);
    expect(fields()[0][1]).toContain(credentialSay("static", ""));
    expect(screen.getByRole("combobox", { name: "sent as" }).textContent).toBe("A bearer token in Authorization");
    expect(text("[data-sends]")).toBe("SendsAuthorization: Bearer <secret>");
    expect(screen.getByText(HOW_THIS_WORKS)).toBeTruthy();
    await userEvent.click(credentialCard("none"));
    expect(fields()).toEqual([["none", credentialSay("none", "")]]);
    expect(screen.queryByText("Nothing to set for this choice.")).toBeNull();
    await userEvent.click(credentialCard("oauth"));
    expect(fields().map(([kind]) => kind)).toEqual(["oauth"]);
    expect(fields()[0][1]).toContain(credentialSay("oauth", "keycloak"));
    expect(screen.getByRole("combobox", { name: "identity provider" }).textContent).toBe("keycloak");
  });

  // Each case is a server, the identity providers as read, a choice that is
  // not available on it, and the reason its row gives.
  const offRows: [string, ManifestDoc, () => void, string, string][] = [
    ["a token on a command server", ECHO, () => {}, "token", CALLER_NOT_HERE],
    ["a sign-in on a command server", ECHO, () => {}, "oauth", CALLER_NOT_HERE],
    ["a sign-in with no identity provider set up", SCOUT, () => vi.mocked(listProviders).mockResolvedValue([]), "oauth", NO_PROVIDER_YET],
    ["a sign-in when the identity providers could not be read", SCOUT, () => vi.mocked(listProviders).mockRejectedValue(failure("unreachable", 0, true)), "oauth", PROVIDERS_UNREAD],
  ];
  it.each(offRows)("keeps %s off with the wizard's reason", async (_case, doc, providers, kind, why) => {
    providers();
    await open(row(doc.metadata?.name || "", doc), "credential");
    await waitFor(() => expect(described(credentialCard(kind))).toBe(why));
    expect(credentialCard(kind).getAttribute("aria-disabled")).toBe("true");
    expect(credentialCard(kind).textContent).toContain(NOT_AVAILABLE);
    await userEvent.click(credentialCard(kind));
    expect(text("[data-change-rows]")).toBe(NOTHING_YET);
  });

  it("keeps the installed sign-in open when no identity provider is set up", async () => {
    vi.mocked(listProviders).mockResolvedValue([]);
    await open(row("midpoint", MIDPOINT), "credential");
    await waitFor(() => expect(listProviders).toHaveBeenCalled());
    expect(credentialCard("oauth").getAttribute("aria-checked")).toBe("true");
    expect(credentialCard("oauth").getAttribute("aria-disabled")).toBeNull();
    expect(described(credentialCard("oauth"))).toBe("");
  });

  // SCOUT_HEAD and SCOUT_TAIL are scout-tools' manifest around its
  // credential block, as editCredential leaves them.
  const SCOUT_HEAD = "apiVersion: straza.dev/v1beta1\nkind: App\nmetadata:\n  name: scout-tools\n  description: Search and read the team's scouting notes.\nserver:\n  name: scout-tools\n  version: \"0\"\nstraza:\n  runtime:\n    kind: remote\n    remote:\n      url: http://scout-tools.internal:8080/mcp\n      auth: inject\n";
  const SCOUT_TAIL = "  exposure:\n    tools:\n      - \"*\"\n";
  const bearer = "    inject:\n      as: header\n      name: Authorization\n      template: \"Bearer {{secret}}\"\n";
  // Each case is what the person does on scout-tools' Credential sheet and
  // the whole document Save and publish sends, written out in full, so the
  // same answers always send the same document.
  const sends: [string, () => Promise<void>, string][] = [
    ["each person's own token", async () => { await userEvent.click(credentialCard("token")); },
      SCOUT_HEAD + "  credential:\n    kind: token\n    agents: own\n" + bearer + SCOUT_TAIL],
    ["each person's own token with the agents' shared secret", async () => { await userEvent.click(credentialCard("token")); await userEvent.click(agentsRow("shared")); },
      SCOUT_HEAD + "  credential:\n    kind: token\n    agents: shared\n" + bearer + SCOUT_TAIL],
    ["one shared secret in a named header", async () => {
      await userEvent.click(screen.getByRole("combobox", { name: "sent as" }));
      await userEvent.click(await screen.findByRole("option", { name: "A named header" }));
      // The installed bearer's header name stays in the box.
      expect((screen.getByRole("textbox", { name: "header name" }) as HTMLInputElement).value).toBe("Authorization");
      await retype(screen.getByRole("textbox", { name: "header name" }), "X-Scout");
    }, SCOUT_HEAD + "  credential:\n    kind: static\n    inject:\n      as: header\n      name: X-Scout\n      template: \"{{secret}}\"\n" + SCOUT_TAIL],
    ["each person's own sign-in", async () => { await userEvent.click(credentialCard("oauth")); },
      SCOUT_HEAD + "  credential:\n    kind: oauth\n    agents: own\n    oauth:\n      provider: keycloak\n      scopes:\n        - openid\n        - profile\n" + bearer + SCOUT_TAIL],
    ["no credential", async () => { await userEvent.click(credentialCard("none")); }, SCOUT_HEAD + SCOUT_TAIL],
  ];
  it.each(sends)("sends the whole document for %s", async (_case, act, want) => {
    await open(row("scout-tools", SCOUT), "credential");
    await waitFor(() => expect(listProviders).toHaveBeenCalled());
    await act();
    await userEvent.click(primary());
    expect(sent(save.publish).doc).toBe(want);
  });

  it("shows a command server's one way as a fact and previews the variable it sends", async () => {
    await open(row("u3-echo", ECHO), "credential");
    expect(screen.queryByRole("combobox", { name: "sent as" })).toBeNull();
    expect(screen.getByText("an environment variable").className).toContain("rounded-pill");
    expect(text("[data-sends]")).toBe("SendsECHO_TOKEN=<secret>");
    await retype(screen.getByRole("textbox", { name: "variable name" }), "SCOUT_TOKEN");
    expect(text("[data-sends]")).toBe("SendsSCOUT_TOKEN=<secret>");
    await userEvent.click(primary());
    expect(sent(save.publish).doc).toContain("  credential:\n    kind: static\n    inject:\n      as: env\n      name: SCOUT_TOKEN\n      template: \"{{secret}}\"\n");
  });

  it("picks the tools exposed and the limits, and says what publishing does", async () => {
    await open(row("scout-tools", SCOUT), "settings");
    expect(screen.getByText("seconds. Empty means the server-wide 30 s.")).toBeTruthy();
    expect(screen.getByText("calls per second, per session. Empty means the default of 20.")).toBeTruthy();
    expect(screen.getByRole("textbox", { name: "Rate limit" }).getAttribute("placeholder")).toBe("20");
    await userEvent.click(screen.getByRole("radio", { name: "expose only picked tools" }));
    const boxes = screen.getAllByRole("checkbox");
    expect(boxes.map((b) => (b as HTMLInputElement).checked)).toEqual([true, true, true, true, true]);
    await userEvent.click(screen.getByRole("checkbox", { name: "delete_note" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Rate limit" }), "fast");
    expect(screen.getByText(remoteRpsMiss(20))).toBeTruthy();
    await retype(screen.getByRole("textbox", { name: "Rate limit" }), "10");
    const rows = [...document.querySelectorAll("[data-change-rows] tbody tr")].map((r) => [...r.children].map((c) => c.textContent));
    expect(rows).toEqual([
      ["Tools exposed", "All tools, including ones it adds later.", "Only search_notes, read_note, list_tags, create_note."],
      ["Rate limit", "20 calls per second, per session, the default.", "10 calls per second, per session."],
    ]);
    expect(paragraphs()).toEqual([
      "Straza reconnects to the server.",
      "Tools you leave out leave every session's list at once. Access rows that name them stay and reach nothing until you expose them again.",
    ]);
    await screen.findByText(/Straza accepts this change/);
    await userEvent.click(primary());
    expect(sent(save.publish).doc).toContain("  exposure:\n    tools:\n      - search_notes\n      - read_note\n      - list_tags\n      - create_note\n  limits:\n    rps: 10\n");
  });

  it("says no limit for an empty rate on a command server", async () => {
    await open(row("u3-echo", ECHO), "settings");
    const rps = screen.getByRole("textbox", { name: "Rate limit" });
    expect(rps.getAttribute("placeholder")).toBe("no limit");
    expect(screen.getByText("calls per second, per session")).toBeTruthy();
    await userEvent.type(rps, "fast");
    expect(screen.getByText(RPS_MISS)).toBeTruthy();
  });

  it("says the remote default unnamed when the config did not answer", async () => {
    vi.mocked(listApps).mockResolvedValue([row("scout-tools", SCOUT)]);
    render(<ChangeSheet app={row("scout-tools", SCOUT)} which="settings" tools={TOOLS} upstreamTimeout={null} remoteRps={null} onClose={vi.fn()} onSaved={vi.fn()} />);
    await waitFor(() => expect(document.querySelector("[data-change-say]")).toBeNull());
    const rps = screen.getByRole("textbox", { name: "Rate limit" });
    expect(rps.getAttribute("placeholder")).toBe("");
    expect(screen.getByText("calls per second, per session. Empty means the default.")).toBeTruthy();
    await userEvent.type(rps, "fast");
    expect(screen.getByText(remoteRpsMiss(null))).toBeTruthy();
  });

  it("saves the manifest the server has now, not the one the page still holds", async () => {
    const fresh: ManifestDoc = JSON.parse(JSON.stringify(SCOUT));
    fresh.metadata = { ...fresh.metadata, description: "Notes, read only." };
    fresh.straza = { ...fresh.straza, runtime: { kind: "remote", remote: { url: "http://scout-tools.internal:9090/mcp", auth: "inject" } } };
    await open(row("scout-tools", SCOUT), "connection", row("scout-tools", fresh));
    expect((address() as HTMLInputElement).value).toBe("http://scout-tools.internal:9090/mcp");
    await retype(address(), NEW_URL);
    expect(text("[data-change-rows]")).toContain("http://scout-tools.internal:9090/mcp");
    expect(text("[data-change-rows]")).not.toContain(":8080");
    await screen.findByText(/Straza accepts this change/);
    await userEvent.click(primary());
    const yaml = sent(save.publish).doc || "";
    expect(yaml).toContain("description: Notes, read only.");
    expect(yaml).not.toContain("Search and read");
  });

  it("says the manifest is still being read, and saves nothing until it lands", async () => {
    let land: (rows: AppRow[]) => void = () => undefined;
    vi.mocked(listApps).mockReturnValue(new Promise<AppRow[]>((r) => { land = r; }));
    render(<ChangeSheet app={row("scout-tools", SCOUT)} which="connection" tools={TOOLS} upstreamTimeout={30} remoteRps={20} onClose={vi.fn()} onSaved={vi.fn()} />);
    expect(screen.getByText(reading("scout-tools"))).toBeTruthy();
    const button = primary();
    expect(button.getAttribute("aria-disabled")).toBe("true");
    expect(button.getAttribute("title")).toBe(reading("scout-tools"));
    await userEvent.click(button);
    expect(save.publish).not.toHaveBeenCalled();
    land([row("scout-tools", SCOUT)]);
    expect(await screen.findByRole("textbox", { name: "Address" })).toBeTruthy();
  });

  const READS: [string, () => void, string][] = [
    ["the server refused the read", () => { vi.mocked(listApps).mockRejectedValue(failure("list apps failed", 500)); }, readRefused("list apps failed")],
    ["strazad did not answer", () => { vi.mocked(listApps).mockRejectedValue(failure("unreachable", 0, true)); }, READ_UNREACHABLE],
    ["the server is not in the list any more", () => { vi.mocked(listApps).mockResolvedValue([]); }, gone("scout-tools")],
  ];
  it.each(READS)("offers no fields when the installed manifest does not arrive: %s", async (_, set, words) => {
    set();
    const onClose = vi.fn();
    render(<ChangeSheet app={row("scout-tools", SCOUT)} which="connection" tools={TOOLS} upstreamTimeout={30} remoteRps={20} onClose={onClose} onSaved={vi.fn()} />);
    expect((await screen.findByText(words)).textContent).toBe(words);
    expect(screen.queryByRole("textbox", { name: "Address" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Save/ })).toBeNull();
    await userEvent.click(within(document.querySelector("[data-slot=sheet-footer]") as HTMLElement).getByRole("button", { name: "Close" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("ticks a tool a kept pattern already admits, and leaves it alone", async () => {
    await open(row("scout-tools", PATTERNS), "settings");
    const covered = screen.getByRole("checkbox", { name: "read_note" }) as HTMLInputElement;
    expect([covered.checked, covered.disabled]).toEqual([true, true]);
    expect(covered.closest("label")?.getAttribute("title")).toBe(coveredWords("read_note", "read_*"));
    expect(covered.closest("label")?.textContent).toContain("read_*");
    expect(screen.getByText(keptWords(["read_*", "gone_tool"]))).toBeTruthy();
    const free = screen.getByRole("checkbox", { name: "search_notes" }) as HTMLInputElement;
    expect(free.checked).toBe(false);
    await userEvent.click(free);
    expect(text("[data-change-rows]")).toContain("Only read_*, gone_tool, search_notes.");
  });

  it("builds the list from what the server offers, so a tool left out today can be ticked back", async () => {
    await open(row("scout-tools", ONLY_ONE), "settings", row("scout-tools", ONLY_ONE, { offered: ["search_notes", "read_note", "archive_note"] }));
    expect(screen.queryByText(OFFERED_SHORT)).toBeNull();
    expect(screen.getAllByRole("checkbox").map((b) => [b.getAttribute("aria-label"), (b as HTMLInputElement).checked]))
      .toEqual([["search_notes", true], ["read_note", false], ["archive_note", false]]);
    await userEvent.click(screen.getByRole("checkbox", { name: "archive_note" }));
    expect(text("[data-change-rows]")).toContain("Only search_notes, archive_note.");
  });

  it("falls back to the tools Straza knows when the server offers no list, and says why", async () => {
    await open(row("scout-tools", ONLY_ONE), "settings");
    expect(screen.getByText(OFFERED_SHORT)).toBeTruthy();
    expect(screen.getAllByRole("checkbox").map((b) => b.getAttribute("aria-label"))).toEqual(TOOLS);
  });

  it("focuses the first tool when the picked list is empty", async () => {
    await open(row("scout-tools", SCOUT), "settings");
    await userEvent.click(screen.getByRole("radio", { name: "expose only picked tools" }));
    for (const b of screen.getAllByRole("checkbox")) await userEvent.click(b);
    expect(screen.getByText("Pick at least one tool, or expose all tools.")).toBeTruthy();
    await userEvent.click(primary());
    expect(document.activeElement).toBe(screen.getByRole("checkbox", { name: "search_notes" }));
  });
  it("keeps the command and container cards off for a server admin, who may not put a process on the host", async () => {
    sess.areas = {};
    await open(row("scout-tools", SCOUT), "connection");
    for (const kind of ["command", "oci"]) {
      const card = runtimeCard(kind);
      expect(card.getAttribute("aria-disabled")).toBe("true");
      expect(card.textContent).toContain("Needs straza-global-mcp-admin");
    }
    await userEvent.click(runtimeCard("command"));
    expect(runtimeCard("remote").getAttribute("aria-checked")).toBe("true");
    sess.areas = null;
  });
});
