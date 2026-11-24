import * as React from "react";
import { ArrowRightIcon, FileIcon, UserRoundIcon } from "lucide-react";
import { Pill, Tag } from "@/components/chip";
import { KIND_ICON } from "@/components/kind-icon";
import { Button } from "@/components/ui/button";
import type { DraftPrincipal } from "@/lib/api";
import type { Kind } from "@/lib/chip-words";
import {
  NOBODY_YET,
  NO_SPONSOR,
  SHOW_FEWER,
  SPONSOR_YOU,
  TOKEN_PILL,
  UPGRADE_NAME,
  UPGRADE_PILL,
  doorPill,
  fileOf,
  holderCount,
  moreCount,
  sponsoredBy,
} from "@/lib/drafts-words";
import { routeByKey } from "@/lib/routes";

// The small parts of a draft's page that its head, In short and its cards
// share: the facts grid, a list that shows five and opens the rest, the
// arrow between a before and an after, the tag of an object, of an author
// and of a publisher, and the pills that follow them.

// Facts is a grid of labelled facts: one label column 128 px wide from the
// sm width up, a label above its value below it, and values that wrap as
// whole pills. A row whose label is empty continues the row above it.
export function Facts({ rows }: { rows: { key?: string; label: string; value: React.ReactNode }[] }) {
  return (
    <dl className="m-0 grid gap-x-4 gap-y-2 sm:grid-cols-[128px_minmax(0,1fr)]">
      {rows.map((r) => (
        <div key={r.key || r.label} className="contents" data-fact={r.label || undefined}>
          <dt className="pt-0.5 text-caption leading-5 text-muted-foreground">{r.label}</dt>
          <dd className="m-0 flex min-w-0 flex-wrap items-center gap-2 text-dense text-foreground">{r.value}</dd>
        </div>
      ))}
    </dl>
  );
}

// FiveMore shows the first five of a list, then a quiet button that opens
// the rest in place and closes them again.
export function FiveMore({ items, limit = 5 }: { items: React.ReactNode[]; limit?: number }) {
  const [open, setOpen] = React.useState(false);
  const rest = items.length - limit;
  return (
    <>
      {rest > 0 && !open ? items.slice(0, limit) : items}
      {rest > 0 && (
        <Button variant="ghost" size="xs" className="text-link" aria-expanded={open} onClick={() => setOpen((o) => !o)} data-more={rest}>
          {open ? SHOW_FEWER : moreCount(rest)}
        </Button>
      )}
    </>
  );
}

// Arrow stands between a value before and a value after.
export const Arrow = () => <ArrowRightIcon className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />;

export const Muted = ({ children }: { children: React.ReactNode }) => <span className="text-caption text-muted-foreground">{children}</span>;

// ObjectTag names an object with its kind icon, and with the Roles icon for
// a role whose kind the page does not know. A name wider than its row is
// cut short on a narrow window, whole on hover.
export function ObjectTag({ kind, name }: { kind: Kind | null; name: string }) {
  return (
    <Tag icon={kind ? KIND_ICON[kind] : routeByKey("roles").icon} title={name} className="max-w-full" data-tag={name}>
      <span className="min-w-0 truncate">{name}</span>
    </Tag>
  );
}

// PrincipalTag names who drafted or decided a draft: an agent in teal, an
// admin API token, a person, or the apps directory file a draft came from.
// Straza's upgrade has no name of its own, so its pill stands in place.
export function PrincipalTag({ p, source }: { p: DraftPrincipal; source?: string }) {
  if (p.via === "upgrade") return <Pill data-author="upgrade">{UPGRADE_NAME}</Pill>;
  if (p.via === "file") return <Tag icon={FileIcon} data-author="file">{fileOf(source)}</Tag>;
  if (p.agent) return <Tag icon={KIND_ICON.agent} agent data-author="agent">{p.username}</Tag>;
  if (p.via === "api-token") return <Tag icon={KIND_ICON["api-token"]} data-author="api-token">{p.username}</Tag>;
  return <Tag icon={KIND_ICON.person} data-author="person">{p.username}</Tag>;
}

// AuthorPill follows the author's tag: who sponsors an agent, what an admin
// API token or Straza's upgrade is, and for anyone else the door the draft
// came in through. me is the reader's username.
export function AuthorPill({ p, me, door }: { p: DraftPrincipal; me: string; door: string }) {
  let text = doorPill(door);
  if (p.via === "upgrade") text = UPGRADE_PILL;
  else if (p.via !== "file" && p.agent) text = p.sponsor ? (p.sponsor === me ? SPONSOR_YOU : sponsoredBy(p.sponsor)) : NO_SPONSOR;
  else if (p.via === "api-token") text = TOKEN_PILL;
  return <Pill data-author-pill>{text}</Pill>;
}

// HoldersPill says how many hold a role: nobody yet, or the count.
export function HoldersPill({ n }: { n: number }) {
  return n === 0 ? <Pill data-holders={NOBODY_YET}>{NOBODY_YET}</Pill> : <Pill icon={UserRoundIcon} data-holders={holderCount(n)}>{holderCount(n)}</Pill>;
}
