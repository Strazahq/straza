import { Switch } from "@/components/ui/switch";
import { Panel, PanelNote, ToneBadge } from "@/components/overview-parts";
import type { AuditRow } from "@/lib/api";
import { EMPTY_CHAIN, ceRow, effectTone, effectWord, rowLabel } from "@/lib/audit-words";
import { FOLLOW, LIVE_HELP, LIVE_OFF, LIVE_TITLE, newSince, seatLine } from "@/lib/config-words";
import { NONE, absTime, relTimeText } from "@/lib/words";
import { cn } from "@/lib/utils";

// Live: the newest audit records as sentences while the
// switch is on, with the decision word and a pulse on the newest. Off by
// default, remembered per browser, and one line says what it does.

const KEY = "straza.overview.live";

// readFollow answers whether this browser left the switch on. A browser
// that refuses storage, such as a private window, reads as off.
export function readFollow(): boolean {
  try {
    return window.localStorage.getItem(KEY) === "on";
  } catch {
    return false;
  }
}

// saveFollow remembers the choice; a refused write keeps it for this page.
export function saveFollow(on: boolean) {
  try {
    window.localStorage.setItem(KEY, on ? "on" : "off");
  } catch {
    // Private mode: the choice lasts this page only.
  }
}

type Props = {
  on: boolean;
  onChange: (on: boolean) => void;
  // rows are the newest records, newest first; null while the switch is on
  // and nothing has answered yet.
  rows: AuditRow[] | null;
  // fresh counts the records that landed since the switch was turned on.
  fresh: number;
  denied: boolean;
};

// Live renders the panel and its switch.
export function Live({ on, onChange, rows, fresh, denied }: Props) {
  return (
    <Panel
      name="live"
      reveal
      title={LIVE_TITLE}
      help={LIVE_HELP}
      right={
        <>
          {on && fresh > 0 && <span data-new-since>{newSince(fresh)}</span>}
          <label className="flex cursor-pointer items-center gap-2 text-[13px] text-text-2">
            <Switch checked={on} onCheckedChange={onChange} aria-label={FOLLOW} />
            {FOLLOW}
          </label>
        </>
      }
      flush={on && !!rows && rows.length > 0}
    >
      {!on ? (
        <PanelNote>{LIVE_OFF}</PanelNote>
      ) : denied ? (
        <PanelNote>{seatLine(LIVE_TITLE, "audit:read")}</PanelNote>
      ) : !rows ? null : rows.length === 0 ? (
        <PanelNote>{EMPTY_CHAIN}</PanelNote>
      ) : (
        <div className="flex flex-col">
          {rows.map((raw, i) => {
            const r = ceRow(raw);
            const tone = effectTone(r.effect);
            return (
              <div key={r.seq} data-live={r.seq} className="flex flex-wrap items-center gap-2.5 border-b border-border px-3.5 py-2 text-sm last:border-b-0">
                <span
                  className={cn("size-2 shrink-0 rounded-full", i === 0 ? "bg-primary motion-safe:animate-pulse" : "bg-border")}
                  data-pulse={i === 0 ? "true" : undefined}
                  aria-hidden="true"
                />
                <b className="font-semibold text-foreground">{r.username || NONE}</b>
                <span className="min-w-0 flex-1 font-mono text-[13px] text-text-2">{rowLabel(r)}</span>
                {r.effect && <ToneBadge tone={tone} word={effectWord(r.effect)} />}
                <span className="text-[13px] whitespace-nowrap text-muted-foreground" title={absTime(r.time)}>{relTimeText(r.time)}</span>
              </div>
            );
          })}
        </div>
      )}
    </Panel>
  );
}
