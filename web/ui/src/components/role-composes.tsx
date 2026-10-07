import * as React from "react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { FetchError, RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { RoleKindBadge } from "@/components/role-kind";
import { type Built, RoleSaves, liveExport } from "@/components/role-saves";
import { ListRow, Section } from "@/components/sheet-parts";
import { RoleChip } from "@/components/status-badge";
import { type ApiError, type BindingRow, type ImplicationRow, type RoleRow, catalogPreview, removeBinding, removeImplication } from "@/lib/api";
import { notify } from "@/lib/notify";
import { roleItem } from "@/lib/role-draft";
import { refused } from "@/lib/say";
import { CANCEL, COMPOSES_EMPTY, COMPOSE_HELP, COMPOSE_MISSING, COMPOSE_PICK, COMPOSE_ROLE, HOLDERS_REACH, HOLDERS_REACH_HELP, LEGACY_ROWS, NO_COMPOSABLE, REACH_EMPTY, REACH_UNREAD, REMOVE, REMOVE_ACCESS, SUBJECT_COMPOSES, TAB, UNCOMPOSE, accessRemovedToast, composeAdminBody, composeBody, composeTitle, composedToast, isComposable, isGlob, isMinted, reachPill, removeAccessBody, removeAccessTitle, uncomposeBody, uncomposeTitle, uncomposedToast } from "@/lib/role-words";

// The Composes tab of a business role: the
// application roles it holds, and what a holder reaches through them. Tools
// arrive through those roles, so nothing here edits a tool.

// The preview statuses that mean the tool exists for the session, whether
// or not a rule gates it. A tool no grant reaches is not counted.
const REACHED = ["visible", "approve_gated", "hidden_policy"];

type Props = {
  role: RoleRow;
  // roles is the catalog: it names the kind of each composed role and the
  // application roles the dialog offers.
  roles: RoleRow[];
  implications: ImplicationRow[];
  // bindings are every access row; a business role's own rows predate the
  // compose-only rule and are listed so they can be removed.
  bindings: BindingRow[];
  composeOpen: boolean;
  onComposeOpenChange: (open: boolean) => void;
  onChanged: () => void;
  problem: string | null;
  lastRead: Date | null;
};

