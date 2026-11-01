import * as React from "react";
import { Loader2Icon, PlusIcon, ShieldIcon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { AddRoleSheet } from "@/components/add-role-sheet";
import { EmptyState } from "@/components/empty-state";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { NoRole, RoleChip } from "@/components/status-badge";
import { SHOWN_VALUES, coversCell } from "@/lib/access-object-words";
import { coversOf, partial } from "@/lib/access-covers";
import { type ApiError, type AppRow, type BindingRow, type RoleRow, type ToolRow, deleteRole, listBindings } from "@/lib/api";
import { notify } from "@/lib/notify";
import { type Nouns, cap, nounsOf } from "@/lib/object-words";
import { CANCEL, deletedToast, isGlob, matchersTitle, toolsWords } from "@/lib/role-words";
import { refused } from "@/lib/say";
import {
  ADD_ROLE,
  COVERS_UNREAD,
  DELETE_ROLE,
  EDIT_ACCESS,
  EVERY_TOOL,
  ROLE_HELP,
  TAB_HEAD,
  certifiedHint,
  countLine,
  coversUnread,
  deleteBody,
  deleteTitle,
  emptyBody,
  emptyTitle,
  frozenTools,
  heldDelete,
  heldRule,
  moreCovers,
  noCovers,
  reachHead,
  unheldLine,
} from "@/lib/server-roles-words";
import { cn } from "@/lib/utils";
import { matchesTool } from "@/lib/words";

// The Roles tab of an MCP server's page: the
// roles this server owns, what each one reaches and, on a server that pins
// a type, which of its objects each covers, who holds it, and the two acts
// on it. A role with holders is frozen: its tools change only by the
// global admin, and it is deleted only once the identity manager has
// removed the holders, so both actions grey out with the server's own
// sentence and say it again on a click.

const DESTRUCTIVE = "bg-danger text-white hover:bg-danger/90";
const DANGER = "border-danger text-danger hover:bg-danger-bg";
// CELL is one tool in the picture of a role's reach: filled in the accent
// for a tool in the role's selection, hollow for one outside it.
const CELL = "size-5 rounded border";
const EVERY = "rounded-md px-2 text-[13px] font-normal bg-accent-bg text-link border-link/40";

export type ServerRolesTabProps = {
  app: AppRow;
  // roles are the roles this server owns, which the page picked out of the
  // roles list it read.
  roles: RoleRow[];
  // tools are the tools list rows of every server, which a server admin
  // may not read. The server row's own names stand in then.
  tools: ToolRow[];
  // bindings are the access rows the page read, from which the column of
  // a pinned type reads each role's covers line, or null when strazad
  // refused the list.
  bindings: BindingRow[] | null;
  // globalAdmin says the session holds the apps grant, so it may widen a held
  // role that its server admin may not, and give a role every tool.
  globalAdmin: boolean;
  onChanged: () => void;
};

type Refusal = { subject: string; message: string };
type Editing = { role: RoleRow; binding: BindingRow | null };

export function ServerRolesTab({ app, roles, tools, bindings, globalAdmin, onChanged }: ServerRolesTabProps) {
  const [sheet, setSheet] = React.useState(false);
  const [editing, setEditing] = React.useState<Editing | null>(null);
  const [refusal, setRefusal] = React.useState<Refusal | null>(null);
  const [ask, setAsk] = React.useState<RoleRow | null>(null);
  const [busy, setBusy] = React.useState(false);

  const known = tools.filter((t) => t.app === app.name);
  const toolRows: ToolRow[] = known.length ? known : (app.tools || []).map((n) => ({ id: n, app: app.name, app_id: app.id, name: n, description: "" }));
  // covered names the objects a held role's covers line reaches on a server
  // that pins a type, as the server's refusal names them, and heads the
  // column of what each role covers.
  const pinned = app.manifest?.straza?.objects;
  const w = pinned ? nounsOf(pinned) : null;
  const covered = w ? w.plural : "";

  const add = () => { setRefusal(null); setEditing(null); setSheet(true); };

  // Edit access replaces the access row, so the row it replaces is read
  // first. A session the server refuses that read is told what it said.
  const edit = async (role: RoleRow) => {
    setRefusal(null);
    setBusy(true);
    let binding: BindingRow | null = null;
    try {
      binding = (await listBindings()).find((b) => b.role === role.name && b.app === app.name) || null;
    } catch (e) {
      setRefusal({ subject: EDIT_ACCESS, message: refused(e as ApiError) });
      setBusy(false);
      return;
    }
    setBusy(false);
    setEditing({ role, binding });
    setSheet(true);
  };

  const remove = async () => {
    if (!ask || busy) return;
    setBusy(true);
    setRefusal(null);
    let setsOff: string[] | undefined;
    try {
      setsOff = (await deleteRole(ask.id))?.sets_off;
    } catch (e) {
      setRefusal({ subject: DELETE_ROLE, message: refused(e as ApiError) });
      setBusy(false);
      setAsk(null);
      return;
    }
    setBusy(false);
    notify.ok(deletedToast(ask.name, setsOff));
    setAsk(null);
    onChanged();
  };

  const sheetParts = (
    <AddRoleSheet
      app={app}
      tools={toolRows}
      role={editing ? editing.role : null}
      roles={roles}
      binding={editing ? editing.binding : null}
      globalAdmin={globalAdmin}
      open={sheet}
      onOpenChange={(o) => { setSheet(o); if (!o) setEditing(null); }}
      onDone={onChanged}
    />
  );

  if (roles.length === 0) {
    return (
      <div data-server-roles={app.name}>
        {refusal && <RefusedError subject={refusal.subject} message={refusal.message} />}
        <EmptyState icon={ShieldIcon} title={emptyTitle(app.name)} action={<Button onClick={add}><PlusIcon /> {ADD_ROLE}</Button>}>
          {emptyBody(app.name)}
        </EmptyState>
        {sheetParts}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3" data-server-roles={app.name}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] text-muted-foreground" data-role-count>{countLine(roles.length)}</span>
        <Button size="sm" className="ml-auto" onClick={add}><PlusIcon /> {ADD_ROLE}</Button>
      </div>

      {refusal && <RefusedError subject={refusal.subject} message={refusal.message} />}

      <div className="rounded-md border border-border">
        <Table className="table-fixed">
          <colgroup>
            <col className={w ? "w-[26%]" : "w-[34%]"} />
            <col className={w ? "w-[26%]" : "w-[38%]"} />
            {w && <col className="w-[20%]" />}
            <col className="w-[8%]" />
            <col className="w-[20%]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead>
                <span className="inline-flex items-center gap-1.5">{TAB_HEAD.name}<HelpTip label={TAB_HEAD.name} text={ROLE_HELP} /></span>
              </TableHead>
              <TableHead>{TAB_HEAD.tools}</TableHead>
              {w && <TableHead>{cap(w.plural)}</TableHead>}
              <TableHead className="text-right">{TAB_HEAD.holders}</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {roles.map((r) => {
              const held = (r.holder_count || 0) > 0;
              const frozen = held && !globalAdmin;
              const later = isGlob(r.tools);
              const reached = toolRows.filter((t) => matchesTool(r.tools, t.name)).map((t) => t.name);
              const head = reachHead(reached.length, toolRows.length, later);
              const names = later ? EVERY_TOOL : reached;
              return (
                <React.Fragment key={r.id}>
                  <TableRow data-owned-role={r.name}>
                    <TableCell className="py-4 align-baseline whitespace-normal">
                      <div className="flex flex-col gap-1.5">
                        <span className="font-mono font-semibold break-words text-foreground">{r.name}</span>
                        {r.description && <span className="max-w-[52ch] text-text-2">{r.description}</span>}
                      </div>
                    </TableCell>
                    <TableCell className="py-4 align-baseline whitespace-normal" title={matchersTitle(r.tools)}>
                      {/* A server that lists no tool has no cell to draw, so the role's own matchers are said in words. */}
                      {toolRows.length === 0 ? toolsWords(r.tools, null) : (
                        <div className="flex flex-col gap-2">
                          <div className="flex items-baseline gap-2">
                            <b className="font-semibold text-foreground tabular-nums">{head[0]}</b>
                            <span className="text-text-2">{head[1]}</span>
                          </div>
                          {/* The headline and the names line say what the cells draw, so the cells stay out of the accessibility tree. */}
                          <div className="flex flex-wrap gap-1" aria-hidden="true" data-reach>
                            {toolRows.map((t) => {
                              const on = reached.includes(t.name);
                              return <span key={t.name} title={t.name} data-cell={on ? "on" : "off"} className={cn(CELL, on ? "border-primary bg-primary" : "border-input bg-secondary")} />;
                            })}
                            {later && <span data-cell="later" className={cn(CELL, "border-dashed border-primary bg-accent-bg")} />}
                          </div>
                          {typeof names === "string"
                            ? <div className="text-[13px] leading-snug text-muted-foreground">{names}</div>
                            : names.length > 0 && <div className="leading-relaxed" data-tool-names>{names.map((n) => <RoleChip key={n} name={n} />)}</div>}
                        </div>
                      )}
                    </TableCell>
                    {w && (
                      <TableCell className="py-4 align-baseline whitespace-normal" data-covers-cell>
                        <CoversCell w={w} rows={bindings} role={r.name} app={app.name} />
                      </TableCell>
                    )}
                    <TableCell className="py-4 text-right align-baseline tabular-nums">
                      <span className={cn("font-semibold", held ? "text-foreground" : "text-muted-foreground")}>{r.holder_count || 0}</span>
                    </TableCell>
                    <TableCell className="py-4 text-right align-baseline whitespace-nowrap">
                      <span className="inline-flex gap-1.5">
                        <Button
                          variant="outline"
                          size="sm"
                          aria-disabled={frozen || undefined}
                          title={frozen ? frozenTools(r.name, r.holder_count || 0, covered) : held ? certifiedHint(r.holder_count || 0) : undefined}
                          className={cn(frozen && "opacity-60")}
                          disabled={busy}
                          onClick={() => (frozen ? setRefusal({ subject: EDIT_ACCESS, message: frozenTools(r.name, r.holder_count || 0, covered) }) : void edit(r))}
                        >
                          {EDIT_ACCESS}
                        </Button>
                        <Button
                          variant="outline"
                          size="sm"
                          aria-disabled={held || undefined}
                          title={held ? heldDelete(r.name, r.holder_count || 0) : undefined}
                          className={cn(DANGER, held && "opacity-60")}
                          disabled={busy}
                          onClick={() => (held ? setRefusal({ subject: DELETE_ROLE, message: heldDelete(r.name, r.holder_count || 0) }) : setAsk(r))}
                        >
                          {DELETE_ROLE}
                        </Button>
                      </span>
                    </TableCell>
                  </TableRow>
                  {!held && (
                    <TableRow data-role-line={r.name}>
                      <TableCell colSpan={w ? 5 : 4} className="whitespace-normal pt-0 text-[13px] leading-snug text-muted-foreground">
                        {unheldLine(r.name)}
                      </TableCell>
                    </TableRow>
                  )}
                </React.Fragment>
              );
            })}
          </TableBody>
        </Table>
      </div>
      {w && !bindings && <p className="text-[13px] text-muted-foreground" data-covers-unread>{coversUnread(w.plural)}</p>}
      <p className="text-[13px] text-muted-foreground" data-held-rule>{heldRule(globalAdmin)}</p>

      {sheetParts}

      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !busy) setAsk(null); }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-delete-role>
          {ask && (
            <>
              <AlertDialogHeader>
                <AlertDialogTitle>{deleteTitle(ask.name)}</AlertDialogTitle>
                <AlertDialogDescription>{deleteBody(app.name)}</AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
                <AlertDialogAction className={DESTRUCTIVE} disabled={busy} onClick={(e) => { e.preventDefault(); void remove(); }}>
                  {busy && <Loader2Icon className="animate-spin" />}
                  {DELETE_ROLE}
                </AlertDialogAction>
              </AlertDialogFooter>
            </>
          )}
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// CoversCell is what one role covers on a server that pins a type, read off
// its access row: the every chip, the first objects its row names as chips
// with the rest counted, or no objects for a row that names none or a role
// with no row here, which the gateway reads as covering nothing. RoleChip
// is the house name chip, so object names wear it as role names do on the
// objects tab.
function CoversCell({ w, rows, role, app }: { w: Nouns; rows: BindingRow[] | null; role: string; app: string }) {
  if (!rows) return <span className="text-muted-foreground">{COVERS_UNREAD}</span>;
  const c = coversOf(rows.find((b) => b.role === role && b.app === app)?.covers);
  if (c && !partial(c)) return <Badge variant="outline" className={EVERY} data-covers="every">{coversCell(w, c)}</Badge>;
  if (!c || !c.objects.length) return <NoRole text={noCovers(w.plural)} />;
  const rest = c.objects.length - SHOWN_VALUES;
  return (
    <div className="leading-relaxed" data-covers="objects">
      {c.objects.slice(0, SHOWN_VALUES).map((o) => <RoleChip key={o.id || o.value} name={o.value} />)}
      {rest > 0 && <span className="text-[13px] text-muted-foreground" data-covers-more>{moreCovers(rest, w)}</span>}
    </div>
  );
}
