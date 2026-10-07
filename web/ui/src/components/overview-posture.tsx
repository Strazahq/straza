import { Badge } from "@/components/ui/badge";
import { Door, Panel, PanelNote } from "@/components/overview-parts";
import {
  ALL_STRICT,
  CONFIG_PARTIAL,
  CONFIG_UNREAD,
  type ConfigRow,
  POSTURE_HELP,
  POSTURE_TITLE,
  VALUE_UNAVAILABLE,
  notesLine,
  postureBadge,
  postureIncomplete,
  postureOf,
  seatLine,
  setLink,
  strictLine,
} from "@/lib/config-words";
import { TABS } from "@/lib/settings-words";

// Posture: each relaxed setting in words with
// its value and one clause of what it costs, then the strict ones folded
// into one line and the choices worth knowing under it. Nothing here can
// be changed in a browser, so every row ends in the door to the row in
// Settings that says where to set it.

const CONFIG_TAB = TABS.find((t) => t.key === "configuration");

type Props = {
  // rows is the configuration read as words, null when the read did not
  // answer or this seat may not make it.
  rows: ConfigRow[] | null;
  denied: boolean;
  expanded?: boolean;
};

// Posture renders the panel. The badge counts the relaxed settings, so the
// header alone answers how strict this server is.
export function Posture({ rows, denied, expanded }: Props) {
  const read = rows ? postureOf(rows) : null;
  const relaxed = read ? read.relaxed : [];
  const incomplete = !rows || rows.some((r) => r.value === VALUE_UNAVAILABLE);
  const badge = (
    <Badge
      variant="outline"
      data-posture={incomplete ? "unknown" : relaxed.length ? "relaxed" : "strict"}
      className={"rounded-md px-2 font-mono text-[13px] font-normal " + (incomplete ? "border-border text-muted-foreground" : relaxed.length ? "border-warn/40 bg-warn-bg text-warn" : "border-ok/40 bg-ok-bg text-ok")}
    >
      {incomplete ? postureIncomplete(relaxed.length) : postureBadge(relaxed.length)}
    </Badge>
  );

  return (
    <Panel
      name="posture"
      reveal={expanded}
      title={POSTURE_TITLE}
      help={POSTURE_HELP}
      lead={read ? badge : undefined}
      right={CONFIG_TAB ? <Door to="settings" rest={[CONFIG_TAB.key]} label={CONFIG_TAB.label} /> : undefined}
    >
      {!read ? (
        <PanelNote>{denied ? seatLine(POSTURE_TITLE, "config:read") : CONFIG_UNREAD}</PanelNote>
      ) : (
        <div className="flex flex-col">
          {relaxed.map((r) => (
            <div key={r.id} data-relaxed={r.id} className="grid grid-cols-[1fr_auto] gap-x-3 gap-y-1 border-b border-border py-2.5 text-sm first:pt-0 last:border-b-0 last:pb-0">
              <span className="min-w-0 text-foreground">{r.short || r.name}</span>
              <span className="text-right font-mono font-semibold text-warn">{r.postureValue || r.value}</span>
              <span className="col-span-2 max-w-[62ch] text-[13px] leading-relaxed text-text-2">
                {r.cost + " "}
                <Door to="settings" rest={CONFIG_TAB ? [CONFIG_TAB.key] : []} hash={r.id} label={setLink(r)} arrow={false} />
              </span>
            </div>
          ))}
          <p className="border-b border-border py-2.5 text-[13px] leading-relaxed text-muted-foreground first:pt-0 last:border-b-0 last:pb-0" data-strict-line>
            {read.strictWords.length ? strictLine(read.strictWords) : incomplete ? CONFIG_PARTIAL : ALL_STRICT}
          </p>
          {incomplete && read.strictWords.length > 0 && (
            <p className="border-b border-border py-2.5 text-[13px] leading-relaxed text-muted-foreground last:border-b-0 last:pb-0" data-partial-line>{CONFIG_PARTIAL}</p>
          )}
          {read.notes.length > 0 && (
            <p className="py-2.5 text-[13px] leading-relaxed text-muted-foreground last:pb-0" data-notes-line>{notesLine(read.notes)}</p>
          )}
        </div>
      )}
    </Panel>
  );
}
