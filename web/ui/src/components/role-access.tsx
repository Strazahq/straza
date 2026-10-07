import * as React from "react";
import { Loader2Icon, PencilIcon, Trash2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { AccessEditor } from "@/components/access-editor";
import { type Built, RoleSaves, liveExport } from "@/components/role-saves";
import { type Plan, emptyPlan, grantMatchers, sameRules } from "@/lib/access-plan";
import { type AccessRead, accessRead, mayWritePolicy, retires } from "@/lib/access-read";
import { type ApiError, type AppRow, type BindingRow, type PreviewEntry, type RoleRow, type ToolRow, catalogPreview, getPolicy, listRoles, removeBinding } from "@/lib/api";
import { type GrantInput, storedRules } from "@/lib/grant-commit";
import { put } from "@/lib/handoff";
import { heldKinds, heldSets } from "@/lib/held-kinds";
import { notify } from "@/lib/notify";
import { type Yaml, asList, asMap, parsePolicy } from "@/lib/policy-yaml";
import { useFirstCall } from "@/lib/first-call";
import { accessItems, readOwnSet } from "@/lib/role-draft";
import { navigate } from "@/lib/router";
import { NOTHING_SAVED, REMOVE_SET, removeSetBody, removeSetTitle, savedEdit } from "@/lib/save-words";
import {
  ACCESS_HEAD,
  CANCEL,
  EDITOR_LEDE,
  EDIT_ACCESS,
  NOTHING_CHANGED,
  NO_SERVER_ROLE,
  OPEN_AUDIT,
  OPEN_IN_AUDIT,
  PICK_A_TOOL,
  POLICY_HELP,
  READING_POLICY,
  REMOVE_ACCESS,
  REMOVE_ACCESS_HELP,
  STEP_SUBJECT,
  UNCHANGED_TOOLS,
  accessRemovedToast,
  accessSetName,
  accessToast,
  editTitle,
  firstCallHit,
  firstCallOff,
  firstCallStopped,
  firstCallWatching,
  heldTitle,
  isGlob,
  matchersTitle,
  outcomeWords,
  ownedNoAccess,
  policySummary,
  removeAccessBody,
  removeAccessTitle,
  staleRules,
  toolsWords,
} from "@/lib/role-words";
import { refused } from "@/lib/say";
import { cn } from "@/lib/utils";
import { relTimeText } from "@/lib/words";

// The Access tab of an application role: the row
// of the one server the role reaches, what its tools are, what policy does
// on a call, and the two acts on the row. Edit access opens the wide sheet
// with the access editor on the row, or on the owning server for a role
// its server owns that has none. A role that belongs to no server and has
// no row reaches no server, and nothing here gives it one. The sheet's foot
// saves the row and the role's own set as one draft, so
// a row and its gate go live together.

const DESTRUCTIVE = "bg-danger text-white hover:bg-danger/90";

// FirstCallRow is the line under a server's row after a grant lands: the
// audit log's own witness that the grant is real.
function FirstCallRow({ app, since }: { app: string; since: number }) {
  const call = useFirstCall(app, true, since);
  const dot = <span aria-hidden="true" className={cn("mr-2 inline-block size-2 rounded-full align-middle", call.state === "watching" ? "bg-ok" : "bg-muted-foreground")} />;
  const openAudit = (seq?: number) => {
    put("audit", seq === undefined ? { q: app } : { q: app, seq });
    navigate("audit");
  };
  return (
    <TableRow data-first-call={call.state}>
      <TableCell colSpan={4} className="whitespace-normal text-[13px] leading-snug text-text-2">
        {call.state === "watching" && <>{dot}{firstCallWatching(app)}</>}
        {call.state === "hit" && call.rec && (
          <>
            {firstCallHit(app, call.rec.username, call.rec.tool, relTimeText(call.rec.time), outcomeWords(call.rec.effect, call.rec.setName, call.rec.ruleId))}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" onClick={() => openAudit(call.rec?.seq)}>{OPEN_IN_AUDIT}</Button>
          </>
        )}
        {call.state === "stopped" && (
          <>
            {dot}{firstCallStopped(app)}{" "}
            <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" onClick={() => openAudit()}>{OPEN_AUDIT}</Button>
          </>
        )}
        {call.state === "off" && firstCallOff(app)}
      </TableCell>
    </TableRow>
  );
}

export type RoleAccessProps = {
  role: RoleRow;
  bindings: BindingRow[];
  apps: AppRow[];
  tools: ToolRow[];
  // sheetOpen is the page's primary asking for the sheet, which opens on
  // the role's row or on the server that owns it.
  sheetOpen: boolean;
  onSheetOpenChange: (open: boolean) => void;
  onChanged: () => void;
  // globalAdmin says the session holds the apps grant, so the glob is
  // offered on a role its server owns.
  globalAdmin: boolean;
};

type Editing = { app: AppRow; binding: BindingRow | null };

export function RoleAccess({ role, bindings, apps, tools, sheetOpen, onSheetOpenChange, onChanged, globalAdmin }: RoleAccessProps) {
  const [previews, setPreviews] = React.useState<Record<string, Record<string, PreviewEntry> | null>>({});
  // ownText is the stored text of the role's own set: null when there is
  // none, undefined while it is read or when it could not be. texts are the
  // other sets a preview says hold a tool, read once per load so the Policy
  // cell and the editor can say hold or ticket.
  const [ownText, setOwnText] = React.useState<string | null | undefined>(undefined);
  // saved is the own set read as a saved edit nobody published, which the
  // sheet says at open and which stops every save.
  const [saved, setSaved] = React.useState(false);
  const [texts, setTexts] = React.useState<Record<string, string | null>>({});
  const [approvers, setApprovers] = React.useState<RoleRow[]>([]);
  const [editing, setEditing] = React.useState<Editing | null>(null);
  const [plan, setPlan] = React.useState<Plan>(emptyPlan);
  // read is what the editor opened on, null while the own set is read.
  const [read, setRead] = React.useState<AccessRead | null>(null);
  const [note, setNote] = React.useState<string | null>(null);
  // saving is the sheet's foot at work, which keeps the sheet open.
  const [saving, setSaving] = React.useState(false);
  const [ask, setAsk] = React.useState<BindingRow | null>(null);
  const [askProblem, setAskProblem] = React.useState<string | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [nonce, setNonce] = React.useState(0);
  const opening = React.useRef("");
  const [landed, setLanded] = React.useState<{ app: string; since: number } | null>(null);

  const grants = bindings.filter((b) => b.role === role.name);
  const appOf = (name: string) => apps.find((a) => a.name === name);
  const toolsOf = (name: string) => tools.filter((t) => t.app === name);
  const open = !!editing;
  const current = editing ? editing.app : null;
  const currentTools = current ? toolsOf(current.name) : [];
  const currentNames = currentTools.map((t) => t.name);
  const ownName = accessSetName(role.name);
  // A session that may not publish policy changes the tools and reads the
  // Policy column. Only a global admin gives a role its server owns the
  // glob, since the server refuses it from anyone else.
  const readOnly = open && !mayWritePolicy();
  const namesOnly = !!role.server && !globalAdmin;

  // The previews are one read per server: what a session holding the role
  // gets on it today, which is what the Policy column folds into counts.
  const key = grants.map((b) => b.id + ":" + (b.tools || []).join("|")).join(",");
  React.useEffect(() => {
    let alive = true;
    for (const b of grants) {
      catalogPreview(role.name, b.app).then(
        (r) => {
          if (!alive) return;
          const byTool: Record<string, PreviewEntry> = {};
          for (const e of r.entries || []) if (e.tool) byTool[e.tool] = e;
          setPreviews((p) => ({ ...p, [b.app]: byTool }));
        },
        () => {
          if (alive) setPreviews((p) => ({ ...p, [b.app]: null }));
        },
      );
    }
    return () => { alive = false; };
  }, [role.name, key, nonce]); // eslint-disable-line react-hooks/exhaustive-deps

  // The role's own set carries the rules this page writes.
  React.useEffect(() => {
    let alive = true;
    void readOwnSet(ownName).then((own) => { if (alive) { setOwnText(own.text); setSaved(own.saved); } });
    return () => { alive = false; };
  }, [ownName, nonce]);

  // The sets other than the role's own that hold a tool are read once each,
  // since the preview names a rule but not whether it is a hold or a ticket.
  const heldKey = Object.values(previews).flatMap((p) => heldSets(p)).filter((n, i, xs) => n !== ownName && xs.indexOf(n) === i).sort().join(",");
  React.useEffect(() => {
    let alive = true;
    for (const name of heldKey ? heldKey.split(",") : []) {
      getPolicy(name).then(
        (s) => { if (alive) setTexts((t) => ({ ...t, [name]: s.yaml || "" })); },
        () => { if (alive) setTexts((t) => ({ ...t, [name]: null })); },
      );
    }
    return () => { alive = false; };
  }, [heldKey, nonce]);

  // The approver roles are the pools the editor offers; they are read once
  // the first sheet opens for a session that may publish policy, not with
  // the tab.
  React.useEffect(() => {
    if (!open || readOnly || approvers.length) return undefined;
    let alive = true;
    listRoles().then(
      (rs) => { if (alive) setApprovers((rs || []).filter((r) => r.kind === "approver")); },
      () => undefined,
    );
    return () => { alive = false; };
  }, [open, readOnly, approvers.length]);

  const ownRules = React.useMemo<Yaml[]>(() => {
    try {
      return asList(asMap(asMap(parsePolicy(ownText || "")).spec).rules);
    } catch {
      return [];
    }
  }, [ownText]);
  const staleFor = (app: string) => ownRules.filter((r) => asList(asMap(r).apps).includes(app)).map((r) => String(asMap(r).id || "")).filter(Boolean);
  const stored = editing ? editing.binding : null;
  const matchers = current ? grantMatchers(plan, currentNames) : [];
  const before = stored ? (isGlob(stored.tools) ? ["*"] : (stored.tools || []).slice().sort()) : null;
  const sameTools = !!before && JSON.stringify(before) === JSON.stringify(matchers);
  const sameCalls = !!read && !!current && sameRules(read.initial, plan, current.name, currentNames, role.name);
  const retiring = !!read && !!current && retires(read, plan, current.name, currentNames, role.name);
  // written is what a save would store for this server in the own set the
  // editor opened on, so the fold shows the kept ids and reasons.
  const written = read && current && typeof read.text === "string" ? storedRules(read.text, plan, current.name, currentNames, role.name) : undefined;
  // changed says a save would store tools or rules other than the ones the
  // editor opened on.
  const changed = !!read && !!current && (!sameCalls || JSON.stringify(grantMatchers(read.initial, currentNames)) !== JSON.stringify(matchers));

  // seed opens the editor on what was read. A reader of policy has no Which
  // tools question, so a row naming every tool opens as ticks it can change.
  const seed = (r: AccessRead) => {
    setRead(r);
    setPlan(!mayWritePolicy() && r.initial.reach === "today" ? { ...r.initial, reach: "tick" } : r.initial);
  };

  const close = () => {
    opening.current = "";
    setEditing(null);
    setPlan(emptyPlan());
    setRead(null);
    setNote(null);
    onSheetOpenChange(false);
  };

  // openEdit reads the role's own set afresh before the editor opens, so
  // the rules a save starts from are the ones stored now. It opens on the
  // row b on server, or with b null on the server that owns a role whose
  // row is gone.
  const openEdit = async (server: string, b: BindingRow | null) => {
    const app = appOf(server) || { id: server, name: server, runtime: "", status: "", reached_by: [] };
    const key = b ? b.id : server;
    opening.current = key;
    setNote(null);
    setRead(null);
    setEditing({ app, binding: b });
    const own = await readOwnSet(ownName);
    if (opening.current !== key) return;
    setSaved(own.saved);
    seed(accessRead(role.name, server, toolsOf(server).map((t) => t.name), b, own.text, b ? previews[server] : undefined, texts));
  };

  // The page's primary is a one-shot ask, answered before the frame paints:
  // a role with its row gets the row's editor, and a role its server owns
  // with none gets the editor on that server. The ask is cleared at once so
  // a later reload of the rows, while the sheet shows what landed, never
  // reopens it.
  const row = grants.length ? grants[0] : null;
  React.useLayoutEffect(() => {
    if (!sheetOpen) return;
    onSheetOpenChange(false);
    if (row) void openEdit(row.app, row);
    else if (role.server) void openEdit(role.server, null);
  }, [sheetOpen]); // eslint-disable-line react-hooks/exhaustive-deps

  // build keeps both saves clickable, and a click
  // that cannot save says what is missing where the fix is. A save that
  // would delete the role's own set asks first. The Role put starts from
  // the role's live export, read now, and is read only when the row
  // changes.
  const build = async (confirmed: boolean): Promise<Built> => {
    if (!current || !read) return null;
    if (!matchers.length) {
      setNote(PICK_A_TOOL);
      return null;
    }
    if (sameTools && sameCalls) {
      setNote(NOTHING_CHANGED);
      return null;
    }
    if (!confirmed && retiring) return "ask";
    setNote(null);
    const input: GrantInput = {
      role: { name: role.name, id: role.id },
      app: current,
      tools: currentTools,
      plan,
      replace: sameTools ? null : stored,
      keep: sameTools,
      before: read.initial,
    };
    const text = sameTools ? "" : await liveExport(role);
    if (typeof text !== "string") return text;
    const answer = await accessItems(input, text);
    return "error" in answer ? { tone: "refused", lines: [NOTHING_SAVED, answer.error] } : answer.items;
  };

  // published follows a publish of the sheet's draft: a new row gets the
  // first-call watcher, and the tab reads its rows again.
  const published = () => {
    if (current && !sameTools) setLanded({ app: current.name, since: Date.now() });
    close();
    setNonce((n) => n + 1);
    onChanged();
  };

  const remove = async () => {
    if (!ask || busy) return;
    setBusy(true);
    setAskProblem(null);
    try {
      await removeBinding(ask.id);
    } catch (e) {
      setAskProblem(refused(e as ApiError));
      setBusy(false);
      return;
    }
    setBusy(false);
    notify.ok(accessRemovedToast(role.name, ask.app));
    setAsk(null);
    setNonce((n) => n + 1);
    onChanged();
  };

  return (
    <div className="flex flex-col gap-3" data-role-access={role.name}>
      <div className="rounded-md border border-border">
        <Table className="table-fixed">
          <colgroup>
            <col className="w-[18%]" />
            <col className="w-[30%]" />
            <col />
            <col className="w-[88px]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead>{ACCESS_HEAD.server}</TableHead>
              <TableHead>{ACCESS_HEAD.tools}</TableHead>
              <TableHead>
                <span className="inline-flex items-center gap-1.5">{ACCESS_HEAD.policy}<HelpTip label={ACCESS_HEAD.policy} text={POLICY_HELP} /></span>
              </TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {grants.length === 0 && (
              <TableRow>
                <TableCell colSpan={4} className="whitespace-normal text-muted-foreground" data-no-access>{role.server ? ownedNoAccess(role.server) : NO_SERVER_ROLE}</TableCell>
              </TableRow>
            )}
            {grants.map((b) => {
              const app = appOf(b.app);
              const known = toolsOf(b.app).length || (app && app.tools ? app.tools.length : 0) || null;
              const kinds = heldKinds(previews[b.app], { ...texts, [ownName]: ownText });
              const summary = policySummary(previews[b.app], kinds);
              return (
                <React.Fragment key={b.id}>
                  <TableRow data-access={b.app}>
                    <TableCell className="truncate">
                      {app ? (
                        <Button variant="link" size="sm" className="h-auto p-0 font-mono text-[13px]" onClick={() => navigate("servers", [app.id])}>{b.app}</Button>
                      ) : (
                        <span className="font-mono text-[13px]">{b.app}</span>
                      )}
                    </TableCell>
                    <TableCell className="truncate" title={matchersTitle(b.tools)}>{toolsWords(b.tools, known)}</TableCell>
                    <TableCell className="truncate text-text-2" title={heldTitle(summary, kinds)} data-policy-cell>{summary}</TableCell>
                    <TableCell className="text-right whitespace-nowrap">
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-xs" aria-label={EDIT_ACCESS} onClick={() => void openEdit(b.app, b)}><PencilIcon /></Button>
                        </TooltipTrigger>
                        <TooltipContent>{EDIT_ACCESS}</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Button variant="ghost" size="icon-xs" className="text-danger hover:bg-danger-bg" aria-label={REMOVE_ACCESS} onClick={() => { setAskProblem(null); setAsk(b); }}><Trash2Icon /></Button>
                        </TooltipTrigger>
                        <TooltipContent>{REMOVE_ACCESS}</TooltipContent>
                      </Tooltip>
                    </TableCell>
                  </TableRow>
                  {landed && landed.app === b.app && <FirstCallRow app={b.app} since={landed.since} />}
                </React.Fragment>
              );
            })}
          </TableBody>
        </Table>
      </div>

      <Sheet open={open} onOpenChange={(o) => { if (!o && !saving) close(); }}>
        <SheetContent className="w-full gap-0 p-0 sm:max-w-3xl" data-access-sheet={editing ? editing.app.name : ""}>
          <SheetHeader className="border-b border-border pr-12">
            <SheetTitle className="text-lg leading-snug">{editing ? editTitle(role.name, editing.app.name) : ""}</SheetTitle>
            <SheetDescription>{EDITOR_LEDE}</SheetDescription>
          </SheetHeader>
          <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-4">
            {saved && <p role="alert" className="m-0 max-w-[75ch] text-[13px] text-danger" data-saved-edit>{savedEdit(ownName)}</p>}
            {current && !read && <p role="status" className="m-0 text-[13px] text-muted-foreground" data-reading>{READING_POLICY}</p>}
            {current && read && (
              <AccessEditor
                role={role.name}
                app={current}
                tools={currentTools}
                plan={plan}
                onPlan={(update) => { setNote(null); setPlan(update); }}
                fixed={read.fixed}
                approvers={approvers}
                ownSet={ownName}
                taken={read.taken}
                stored={stored}
                readOnly={readOnly}
                namesOnly={namesOnly}
                unread={read.unread}
                written={written}
              />
            )}
            {note ? (
              <p role="status" className="m-0 text-[13px] text-danger" data-save-note>{note}</p>
            ) : sameTools && read && !sameCalls && !retiring ? (
              <p role="status" className="m-0 text-[13px] text-muted-foreground" data-save-note>{UNCHANGED_TOOLS}</p>
            ) : null}
          </div>
          <SheetFooter className="mt-0 border-t border-border">
            <RoleSaves
              role={role.name}
              toast={current && !sameTools ? accessToast(role.name, current.name) : undefined}
              build={build}
              ask={(asked, confirm, cancel, publish) => (
                <AlertDialog open={asked} onOpenChange={(o) => { if (!o) cancel(); }}>
                  <AlertDialogContent className="sm:max-w-[560px]" data-retire-dialog>
                    <AlertDialogHeader>
                      <AlertDialogTitle>{removeSetTitle(ownName)}</AlertDialogTitle>
                      <AlertDialogDescription>{removeSetBody(role.name, current ? current.name : "", publish)}</AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
                      <AlertDialogAction className={DESTRUCTIVE} onClick={confirm}>{REMOVE_SET}</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              )}
              onPublished={published}
              onCancel={close}
              changed={changed}
              edits={JSON.stringify(plan)}
              onBusy={setSaving}
            />
          </SheetFooter>
        </SheetContent>
      </Sheet>

      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !busy) { setAsk(null); setAskProblem(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]" data-remove-dialog>
          {ask && (
            <>
              <AlertDialogHeader>
                <AlertDialogTitle>{removeAccessTitle(ask.app)}</AlertDialogTitle>
                <AlertDialogDescription>
                  {removeAccessBody(role.name, ask.app)}
                  {staleFor(ask.app).length > 0 && <span data-stale-rules>{" " + staleRules(role.name, ask.app, staleFor(ask.app))}</span>}
                  <HelpTip label={REMOVE_ACCESS} text={REMOVE_ACCESS_HELP} className="ml-1" />
                </AlertDialogDescription>
              </AlertDialogHeader>
              {askProblem && <RefusedError subject={STEP_SUBJECT.remove} message={askProblem} />}
              <AlertDialogFooter>
                <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
                <AlertDialogAction className={DESTRUCTIVE} disabled={busy} onClick={(e) => { e.preventDefault(); void remove(); }}>
                  {busy && <Loader2Icon className="animate-spin" />}
                  {REMOVE_ACCESS}
                </AlertDialogAction>
              </AlertDialogFooter>
            </>
          )}
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