export function RoleComposes({ role, roles, implications, bindings, composeOpen, onComposeOpenChange, onChanged, problem, lastRead }: Props) {
  const [previews, setPreviews] = React.useState<Record<string, Record<string, number> | null>>({});
  const [ask, setAsk] = React.useState<ImplicationRow | null>(null);
  const [drop, setDrop] = React.useState<BindingRow | null>(null);
  const [refusal, setRefusal] = React.useState<{ subject: string; message: string } | null>(null);

  const names = implications.map((i) => i.implies_name).join(",");

  // What a holder reaches arrives through the composed roles, so each one
  // is asked what its own sessions get, folded to a count per server.
  React.useEffect(() => {
    let alive = true;
    for (const other of names ? names.split(",") : []) {
      catalogPreview(other).then(
        (answer) => {
          if (!alive) return;
          const counts: Record<string, number> = {};
          for (const e of answer?.entries || []) if (e.app && REACHED.includes(e.status)) counts[e.app] = (counts[e.app] || 0) + 1;
          setPreviews((p) => ({ ...p, [other]: counts }));
        },
        () => { if (alive) setPreviews((p) => ({ ...p, [other]: null })); },
      );
    }
    return () => { alive = false; };
  }, [names]);

  const kindOfName = (name: string) => roles.find((r) => r.name === name) || { kind: "application" };
  const legacy = bindings.filter((b) => b.role === role.name);
  const unread = implications.some((i) => previews[i.implies_name] === null);
  const pills: { server: string; via: string; tools: number }[] = [];
  for (const i of implications) {
    const counts = previews[i.implies_name];
    if (!counts) continue;
    for (const server of Object.keys(counts).sort()) pills.push({ server, via: i.implies_name, tools: counts[server] });
  }

  const uncompose = async () => {
    if (!ask) return;
    setRefusal(null);
    try {
      await removeImplication(role.id, ask.implies_id);
      notify.ok(uncomposedToast(role.name, ask.implies_name));
      setAsk(null);
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: UNCOMPOSE, message: refused(err) });
    }
  };

  const removeAccess = async () => {
    if (!drop) return;
    setRefusal(null);
    try {
      await removeBinding(drop.id);
      notify.ok(accessRemovedToast(role.name, drop.app));
      setDrop(null);
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setRefusal({ subject: REMOVE_ACCESS, message: refused(err) });
    }
  };

  return (
    <div className="flex flex-col gap-4">
      {problem && <FetchError subject={SUBJECT_COMPOSES} detail={problem} lastRead={lastRead} />}
      {implications.length === 0 && !problem && <p className="text-sm text-muted-foreground">{COMPOSES_EMPTY}</p>}
      {implications.map((i) => (
        <ListRow
          key={i.implies_id}
          actions={<Button variant="outline" size="sm" className="border-danger text-danger hover:bg-danger-bg" onClick={() => setAsk(i)}>{REMOVE}</Button>}
        >
          <span data-implies={i.implies_name}><RoleChip name={i.implies_name} /></span>
          <RoleKindBadge role={kindOfName(i.implies_name)} />
        </ListRow>
      ))}

      <Section title={HOLDERS_REACH} action={<HelpTip label={HOLDERS_REACH} text={HOLDERS_REACH_HELP} />}>
        {implications.length === 0 && <p className="text-sm text-muted-foreground">{REACH_EMPTY}</p>}
        <div className="flex flex-wrap gap-1.5">
          {pills.map((p) => {
            // The access row behind the pill says whether the tools are
            // stored as a glob, which the pill reads as tools added later.
            const b = bindings.find((x) => x.role === p.via && x.app === p.server);
            const glob = !!b && isGlob(b.tools);
            return (
              <span key={p.server + "/" + p.via} className="inline-flex items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-sm" data-reach-pill={p.server}>
                <span className="font-mono text-foreground">{p.server}</span>
                <span className="text-[13px] text-muted-foreground">{reachPill(p.tools, p.via, glob)}</span>
              </span>
            );
          })}
        </div>
        {unread && <p className="text-[13px] text-muted-foreground">{REACH_UNREAD}</p>}
      </Section>

      {legacy.length > 0 && (
        <Section title={TAB.access}>
          <p className="text-[13px] text-warn">{LEGACY_ROWS}</p>
          {legacy.map((b) => (
            <ListRow
              key={b.id}
              actions={<Button variant="outline" size="sm" className="border-danger text-danger hover:bg-danger-bg" onClick={() => setDrop(b)}>{REMOVE_ACCESS}</Button>}
            >
              <span className="font-mono text-foreground" data-legacy={b.app}>{b.app}</span>
            </ListRow>
          ))}
        </Section>
      )}

      <ComposeDialog
        role={role}
        roles={roles}
        implications={implications}
        open={composeOpen}
        onOpenChange={onComposeOpenChange}
        onComposed={onChanged}
      />

      <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o) { setAsk(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{ask ? uncomposeTitle(ask.implies_name) : ""}</AlertDialogTitle>
            <AlertDialogDescription>{ask ? uncomposeBody(role.name, ask.implies_name) : ""}</AlertDialogDescription>
          </AlertDialogHeader>
          {refusal?.subject === UNCOMPOSE && <RefusedError subject={UNCOMPOSE} message={refusal.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={(e) => { e.preventDefault(); void uncompose(); }}>{UNCOMPOSE}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!drop} onOpenChange={(o) => { if (!o) { setDrop(null); setRefusal(null); } }}>
        <AlertDialogContent className="sm:max-w-[560px]">
          <AlertDialogHeader>
            <AlertDialogTitle>{drop ? removeAccessTitle(drop.app) : ""}</AlertDialogTitle>
            <AlertDialogDescription>{drop ? removeAccessBody(role.name, drop.app) : ""}</AlertDialogDescription>
          </AlertDialogHeader>
          {refusal?.subject === REMOVE_ACCESS && <RefusedError subject={REMOVE_ACCESS} message={refusal.message} />}
          <AlertDialogFooter>
            <AlertDialogCancel>{CANCEL}</AlertDialogCancel>
            <AlertDialogAction className="bg-danger text-white hover:bg-danger/90" onClick={(e) => { e.preventDefault(); void removeAccess(); }}>{REMOVE_ACCESS}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

// ComposeDialog picks the role to compose. Application roles are offered,
// and the minted admin role of an MCP server, so a team over several servers
// is one business role. The server refuses an edge that closes a cycle. Its
// foot saves the role's live document with the picked role added to what
// it implies.
function ComposeDialog({ role, roles, implications, open, onOpenChange, onComposed }: { role: RoleRow; roles: RoleRow[]; implications: ImplicationRow[]; open: boolean; onOpenChange: (o: boolean) => void; onComposed: () => void }) {
  const [pick, setPick] = React.useState("");
  const [missing, setMissing] = React.useState(false);
  const trigger = React.useRef<HTMLButtonElement>(null);

  React.useEffect(() => {
    if (open) return;
    setPick("");
    setMissing(false);
  }, [open]);

  const held = implications.map((i) => i.implies_id);
  const free = roles.filter((r) => isComposable(r) && r.id !== role.id && !held.includes(r.id));
  const other = free.find((r) => r.id === pick);

  // build reads the role's live export now and adds the picked role to
  // what it implies.
  const build = async (): Promise<Built> => {
    if (!other) {
      setMissing(true);
      trigger.current?.focus();
      return null;
    }
    const text = await liveExport(role);
    if (typeof text !== "string") return text;
    return [roleItem(text, (s) => { s.implies = (s.implies || []).concat(other.name).sort(); })];
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>{composeTitle(role.name)}</DialogTitle>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          <div className="flex items-center gap-1.5">
            <Select value={pick} onValueChange={(v) => { setPick(v); setMissing(false); }}>
              <SelectTrigger ref={trigger} aria-label={COMPOSE_PICK} className="h-9 w-[280px]" data-compose-pick>
                <SelectValue placeholder={COMPOSE_PICK} />
              </SelectTrigger>
              <SelectContent>
                {free.map((r) => <SelectItem key={r.id} value={r.id} className="font-mono">{r.name}</SelectItem>)}
              </SelectContent>
            </Select>
            <HelpTip label={COMPOSE_ROLE} text={COMPOSE_HELP} />
          </div>
          {free.length === 0 && <p className="text-sm text-muted-foreground">{NO_COMPOSABLE}</p>}
          {missing && <p className="text-[13px] text-danger">{COMPOSE_MISSING}</p>}
          {other && <p className="text-sm text-text-2">{isMinted(other) ? composeAdminBody(role.name, other.name) : composeBody(role.name, other.name)}</p>}
        </div>
        <RoleSaves
          role={role.name}
          toast={other ? composedToast(role.name, other.name) : undefined}
          build={build}
          onPublished={() => { onOpenChange(false); onComposed(); }}
          onCancel={() => onOpenChange(false)}
        />
      </DialogContent>
    </Dialog>
  );
}
