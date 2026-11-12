import * as React from "react";
import { CheckIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { type SigningKeyRow, usesEntra } from "@/lib/keys-api";
import {
  BLUEPRINT,
  BLUEPRINT_COPIED,
  COPY,
  DOWNLOAD_CERT,
  ENTRA_LISTS,
  LINK_LINE,
  MARK_REMOVED,
  MARKED_REMOVED,
  OPEN_USERS,
  PROMOTE,
  PROMOTE_READY,
  PROMOTED_LINE,
  ROTATING,
  STEP_LINK,
  STEP_PROMOTE,
  STEP_PROMOTED,
  STEP_STAGED,
  STEP_UPLOAD,
  THUMB_COPIED,
  certFile,
  promoteWait,
  promotedTitle,
  removeOldLine,
  removeOldTitle,
  retiresLine,
  stagedLine,
  uploadLine,
} from "@/lib/keys-words";
import { cn } from "@/lib/utils";

// The rotation panel of the Agent signing keys tab: the steps of a
// rotation in the order that keeps Entra working, drawn from the key list
// alone, so a reload or a second administrator sees the same rotation.
// Straza cannot see the Entra blueprint, so the upload step shows the
// certificate's thumbprint for the person to compare with the row Entra
// lists. When no provider uses Entra, the panel has no Entra step at all.

// PROMOTE_AFTER_MS is the server's own wait before a staged key can be
// promoted, two key reload intervals, so every replica publishes it first.
export const PROMOTE_AFTER_MS = 60_000;

const MUTED = "text-sm text-muted-foreground";

function Step({ n, done, title, children }: { n: number; done: boolean; title: string; children?: React.ReactNode }) {
  return (
    <li className="grid grid-cols-[24px_1fr] items-start gap-2.5" data-step={n} data-done={done || undefined}>
      <span className={cn("mt-0.5 grid size-[22px] place-items-center rounded-full border text-xs font-semibold", done ? "border-ok bg-ok text-white" : "border-link text-link")}>
        {done ? <CheckIcon className="size-3.5" /> : n}
      </span>
      <div className="flex min-w-0 flex-col gap-1.5">
        <p className="text-sm font-semibold text-foreground">{title}</p>
        {children}
      </div>
    </li>
  );
}

// Copyable is a value with its copy button, or nothing while it is not
// known, so no wrong value is ever on screen. attr names the value for
// tests, as data-thumbprint or data-blueprint.
function Copyable({ label, value, copied, attr }: { label?: string; value: string | null | undefined; copied: string; attr: string }) {
  if (!value) return null;
  return (
    <span className="flex flex-wrap items-center gap-2 text-[13px]" {...{ [attr]: value }}>
      {label && <span className="text-muted-foreground">{label}</span>}
      <code className="break-all rounded border border-border bg-muted px-1.5 font-mono">{value}</code>
      <Button variant="outline" size="xs" onClick={() => copyText(value, copied)}>{COPY}</Button>
    </span>
  );
}

type StagedProps = {
  staged: SigningKeyRow;
  active: SigningKeyRow | null;
  // blueprints are the providers' Entra blueprints, empty when none uses
  // Entra and null when that is not known.
  blueprints: string[] | null;
  thumb: string | null | undefined;
  downloaded: boolean;
  onDownload: () => void;
  onPromote: () => void;
};

// StagedPanel is the rotation while a key is staged: staged, upload, then
// promote once the server's wait is over, counted down to the second.
// Without Entra it is staged, then promote.
export function StagedPanel({ staged, active, blueprints, thumb, downloaded, onDownload, onPromote }: StagedProps) {
  const readyAt = Date.parse(staged.created_at) + PROMOTE_AFTER_MS;
  const [now, setNow] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (Date.now() >= readyAt) return;
    const t = setInterval(() => {
      setNow(Date.now());
      if (Date.now() >= readyAt) clearInterval(t);
    }, 1000);
    return () => clearInterval(t);
  }, [readyAt]);
  const wait = Math.max(0, Math.ceil((readyAt - now) / 1000));
  const entra = usesEntra(blueprints);
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-link bg-card px-4 py-3" data-rotation="staged">
      <h3 className="text-[15px] font-semibold">{ROTATING}</h3>
      <ol className="flex flex-col gap-3">
        <Step n={1} done title={STEP_STAGED}><p className={MUTED}>{stagedLine(staged.kid, active ? active.kid : null)}</p></Step>
        {entra && (
          <Step n={2} done={downloaded} title={STEP_UPLOAD}>
            <p className={MUTED}>{uploadLine(blueprints)}</p>
            {(blueprints || []).map((b) => <Copyable key={b} label={BLUEPRINT} value={b} copied={BLUEPRINT_COPIED} attr="data-blueprint" />)}
            <span className="flex flex-wrap items-center gap-2">
              <Button size="sm" variant={downloaded ? "outline" : "default"} onClick={onDownload}>{DOWNLOAD_CERT}</Button>
              <span className="text-[13px] text-muted-foreground">{certFile(staged.kid)}</span>
            </span>
            <Copyable label={ENTRA_LISTS} value={thumb} copied={THUMB_COPIED} attr="data-thumbprint" />
          </Step>
        )}
        <Step n={entra ? 3 : 2} done={false} title={STEP_PROMOTE}>
          <p className={MUTED} data-promote-wait={wait}>{wait > 0 ? promoteWait(wait) : PROMOTE_READY}</p>
          <span><Button size="sm" variant={wait > 0 ? "outline" : "default"} disabled={wait > 0} onClick={onPromote}>{PROMOTE}</Button></span>
        </Step>
      </ol>
    </section>
  );
}

