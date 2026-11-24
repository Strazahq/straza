import * as React from "react";
import { Loader2Icon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Chip } from "@/components/chip";
import { RefusedError } from "@/components/error-state";
import { MUTED } from "@/components/user-parts";
import { type ApiError, type KeyPosture, setKey } from "@/lib/api";
import { ROTATE_KEY } from "@/lib/keys-words";
import { notify } from "@/lib/notify";
import { CANCEL } from "@/lib/role-words";
import { refused } from "@/lib/say";
import { KEY_HINT, KEY_LABEL, KEY_MISSING, NO_KEY, NO_KEY_LINE, REGISTER_KEY, REGISTERED, REVOKE_KEY, keySetToast, since } from "@/lib/user-words";
import { absTime, dayWord } from "@/lib/words";

// shortPrint cuts a fingerprint to its first 15 characters, an ellipsis and
// its last 7, and keeps a short one whole.
export const shortPrint = (fp: string) => (fp.length > 22 ? fp.slice(0, 15) + "…" + fp.slice(-7) : fp);

type Props = {
  id: string;
  name: string;
  posture: KeyPosture;
  onRevoke: () => void;
  // onChanged reads the page again after a key was registered.
  onChanged: () => void;
};

// UserKey is the Assertion key fact of an AI agent: the key registered
// since a day with its fingerprint, Rotate key and Revoke key, or no key
// and Register key. The form to register or rotate opens in place.
export function UserKey({ id, name, posture, onRevoke, onChanged }: Props) {
  const [form, setForm] = React.useState<"" | "register" | "rotate">("");
  const [value, setValue] = React.useState("");
  const [miss, setMiss] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [problem, setProblem] = React.useState<string | null>(null);
  const field = React.useRef<HTMLInputElement>(null);
  const cancelled = React.useRef(false);
  const verb = form === "rotate" ? ROTATE_KEY : REGISTER_KEY;
  const open = (to: "register" | "rotate") => { setValue(""); setMiss(false); setProblem(null); setForm(to); };
  const cancel = () => { cancelled.current = true; setForm(""); setMiss(false); setProblem(null); };
  // opener is the ref of the button that opens the form. Cancel hides the
  // form and draws that button again, and the focus goes to it.
  const opener = (el: HTMLButtonElement | null) => {
    if (!el || !cancelled.current) return;
    cancelled.current = false;
    el.focus();
  };

  const submit = async () => {
    const key = value.trim();
    if (!key) { setMiss(true); field.current?.focus(); return; }
    setBusy(true);
    setProblem(null);
    try {
      await setKey(id, key);
      setForm("");
      setValue("");
      notify.ok(keySetToast(name));
      onChanged();
    } catch (e) {
      const err = e as ApiError;
      if (err.status !== 401) setProblem(refused(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-w-0 flex-1 flex-col gap-2" data-key-fact>
      {posture.registered ? (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Chip tone="plain" data-key="registered">{REGISTERED}</Chip>
            <span title={absTime(posture.created)}>{since(dayWord(posture.created))}</span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-caption break-all" title={posture.fingerprint}>{shortPrint(posture.fingerprint || "")}</span>
            {form === "" && <Button ref={opener} variant="outline" size="sm" onClick={() => open("rotate")}>{ROTATE_KEY}</Button>}
            <Button variant="outline" size="sm" onClick={onRevoke}>{REVOKE_KEY}</Button>
          </div>
        </>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Chip tone="warn" data-key="no key">{NO_KEY}</Chip>
          <span>{NO_KEY_LINE}</span>
          {form === "" && <Button ref={opener} size="sm" onClick={() => open("register")}>{REGISTER_KEY}</Button>}
        </div>
      )}
      {form !== "" && (
        <div className="flex flex-col gap-1.5 rounded-md border border-border px-3 py-3" data-key-form={form}>
          <Label htmlFor="user-key">{KEY_LABEL}</Label>
          <Input id="user-key" ref={field} value={value} className="font-mono" autoFocus onChange={(e) => { setValue(e.target.value); setMiss(false); }} />
          <p className={MUTED + " m-0"}>{KEY_HINT}</p>
          {miss && <p className="m-0 text-caption text-danger" role="alert">{KEY_MISSING}</p>}
          {problem && <RefusedError subject={verb} message={problem} />}
          <div className="flex items-center justify-end gap-2">
            <Button variant="outline" size="sm" onClick={cancel}>{CANCEL}</Button>
            <Button size="sm" disabled={busy} onClick={() => void submit()}>{busy && <Loader2Icon className="animate-spin" />}{verb}</Button>
          </div>
        </div>
      )}
    </div>
  );
}
