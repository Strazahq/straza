import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { ServerOverview } from "./server-overview";
import { TooltipProvider } from "@/components/ui/tooltip";
import { type AppRow, type SecretRow, listSecrets, listUsers } from "@/lib/api";
import { notify } from "@/lib/notify";
import { ENV_WARNING_SHORT } from "@/lib/words";

vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), listSecrets: vi.fn(), listRoles: vi.fn(), setSecret: vi.fn(), removeSecret: vi.fn(), listUsers: vi.fn() }));
vi.mock("@/lib/notify", () => ({ notify: { ok: vi.fn(), warn: vi.fn(), failed: vi.fn() } }));
// The origin line reads the drafts list and has its own suite; here it
// stands in with the props the Overview hands it.
vi.mock("@/components/origin-line", () => ({
  OriginLine: ({ object, file, differs }: { object: string; file?: string; differs?: boolean }) => <div data-origin-stub={object} data-file={file || ""} data-differs={String(!!differs)} />,
}));

const BEARER = { as: "header", name: "Authorization", template: "Bearer {{secret}}" };
const shared = (fingerprint: string, set_at: string): SecretRow => ({ id: "s-" + fingerprint, scope: "app", role: "", fingerprint, set_at });
const forRole = (role: string, fingerprint: string, set_at: string): SecretRow => ({ id: "s-" + fingerprint, scope: "role", role, fingerprint, set_at });
const withCredential = (app: AppRow, credential: Record<string, unknown>): AppRow => ({ ...app, manifest: { ...app.manifest, straza: { ...app.manifest!.straza, credential } } });

// The fixture servers: midpoint as a file in the apps
// directory names it, scout-tools added through the API, u3-echo as a command, and a
// server whose manifest sets every limit.
const midpoint: AppRow = {
  id: "01a07c92", name: "midpoint", runtime: "remote", version: "0.3.1-dev", status: "running", tools: [], reached_by: ["dev-tools"],
  url: "http://mcp-midpoint-http:3001/mcp", file: "/etc/straza/apps/midpoint.app.yaml",
  manifest: {
    apiVersion: "straza.dev/v1beta1", kind: "App",
    metadata: { name: "midpoint", description: "midPoint IGA operations, per-user identity (Switch-To-Principal) - reads allowed, writes approve-gated." },
    server: { name: "midpoint", version: "0.3.1-dev" },
    straza: {
      runtime: { kind: "remote", remote: { url: "http://mcp-midpoint-http:3001/mcp" } },
      credential: { kind: "oauth", agents: "own", oauth: { provider: "keycloak", scopes: ["openid"] }, inject: BEARER },
      exposure: { tools: ["*"] },
      limits: { rps: 10 },
    },
  },
};
const scout: AppRow = {
  id: "01a0913c", name: "scout-tools", runtime: "remote", version: "1.4.0", status: "running", tools: [], reached_by: ["dev-tools"],
  url: "http://scout-tools.internal:8080/mcp",
  manifest: {
    metadata: { name: "scout-tools", description: "Search and read the team's scouting notes." },
    straza: { runtime: { kind: "remote", remote: { url: "http://scout-tools.internal:8080/mcp" } }, credential: { kind: "static", inject: BEARER }, exposure: { tools: ["*"] } },
  },
};
const echo: AppRow = {
  id: "01a0913d", name: "u3-echo", runtime: "command", version: "0.1.0", status: "stopped", paused: true, tools: [], reached_by: [],
  manifest: {
    metadata: { name: "u3-echo" },
    straza: {
      runtime: { kind: "command", command: { exec: "/var/lib/straza/u3-echo", args: ["--stdio"], env: [{ name: "LOG_LEVEL", value: "info" }] } },
      credential: { kind: "static", inject: { as: "env", name: "ECHO_TOKEN", template: "{{secret}}" } },
      exposure: { tools: ["*"] },
    },
  },
};
const limited: AppRow = {
  id: "01a0913e", name: "notes-limited", runtime: "remote", version: "2.0.0", status: "running", tools: [], reached_by: [],
  url: "http://notes.internal:8080/mcp",
  manifest: {
    metadata: { name: "notes-limited" },
    straza: {
      runtime: { kind: "remote", remote: { url: "http://notes.internal:8080/mcp", auth: "passthrough" } },
      exposure: { tools: ["search_notes", "read_note"] },
      limits: { cpu: "500m", mem: "256Mi", rps: 5, timeoutSeconds: 20 },
    },
  },
};
const legacy: AppRow = { id: "app-2", name: "legacy-app", runtime: "command", version: "1.0", status: "running", tools: ["ping"], reached_by: [] };

