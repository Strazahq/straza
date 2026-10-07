import * as React from "react";
import { Columns3Icon, PlayIcon, PlusIcon } from "lucide-react";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { type PolicyDoor, PoliciesByRole, openNewPolicy } from "@/components/policies-by-role";
import { HIDEABLE, PoliciesTable } from "@/components/policies-table";
import { PolicyTest } from "@/components/policy-test";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { type ApiError, type BindingRow, type PoliciesAnswer, type PolicySetRow, type RoleRow, listBindings, listPolicies, listRoles, query } from "@/lib/api";
import { AREA_DESC, CATEGORY, COLUMN, COLUMNS, DRAFT, EDITED, EVERYONE, FILTER, LANE_FILTER, LIST_FOOT, LIVE, NEW_POLICY, NO_MATCH, OUTSIDE_FOOT, READING, RELOAD_NOW, SEARCH, SHOWN_COLUMNS, SUBJECT_POLICIES, SUBJECT_REACH, SUBJECT_ROLES, TEST_A_CALL, VIEW, VIEW_LABEL, countWords, roleCountWords } from "@/lib/policy-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// The Policies list: the policies that
// apply to everyone, then the ones that name a role, on one column
// skeleton, with By role as a second view of the same reads. The list is
// read whole once per open, and every filter narrows the rows the browser
// already holds.

const route = routeByKey("policies");

type Ready = { kind: "ready"; rows: PolicySetRow[]; lastRead: Date; problem: string | null };
type State = { kind: "loading" } | { kind: "error"; message: string } | Ready;
// Side holds the two reads beside the policies: the application roles the
// Applies to filter offers, and what each role reaches for the By role
// hover. A null list means that read failed, so the view says so.
type Side = { roles: RoleRow[] | null; bindings: BindingRow[] | null; problems: { subject: string; detail: string }[] };

const settled = <T,>(r: PromiseSettledResult<T>) => (r.status === "fulfilled" ? r.value : null);

// sideProblem words a failed side read, and says nothing for a 401, which
// hands the session back to the sign-in lane on its own.
function sideProblem(subject: string, r: PromiseSettledResult<unknown>): string {
  if (r.status !== "rejected") return "";
  const err = r.reason as ApiError;
  return err.status === 401 ? "" : readFailed(subject, err);
}

// The three categories, in the order they are read down the page. A policy
// with no role and no other selector applies to everyone; one that names a
// role sits under For roles, whatever else it names; one scoped by user or
// identity alone sits under Outside roles.
type Category = "everyone" | "roles" | "outside";

const CATEGORIES: { key: Category; title: string; line: string; none: string; door: string; prefill: PolicyDoor | null }[] = [
  { key: "everyone", ...CATEGORY.everyone, prefill: { role: null } },
  { key: "roles", ...CATEGORY.roles, prefill: {} },
  { key: "outside", ...CATEGORY.outside, door: "", prefill: null },
];

function categoryOf(row: PolicySetRow): Category {
  const s = row.summary;
  if (s && s.matchRoles && s.matchRoles.length) return "roles";
  if (s && s.matchOther) return "outside";
  return "everyone";
}

