import * as React from "react";
import { Loader2Icon, XIcon } from "lucide-react";
import { stringify } from "yaml";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { CardGroup, Code, Fold, OptionCard } from "@/components/wizard/parts";
import { type Covers, type Picked, listed, partial, reachOf, withPick } from "@/lib/access-covers";
import { everyCard, everyLine, onWhich, onWhichHelp, onWhichLede, pickLabel, pickedWords, removeLabel, STORED, theseCard, theseLine, unticked as untickedWords } from "@/lib/access-object-words";
import type { Plan } from "@/lib/access-plan";
import { type ApiError, type AppRow, type ObjectListing, type ObjectRow, type ObjectsBlock, appObjects, query } from "@/lib/api";
import { cap, emptyList, filterLabel, noMatch, nounsOf, reading, stripOf } from "@/lib/object-words";
import { bindingOf } from "@/lib/role-draft";
import { LOAD_MORE, shownWords } from "@/lib/role-words";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// The access editor's On which question on a server whose manifest pins an
// object type: every object, or these objects picked from the list the
// server's listing keeps, searched on the server. Nothing is typed as a
// value, so a row can only name an object the list holds. Under it, the
// access row as the role file stores it.

const DEBOUNCE_MS = 300;
const CARDS = "grid-cols-[repeat(auto-fit,minmax(220px,1fr))]";
const STRIP: Record<string, string> = { plain: "border-border bg-background", ok: "border-border bg-background", danger: "border-danger/40 bg-danger-bg" };

type List =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: ObjectRow[]; total: number; next?: string; problem: string | null };

export type CoversBlockProps = {
  app: AppRow;
  block: ObjectsBlock;
  plan: Plan;
  // names are the server's tools, for the count of tools that name one
  // object and the stored row.
  names: string[];
  onCovers: (c: Covers) => void;
  // unticked are the tools the last pick of These took off the row.
  unticked: string[];
  // everyOff is the sentence that turns the Every card off, empty when it
  // is offered.
  everyOff: string;
};

