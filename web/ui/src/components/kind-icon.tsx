import type { LucideIcon } from "lucide-react";
import {
  AppWindowIcon, BellRingIcon, BotIcon, BriefcaseBusinessIcon, Building2Icon, CircleSlashIcon, CloudIcon, CpuIcon,
  FileCheckIcon, FileKeyIcon, FilePenIcon, HardDriveIcon, HourglassIcon, IdCardIcon, KeyRoundIcon, LaptopIcon, LockIcon,
  MessagesSquareIcon, ScrollTextIcon, ServerCogIcon, ServerIcon, ShieldIcon, SmartphoneIcon, SquareTerminalIcon,
  TicketIcon, UserCheckIcon, UserRoundIcon, WrenchIcon,
} from "lucide-react";
import { KIND_WORD, type Kind } from "@/lib/chip-words";
import { cn } from "@/lib/utils";

// KIND_ICON is the one place that maps a kind to its icon, so a kind looks
// the same on every screen.
export const KIND_ICON: Record<Kind, LucideIcon> = {
  person: UserRoundIcon,
  agent: BotIcon,
  service: CpuIcon,
  "role-application": IdCardIcon,
  "role-business": BriefcaseBusinessIcon,
  "role-approver": UserCheckIcon,
  "role-straza": ShieldIcon,
  "role-server-admin": ServerCogIcon,
  server: ServerIcon,
  "local-server": LaptopIcon,
  tool: WrenchIcon,
  "policy-set": FileCheckIcon,
  session: SquareTerminalIcon,
  hold: HourglassIcon,
  ticket: TicketIcon,
  phone: SmartphoneIcon,
  browser: AppWindowIcon,
  channel: BellRingIcon,
  draft: FilePenIcon,
  "api-token": KeyRoundIcon,
  credential: KeyRoundIcon,
  "signing-key": FileKeyIcon,
  record: ScrollTextIcon,
  transcript: MessagesSquareIcon,
  "from-idm": Building2Icon,
  "from-straza": HardDriveIcon,
  "from-entra": CloudIcon,
};

// KindIcon is the bare icon of a kind, 16 px unless the class says other.
// Beside its own word it is hidden from assistive technology. named makes
// it the only carrier of the meaning, so it takes the kind's word as its
// accessible name and its hover text.
export function KindIcon({ kind, named, className }: { kind: Kind; named?: boolean; className?: string }) {
  const Icon = KIND_ICON[kind];
  if (!named) return <Icon className={cn("size-4", className)} aria-hidden="true" data-kind-icon={kind} />;
  return (
    <Icon className={cn("size-4", className)} role="img" aria-label={KIND_WORD[kind]} data-kind-icon={kind}>
      <title>{KIND_WORD[kind]}</title>
    </Icon>
  );
}

const TILE = { sm: "size-6", md: "size-7", lg: "size-10" } as const;
const TILE_ICON = { sm: "size-3.5", md: "size-4", lg: "size-[22px]" } as const;

// KindTile is a kind's icon on a square. Only an AI agent's tile has a hue,
// since which identity is a machine is the one distinction that needs it.
// The status overlay is hidden from assistive technology, because the
// status is a word beside the tile.
export function KindTile({ kind, size = "sm", status, named }: { kind: Kind; size?: "sm" | "md" | "lg"; status?: "locked" | "disabled"; named?: boolean }) {
  return (
    <span className={cn("relative inline-grid shrink-0 place-items-center rounded-md align-middle", TILE[size], kind === "agent" ? "bg-teal-bg text-teal" : "bg-secondary text-foreground")}>
      <KindIcon kind={kind} named={named} className={TILE_ICON[size]} />
      {status && (
        <span className="absolute -right-1 -bottom-1 grid size-3 place-items-center rounded-pill bg-background ring-2 ring-background" aria-hidden="true">
          {status === "locked" ? <LockIcon className="size-3 text-danger" /> : <CircleSlashIcon className="size-3 text-muted-foreground" />}
        </span>
      )}
    </span>
  );
}
