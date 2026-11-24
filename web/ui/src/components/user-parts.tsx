import type * as React from "react";
import { Chip, StatusChip, Tag } from "@/components/chip";
import { FetchError } from "@/components/error-state";
import { KIND_ICON, KindTile } from "@/components/kind-icon";
import { MARKS, markOf } from "@/components/role-kind";
import type { RoleRow, UserRow } from "@/lib/api";
import { KIND_WORD } from "@/lib/chip-words";
import type { Read } from "@/lib/user-read";
import { userKind, userState } from "@/lib/user-look";
import { type RouteKey, routeByKey } from "@/lib/routes";
import { isPlainClick, navigate, pathFor } from "@/lib/router";

// The small parts the page of one user shares between its head and its
// tabs: the word or the failure of a read, a link to a page, the tag of a
// role, the sponsor's name, the kind chip and the state chips.

export const MUTED = "text-caption text-muted-foreground";

// Waiting renders what a read has to say while it is out or after it
// failed, and nothing once it answered or when the page does not make it.
export function Waiting<T>({ read, word, subject }: { read: Read<T>; word: string; subject: string }) {
  if (read.kind === "loading") return <p className={MUTED} role="status">{word}</p>;
  if (read.kind === "failed") return <FetchError subject={subject} detail={read.message} />;
  return null;
}

// PageLink is a link to a page of the console that a plain click opens in
// place and a middle click opens in a new tab.
export function PageLink({ to, rest, className, children, ...data }: { to: RouteKey; rest: string[]; className?: string; children: React.ReactNode; [key: `data-${string}`]: string | undefined }) {
  return (
    <a
      href={pathFor(to, rest)}
      className={className || "text-link underline-offset-4 hover:underline"}
      onClick={(e) => { if (!isPlainClick(e)) return; e.preventDefault(); navigate(to, rest); }}
      {...data}
    >
      {children}
    </a>
  );
}

// RoleTag names a role with its kind icon, as a link to the role's page
// when the catalog holds it. A name wider than its place is cut short on a
// narrow window, whole on hover.
export function RoleTag({ name, role, icon }: { name: string; role: RoleRow | null | undefined; icon?: React.ComponentProps<typeof Tag>["icon"] }) {
  const tag = (
    <Tag icon={icon || (role ? KIND_ICON[MARKS[markOf(role)]] : routeByKey("roles").icon)} title={name} className="max-w-full" data-role-tag={name}>
      <span className="min-w-0 truncate">{name}</span>
    </Tag>
  );
  return role ? <PageLink to="roles" rest={[role.id]} className="inline-flex max-w-full rounded-md hover:opacity-80">{tag}</PageLink> : tag;
}

// SponsorName is an agent's sponsor: a link to the sponsor's page with a
// small person tile once the sponsor's id was found, the name as plain
// text until then and when it was not.
export function SponsorName({ name, id }: { name: string; id: string | null }) {
  if (!id) return <span data-sponsor={name}>{name}</span>;
  return (
    <span className="inline-flex items-center gap-1 align-middle" data-sponsor={name}>
      <KindTile kind="person" size="sm" />
      <PageLink to="users" rest={[id]}>{name}</PageLink>
    </span>
  );
}

// KindChip says what the user is. Teal marks an AI agent and nothing else.
export function KindChip({ user }: { user: Pick<UserRow, "kind" | "user_type"> }) {
  const kind = userKind(user);
  return <Chip tone={kind === "agent" ? "teal" : "plain"} icon={KIND_ICON[kind]} data-kind-chip={kind}>{KIND_WORD[kind]}</Chip>;
}

// StateChips is the state of a user: active when neither disabled nor
// locked, else the disabled chip, then the locked chip when there is a
// lock. A status word that is neither active nor disabled keeps a danger
// chip with the server's word.
export function StateChips({ user }: { user: Pick<UserRow, "status" | "locks"> }) {
  const s = userState(user);
  const odd = user.status !== "active" && user.status !== "disabled";
  if (odd) return <Chip tone="danger" data-user-status={user.status}>{user.status}</Chip>;
  if (!s.disabled && s.locks.length === 0) return <StatusChip status="active" data-user-status="active" />;
  return (
    <>
      {s.disabled && <StatusChip status="disabled" data-user-status="disabled" />}
      {s.locks.length > 0 && <StatusChip status="locked" data-user-status="locked" />}
    </>
  );
}

// Heading is a heading of the page's tabs, in the section size.
export function Heading({ children, ...data }: { children: React.ReactNode; [key: `data-${string}`]: string | boolean | undefined }) {
  return <h2 className="m-0 flex min-w-0 items-center gap-1.5 text-section leading-7 font-semibold [overflow-wrap:anywhere] text-foreground" {...data}>{children}</h2>;
}
