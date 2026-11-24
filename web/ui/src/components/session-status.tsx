import { Chip, StatusChip, isStatus } from "@/components/chip";
import { statusTone } from "@/lib/session-words";

// SessionStatus is a session's status: the status chip for the words the
// console knows, and a plain chip for any other the server sends.
export function SessionStatus({ status }: { status: string }) {
  return isStatus(status) ? <StatusChip status={status} /> : <Chip tone={statusTone(status)}>{status}</Chip>;
}
