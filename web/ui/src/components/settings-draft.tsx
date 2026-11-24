import * as React from "react";
import { ArrowRightIcon, CopyIcon, PlusIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Section } from "@/components/sheet-parts";
import {
  ADD_ROW_HINT,
  CHANGE_NOW,
  CLOSE,
  COPY_FRAGMENT,
  type ConfigRow,
  DRAFT_CHANGES,
  DRAFT_DEPLOY,
  DRAFT_EMPTY,
  DRAFT_LEDE,
  DRAFT_TITLE,
  ENV_LABEL,
  FILE_LABEL,
  FRAGMENT_COPIED,
  FRAGMENT_TITLE,
  MISSING_VALUE,
  NO_ENV_LINE,
  PICK_LABEL,
  VALUES_LABEL,
  VALUE_PLACEHOLDER,
  consequenceOf,
  includeLabel,
  valueLabel,
  valuesOf,
} from "@/lib/config-words";
import { configFragment, draftScalar, draftValueError, shellValue } from "@/lib/config-export";
import { copyText } from "@/lib/clipboard";

// The Prepare a change sheet of the Configuration tab: pick the rows to
// change, read what each one costs, and
// take the lines for the config file, the chart values or the
// environment. Nothing here reaches the server; the deployment applies the
// change its own way. The lines show once a picked row has a value.

// CHART_KEY is the chart value that carries the whole strazad config file
// (deploy/helm/straza/values.yaml configYaml), so the chart fragment is the
// file fragment under that one key.
const CHART_KEY = "configYaml";

// Drafted is one drafted change: the row's id, the value the person picked or
// typed, and whether the row is in the fragment.
type Drafted = { id: string; value: string; include: boolean };

// View is the tab of the lines that shows, the one Copy takes.
type View = "file" | "chart" | "env";
const VIEW_LABEL: Record<View, string> = { file: FILE_LABEL, chart: VALUES_LABEL, env: ENV_LABEL };

type Props = {
  open: boolean;
  rows: ConfigRow[];
  onClose: () => void;
};

// firstValue is what a picked row starts at: its current value when the
// row has a closed set that holds it, the first allowed value when it does
// not, and empty when the row takes free text, since the value on screen
// is a reading in words and not what the config file takes.
function firstValue(r: ConfigRow): string {
  const values = valuesOf(r);
  if (!values.length) return "";
  const current = r.raw === undefined ? r.value : String(r.raw);
  return values.includes(current) ? current : values[0];
}

