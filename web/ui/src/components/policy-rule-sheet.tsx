import * as React from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { FetchError } from "@/components/error-state";
import { RuleEditor } from "@/components/policy-rule-editor";
import { CAPS, HINT } from "@/components/wizard/parts";
import { type ApiError, type AppRow, type EventSupport, type RoleRow, listApps } from "@/lib/api";
import {
  type Bucket, type Doc, type Plain, type Posture, type RuleView,
  buildRule, newPolicyDoc, proposeRuleId, ruleGaps, rulesOf,
} from "@/lib/policy-model";
import {
  APPLY_TO_PAGE, BUCKET_WORD, CANCEL, CHECK_RULE, CLOSE_SHEET, CONFIRM_RULE, NEED, NEW_RULE_ID, NEW_RULE_LEDE, NEW_RULE_TITLE,
  PICK_SERVER, RULE_ID, RULE_ID_HINT, RULE_ID_TAKEN, RULE_SHEET_LEDE, SUBJECT_SERVERS, WHAT_HAPPENS, WHERE_CHOICE, WHERE_Q, stillNeeded,
} from "@/lib/policy-words";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// The rule sheet of a policy's page: the table's row opens the rule on the
// right, where the rule editor does the
// changing, and Add rule opens the same sheet with nothing chosen yet. An
// edit to a rule already on the page lands in the page's document at once,
// so only the new rule has a button that applies it.

// NEW_SHEET is the open value the page passes for the new rule.
export const NEW_SHEET = "new";

// Lane4 are the lanes a new rule can be written for, the ones the model
// builds a rule on.
type Lane4 = "mcp" | "shell" | "files" | "net";
// NewPosture is what a new rule can do; the other two postures are written
// by hand in the text.
type NewPosture = "deny" | "allow" | "hold" | "ticket";

const LANES: Lane4[] = ["mcp", "shell", "files", "net"];
const BUCKETS: Bucket[] = ["deny", "hum", "allow"];
const SEG = "inline-flex h-8 items-center border-r border-border px-3.5 text-[13px] text-text-2 last:border-r-0 hover:bg-accent focus-visible:z-10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50";

// SCRATCH names the document the new rule is edited on. It never reaches
// the page or the server: only the rule inside it does, on apply.
const SCRATCH = "scratch";

// newPostureOf narrows a rule's posture to the four a new rule holds.
const newPostureOf = (p: Posture): NewPosture => (p === "deny" ? "deny" : p === "allow" ? "allow" : p === "ticket" ? "ticket" : "hold");
const bucketOfPosture = (p: NewPosture): Bucket => (p === "deny" ? "deny" : p === "allow" ? "allow" : "hum");

type Props = {
  // open is the rule the sheet shows: a rule id, NEW_SHEET for the new
  // rule, or null while the sheet is closed.
  open: string | null;
  onOpenChange: (open: boolean) => void;
  doc: Doc;
  rules: RuleView[];
  // events is the harness matrix the page read once; null while the read
  // is in flight or after it failed.
  events: EventSupport | null;
  roles: RoleRow[];
  onEdit: (touched: string, fn: (doc: Doc) => void) => void;
  onRemove: (id: string) => void;
  // onSplit moves one call of a rule into a rule of its own.
  onSplit: (id: string, name: string) => void;
  // onAdd puts the new rule on the page's document under the id typed.
  onAdd: (id: string, rule: Plain) => void;
};