export function Policies({ view = "sets" }: { view?: "sets" | "roles" }) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [side, setSide] = React.useState<Side>({ roles: null, bindings: null, problems: [] });
  const [needle, setNeedle] = React.useState("");
  const [status, setStatus] = React.useState("");
  const [applies, setApplies] = React.useState("");
  const [lane, setLane] = React.useState("");
  const [hidden, setHidden] = React.useState<Record<string, boolean>>({});
  const [testing, setTesting] = React.useState(false);

  const load = React.useCallback(async () => {
    setState((s) => (s.kind === "ready" ? s : { kind: "loading" }));
    const [policiesR, rolesR, bindingsR] = await Promise.allSettled([listPolicies(query({ limit: 0 })), listRoles(), listBindings()]);

    const answer = settled<PoliciesAnswer>(policiesR);
    const roles = settled<RoleRow[]>(rolesR);
    const problems = [
      { subject: SUBJECT_ROLES, detail: sideProblem(SUBJECT_ROLES, rolesR) },
      { subject: SUBJECT_REACH, detail: sideProblem(SUBJECT_REACH, bindingsR) },
    ].filter((p) => p.detail);
    setSide({
      roles: roles ? roles.filter((r) => r.kind === "application").sort((a, b) => a.name.localeCompare(b.name)) : null,
      bindings: settled<BindingRow[]>(bindingsR),
      problems,
    });

    if (!answer) {
      const err = (policiesR as PromiseRejectedResult).reason as ApiError;
      if (err.status === 401) return;
      const message = readFailed(SUBJECT_POLICIES, err);
      setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      return;
    }
    setState({ kind: "ready", rows: answer.items || [], lastRead: new Date(), problem: null });
  }, []);

  React.useEffect(() => { void load(); }, [load]);

  const rows = state.kind === "ready" ? state.rows : [];

  // Every filter narrows the rows in the browser: the list arrives whole,
  // so nothing here asks the server again.
  const shown = React.useMemo(() => {
    const q = needle.trim().toLowerCase();
    return rows
      .filter((row) => {
        const s = row.summary;
        const roles = (s && s.matchRoles) || [];
        if (q && !(row.name.toLowerCase().includes(q) || ((s && s.description) || "").toLowerCase().includes(q))) return false;
        if (status === "active" && !(row.status === "active" && !row.drift)) return false;
        if (status === "draft" && row.status === "active") return false;
        if (status === "edited" && !row.drift) return false;
        if (applies === "everyone" && (roles.length > 0 || (s && s.matchOther))) return false;
        if (applies === "outside" && !(s && s.matchOther)) return false;
        if (applies.startsWith("role:") && !roles.includes(applies.slice(5))) return false;
        if (lane && !(s && s.lanes && s.lanes[lane])) return false;
        return true;
      })
      .sort((a, b) => a.name.localeCompare(b.name));
  }, [rows, needle, status, applies, lane]);

  // By role lists the application roles, narrowed by the same search.
  const shownRoles = React.useMemo(() => {
    const q = needle.trim().toLowerCase();
    const all = side.roles || [];
    if (!q) return all;
    return all.filter((r) => r.name.toLowerCase().includes(q) || (r.description || "").toLowerCase().includes(q));
  }, [side.roles, needle]);

  const sets = view === "sets";
  const lastRead = state.kind === "ready" ? state.lastRead : null;
  const hasOutside = rows.some((r) => categoryOf(r) === "outside");

  const viewButton = (key: "sets" | "roles", label: string, rest: string[]) => (
    <Button
      variant="outline"
      size="sm"
      aria-pressed={view === key}
      onClick={() => navigate("policies", rest)}
      className={cn("-ml-px h-9 rounded-none first:ml-0 first:rounded-l-md last:rounded-r-md", view === key && "bg-accent-bg text-foreground")}
    >
      {label}
    </Button>
  );

  const picker = (label: string, value: string, onChange: (v: string) => void, options: [string, string][]) => (
    <Select value={value || "any"} onValueChange={onChange}>
      <SelectTrigger aria-label={label} className="h-9 gap-1.5">
        <span className="text-muted-foreground">{label}</span>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map(([v, text]) => <SelectItem key={v || "any"} value={v || "any"}>{text}</SelectItem>)}
      </SelectContent>
    </Select>
  );

  const pick = (set: (v: string) => void) => (v: string) => set(v === "any" ? "" : v);

  return (
    <>
      <PageHead
        label={route.label}
        description={AREA_DESC}
        actions={
          <>
            <Button variant="outline" onClick={() => setTesting(true)}>
              <PlayIcon /> {TEST_A_CALL}
            </Button>
            <Button onClick={() => navigate("policies", ["new"])}>
              <PlusIcon /> {NEW_POLICY}
            </Button>
          </>
        }
      />
      <div className="flex flex-col gap-5 px-6 py-5">
        {state.kind === "loading" && <p className="text-sm text-muted-foreground">{READING}</p>}
        {state.kind === "error" && (
          <div className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-sm text-foreground" role="alert">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => void load()}>{RELOAD_NOW}</Button>
          </div>
        )}
        {state.kind === "ready" && (
          <>
            {state.problem && <FetchError subject={SUBJECT_POLICIES} detail={state.problem} lastRead={state.lastRead} />}
            {side.problems
              .filter((p) => p.subject !== SUBJECT_REACH || !sets)
              .map((p) => <FetchError key={p.subject} subject={p.subject} detail={p.detail} lastRead={lastRead} />)}

            <div className="flex flex-wrap items-center gap-2">
              <Input
                value={needle}
                onChange={(e) => setNeedle(e.target.value)}
                placeholder={SEARCH}
                aria-label={SEARCH}
                className="h-9 w-full max-w-xs"
              />
              {sets && picker(FILTER.status, status, pick(setStatus), [["", FILTER.any], ["active", LIVE], ["draft", DRAFT], ["edited", EDITED]])}
              {sets && picker(FILTER.applies, applies, pick(setApplies), [
                ["", FILTER.anyRole],
                ["everyone", EVERYONE],
                ...(side.roles || []).map((r) => ["role:" + r.name, r.name] as [string, string]),
                ["outside", FILTER.outside],
              ])}
              {picker(FILTER.lane, lane, pick(setLane), [["", FILTER.any], ...Object.entries(LANE_FILTER)])}
              <div role="group" aria-label={VIEW_LABEL} className="flex items-center">
                {viewButton("sets", VIEW.sets, [])}
                {viewButton("roles", VIEW.roles, ["by-role"])}
              </div>
              <span className="ml-auto text-[13px] text-muted-foreground" data-row-count>
                {sets ? countWords(shown.length, rows.length) : roleCountWords(shownRoles.length)}
              </span>
              {sets && (
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="outline" size="sm" className="h-9"><Columns3Icon /> {COLUMNS}</Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuLabel>{SHOWN_COLUMNS}</DropdownMenuLabel>
                    <DropdownMenuSeparator />
                    {HIDEABLE.map((id) => (
                      <DropdownMenuCheckboxItem key={id} checked={!hidden[id]} onCheckedChange={(v) => setHidden((h) => ({ ...h, [id]: !v }))}>
                        {COLUMN[id]}
                      </DropdownMenuCheckboxItem>
                    ))}
                  </DropdownMenuContent>
                </DropdownMenu>
              )}
            </div>

            {sets ? (
              <>
                {CATEGORIES.filter((c) => c.key !== "outside" || hasOutside).map((c) => {
                  const here = shown.filter((r) => categoryOf(r) === c.key);
                  const any = rows.some((r) => categoryOf(r) === c.key);
                  return (
                    <section key={c.key} className="flex flex-col gap-2" data-category={c.key}>
                      <div className="flex flex-wrap items-baseline gap-2">
                        <h2 className="text-[15px] font-semibold text-foreground">{c.title}</h2>
                        <span className="font-mono text-[13px] text-muted-foreground" data-category-count={c.key}>{here.length}</span>
                        <span className="text-[13px] text-muted-foreground">{c.line}</span>
                      </div>
                      {here.length > 0 && <PoliciesTable rows={here} hidden={hidden} onOpen={(row) => navigate("policies", [row.name])} />}
                      {here.length === 0 && (
                        <p className="rounded-md border border-border bg-card px-4 py-3 text-sm text-muted-foreground" data-category-empty={c.key}>
                          {any ? NO_MATCH : c.none}{" "}
                          {!any && c.prefill && (
                            <button
                              type="button"
                              className="rounded-md border border-dashed border-border px-1.5 py-px text-[13px] text-link hover:border-link hover:text-foreground"
                              onClick={() => openNewPolicy(c.prefill as PolicyDoor)}
                            >
                              {c.door}
                            </button>
                          )}
                        </p>
                      )}
                    </section>
                  );
                })}
                <p className="text-[13px] text-muted-foreground">{LIST_FOOT}{hasOutside ? "" : " " + OUTSIDE_FOOT}</p>
              </>
            ) : (
              <PoliciesByRole
                rows={rows}
                roles={shownRoles}
                bindings={side.bindings}
                lane={lane}
                onOpenPolicy={(name) => navigate("policies", [name])}
              />
            )}
          </>
        )}
      </div>
      <PolicyTest open={testing} onOpenChange={setTesting} />
    </>
  );
}
