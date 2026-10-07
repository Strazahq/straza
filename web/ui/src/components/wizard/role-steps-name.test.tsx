import * as React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type Draft, NameStep } from "./role-steps";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { AppRow, RoleRow, ToolRow } from "@/lib/api";

// The first step of New role: an application role picks its server under
// the kind cards and types only the word after the server's prefix, while a
// business or an approver role keeps a free name that stays out of every
// server's prefix.

const apps: AppRow[] = [
  { id: "app-9", name: "demo-tools", runtime: "http", status: "running", reached_by: [] },
  { id: "app-2", name: "midpoint", runtime: "http", status: "running", reached_by: [] },
  { id: "app-3", name: "obsidian", runtime: "command", status: "stopped", reached_by: [] },
];
const tools: ToolRow[] = [
  { id: "t1", app: "demo-tools", app_id: "app-9", name: "echo" },
  { id: "t2", app: "demo-tools", app_id: "app-9", name: "add" },
  { id: "t3", app: "midpoint", app_id: "app-2", name: "read-user" },
];
const toolsOf = (app: string) => tools.filter((t) => t.app === app);
const roles: RoleRow[] = [
  { id: "r-1", name: "dev-tools", kind: "application" },
  { id: "r-2", name: "demo-tools-readers", kind: "application", server: "demo-tools" },
  { id: "r-3", name: "demo-tools-sandbox", kind: "application", server: "demo-tools" },
];

const onOpenServer = vi.fn();

type MountedProps = { fromServer?: string; kind?: Draft["kind"]; server?: string; plans?: Draft["plans"]; miss?: "server" | null };

function Mounted({ fromServer, kind = "application", server = "", plans = {}, miss = null }: MountedProps) {
  const [draft, setDraft] = React.useState<Draft>({ kind, name: "", description: "", server, plans, composed: [], packs: [] });
  return (
    <TooltipProvider>
      <NameStep
        draft={draft}
        roles={roles}
        apps={apps}
        read
        toolsOf={toolsOf}
        problem={null}
        fromServer={fromServer}
        miss={miss}
        nameBox={React.createRef<HTMLInputElement>()}
        onKind={(k) => setDraft((d) => ({ ...d, kind: k }))}
        onPick={(s) => setDraft((d) => ({ ...d, server: s }))}
        onName={(name) => setDraft((d) => ({ ...d, name }))}
        onDescription={vi.fn()}
        onOpenExact={vi.fn()}
        onOpenServer={onOpenServer}
      />
    </TooltipProvider>
  );
}

const check = () => document.querySelector("[data-name-check]") as HTMLElement;
const preview = () => (document.querySelector("[data-name-preview]") as HTMLElement).textContent;
const prefix = () => (document.querySelector("[data-name-prefix]") as HTMLElement).textContent;
const rail = (server: string) => screen.getByRole("radio", { name: "server " + server });
const suffix = () => screen.getByRole("textbox", { name: "role name suffix" });

