import { Button } from "@/components/ui/button";
import { put } from "@/lib/handoff";
import { navigate } from "@/lib/router";
import { NOBODY_REACHES_LINE, SAME_AS_COMMANDS, createRoleFor, nobodyReaches } from "@/lib/server-words";
import { newRoleCommand } from "@/lib/server-roles-words";
import type { HealthRead } from "@/lib/words";
import { cn } from "@/lib/utils";
import { CAPS, Code, Fold, Landed, type Problem, ProblemBlock, type Row } from "./parts";

// agentSnippet is the agent-side check with a real tool name: the three
// JSON-RPC lines a harness sends through the gateway.
export const agentSnippet = (app: string, tool: string) => [
  "printf '%s\\n' \\",
  "  '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"check\",\"version\":\"1\"}}}' \\",
  "  '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}' \\",
  "  '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"" + app + "__" + tool + "\",\"arguments\":{\"message\":\"hello through the gateway\"}}}' \\",
  "  | straza mcp --harness claude-code",
].join("\n");

const TONE: Record<HealthRead["tone"], { box: string; lead: string }> = {
  ok: { box: "border-ok/40 bg-ok-bg", lead: "text-ok" },
  warn: { box: "border-warn/40 bg-warn-bg", lead: "text-warn" },
  danger: { box: "border-danger/40 bg-danger-bg", lead: "text-danger" },
};

type Props = { rows: Row[]; read: HealthRead | null; name: string; tools: string[]; problem: Problem | null };

// openNewRole hands New role the server to open on, then goes there. The
// server travels in module memory, never the address, so a reload lands on
// the plain wizard.
function openNewRole(server: string) {
  put("roles-new", { server });
  navigate("roles", ["new"]);
}

// CheckStep is what landed, what the server answered, and, once it runs,
// the door to the role that gives access to it.
export function CheckStep({ rows, read, name, tools, problem }: Props) {
  return (
    <div className="flex flex-col gap-4">
      <Landed rows={rows} />
      {read && (
        <div className={cn("rounded-md border px-3 py-2.5 text-sm text-foreground", TONE[read.tone].box)} data-health={read.kind}>
          <b className={cn("font-semibold", TONE[read.tone].lead)}>{read.lead}</b>{" " + read.text}
        </div>
      )}
      <ProblemBlock problem={problem} />
      {read && read.kind === "running" && (
        <>
          <div className="flex flex-col gap-2 rounded-md border border-border bg-card px-4 py-3" data-nobody-reaches>
            <div className="font-semibold text-foreground">{nobodyReaches(name)}</div>
            <p className="max-w-[80ch] text-sm text-text-2">{NOBODY_REACHES_LINE}</p>
            <div>
              <Button onClick={() => openNewRole(name)}>{createRoleFor(name)}</Button>
            </div>
            <Fold title={SAME_AS_COMMANDS}>
              <Code className="rounded-none border-0">{newRoleCommand(name)}</Code>
            </Fold>
          </div>
          <div className="flex flex-col gap-1.5">
            <div className={CAPS}>from the agent's machine</div>
            <Code>{agentSnippet(name, tools[0] || "tool")}</Code>
          </div>
        </>
      )}
    </div>
  );
}
