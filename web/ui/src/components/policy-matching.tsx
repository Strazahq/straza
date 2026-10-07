import * as React from "react";
import { ChevronRightIcon, XIcon } from "lucide-react";
import { stringify } from "yaml";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { DropdownMenu, DropdownMenuCheckboxItem, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { HelpTip } from "@/components/help-tip";
import { HINT } from "@/components/wizard/parts";
import type { EventSupport } from "@/lib/api";
import { type Doc, type Plain, type RuleView, setEvents, setNames } from "@/lib/policy-model";
import {
  ADD_PATH, ADD_PATTERN, ADD_TOOL, ADVANCED, AS_WRITTEN, AS_WRITTEN_HELP, CHOOSE_EVENTS, CONDITION_HELP, EVENTS_DEFAULT_HELP, EVERY_SERVER,
  EVERY_WORD, MATCH, MOVE_OUT, PATTERNS_HINT, REQUIRE_NONE, WHERE_WORD, eventsCoverage, eventsNeverFire, eventsWords, foldSummary, moveOut,
  removePick,
} from "@/lib/policy-words";
import { cn } from "@/lib/utils";

// Advanced matching, the fold of an open rule card: what the rule matches
// on its lane, the events it fires on
// with the harness truth from the server's matrix, what it requires of a
// session, and anything the cards cannot show, kept as written.

type Props = {
  rule: RuleView;
  // events is the harness matrix; null while the read is in flight or
  // after it failed, and the fold then says nothing about coverage.
  events: EventSupport | null;
  onEdit: (touched: string, fn: (doc: Doc) => void) => void;
  // onSplit moves one name into a rule of its own. The chips offer the
  // link only where the page passes it and the rule names more than one
  // call.
  onSplit?: (name: string) => void;
};

const CHIP = "inline-flex items-center gap-1 rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] text-foreground";
const KEY = "cursor-help text-[13px] text-muted-foreground underline decoration-dotted underline-offset-[3px]";

// Row is one label and value pair. The label carries the YAML key it
// writes, so the card and the text read as one document.
function Row({ slot, label, yamlKey, help, children }: { slot: string; label: string; yamlKey: string; help?: string; children: React.ReactNode }) {
  return (
    <>
      <span className="flex items-center gap-1.5">
        <span className={KEY} title={yamlKey}>{label}</span>
        {help && <HelpTip label={label} text={help} />}
      </span>
      <div className="flex min-w-0 flex-col items-start gap-1.5 text-sm text-foreground" data-match={slot}>{children}</div>
    </>
  );
}

// Names are what the rule matches on its lane, one chip each. Removing the
// last chip widens the rule to every call on the lane, which is what an
// absent matcher means, so nothing is confirmed.
function Names({ rule, label, add, hint, onEdit, onSplit }: { rule: RuleView; label: string; add: string; hint?: string; onEdit: Props["onEdit"]; onSplit?: Props["onSplit"] }) {
  const [text, setText] = React.useState("");
  const names = rule.names || [];
  const split = onSplit && names.length > 1 ? onSplit : null;
  const write = (next: string[]) => onEdit(rule.id, (doc) => setNames(doc, rule.id, next.length ? next : null));
  const push = () => {
    const value = text.trim();
    if (!value || names.includes(value)) { setText(""); return; }
    write([...names, value]);
    setText("");
  };
  return (
    <>
      <div className="flex flex-wrap items-center gap-1.5">
        {names.length === 0 && <span className="text-sm text-muted-foreground">{EVERY_WORD[rule.lane]}</span>}
        {names.map((n) => (
          <span key={n} className="inline-flex items-center gap-1">
            <span className={CHIP} data-name={n}>
              {n}
              <button type="button" aria-label={removePick(n)} className="text-muted-foreground hover:text-foreground" onClick={() => write(names.filter((x) => x !== n))}>
                <XIcon className="size-3" aria-hidden="true" />
              </button>
            </span>
            {split && (
              <Button variant="link" size="sm" className="h-6 px-1" aria-label={moveOut(n)} data-move-out={n} onClick={() => split(n)}>{MOVE_OUT}</Button>
            )}
          </span>
        ))}
      </div>
      <div className="flex items-center gap-2">
        <Input
          aria-label={label}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); push(); } }}
          className="h-8 w-56 font-mono"
        />
        <Button variant="outline" size="sm" onClick={push}>{add}</Button>
      </div>
      {hint && <span className={HINT}>{hint}</span>}
    </>
  );
}

