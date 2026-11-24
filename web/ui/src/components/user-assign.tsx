import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { KindTile } from "@/components/kind-icon";
import { MARKS } from "@/components/role-kind";
import { MUTED } from "@/components/user-parts";
import { type ApiError, type AssignmentRow, type BindingRow, type PreviewMachine, type RoleRow, type ToolRow, catalogPreview, grantRole } from "@/lib/api";
import { CANCEL, CATEGORY, GRANT_HELP, READING_ROLES, SEARCH_ROLES, grantBody as gainsBody, kindOf } from "@/lib/role-words";
import { refused } from "@/lib/say";
import { givesLine, offerGroups } from "@/lib/user-roles";
import { ASSIGN_A_ROLE, GRANT_MISSING, IDM_OWNS, NO_ROLE_MATCHES, assignName, assignTitle, machineGives, nothingToGrant } from "@/lib/user-words";

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  userID: string;
  name: string;
  // machine marks an AI agent or a service, which the console does not
  // make an approver. scim marks a user the identity manager owns.
  machine: boolean;
  scim: boolean;
  // servers says the reader's role includes the MCP servers area. Only
  // then does the dialog read what a picked role gives on a server on a
  // person's machine.
  servers: boolean;
  // roles is the catalog, null when its read failed, and reading says its
  // read is still out. bindings and tools are null when they were not
  // read, which leaves an application role's line to its description.
  roles: RoleRow[] | null;
  reading?: boolean;
  assignments: AssignmentRow[];
  bindings: BindingRow[] | null;
  tools: ToolRow[] | null;
  onAssigned: (role: string) => void;
  // onClosed runs once the dialog is gone, where the page puts the focus
  // back on what opened it.
  onClosed?: () => void;
};

// UserAssign is the Assign a role dialog: the roles the user does not hold
// directly, grouped by kind, each with the line that says what it gives,
// and the sentence of what the user gains once one is picked.
export function UserAssign(props: Props) {
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="flex max-h-[calc(100dvh-32px)] flex-col sm:max-w-[560px]" data-assign-dialog onCloseAutoFocus={(e) => { if (props.onClosed) { e.preventDefault(); props.onClosed(); } }}>
        {props.open && <Body {...props} />}
      </DialogContent>
    </Dialog>
  );
}

function Body({ onOpenChange, userID, name, machine, scim, servers, roles, reading, assignments, bindings, tools, onAssigned }: Props) {
  const [needle, setNeedle] = React.useState("");
  const [pick, setPick] = React.useState("");
  const [miss, setMiss] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [onMachine, setOnMachine] = React.useState<{ role: string; servers: PreviewMachine[] } | null>(null);
  const list = React.useRef<HTMLDivElement>(null);
  const catalog = roles || [];
  const offered = offerGroups(catalog, assignments, "");
  const groups = offerGroups(catalog, assignments, needle);
  const role = catalog.find((r) => r.id === pick) || null;
  const picked = role ? role.name : "";

  // Each pick reads the servers on a person's machine that the role's
  // policies name. Nothing stands for them while the read runs or after it
  // failed, and the answer of an earlier pick is never shown for this one.
  React.useEffect(() => {
    setOnMachine(null);
    if (!picked || !servers) return;
    let alive = true;
    catalogPreview(picked).then(
      (answer) => { if (alive) setOnMachine({ role: picked, servers: answer.machine || [] }); },
      () => {},
    );
    return () => { alive = false; };
  }, [picked, servers]);
  const gives = onMachine && onMachine.role === picked ? onMachine.servers : [];

  const assign = async () => {
    if (!role) {
      setMiss(true);
      (list.current?.querySelector("[role=radio]:not([disabled])") as HTMLElement | null)?.focus();
      return;
    }
    setBusy(true);
    setProblem(null);
    try {
      await grantRole(userID, role.id);
      onAssigned(role.name);
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <DialogHeader>
        <DialogTitle>{assignTitle(name)}</DialogTitle>
      </DialogHeader>
      <Input value={needle} onChange={(e) => setNeedle(e.target.value)} placeholder={SEARCH_ROLES} aria-label={SEARCH_ROLES} className="h-9" />
      <div ref={list} className="-mx-1 min-h-0 flex-1 overflow-y-auto px-1">
        {offered.length === 0 ? (
          <p className="m-0 text-dense text-text-2" role={reading ? "status" : undefined} data-nothing-to-offer>{reading ? READING_ROLES : nothingToGrant(catalog.length === 0, roles === null)}</p>
        ) : groups.length === 0 ? (
          <p className="m-0 text-dense text-text-2" data-no-match>{NO_ROLE_MATCHES}</p>
        ) : (
          <RadioGroup value={pick} onValueChange={(v) => { setPick(v); setMiss(false); }} aria-label={ASSIGN_A_ROLE} className="gap-4">
            {groups.map((g) => (
              <div key={g.kind} className="flex flex-col gap-1.5" data-role-group={g.kind}>
                <div className="flex items-center gap-2">
                  <KindTile kind={MARKS[g.kind]} size="sm" />
                  <span className="text-caption font-semibold text-muted-foreground">{CATEGORY[g.kind].title}</span>
                </div>
                {g.roles.map((r) => {
                  const line = givesLine(r, machine, bindings, tools);
                  const text = line?.text || "";
                  const id = "assign-" + r.id;
                  // A role the console does not assign keeps its words at
                  // full opacity: its name muted, its reason in the second
                  // text colour, both readable.
                  return (
                    <label key={r.id} htmlFor={id} className="flex items-start gap-2.5 rounded-md border border-border px-3 py-2 has-[[data-state=checked]]:border-link" data-role-option={r.name}>
                      <RadioGroupItem id={id} value={r.id} disabled={!!line?.disabled} className="mt-0.5" />
                      <span className="flex min-w-0 flex-col gap-0.5">
                        <span className={"font-mono text-dense break-all " + (line?.disabled ? "text-muted-foreground" : "text-foreground")}>{r.name}</span>
                        {text && <span className="text-caption text-text-2">{text}</span>}
                      </span>
                    </label>
                  );
                })}
              </div>
            ))}
          </RadioGroup>
        )}
      </div>
      {miss && <p className="m-0 text-caption text-danger" role="alert">{GRANT_MISSING}</p>}
      {role && (
        <div className="flex flex-col gap-1">
          <p className="m-0 flex items-center gap-1.5 text-dense text-foreground" data-gains>
            {gainsBody(kindOf(role), name, role.name)}
            <HelpTip label={assignName(role.name)} text={GRANT_HELP} />
          </p>
          {gives.map((m) => <p key={m.app} className={"m-0 " + MUTED} data-machine-gives={m.app}>{machineGives(m.app, (m.tools || []).length, !!m.open)}</p>)}
          {scim && <p className="m-0 text-caption text-text-2">{IDM_OWNS}</p>}
        </div>
      )}
      {problem && <RefusedError subject={role ? assignName(role.name) : ASSIGN_A_ROLE} message={problem} />}
      <DialogFooter>
        <Button variant="outline" onClick={() => onOpenChange(false)}>{CANCEL}</Button>
        <Button disabled={busy} onClick={() => void assign()}>{busy && <Loader2Icon className="animate-spin" />}{role ? assignName(role.name) : ASSIGN_A_ROLE}</Button>
      </DialogFooter>
    </>
  );
}