export function CoversBlock({ app, block, plan, names, onCovers, unticked, everyOff }: CoversBlockProps) {
  const w = nounsOf(block);
  const [typed, setTyped] = React.useState("");
  const [q, setQ] = React.useState("");
  const [list, setList] = React.useState<List>({ kind: "loading" });
  // total is the size of the whole list, read with no filter, which the
  // Every card counts once a listing gave one. listing is the state of the
  // listing, which says why a list is empty before it is.
  const [total, setTotal] = React.useState<number | null>(null);
  const [listing, setListing] = React.useState<ObjectListing | null>(null);
  const [busy, setBusy] = React.useState(false);
  const seq = React.useRef(0);
  const c = plan.covers;
  const some = partial(c);
  const picked = some ? c.objects : [];
  const named = names.filter((t) => reachOf(block, t) === "argument").length;

  React.useEffect(() => {
    const t = setTimeout(() => setQ(typed.trim()), DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [typed]);

  React.useEffect(() => {
    const my = ++seq.current;
    appObjects(app.id, query({ q })).then(
      (answer) => {
        if (my !== seq.current) return;
        if (!q) {
          setTotal(listed(answer.listing) ? answer.total : null);
          setListing(answer.listing || null);
        }
        setList({ kind: "ready", rows: answer.objects || [], total: answer.total, next: answer.next, problem: null });
      },
      (e) => {
        if (my !== seq.current || (e as ApiError).status === 401) return;
        const message = readFailed("The " + w.plural, e as ApiError);
        setList((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
  }, [app.id, q]); // eslint-disable-line react-hooks/exhaustive-deps

  const more = async () => {
    if (list.kind !== "ready" || !list.next) return;
    const my = ++seq.current;
    setBusy(true);
    try {
      const answer = await appObjects(app.id, query({ q, after: list.next }));
      if (my === seq.current) setList((s) => (s.kind === "ready" ? { kind: "ready", rows: [...s.rows, ...(answer.objects || [])], total: answer.total, next: answer.next, problem: null } : s));
    } catch (e) {
      if (my === seq.current && (e as ApiError).status !== 401) setList((s) => (s.kind === "ready" ? { ...s, problem: readFailed("The " + w.plural, e as ApiError) } : s));
    } finally {
      setBusy(false);
    }
  };

  const pick = (o: Picked) => onCovers(withPick(c, o));
  const value = !c ? "" : some ? "these" : "every";
  const strip = listing && !listed(listing) ? stripOf(app.name, w, listing) : null;

  return (
    <div className="flex flex-col gap-2" data-covers={app.name}>
      <div className="flex flex-col gap-0.5">
        <span className="flex items-center gap-1.5 text-sm font-semibold text-foreground">
          {onWhich(w)}
          <HelpTip label={onWhich(w)} text={onWhichHelp(app.name, w)} />
        </span>
        <p className="m-0 max-w-[80ch] text-[13px] leading-snug text-muted-foreground">{onWhichLede(app.name, w, named)}</p>
      </div>
      <CardGroup label={onWhich(w)} value={value} onPick={(v) => (v === "every" ? (everyOff ? undefined : onCovers({ every: true })) : onCovers(some ? c : { objects: [] }))} className={CARDS}>
        <OptionCard value="every" label={everyCard(w)} name={everyCard(w)} tag="" on={value === "every"} off={!!everyOff} line={everyOff || everyLine(app.name, w, total)} />
        <OptionCard value="these" label={theseCard(w)} name={theseCard(w)} tag="" on={value === "these"} line={theseLine(app.name, w)} />
      </CardGroup>
      {unticked.length > 0 && <p role="status" className="m-0 max-w-[80ch] text-[13px] leading-snug text-warn" data-unticked>{untickedWords(unticked, w)}</p>}

      {some && (
        <div className="flex flex-col gap-2.5 rounded-md border border-border bg-card px-3.5 py-3" data-picker>
          <div className="flex flex-wrap items-center gap-1.5" data-picked>
            {picked.length === 0 && <span className="text-[13px] text-muted-foreground">{pickedWords(0, w)}</span>}
            {picked.length > 0 && <span className="mr-1 text-[13px] text-muted-foreground">{pickedWords(picked.length, w)}</span>}
            {picked.map((o) => (
              <span key={o.id || o.value} className="inline-flex items-center gap-1.5 rounded-md border border-link/40 bg-accent-bg py-0.5 pr-0.5 pl-2 text-[13px]" data-pill={o.value}>
                <span className="font-mono text-foreground">{o.value}</span>
                {o.id && <span className="font-mono text-xs text-muted-foreground">{o.id}</span>}
                <Button variant="ghost" size="icon-xs" aria-label={removeLabel(o.value)} onClick={() => pick(o)}><XIcon /></Button>
              </span>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Input value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={filterLabel(w)} aria-label={filterLabel(w)} className="h-8 w-full max-w-xs" />
            {list.kind === "ready" && <span className="text-[13px] text-muted-foreground">{shownWords(list.rows.length, list.total)}</span>}
          </div>
          {list.kind === "loading" && <p className="m-0 text-[13px] text-muted-foreground">{reading(w)}</p>}
          {list.kind === "error" && <FetchError subject={cap(w.plural)} detail={list.message} />}
          {strip && (
            <div className={cn("flex flex-col gap-0.5 rounded-md border px-3 py-2 text-[13px] leading-snug text-foreground", STRIP[strip.tone])} data-listing>
              {strip.lines.map((l) => <p key={l} className="m-0">{l}</p>)}
            </div>
          )}
          {list.kind === "ready" && (list.rows.length > 0 || !!q || !strip) && (
            <>
              {list.problem && <FetchError subject={cap(w.plural)} detail={list.problem} />}
              <div className="max-h-[320px] overflow-y-auto rounded-md border border-border bg-background">
                <Table className="table-fixed">
                  <colgroup>
                    <col className="w-[34px]" />
                    <col />
                    <col className="w-[28%]" />
                  </colgroup>
                  <TableBody>
                    {list.rows.length === 0 && (
                      <TableRow>
                        <TableCell colSpan={3} className="whitespace-normal text-muted-foreground">{q ? noMatch(w, q) : emptyList(w)}</TableCell>
                      </TableRow>
                    )}
                    {list.rows.map((o) => {
                      const on = picked.some((x) => x.id === o.id);
                      return (
                        <TableRow key={o.id} className={cn("text-sm", on && "bg-accent-bg")} data-object={o.value} onClick={() => pick({ value: o.value, id: o.id, title: o.title })}>
                          <TableCell className="px-2 align-top">
                            <input
                              type="checkbox"
                              aria-label={pickLabel(o.value)}
                              checked={on}
                              onClick={(e) => e.stopPropagation()}
                              onChange={() => pick({ value: o.value, id: o.id, title: o.title })}
                              className="size-3.5 align-middle accent-[var(--link)]"
                            />
                          </TableCell>
                          <TableCell className="align-top whitespace-normal">
                            <span className="font-mono break-words text-foreground">{o.value}</span>
                            {o.title && <span className="ml-2 text-[13px] text-muted-foreground">{o.title}</span>}
                          </TableCell>
                          <TableCell className="align-top font-mono text-[13px] break-all text-muted-foreground">{o.id}</TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </div>
              {list.next && (
                <div>
                  <Button variant="outline" size="sm" disabled={busy} onClick={() => void more()}>
                    {busy && <Loader2Icon className="animate-spin" />} {LOAD_MORE}
                  </Button>
                </div>
              )}
            </>
          )}
        </div>
      )}

      <Fold title={STORED}>
        <Code className="rounded-none border-0">{stringify({ bindings: [bindingOf(app, plan, names)] })}</Code>
      </Fold>
    </div>
  );
}
