import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { CAPS, CardGroup, ERROR, Field, HINT, OptionCard } from "@/components/wizard/parts";
import { DESC_HINT, OFFERED_SHORT, PICK_NONE, RPS_MISS, coveredWords, keptWords, noToolsYet, serverWide, timeoutMiss } from "@/lib/change-words";
import { type Settings, coveredBy } from "@/lib/manifest-edit";
import { cn } from "@/lib/utils";
import { type Miss, sayOf } from "./server-change-connection";

// settingsMisses lists what the Settings card cannot save without, in the
// order the card draws it: a picked list with nothing in it, a rate limit
// that is not a number above zero, a timeout that is not whole seconds.
export function settingsMisses(s: Settings, tools: string[], upstreamTimeout: number | null): Miss[] {
  const out: Miss[] = [];
  if (s.expose === "only" && s.picked.length + s.kept.length === 0) out.push({ id: tools.length ? "cs-tool-0" : "cs-tools", text: PICK_NONE });
  const rps = s.rps.trim();
  if (rps && !(Number.isFinite(Number(rps)) && Number(rps) > 0)) out.push({ id: "cs-rps", text: RPS_MISS });
  const timeout = s.timeout.trim();
  if (timeout && !(/^\d+$/.test(timeout) && Number(timeout) > 0)) out.push({ id: "cs-timeout", text: timeoutMiss(upstreamTimeout) });
  return out;
}

// short says the tool list is only what Straza already knows, because the
// server has no live instance to list what it offers.
type Props = { name: string; s: Settings; set: (patch: Partial<Settings>) => void; misses: Miss[]; tools: string[]; short: boolean; upstreamTimeout: number | null };

// SettingsFields is the Settings card's sheet: the description, the tools
// exposed as all or a picked list of what the server offers, and the two
// limits as numbers with their units. A tool a kept pattern already admits
// is ticked and left alone, since only the pattern could drop it.
export function SettingsFields({ name, s, set, misses, tools, short, upstreamTimeout }: Props) {
  const pickNone = sayOf(misses, "cs-tool-0") || sayOf(misses, "cs-tools");
  const rps = sayOf(misses, "cs-rps");
  const timeout = sayOf(misses, "cs-timeout");
  const free = (t: string) => !coveredBy(s.kept, t);
  // Opening the picked list with nothing picked starts from every tool a
  // pattern does not already admit, so the person unticks what to leave out.
  const expose = (v: string) => set(v === "only" && s.picked.length === 0 ? { expose: "only", picked: tools.filter(free) } : { expose: v === "only" ? "only" : "all" });
  const tick = (t: string, on: boolean) => set({ picked: tools.filter((x) => (x === t ? on : s.picked.includes(x))) });

  const limit = (id: string, label: string, value: string, key: "rps" | "timeout", unit: string, placeholder: string, error?: string) => (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className={CAPS}>{label}</label>
      <div className="flex items-center gap-2">
        <Input id={id} inputMode={key === "rps" ? "decimal" : "numeric"} placeholder={placeholder} autoComplete="off" className="w-28 font-mono"
          aria-invalid={!!error || undefined} aria-describedby={id + "-say"} value={value} onChange={(e) => set({ [key]: e.target.value })} />
        <span className={HINT}>{unit}</span>
      </div>
      {error && <p id={id + "-say"} className={ERROR}>{error}</p>}
    </div>
  );

  return (
    <>
      <Field id="cs-desc" label="Description" hint={DESC_HINT}>
        <Textarea id="cs-desc" rows={2} aria-describedby="cs-desc-say" value={s.desc} onChange={(e) => set({ desc: e.target.value })} />
      </Field>
      <div className="flex flex-col gap-2">
        <span className={CAPS}>Tools exposed</span>
        <CardGroup label="tools exposed" value={s.expose} onPick={expose}>
          <OptionCard value="all" label="expose all tools" name="All tools" tag="" line="Including ones the server adds later." on={s.expose === "all"} />
          <OptionCard value="only" label="expose only the tools I pick" name="Only the tools I pick" tag="" line="A tool the server adds later stays out until you pick it." on={s.expose === "only"}>
            {s.expose === "only" && (
              tools.length === 0
                ? <p id="cs-tools" tabIndex={-1} className={cn(HINT, "outline-none")}>{noToolsYet(name)}</p>
                : (
                  <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-x-3 gap-y-1.5" data-tool-picks>
                    {tools.map((t, i) => {
                      const pattern = coveredBy(s.kept, t);
                      return (
                        <label key={t} className="flex items-center gap-2 text-sm text-foreground" title={pattern ? coveredWords(t, pattern) : undefined}>
                          <input id={"cs-tool-" + i} type="checkbox" aria-label={t} className="size-4 accent-link" disabled={!!pattern}
                            aria-describedby={pattern ? "cs-covered-" + i : undefined}
                            checked={!!pattern || s.picked.includes(t)} onChange={(e) => tick(t, e.target.checked)} />
                          <span className="truncate font-mono">{t}</span>
                          {pattern && <span aria-hidden="true" className="truncate font-mono text-[13px] text-muted-foreground">{pattern}</span>}
                          {pattern && <span id={"cs-covered-" + i} className="sr-only">{coveredWords(t, pattern)}</span>}
                        </label>
                      );
                    })}
                  </div>
                )
            )}
          </OptionCard>
        </CardGroup>
        {s.expose === "only" && short && <p className={HINT}>{OFFERED_SHORT}</p>}
        {s.expose === "only" && s.kept.length > 0 && <p className={HINT}>{keptWords(s.kept)}</p>}
        {pickNone && <p className={ERROR}>{pickNone}</p>}
      </div>
      {limit("cs-rps", "Rate limit", s.rps, "rps", "calls per second, per session", "no limit", rps)}
      {limit("cs-timeout", "Per-call timeout", s.timeout, "timeout", "seconds; empty means " + serverWide(upstreamTimeout), upstreamTimeout ? String(upstreamTimeout) : "", timeout)}
    </>
  );
}