type RetiringProps = {
  active: SigningKeyRow | null;
  retiring: SigningKeyRow;
  blueprints: string[] | null;
  thumb: string | null | undefined;
  leaves: number | null;
  removed: boolean;
  onRemoved: () => void;
  onUsers: () => void;
};

// RetiringPanel is what is left after a promote, while the previous key is
// retiring: remove its certificate from the blueprint, then link agents.
// Without Entra it only says when the previous key retires.
export function RetiringPanel({ active, retiring, blueprints, thumb, leaves, removed, onRemoved, onUsers }: RetiringProps) {
  const title = <h3 className="text-[15px] font-semibold">{active ? promotedTitle(active.kid) : STEP_PROMOTED}</h3>;
  if (!usesEntra(blueprints)) {
    return (
      <section className="flex flex-col gap-3 rounded-lg border border-link bg-card px-4 py-3" data-rotation="retiring">
        {title}
        <ol className="flex flex-col gap-3">
          <Step n={2} done title={STEP_PROMOTED}><p className={MUTED}>{PROMOTED_LINE + " " + retiresLine(retiring.kid, leaves)}</p></Step>
        </ol>
      </section>
    );
  }
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-link bg-card px-4 py-3" data-rotation="retiring">
      {title}
      <ol className="flex flex-col gap-3">
        <Step n={3} done title={STEP_PROMOTED}><p className={MUTED}>{PROMOTED_LINE}</p></Step>
        <Step n={4} done={removed} title={removeOldTitle(blueprints)}>
          <p className={MUTED}>{removeOldLine(retiring.kid, leaves)}</p>
          <span className="flex flex-wrap items-center gap-2">
            <Copyable value={thumb} copied={THUMB_COPIED} attr="data-thumbprint" />
            {removed
              ? <span className="text-[13px] text-muted-foreground">{MARKED_REMOVED}</span>
              : <Button variant="outline" size="xs" onClick={onRemoved}>{MARK_REMOVED}</Button>}
          </span>
        </Step>
        <Step n={5} done={false} title={STEP_LINK}>
          <p className={MUTED}>{LINK_LINE}</p>
          <span><Button variant="outline" size="sm" onClick={onUsers}>{OPEN_USERS}</Button></span>
        </Step>
      </ol>
    </section>
  );
}
