import * as React from "react";
import { Columns3Icon, PlusIcon, RefreshCwIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { KindGlyph } from "@/components/role-kind";
import { type Chip, HIDEABLE, RolesTable, serverChips } from "@/components/roles-table";
import { type ApiError, type AppRow, type BindingRow, type PoliciesAnswer, type RoleRow, type ToolRow, exportRole, listApps, listBindings, listPolicies, listRoles, listTools, query } from "@/lib/api";
import { notify } from "@/lib/notify";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { type Kind, CATEGORY, COLUMN, COLUMNS, EXPORT_FAILED, KINDS, NEW_ROLE, READING_ROLES, RELOAD, RELOAD_LIST, RELOAD_NOW, ROW_HINT, SEARCH_ROLES_BY, SHOWN_COLUMNS, SUBJECT_POLICY_COUNTS, SUBJECT_REACH, SUBJECT_ROLES, countWords, isMinted, kindOf, notReadRefused, roleFileName, sideRefused } from "@/lib/role-words";
import { readFailed } from "@/lib/say";
import { downloadText } from "@/lib/utils";

// The Roles list: four tables, one per
// category, on one column skeleton, the Application table trading Reach for
// Server and Tools. The list reads once per open and on Reload, with no
// poll, and the search narrows the rows the browser already holds.

const route = routeByKey("roles");

type Ready = { kind: "ready"; roles: RoleRow[]; lastRead: Date; problem: string | null };
type State = { kind: "loading" } | { kind: "error"; message: string } | Ready;
// Side holds what the four reads beside the roles answered. A null value
// means that read failed, so its cells say so instead of reading as zero.
type Problem = { subject: string; detail: string; refused: boolean };
type Side = { chips: Record<string, Chip[] | null>; administers: Record<string, string[]> | null; facets: Record<string, number> | null; refused: { apps: boolean; policy: boolean }; problems: Problem[] };

const settled = <T,>(r: PromiseSettledResult<T>) => (r.status === "fulfilled" ? r.value : null);

// sideProblem words a failed side read, or "" for one that landed and for a
// 401, which hands the session back to the sign-in lane on its own. A 403
// is this session's standing, which a reload does not change, so it names
// the grant the read needs in the words the table's cells use.
function sideProblem(subject: string, grant: string, ...results: PromiseSettledResult<unknown>[]): Problem {
  for (const r of results) {
    if (r.status !== "rejected") continue;
    const err = r.reason as ApiError;
    if (err.status === 401) break;
    if (err.status === 403) return { subject, detail: notReadRefused(grant), refused: true };
    return { subject, detail: readFailed(subject, err), refused: false };
  }
  return { subject, detail: "", refused: false };
}

// administersOf names the servers each role administers, keyed by role id.
// It is read off the servers themselves, so a role that looks minted by
// name but no server names is read as the plain role it is.
function administersOf(apps: AppRow[]): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const a of apps) if (a.admin_role_id) out[a.admin_role_id] = (out[a.admin_role_id] || []).concat(a.name);
  return out;
}

