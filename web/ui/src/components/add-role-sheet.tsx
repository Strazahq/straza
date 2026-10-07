import * as React from "react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { AccessEditor } from "@/components/access-editor";
import { type Built, RoleSaves, liveExport } from "@/components/role-saves";
import { Field, HINT } from "@/components/wizard/parts";
import { type Plan, emptyPlan, grantMatchers, sameRules } from "@/lib/access-plan";
import { type AccessRead, accessRead, mayWritePolicy, readSetText, retires } from "@/lib/access-read";
import { type AppRow, type BindingRow, type PreviewEntry, type RoleRow, type ToolRow, catalogPreview, listRoles } from "@/lib/api";
import { type GrantInput, storedRules } from "@/lib/grant-commit";
import { heldSets } from "@/lib/held-kinds";
import { accessItems, newRoleItems, readOwnSet } from "@/lib/role-draft";
import { CANCEL, NAME_MISSING, NOTHING_CHANGED, PICK_ANOTHER, PICK_A_TOOL, READING_POLICY, UNCHANGED_TOOLS, accessSetName, accessToast, isGlob, liveRole, nameTaken } from "@/lib/role-words";
import { NOTHING_SAVED, REMOVE_SET, removeSetBody, removeSetTitle, savedEdit } from "@/lib/save-words";
import {
  ADD_LEDE,
  DESCRIPTION,
  DESCRIPTION_HINT,
  EDIT_LEDE,
  NAME,
  NAME_SUFFIX,
  addTitle,
  cliCreateRole,
  editTitle,
  foldName,
  previewParts,
  roleNameOf,
  rolePrefix,
} from "@/lib/server-roles-words";
import { cn } from "@/lib/utils";

// The Add role sheet of a server's Roles tab:
// the name with the server's prefix fixed and only the suffix typed, the
// live preview of what is stored, and the same access editor as New role.
// Only a global admin gives a role every tool and tools added later, so a
// server admin names its tools. Edit tools opens the same sheet with the
// name fixed. Both end in Save draft and Save and publish, so the role's
// document and its own set go live together in one
// draft or not at all.

const CODE = "rounded bg-muted px-1 font-mono text-[13px] text-foreground";
const DESTRUCTIVE = "bg-danger text-white hover:bg-danger/90";

export type AddRoleSheetProps = {
  app: AppRow;
  // tools are this server's tools. A server admin cannot read the tools
  // list, so the caller falls back to the names the server row carries.
  tools: ToolRow[];
  // role is the role being edited, absent when the sheet adds one.
  role?: RoleRow | null;
  // roles are the roles this server owns, which a new role's name is
  // checked against at the field.
  roles?: RoleRow[];
  // binding is the access row an edit replaces, null when the row could
  // not be read or does not exist, in which case the save writes a new one.
  binding?: BindingRow | null;
  // globalAdmin says the session holds the apps grant, so the glob is
  // offered; a server admin only narrows a role that has it.
  globalAdmin: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
};

const NEW_READ: AccessRead = { initial: emptyPlan(), fixed: {}, taken: [], unread: { own: false, others: false }, text: null };

// readFor reads what Edit tools opens on: the role's own set, the server's
// preview of the role, and the other sets that hold one of its tools.
// saved says the own set holds a saved edit nobody published.
async function readFor(role: RoleRow, app: AppRow, names: string[], row: BindingRow): Promise<{ read: AccessRead; saved: boolean }> {
  const [own, answer] = await Promise.all([readOwnSet(accessSetName(role.name)), catalogPreview(role.name, app.name).catch(() => null)]);
  const preview: Record<string, PreviewEntry> = {};
  for (const e of answer?.entries || []) if (e.tool) preview[e.tool] = e;
  const others = heldSets(preview).filter((n) => n !== accessSetName(role.name));
  const texts: Record<string, string | null> = {};
  await Promise.all(others.map(async (n) => { texts[n] = (await readSetText(n)) ?? null; }));
  return { read: accessRead(role.name, app.name, names, row, own.text, answer ? preview : null, texts), saved: own.saved };
}

