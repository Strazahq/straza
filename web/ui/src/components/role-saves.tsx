import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { type DraftSaveOptions, type SaveNote, SaveNoteLine, useDraftSave, useWorkingDraft } from "@/components/use-draft-save";
import { HINT } from "@/components/wizard/parts";
import { type ApiError, exportRole } from "@/lib/api";
import type { SaveItem } from "@/lib/draft-save";
import { CANCEL, KEEP_EDITING, liveDocument } from "@/lib/role-words";
import { CLOSE_BODY, CLOSE_DROP, CLOSE_TITLE, NOTHING_SAVED, SAVE_DRAFT, SAVE_PUBLISH, workingHolds } from "@/lib/save-words";
import { readFailed } from "@/lib/say";
import { cn } from "@/lib/utils";

// Built is what a role editor's build answers: the items to send, a note
// that says why nothing is sent, null when the editor already says why at
// the field, or ask when a question comes before the save.
export type Built = SaveItem[] | SaveNote | null | "ask";

// liveExport reads the role's live export, the one document a Role put
// starts from, or answers the note of a read that failed. A 401 is the
// session module's, so it answers null.
export async function liveExport(role: { id: string; name: string }): Promise<string | SaveNote | null> {
  try {
    return await exportRole(role.id);
  } catch (e) {
    const err = e as ApiError;
    if (err.status === 401) return null;
    return { tone: err.unreachable ? "unreachable" : "refused", lines: [NOTHING_SAVED, readFailed(liveDocument(role.name), err)] };
  }
}

type Props = {
  // role is the role the editor changes, as the toast, the dialog title and
  // the working-draft line name it.
  role: string;
  toast?: string;
  // creates is what a create door hands the save flow, so a check that
  // answers the role as live refuses the save.
  creates?: DraftSaveOptions["creates"];
  // build reads what the save starts from and answers what to send.
  // confirmed is true once the question of ask was answered yes.
  build: (confirmed: boolean) => Promise<Built>;
  // ask draws the question a build answered ask for, told whether the
  // pressed button publishes.
  ask?: (open: boolean, confirm: () => void, cancel: () => void, publish: boolean) => React.ReactNode;
  onPublished: () => void;
  onCancel: () => void;
  // changed says the editor holds a change a save would store, so Cancel
  // asks before it drops one that no draft holds. edits is the editor's
  // state as one string: a new value is an edit no draft holds yet.
  changed?: boolean;
  edits?: string;
  onBusy?: (busy: boolean) => void;
};

const DESTRUCTIVE = "bg-danger text-white hover:bg-danger/90";

// RoleSaves is the foot of a role editor: Cancel, Save draft and Save and
// publish through the editors' one save flow,
// the note beside them, and the line that says the person's working draft
// already changes the role. It mounts with its editor, so every opening
// starts with no note.
export function RoleSaves({ role, toast, creates, build, ask, onPublished, onCancel, changed, edits, onBusy }: Props) {
  const held = useWorkingDraft("Role/" + role);
  const save = useDraftSave({ name: role, toast, creates, onPublished });
  // reading is the button whose build is still reading what the save
  // starts from.
  const [reading, setReading] = React.useState<"draft" | "publish" | null>(null);
  const [note, setNote] = React.useState<SaveNote | null>(null);
  // asking holds whether the pressed button publishes while the question
  // of ask is open.
  const [asking, setAsking] = React.useState<boolean | null>(null);
  const [closing, setClosing] = React.useState(false);
  const busy = reading !== null || save.busy !== null;
  React.useEffect(() => { onBusy?.(busy); }, [busy]); // eslint-disable-line react-hooks/exhaustive-deps
  // An edit after a save that left the change in a draft drops the note
  // that names the draft, so Cancel asks again.
  React.useEffect(() => { save.edited(); }, [edits]); // eslint-disable-line react-hooks/exhaustive-deps

  const send = async (publish: boolean, confirmed = false) => {
    if (busy) return;
    setNote(null);
    setReading(publish ? "publish" : "draft");
    let built: Built = null;
    try {
      built = await build(confirmed);
    } finally {
      setReading(null);
    }
    if (built === "ask") { setAsking(publish); return; }
    if (!built) return;
    if (!Array.isArray(built)) { setNote(built); return; }
    await (publish ? save.saveAndPublish(built) : save.saveDraft(built));
  };

  const shown = note || save.note;
  const spin = <Loader2Icon className="animate-spin" />;
  // A note that names a draft follows a save that stored the change there,
  // so Cancel then drops nothing.
  const cancel = () => (changed && !shown?.draft ? setClosing(true) : onCancel());
  return (
    <div className="flex flex-col gap-2" data-role-saves>
      {shown && <SaveNoteLine note={shown} />}
      {held && <p className={cn(HINT, "m-0 max-w-[75ch]")} data-working-holds>{workingHolds(held, role)}</p>}
      <div className="flex items-center justify-end gap-2">
        <Button variant="outline" onClick={cancel} disabled={busy}>{CANCEL}</Button>
        <Button variant="outline" onClick={() => void send(false)} disabled={busy} data-role-draft>
          {(reading === "draft" || save.busy === "draft") && spin}
          {SAVE_DRAFT}
        </Button>
        <Button onClick={() => void send(true)} disabled={busy} data-role-publish>
          {(reading === "publish" || save.busy === "publish") && spin}
          {SAVE_PUBLISH}
        </Button>
      </div>
      {ask && ask(asking !== null, () => { const publish = !!asking; setAsking(null); void send(publish, true); }, () => setAsking(null), !!asking)}
      <AlertDialog open={closing} onOpenChange={setClosing}>
        <AlertDialogContent className="sm:max-w-[560px]" data-close-ask>
          <AlertDialogHeader>
            <AlertDialogTitle>{CLOSE_TITLE}</AlertDialogTitle>
            <AlertDialogDescription>{CLOSE_BODY}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{KEEP_EDITING}</AlertDialogCancel>
            <AlertDialogAction className={DESTRUCTIVE} onClick={onCancel}>{CLOSE_DROP}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      {save.dialog}
    </div>
  );
}