export function DraftSheet({ open, rows: sourceRows, onClose }: Props) {
  const rows = sourceRows.flatMap((row) => row.id === "tls" ? [
    { ...row, id: "tlsCert", name: "TLS certificate file", key: "server.tls.certFile", env: "STRAZA_TLS_CERT_FILE", raw: undefined },
    { ...row, id: "tlsKey", name: "TLS private key file", key: "server.tls.keyFile", env: "STRAZA_TLS_KEY_FILE", raw: undefined },
  ] : [row]);
  const [picks, setPicks] = React.useState<Drafted[]>([]);
  const [missing, setMissing] = React.useState<string | null>(null);
  const [view, setView] = React.useState<View>("file");

  const settable = rows.filter((r) => r.key && !r.key.includes(","));
  const rowOf = (id: string) => rows.find((r) => r.id === id) as ConfigRow;
  const add = (id: string) => {
    const row = rows.find((r) => r.id === id);
    if (!row) return;
    setPicks((cur) => (cur.some((p) => p.id === id) ? cur : [...cur, { id, value: firstValue(row), include: true }]));
  };
  const setValue = (id: string, value: string) => {
    setMissing((m) => (m === id ? null : m));
    setPicks((cur) => cur.map((p) => (p.id === id ? { ...p, value } : p)));
  };
  const setInclude = (id: string, include: boolean) => setPicks((cur) => cur.map((p) => (p.id === id ? { ...p, include } : p)));

  const taken = picks.filter((p) => p.include && p.value.trim() !== "" && !draftValueError(p.id, p.value.trim()));
  const lines = taken.map((p) => ({ key: rowOf(p.id).key.split(",")[0].trim(), value: draftScalar(p.id, p.value.trim()) }));
  const file = configFragment(lines);
  const chart = lines.length ? CHART_KEY + ": |\n" + file.split("\n").map((line) => "  " + line).join("\n") : "";
  const env = taken
    .map((p) => {
      const row = rowOf(p.id);
      const key = row.key.split(",")[0].trim();
      const variable = row.env.split(",")[0].trim();
      return variable ? variable + "=" + shellValue(p.value.trim()) : NO_ENV_LINE(key);
    })
    .join("\n");
  const blocks: Record<View, string> = { file, chart, env };

  const copy = () => {
    // The primary stays clickable: an empty value focuses its field and
    // says what is missing, instead of a greyed button.
    const blank = picks.find((p) => p.include && (p.value.trim() === "" || draftValueError(p.id, p.value.trim())));
    if (blank) {
      setMissing(blank.id);
      const field = document.getElementById("draft-value-" + blank.id);
      if (field instanceof HTMLElement) field.focus();
      return;
    }
    copyText(blocks[view], FRAGMENT_COPIED);
  };

  return (
    <Sheet open={open} onOpenChange={(o) => { if (!o) onClose(); }}>
      <SheetContent className="w-full gap-0 p-0 sm:max-w-[720px]" data-draft-sheet>
        <SheetHeader className="border-b border-border">
          <SheetTitle>{DRAFT_TITLE}</SheetTitle>
          <SheetDescription className="max-w-[70ch]">{DRAFT_LEDE}</SheetDescription>
        </SheetHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto p-4">
          <Section title={DRAFT_CHANGES}>
            {picks.length === 0 && <p className="text-dense text-text-2" data-draft-empty>{DRAFT_EMPTY}</p>}
            {picks.map((p) => (
              <DraftRow key={p.id} row={rowOf(p.id)} pick={p} missing={missing === p.id} onValue={setValue} onInclude={setInclude} />
            ))}
            <div className="flex flex-col gap-1">
              <label htmlFor="draft-add" className="text-caption font-medium text-text-2">{PICK_LABEL}</label>
              <Select value="" onValueChange={add}>
                <SelectTrigger id="draft-add" className="h-8 w-[280px] max-w-full">
                  <PlusIcon className="size-3.5 text-muted-foreground" />
                  <SelectValue placeholder={ADD_ROW_HINT} />
                </SelectTrigger>
                <SelectContent>
                  {settable.map((r) => (
                    <SelectItem key={r.id} value={r.id} disabled={picks.some((p) => p.id === r.id)}>{r.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </Section>

          {lines.length > 0 && (
            <Section title={FRAGMENT_TITLE}>
              <div className="flex flex-col gap-3">
                <Tabs value={view} onValueChange={(v) => setView(v as View)}>
                  <div className="border-b border-border">
                    <TabsList variant="line">
                      {(Object.keys(VIEW_LABEL) as View[]).map((v) => <TabsTrigger key={v} value={v}>{VIEW_LABEL[v]}</TabsTrigger>)}
                    </TabsList>
                  </div>
                  {(Object.keys(VIEW_LABEL) as View[]).map((v) => (
                    <TabsContent key={v} value={v}>
                      <pre className="overflow-x-auto rounded-md border border-border bg-card px-3 py-2 font-mono text-caption leading-relaxed text-foreground" data-fragment={VIEW_LABEL[v]}>{blocks[v]}</pre>
                    </TabsContent>
                  ))}
                </Tabs>
                <p className="max-w-[70ch] text-caption text-muted-foreground">{DRAFT_DEPLOY}</p>
              </div>
            </Section>
          )}
        </div>
        <SheetFooter className="flex-row justify-end border-t border-border">
          <Button variant="outline" onClick={onClose}>{CLOSE}</Button>
          <Button onClick={copy}><CopyIcon /> {COPY_FRAGMENT}</Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

type RowProps = {
  row: ConfigRow;
  pick: Drafted;
  missing: boolean;
  onValue: (id: string, value: string) => void;
  onInclude: (id: string, include: boolean) => void;
};

// DraftRow is one picked setting: the box that keeps it in the fragment,
// its name and key, the value it holds now, the value to write, and the
// sentence saying what the change costs.
function DraftRow({ row, pick, missing, onValue, onInclude }: RowProps) {
  const values = valuesOf(row);
  const key = row.key.split(",")[0].trim();
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-border px-3 py-2.5 text-dense" data-draft-row={row.id}>
      <input
        type="checkbox"
        className="size-4 accent-primary"
        aria-label={includeLabel(row.name)}
        checked={pick.include}
        onChange={(e) => onInclude(row.id, e.target.checked)}
      />
      <span className="flex min-w-0 flex-1 basis-[200px] flex-col">
        <b className="font-medium text-foreground">{row.name}</b>
        <span className="truncate font-mono text-caption text-muted-foreground">{key}</span>
      </span>
      {/* The current value and its new one wrap as one group, so "now" stays beside the value it leads. */}
      <span className="flex min-w-0 flex-wrap items-center gap-3" data-draft-change={row.id}>
        <span className="flex min-w-0 items-baseline gap-1.5">
          <span className="text-caption text-muted-foreground">{CHANGE_NOW}</span>
          <span className="font-mono text-caption break-all text-foreground">{row.value}</span>
        </span>
        <ArrowRightIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        {values.length > 0 ? (
          <Select value={pick.value} onValueChange={(v) => onValue(row.id, v)}>
            <SelectTrigger id={"draft-value-" + row.id} aria-label={valueLabel(row.name)} className="h-8 w-[180px] font-mono"><SelectValue /></SelectTrigger>
            <SelectContent>
              {values.map((v) => <SelectItem key={v} value={v} className="font-mono">{v}</SelectItem>)}
            </SelectContent>
          </Select>
        ) : (
          <Input
            id={"draft-value-" + row.id}
            aria-label={valueLabel(row.name)}
            placeholder={VALUE_PLACEHOLDER}
            value={pick.value}
            onChange={(e) => onValue(row.id, e.target.value)}
            aria-invalid={missing || undefined}
            aria-describedby={missing ? "draft-miss-" + row.id : undefined}
            className="h-8 w-[180px] font-mono"
          />
        )}
      </span>
      <span className="basis-full text-caption leading-relaxed text-text-2" data-draft-why={row.id}>{consequenceOf(row)}</span>
      {missing && <span id={"draft-miss-" + row.id} className="basis-full text-caption text-danger">{draftValueError(row.id, pick.value.trim()) || MISSING_VALUE}</span>}
    </div>
  );
}