export function AddRoleSheet({ app, tools, role, roles, binding, globalAdmin, open, onOpenChange, onDone }: AddRoleSheetProps) {
  const editing = !!role;
  const [suffix, setSuffix] = React.useState("");
  const [description, setDescription] = React.useState("");
  const [plan, setPlan] = React.useState<Plan>(emptyPlan);
  const [read, setRead] = React.useState<AccessRead | null>(null);
  const [approvers, setApprovers] = React.useState<RoleRow[]>([]);
  // busy is the foot at work, which keeps the sheet open.
  const [busy, setBusy] = React.useState(false);
  const [note, setNote] = React.useState<string | null>(null);
  // miss is what a save found missing at the name field, cleared as the
  // suffix is typed.
  const [miss, setMiss] = React.useState<"name" | "taken" | null>(null);
  const [saved, setSaved] = React.useState(false);
  const suffixRef = React.useRef<HTMLInputElement>(null);
  const roleKey = role ? role.id + ":" + (role.tools || []).join(",") : "";
  const names = tools.map((t) => t.name);
  // A session that may not publish policy picks the tools and reads the
  // Policy column; it is asked only while the sheet is open.
  const readOnly = open && !mayWritePolicy();

  // Every open starts from the role the sheet was opened over, so a
  // cancelled edit never leaves its ticks behind on the next one. An edit
  // reads the role's own set first; a row that could not be read stands in
  // as the role's own tool list.
  React.useEffect(() => {
    if (!open) return undefined;
    let alive = true;
    setSuffix("");
    setNote(null);
    setMiss(null);
    setSaved(false);
    setDescription(role ? role.description || "" : "");
    const seed = (r: AccessRead) => {
      setRead(r);
      setPlan(!mayWritePolicy() && r.initial.reach === "today" ? { ...r.initial, reach: "tick" } : r.initial);
    };
    if (!role) seed(NEW_READ);
    else {
      setRead(null);
      const row = binding || { id: "", app: app.name, role: role.name, tools: role.tools || [] };
      void readFor(role, app, names, row).then((r) => { if (alive) { seed(r.read); setSaved(r.saved); } });
    }
    return () => { alive = false; };
  }, [open, roleKey]); // eslint-disable-line react-hooks/exhaustive-deps

  // The approver roles are the pools the editor offers, read once a sheet
  // opens for a session that may publish policy.
  React.useEffect(() => {
    if (!open || readOnly || approvers.length) return undefined;
    let alive = true;
    listRoles().then(
      (rs) => { if (alive) setApprovers((rs || []).filter((r) => r.kind === "approver")); },
      () => undefined,
    );
    return () => { alive = false; };
  }, [open, readOnly, approvers.length]);

  const matchers = grantMatchers(plan, names);
  const name = editing && role ? role.name : roleNameOf(app.name, suffix);
  const preview = previewParts(name);
  const before = binding ? (isGlob(binding.tools) ? ["*"] : (binding.tools || []).slice().sort()) : null;
  const sameTools = !!before && JSON.stringify(before) === JSON.stringify(matchers);
  const sameCalls = !!read && sameRules(read.initial, plan, app.name, names, name);
  const retiring = !!read && retires(read, plan, app.name, names, name);
  // A new role's sentence and rules wait for its name.
  const unnamed = !editing && !foldName(suffix);
  // taken says a role of this server already has the new role's name, which
  // a draft put would change rather than make a new role.
  const taken = !editing && (roles || []).some((r) => (r.name || "").toLowerCase() === name);
  // written is what a save would store for this server in the role's own
  // set, so the fold shows the kept ids and reasons.
  const written = read && typeof read.text === "string" ? storedRules(read.text, plan, app.name, names, name) : undefined;
  // changed says a save would store something the sheet did not open on: a
  // new role's name or description, its tools, or what a call does.
  const changed = !!read && ((!editing && !!(suffix.trim() || description.trim())) || !sameCalls || JSON.stringify(grantMatchers(read.initial, names)) !== JSON.stringify(matchers));

  const close = () => { if (!busy) onOpenChange(false); };

  // build keeps both saves clickable, and a click
  // that cannot save says what is missing where the fix is. A new role is
  // its Role document, which names this server as its owner, and its own set
  // when a call requires approval. An edit is the role's live export with
  // the one row changed, read only when the tools change, and its own set
  // when the rules change, and it asks before the set is removed.
  const build = async (confirmed: boolean): Promise<Built> => {
    if (!read) return null;
    if (unnamed || taken) {
      setMiss(unnamed ? "name" : "taken");
      suffixRef.current?.focus();
      return null;
    }
    if (!matchers.length) {
      setNote(PICK_A_TOOL);
      return null;
    }
    const input: GrantInput = { role: { name, id: role ? role.id : undefined }, app, tools, plan };
    if (!role) {
      const made = await newRoleItems({ name, kind: "application", description: description.trim(), implies: [] }, input);
      return "error" in made ? { tone: "refused", lines: [NOTHING_SAVED, made.error] } : made.items;
    }
    if (sameTools && sameCalls) {
      setNote(NOTHING_CHANGED);
      return null;
    }
    if (!confirmed && retiring) return "ask";
    const text = sameTools ? "" : await liveExport(role);
    if (typeof text !== "string") return text;
    const answer = await accessItems({ ...input, replace: sameTools ? null : binding, keep: sameTools, before: read.initial }, text);
    return "error" in answer ? { tone: "refused", lines: [NOTHING_SAVED, answer.error] } : answer.items;
  };

  // The toasts after a publish are New role's and the role page's.
  const toast = !editing ? liveRole(name) : sameTools ? undefined : accessToast(name, app.name);

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) close(); }}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-3xl" data-add-role={app.name}>
        <SheetHeader className="border-b border-border pr-12">
          <SheetTitle className="text-lg leading-snug">{editing && role ? editTitle(role.name) : addTitle(app.name)}</SheetTitle>
          <SheetDescription>{editing ? EDIT_LEDE : ADD_LEDE}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 py-4">
          {saved && <p role="alert" className="m-0 max-w-[75ch] text-[13px] text-danger" data-saved-edit>{savedEdit(accessSetName(name))}</p>}
          <Field id="ar-suffix" label={NAME} error={miss === "name" ? NAME_MISSING : miss === "taken" ? PICK_ANOTHER : undefined}>
            {editing ? (
              <p className="m-0 font-mono text-sm text-foreground" data-role-name>{name}</p>
            ) : (
              <div className="flex max-w-[420px] items-center rounded-md border border-border bg-background">
                <span className="pl-2.5 font-mono text-[13px] whitespace-nowrap text-muted-foreground">{rolePrefix(app.name)}</span>
                <Input
                  id="ar-suffix"
                  ref={suffixRef}
                  aria-label={NAME_SUFFIX}
                  value={suffix}
                  autoFocus
                  spellCheck={false}
                  onChange={(e) => { setMiss(null); setSuffix(e.target.value); }}
                  className="h-9 border-0 bg-transparent font-mono shadow-none focus-visible:ring-0"
                />
              </div>
            )}
            {taken && <p className="m-0 max-w-[75ch] text-[13px] leading-snug text-danger" data-name-check="error">{nameTaken(name)}</p>}
            <p className={cn(HINT, "m-0")} data-name-preview>
              {preview.lead}<span className={CODE}>{preview.stored}</span>{preview.mid}<span className={CODE}>{preview.ar}</span>{preview.end}
              <br />
              <span className="font-mono text-xs">{cliCreateRole(name, app.name, matchers)}</span>
            </p>
          </Field>

          {!editing && (
            <Field id="ar-description" label={DESCRIPTION} hint={DESCRIPTION_HINT}>
              <Input id="ar-description" value={description} onChange={(e) => setDescription(e.target.value)} className="w-[420px] max-w-full" />
            </Field>
          )}

          {read ? (
            <AccessEditor
              role={name}
              app={app}
              tools={tools}
              plan={plan}
              onPlan={(update) => { setNote(null); setPlan(update); }}
              fixed={read.fixed}
              approvers={approvers}
              ownSet={accessSetName(name)}
              taken={read.taken}
              stored={binding}
              readOnly={readOnly}
              namesOnly={!globalAdmin}
              unread={read.unread}
              written={written}
              unnamed={unnamed}
            />
          ) : (
            <p role="status" className="m-0 text-[13px] text-muted-foreground" data-reading>{READING_POLICY}</p>
          )}
          {note ? (
            <p role="status" className="m-0 text-[13px] text-danger" data-save-note>{note}</p>
          ) : sameTools && read && !sameCalls && !retiring ? (
            <p role="status" className="m-0 text-[13px] text-muted-foreground" data-save-note>{UNCHANGED_TOOLS}</p>
          ) : null}
        </div>

        <SheetFooter className="mt-0 border-t border-border">
          <RoleSaves
            role={name}
            toast={toast}
            creates={editing ? undefined : { object: "Role/" + name, taken: nameTaken(name) + " " + PICK_ANOTHER }}
            build={build}
            ask={(asked, confirm, cancel, publish) => (
              <AlertDialog open={asked} onOpenChange={(o) => { if (!o) cancel(); }}>
                <AlertDialogContent className="sm:max-w-[560px]" data-retire-dialog>
                  <AlertDialogHeader>
                    <AlertDialogTitle>{removeSetTitle(accessSetName(name))}</AlertDialogTitle>
                    <AlertDialogDescription>{removeSetBody(name, app.name, publish)}</AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
                    <AlertDialogAction className={DESTRUCTIVE} onClick={confirm}>{REMOVE_SET}</AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
            onPublished={() => { onOpenChange(false); onDone(); }}
            onCancel={close}
            changed={changed}
            edits={JSON.stringify([suffix, description, plan])}
            onBusy={setBusy}
          />
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}
