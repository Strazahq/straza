import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { DANGER_FILL } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RefusedError } from "@/components/error-state";
import { HelpTip } from "@/components/help-tip";
import type { AssignmentRow, DeviceRow } from "@/lib/api";
import { removeBothBody } from "@/lib/entra-words";
import { CANCEL } from "@/lib/role-words";
import { OWN_SESSION_WARNING, revokeLabel } from "@/lib/session-words";
import {
  DEVICE_HELP, DISABLE, DISABLE_HELP, IDM_OWNS, KEY_REVOKE_HELP, LOCK, LOCK_HELP, REASON_LABEL, REASON_MISSING, REMOVE_ROLE, REVOKE_DEVICE, REVOKE_KEY,
  UNLOCK, UNLOCK_HELP, deadRowBody, deviceRevokeBody, deviceTitle, disableBody, disableTitle, keyRevokeBody, keyTitle, lockBody, lockTitle, removeTitle, revokeAllBody, revokeAllTitle, revokeRoleBody, unlockBody, unlockTitle, windowWord,
} from "@/lib/user-words";

// Ask is the confirm a write of the page opens. Every one restates the
// object in its title.
export type Ask =
  | { kind: "lock" }
  | { kind: "unlock" }
  | { kind: "disable" }
  | { kind: "removeRole"; row: AssignmentRow; role: string; fromEntra?: boolean }
  | { kind: "sessions" }
  | { kind: "device"; device: DeviceRow; label: string }
  | { kind: "revokeKey" };

type Say = { title: string; body: string; help: string; label: string; red: boolean };

// words is the copy of one confirm: the title names the object, the body
// says what changes, the help carries the mechanics, and the button
// repeats the verb. n is the user's count of active sessions.
function words(ask: Ask, name: string, scim: boolean, n: number): Say {
  switch (ask.kind) {
    case "lock":
      return { title: lockTitle(name), body: lockBody(name), help: LOCK_HELP, label: LOCK, red: true };
    case "unlock":
      return { title: unlockTitle(name), body: unlockBody(name), help: UNLOCK_HELP, label: UNLOCK, red: false };
    case "disable":
      return { title: disableTitle(name), body: disableBody(name), help: DISABLE_HELP, label: DISABLE, red: true };
    case "removeRole": {
      const dead = windowWord(ask.row);
      const body = (dead ? deadRowBody(name, ask.role, dead) : ask.fromEntra ? removeBothBody(name, ask.role, ask.row.origin) : revokeRoleBody(name, ask.role)) + (scim ? " " + IDM_OWNS : "");
      return { title: removeTitle(ask.role, name), body, help: "", label: REMOVE_ROLE, red: true };
    }
    case "sessions":
      return { title: revokeAllTitle(name), body: revokeAllBody(n, name), help: "", label: revokeLabel(n), red: true };
    case "device":
      return { title: deviceTitle(ask.label), body: deviceRevokeBody(name, ask.label), help: DEVICE_HELP, label: REVOKE_DEVICE, red: true };
    default:
      return { title: keyTitle(name), body: keyRevokeBody(name), help: KEY_REVOKE_HELP, label: REVOKE_KEY, red: true };
  }
}

type Props = {
  ask: Ask | null;
  name: string;
  scim: boolean;
  busy: boolean;
  problem: { subject: string; text: string } | null;
  // sessions is the user's count of active sessions, which Revoke all
  // sessions ends, and own says the reader's own session is among them.
  sessions: number;
  own: boolean;
  onGo: (reason: string) => void;
  onClose: () => void;
  // onClosed runs once the dialog is gone, where the page puts the focus
  // back on what opened it.
  onClosed?: () => void;
};

// UserConfirm is the one dialog the page's writes ask in. Its primary stays
// clickable, so an empty reason is answered at the field, and a refusal
// shows inside it with the server's own sentence.
export function UserConfirm({ ask, name, scim, busy, problem, sessions, own, onGo, onClose, onClosed }: Props) {
  return (
    <AlertDialog open={!!ask} onOpenChange={(o) => { if (!o && !busy) onClose(); }}>
      <AlertDialogContent className="sm:max-w-[560px]" data-user-confirm={ask ? ask.kind : undefined} onCloseAutoFocus={(e) => { if (onClosed) { e.preventDefault(); onClosed(); } }}>
        {ask && <Body key={ask.kind} ask={ask} name={name} scim={scim} busy={busy} problem={problem} sessions={sessions} own={own} onGo={onGo} />}
      </AlertDialogContent>
    </AlertDialog>
  );
}

function Body({ ask, name, scim, busy, problem, sessions, own, onGo }: Omit<Props, "ask" | "onClose" | "onClosed"> & { ask: Ask }) {
  const [reason, setReason] = React.useState("");
  const [miss, setMiss] = React.useState(false);
  const field = React.useRef<HTMLInputElement>(null);
  const say = words(ask, name, scim, sessions);
  const go = () => {
    if (ask.kind === "lock" && !reason.trim()) { setMiss(true); field.current?.focus(); return; }
    onGo(reason.trim());
  };
  return (
    <>
      <AlertDialogHeader>
        <AlertDialogTitle>{say.title}</AlertDialogTitle>
        <AlertDialogDescription>
          {say.body}
          {ask.kind === "sessions" && own && " " + OWN_SESSION_WARNING}
          {say.help && <HelpTip label={say.label} text={say.help} className="ml-1" />}
        </AlertDialogDescription>
      </AlertDialogHeader>
      {ask.kind === "lock" && (
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="lock-reason">{REASON_LABEL}</Label>
          <Input id="lock-reason" ref={field} value={reason} autoFocus onChange={(e) => { setReason(e.target.value); setMiss(false); }} />
          {miss && <p className="text-caption text-danger" role="alert">{REASON_MISSING}</p>}
        </div>
      )}
      {problem && <RefusedError subject={problem.subject} message={problem.text} />}
      <AlertDialogFooter>
        <AlertDialogCancel disabled={busy}>{CANCEL}</AlertDialogCancel>
        <AlertDialogAction className={say.red ? DANGER_FILL : undefined} disabled={busy} onClick={(e) => { e.preventDefault(); go(); }}>
          {busy && <Loader2Icon className="animate-spin" />}{say.label}
        </AlertDialogAction>
      </AlertDialogFooter>
    </>
  );
}