type Want = string | string[] | { value: string; sub?: string; warn?: string };
type Row = { value: string; sub: string; warn: string };

// facts reads a card's rows in order as label to value; the value leaves
// out the row's buttons and its sub and warn lines, read on their own.
function facts(name: string): [string, Row][] {
  const card = screen.getByRole("region", { name });
  return [...card.querySelectorAll("dt")].map((dt) => {
    const dd = dt.nextElementSibling as HTMLElement;
    const copy = dd.cloneNode(true) as HTMLElement;
    copy.querySelectorAll("button, [data-sub], [data-warn]").forEach((n) => n.remove());
    return [dt.textContent || "", {
      value: (copy.textContent || "").replace(/\s+/g, " ").trim(),
      sub: dd.querySelector("[data-sub]")?.textContent || "",
      warn: dd.querySelector("[data-warn]")?.textContent || "",
    }];
  });
}

function expectFacts(name: string, want: Record<string, Want>) {
  const got = facts(name);
  expect(got.map(([label]) => label)).toEqual(Object.keys(want));
  for (const [label, row] of got) {
    const w = want[label];
    if (typeof w === "string") expect(row.value, label).toBe(w);
    else if (Array.isArray(w)) for (const part of w) expect(row.value, label).toContain(part);
    else {
      expect(row.value, label).toBe(w.value);
      expect(row.sub, label).toBe(w.sub || "");
      expect(row.warn, label).toBe(w.warn || "");
    }
  }
}

const onChange = vi.fn();
// mount renders the Overview with both defaults the config names, the
// per-call timeout and the remote rate limit, and with neither when
// upstreamTimeout is null, as when the config did not answer.
const mount = (app: AppRow, upstreamTimeout: number | null) => render(
  <TooltipProvider><ServerOverview app={app} upstreamTimeout={upstreamTimeout} remoteRps={upstreamTimeout === null ? null : 20} onChange={onChange} onRefused={vi.fn()} /></TooltipProvider>,
);
const settled = () => waitFor(() => expect(Object.fromEntries(facts("Server authentication"))["Stored here"].value).not.toBe("Reading the stored credential."));

