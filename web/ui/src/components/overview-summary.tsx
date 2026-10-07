import { ArrowRightIcon, ChevronRightIcon, KeyRoundIcon, ServerIcon, SettingsIcon, ShieldIcon, UsersIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet";
import { Door, type ChainRead } from "@/components/overview-parts";
import type { OverviewAnswer } from "@/lib/api";
import {
  CHAIN_INTACT, CHAIN_SEAT, CHAIN_UNREAD, CHAIN_UNVERIFIED, CONFIG_DENIED, CONFIG_NOTE, CONFIG_PARTIAL, CONFIG_UNREAD, type ConfigRow, GUIDE, INVENTORY, NO_RECORDS_TO_VERIFY,
  OVERVIEW_TABS, PERMISSION_REQUIRED, REVIEW_SETTINGS, SETTING_LABEL, VALUE_UNAVAILABLE, chainBrokenDetail, lastChecked, nfmt, toReview, valuesLastRead,
} from "@/lib/config-words";
import { isPlainClick, navigate, pathFor } from "@/lib/router";

// Inventory exposes current counts without mixing them with lifetime totals.
export function Inventory({ answer, denied }: { answer: OverviewAnswer | null; denied: boolean }) {
  const a = answer || {};
  const rows = [
    { key: "servers", label: INVENTORY.servers, count: a.apps?.running, total: a.apps?.total, icon: ServerIcon },
    { key: "sessions", label: INVENTORY.sessions, count: a.sessions?.active, icon: KeyRoundIcon },
    { key: "users", label: INVENTORY.users, count: a.users?.active, icon: UsersIcon },
    { key: "policies", label: INVENTORY.policies, count: a.policies?.active, icon: ShieldIcon },
  ] as const;
  return <div className="overview-inventory" data-steer="OV-05">{rows.map((r) => <a key={r.key} href={pathFor(r.key)} data-inventory={r.key} onClick={(e) => { if (!isPlainClick(e)) return; e.preventDefault(); navigate(r.key); }}>
    <r.icon size={17} aria-hidden="true" /><span>{r.count === undefined ? <>{r.key === "servers" ? INVENTORY.serversShort : r.label}: <strong>{denied ? PERMISSION_REQUIRED : VALUE_UNAVAILABLE}</strong></> : <><strong>{nfmt(r.count)}{"total" in r && r.total !== undefined ? " of " + nfmt(r.total) : ""}</strong> {r.label}</>}</span><ChevronRightIcon size={13} aria-hidden="true" />
  </a>)}</div>;
}

const settingLabel = (r: ConfigRow) => r.id === "tls" ? SETTING_LABEL.tls
  : r.id === "floor" ? r.value === "none" ? SETTING_LABEL.floorNone : SETTING_LABEL.floorAdvisory
  : (r.short || r.name) + ": " + (r.postureValue || r.value);

// ConfigurationNote keeps actual exceptions visible without repeating the posture table.
export function ConfigurationNote({ rows, denied, stale, onDetails }: { rows: ConfigRow[] | null; denied: boolean; stale?: string; onDetails: () => void }) {
  const relaxed = rows?.filter((r) => r.posture === "relaxed") || [];
  const incomplete = !rows || rows.some((r) => r.value === VALUE_UNAVAILABLE);
  const title = denied ? CONFIG_NOTE.denied : incomplete ? CONFIG_NOTE.incomplete : relaxed.length ? toReview(relaxed.length) : CONFIG_NOTE.none;
  // why is the reason under a title that says the values are not all here,
  // and what to do about it.
  const why = denied ? CONFIG_DENIED : !incomplete ? "" : rows ? CONFIG_PARTIAL : CONFIG_UNREAD;
  return <section className="overview-config-note" data-steer="OV-02">
    <SettingsIcon size={18} className={incomplete ? "text-unknown" : relaxed.length ? "text-warn" : "text-muted-foreground"} aria-hidden="true" />
    <div><h2>{title}</h2>{stale && <p className="overview-stale">{valuesLastRead(stale)}</p>}
      {why && <p data-config-why>{why}</p>}
      {relaxed.length > 0 && <p>{relaxed.map(settingLabel).join(" · ")}</p>}
      <Button variant="link" size="sm" className="overview-text-button" onClick={onDetails}>{REVIEW_SETTINGS} <ArrowRightIcon size={14} /></Button>
    </div>
  </section>;
}

// AuditStatus states the limited verification scope, including failure and unavailable states.
export function AuditStatus({ chain, stale, onDetails }: { chain: ChainRead; stale?: string; onDetails: () => void }) {
  const line = chain.word === "intact" ? chain.seq ? CHAIN_INTACT : NO_RECORDS_TO_VERIFY
    : chain.word === "broken" ? chainBrokenDetail(chain.seq)
    : chain.word === "seat" ? CHAIN_SEAT : chain.word === "unverified" ? CHAIN_UNVERIFIED : CHAIN_UNREAD;
  return <div className="overview-audit-status"><span className={chain.word === "broken" ? "text-danger" : undefined}><ShieldIcon size={15} aria-hidden="true" />{stale ? lastChecked(stale) : ""}{line}</span><Button variant="link" size="sm" className="overview-text-button" onClick={onDetails}>{OVERVIEW_TABS.system} <ArrowRightIcon size={14} /></Button></div>;
}

// AccessGuide preserves the complete path from server configuration to a real audited call.
export function AccessGuide() {
  return <Sheet><SheetTrigger asChild><Button variant="link" size="sm" className="overview-text-button">{GUIDE.open} <ArrowRightIcon size={14} /></Button></SheetTrigger>
    <SheetContent className="overflow-y-auto"><SheetHeader><SheetTitle>{GUIDE.title}</SheetTitle><SheetDescription>{GUIDE.lede}</SheetDescription></SheetHeader>
      <ol className="overview-access-guide" data-access-guide>
        {GUIDE.steps.map((s) => <li key={s.to}>{s.line} <Door to={s.to} /></li>)}
      </ol>
    </SheetContent>
  </Sheet>;
}
