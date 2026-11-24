import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { DANGER_FILL } from "@/components/ui/button";
import { RefusedError } from "@/components/error-state";
import { Tag } from "@/components/chip";
import { KIND_ICON } from "@/components/kind-icon";
import { type ApiError, type AssignmentRow, type RoleEntraGroup, type RoleRow, type UserRow, listUsers, query, recreateEntraGroup, releaseEntraRemovals } from "@/lib/api";
import { APPLY_REMOVALS, KEEP_LABEL, RECREATE, RECREATE_TITLE, appliedToast, applyTitle, loseLabel, recreateBody, recreatedToast } from "@/lib/entra-words";
import { notify } from "@/lib/notify";
import { CANCEL } from "@/lib/role-words";
import { refused } from "@/lib/say";
import { holdingOf } from "@/lib/user-access";
import { userKind } from "@/lib/user-look";

type Props = {
  ask: "apply" | "recreate" | null;
  role: RoleRow;
  group: RoleEntraGroup;
  // name is the group's name in Entra.
  name: string;
  roles: RoleRow[];
  assignments: AssignmentRow[] | null;
  onClose: () => void;
  onDone: () => void;
};

// split names who loses the role when its group is created again, the
// holders from Entra alone, and who keeps it, assigned another way or
// through a role that composes it.
function split(role: RoleRow, assignments: AssignmentRow[], roles: RoleRow[]): { lose: string[]; keep: string[] } {
  const fromEntra = assignments.filter((a) => a.role_id === role.id && a.origin === "entra").map((a) => a.subject_id);
  const others = assignments.filter((a) => !(a.role_id === role.id && a.origin === "entra"));
  const lose: string[] = [];
  const keep: string[] = [];
  for (const id of [...new Set(fromEntra)]) {
    const h = holdingOf(id, role, others, roles);
    (h.origins.length || h.through.length ? keep : lose).push(id);
  }
  return { lose, keep };
}

// EntraConfirm is the confirm of the block's two writes: Apply the
// removals of a held group, and Create the group again for a deleted one.
// A refusal shows inside it with the server's own sentence.
export function EntraConfirm({ ask, role, group, name, roles, assignments, onClose, onDone }: Props) {
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);
  const [names, setNames] = React.useState<Map<string, UserRow>>(new Map());

  // The holders who lose or keep the role are named by their sign-in name
  // with their kind's icon, read once when the confirm opens. An ID the
  // read did not answer shows as it is.
  React.useEffect(() => {
    setProblem(null);
    if (ask !== "recreate") return;
    let alive = true;
    listUsers(query({ role: role.name, limit: 500 })).then(
      (page) => { if (alive) setNames(new Map((page.items || []).map((u) => [u.id, u]))); },
      () => { /* the IDs stand in for the names */ },
    );
    return () => { alive = false; };
  }, [ask, role.name]);

  const run = async () => {
    setBusy(true);
    setProblem(null);
    try {
      if (ask === "apply") {
        await releaseEntraRemovals(role.id);
        notify.ok(appliedToast(role.name));
      } else {
        await recreateEntraGroup(role.id);
        notify.ok(recreatedToast(name));
      }
      onClose();
      onDone();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  const who = ask === "recreate" && assignments ? split(role, assignments, roles) : null;
  const tags = (ids: string[]) => (
    <div className="flex flex-wrap gap-1.5">
      {ids.map((id) => {
        const u = names.get(id);
        const kind = u ? userKind(u) : "person";
        return <Tag key={id} icon={KIND_ICON[kind]} agent={kind === "agent"} data-entra-holder={u ? u.username : id}>{u ? u.username : id}</Tag>;
      })}
    </div>
  );
  const verb = ask === "apply" ? APPLY_REMOVALS : RECREATE;

  return (
    <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !busy) onClose(); }}>
      <AlertDialogContent className="sm:max-w-[560px]" data-entra-confirm={ask || undefined}>
        <AlertDialogHeader>
          <AlertDialogTitle>{ask === "apply" ? applyTitle(role.name) : RECREATE_TITLE}</AlertDialogTitle>
          <AlertDialogDescription>{ask === "apply" ? group.error || "" : recreateBody(name)}</AlertDialogDescription>
        </AlertDialogHeader>
        {who && who.lose.length > 0 && (
          <div className="flex flex-col gap-1.5" data-entra-lose>
            <p className="m-0 text-caption text-muted-foreground">{loseLabel(role.name)}</p>
            {tags(who.lose)}
          </div>
        )}
        {who && who.keep.length > 0 && (
          <div className="flex flex-col gap-1.5" data-entra-keep>
            <p className="m-0 text-caption text-muted-foreground">{KEEP_LABEL}</p>
            {tags(who.keep)}
          </div>
        )}
        {problem && <RefusedError subject={verb} message={problem} />}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
          <AlertDialogAction className={ask === "apply" ? DANGER_FILL : undefined} disabled={busy} onClick={(e) => { e.preventDefault(); void run(); }}>
            {busy && <Loader2Icon className="animate-spin" />}{verb}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