const cases: { name: string; app: AppRow; secrets: SecretRow[]; upstream: number | null; want: Record<"Connection" | "Server authentication" | "Tools and limits", Record<string, Want>> }[] = [
  {
    name: "a remote sign-in server a file defines",
    app: midpoint, secrets: [], upstream: 30,
    want: {
      Connection: {
        Transport: "Streamable HTTP",
        Address: "http://mcp-midpoint-http:3001/mcp",
        Version: { value: "0.3.1-dev" },
      },
      "Server authentication": {
        Type: { value: "Each person's own sign-in" },
        "Provider and scopes": "keycloak, scopes openid",
        "Sent as": "Authorization: Bearer <the person's token>",
        "Agents with nothing of their own": "Refused, because an agent cannot sign in through a browser.",
        "Stored here": "Each person's own, kept per person.",
        "Per-role secrets": "Not used here. Each person's own sign-in is the credential.",
      },
      "Tools and limits": {
        Description: "midPoint IGA operations, per-user identity (Switch-To-Principal) - reads allowed, writes approve-gated.",
        "Tools exposed": "All tools, including ones it adds later.",
        "Rate limit": "10 calls per second, per session.",
        "Per-call timeout": "30 s per call, the server-wide default.",
      },
    },
  },
  {
    name: "a remote static server added through the API, with a stored secret",
    app: scout, secrets: [shared("9f3a1c", "2026-09-11T15:02:00Z")], upstream: null,
    want: {
      Connection: {
        Transport: "Streamable HTTP",
        Address: "http://scout-tools.internal:8080/mcp",
        Version: { value: "1.4.0" },
      },
      "Server authentication": {
        Type: { value: "One shared secret", sub: "The server sees one identity. Straza's audit keeps the person or agent." },
        "Sent as": "Authorization: Bearer <secret>",
        "Stored here": { value: "Set 2026-09-11 15:02:00 UTC, fingerprint 9f3a1c…" },
        "Per-role secrets": "None. A role's own secret replaces the shared one for that role's calls.",
      },
      "Tools and limits": {
        Description: "Search and read the team's scouting notes.",
        "Tools exposed": "All tools, including ones it adds later.",
        "Rate limit": "The default for a remote server.",
        "Per-call timeout": "The server-wide default.",
      },
    },
  },
  {
    name: "a command server with its environment and a secret sent in it",
    app: echo, secrets: [shared("41be07", "2026-09-11T13:40:00Z")], upstream: 30,
    want: {
      Connection: {
        Transport: "stdio, a process Straza starts on the strazad host",
        Executable: "/var/lib/straza/u3-echo",
        Arguments: "--stdio",
        "Working directory": "Straza's own.",
        Environment: ["LOG_LEVEL=info", "ECHO_TOKEN = the stored secret"],
        Version: { value: "0.1.0" },
      },
      "Server authentication": {
        Type: { value: "One shared secret", sub: "The server sees one identity. Straza's audit keeps the person or agent." },
        "Sent as": "Variable ECHO_TOKEN",
        "Stored here": "Set 2026-09-11 13:40:00 UTC, fingerprint 41be07…",
        "Per-role secrets": "None. A role's own secret replaces the shared one for that role's calls.",
        Risk: ENV_WARNING_SHORT,
      },
      "Tools and limits": {
        Description: "None.",
        "Tools exposed": "All tools, including ones it adds later.",
        "Rate limit": "Not limited.",
        "Per-call timeout": "30 s per call, the server-wide default.",
      },
    },
  },
  {
    name: "a server whose manifest sets a rate, a timeout and picked tools, and a cpu and mem limit the card leaves out",
    app: limited, secrets: [], upstream: 30,
    want: {
      Connection: {
        Transport: "Streamable HTTP",
        Address: "http://notes.internal:8080/mcp",
        Version: { value: "2.0.0" },
        Authentication: "Straza adds no credential: auth passthrough is set in its manifest.",
      },
      "Server authentication": {
        Type: { value: "None", sub: "No credential is sent." },
        "Stored here": "Nothing.",
      },
      "Tools and limits": {
        Description: "None.",
        "Tools exposed": "Only search_notes, read_note.",
        "Rate limit": "5 calls per second, per session.",
        "Per-call timeout": "20 s per call.",
      },
    },
  },
  {
    name: "an older row that carries no manifest",
    app: legacy, secrets: [], upstream: 30,
    want: {
      Connection: {
        Transport: "stdio, a process Straza starts on the strazad host",
        Version: { value: "1.0" },
      },
      "Server authentication": {
        Type: "Not known here: the console cannot read a stored manifest for this server. The rows below are what Straza holds.",
        "Stored here": "No shared secret is stored yet.",
        "Per-role secrets": "None. A role's own secret replaces the shared one for that role's calls.",
      },
      "Tools and limits": { Manifest: "Not known here: the console cannot read a stored manifest for this server." },
    },
  },
];

