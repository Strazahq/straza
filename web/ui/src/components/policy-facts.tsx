import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { HelpTip } from "@/components/help-tip";
import { WordBadge } from "@/components/users-table";
import type { RoleRow } from "@/lib/api";
import { type Doc, type SetView, setCapture, setMatchRoles, setPriority } from "@/lib/policy-model";
import {
  APPLIES_FACT_HELP, APPLY_TO_PAGE, CANCEL, CHANGE_APPLIES, CHANGE_PRIORITY, CHANGE_RECORDING, EVERYONE, EVERYONE_MEANS,
  FACT, NO_RECORDING, NOTHING_TICKED, PRIORITY_HELP, PRIORITY_LINE, REC, REC_MASKED, REC_VERBATIM, RECORDING_CHOICE,
  RECORDING_FACT_HELP, TOUCHED, recWord,
} from "@/lib/policy-words";
import { holders } from "@/lib/words";

// The three facts that open a policy's Rules tab:
// who it applies to, whether it records, and its priority. Each fact opens
// a small sheet that changes the page's document; nothing here reaches the
// server, which is what the unpublished bar above says.

type Fact = "applies" | "recording" | "priority";

type Props = {
  view: SetView;
  // roles are every role the deployment holds; the Applies to sheet offers
  // the application ones, the kind a policy names.
  roles: RoleRow[];
  onEdit: (touched: string, fn: (doc: Doc) => void) => void;
};

// RoleChips names who the policy applies to: its roles, or Everyone when
// it names none.
export function RoleChips({ roles }: { roles: string[] }) {
  if (roles.length === 0) return <WordBadge word={EVERYONE} tone="plain" attr="data-applies" />;
  return (
    <>
      {roles.map((r) => <WordBadge key={r} word={r} tone="plain" mono attr="data-applies" />)}
    </>
  );
}

// RecChip is the one orange tag of the console: this policy records the
// conversations of every session it matches.
export function RecChip({ capture }: { capture: "verbatim" | "redact" | null }) {
  if (!capture) return null;
  return <WordBadge word={REC} tone="warn" mono title={capture === "redact" ? REC_MASKED : REC_VERBATIM} attr="data-rec" />;
}

export function PolicyFacts({ view, roles, onEdit }: Props) {
  const [open, setOpen] = React.useState<Fact | null>(null);
  const cell = (key: Fact, label: string, help: string | null, value: React.ReactNode, note?: string) => (
    <div className="flex min-w-0 flex-1 flex-col gap-1 border-r border-border px-3.5 py-2.5 last:border-r-0" data-fact={key}>
      <span className="flex items-center gap-1.5 text-[12px] font-semibold uppercase tracking-[.06em] text-muted-foreground">
        {label}
        {help && <HelpTip label={label} text={help} />}
      </span>
      <span className="flex flex-wrap items-center gap-2 text-sm">
        {value}
        <Button variant="link" size="sm" className="h-auto p-0 text-[13px]" onClick={() => setOpen(key)}>{FACT.change}</Button>
      </span>
      {note && <p className="text-[13px] leading-relaxed text-muted-foreground">{note}</p>}
    </div>
  );

  return (
    <>
      <div className="flex flex-wrap rounded-md border border-border bg-card" data-facts-row>
        {cell("applies", FACT.applies, APPLIES_FACT_HELP, <RoleChips roles={view.roles} />)}
        {cell("recording", FACT.recording, RECORDING_FACT_HELP, view.capture
          ? <><RecChip capture={view.capture} /><span className="text-text-2">{recWord(view.capture)}</span></>
          : <span className="text-muted-foreground">{NO_RECORDING}</span>)}
        {cell("priority", FACT.priority, null, <b className="font-mono font-semibold text-foreground">{view.priority}</b>, PRIORITY_LINE)}
      </div>

      <Sheet open={open !== null} onOpenChange={(o) => { if (!o) setOpen(null); }}>
        <SheetContent className="w-full gap-0 p-0 sm:max-w-[520px]" data-fact-sheet={open || undefined}>
          {open && <FactBody key={open} which={open} view={view} roles={roles} onEdit={onEdit} onClose={() => setOpen(null)} />}
        </SheetContent>
      </Sheet>
    </>
  );
}

type BodyProps = { which: Fact; view: SetView; roles: RoleRow[]; onEdit: Props["onEdit"]; onClose: () => void };

const TITLE: Record<Fact, string> = { applies: CHANGE_APPLIES, recording: CHANGE_RECORDING, priority: CHANGE_PRIORITY };
// Each sheet leads with the sentence its fact carries, which is also what
// the screen reader announces when the sheet opens.
const LEDE: Record<Fact, string> = { applies: APPLIES_FACT_HELP, recording: RECORDING_FACT_HELP, priority: PRIORITY_HELP };

