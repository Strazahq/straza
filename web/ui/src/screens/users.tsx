import * as React from "react";
import { Loader2Icon, RefreshCwIcon, SearchIcon, XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DataTable } from "@/components/data-table";
import { FetchError } from "@/components/error-state";
import { KIND_ICON } from "@/components/kind-icon";
import { PageHead } from "@/components/page-head";
import { Segmented } from "@/components/segmented";
import { USER_WIDTHS, userColumns, userMinWidth } from "@/components/users-table";
import { type KindCounts, type RoleRow, type UserRow, listRoles, listUsers } from "@/lib/api";
import { take } from "@/lib/handoff";
import { type Sorting, usePagedList } from "@/lib/paged";
import { LOAD_MORE, RELOAD, RELOAD_LIST, RELOAD_NOW } from "@/lib/role-words";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { ANY_ROLE, CLEAR_SPONSOR, KIND_TAB, KIND_TABS_LABEL, ROLE_LABEL, SEARCH_LABEL, SEARCH_PLACEHOLDER, STATUS_LABEL, STATUS_OPTION, USER_LABELS, countLine, emptyKind } from "@/lib/user-list-words";
import type { UserKind } from "@/lib/user-look";
import { EMPTY_DIRECTORY, EMPTY_FILTERED, READING_LIST, sponsorFilterWords } from "@/lib/user-words";

// The Users list: one row per identity Straza knows, every filter, the kind
// tabs and the sort answered by the server, and a row opening the user's
// page. There is no Add user: identities arrive over SCIM or through
// strazactl.

const route = routeByKey("users");
const ALL = "all";
const ANY = "any";
const DEBOUNCE_MS = 300;
const KINDS: UserKind[] = ["person", "agent", "service"];