describe("the Overview of a server", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it.each(cases)("reads $name as three cards of labeled rows", async ({ app, secrets, upstream, want }) => {
    vi.mocked(listSecrets).mockResolvedValue(secrets);
    mount(app, upstream);
    await settled();
    expectFacts("Connection", want.Connection);
    expectFacts("Server authentication", want["Server authentication"]);
    expectFacts("Tools and limits", want["Tools and limits"]);
    expect(listSecrets).toHaveBeenCalledWith(app.id);
  });

  it.each([
    ["with no rate", scout],
    ["with a rate of 0", { ...scout, manifest: { ...scout.manifest, straza: { ...scout.manifest!.straza, limits: { rps: 0 } } } }],
  ])("names the default a remote server %s gets, as the config answered it", async (_case, app) => {
    vi.mocked(listSecrets).mockResolvedValue([]);
    mount(app, 30);
    await settled();
    expect(Object.fromEntries(facts("Tools and limits"))["Rate limit"].value).toBe("20 calls per second, per session, the default.");
  });

  it("gives a server a file names the origin line and Change on every card, and keeps its secret doors", async () => {
    const sharedFile: AppRow = { ...midpoint, file_differs: true, manifest: { ...midpoint.manifest, straza: { ...midpoint.manifest!.straza, credential: { kind: "oauth", agents: "shared", oauth: { provider: "keycloak" }, inject: BEARER } } } };
    vi.mocked(listSecrets).mockResolvedValue([shared("7c1e", "2026-09-10T09:12:00Z")]);
    mount(sharedFile, 30);
    await settled();
    const origin = document.querySelector("[data-origin-stub]") as HTMLElement;
    expect([origin.getAttribute("data-origin-stub"), origin.getAttribute("data-file"), origin.getAttribute("data-differs")]).toEqual(["App/midpoint", "/etc/straza/apps/midpoint.app.yaml", "true"]);
    expect(document.querySelector("[data-file-strip]")).toBeNull();
    for (const name of ["Change connection", "Change server authentication", "Change tools and limits"]) expect(screen.getByRole("button", { name })).toBeTruthy();
    const cred = screen.getByRole("region", { name: "Server authentication" });
    for (const door of ["Set it again", "Remove", "Add for a role"]) expect(within(cred).getByRole("button", { name: door })).toBeTruthy();
    expect(screen.queryByText(/Defined in/)).toBeNull();
  });

  it("puts one outline Change button on each card of a server the console may change", async () => {
    vi.mocked(listSecrets).mockResolvedValue([shared("9f3a1c", "2026-09-11T15:02:00Z")]);
    mount(scout, null);
    await settled();
    expect(document.querySelector("[data-origin-stub]")?.getAttribute("data-file")).toBe("");
    for (const [card, which] of [["Connection", "connection"], ["Server authentication", "credential"], ["Tools and limits", "settings"]] as const) {
      const button = within(screen.getByRole("region", { name: card })).getByRole("button", { name: "Change " + card.toLowerCase() });
      expect(button.textContent).toBe("Change");
      expect(button.getAttribute("data-variant")).toBe("outline");
      fireEvent.click(button);
      expect(onChange).toHaveBeenLastCalledWith(which);
    }
    expect(onChange).toHaveBeenCalledTimes(3);
  });

  it.each([
    { name: "a token server whose agents get nothing", credential: { kind: "token", agents: "own", inject: BEARER }, doors: ["Set it again", "Remove"] },
    { name: "a server that takes no credential", credential: { kind: "none" }, doors: ["Set it again", "Remove"] },
  ])("marks a shared secret stored on $name as unused", async ({ credential, doors }) => {
    vi.mocked(listSecrets).mockResolvedValue([shared("5d2a", "2026-09-11T12:00:00Z")]);
    mount(withCredential(scout, credential), null);
    await settled();
    const row = Object.fromEntries(facts("Server authentication"))["Stored here"];
    expect(row.value).toBe("Set 2026-09-11 12:00:00 UTC, fingerprint 5d2a…");
    expect(row.warn).toBe("Stored, but this credential type does not read it. Remove it, or let agents fall back to it.");
    expect(document.querySelector("[data-warn]")?.className).toContain("text-warn");
    const cred = screen.getByRole("region", { name: "Server authentication" });
    for (const door of doors) expect(within(cred).getByRole("button", { name: door })).toBeTruthy();
  });

  it.each([
    { name: "a static server, which reads them", credential: { kind: "static", inject: BEARER }, add: true, warn: "" },
    { name: "a token server whose agents get nothing", credential: { kind: "token", agents: "own", inject: BEARER }, add: false, warn: "Stored, but this credential type does not read them. Remove them, or let agents fall back to them." },
  ])("lists every stored per-role secret of $name, each with its Remove door", async ({ credential, add, warn }) => {
    vi.mocked(listSecrets).mockResolvedValue([forRole("dev-tools", "b7e2", "2026-09-04T08:00:00Z"), forRole("scout-role", "3ac9", "2026-09-05T09:00:00Z")]);
    mount(withCredential(scout, credential), null);
    await settled();
    const row = Object.fromEntries(facts("Server authentication"))["Per-role secrets"];
    expect(row.value).toContain("dev-tools, fingerprint b7e2…, set 2026-09-04 08:00:00 UTC");
    expect(row.value).toContain("scout-role, fingerprint 3ac9…, set 2026-09-05 09:00:00 UTC");
    expect(row.value).not.toContain("Not used here");
    expect(row.warn).toBe(warn);
    // The mark is said once for the row, not once per stored line.
    expect(document.querySelectorAll("[data-warn]")).toHaveLength(warn ? 1 : 0);
    const roles = document.querySelector("[data-role-secrets]") as HTMLElement;
    expect(within(roles).getAllByRole("button", { name: "Remove" })).toHaveLength(2);
    expect(within(roles).queryByRole("button", { name: "Add for a role" }) !== null).toBe(add);
  });

  it("says a removal changes no call when the credential type reads neither the shared secret nor a role's", async () => {
    vi.mocked(listSecrets).mockResolvedValue([forRole("dev-tools", "b7e2", "2026-09-04T08:00:00Z")]);
    mount(withCredential(scout, { kind: "token", agents: "own", inject: BEARER }), null);
    await settled();
    fireEvent.click(within(document.querySelector("[data-role-secrets]") as HTMLElement).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).toContain("Remove the secret for dev-tools?");
    expect(dialog.textContent).toContain("Nothing reads it while this credential type is set, so removing it changes no call. The stored value is deleted.");
  });

  it("quotes an argument that holds a space, as the Change sheet writes it", async () => {
    const titled: AppRow = { ...echo, manifest: { ...echo.manifest, straza: { ...echo.manifest!.straza, runtime: { kind: "command", command: { exec: "/var/lib/straza/u3-echo", args: ["--title", "My Server", "--stdio"] } } } } };
    vi.mocked(listSecrets).mockResolvedValue([]);
    mount(titled, null);
    await settled();
    expect(Object.fromEntries(facts("Connection"))["Arguments"].value).toBe('--title "My Server" --stdio');
  });

  it("leaves the unused mark off a secret the credential reads", async () => {
    vi.mocked(listSecrets).mockResolvedValue([shared("9f3a1c", "2026-09-11T15:02:00Z")]);
    mount(scout, null);
    await settled();
    expect(document.querySelector("[data-warn]")).toBeNull();
  });

  it("copies the address and says so, and says what to do when the browser refuses", async () => {
    vi.mocked(listSecrets).mockResolvedValue([]);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(window.navigator, "clipboard", { value: { writeText }, configurable: true, writable: true });
    mount(scout, null);
    await settled();
    fireEvent.click(screen.getByRole("button", { name: "Copy the address" }));
    await waitFor(() => expect(notify.ok).toHaveBeenCalledWith("Copied the address."));
    expect(writeText).toHaveBeenCalledWith("http://scout-tools.internal:8080/mcp");
    writeText.mockRejectedValueOnce(new Error("NotAllowedError"));
    fireEvent.click(screen.getByRole("button", { name: "Copy the address" }));
    await waitFor(() => expect(notify.failed).toHaveBeenCalledWith("The address could not be copied: the browser refused the clipboard. Select it and copy it by hand."));
  });

  it("reads the Administered by card: the minted role with its help, the holders, the roles above as tags, and no Change button", async () => {
    vi.mocked(listSecrets).mockResolvedValue([]);
    vi.mocked(listUsers).mockResolvedValue({ items: [{ id: "u1", username: "erin", status: "active" }, { id: "u2", username: "hana", status: "active", user_type: "agent" }] as never, next_cursor: "" });
    mount({ ...scout, admin_role: "mcp-admin-scout-tools", admin_role_id: "r-9" }, null);
    const card = within(document.querySelector('[data-card="admin-role"]') as HTMLElement);
    expect(card.getByRole("heading", { name: "Server administration" })).toBeTruthy();
    expect(card.getByText("mcp-admin-scout-tools").querySelector("svg")?.getAttribute("class")).toContain("lucide-server-cog");
    expect(card.queryByText("server admin role")).toBeNull();
    expect(card.getByRole("button", { name: "Help: admin role" })).toBeTruthy();
    await card.findByText("erin");
    expect(card.getByText("erin").querySelector('svg')?.getAttribute("class")).toContain("lucide-user-round");
    expect(card.getByText("hana").querySelector('svg')?.getAttribute("class")).toContain("lucide-bot");
    expect(card.getByText("hana").className).toContain("bg-teal-bg");
    expect(card.getByText("Takes effect within 5 minutes of an assignment.")).toBeTruthy();
    for (const role of ["straza-admin", "straza-global-mcp-admin"]) expect(card.getByText(role).querySelector("svg")?.getAttribute("class")).toContain("lucide-shield");
    expect(card.getByText("Assign it in your identity manager.")).toBeTruthy();
    expect(card.queryByRole("button", { name: /^Change/ })).toBeNull();
    expect(listUsers).toHaveBeenCalledWith("role=mcp-admin-scout-tools&sort=name&order=asc&limit=100");
  });

  it("says who holds the role is not readable when the session may not list users, and Nobody yet when nobody does", async () => {
    vi.mocked(listSecrets).mockResolvedValue([]);
    vi.mocked(listUsers).mockRejectedValueOnce(Object.assign(new Error("session lacks grant identity:read"), { status: 403 }));
    const { unmount } = mount({ ...scout, admin_role: "mcp-admin-scout-tools", admin_role_id: "r-9" }, null);
    await screen.findByText("The holders are not shown, because your role does not include Users and Roles.");
    unmount();
    vi.mocked(listUsers).mockResolvedValueOnce({ items: [], next_cursor: "" });
    mount({ ...scout, admin_role: "mcp-admin-scout-tools", admin_role_id: "r-9" }, null);
    await screen.findByText("Nobody yet.");
  });
});
