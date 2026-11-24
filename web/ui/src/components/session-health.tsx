import type * as React from "react";
import { ChevronRightIcon, type LucideIcon, ShieldAlertIcon, ShieldCheckIcon } from "lucide-react";
import { Facts } from "@/components/draft-parts";
import { SessionCard } from "@/components/session-card";
import type { SessionRow } from "@/lib/api";
import { CLIENT_ROW, HEALTH, HOOK_ROW, IDS_AND_HASHES, ID_FACT, NOT_MEASURED_ROW, healthOf } from "@/lib/session-words";
import { absTime } from "@/lib/words";
import { cn } from "@/lib/utils";

type Look = { icon: LucideIcon; tone: string };
const OK: Look = { icon: ShieldCheckIcon, tone: "text-ok" };
const WARN: Look = { icon: ShieldAlertIcon, tone: "text-warn" };
const DANGER: Look = { icon: ShieldAlertIcon, tone: "text-danger" };
const CLIENT_LOOK: Record<string, Look> = { managed: OK, advisory: WARN, none: DANGER };
const HOOK_LOOK: Record<string, Look> = { current: OK, allowed: WARN, unmeasured: WARN, mismatch: DANGER };

// HealthRow is one row of the card: an icon in its tone when there is one,
// a line in 600 weight and a line in the second text colour.
function HealthRow({ pair, look, ...data }: { pair: [string, string]; look?: Look; [key: `data-${string}`]: string }) {
  const Icon = look ? look.icon : null;
  return (
    <div className="flex min-w-0 items-start gap-2.5" {...data}>
      {Icon && look && <Icon className={cn("mt-0.5 size-4 shrink-0", look.tone)} aria-hidden="true" />}
      <div className="min-w-0">
        <p className="m-0 text-dense font-semibold text-foreground">{pair[0]}</p>
        <p className="m-0 text-dense text-text-2">{pair[1]}</p>
      </div>
    </div>
  );
}

// SessionHealth is the Health card: the client and the hook configuration
// as the session measured them, or one row for a session that runs no
// agent, then the ids and hashes in a closed fold.
export function SessionHealth({ row }: { row: SessionRow }) {
  const admin = healthOf(row).level === "plain";
  const att = Object.hasOwn(CLIENT_ROW, row.attestation) ? row.attestation : "none";
  const wiring = row.wiring_status && Object.hasOwn(HOOK_ROW, row.wiring_status) ? row.wiring_status : "unmeasured";
  const mono = (v: string) => <span className="font-mono [overflow-wrap:anywhere]">{v}</span>;
  const facts: { label: string; value: React.ReactNode }[] = [
    { label: ID_FACT.id, value: mono(row.id) },
    ...(row.client_version ? [{ label: ID_FACT.client, value: mono(row.client_version) }] : []),
    ...(row.wiring_hash ? [{ label: ID_FACT.hook, value: mono(row.wiring_hash) }] : []),
    { label: ID_FACT.started, value: absTime(row.started_at) },
    { label: ID_FACT.seen, value: absTime(row.last_seen) },
  ];
  return (
    <SessionCard title={HEALTH} data-health-card>
      {admin ? (
        <HealthRow pair={NOT_MEASURED_ROW} data-health-row="admin" />
      ) : (
        <>
          <HealthRow pair={CLIENT_ROW[att]} look={CLIENT_LOOK[att]} data-health-row={"client-" + att} />
          <HealthRow pair={HOOK_ROW[wiring]} look={HOOK_LOOK[wiring]} data-health-row={"hook-" + wiring} />
        </>
      )}
      <details className="group" data-ids-fold>
        <summary className="inline-flex cursor-pointer list-none items-center gap-1 rounded-md text-dense font-semibold text-text-2 outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
          <ChevronRightIcon className="size-4 transition-transform group-open:rotate-90" aria-hidden="true" />
          {IDS_AND_HASHES}
        </summary>
        <div className="mt-2">
          <Facts rows={facts} />
        </div>
      </details>
    </SessionCard>
  );
}
