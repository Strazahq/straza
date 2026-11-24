import * as React from "react";
import type { ColumnDef } from "@tanstack/react-table";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { DataTable } from "@/components/data-table";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { HolderSource } from "@/components/holder-source";
import { KindChip } from "@/components/user-parts";
import { type PickedUser, UserPicker } from "@/components/user-picker";
import { USER_WIDTHS, UserName, userColumns, userMinWidth } from "@/components/users-table";
import { type ApiError, type AssignmentRow, type RoleRow, type UserRow, getUser, grantRole, listUsers } from "@/lib/api";
import { holdingOf } from "@/lib/user-access";
import { userKind } from "@/lib/user-look";
import { notify } from "@/lib/notify";
import { usePagedList } from "@/lib/paged";
import { navigate } from "@/lib/router";
import { CANCEL, DRIFT_SHORT, GRANT_HELP, GRANT_ROLE, HOLDERS_TAB_HELP, LOAD_MORE, SEARCH_HOLDERS, SUBJECT_HOLDERS, TAB, USER_LABEL, USER_MISSING, grantBody, grantTitle, grantedToast, holdersCount, kindOf, nobodyHolds } from "@/lib/role-words";
import { refused } from "@/lib/say";
import { USER_LABELS } from "@/lib/user-list-words";

// The Holders tab: the Users list
// narrowed to everyone who holds the role through any path, paged on the
// server, and the one write the console makes here, a direct grant. A row
// opens the user's page.

const DEBOUNCE_MS = 300;

type Props = {
  role: RoleRow;
  // roles is the catalog, which names the kind of each role a holder holds.
  roles: RoleRow[];
  // grantOpen is the head's primary opening the Grant dialog.
  grantOpen: boolean;
  onGrantOpenChange: (open: boolean) => void;
  // onChanged tells the page a membership write landed.
  onChanged: () => void;
  // source turns the Source column into where each holder's role comes
  // from, for a role with a group in Entra: every assignment, the group's
  // ID and its name in Entra.
  source?: { assignments: AssignmentRow[]; groupID: string; group: string };
};

export function RoleHolders({ role, roles, grantOpen, onGrantOpenChange, onChanged, source }: Props) {
  const [typed, setTyped] = React.useState("");
  const [q, setQ] = React.useState("");

  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  const params = React.useMemo(() => ({ q, role: role.name }), [q, role.name]);
  const { state, busy, reload, loadMore } = usePagedList<UserRow>({ read: listUsers, subject: SUBJECT_HOLDERS, params, sorting: { key: "name", order: "asc" }, limit: 100 });
  // mixed says the holders read so far are of more than one kind, so each
  // row carries its kind chip under the name, where the tile alone would
  // leave a person and an agent to be told apart by colour.
  const mixed = state.kind === "ready" && new Set(state.rows.map(userKind)).size > 1;
  const columns = React.useMemo(() => {
    const cols = userColumns((u) => navigate("users", [u.id, "agents"]), roles);
    return cols.map((c): ColumnDef<UserRow> => {
      if (c.id === "name" && mixed) {
        return { ...c, cell: ({ row }) => <div className="flex min-w-0 flex-col items-start gap-1"><UserName user={row.original} /><KindChip user={row.original} /></div> };
      }
      if (c.id === "source" && source) {
        return { ...c, cell: ({ row }) => <HolderSource holding={holdingOf(row.original.id, role, source.assignments, roles)} groupID={source.groupID} group={source.group} /> };
      }
      return c;
    });
  }, [roles, role, source, mixed]);

  const changed = () => { void reload(); onChanged(); };

  const filterBar = (
    <>
      <Input
        value={typed}
        onChange={(e) => setTyped(e.target.value)}
        placeholder={SEARCH_HOLDERS}
        aria-label={SEARCH_HOLDERS}
        className="h-9 w-full max-w-xs"
      />
      <HelpTip label={TAB.holders} text={HOLDERS_TAB_HELP} />
    </>
  );

  return (
    <div className="flex flex-col gap-3">
      {state.kind === "error" && <FetchError subject={TAB.holders} detail={state.message} />}
      {state.kind === "ready" && state.problem && <FetchError subject={TAB.holders} detail={state.problem} lastRead={state.lastRead} />}
      {state.kind === "ready" && (
        <DataTable
          rows={state.rows}
          columns={columns}
          labels={USER_LABELS}
          rowKey={(r) => r.id}
          rowName={(r) => r.username}
          dataAttr="data-user"
          onOpen={(r) => navigate("users", [r.id])}
          filterBar={filterBar}
          count={() => holdersCount(state.rows.length, state.more)}
          emptyText={nobodyHolds(role.name, kindOf(role) === "approver" || kindOf(role) === "straza")}
          widths={USER_WIDTHS}
          minWidth={userMinWidth}
          hiddenByDefault={["created"]}
          foot={state.more ? (
            <div>
              <Button variant="outline" size="sm" disabled={busy} onClick={() => void loadMore()}>
                {busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
              </Button>
            </div>
          ) : undefined}
        />
      )}

      <GrantDialog role={role} open={grantOpen} onOpenChange={onGrantOpenChange} onGranted={changed} />
    </div>
  );
}

// GrantDialog is the one membership write of this tab: a user picked on the
// server, one line of consequence, and the drift warning where the identity
// provider masters the membership.
function GrantDialog({ role, open, onOpenChange, onGranted }: { role: RoleRow; open: boolean; onOpenChange: (o: boolean) => void; onGranted: () => void }) {
  const [picked, setPicked] = React.useState<PickedUser | null>(null);
  const [origin, setOrigin] = React.useState("");
  const [missing, setMissing] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const box = React.useRef<HTMLDivElement>(null);

  // The picker answers an id and a username; the drift line needs to know
  // where the identity came from, so the picked row is read once.
  React.useEffect(() => {
    setOrigin("");
    if (!picked) return;
    let alive = true;
    getUser(picked.id).then((u) => { if (alive) setOrigin(u.origin || ""); }, () => { /* no claim about drift without the row */ });
    return () => { alive = false; };
  }, [picked]);

  React.useEffect(() => {
    if (open) return;
    setPicked(null);
    setMissing(false);
    setProblem(null);
  }, [open]);

  const grant = async () => {
    if (!picked) {
      setMissing(true);
      box.current?.querySelector("input")?.focus();
      return;
    }
    setBusy(true);
    setProblem(null);
    try {
      await grantRole(picked.id, role.id);
      notify.ok(grantedToast(picked.username, role.name));
      onOpenChange(false);
      onGranted();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{grantTitle(role.name)}</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-3" ref={box}>
          <UserPicker value={picked} onChange={(u) => { setPicked(u); setMissing(false); }} label={USER_LABEL} pick />
          {missing && <p className="text-caption text-danger">{USER_MISSING}</p>}
          {picked && (
            <p className="flex flex-wrap items-center gap-1 text-dense text-text-2">
              {grantBody(kindOf(role), picked.username, role.name)}
              <HelpTip label={GRANT_ROLE} text={GRANT_HELP} />
            </p>
          )}
          {picked && origin === "scim" && (
            <p className="text-dense text-text-2" data-drift>{DRIFT_SHORT}</p>
          )}
          {problem && <RefusedError subject={GRANT_ROLE} message={problem} />}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>{CANCEL}</Button>
          <Button onClick={() => void grant()} aria-busy={busy || undefined}>
            {busy && <Loader2Icon className="animate-spin" />} {GRANT_ROLE}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
