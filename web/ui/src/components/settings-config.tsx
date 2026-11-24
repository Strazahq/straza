import * as React from "react";
import { ArrowRightIcon, ChevronRightIcon, CopyIcon, LockIcon, TriangleAlertIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { DraftSheet } from "@/components/settings-draft";
import { Chip } from "@/components/chip";
import { type ApiError, type RoleRow, getConfig, listRoles } from "@/lib/api";
import {
  COPY_FRAGMENT,
  CONFIG_BANNER,
  CONFIG_WHY,
  CONSOLE_ACCESS_LINE,
  CONSOLE_ACCESS_TITLE,
  DRAFT_TITLE,
  FACT,
  FROM_SETS,
  LINE_COPIED,
  NO_ENV,
  NOT_SET,
  ON_PUBLISH,
  ON_RESTART,
  OPEN_REGISTRY,
  READING_CONFIG,
  RELAXED_BADGE,
  SECTIONS,
  SUBJECT_CONFIG,
  VALUE_UNAVAILABLE,
  type ConfigRow,
  configRows,
  consoleAreasWords,
  copyLabel,
  envCopyLabel,
  fileLine,
  openRole,
  postureBadge,
} from "@/lib/config-words";
import { shellValue } from "@/lib/config-export";
import { copyText } from "@/lib/clipboard";
import { isPlainClick, navigate, pathFor } from "@/lib/router";
import { SUBJECT_ROLES, kindOf } from "@/lib/role-words";
import { tabLabel } from "@/lib/settings-words";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";
import { holders } from "@/lib/words";

// The Configuration tab of Settings: every setting this strazad runs on,
// read from the server and never written from here. A banner says so and
// opens the sheet that copies a change, the relaxed settings come first,
// and a row opens in place to say where its value is set. The Console
// access section closes the tab with the straza roles the config maps
// console areas to.

type Props = {
  // row is the row the address names (settings/configuration/floor), so a
  // link from another screen opens that row.
  row?: string;
};

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: ConfigRow[]; lastRead: Date; problem: string | null };

// reveal scrolls a row into the middle of the window.
function reveal(id: string) {
  const el = document.getElementById("config-" + id);
  if (el) el.scrollIntoView({ block: "center" });
}