export function PolicyRuleSheet({ open, onOpenChange, doc, rules, events, roles, onEdit, onRemove, onSplit, onAdd }: Props) {
  const rule = open === null || open === NEW_SHEET ? null : rules.find((r) => r.id === open) || null;
  const close = () => onOpenChange(false);
  return (
    <Sheet open={open !== null} onOpenChange={onOpenChange}>
      <SheetContent showCloseButton={false} className="w-full gap-0 p-0 sm:max-w-[560px]" data-rule-sheet={open || undefined}>
        {open === NEW_SHEET && <NewRule rules={rules} events={events} roles={roles} onAdd={onAdd} onClose={close} />}
        {rule && (
          <>
            <SheetHeader className="border-b border-border">
              <SheetTitle className="font-mono text-base leading-snug">{rule.id}</SheetTitle>
              <SheetDescription>{RULE_SHEET_LEDE}</SheetDescription>
            </SheetHeader>
            <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4">
              {rule.posture === "check" && <p className="text-sm leading-relaxed text-text-2">{CHECK_RULE}</p>}
              {rule.posture === "confirm" && <p className="text-sm leading-relaxed text-text-2">{CONFIRM_RULE}</p>}
              {rule.posture !== "check" && rule.posture !== "confirm" && (
                <RuleEditor
                  doc={doc}
                  rule={rule}
                  events={events}
                  roles={roles}
                  onEdit={onEdit}
                  onRemove={onRemove}
                  onSplit={(name) => onSplit(rule.id, name)}
                />
              )}
            </div>
            <SheetFooter className="border-t border-border sm:flex-row sm:justify-end">
              <Button variant="outline" onClick={close}>{CLOSE_SHEET}</Button>
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

type NewProps = {
  rules: RuleView[];
  events: EventSupport | null;
  roles: RoleRow[];
  onAdd: (id: string, rule: Plain) => void;
  onClose: () => void;
};

// NewRule asks where the call goes and what should happen to it, then
// hands the same editor an existing rule gets, working on a scratch
// document. Nothing reaches the page until the apply button, which stays
// out of reach while the rule is missing an answer.
function NewRule({ rules, events, roles, onAdd, onClose }: NewProps) {
  const [lane, setLane] = React.useState<Lane4 | null>(null);
  const [app, setApp] = React.useState("");
  const [picked, setPicked] = React.useState<NewPosture | null>(null);
  const [scratch, setScratch] = React.useState<Doc | null>(null);
  const [typed, setTyped] = React.useState<string | null>(null);
  const [apps, setApps] = React.useState<AppRow[] | null>(null);
  const [appsProblem, setAppsProblem] = React.useState<string | null>(null);
  // The editor writes into the scratch document in place, the way it
  // writes into the page's, so a change of it is told by hand.
  const [, bump] = React.useReducer((n: number) => n + 1, 0);

  React.useEffect(() => {
    listApps().then(setApps, (e) => {
      const err = e as ApiError;
      if (err.status !== 401) setAppsProblem(readFailed(SUBJECT_SERVERS, err));
    });
  }, []);

  const view = scratch ? rulesOf(scratch)[0] || null : null;
  const posture = view ? newPostureOf(view.posture) : picked;
  const names = view && view.names ? view.names : [];
  const taken = rules.map((r) => r.id);
  const proposed = lane && posture ? proposeRuleId(posture, lane, app || null, names, taken) : "";
  const id = typed === null ? proposed : typed;
  const gaps = ruleGaps({ lane, names: view ? view.names : null, posture: view ? view.posture : picked, reason: view ? view.reason : "" });
  // The document refuses a second rule under one id, so the id typed is
  // checked here rather than on the page that would throw.
  const clash = view !== null && taken.includes(id);

  // build makes the scratch document the editor works on, carrying every
  // answer the rule already holds. A change of lane drops the calls, since
  // a command pattern is not a tool name.
  const build = (next: { lane: Lane4; app: string; posture: NewPosture }) => {
    const keep = view && next.lane === lane ? view : null;
    setScratch(newPolicyDoc({
      name: SCRATCH,
      rules: [buildRule({
        id: NEW_RULE_ID,
        lane: next.lane,
        posture: next.posture,
        app: next.lane === "mcp" && next.app ? next.app : undefined,
        names: keep && keep.names ? keep.names : [],
        who: keep ? keep.who : undefined,
        timeoutSeconds: keep ? keep.timeoutSeconds : undefined,
        ticketTTLSeconds: keep ? keep.ticketTTLSeconds : undefined,
        grantTTLSeconds: keep ? keep.grantTTLSeconds : undefined,
        reason: keep ? keep.reason : undefined,
      })],
    }));
  };

  const pickLane = (next: Lane4) => {
    setLane(next);
    if (posture) build({ lane: next, app, posture });
  };

  const pickApp = (next: string) => {
    setApp(next);
    if (lane && posture) build({ lane, app: next, posture });
  };

  const pickBucket = (b: Bucket) => {
    const next: NewPosture = b === "hum" ? "hold" : b;
    setPicked(next);
    if (lane) build({ lane, app, posture: next });
  };

  const editScratch = (_touched: string, fn: (doc: Doc) => void) => {
    if (!scratch) return;
    fn(scratch);
    bump();
  };

  const apply = () => {
    if (gaps.length || clash || !view || !id) return;
    onAdd(id, { ...view.raw, id });
  };

  return (
    <>
      <SheetHeader className="border-b border-border">
        <SheetTitle className="text-lg leading-snug">{NEW_RULE_TITLE}</SheetTitle>
        <SheetDescription>{NEW_RULE_LEDE}</SheetDescription>
      </SheetHeader>

      <div className="flex min-h-0 flex-1 flex-col gap-3.5 overflow-y-auto px-4 py-4">
        <div className="flex flex-col gap-2">
          <span className={CAPS}>{WHERE_Q}</span>
          <RadioGroup value={lane || ""} onValueChange={(v) => pickLane(v as Lane4)} className="gap-2.5" data-where={lane || ""}>
            {LANES.map((l) => (
              <div key={l} className="flex items-center gap-2.5">
                <RadioGroupItem value={l} id={"where-" + l} aria-label={WHERE_CHOICE[l]} />
                <label htmlFor={"where-" + l} className="text-sm text-foreground">{WHERE_CHOICE[l]}</label>
              </div>
            ))}
          </RadioGroup>
        </div>

        {lane === "mcp" && (
          <div className="flex flex-col gap-1.5">
            <span className={CAPS}>{PICK_SERVER}</span>
            {appsProblem && <FetchError subject={SUBJECT_SERVERS} detail={appsProblem} />}
            {appsProblem
              ? <Input aria-label={PICK_SERVER} value={app} spellCheck={false} className="h-8 max-w-[320px] font-mono" onChange={(e) => pickApp(e.target.value)} />
              : (
                <Select value={app} onValueChange={pickApp}>
                  <SelectTrigger aria-label={PICK_SERVER} className="h-8 w-72"><SelectValue placeholder={PICK_SERVER} /></SelectTrigger>
                  <SelectContent>
                    {(apps || []).map((a) => <SelectItem key={a.id} value={a.name}>{a.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              )}
          </div>
        )}

        {!view && (
          <div className="flex flex-col gap-1.5">
            <span className={CAPS}>{WHAT_HAPPENS}</span>
            <div role="group" aria-label={WHAT_HAPPENS} className="inline-flex w-fit overflow-hidden rounded-md border border-border" data-posture={picked ? bucketOfPosture(picked) : ""}>
              {BUCKETS.map((b) => (
                <button
                  key={b}
                  type="button"
                  aria-pressed={picked !== null && bucketOfPosture(picked) === b}
                  className={cn(SEG, picked !== null && bucketOfPosture(picked) === b && "bg-accent-bg font-semibold text-foreground")}
                  onClick={() => pickBucket(b)}
                >
                  {BUCKET_WORD[b]}
                </button>
              ))}
            </div>
          </div>
        )}

        {scratch && view && (
          <>
            <RuleEditor doc={scratch} rule={view} events={events} roles={roles} onEdit={editScratch} />
            <div className="flex flex-col gap-1.5">
              <label htmlFor="new-rule-id" className={CAPS}>{RULE_ID}</label>
              <Input
                id="new-rule-id"
                value={id}
                spellCheck={false}
                className="h-8 max-w-[420px] font-mono"
                onChange={(e) => setTyped(e.target.value)}
              />
              <span className={HINT}>{RULE_ID_HINT}</span>
              {clash && <span className="text-[13px] leading-snug text-danger" data-id-taken>{RULE_ID_TAKEN}</span>}
            </div>
          </>
        )}
      </div>

      <SheetFooter className="border-t border-border sm:flex-row sm:items-center sm:justify-end">
        {gaps.length > 0 && (
          <span className="mr-auto text-[13px] leading-snug text-danger" data-still-needed>{stillNeeded(gaps.map((g) => NEED[g]))}</span>
        )}
        <Button variant="outline" onClick={onClose}>{CANCEL}</Button>
        <Button aria-disabled={gaps.length || clash ? true : undefined} onClick={apply}>{APPLY_TO_PAGE}</Button>
      </SheetFooter>
    </>
  );
}
