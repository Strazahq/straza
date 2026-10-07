import * as React from "react";
import { FileCheckIcon, PlusIcon, ServerIcon, UserIcon } from "lucide-react";
import { CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandShortcut } from "@/components/ui/command";
import { KindGlyph, markOf } from "@/components/role-kind";
import { StatusBadge } from "@/components/status-badge";
import { type ApiError, type AppRow, type PolicySetRow, type RoleRow, type UserRow, listApps, listPolicies, listRoles, listUsers, query } from "@/lib/api";
import { holders } from "@/lib/words";
import { type Route } from "@/lib/routes";
import { navigate } from "@/lib/router";
import { adminAreas } from "@/lib/session";
import { NEW_POLICY, PALETTE_GROUP, paletteLine } from "@/lib/policy-words";
import { TABS } from "@/lib/settings-words";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  routes: Route[];
  onOpenServer: (app: AppRow) => void;
  onOpenUser: (user: UserRow) => void;
  onOpenRole: (role: RoleRow) => void;
};

type Servers = { kind: "idle" } | { kind: "loading" } | { kind: "ready"; apps: AppRow[] } | { kind: "error"; message: string };
type Roles = { kind: "idle" } | { kind: "loading" } | { kind: "ready"; roles: RoleRow[] } | { kind: "error"; message: string };
type Users = { kind: "idle" } | { kind: "loading" } | { kind: "ready"; rows: UserRow[] } | { kind: "error"; message: string };
type Policies = { kind: "idle" } | { kind: "loading" } | { kind: "ready"; rows: PolicySetRow[] } | { kind: "error"; message: string };

// matches tells whether every typed word is the start of some word of the
// name, split on spaces and hyphens, case-insensitive. It never matches
// letters in order: "demo" finds demo-tools and not midpoint, "mid" finds
// midpoint, and "run" finds nothing because runtime and status are not
// searched.
export function matches(query: string, name: string): boolean {
  const q = query.toLowerCase().split(/[\s-]+/).filter(Boolean);
  if (q.length === 0) return true;
  const words = name.toLowerCase().split(/[\s-]+/).filter(Boolean);
  return q.every((part) => words.some((w) => w.startsWith(part)));
}

const SHORTCUTS: { label: string; keys: string }[] = [
  { label: "Open this palette", keys: "Ctrl K" },
  { label: "Close the innermost overlay", keys: "Esc" },
  { label: "Move and open", keys: "Arrows, Enter" },
];