export function ConfigTab({ row }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [roles, setRoles] = React.useState<RoleRow[] | null>(null);
  const [rolesProblem, setRolesProblem] = React.useState<string | null>(null);
  const [open, setOpen] = React.useState<string | null>(row || null);
  const [draftOpen, setDraftOpen] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    getConfig().then(
      (answer) => { if (alive) setState({ kind: "ready", rows: configRows(answer), lastRead: new Date(), problem: null }); },
      (e: ApiError) => {
        if (!alive || e.status === 401) return;
        const message = readFailed(SUBJECT_CONFIG, e);
        setState((s) => (s.kind === "ready" ? { ...s, problem: message } : { kind: "error", message }));
      },
    );
    listRoles().then(
      (rows) => { if (alive) setRoles(rows || []); },
      (e: ApiError) => {
        // A seat without the identity area may not read the roles, so the
        // section leaves rather than failing the tab.
        if (!alive || e.status === 403 || e.status === 401) return;
        setRolesProblem(readFailed(SUBJECT_ROLES, e));
      },
    );
    return () => { alive = false; };
  }, []);

  // A row named in the address opens and is scrolled to once the rows are
  // on screen, so a link from another screen lands on the row it named.
  const rows = state.kind === "ready" ? state.rows : [];
  React.useEffect(() => {
    if (!row || !rows.length) return;
    setOpen(row);
    reveal(row);
  }, [row, rows.length]);

  if (state.kind === "loading") return <p className="text-dense text-muted-foreground">{READING_CONFIG}</p>;
  if (state.kind === "error") return <FetchError subject={SUBJECT_CONFIG} detail={state.message} />;

  const straza = (roles || []).filter((r) => kindOf(r) === "straza");
  const relaxed = rows.filter((r) => r.posture === "relaxed");

  return (
    <div className="flex flex-col gap-4">
      {state.problem && <FetchError subject={SUBJECT_CONFIG} detail={state.problem} lastRead={state.lastRead} />}
      <div role="note" className="flex flex-wrap items-center gap-3 rounded-md border border-border bg-card px-4 py-3" data-config-banner>
        <LockIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <p className="min-w-0 flex-1 basis-[240px] text-dense text-text-2" data-config-lede>
          {CONFIG_BANNER} <HelpTip label={tabLabel("configuration")} text={CONFIG_WHY} />
        </p>
        <Button variant="outline" size="sm" className="ml-auto" onClick={() => setDraftOpen(true)}><CopyIcon /> {DRAFT_TITLE}</Button>
      </div>

      {relaxed.length > 0 && (
        <section className="overflow-hidden rounded-md border border-warn/40" data-relaxed-card>
          <div className="flex items-center gap-2 border-b border-warn/40 bg-warn-bg px-3 py-2 text-warn">
            <TriangleAlertIcon className="size-4 shrink-0" aria-hidden="true" />
            <h2 className="text-body font-semibold">{postureBadge(relaxed.length)}</h2>
          </div>
          {relaxed.map((r) => (
            <button
              key={r.id}
              type="button"
              className="flex w-full flex-col gap-0.5 border-b border-border px-3 py-2.5 text-left text-dense outline-none last:border-b-0 hover:bg-secondary/60 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
              onClick={() => { setOpen(r.id); reveal(r.id); }}
              data-relaxed-row={r.id}
            >
              <span className="flex flex-wrap items-baseline gap-x-2">
                <b className="font-semibold text-foreground">{r.name}</b>
                <span className="font-mono text-caption break-all text-foreground">{r.value}</span>
              </span>
              <span className="text-text-2">{r.cost}</span>
            </button>
          ))}
        </section>
      )}

      {SECTIONS.map((section) => {
        const mine = rows.filter((r) => r.section === section);
        if (!mine.length) return null;
        return (
          <section key={section} className="overflow-hidden rounded-md border border-border" data-config-section={section}>
            <div className="flex flex-wrap items-baseline gap-2 border-b border-border bg-card px-3 py-2">
              <h2 className="text-body font-semibold text-foreground">{section}</h2>
            </div>
            {mine.map((r) => (
              <Row key={r.id} row={r} open={open === r.id} onToggle={() => setOpen((cur) => (cur === r.id ? null : r.id))} />
            ))}
          </section>
        );
      })}

      {rolesProblem && <FetchError subject={SUBJECT_ROLES} detail={rolesProblem} />}
      {straza.length > 0 && (
        <section className="overflow-hidden rounded-md border border-border" data-config-section="Console access">
          <div className="flex flex-wrap items-baseline gap-2 border-b border-border bg-card px-3 py-2">
            <h2 className="text-dense font-semibold text-foreground">{CONSOLE_ACCESS_TITLE}</h2>
            <span className="text-caption text-muted-foreground">{CONSOLE_ACCESS_LINE}</span>
          </div>
          {straza.map((role) => (
            <div key={role.id} className="flex flex-wrap items-center gap-3 border-b border-border px-3 py-2.5 text-dense last:border-b-0" data-console-role={role.name}>
              <span className="flex min-w-0 flex-1 basis-[240px] flex-col">
                <span className="font-mono font-medium text-foreground">{role.name}</span>
                <span className="text-caption text-muted-foreground">{holders(role.holder_count || 0)}</span>
              </span>
              <span className="text-dense text-text-2">{consoleAreasWords(role.areas)}</span>
              <span className="ml-auto shrink-0">
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button variant="ghost" size="icon-xs" aria-label={openRole(role.name)} onClick={() => navigate("roles", [role.id])}>
                      <ChevronRightIcon />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{openRole(role.name)}</TooltipContent>
                </Tooltip>
              </span>
            </div>
          ))}
        </section>
      )}

      <DraftSheet open={draftOpen} rows={rows.filter((r) => r.section !== "Entra")} onClose={() => setDraftOpen(false)} />
    </div>
  );
}

