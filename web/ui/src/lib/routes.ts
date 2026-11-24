// The route table of the console: the eleven areas in sidebar order. Each
// area names the admin area its screen reads (the server's adminRouteArea
// vocabulary) and whether its screen is built. A delegated admin only sees
// the areas their grants cover; the display is a courtesy, the server
// enforces every request.
import {
  CircleCheckIcon,
  FileCheckIcon,
  FilePenIcon,
  GaugeIcon,
  type LucideIcon,
  MessagesSquareIcon,
  ScrollTextIcon,
  ServerIcon,
  SettingsIcon,
  ShieldIcon,
  SquareTerminalIcon,
  UsersIcon,
} from "lucide-react";

export type RouteKey = "overview" | "sessions" | "audit" | "transcripts" | "users" | "roles" | "servers" | "policies" | "approvals" | "drafts" | "settings";

export type Route = {
  key: RouteKey;
  label: string;
  icon: LucideIcon;
  path: string;
  areas: string[];
  built: boolean;
  description: string;
};

export const ROUTES: Route[] = [
  { key: "overview", label: "Overview", icon: GaugeIcon, path: "overview", areas: ["config"], built: true, description: "Deployment health, access and recent activity." },
  { key: "sessions", label: "Sessions", icon: SquareTerminalIcon, path: "sessions", areas: ["sessions"], built: true, description: "" },
  { key: "audit", label: "Audit", icon: ScrollTextIcon, path: "audit", areas: ["audit"], built: true, description: "Every recorded decision and change." },
  { key: "transcripts", label: "Transcripts", icon: MessagesSquareIcon, path: "transcripts", areas: ["transcripts"], built: true, description: "Recorded prompts and replies." },
  { key: "users", label: "Users", icon: UsersIcon, path: "users", areas: ["identity"], built: true, description: "" },
  { key: "roles", label: "Roles", icon: ShieldIcon, path: "roles", areas: ["identity"], built: true, description: "" },
  { key: "servers", label: "MCP servers", icon: ServerIcon, path: "servers", areas: ["apps"], built: true, description: "" },
  { key: "policies", label: "Policies", icon: FileCheckIcon, path: "policies", areas: ["policy"], built: true, description: "The policy rules that decide every call: what is denied, what needs approval and what is allowed." },
  { key: "approvals", label: "Approvals", icon: CircleCheckIcon, path: "approvals", areas: ["approvals"], built: true, description: "" },
  // Drafts opens to the grants that may draft: the drafts area, and the
  // areas whose objects a draft changes.
  { key: "drafts", label: "Drafts", icon: FilePenIcon, path: "drafts", areas: ["drafts", "apps", "identity", "policy"], built: true, description: "Changes to servers, roles, access and policies wait here until a person publishes them." },
  { key: "settings", label: "Settings", icon: SettingsIcon, path: "settings", areas: ["config", "tokens"], built: true, description: "" },
];

// landing is the first built area in table order, the page the base path
// redirects to.
export const landing: Route = ROUTES.find((r) => r.built) as Route;

// routeByKey returns the table row for a key.
export function routeByKey(key: RouteKey): Route {
  return ROUTES.find((r) => r.key === key) as Route;
}

// routeByPath returns the table row whose path segment matches, or null.
export function routeByPath(segment: string): Route | null {
  return ROUTES.find((r) => r.path === segment) || null;
}

// visibleRoutes filters the table by the session's admin areas: null is
// the root admin and sees all, an object keyed by area name sees the rows
// one of its areas covers. servers is how many MCP servers the session
// administers: holding a server's admin role opens the MCP servers area and
// the Drafts it may write for them, and no other, and the server answers
// both lists with those servers alone.
export function visibleRoutes(areas: Record<string, boolean> | null, servers = 0): Route[] {
  if (!areas) return ROUTES;
  return ROUTES.filter((r) => r.areas.some((a) => areas[a]) || ((r.key === "servers" || r.key === "drafts") && servers > 0));
}

// serverBadge is the number the rail shows beside MCP servers: the servers
// this session administers, and none when an apps grant already opens every
// server, where a count of a few would read as a limit that is not there.
export function serverBadge(areas: Record<string, boolean> | null, servers: number): number {
  return areas && !areas.apps ? servers : 0;
}

// serversBadgeTitle is the hover of that number.
export const serversBadgeTitle = (n: number) => "This account administers " + (n === 1 ? "1 MCP server" : n + " MCP servers") + ".";

// draftsBadgeTitle is the hover of the number beside Drafts: the open
// drafts this session may read, "200+" past one page.
export const draftsBadgeTitle = (label: string) => label + (label === "1" ? " draft waits" : " drafts wait") + " for review.";

// approvalsBadgeTitle is the hover of the number beside Approvals: the
// requests that wait.
export const approvalsBadgeTitle = (n: number) => (n === 1 ? "1 request waits" : n + " requests wait") + " for a decision.";

export const NAV_GROUPS: { label: string; keys: RouteKey[] }[] = [
  { label: "Workspace", keys: ["overview"] },
  { label: "Activity", keys: ["sessions", "audit", "transcripts"] },
  { label: "Access & governance", keys: ["users", "roles", "servers", "policies"] },
  { label: "Decisions", keys: ["approvals", "drafts"] },
  { label: "Administration", keys: ["settings"] },
];
export function groupedRoutes(routes: Route[]) {
  return NAV_GROUPS.map((group) => ({ ...group, routes: routes.filter((r) => group.keys.includes(r.key)) })).filter((group) => group.routes.length);
}
