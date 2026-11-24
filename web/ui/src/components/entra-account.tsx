import type * as React from "react";
import { AppWindowIcon, BotIcon, ExternalLinkIcon, type LucideIcon, UserRoundIcon, UsersIcon } from "lucide-react";
import { Tag } from "@/components/chip";
import { KIND_ICON } from "@/components/kind-icon";
import { ENTRA, ENTRA_GROUP_URL, OPEN_IN_ENTRA, REMOVE_IN_ENTRA, opensAccount, opensGroup } from "@/lib/entra-words";
import { cn } from "@/lib/utils";

const CloudIcon = KIND_ICON["from-entra"];

// ENTRA_TYPE_ICON is the icon of an object's type in Entra. A type the
// table does not know has no icon.
export const ENTRA_TYPE_ICON: Record<string, LucideIcon> = { user: UserRoundIcon, agentUser: BotIcon, agentIdentity: BotIcon, servicePrincipal: AppWindowIcon, group: UsersIcon };

// Opening an account in Entra never reaches the row or the menu item it
// sits in, so a click on the link only opens the new tab.
const stop = (e: React.SyntheticEvent) => e.stopPropagation();

// EntraTag is the tag Entra with the cloud icon of the From Entra chip. With
// url it is a link that opens the account of who in the Entra admin centre
// in a new tab, and without one it is the plain tag.
export function EntraTag({ url, who }: { url: string; who: string }) {
  if (!url) return <Tag icon={CloudIcon} data-entra-tag>{ENTRA}</Tag>;
  return (
    <a href={url} target="_blank" rel="noreferrer" title={opensAccount(who)} className="inline-flex max-w-full min-w-0 rounded-md align-middle" onClick={stop} onKeyDown={stop} data-entra-tag={url}>
      <Tag icon={CloudIcon} className="text-link hover:border-link">{ENTRA}<ExternalLinkIcon className="size-3 shrink-0 text-link" aria-hidden="true" /></Tag>
    </a>
  );
}

// OpenInEntra is the link that opens the account of who in the Entra admin
// centre in a new tab.
export function OpenInEntra({ url, who, className }: { url: string; who: string; className?: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      title={opensAccount(who)}
      className={cn("inline-flex items-center gap-1 text-caption whitespace-nowrap text-link underline-offset-4 hover:underline", className)}
      onClick={stop}
      onKeyDown={stop}
      data-open-in-entra={url}
    >
      {OPEN_IN_ENTRA}<ExternalLinkIcon className="size-3" aria-hidden="true" />
    </a>
  );
}

// RemoveInEntra is the link that opens a role's group in the Entra admin
// centre, where a member is removed, since the next read would give the
// role again.
export function RemoveInEntra({ groupID, group }: { groupID: string; group: string }) {
  return (
    <a
      href={ENTRA_GROUP_URL(groupID)}
      target="_blank"
      rel="noreferrer"
      title={opensGroup(group)}
      className="inline-flex items-center gap-1 text-caption whitespace-nowrap text-link hover:underline"
      onClick={stop}
      onKeyDown={stop}
      data-remove-in-entra
    >
      {REMOVE_IN_ENTRA}<ExternalLinkIcon className="size-3" aria-hidden="true" />
    </a>
  );
}