export function Roles() {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [side, setSide] = React.useState<Side>({ chips: {}, administers: null, facets: null, refused: { apps: false, policy: false }, problems: [] });
  const [category, setCategory] = React.useState<Kind | "all">("all");
  const [needle, setNeedle] = React.useState("");
  const [hidden, setHidden] = React.useState<Record<string, boolean>>({});

  const load = React.useCallback(async () => {
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    const [rolesR, bindingsR, toolsR, appsR, policiesR] = await Promise.allSettled([
      listRoles(),
      listBindings(),
      listTools(),
      listApps(),
      listPolicies(query({ limit: 0 })),
    ]);

    const bindings = settled<BindingRow[]>(bindingsR);
    const tools = settled<ToolRow[]>(toolsR);
    const apps = settled<AppRow[]>(appsR);
    const answer = settled<PoliciesAnswer>(policiesR);
    const totals: Record<string, number> = {};
    for (const t of tools || []) totals[t.app] = (totals[t.app] || 0) + 1;
    const facets = answer ? Object.fromEntries((answer.roles || []).map((f) => [f.role, f.sets])) : null;
    const reach = sideProblem(SUBJECT_REACH, "apps:read", bindingsR, toolsR, appsR);
    const facetProblem = sideProblem(SUBJECT_POLICY_COUNTS, "policy:read", policiesR);

    if (rolesR.status === "rejected") {
      const err = rolesR.reason as ApiError;
      if (err.status === 401) return;
      const message = readFailed(SUBJECT_ROLES, err);
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      return;
    }
    const roles = rolesR.value || [];
    const chips = Object.fromEntries(roles.map((r) => [r.name, serverChips(r, bindings, totals)]));
    const problems = [reach, facetProblem].filter((p) => p.detail);
    setSide({ chips, administers: apps ? administersOf(apps) : null, facets, refused: { apps: reach.refused, policy: facetProblem.refused }, problems });
    setState({ kind: "ready", roles, lastRead: new Date(), problem: null });
  }, []);

  React.useEffect(() => { void load(); }, [load]);

  const shownRoles = React.useMemo(() => {
    if (state.kind !== "ready") return [];
    const q = needle.trim().toLowerCase();
    if (!q) return state.roles;
    return state.roles.filter((r) => r.name.toLowerCase().includes(q) || (r.description || "").toLowerCase().includes(q));
  }, [state, needle]);

  // The export is the document the server itself writes, saved under the
  // name strazactl gives it.
  const onExport = async (role: RoleRow) => {
    try {
      const text = await exportRole(role.id);
      downloadText(roleFileName(role.name), text, "application/yaml");
    } catch (e) {
      notify.failed(readFailed(EXPORT_FAILED, e as ApiError));
    }
  };

  const lastRead = state.kind === "ready" ? state.lastRead : null;
  const searching = needle.trim() !== "";

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <>
            <Button variant="ghost" size="sm" onClick={() => void load()} aria-label={RELOAD_LIST}>
              <RefreshCwIcon /> {RELOAD}
            </Button>
            <Button onClick={() => navigate("roles", ["new"])}>
              <PlusIcon /> {NEW_ROLE}
            </Button>
          </>
        }
      />
      <div className="flex flex-col gap-5 px-6 py-5">
        {state.kind === "loading" && <p className="text-sm text-muted-foreground">{READING_ROLES}</p>}
        {state.kind === "error" && (
          <div className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-sm text-foreground" role="alert">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => void load()}>{RELOAD_NOW}</Button>
          </div>
        )}
        {state.kind === "ready" && (
          <>
            {state.problem && <FetchError subject={route.label} detail={state.problem} lastRead={state.lastRead} />}
            {side.problems.map((p) => p.refused
              ? <p key={p.subject} role="status" className="border-l-[3px] border-border bg-card px-4 py-3 text-sm leading-relaxed text-text-2" data-side-refused><span className="font-semibold text-foreground">{sideRefused(p.subject)}</span>{" " + p.detail}</p>
              : <FetchError key={p.subject} subject={p.subject} detail={p.detail} lastRead={lastRead} />)}
            <div className="flex flex-wrap items-center gap-2">
              <Input
                value={needle}
                onChange={(e) => setNeedle(e.target.value)}
                placeholder={SEARCH_ROLES_BY}
                aria-label={SEARCH_ROLES_BY}
                className="h-9 w-full max-w-xs"
              />
              <span className="text-[13px] text-muted-foreground" data-row-count>{countWords(shownRoles.length)}</span>
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button variant="outline" size="sm" className="ml-auto h-9"><Columns3Icon /> {COLUMNS}</Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end">
                  <DropdownMenuLabel>{SHOWN_COLUMNS}</DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  {HIDEABLE.filter((id) => category !== "all" || ["reach", "holders", "policies"].includes(id)).map((id) => (
                    <DropdownMenuCheckboxItem key={id} checked={!hidden[id]} onCheckedChange={(v) => setHidden((h) => ({ ...h, [id]: !v }))}>
                      {COLUMN[id]}
                    </DropdownMenuCheckboxItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            </div>

            <div className="role-categories" role="group" aria-label="Role category">
              {(["all", ...KINDS] as const).map((kind) => <Button key={kind} type="button" variant="outline" size="sm" aria-pressed={category === kind} onClick={() => setCategory(kind)}>{kind === "all" ? "All roles" : CATEGORY[kind].title}<span>{kind === "all" ? shownRoles.length : shownRoles.filter((r) => kindOf(r) === kind).length}</span></Button>)}
            </div>
            {category === "all" && <section data-category="all"><RolesTable kind="all" rows={shownRoles} chips={side.chips} administers={side.administers} facets={side.facets} refused={side.refused} hidden={hidden} onOpen={(r) => navigate("roles", [r.id])} onExport={(r) => void onExport(r)} /></section>}
            {KINDS.filter((kind) => kind === category).map((kind) => {
              const all = shownRoles.filter((r) => kindOf(r) === kind);
              // Straza mints one role per MCP server, so a box with many
              // servers has many roles nobody holds yet. Those fold at the
              // end of the Straza table; a search shows them in the table
              // proper, since a search is the person asking for them.
              const folding = kind === "straza" && !searching;
              const held = (r: RoleRow) => !isMinted(r) || (r.holder_count || 0) > 0;
              const rows = folding ? all.filter(held) : all;
              const folded = folding ? all.filter((r) => !held(r)) : [];
              return (
                <section key={kind} className="flex flex-col gap-2" data-category={kind}>
                  <div className="flex flex-wrap items-baseline gap-2">
                    <div className="flex items-center gap-2">
                      <KindGlyph mark={kind} />
                      <h2 className="text-[15px] font-semibold text-foreground">{CATEGORY[kind].title}</h2>
                    </div>
                    <span className="font-mono text-[13px] text-muted-foreground">{all.length}</span>
                    <span className="text-[13px] text-muted-foreground">{CATEGORY[kind].line}</span>
                  </div>
                  {all.length === 0 ? (
                    <p className="rounded-md border border-border bg-card px-4 py-3 text-sm text-muted-foreground" data-category-empty={kind}>
                      {searching ? CATEGORY[kind].noMatch : CATEGORY[kind].none}
                    </p>
                  ) : (
                    <RolesTable
                      kind={kind}
                      rows={rows}
                      folded={folded}
                      chips={side.chips}
                      administers={side.administers}
                      facets={side.facets}
                      refused={side.refused}
                      hidden={hidden}
                      onOpen={(r) => navigate("roles", [r.id])}
                      onExport={(r) => void onExport(r)}
                    />
                  )}
                </section>
              );
            })}
            <p className="text-[13px] text-muted-foreground">{ROW_HINT}</p>
          </>
        )}
      </div>
    </>
  );
}
