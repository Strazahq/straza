import * as React from "react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { ListRow, Section } from "@/components/sheet-parts";
import { type ApiError, type PackRow, type RoleRow, bindPack, unbindPack } from "@/lib/api";
import { notify } from "@/lib/notify";
import { refused } from "@/lib/say";
import { BIND_PACK, CANCEL, NO_PACK_BOUND, PACKS_HELP, PACK_MISSING, PACK_PICK, TAB, UNBIND, UNBIND_PACK, boundToast, packVersion, unbindBody, unbindTitle, unboundToast } from "@/lib/role-words";

// The Packs tab: the reviewed context sessions
// holding this role receive at check-in. The tab exists only where a pack
// exists, so the page decides whether to render it at all.

type Props = {
  role: RoleRow;
  packs: PackRow[];
  // onChanged tells the page to read the packs again.
  onChanged: () => void;
};

export function RolePacks({ role, packs, onChanged }: Props) {
  const [pick, setPick] = React.useState("");
  const [missing, setMissing] = React.useState(false);
  const [ask, setAsk] = React.useState<PackRow | null>(null);
  const [problem, setProblem] = React.useState<{ subject: string; message: string } | null>(null);

  const bound = packs.filter((p) => (p.bindings || []).some((b) => b.role_id === role.id));
  const free = packs.filter((p) => !bound.includes(p));

  const bind = async () => {
    if (!pick) { setMissing(true); return; }
    const pack = packs.find((p) => p.id === pick);
    if (!pack) return;
    setProblem(null);
    try {
      await bindPack(pack.id, role.id);
      notify.ok(boundToast(role.name, pack.name));
      setPick("");
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setProblem({ subject: BIND_PACK, message: refused(err) });
      notify.failed(refused(err));
    }
  };

  const unbind = async () => {
    if (!ask) return;
    setProblem(null);
    try {
      await unbindPack(ask.id, role.id);
      notify.ok(unboundToast(role.name, ask.name));
      setAsk(null);
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 401) return;
      setProblem({ subject: UNBIND_PACK, message: refused(err) });
    }
  };

  return (
    <Section title={TAB.packs} action={<HelpTip label={TAB.packs} text={PACKS_HELP} />}>
      {bound.length === 0 && <p className="text-sm text-muted-foreground">{NO_PACK_BOUND}</p>}
      {bound.map((p) => (
        <ListRow
          key={p.id}
          actions={<Button variant="outline" size="sm" className="border-danger text-danger hover:bg-danger-bg" onClick={() => setAsk(p)}>{UNBIND}</Button>}
        >
          <span className="font-semibold text-foreground" data-pack={p.name}>{p.name}</span>
          {p.version && <span className="text-[13px] text-muted-foreground">{packVersion(p.version)}</span>}
        </ListRow>
      ))}

      {problem && problem.subject === BIND_PACK && <RefusedError subject={BIND_PACK} message={problem.message} />}

      <div className="flex flex-wrap items-center gap-2">
        <Select value={pick} onValueChange={(v) => { setPick(v); setMissing(false); }}>
          <SelectTrigger aria-label={PACK_PICK} className="h-9 w-[260px]" data-pack-pick>
            <SelectValue placeholder={PACK_PICK} />
          </SelectTrigger>
          <SelectContent>
            {free.map((p) => <SelectItem key={p.id} value={p.id}>{p.name}</SelectItem>)}
          </SelectContent>
        </Select>
        <Button variant="outline" size="sm" onClick={() => void bind()}>{BIND_PACK}</Button>
      </div>
      {missing && <p className="text-[13px] text-danger">{PACK_MISSING}</p>}

      <AlertDialog open={!!ask} onOpenChange={(open) => { if (!open) { setAsk(null); setProblem(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{ask ? unbindTitle(ask.name) : ""}</AlertDialogTitle>
            <AlertDialogDescription>{ask ? unbindBody(role.name, ask.name) : ""}</AlertDialogDescription>
          </AlertDialogHeader>
          {problem && problem.subject === UNBIND_PACK && <RefusedError subject={UNBIND_PACK} message={problem.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={(e) => { e.preventDefault(); void unbind(); }}>{UNBIND_PACK}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Section>
  );
}
