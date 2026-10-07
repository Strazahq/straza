import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// TONE maps a server status word onto the reserved trust hues: green for
// running, amber for degraded and starting, red for a stopped or failed
// server, violet when the state cannot be known.
const TONE: Record<string, string> = {
  running: "bg-ok-bg text-ok border-ok/40",
  degraded: "bg-warn-bg text-warn border-warn/40",
  starting: "bg-warn-bg text-warn border-warn/40",
  stopped: "bg-danger-bg text-danger border-danger/40",
  failed: "bg-danger-bg text-danger border-danger/40",
  unknown: "bg-unknown-bg text-unknown border-unknown/40",
};

// StatusBadge renders one status word in its trust hue.
export function StatusBadge({ status, className }: { status?: string; className?: string }) {
  const word = status || "unknown";
  return (
    <Badge variant="outline" data-status={word} className={cn("rounded-md px-2 font-mono text-[13px] font-normal", TONE[word] || TONE.unknown, className)}>
      {word}
    </Badge>
  );
}

// RoleChip names a role that holds an access row on a server.
export function RoleChip({ name }: { name: string }) {
  return (
    <span className="mr-1 inline-block rounded-md border border-border bg-background px-1.5 py-px font-mono text-[13px] text-text-2">
      {name}
    </span>
  );
}

// NoRole is the dashed placeholder where a role chip would be.
export function NoRole({ text }: { text: string }) {
  return <span className="inline-block rounded-md border border-dashed border-border px-1.5 py-px text-[13px] italic text-muted-foreground">{text}</span>;
}