// Row is one setting: a closed line that is the name and the value with
// its posture chip, and the block it opens in place.
function Row({ row, open, onToggle }: { row: ConfigRow; open: boolean; onToggle: () => void }) {
  return (
    <div id={"config-" + row.id} className="border-b border-border last:border-b-0" data-config-row={row.id}>
      <button
        type="button"
        aria-expanded={open}
        onClick={onToggle}
        className="flex w-full items-center gap-3 px-3 py-2.5 text-left text-dense outline-none hover:bg-secondary/60 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset"
      >
        <span className="min-w-0 flex-1 font-medium text-foreground">{row.name}</span>
        <span className="flex min-w-0 max-w-[60%] flex-wrap items-center justify-end gap-2">
          {row.posture === "relaxed" && <Chip tone="warn" data-posture={RELAXED_BADGE}>{RELAXED_BADGE}</Chip>}
          <span className="min-w-0 text-right font-mono text-caption break-all text-foreground" data-config-value={row.id}>{row.value}</span>
        </span>
        <ChevronRightIcon className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")} aria-hidden="true" />
      </button>
      {open && <Opened row={row} />}
    </div>
  );
}

// Opened is a row's open block: its sentence, where its value is set and
// when a change takes effect. A row whose value the answer left out says
// so and shows no facts, and the two rows the policy sets decide name them.
// A key the answer carries no value for, such as a TLS file path, shows
// and copies the key alone.
function Opened({ row }: { row: ConfigRow }) {
  const keys = row.key ? row.key.split(",").map((k) => k.trim()) : [];
  const envs = row.env ? row.env.split(",").map((v) => v.trim()) : [];
  // A value the answer left out, or one the file does not set, shows and
  // copies the key alone.
  const unset = row.raw === "";
  const has = row.raw !== undefined && !unset;
  return (
    <div className="flex flex-col gap-3 px-3 pt-1 pb-3 pl-6">
      <p className="max-w-[75ch] text-body leading-relaxed text-foreground" data-row-help={row.id}>{row.help}</p>
      {row.value !== VALUE_UNAVAILABLE && (
        <dl className="grid grid-cols-1 gap-x-4 gap-y-1 text-dense sm:grid-cols-[auto_minmax(0,1fr)] sm:gap-y-2" data-row-facts={row.id}>
          {keys.length > 0 ? (
            <>
              <dt className="text-muted-foreground">{FACT.file}</dt>
              <dd className="flex min-w-0 flex-col gap-1">
                {keys.map((k) => <CopyLine key={k} text={has ? k + ": " + String(row.raw) : k} copy={has ? fileLine(row) : k} label={copyLabel(k)} />)}
                {unset && <span className="text-text-2" data-not-set>{NOT_SET}</span>}
              </dd>
              <dt className="text-muted-foreground">{FACT.env}</dt>
              <dd className="flex min-w-0 flex-col gap-1">
                {envs.length > 0
                  ? envs.map((v) => {
                    const line = has ? v + "=" + shellValue(String(row.raw)) : v;
                    return <CopyLine key={v} text={line} copy={line} label={envCopyLabel(v)} />;
                  })
                  : <span className="text-text-2">{NO_ENV}</span>}
              </dd>
              <dt className="text-muted-foreground">{FACT.effect}</dt>
              <dd className="text-foreground">{ON_RESTART}</dd>
            </>
          ) : (
            <>
              <dt className="text-muted-foreground">{FACT.from}</dt>
              <dd className="text-foreground">{FROM_SETS}</dd>
              <dt className="text-muted-foreground">{FACT.effect}</dt>
              <dd className="text-foreground">{ON_PUBLISH}</dd>
            </>
          )}
        </dl>
      )}
      {row.id === "floor" && (
        <a
          href={pathFor("settings", ["registry"])}
          className="inline-flex w-fit items-center gap-1 text-dense text-link underline-offset-4 hover:underline"
          onClick={(e) => {
            if (!isPlainClick(e)) return;
            e.preventDefault();
            navigate("settings", ["registry"]);
          }}
        >
          {OPEN_REGISTRY}
          <ArrowRightIcon className="size-3.5" aria-hidden="true" />
        </a>
      )}
    </div>
  );
}

// CopyLine is one mono line with the button that copies it, or copies the
// lines it stands for.
function CopyLine({ text, copy, label }: { text: string; copy: string; label: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1">
      <span className="min-w-0 font-mono text-caption break-all text-foreground">{text}</span>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button variant="ghost" size="icon-xs" aria-label={label} onClick={() => copyText(copy, LINE_COPIED)}><CopyIcon /></Button>
        </TooltipTrigger>
        <TooltipContent>{COPY_FRAGMENT}</TooltipContent>
      </Tooltip>
    </span>
  );
}