// FactBody holds one sheet's answer until it is applied. It is keyed by
// the fact, so opening a sheet again starts from what the document says.
function FactBody({ which, view, roles, onEdit, onClose }: BodyProps) {
  const [picked, setPicked] = React.useState<string[]>(view.roles);
  // everyone is the first row of the Applies to list, not the absence of a
  // tick: a set that names no role applies to every session, and the
  // difference between that and an unfinished answer is what the refusal
  // under the list catches.
  const [everyone, setEveryone] = React.useState(view.roles.length === 0);
  const [mode, setMode] = React.useState<string>(view.capture || "off");
  const [priority, setPriorityText] = React.useState(String(view.priority));
  const nothing = which === "applies" && !everyone && picked.length === 0;

  // A role the policy names stays on the list even where the roles read
  // did not bring it back, so applying never drops it silently.
  const offered = [...new Set([...roles.filter((r) => r.kind === "application").map((r) => r.name), ...view.roles])].sort();
  const holderCount = Object.fromEntries(roles.map((r) => [r.name, r.holder_count || 0]));

  const apply = () => {
    if (nothing) return;
    if (which === "applies") onEdit(TOUCHED.applies, (doc) => setMatchRoles(doc, picked));
    else if (which === "recording") onEdit(TOUCHED.recording, (doc) => setCapture(doc, mode === "off" ? null : (mode as "verbatim" | "redact")));
    else {
      const n = Number.parseInt(priority, 10);
      onEdit(TOUCHED.priority, (doc) => setPriority(doc, Number.isFinite(n) ? n : view.priority));
    }
    onClose();
  };

  return (
    <>
      <SheetHeader className="border-b border-border pr-12">
        <SheetTitle className="text-lg leading-snug">{TITLE[which]}</SheetTitle>
        <SheetDescription>{LEDE[which]}</SheetDescription>
      </SheetHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4" data-fact-body={which}>
        {which === "applies" && (
          <>
            <div className="flex flex-col gap-1.5">
              <label className="flex items-center gap-2.5 rounded-md border border-border px-3 py-2 text-sm">
                <input
                  type="checkbox"
                  aria-label={EVERYONE}
                  className="size-4 accent-primary"
                  checked={everyone}
                  onChange={() => { setEveryone((on) => !on); setPicked([]); }}
                />
                <span className="text-foreground">{EVERYONE}</span>
              </label>
              {offered.map((name) => (
                <label key={name} className="flex items-center gap-2.5 rounded-md border border-border px-3 py-2 text-sm">
                  <input
                    type="checkbox"
                    aria-label={name}
                    className="size-4 accent-primary"
                    checked={picked.includes(name)}
                    onChange={() => {
                      setEveryone(false);
                      setPicked((p) => (p.includes(name) ? p.filter((x) => x !== name) : [...p, name]));
                    }}
                  />
                  <span className="font-mono text-foreground">{name}</span>
                  <span className="ml-auto text-[13px] text-muted-foreground">{holders(holderCount[name] || 0)}</span>
                </label>
              ))}
            </div>
            <p className="flex flex-wrap items-center gap-2 text-[13px] leading-relaxed text-muted-foreground" data-applies-state>
              {everyone && <><WordBadge word={EVERYONE} tone="plain" />{EVERYONE_MEANS}</>}
            </p>
            {nothing && <p className="text-[13px] leading-relaxed text-danger" data-nothing-ticked>{NOTHING_TICKED}</p>}
          </>
        )}

        {which === "recording" && (
          <RadioGroup value={mode} onValueChange={setMode} className="gap-2.5">
            {([["off", RECORDING_CHOICE.off], ["verbatim", RECORDING_CHOICE.verbatim], ["redact", RECORDING_CHOICE.masked]] as const).map(([value, label]) => (
              <div key={value} className="flex items-center gap-2.5 text-sm">
                <RadioGroupItem value={value} id={"recording-" + value} />
                <Label htmlFor={"recording-" + value} className="font-normal">{label}</Label>
              </div>
            ))}
          </RadioGroup>
        )}

        {which === "priority" && (
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="policy-priority">{FACT.priority}</Label>
            <Input id="policy-priority" type="number" className="w-28 font-mono" value={priority} onChange={(e) => setPriorityText(e.target.value)} />
          </div>
        )}
      </div>

      <SheetFooter className="mt-0 border-t border-border">
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" onClick={onClose}>{CANCEL}</Button>
          <Button onClick={apply} aria-disabled={nothing || undefined}>{APPLY_TO_PAGE}</Button>
        </div>
      </SheetFooter>
    </>
  );
}