// Events says when the rule fires, in one shape on every lane: the events
// as stored, then what they mean. An MCP rule has one moment, so the
// gateway needs no picker. On the hook lanes the picker lists the matrix
// and each choice carries the harnesses it never reaches.
function Events({ rule, events, onEdit }: Props) {
  const matrix = events ? events.events : [];
  const all = events && events.harnesses ? events.harnesses : [];
  const picked = rule.events;
  const missingOf = (kind: string) => {
    const row = matrix.find((e) => e.kind === kind);
    return all.filter((h) => !(row && row.harnesses ? row.harnesses : []).includes(h));
  };
  const known = picked.filter((k) => matrix.some((e) => e.kind === k));
  const never = known.length ? all.filter((h) => known.every((k) => missingOf(k).includes(h))) : [];
  const plain = picked.length === 0 || (picked.length === 1 && picked[0] === "tool.pre");
  const cover = known.map((k) => eventsCoverage(k, missingOf(k))).join(" ");

  if (rule.lane === "mcp") return <span className="text-sm text-text-2">{eventsWords(picked, rule.lane)}</span>;
  return (
    <>
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-sm text-text-2">{eventsWords(picked, rule.lane)}</span>
        {plain ? <HelpTip label={MATCH.events} text={EVENTS_DEFAULT_HELP} /> : cover && <HelpTip label={MATCH.events} text={cover} />}
        {matrix.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="link" size="sm" className="h-6 px-1">{CHOOSE_EVENTS}</Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="max-w-[46ch]">
              {matrix.map((e) => (
                <DropdownMenuCheckboxItem
                  key={e.kind}
                  data-event={e.kind}
                  checked={picked.includes(e.kind)}
                  onSelect={(ev) => ev.preventDefault()}
                  onCheckedChange={(on) => onEdit(rule.id, (doc) => setEvents(doc, rule.id, on ? [...picked, e.kind] : picked.filter((k) => k !== e.kind)))}
                >
                  <span className="flex flex-col gap-0.5">
                    <span className="font-mono text-[13px] text-foreground">{e.kind}</span>
                    <span className={HINT}>{eventsCoverage(e.kind, missingOf(e.kind))}</span>
                  </span>
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {never.length > 0 && <span className="text-[13px] leading-relaxed text-warn" data-never-fires>{eventsNeverFire(never)}</span>}
    </>
  );
}

// requireWords reads the require block as the rule stores it: the values
// stay as written, because the engine reads them and the card never
// changes them.
function requireWords(raw: Plain): string[] {
  const block = raw.require && typeof raw.require === "object" && !Array.isArray(raw.require) ? (raw.require as Plain) : null;
  if (!block) return [];
  return Object.entries(block).map(([k, v]) => k + ": " + (Array.isArray(v) ? v.join(", ") : String(v)));
}

// asWritten renders the keys the cards cannot show back as YAML, the
// approve keys nested where they live. Nothing is dropped and nothing is
// reshaped, so the block reads as the text does.
function asWritten(rule: RuleView): string {
  const out: Plain = {};
  const approve: Plain = {};
  const raw = rule.raw.approve && typeof rule.raw.approve === "object" ? (rule.raw.approve as Plain) : {};
  for (const key of rule.extra) {
    if (key === "require") continue;
    if (key.startsWith("approve.")) approve[key.slice("approve.".length)] = raw[key.slice("approve.".length)];
    else out[key] = rule.raw[key];
  }
  if (Object.keys(approve).length) out.approve = approve;
  return Object.keys(out).length ? stringify(out, { lineWidth: 0 }) : "";
}

export function PolicyMatching({ rule, events, onEdit, onSplit }: Props) {
  const [open, setOpen] = React.useState(false);
  const requires = requireWords(rule.raw);
  const written = asWritten(rule);
  const editable = rule.lane === "mcp" || rule.lane === "shell" || rule.lane === "files";
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-md border border-border bg-background" data-fold={rule.id}>
      <CollapsibleTrigger className="flex w-full items-center gap-2 rounded-md px-2.5 py-2 text-left text-[13px] text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50">
        <ChevronRightIcon aria-hidden="true" className={cn("size-3.5 shrink-0 transition-transform", open && "rotate-90")} />
        <b className="font-semibold text-text-2">{ADVANCED}</b>
        <span className="min-w-0 truncate">{foldSummary(rule)}</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="grid grid-cols-[minmax(96px,150px)_1fr] items-start gap-x-3 gap-y-2.5 border-t border-border px-2.5 py-3">
        <Row slot="lane" label={MATCH.lane} yamlKey="tools"><span className="text-sm text-text-2">{WHERE_WORD[rule.lane]}</span></Row>

        {rule.lane === "mcp" && (
          <Row slot="server" label={MATCH.server} yamlKey="apps">
            <span className="font-mono text-[13px] text-foreground">{rule.app || EVERY_SERVER}</span>
          </Row>
        )}
        {rule.lane === "mcp" && (
          <Row slot="tools" label={MATCH.tools} yamlKey={rule.namesKey}>
            <Names rule={rule} onEdit={onEdit} onSplit={onSplit} label={MATCH.tools} add={ADD_TOOL} />
          </Row>
        )}
        {rule.lane === "shell" && (
          <Row slot="patterns" label={MATCH.patterns} yamlKey={rule.namesKey}>
            <Names rule={rule} onEdit={onEdit} onSplit={onSplit} label={MATCH.patterns} add={ADD_PATTERN} hint={PATTERNS_HINT} />
          </Row>
        )}
        {rule.lane === "files" && (
          <Row slot="paths" label={MATCH.paths} yamlKey={rule.namesKey}>
            <Names rule={rule} onEdit={onEdit} onSplit={onSplit} label={MATCH.paths} add={ADD_PATH} />
          </Row>
        )}
        {!editable && (
          <Row slot="tools" label={MATCH.tools} yamlKey={rule.namesKey}>
            <div className="flex flex-wrap items-center gap-1.5">
              {(rule.names && rule.names.length ? rule.names : [EVERY_WORD[rule.lane]]).map((n) => <span key={n} className={CHIP} data-name={n}>{n}</span>)}
            </div>
          </Row>
        )}

        <Row slot="events" label={MATCH.events} yamlKey="events">
          <Events rule={rule} events={events} onEdit={onEdit} />
        </Row>

        <Row slot="require" label={MATCH.require} yamlKey="require" help={CONDITION_HELP}>
          {requires.length === 0
            ? <span className="text-sm text-muted-foreground">{REQUIRE_NONE}</span>
            : requires.map((r) => <span key={r} className="font-mono text-[13px] text-foreground">{r}</span>)}
        </Row>

        {written && (
          <Row slot="written" label={AS_WRITTEN} yamlKey={rule.extra.filter((k) => k !== "require").join(", ")} help={AS_WRITTEN_HELP}>
            <pre className="m-0 w-full overflow-x-auto rounded-md border border-border bg-card px-2.5 py-2 font-mono text-[13px] leading-relaxed text-text-2" data-written>{written}</pre>
          </Row>
        )}
      </CollapsibleContent>
    </Collapsible>
  );
}
