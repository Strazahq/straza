import type * as React from "react";
import { ChevronDownIcon, CircleSlashIcon, ExternalLinkIcon, LockIcon, PowerIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { opensAccount } from "@/lib/entra-words";
import { DISABLE, DISABLE_IN_ENTRA, LOCK, NO_ACTIVE_SESSION, REVOKE_ALL_SESSIONS, STOP_ACCESS, STOP_DISABLE_LINE, STOP_LOCK_LINE, stopSessionsLine } from "@/lib/user-words";

// Stop is a choice of the Stop access menu.
export type Stop = "sessions" | "lock" | "disable";

type Item = { stop: Stop; icon: React.ComponentType<{ className?: string }>; word: string; line: string; disabled: boolean };

type Props = {
  name: string;
  // sessions offers the sessions choice, for a reader with the sessions
  // area. active is the user's count of active sessions.
  sessions: boolean;
  active: number;
  locked: boolean;
  disabled: boolean;
  // entra is set when Entra owns the user, with the address of the account
  // in the Entra admin centre, or "" when the console cannot place it.
  entra: { url: string } | null;
  onStop: (stop: Stop) => void;
};

// StopMenu is the head's Stop access button and its menu. Each choice says
// what it does before it is picked and opens its confirm. A choice that
// does not apply is left out, and with none left the button is not drawn.
// Straza refuses to disable a user Entra owns, so Disable is then a link
// that opens the account in Entra.
export function StopMenu({ name, sessions, active, locked, disabled, entra, onStop }: Props) {
  const items: Item[] = [];
  if (sessions) items.push({ stop: "sessions", icon: PowerIcon, word: REVOKE_ALL_SESSIONS, line: active > 0 ? stopSessionsLine(name) : NO_ACTIVE_SESSION, disabled: active === 0 });
  if (!locked) items.push({ stop: "lock", icon: LockIcon, word: LOCK, line: STOP_LOCK_LINE, disabled: false });
  if (!disabled && !entra) items.push({ stop: "disable", icon: CircleSlashIcon, word: DISABLE, line: STOP_DISABLE_LINE, disabled: false });
  const inEntra = !disabled && entra && entra.url ? entra.url : "";
  if (items.length === 0 && !inEntra) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" data-stop-trigger>{STOP_ACCESS}<ChevronDownIcon /></Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" collisionPadding={16} className="w-[min(340px,calc(100vw-32px))]" data-stop-menu>
        {items.map((it) => (
          // A choice that does not apply keeps its words at full opacity:
          // its name muted, its reason in the second text colour.
          <DropdownMenuItem key={it.stop} disabled={it.disabled} onSelect={() => onStop(it.stop)} className="items-start gap-2.5 py-2 data-[disabled]:opacity-100" data-stop={it.stop}>
            <it.icon className="mt-0.5" />
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className={"font-semibold " + (it.disabled ? "text-muted-foreground" : "text-foreground")}>{it.word}</span>
              <span className="text-caption leading-snug whitespace-normal text-text-2">{it.line}</span>
            </span>
          </DropdownMenuItem>
        ))}
        {inEntra && items.length > 0 && <DropdownMenuSeparator />}
        {inEntra && (
          <DropdownMenuItem asChild className="gap-2.5 py-2 text-link focus:text-link" data-stop="entra">
            <a href={inEntra} target="_blank" rel="noreferrer" title={opensAccount(name)}>
              <CircleSlashIcon />
              {DISABLE_IN_ENTRA}
              <ExternalLinkIcon className="-ml-1 size-3 text-link" aria-hidden="true" />
            </a>
          </DropdownMenuItem>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
