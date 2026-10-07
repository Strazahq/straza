import * as React from "react";
import { Loader2Icon, RefreshCwIcon, XIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DataTable } from "@/components/data-table";
import { FetchError } from "@/components/error-state";
import { PageHead } from "@/components/page-head";
import { USER_LABELS, userColumns } from "@/components/users-table";
import { UserSheet } from "@/components/user-sheet";
import { type RoleRow, type UserRow, getUser, listRoles, listUsers } from "@/lib/api";
import { put, take } from "@/lib/handoff";
import { type Sorting, usePagedList } from "@/lib/paged";
import { navigate } from "@/lib/router";
import { routeByKey } from "@/lib/routes";
import { EMPTY_DIRECTORY, EMPTY_FILTERED, READING_LIST, SEARCH_PLACEHOLDER, countWords, sortedWords, sponsorFilterWords } from "@/lib/user-words";

// The Users directory:
// one row per identity Straza knows, every filter and the sort answered by
// the server, and a row opening the user's sheet beside the list. There is
// no Add user: identities arrive over SCIM or through strazactl.

const route = routeByKey("users");
const ALL = "all";
const ANY = "any";
const DEBOUNCE_MS = 300;

type Props = {
  // openID is the user whose sheet opens at once, from the address
  // (users/<id>); the palette hands the same thing over through the box.
  openID?: string;
};

export function Users({ openID }: Props) {
  const [typed, setTyped] = React.useState("");
  const [q, setQ] = React.useState("");
  const [status, setStatus] = React.useState(ALL);
  const [role, setRole] = React.useState(ANY);
  // The hand-off box carries a user to open, from the palette, or a sponsor
  // to filter by, from a person's Agents count on another screen.
  const [handoff] = React.useState(() => take<{ open?: string; sponsor?: string }>("users") || {});
  const [sponsor, setSponsor] = React.useState(handoff.sponsor || "");
  const [sorting, setSorting] = React.useState<Sorting>({ key: "name", order: "asc" });
  const [roles, setRoles] = React.useState<RoleRow[]>([]);
  const [rolesStale, setRolesStale] = React.useState(false);
  const [open, setOpen] = React.useState<UserRow | null>(null);
  const [wanted] = React.useState(() => openID || handoff.open || "");

  // A user named on the way in opens at once, from its own read, since the
  // list page may not hold it.
  React.useEffect(() => {
    if (!wanted) return;
    let alive = true;
    getUser(wanted).then((u) => { if (alive) setOpen(u); }, () => {});
    return () => { alive = false; };
  }, [wanted]);

  // The search box asks the server, so it waits for the typing to settle.
  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  // The role catalog is read once: it names the Role filter's options.
  React.useEffect(() => {
    let alive = true;
    listRoles().then(
      (rs) => { if (alive) setRoles(rs || []); },
      () => { if (alive) setRolesStale(true); },
    );
    return () => { alive = false; };
  }, []);

  const params = React.useMemo(
    () => ({ q, status: status === ALL ? "" : status, role: role === ANY ? "" : role, sponsor }),
    [q, status, role, sponsor],
  );
  const { state, busy, reload, loadMore } = usePagedList<UserRow>({ read: listUsers, subject: "The user list", params, sorting, limit: 100, pollMs: 60000 });
  const rows = state.kind === "ready" ? state.rows : [];

  // The open sheet reads the row the list holds, so a poll that lands
  // while the sheet is open keeps it fresh.
  React.useEffect(() => {
    if (!open) return;
    const fresh = rows.find((r) => r.id === open.id);
    if (fresh && fresh !== open) setOpen(fresh);
  }, [rows]); // eslint-disable-line react-hooks/exhaustive-deps

  const columns = React.useMemo(() => userColumns((username) => setSponsor(username)), []);
  const filtered = !!(q || sponsor || status !== ALL || role !== ANY);

  const filterBar = (
    <>
      <Input
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        placeholder={SEARCH_PLACEHOLDER}
        aria-label="Search users"
        className="h-9 w-full max-w-xs"
      />
      <Select value={status} onValueChange={setStatus}>
        <SelectTrigger aria-label="Status" className="h-9 w-[150px]" data-status-filter>
          <span className="text-muted-foreground">Status</span>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>all</SelectItem>
          <SelectItem value="active">active</SelectItem>
          <SelectItem value="disabled">disabled</SelectItem>
        </SelectContent>
      </Select>
      <Select value={role} onValueChange={setRole}>
        <SelectTrigger aria-label="Role" className="h-9 w-[190px]" data-role-filter>
          <span className="text-muted-foreground">Role</span>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ANY}>any role</SelectItem>
          {roles.map((r) => <SelectItem key={r.id} value={r.name} className="font-mono">{r.name}</SelectItem>)}
        </SelectContent>
      </Select>
      {sponsor && (
        <span className="inline-flex h-9 items-center gap-1.5 rounded-md border border-border bg-background pl-2.5 pr-1 text-sm" data-sponsor-chip={sponsor}>
          {sponsorFilterWords(sponsor)}
          <Button variant="ghost" size="icon-xs" aria-label="Clear the sponsor filter" onClick={() => setSponsor("")}><XIcon /></Button>
        </span>
      )}
      <span className="order-last basis-full text-[13px] text-muted-foreground" data-sorted-hint>
        {sortedWords(sorting.key, sorting.order === "asc")}
      </span>
    </>
  );

  return (
    <>
      <PageHead
        label={route.label}
        description={route.description}
        actions={
          <Button variant="ghost" size="sm" onClick={() => void reload()} aria-label="Reload the list">
            <RefreshCwIcon /> Reload
          </Button>
        }
      />
      <div className="px-6 py-5">
        {state.kind === "loading" && <p className="text-sm text-muted-foreground">{READING_LIST}</p>}
        {state.kind === "error" && (
          <div className="rounded-md border border-danger/40 bg-danger-bg px-4 py-3 text-sm text-foreground" role="alert">
            {state.message}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-link" onClick={() => void reload()}>Reload now</Button>
          </div>
        )}
        {state.kind === "ready" && state.problem && (
          <div className="mb-3"><FetchError subject={route.label} detail={state.problem} lastRead={state.lastRead} /></div>
        )}
        {state.kind === "ready" && (
          <DataTable
            rows={state.rows}
            columns={columns}
            labels={USER_LABELS}
            rowKey={(r) => r.id}
            rowName={(r) => r.username}
            dataAttr="data-user"
            onOpen={(r) => setOpen(r)}
            openKey={open?.id || null}
            filterBar={filterBar}
            count={() => countWords(state.rows.length, state.more)}
            emptyText={filtered ? EMPTY_FILTERED : EMPTY_DIRECTORY}
            serverSort={{
              state: { id: sorting.key, desc: sorting.order === "desc" },
              onChange: (s) => setSorting(s ? { key: s.id, order: s.desc ? "desc" : "asc" } : { key: "name", order: "asc" }),
            }}
            hiddenByDefault={["created"]}
            foot={state.more ? (
              <div>
                <Button variant="outline" size="sm" disabled={busy} onClick={() => void loadMore()}>
                  {busy && <Loader2Icon className="animate-spin" />} Load more
                </Button>
              </div>
            ) : undefined}
          />
        )}
      </div>

      {open && (
        <UserSheet
          user={open}
          roles={roles}
          rolesStale={rolesStale}
          open={true}
          onOpenChange={(o) => { if (!o) setOpen(null); }}
          onChanged={() => void reload()}
          onFilterSessions={(id) => { put("sessions", { id, username: open.username }); navigate("sessions"); }}
        />
      )}
    </>
  );
}