export function Users() {
  const [typed, setTyped] = React.useState("");
  const [q, setQ] = React.useState("");
  const [status, setStatus] = React.useState(ALL);
  const [role, setRole] = React.useState(ANY);
  const [kind, setKind] = React.useState<"all" | UserKind>("all");
  // The hand-off box carries a sponsor to filter by, from the Agents tab of
  // a person's page.
  const [sponsor, setSponsor] = React.useState(() => take<{ sponsor?: string }>("users")?.sponsor || "");
  const [sorting, setSorting] = React.useState<Sorting>({ key: "name", order: "asc" });
  const [roles, setRoles] = React.useState<RoleRow[]>([]);

  // The search box asks the server, so it waits for the typing to settle.
  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  // The role catalog is read once: it names the Role filter's options. A
  // failed read leaves the filter with any role alone.
  React.useEffect(() => {
    let alive = true;
    listRoles().then((rs) => { if (alive) setRoles(rs || []); }, () => {});
    return () => { alive = false; };
  }, []);

  const params = React.useMemo(
    () => ({ q, status: status === ALL ? "" : status, role: role === ANY ? "" : role, sponsor, kind: kind === "all" ? "" : kind }),
    [q, status, role, sponsor, kind],
  );
  // read keeps the kind counts of the newest answer, as the paged list
  // keeps the rows of its newest read and drops an older one.
  const [counts, setCounts] = React.useState<KindCounts | undefined>(undefined);
  const asked = React.useRef(0);
  const read = React.useCallback((qs: string) => {
    const my = ++asked.current;
    return listUsers(qs).then((page) => {
      if (my === asked.current) setCounts(page.kind_counts);
      return page;
    });
  }, []);
  const { state, busy, reload, loadMore } = usePagedList<UserRow>({ read, subject: "The user list", params, sorting, limit: 100, pollMs: 60000 });
  const rows = state.kind === "ready" ? state.rows : [];
  const more = state.kind === "ready" && state.more;

  const columns = React.useMemo(() => userColumns((u) => navigate("users", [u.id, "agents"])), []);
  const filtered = !!(q || sponsor || status !== ALL || role !== ANY);
  // A tab counts what the server counts for its kind, and All their sum.
  const count = (k: "all" | UserKind) => (counts ? (k === "all" ? counts.person + counts.agent + counts.service : counts[k]) : undefined);
  const tabs = [
    { value: "all" as const, label: KIND_TAB.all, count: count("all"), "data-kind-tab": "all" },
    ...KINDS.map((k) => ({ value: k, label: KIND_TAB[k], icon: KIND_ICON[k], count: count(k), "data-kind-tab": k })),
  ];

  const filterBar = (
    <>
      <div className="relative w-full max-w-xs">
        <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
        <Input
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder={SEARCH_PLACEHOLDER}
          aria-label={SEARCH_LABEL}
          className="h-9 pl-8"
        />
      </div>
      <Select value={status} onValueChange={setStatus}>
        <SelectTrigger aria-label={STATUS_LABEL} className="h-9 w-[150px]" data-status-filter>
          <span className="text-muted-foreground">{STATUS_LABEL}</span>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{STATUS_OPTION.all}</SelectItem>
          <SelectItem value="active">{STATUS_OPTION.active}</SelectItem>
          <SelectItem value="disabled">{STATUS_OPTION.disabled}</SelectItem>
        </SelectContent>
      </Select>
      <Select value={role} onValueChange={setRole}>
        <SelectTrigger aria-label={ROLE_LABEL} className="h-9 w-[190px]" data-role-filter>
          <span className="text-muted-foreground">{ROLE_LABEL}</span>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>{ANY_ROLE}</SelectItem>
          {roles.map((r) => <SelectItem key={r.id} value={r.name} className="font-mono">{r.name}</SelectItem>)}
        </SelectContent>
      </Select>
      {sponsor && (
        <span className="inline-flex h-9 items-center gap-1.5 rounded-md border border-border bg-background pl-2.5 pr-1 text-dense" data-sponsor-chip={sponsor}>
          {sponsorFilterWords(sponsor)}
          <Button variant="ghost" size="icon-xs" aria-label={CLEAR_SPONSOR} onClick={() => setSponsor("")}><XIcon /></Button>
        </span>
      )}
    </>
  );

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <Button variant="outline" size="sm" onClick={() => void reload()} aria-label={RELOAD_LIST}>
            <RefreshCwIcon /> {RELOAD}
          </Button>
        }
      />
      <div className="px-6 py-5">
        {state.kind === "loading" && <p className="text-dense text-muted-foreground">{READING_LIST}</p>}
        {state.kind === "error" && (
          <div className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-dense text-foreground" role="alert">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => void reload()}>{RELOAD_NOW}</Button>
          </div>
        )}
        {state.kind === "ready" && state.problem && (
          <div className="mb-3"><FetchError subject={route.label} detail={state.problem} lastRead={state.lastRead} /></div>
        )}
        {state.kind === "ready" && (
          <>
            <div className="mb-3">
              <Segmented label={KIND_TABS_LABEL} value={kind} onChange={setKind} options={tabs} data-kind-tabs />
            </div>
            <DataTable
              rows={rows}
              columns={columns}
              labels={USER_LABELS}
              rowKey={(r) => r.id}
              rowName={(r) => r.username}
              dataAttr="data-user"
              onOpen={(r) => navigate("users", [r.id])}
              filterBar={filterBar}
              count={(n) => countLine(n, count(kind), more)}
              emptyText={filtered ? EMPTY_FILTERED : kind !== "all" ? emptyKind(kind) : EMPTY_DIRECTORY}
              serverSort={{
                state: { id: sorting.key, desc: sorting.order === "desc" },
                onChange: (s) => setSorting(s ? { key: s.id, order: s.desc ? "desc" : "asc" } : { key: "name", order: "asc" }),
              }}
              widths={USER_WIDTHS}
              minWidth={userMinWidth}
              hiddenByDefault={["created"]}
              foot={more ? (
                <div>
                  <Button variant="outline" size="sm" disabled={busy} onClick={() => void loadMore()}>
                    {busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
                  </Button>
                </div>
              ) : undefined}
            />
          </>
        )}
      </div>
    </>
  );
}
