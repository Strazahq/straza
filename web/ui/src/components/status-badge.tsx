import { Chip, Tag, type Tone } from "@/components/chip";

// TONE maps a server status word onto the reserved trust hues: green for
// running, amber for degraded and starting, red for a stopped or failed
// server, violet when the state cannot be known.
const TONE: Record<string, Tone> = {
  running: "ok",
  degraded: "warn",
  starting: "warn",
  stopped: "danger",
  failed: "danger",
  unknown: "unknown",
};

// StatusBadge renders one status word in its trust hue.
export function StatusBadge({ status, className }: { status?: string; className?: string }) {
  const word = status || "unknown";
  return (
    <Chip tone={TONE[word] || TONE.unknown} data-status={word} className={className}>
      {word}
    </Chip>
  );
}

// RoleChip names a role that holds an access row on a server.
export function RoleChip({ name }: { name: string }) {
  return <Tag className="mr-1">{name}</Tag>;
}

// NoRole is the dashed placeholder where a role chip would be.
export function NoRole({ text }: { text: string }) {
  return <span className="inline-block rounded-md border border-dashed border-border px-1.5 py-px text-caption italic text-muted-foreground">{text}</span>;
}
