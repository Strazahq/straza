import * as React from "react";
import { PlusIcon, RefreshCwIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { ServersTable } from "@/components/servers-table";
import { type ApiError, type AppRow, listApps } from "@/lib/api";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { serverAdminLine } from "@/lib/server-words";
import { adminAreas } from "@/lib/session";

// A ready state keeps its data through a failed reload: problem carries
// what went wrong and lastRead when the data was last confirmed (rule 22).
type State =
  | { kind: "loading" }
  | { kind: "ready"; apps: AppRow[]; lastRead: Date; problem: string | null }
  | { kind: "error"; message: string };

const route = routeByKey("servers");

// Servers is the MCP servers screen: the list, with Reload and Add MCP
// server as the page actions. A row opens the server's page, which holds
// everything about it (rule 21), and the wizard is a route of its own. A
// session that holds servers' admin roles and no apps grant sees the
// servers strazad lists for it, Reload alone, and a line that says who
// registers new servers.
export function Servers() {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [, tick] = React.useState(0);
  const areas = adminAreas();
  const serverAdmin = areas !== null && !areas.apps;

  const load = React.useCallback(async () => {
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    try {
      const apps = await listApps();
      setState({ kind: "ready", apps, lastRead: new Date(), problem: null });
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      const message = err.unreachable
        ? "The server list could not be read because strazad did not answer. Check that it is running, then reload."
        : "The server list could not be read: " + err.message + ". Reload to try again.";
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
    }
  }, []);

  React.useEffect(() => { void load(); }, [load]);

  // Relative times move on their own; a re-render every 30 s keeps "2 m ago" honest.
  React.useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 30000);
    return () => clearInterval(t);
  }, []);

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <>
            <Button variant="ghost" size="sm" onClick={() => void load()} aria-label="Reload the list">
              <RefreshCwIcon /> Reload
            </Button>
            {!serverAdmin && (
              <Button size="sm" onClick={() => navigate("servers", ["new"])}>
                <PlusIcon /> Add MCP server
              </Button>
            )}
          </>
        }
      />
      <div className="px-6 py-5">
        {state.kind === "loading" && <p className="text-sm text-muted-foreground">Reading the server list.</p>}
        {state.kind === "error" && (
          <div className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-sm text-foreground" role="alert">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => void load()}>Reload now</Button>
          </div>
        )}
        {state.kind === "ready" && state.problem && (
          <div className="mb-3"><FetchError subject={route.label} detail={state.problem} lastRead={state.lastRead} /></div>
        )}
        {state.kind === "ready" && serverAdmin && (
          <p className="mb-3 text-[13px] text-muted-foreground" data-admin-line>
            {serverAdminLine(state.apps.length, state.apps.map((a) => a.admin_role || "").filter((r, i, xs) => r && xs.indexOf(r) === i))}
          </p>
        )}
        {state.kind === "ready" && <ServersTable rows={state.apps} onOpen={(row) => navigate("servers", [row.id])} />}
      </div>
    </>
  );
}
