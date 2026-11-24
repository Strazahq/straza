import { CircleCheckIcon } from "lucide-react";
import { KindIcon } from "@/components/kind-icon";
import { openNewPolicy } from "@/components/policies-by-role";
import { Button } from "@/components/ui/button";
import type { Kind } from "@/lib/chip-words";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { type RouteKey, visibleRoutes } from "@/lib/routes";
import { FROM_AGENT_MACHINE, NEXT_TILE, WHAT_NEXT, liveAndRead } from "@/lib/save-words";
import { CREATE_ROLE, SAME_AS_COMMANDS, nobodyReaches } from "@/lib/server-words";
import { newRoleCommand } from "@/lib/server-roles-words";
import { adminAreas, adminServers } from "@/lib/session";
import type { HealthRead } from "@/lib/words";
import { Code, Fold, HealthBox, Landed, type Problem, ProblemBlock, type Row } from "./parts";

// agentSnippet is the agent-side check with a real tool name: the three
// JSON-RPC lines a harness sends through the gateway.
export const agentSnippet = (app: string, tool: string) => [
  "printf '%s\\n' \\",
  "  '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"check\",\"version\":\"1\"}}}' \\",
  "  '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}' \\",
  "  '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"" + app + "__" + tool + "\",\"arguments\":{}}}' \\",
  "  | straza mcp --harness claude-code",
].join("\n");

type Props = { rows: Row[]; read: HealthRead | null; name: string; tools: string[]; problem: Problem | null; onOpen: () => void };

// openNewRole hands New role the server to open on, then goes there. The
// server travels in module memory, never the address, so a reload lands on
// the plain wizard.
function openNewRole(server: string) {
  put("roles-new", { server });
  navigate("roles", ["new"]);
}

// whatNextShown says the run landed whole on a server that runs and lists
// a tool, the one outcome that ends on What next. Every other outcome keeps
// the rows, the check's words and their doors.
export const whatNextShown = (rows: Row[], read: HealthRead | null) => !!read && read.kind === "running" && rows.every((r) => r.state === "done");

type Tile = { key: RouteKey; kind: Kind; title: string; line: string; onClick: () => void };

// TileButton is one door of What next: an icon, a line in the 600 weight
// and one sentence under it, all one button.
function TileButton({ tile }: { tile: Tile }) {
  return (
    <button
      type="button"
      onClick={tile.onClick}
      className="flex min-w-0 flex-col items-start gap-1 rounded-md border border-border bg-card px-4 py-3 text-left outline-none hover:border-link hover:bg-accent-bg focus-visible:ring-[3px] focus-visible:ring-ring/50"
      data-next-tile={tile.key}
    >
      <span className="flex items-center gap-2 font-semibold text-foreground">
        <KindIcon kind={tile.kind} className="text-muted-foreground" />
        {tile.title}
      </span>
      {tile.line && <span className="text-dense text-text-2">{tile.line}</span>}
    </button>
  );
}

// WhatNext ends the wizard on a server that runs: the live line, then a
// tile for each next step whose page this session's areas open, then the
// same role as commands and the check from the agent's machine.
function WhatNext({ name, tools, onOpen }: { name: string; tools: string[]; onOpen: () => void }) {
  const open = visibleRoutes(adminAreas(), adminServers()).map((r) => r.key);
  const all: Tile[] = [
    { key: "roles", kind: "role-application", title: NEXT_TILE.role.title, line: NEXT_TILE.role.line(tools.length), onClick: () => openNewRole(name) },
    { key: "policies", kind: "policy-set", title: NEXT_TILE.policy.title, line: NEXT_TILE.policy.line, onClick: () => openNewPolicy({ lane: "mcp", app: name }) },
    { key: "servers", kind: "server", title: NEXT_TILE.open.title(name), line: NEXT_TILE.open.line, onClick: onOpen },
  ];
  const tiles = all.filter((t) => open.includes(t.key));
  return (
    <div className="flex flex-col gap-4" data-what-next>
      <div role="status" className="flex items-center gap-2 rounded-md border border-ok/40 bg-ok-bg px-3 py-2.5 text-dense text-foreground" data-live>
        <CircleCheckIcon aria-hidden="true" className="size-4 shrink-0 text-ok" />
        <span>{liveAndRead(name, tools.length)}</span>
      </div>
      <h3 className="text-section leading-7 font-semibold text-foreground">{WHAT_NEXT}</h3>
      {tiles.length > 0 && (
        <div className="grid gap-3 sm:grid-cols-3">
          {tiles.map((t) => <TileButton key={t.key} tile={t} />)}
        </div>
      )}
      <Fold title={SAME_AS_COMMANDS}>
        <Code className="rounded-none border-0">{newRoleCommand(name)}</Code>
      </Fold>
      <Snippet name={name} tools={tools} />
    </div>
  );
}

// Snippet is the check from the agent's machine with the server's first
// tool, under a closed fold.
function Snippet({ name, tools }: { name: string; tools: string[] }) {
  return (
    <Fold title={FROM_AGENT_MACHINE}>
      <Code className="rounded-none border-0">{agentSnippet(name, tools[0] || "tool")}</Code>
    </Fold>
  );
}

// CheckStep is what landed and what the server answered. A run that landed
// whole on a server that runs ends on What next; any other outcome shows
// the rows, the check's words and, once the server runs, the door to the
// role that gives access to it.
export function CheckStep({ rows, read, name, tools, problem, onOpen }: Props) {
  if (whatNextShown(rows, read)) return <WhatNext name={name} tools={tools} onOpen={onOpen} />;
  return (
    <div className="flex flex-col gap-4">
      <Landed rows={rows} />
      {read && <HealthBox read={read} />}
      <ProblemBlock problem={problem} />
      {read && read.kind === "running" && (
        <>
          <div className="flex flex-col gap-2 rounded-md border border-border bg-card px-4 py-3" data-nobody-reaches>
            <div className="font-semibold text-foreground">{nobodyReaches(name)}</div>
            <div>
              <Button onClick={() => openNewRole(name)}>{CREATE_ROLE}</Button>
            </div>
            <Fold title={SAME_AS_COMMANDS}>
              <Code className="rounded-none border-0">{newRoleCommand(name)}</Code>
            </Fold>
          </div>
          <Snippet name={name} tools={tools} />
        </>
      )}
    </div>
  );
}
