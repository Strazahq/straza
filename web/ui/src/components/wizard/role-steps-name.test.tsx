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
  const [draft, setDraft] = React.useState<Draft>({ kind, name: "", description: "", server, plans, composed: [] });
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
// tags reads the two tags beside the name's chip: the name as stored, and
// the name the identity manager imports.
const tags = () => ["[data-name-stored]", "[data-name-iga]"].map((s) => (document.querySelector("[data-name-preview] " + s) as HTMLElement).textContent);
const prefix = () => (document.querySelector("[data-name-prefix]") as HTMLElement).textContent;
const rail = (server: string) => screen.getByRole("radio", { name: "server " + server });
const suffix = () => screen.getByRole("textbox", { name: "role name suffix" });

describe("the first step of an application role", () => {
  it("lists the servers under the kind cards only for an application role", async () => {
    render(<Mounted server="demo-tools" />);
    expect(screen.getAllByRole("radio", { name: /^server / }).map((b) => b.getAttribute("aria-label"))).toEqual(["server demo-tools", "server midpoint", "server obsidian"]);
    expect(rail("obsidian").getAttribute("aria-disabled")).toBe("true");
    expect(rail("obsidian").textContent).toContain("no tools known: the server is stopped");
    await userEvent.click(screen.getByRole("radio", { name: "Business role" }));
    expect(screen.queryByRole("radio", { name: /^server / })).toBeNull();
    expect(screen.getByRole("textbox", { name: "Name" })).toBeTruthy();
    await userEvent.click(screen.getByRole("radio", { name: "Approver role" }));
    expect(screen.queryByRole("radio", { name: /^server / })).toBeNull();
  });

  it("fixes the picked server's prefix, keeps the typed word on a switch and tags the stored name", async () => {
    render(<Mounted server="demo-tools" />);
    expect(prefix()).toBe("demo-tools-");
    expect(tags()).toEqual(["stored as demo-tools-", "identity manager: AR:demo-tools-"]);
    await userEvent.type(suffix(), "Read Only");
    expect(tags()).toEqual(["stored as demo-tools-read-only", "identity manager: AR:demo-tools-read-only"]);
    await userEvent.click(rail("midpoint"));
    expect(prefix()).toBe("midpoint-");
    expect((suffix() as HTMLInputElement).value).toBe("Read Only");
    expect(tags()).toEqual(["stored as midpoint-read-only", "identity manager: AR:midpoint-read-only"]);
  });

  it("draws the prefix as text inside the one name box, and a click on it lands in the input", async () => {
    render(<Mounted server="demo-tools" />);
    const box = document.querySelector("[data-name-prefix]")?.parentElement as HTMLElement;
    expect(box.tagName).toBe("LABEL");
    expect(box.contains(suffix())).toBe(true);
    // The box draws the border and the focus ring. The input draws neither,
    // so focus shows one ring around the prefix and the word together.
    expect(box.className.split(" ")).toEqual(expect.arrayContaining(["border", "focus-within:ring-[3px]"]));
    expect(suffix().className.split(" ")).toEqual(expect.arrayContaining(["border-0", "focus-visible:ring-0", "focus-visible:outline-none!"]));
    expect(suffix().id).toBe("nr-name");
    expect(screen.getByLabelText("Name")).toBe(suffix());
    await userEvent.click(document.querySelector("[data-name-prefix]") as HTMLElement);
    expect(document.activeElement).toBe(suffix());
  });

  it("puts the outcome chip and the two name tags in one row under the name", async () => {
    render(<Mounted server="demo-tools" />);
    await userEvent.type(suffix(), "video");
    const row = document.querySelector("[data-name-preview]") as HTMLElement;
    const chip = row.querySelector('[data-name-check="free"]') as HTMLElement;
    expect(chip.getAttribute("data-tone")).toBe("ok");
    // A screen reader hears the chip as Name available.
    expect(chip.querySelector(".sr-only")?.textContent).toBe("Name ");
    expect(chip.textContent).toBe("Name available");
    expect([...row.children].map((c) => c.textContent)).toEqual(["Name available", "stored as demo-tools-video", "identity manager: AR:demo-tools-video"]);
    expect(screen.queryByText("Policies, access rows and your identity manager refer to the role by this name.")).toBeNull();
  });

  // Each case is the server, the typed word, and the check line the stored
  // name gets. The near check compares the words after the server's prefix,
  // so the server's other roles are not near just for sharing it.
  const checks: [string, string, string, string, string][] = [
    ["refuses a stored name a role already has", "demo-tools", "readers", "error", "A role named demo-tools-readers already exists.Open it"],
    ["warns on a word close to one of the server's roles, and names that role only", "demo-tools", "reader", "warn", "Close to existing: demo-tools-readers."],
    ["reads a word far from every role of the server as free, though they share its prefix", "demo-tools", "audit", "free", "Name available"],
    ["reads a free stored name as free", "midpoint", "readers", "free", "Name available"],
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
      "1 tool ticked on demo-tools is not saved while midpoint is picked.",
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
    expect(check().textContent).toBe("Names beginning with demo-tools- belong to demo-tools. Pick another name, or make an application role for demo-tools.Open demo-tools");
    await userEvent.click(screen.getByRole("button", { name: "Open demo-tools" }));
    expect(onOpenServer).toHaveBeenCalledWith(apps[0]);
  });

  it("keeps the whole-name near check for a business role", async () => {
    render(<Mounted kind="business" />);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "demo-team");
    expect(check().getAttribute("data-name-check")).toBe("warn");
    expect(check().textContent).toBe("Close to existing: demo-tools-readers and demo-tools-sandbox.");
  });

  // Each case is the picked kind, the typed name, and the tags under the
  // field: the stored name and the name an IGA imports, BR: for a business
  // role and AR: for an approver role.
  const lines: [Draft["kind"], string, string[]][] = [
    ["business", "analysts", ["stored as analysts", "identity manager: BR:analysts"]],
    ["approver", "pay-approvers", ["stored as pay-approvers", "identity manager: AR:pay-approvers"]],
  ];
  it.each(lines)("tags the stored and the imported name of a %s role", async (kind, name, want) => {
    render(<Mounted kind={kind} />);
    expect(document.querySelector("[data-name-preview]")).toBeNull();
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), name);
    expect(tags()).toEqual(want);
  });

  it("shows no name line for a business role's name inside a server's prefix", async () => {
    render(<Mounted kind="business" />);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "demo-tools-x");
    expect(document.querySelector("[data-name-preview]")).toBeNull();
  });

  it("leaves a name outside every server's prefix free", async () => {
    render(<Mounted kind="business" />);
    await userEvent.type(screen.getByRole("textbox", { name: "Name" }), "analysts");
    expect(check().getAttribute("data-name-check")).toBe("free");
    expect(check().textContent).toBe("Name available");
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