describe("the first step of an application role", () => {
  it("lists the servers under the kind cards only for an application role", async () => {
    render(<Mounted server="demo-tools" />);
    expect(screen.getAllByRole("radio", { name: /^server / }).map((b) => b.getAttribute("aria-label"))).toEqual(["server demo-tools", "server midpoint", "server obsidian"]);
    expect(rail("obsidian").getAttribute("aria-disabled")).toBe("true");
    expect(rail("obsidian").textContent).toContain("no tools known: the server is stopped");
    await userEvent.click(screen.getByRole("radio", { name: "kind business" }));
    expect(screen.queryByRole("radio", { name: /^server / })).toBeNull();
    expect(screen.getByRole("textbox", { name: "Name" })).toBeTruthy();
    await userEvent.click(screen.getByRole("radio", { name: "kind approver" }));
    expect(screen.queryByRole("radio", { name: /^server / })).toBeNull();
  });

  it("fixes the picked server's prefix, keeps the typed word on a switch and previews the stored name", async () => {
    render(<Mounted server="demo-tools" />);
    expect(prefix()).toBe("demo-tools-");
    expect(preview()).toBe("Stored as demo-tools-, in midPoint as AR:demo-tools-.");
    await userEvent.type(suffix(), "Read Only");
    expect(preview()).toBe("Stored as demo-tools-read-only, in midPoint as AR:demo-tools-read-only.");
    await userEvent.click(rail("midpoint"));
    expect(prefix()).toBe("midpoint-");
    expect((suffix() as HTMLInputElement).value).toBe("Read Only");
    expect(preview()).toBe("Stored as midpoint-read-only, in midPoint as AR:midpoint-read-only.");
  });

  // Each case is the server, the typed word, and the check line the stored
  // name gets. The near check compares the words after the server's prefix,
  // so the server's other roles are not near just for sharing it.
  const checks: [string, string, string, string, string][] = [
    ["refuses a stored name a role already has", "demo-tools", "readers", "error", "A role named demo-tools-readers already exists.Open it"],
    ["warns on a word close to one of the server's roles, and names that role only", "demo-tools", "reader", "warn", "Close to existing: demo-tools-readers."],
    ["reads a word far from every role of the server as free, though they share its prefix", "demo-tools", "audit", "free", "Name available."],
    ["reads a free stored name as free", "midpoint", "readers", "free", "Name available."],
  ];
  it.each(checks)("%s", async (_case, server, word, level, text) => {
    render(<Mounted server={server} />);
    await userEvent.type(suffix(), word);
    expect(check().getAttribute("data-name-check")).toBe(level);
    expect(check().textContent).toBe(text);
  });

  it("says under the servers that ticks on another server are not given", () => {
    render(<Mounted server="midpoint" plans={{ "demo-tools": { reach: "tick", picked: { echo: true }, call: "allow", choice: {}, shape: { pool: "sponsor", how: "hold", hold: 120, ticket: 86400, grant: 3600 }, own: {} } }} />);
    expect((document.querySelector('[data-switch-warning="demo-tools"]') as HTMLElement).textContent).toBe(
      "An application role reaches one server: the 1 tool ticked on demo-tools is not given while midpoint is picked.",
    );
  });

  it("says a server is missing when Next found none", () => {
    render(<Mounted server="midpoint" miss="server" />);
    expect((document.querySelector("[data-server-miss]") as HTMLElement).textContent).toBe("Pick the server to give access to.");
  });
});

describe("the name check against a server's prefix", () => {
  it("refuses a business role's name inside a server's prefix and opens that server", async () => {
    render(<Mounted kind="business" />);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "demo-tools-x");
    expect(check().getAttribute("data-name-check")).toBe("server");
    expect(check().textContent).toBe("Role names beginning with demo-tools- belong to the server demo-tools. Create it on that server's page so it becomes server-owned.Open demo-tools");
    await userEvent.click(screen.getByRole("button", { name: "Open demo-tools" }));
    expect(onOpenServer).toHaveBeenCalledWith(apps[0]);
  });

  it("keeps the whole-name near check for a business role", async () => {
    render(<Mounted kind="business" />);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "demo-team");
    expect(check().getAttribute("data-name-check")).toBe("warn");
    expect(check().textContent).toBe("Close to existing: demo-tools-readers and demo-tools-sandbox.");
  });

  it("leaves a name outside every server's prefix free", async () => {
    render(<Mounted kind="business" />);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "analysts");
    expect(check().getAttribute("data-name-check")).toBe("free");
    expect(check().textContent).toBe("Name available.");
  });
});

describe("the first step opened from a new server", () => {
  // Each case is the door's server, the kind and the picked server, and
  // the line the step shows.
  const cases: [string, string | undefined, Draft["kind"], string, string | null][] = [
    ["names the server Access opens on", "demo-tools", "application", "demo-tools", "Access opens on demo-tools, the server you just added."],
    ["says nothing without a door", undefined, "application", "demo-tools", null],
    ["says nothing for a business role", "demo-tools", "business", "demo-tools", null],
    ["says nothing once another server is picked", "demo-tools", "application", "midpoint", null],
  ];
  it.each(cases)("%s", (_name, fromServer, kind, server, want) => {
    render(<Mounted fromServer={fromServer} kind={kind} server={server} />);
    expect(document.querySelector("[data-from-server]")?.textContent ?? null).toBe(want);
  });
});
