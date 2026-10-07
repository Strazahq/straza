import * as React from "react";
import { ChevronRightIcon, CopyIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { FetchError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import { DraftSheet } from "@/components/settings-draft";
import { WordBadge } from "@/components/users-table";
import { type ApiError, type RoleRow, getConfig, listRoles } from "@/lib/api";
import {
  CAPTURE_SUB,
  CONFIG_LEDE,
  CONFIG_WHY,
  CONSOLE_ACCESS_LINE,
  CONSOLE_ACCESS_TITLE,
  LINE_COPIED,
  READING_CONFIG,
  RELAXED_BADGE,
  SECTIONS,
  SUBJECT_CONFIG,
  type ConfigRow,
  configRows,
  consoleAreasWords,
  copyLabel,
  fileLine,
  openRole,
  setWords,
  whereWords,
} from "@/lib/config-words";
import { copyText } from "@/lib/clipboard";
import { navigate } from "@/lib/router";
import { SUBJECT_ROLES, kindOf } from "@/lib/role-words";
import { TABS } from "@/lib/settings-words";
import { readFailed } from "@/lib/say";
import { holders } from "@/lib/words";

// The Configuration tab of Settings: every setting this strazad runs on, in
// sections, read
// from the server and never written from here. A row says where its value
// is set, copies that line, and opens its own sentence under it. The
// Console access section closes the tab with the straza roles the config
// maps console areas to.

type Props = {
  // draftRequest is the page head's counter: Write a config change moves
  // it, and the tab opens the sheet when it moves.
  draftRequest: number;
};

type State =
  | { kind: "loading" }
  | { kind: "error"; message: string }
  | { kind: "ready"; rows: ConfigRow[]; lastRead: Date; problem: string | null };

// hashRow is the row id the address names (settings/configuration#floor),
// so a link from Overview opens that row's sentence.
function hashRow(): string | null {
  if (typeof window === "undefined") return null;
  const h = (window.location.hash || "").replace(/^#/, "");
  return h ? decodeURIComponent(h) : null;
}

export function ConfigTab({ draftRequest }: Props) {
  const [state, setState] = React.useState<State>({ kind: "loading" });
  const [roles, setRoles] = React.useState<RoleRow[] | null>(null);
  const [rolesProblem, setRolesProblem] = React.useState<string | null>(null);
  const [open, setOpen] = React.useState<string | null>(() => hashRow());
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

  // The head's Write a config change moves the counter; the first render of
  // the tab carries a zero and opens nothing.
  React.useEffect(() => { if (draftRequest > 0) setDraftOpen(true); }, [draftRequest]);

  // A row named in the address is scrolled to once its section is on
  // screen, so the link from Overview lands on the row it named.
  const rows = state.kind === "ready" ? state.rows : [];
  React.useEffect(() => {
    const id = hashRow();
    if (!id || !rows.length) return;
    const el = document.getElementById("config-" + id);
    if (el) el.scrollIntoView({ block: "center" });
  }, [rows.length]);

  if (state.kind === "loading") return <p className="text-sm text-muted-foreground">{READING_CONFIG}</p>;
  if (state.kind === "error") return <FetchError subject={SUBJECT_CONFIG} detail={state.message} />;

  const straza = (roles || []).filter((r) => kindOf(r) === "straza");

  return (
    <div className="flex flex-col gap-4">
      {state.problem && <FetchError subject={SUBJECT_CONFIG} detail={state.problem} lastRead={state.lastRead} />}
      <p className="flex max-w-[75ch] flex-wrap items-center gap-1.5 text-sm text-text-2" data-config-lede>
        {CONFIG_LEDE}
        <HelpTip label={TABS[0].label} text={CONFIG_WHY} />
      </p>

      {SECTIONS.map((section) => {
        const mine = rows.filter((r) => r.section === section);
        if (!mine.length) return null;
        return (
          <section key={section} className="overflow-hidden rounded-md border border-border" data-config-section={section}>
            <div className="flex flex-wrap items-baseline gap-2 border-b border-border bg-card px-3 py-2">
              <h2 className="text-sm font-semibold text-foreground">{section}</h2>
              {section === "Recording" && <span className="text-[13px] text-muted-foreground">{CAPTURE_SUB}</span>}
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
            <h2 className="text-sm font-semibold text-foreground">{CONSOLE_ACCESS_TITLE}</h2>
            <span className="text-[13px] text-muted-foreground">{CONSOLE_ACCESS_LINE}</span>
          </div>
          {straza.map((role) => (
            <div key={role.id} className="flex flex-wrap items-center gap-3 border-b border-border px-3 py-2.5 text-sm last:border-b-0" data-console-role={role.name}>
              <span className="flex min-w-0 flex-1 basis-[240px] flex-col">
                <span className="font-mono font-medium text-foreground">{role.name}</span>
                <span className="text-[13px] text-muted-foreground">{holders(role.holder_count || 0)}</span>
              </span>
              <span className="text-sm text-text-2">{consoleAreasWords(role.areas)}</span>
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

      <DraftSheet open={draftOpen} rows={rows} onClose={() => setDraftOpen(false)} />
    </div>
  );
}

// Row is one setting: the name that opens its sentence, where it is set,
// the value with its posture badge, the copy button for the config file
// line, and the help icon carrying the same sentence.
function Row({ row, open, onToggle }: { row: ConfigRow; open: boolean; onToggle: () => void }) {
  const where = whereWords(row);
  const set = setWords(row);
  const sentence = (set ? row.help + " " + set : row.help) + (row.key && !fileLine(row) ? " The current configuration value is unavailable or redacted, so it cannot be copied." : "");
  const line = row.key ? fileLine(row) : "";
  const copy = () => copyText(line, LINE_COPIED);
  return (
    <div id={"config-" + row.id} className="flex flex-wrap items-center gap-3 border-b border-border px-3 py-2.5 text-sm last:border-b-0" data-config-row={row.id}>
      <span className="flex min-w-0 flex-1 basis-[260px] flex-col">
        <button type="button" aria-expanded={open} onClick={onToggle} className="self-start text-left font-medium text-foreground underline-offset-4 outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50">
          {row.name}
        </button>
        <span className="truncate font-mono text-[13px] text-muted-foreground" title={where}>{where}</span>
      </span>
      <span className="flex items-center gap-2 font-mono text-[13px] text-foreground" data-config-value={row.id}>
        {row.value}
        {row.posture === "relaxed" && <WordBadge word={RELAXED_BADGE} tone="warn" attr="data-posture" />}
      </span>
      <span className="ml-auto flex shrink-0 items-center gap-1">
        {line && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon-xs" aria-label={copyLabel(row.key)} onClick={copy}><CopyIcon /></Button>
            </TooltipTrigger>
            <TooltipContent>{copyLabel(row.key)}</TooltipContent>
          </Tooltip>
        )}
        <HelpTip label={row.name} text={sentence} />
      </span>
      {open && <p className="max-w-[75ch] basis-full text-sm leading-relaxed text-text-2" data-row-help={row.id}>{sentence}</p>}
    </div>
  );
}