// Palette is the Ctrl+K switcher: pages the person can see, the MCP servers
// and the roles read once per session, the users the server search finds
// for what is typed, and the keyboard shortcuts while nothing is typed.
// Enter on a page navigates; Enter on a server, a role or a user opens it.
export function Palette({ open, onOpenChange, routes, onOpenServer, onOpenUser, onOpenRole }: Props) {
  const [typedQuery, setQuery] = React.useState("");
  const [servers, setServers] = React.useState<Servers>({ kind: "idle" });
  const [roleList, setRoleList] = React.useState<Roles>({ kind: "idle" });
  const [users, setUsers] = React.useState<Users>({ kind: "idle" });
  const [policyList, setPolicyList] = React.useState<Policies>({ kind: "idle" });
  const userSeq = React.useRef(0);

  React.useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        onOpenChange(!open);
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [open, onOpenChange]);

  // The server list is read the first time the palette opens and kept for
  // the session; a failed read is retried at the next open.
  React.useEffect(() => {
    if (!open) { setQuery(""); setUsers({ kind: "idle" }); return; }
    if (servers.kind !== "idle" && servers.kind !== "error") return;
    let live = true;
    setServers({ kind: "loading" });
    listApps().then(
      (apps) => { if (live) setServers({ kind: "ready", apps: [...apps].sort((a, b) => a.name.localeCompare(b.name)) }); },
      (e: ApiError) => { if (live) setServers({ kind: "error", message: e.unreachable ? "The server list could not be read because strazad did not answer." : "The server list could not be read: " + e.message + "." }); },
    );
    return () => { live = false; };
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  // The role catalog is small and read the same way as the servers.
  React.useEffect(() => {
    if (!open) return;
    if (roleList.kind !== "idle" && roleList.kind !== "error") return;
    let live = true;
    setRoleList({ kind: "loading" });
    listRoles().then(
      (rows) => { if (live) setRoleList({ kind: "ready", roles: [...rows].sort((a, b) => a.name.localeCompare(b.name)) }); },
      (e: ApiError) => { if (live) setRoleList({ kind: "error", message: e.unreachable ? "The role list could not be read because strazad did not answer." : "The role list could not be read: " + e.message + "." }); },
    );
    return () => { live = false; };
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  // The policy list is read whole the same way, so the palette opens a
  // policy by name without a walk through the list.
  React.useEffect(() => {
    if (!open) return;
    if (policyList.kind !== "idle" && policyList.kind !== "error") return;
    let live = true;
    setPolicyList({ kind: "loading" });
    listPolicies(query({ limit: 0 })).then(
      (answer) => { if (live) setPolicyList({ kind: "ready", rows: [...(answer.items || [])].sort((a, b) => a.name.localeCompare(b.name)) }); },
      (e: ApiError) => { if (live) setPolicyList({ kind: "error", message: e.unreachable ? "The policy list could not be read because strazad did not answer." : "The policy list could not be read: " + e.message + "." }); },
    );
    return () => { live = false; };
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  // Users are searched on the server for what is typed, since the
  // directory is never held whole in the browser.
  React.useEffect(() => {
    const needle = typedQuery.trim();
    const my = ++userSeq.current;
    if (!open || !needle) { setUsers({ kind: "idle" }); return; }
    setUsers({ kind: "loading" });
    const t = setTimeout(() => {
      listUsers(query({ q: needle, limit: 8, sort: "name", order: "asc" })).then(
        (page) => { if (my === userSeq.current) setUsers({ kind: "ready", rows: page.items || [] }); },
        (e: ApiError) => { if (my === userSeq.current) setUsers({ kind: "error", message: e.unreachable ? "The directory could not be searched because strazad did not answer." : "The directory could not be searched: " + e.message + "." }); },
      );
    }, 250);
    return () => clearTimeout(t);
  }, [typedQuery, open]);

  const pages = routes.filter((r) => matches(typedQuery, r.label));
  // The Settings tabs are pages of their own, so the palette
  // finds "Attestation registry" without a walk through Settings. A
  // delegated seat is offered only the tabs its grants cover.
  const settings = routes.find((r) => r.key === "settings");
  const areas = adminAreas();
  const subPages = settings
    ? TABS.filter((t) => (!areas || areas[t.area]) && matches(typedQuery, settings.label + " " + t.label)).map((t) => ({
      key: t.key,
      label: settings.label + " › " + t.label,
      address: settings.path + "/" + t.key,
    }))
    : [];
  // New policy is a page of its own, at policies/new, listed the way the
  // Settings tabs are.
  const policiesRoute = routes.find((r) => r.key === "policies");
  const newPolicy = policiesRoute && matches(typedQuery, policiesRoute.label + " " + NEW_POLICY) ? policiesRoute : null;
  const apps = servers.kind === "ready" ? servers.apps.filter((a) => matches(typedQuery, a.name)) : [];
  const policies = policyList.kind === "ready" ? policyList.rows.filter((p) => matches(typedQuery, p.name)) : [];
  const roles = roleList.kind === "ready" ? roleList.roles.filter((r) => matches(typedQuery, r.name)) : [];
  // A role wears the same mark as on the Roles list: the servers that name
  // it as their admin role, read off the server list the palette already
  // holds, and never off the role's name.
  const administers = (role: RoleRow) => (servers.kind === "ready" ? servers.apps.filter((a) => a.admin_role_id === role.id).map((a) => a.name) : null);
  const typed = typedQuery.trim().length > 0;

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} title="Find a page, server, user, role or policy" description="Type the start of a page, server, user, role or policy name, then press Enter to open it." shouldFilter={false}>
      <CommandInput placeholder="Find a page, server, user, role or policy" value={typedQuery} onValueChange={setQuery} />
      <CommandList>
        <CommandEmpty>Nothing matches what you typed.</CommandEmpty>
        {(pages.length > 0 || subPages.length > 0 || newPolicy) && (
          <CommandGroup heading="Pages">
            {pages.map((r) => (
              <CommandItem key={r.key} value={"page:" + r.key} onSelect={() => { onOpenChange(false); navigate(r.key); }}>
                <r.icon />
                {r.label}
                {!r.built && <span className="text-muted-foreground">not built yet</span>}
              </CommandItem>
            ))}
            {settings && subPages.map((p) => (
              <CommandItem key={"settings:" + p.key} value={"page:settings:" + p.key} onSelect={() => { onOpenChange(false); navigate("settings", [p.key]); }}>
                <settings.icon />
                {p.label}
                <span className="font-mono text-muted-foreground">{p.address}</span>
              </CommandItem>
            ))}
            {newPolicy && (
              <CommandItem value="page:policies:new" onSelect={() => { onOpenChange(false); navigate("policies", ["new"]); }}>
                <PlusIcon />
                {newPolicy.label + " › " + NEW_POLICY}
                <span className="font-mono text-muted-foreground">{newPolicy.path + "/new"}</span>
              </CommandItem>
            )}
          </CommandGroup>
        )}
        {servers.kind === "loading" && (
          <CommandGroup heading="MCP servers">
            <CommandItem value="servers:loading" disabled>Reading the server list.</CommandItem>
          </CommandGroup>
        )}
        {servers.kind === "error" && (
          <CommandGroup heading="MCP servers">
            <CommandItem value="servers:error" disabled>{servers.message}</CommandItem>
          </CommandGroup>
        )}
        {apps.length > 0 && (
          <CommandGroup heading="MCP servers">
            {apps.map((a) => (
              <CommandItem key={a.id} value={"server:" + a.id} onSelect={() => { onOpenChange(false); onOpenServer(a); }}>
                <ServerIcon />
                <span className="font-mono">{a.name}</span>
                <span className="text-muted-foreground">{a.runtime}</span>
                <StatusBadge status={a.status} className="ml-auto" />
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {roleList.kind === "error" && (
          <CommandGroup heading="Roles">
            <CommandItem value="roles:error" disabled>{roleList.message}</CommandItem>
          </CommandGroup>
        )}
        {roles.length > 0 && (
          <CommandGroup heading="Roles">
            {roles.map((r) => (
              <CommandItem key={r.id} value={"role:" + r.id} onSelect={() => { onOpenChange(false); onOpenRole(r); }}>
                <KindGlyph mark={markOf(r, administers(r))} />
                <span className="font-mono">{r.name}</span>
                <span className="text-muted-foreground">{r.kind}</span>
                <span className="ml-auto text-muted-foreground">{holders(r.holder_count || 0)}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {policyList.kind === "error" && (
          <CommandGroup heading={PALETTE_GROUP}>
            <CommandItem value="policies:error" disabled>{policyList.message}</CommandItem>
          </CommandGroup>
        )}
        {policies.length > 0 && (
          <CommandGroup heading={PALETTE_GROUP}>
            {policies.map((p) => (
              <CommandItem key={p.name} value={"policy:" + p.name} onSelect={() => { onOpenChange(false); navigate("policies", [p.name]); }}>
                <FileCheckIcon />
                <span className="font-mono">{p.name}</span>
                <span className="ml-auto text-muted-foreground">{paletteLine(p.status, (p.summary && p.summary.matchRoles) || [])}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {users.kind === "error" && (
          <CommandGroup heading="Users">
            <CommandItem value="users:error" disabled>{users.message}</CommandItem>
          </CommandGroup>
        )}
        {users.kind === "ready" && users.rows.length > 0 && (
          <CommandGroup heading="Users">
            {users.rows.map((u) => (
              <CommandItem key={u.id} value={"user:" + u.id} onSelect={() => { onOpenChange(false); onOpenUser(u); }}>
                <UserIcon />
                <span className="font-mono">{u.username}</span>
                {u.display && <span className="text-muted-foreground">{u.display}</span>}
              </CommandItem>
            ))}
          </CommandGroup>
        )}
        {!typed && (
          <CommandGroup heading="Shortcuts">
            {SHORTCUTS.map((s) => (
              <CommandItem key={s.label} value={"shortcut:" + s.label} onSelect={() => onOpenChange(false)}>
                {s.label}
                <CommandShortcut className="font-mono tracking-normal">{s.keys}</CommandShortcut>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
      </CommandList>
      <div className="border-t border-border px-3 py-2 text-[13px] text-muted-foreground">Matches the start of a word in a page, server, role or policy name; users are searched on the server</div>
    </CommandDialog>
  );
}
